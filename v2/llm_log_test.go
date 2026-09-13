package core

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// llmLogApp builds an App whose logger writes somewhere a test can read.
func llmLogApp(t *testing.T, kv map[string]string, model ILLM) (*App, *bytes.Buffer) {
	t.Helper()
	if kv == nil {
		kv = map[string]string{}
	}
	kv["ENV"] = "test"
	kv["LOG_LEVEL"] = "debug"

	env := mustEnv(t, kv)
	buf := &bytes.Buffer{}
	app, err := NewApp(env, WithLogger(NewLoggerTo(buf, env)), WithLLM(model))
	require.NoError(t, err)
	return app, buf
}

// logLines parses what the JSON handler wrote.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), "log must be valid JSON: %s", line)
		out = append(out, m)
	}
	return out
}

func findLine(lines []map[string]any, contains string) map[string]any {
	for _, l := range lines {
		if msg, _ := l["msg"].(string); strings.Contains(msg, contains) {
			return l
		}
	}
	return nil
}

func TestLLMLog_failureIsNotBuriedAtDebug(t *testing.T) {
	// The bug this replaces: every call logged at debug, so on a production
	// service at info a provider outage produced no line at all.
	m := NewMemoryLLM()
	QueueLLMReply(m, MemoryLLMReply{Err: New(503, "LLM_PROVIDER_ERROR", "provider is down")})

	app, buf := llmLogApp(t, map[string]string{"AI_LOG_LEVEL": "error"}, m)
	_, err := LLM(app.NewContext(context.Background(), ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})
	require.Error(t, err)

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line, "a provider failure has to leave a line even at AI_LOG_LEVEL=error")
	assert.Equal(t, "ERROR", line["level"])
	assert.Equal(t, "LLM_PROVIDER_ERROR", line["error.code"],
		"the code is what a dashboard filters on; without it the line says a call happened, not why it failed")
}

func TestLLMLog_rejectedRequestWarnsRatherThanErrors(t *testing.T) {
	// A 4xx is an answer — a bad key, a model that cannot see — and the service
	// that asked decides what it means. It must be visible without paging anyone.
	m := NewMemoryLLM()
	QueueLLMReply(m, MemoryLLMReply{Err: New(401, "LLM_UNAUTHORIZED", "bad key")})

	app, buf := llmLogApp(t, nil, m)
	_, err := LLM(app.NewContext(context.Background(), ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})
	require.Error(t, err)

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	assert.Equal(t, "WARN", line["level"])
	assert.Equal(t, "LLM_UNAUTHORIZED", line["error.code"])
}

func TestLLMLog_successStaysAtDebug(t *testing.T) {
	app, buf := llmLogApp(t, nil, NewMemoryLLM("fine"))
	_, err := LLM(app.NewContext(context.Background(), ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})
	require.NoError(t, err)

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	assert.Equal(t, "DEBUG", line["level"],
		"a call that worked is only interesting while something is being debugged")
}

func TestLLMLog_headlineSaysWhatHappened(t *testing.T) {
	// A JSON pipeline shows msg as the headline, and Sentry Logs shows nothing
	// else until the line is opened. "llm call" answers none of the questions
	// being scanned for.
	app, buf := llmLogApp(t, nil, NewMemoryLLM("fine"))
	_, err := LLM(app.NewContext(context.Background(), ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})
	require.NoError(t, err)

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	msg, _ := line["msg"].(string)
	assert.Contains(t, msg, "memory/memory", "which model ran")
	assert.Contains(t, msg, "stop", "whether it finished")
}

func TestLLMLog_silentWritesNothing(t *testing.T) {
	m := NewMemoryLLM()
	QueueLLMReply(m, MemoryLLMReply{Err: New(503, "LLM_PROVIDER_ERROR", "down")})

	app, buf := llmLogApp(t, map[string]string{"AI_LOG_LEVEL": "silent"}, m)
	_, _ = LLM(app.NewContext(context.Background(), ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})

	assert.Nil(t, findLine(logLines(t, buf), "generate"),
		"silent means silent — a service that logs model traffic elsewhere must be able to turn this off")
}

