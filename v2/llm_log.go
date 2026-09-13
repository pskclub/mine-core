package core

import (
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// llmLogLevel is how much of the model traffic is written, mirroring the levels
// the SQL and HTTP loggers use.
type llmLogLevel uint8

const (
	llmLogSilent llmLogLevel = iota
	llmLogError
	llmLogWarn
	llmLogAll
)

// defaultSlowGeneration is when a call becomes worth a warning on its own.
//
// It is measured in tens of seconds rather than the hundreds of milliseconds an
// HTTP call is judged by: a reasoning model on a hard prompt genuinely takes
// that long, and warning at HTTP timescales would mark every normal call as
// slow — which is the same as marking none of them.
const defaultSlowGeneration = 30 * time.Second

// llmLogLevelFrom decides how much is logged: AI_LOG_LEVEL when set, otherwise
// it follows LOG_LEVEL.
//
// The separate key exists for the same reason DB_LOG_LEVEL and HTTP_LOG_LEVEL
// do: reading one generation should not require turning the whole application
// up to debug, and a service happy at info may still want every call while a
// prompt is being written.
func llmLogLevelFrom(env IENV) llmLogLevel {
	if env == nil {
		return llmLogWarn
	}
	if level, ok := parseLLMLogLevel(env.Config().AILogLevel); ok {
		return level
	}
	switch parseLevel(env.Config().LogLevel) {
	case slog.LevelDebug:
		return llmLogAll
	case slog.LevelError:
		return llmLogError
	default:
		return llmLogWarn
	}
}

// parseLLMLogLevel reads AI_LOG_LEVEL. "off"/"false" are accepted alongside
// "silent" because the setting is reached for as a switch.
func parseLLMLogLevel(s string) (llmLogLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "silent", "off", "none", "false":
		return llmLogSilent, true
	case "error":
		return llmLogError, true
	case "warn", "warning":
		return llmLogWarn, true
	case "info", "debug", "all", "true":
		return llmLogAll, true
	default:
		return 0, false
	}
}

// llmCallMessage is the line's headline: "generate anthropic/claude-opus-5 stop 3.2s".
//
// The message has to say what happened on its own — a JSON pipeline shows it as
// the headline, and Sentry Logs shows nothing else until the line is opened.
// "llm call" answers none of the questions being scanned for: which model, did
// it finish, how long did it take. The same values stay in the fields, which is
// what you filter on.
func llmCallMessage(op, provider, model string, finish LLMFinishReason, took time.Duration) string {
	var b strings.Builder
	b.WriteString(op)
	b.WriteString(" ")
	if provider != "" {
		b.WriteString(provider)
		b.WriteString("/")
	}
	if model == "" {
		model = "?"
	}
	b.WriteString(model)
	if finish != "" {
		b.WriteString(" ")
		b.WriteString(string(finish))
	}
	b.WriteString(" ")
	b.WriteString(took.Round(time.Millisecond).String())
	return b.String()
}

// llmPromptMaxChars bounds what AI_LOG_PROMPT writes per field. A prompt with a
// document pasted into it would otherwise put a megabyte in the log store on
// every call.
const llmPromptMaxChars = 2000

// promptFields renders the prompt for the log. It is only ever called when
// AI_LOG_PROMPT is on, which is off by default and documented as a development
// setting: a prompt carries whatever the user typed, and a log store is the
// easiest place for that to end up somewhere nobody meant it to be.
func (r LLMRequest) promptFields() []any {
	out := make([]any, 0, 4)
	if r.System != "" {
		out = append(out, "system", truncateForLog(r.System))
	}
	for i, m := range r.Messages {
		key := "msg." + strconv.Itoa(i) + "." + string(m.Role)
		text := m.Text
		// An attachment's bytes are never written — the point of the setting is
		// to read the wording, and a base64 image would bury it.
		if len(m.Parts) > 0 {
			text += " [+" + strconv.Itoa(len(m.Parts)) + " attachment(s)]"
		}
		out = append(out, key, truncateForLog(text))
	}
	return out
}

// completionFields renders what came back for the log. Only ever called when
// AI_LOG_COMPLETION is on — a reply repeats whatever it was given and a source
// list says what the user was reading, so both follow the same rule as the
// prompt.
func (r LLMResponse) completionFields() []any {
	out := make([]any, 0, 4)
	if r.Text != "" {
		out = append(out, "completion", truncateForLog(r.Text))
	}
	for i, s := range r.Sources {
		// Grounding citations are the one part of a reply worth logging in
		// full: they are what the answer claims to be based on, and an answer
		// nobody can trace back is the thing this whole feature exists to
		// avoid.
		out = append(out, "source."+strconv.Itoa(i), s.URL)
	}
	return out
}

func truncateForLog(s string) string {
	if len(s) <= llmPromptMaxChars {
		return s
	}
	return s[:llmPromptMaxChars] + "…(" + strconv.Itoa(len(s)) + " chars)"
}