func TestLLMLog_promptIsNotWrittenByDefault(t *testing.T) {
	// A prompt carries whatever the user typed. The default has to be off, or a
	// log store becomes the place that data lives.
	secret := "my national id is 1234567890123"
	app, buf := llmLogApp(t, nil, NewMemoryLLM("ok"))

	_, err := LLM(app.NewContext(context.Background(), ModeTest)).Generate(LLMRequest{
		System:   "you are a helpful assistant",
		Messages: []LLMMessage{LLMUser(secret)},
	})
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), secret)
	assert.NotContains(t, buf.String(), "helpful assistant")

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	assert.NotZero(t, line["prompt_chars"], "the length still has to be there to explain a cost")
}

func TestLLMLog_promptIsWrittenWhenAsked(t *testing.T) {
	app, buf := llmLogApp(t, map[string]string{"AI_LOG_PROMPT": "true"}, NewMemoryLLM("ok"))

	_, err := LLM(app.NewContext(context.Background(), ModeTest)).Generate(LLMRequest{
		System:   "you are a helpful assistant",
		Messages: []LLMMessage{LLMUser("what is 2+2")},
	})
	require.NoError(t, err)

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	assert.Equal(t, "you are a helpful assistant", line["system"])
	assert.Equal(t, "what is 2+2", line["msg.0.user"])
}

func TestLLMLog_promptIsTruncated(t *testing.T) {
	// A prompt with a document pasted into it would otherwise put a megabyte in
	// the log store on every call.
	long := strings.Repeat("x", llmPromptMaxChars*2)
	app, buf := llmLogApp(t, map[string]string{"AI_LOG_PROMPT": "true"}, NewMemoryLLM("ok"))

	_, err := LLM(app.NewContext(context.Background(), ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser(long)}})
	require.NoError(t, err)

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	got, _ := line["msg.0.user"].(string)
	assert.Less(t, len(got), len(long))
	assert.Contains(t, got, "chars)", "the line has to say it was cut, and by how much")
}

func TestLLMLog_attachmentBytesAreNeverWritten(t *testing.T) {
	// Even with the prompt on: base64 image data would bury the wording the
	// setting exists to show.
	app, buf := llmLogApp(t, map[string]string{"AI_LOG_PROMPT": "true"}, NewMemoryLLM("ok"))
	png := bytes.Repeat([]byte{0xAB}, 64)

	_, err := LLM(app.NewContext(context.Background(), ModeTest)).Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("read this").With(LLMImage(png, "image/png"))},
	})
	require.NoError(t, err)

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	assert.Contains(t, line["msg.0.user"], "[+1 attachment(s)]")
	assert.EqualValues(t, 64, line["attachment_bytes"])
	assert.NotContains(t, buf.String(), "qq", "no base64 payload in the log")
}

func TestLLMLog_toolCallsLeaveAnAuditTrail(t *testing.T) {
	// The gap this closes: an agent loop could change data and leave no record
	// unless the service happened to write an Approve that logs. The service
	// with no approval policy is the one whose trail matters most.
	var ran []string
	m := NewMemoryLLM()
	QueueLLMReply(m,
		MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("refund_order", `{"id":"A1"}`)}},
		MemoryLLMReply{Text: "done"},
	)

	app, buf := llmLogApp(t, nil, m)
	_, err := LLM(app.NewContext(context.Background(), ModeTest)).Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("refund A1")},
		Tools:    []LLMTool{echoTool("refund_order", &ran)},
		MaxSteps: 5,
	})
	require.NoError(t, err)

	line := findLine(logLines(t, buf), "llm tool ran refund_order")
	require.NotNil(t, line, "every call the loop made has to leave a line")
	assert.Equal(t, "ran", line["outcome"])
	assert.Equal(t, "refund_order", line["tool"])
}

func TestLLMLog_deniedToolWarnsEvenAtWarnLevel(t *testing.T) {
	// A denial is the security control working. It must be visible without
	// anyone turning the level up first.
	var ran []string
	m := NewMemoryLLM()
	QueueLLMReply(m,
		MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("refund_order", `{"id":"A1"}`)}},
		MemoryLLMReply{Text: "cannot"},
	)

	app, buf := llmLogApp(t, map[string]string{"AI_LOG_LEVEL": "warn"}, m)
	_, err := LLM(app.NewContext(context.Background(), ModeTest)).Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("refund A1")},
		Tools:    []LLMTool{echoTool("refund_order", &ran)},
		MaxSteps: 5,
		Approve: func(context.Context, LLMToolCall) IError {
			return New(403, "NEEDS_HUMAN", "operator approval required")
		},
	})
	require.NoError(t, err)
	require.Empty(t, ran)

	line := findLine(logLines(t, buf), "llm tool denied refund_order")
	require.NotNil(t, line)
	assert.Equal(t, "WARN", line["level"])
	assert.Equal(t, "denied", line["outcome"])
}

func TestLLMLog_toolArgumentsFollowThePromptRule(t *testing.T) {
	// Tool arguments are the model's words about what it wants done, and carry
	// user data just as a prompt does.
	var ran []string
	m := NewMemoryLLM()
	QueueLLMReply(m,
		MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("lookup", `{"id":"A1"}`)}},
		MemoryLLMReply{Text: "done"},
	)

	app, buf := llmLogApp(t, nil, m)
	_, err := LLM(app.NewContext(context.Background(), ModeTest)).Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("go")},
		Tools:    []LLMTool{echoTool("lookup", &ran)},
		MaxSteps: 5,
	})
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), `"A1"`, "arguments stay out unless AI_LOG_PROMPT is on")
}

func TestLLMLog_completionIsNotWrittenByDefault(t *testing.T) {
	// A reply repeats whatever it was given, so it carries the same data the
	// prompt does and defaults off for the same reason.
	answer := "the customer's card ends 4242"
	app, buf := llmLogApp(t, nil, NewMemoryLLM(answer))

	_, err := LLM(app.NewContext(context.Background(), ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser("summarise")}})
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), "4242")
	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	assert.NotZero(t, line["output_tokens"], "what it cost still has to be there")
}

func TestLLMLog_completionIsWrittenWhenAsked(t *testing.T) {
	app, buf := llmLogApp(t, map[string]string{"AI_LOG_COMPLETION": "true"}, NewMemoryLLM("42"))

	_, err := LLM(app.NewContext(context.Background(), ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser("what is 6*7")}})
	require.NoError(t, err)

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	assert.Equal(t, "42", line["completion"])
	assert.NotContains(t, buf.String(), "what is 6*7",
		"the two settings expose different things — the completion must not drag the prompt in with it")
}

func TestLLMLog_completionIsTruncated(t *testing.T) {
	long := strings.Repeat("y", llmPromptMaxChars*2)
	app, buf := llmLogApp(t, map[string]string{"AI_LOG_COMPLETION": "true"}, NewMemoryLLM(long))

	_, err := LLM(app.NewContext(context.Background(), ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser("go")}})
	require.NoError(t, err)

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	got, _ := line["completion"].(string)
	assert.Less(t, len(got), len(long))
	assert.Contains(t, got, "chars)")
}

func TestLLMLog_toolOutputFollowsTheCompletionRule(t *testing.T) {
	// What a tool returns is usually a row out of the database, so it follows
	// the reply's rule rather than the prompt's — and the two are separate keys
	// precisely so turning on prompt logging does not start writing query
	// results.
	var ran []string
	script := func() ILLM {
		m := NewMemoryLLM()
		QueueLLMReply(m,
			MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("lookup", `{"id":"A1"}`)}},
			MemoryLLMReply{Text: "done"},
		)
		return m
	}
	call := func(app *App) {
		_, err := LLM(app.NewContext(context.Background(), ModeTest)).Generate(LLMRequest{
			Messages: []LLMMessage{LLMUser("go")},
			Tools:    []LLMTool{echoTool("lookup", &ran)},
			MaxSteps: 5,
		})
		require.NoError(t, err)
	}

	app, buf := llmLogApp(t, nil, script())
	call(app)
	assert.NotContains(t, buf.String(), "ok from lookup", "off by default")

	app, buf = llmLogApp(t, map[string]string{"AI_LOG_PROMPT": "true"}, script())
	call(app)
	assert.NotContains(t, buf.String(), "ok from lookup",
		"AI_LOG_PROMPT is the input side only; a tool's result is not the user's words")

	app, buf = llmLogApp(t, map[string]string{"AI_LOG_COMPLETION": "true"}, script())
	call(app)
	line := findLine(logLines(t, buf), "llm tool ran lookup")
	require.NotNil(t, line)
	assert.Equal(t, "ok from lookup", line["output"],
		"the trail has to say what the model was told, not only that something was looked up")
}

func TestLLMLog_deniedToolRecordsWhatWentBackToTheModel(t *testing.T) {
	// A denial's text is what the model reads and explains to the user, so it
	// is the part of the trail that explains the answer.
	var ran []string
	m := NewMemoryLLM()
	QueueLLMReply(m,
		MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("refund_order", `{"id":"A1"}`)}},
		MemoryLLMReply{Text: "cannot"},
	)

	app, buf := llmLogApp(t, map[string]string{"AI_LOG_COMPLETION": "true"}, m)
	resp, err := LLM(app.NewContext(context.Background(), ModeTest)).Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("refund A1")},
		Tools:    []LLMTool{echoTool("refund_order", &ran)},
		MaxSteps: 5,
		Approve: func(context.Context, LLMToolCall) IError {
			return New(403, "NEEDS_HUMAN", "operator approval required")
		},
	})
	require.NoError(t, err)

	require.Len(t, resp.ToolCalls, 1)
	assert.Contains(t, resp.ToolCalls[0].Output, "operator approval required",
		"the response carries the trail even when nothing is logged")

	line := findLine(logLines(t, buf), "llm tool denied refund_order")
	require.NotNil(t, line)
	assert.Contains(t, line["output"], "operator approval required")
}

func TestLLMLog_groundedGenerationSaysItSearched(t *testing.T) {
	// A tool the provider runs never reaches RunTool, so without a count here a
	// generation that searched the web is indistinguishable in the log from one
	// that answered from memory.
	m := NewMemoryLLM()
	QueueLLMReply(m, MemoryLLMReply{
		Text:    "Go 1.26.5 is the latest release.",
		Sources: []LLMSource{{Type: "url", URL: "https://go.dev/doc/devel/release", Title: "Releases"}},
	})

	app, buf := llmLogApp(t, nil, m)
	_, err := LLM(app.NewContext(context.Background(), ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser("latest go")}})
	require.NoError(t, err)

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	assert.EqualValues(t, 1, line["sources"], "the count is not content and is always worth having")
	assert.NotContains(t, buf.String(), "go.dev", "the URLs themselves wait for AI_LOG_COMPLETION")
}

func TestLLMLog_completionWritesTheSourcesItCited(t *testing.T) {
	m := NewMemoryLLM()
	QueueLLMReply(m, MemoryLLMReply{
		Text:    "Go 1.26.5 is the latest release.",
		Sources: []LLMSource{{Type: "url", URL: "https://go.dev/doc/devel/release"}},
	})

	app, buf := llmLogApp(t, map[string]string{"AI_LOG_COMPLETION": "true"}, m)
	_, err := LLM(app.NewContext(context.Background(), ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser("latest go")}})
	require.NoError(t, err)

	line := findLine(logLines(t, buf), "generate")
	require.NotNil(t, line)
	assert.Equal(t, "https://go.dev/doc/devel/release", line["source.0"],
		"an answer nobody can trace back to what it read is what grounding exists to prevent")
}

func TestLLMLog_levelFallsBackToLogLevel(t *testing.T) {
	assert.Equal(t, llmLogAll, llmLogLevelFrom(mustEnv(t, map[string]string{"LOG_LEVEL": "debug"})))
	assert.Equal(t, llmLogWarn, llmLogLevelFrom(mustEnv(t, map[string]string{"LOG_LEVEL": "info"})))
	assert.Equal(t, llmLogError, llmLogLevelFrom(mustEnv(t, map[string]string{"LOG_LEVEL": "error"})))
	assert.Equal(t, llmLogWarn, llmLogLevelFrom(nil))

	// The explicit key wins, which is the point of having it.
	assert.Equal(t, llmLogSilent, llmLogLevelFrom(mustEnv(t, map[string]string{
		"LOG_LEVEL": "debug", "AI_LOG_LEVEL": "silent",
	})))
}

func TestLLMLog_levelAliases(t *testing.T) {
	for _, s := range []string{"silent", "off", "none", "false"} {
		got, ok := parseLLMLogLevel(s)
		require.True(t, ok, s)
		assert.Equal(t, llmLogSilent, got, s)
	}
	for _, s := range []string{"info", "debug", "all", "true"} {
		got, ok := parseLLMLogLevel(s)
		require.True(t, ok, s)
		assert.Equal(t, llmLogAll, got, s)
	}
	_, ok := parseLLMLogLevel("loud")
	assert.False(t, ok, "an unknown value must fall through to LOG_LEVEL, not be guessed at")
}
