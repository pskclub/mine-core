package core

import (
	"context"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
)

// ANSI escapes. Kept literal and few: a log line should be scannable, not a
// paint chart.
const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiDim     = "\x1b[2m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiMagenta = "\x1b[35m"
	ansiCyan    = "\x1b[36m"
)

// prettyHandler writes human-readable, optionally coloured lines:
//
//	21:15:15.481 INFO  request  method=GET path=/users status=200 latency_ms=4
//
// The eye finds the level and the message first; timestamps and attribute keys
// are dimmed so they stay available without competing. Errors are red wherever
// they appear, so a failure is visible while scrolling.
//
// Colour is decided once, at construction — see wantColor.
type prettyHandler struct {
	w      io.Writer
	mu     *sync.Mutex
	level  slog.Leveler
	color  bool
	source bool

	// attrs is pre-rendered output from WithAttrs, so pinned attributes cost
	// nothing per record.
	attrs string
	group string
}

var _ slog.Handler = (*prettyHandler)(nil)

func newPrettyHandler(w io.Writer, level slog.Leveler, color, source bool) *prettyHandler {
	if color {
		// On Windows this translates ANSI for consoles that do not handle it
		// natively; elsewhere it passes straight through.
		if f, ok := w.(*os.File); ok {
			w = colorable.NewColorable(f)
		}
	}
	return &prettyHandler{w: w, mu: &sync.Mutex{}, level: level, color: color, source: source}
}

func (h *prettyHandler) Enabled(_ context.Context, l slog.Level) bool {
	min := slog.LevelInfo
	if h.level != nil {
		min = h.level.Level()
	}
	return l >= min
}

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	b.WriteString(h.paint(r.Time.Format("15:04:05.000"), ansiDim))
	b.WriteByte(' ')
	b.WriteString(h.paint(levelLabel(r.Level), levelColor(r.Level)))
	b.WriteByte(' ')
	b.WriteString(h.paint(r.Message, ansiBold))

	// The record's own attributes come before the pinned ones, so the payload of
	// the line (the statement, the status) starts at a predictable column and the
	// correlation ids you only read when tracing trail at the end.
	r.Attrs(func(a slog.Attr) bool {
		h.appendAttr(&b, h.group, a)
		return true
	})
	b.WriteString(h.attrs)
	// the call site closes the line, dimmed and unlabelled: it reads as the
	// footnote it is, and never pushes the message off the left of the eye
	if src := h.sourceOf(r); src != "" {
		b.WriteByte(' ')
		b.WriteString(h.paint(src, ansiDim))
	}
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

// sourceOf resolves the recorded program counter to "dir/file.go:line", or ""
// when the line carries none.
func (h *prettyHandler) sourceOf(r slog.Record) string {
	if !h.source || r.PC == 0 {
		return ""
	}
	frame, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
	if frame.File == "" {
		return ""
	}
	return shortSource(frame.File, frame.Line)
}

func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	c := *h // the mutex is a pointer, so clones keep writes serialised
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range attrs {
		h.appendAttr(&b, h.group, a)
	}
	c.attrs = b.String()
	return &c
}

func (h *prettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.group = h.group + name + "."
	return &c
}

// appendAttr renders one attribute, flattening groups into dotted keys.
func (h *prettyHandler) appendAttr(b *strings.Builder, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}

	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		if len(group) == 0 {
			return
		}
		inner := prefix
		if a.Key != "" {
			inner = prefix + a.Key + "."
		}
		for _, ga := range group {
			h.appendAttr(b, inner, ga)
		}
		return
	}

	b.WriteByte(' ')
	b.WriteString(h.paint(prefix+a.Key+"=", ansiDim))
	b.WriteString(h.paintValue(a.Key, a.Value))
}

// rawValueKeys hold a payload rather than a datum — a SQL statement, a stack.
// Quoting those buries the content under escapes: `\"users\".\"deleted_at\"`
// instead of `"users"."deleted_at"`, which is most of a query once the
// identifiers are quoted. Pretty output exists for a person to read, so the
// payload wins over key=value parseability here; JSON still encodes properly for
// machines.
var rawValueKeys = map[string]bool{
	"sql":   true,
	"query": true,
	"stack": true,
}

// sqlValueKeys are the raw keys whose value is a statement, and so worth
// syntax-highlighting. A stack is raw for the same reason but is already
// structured, and colouring it would only add noise.
var sqlValueKeys = map[string]bool{
	"sql":   true,
	"query": true,
}

// paintValue colours a value by what it means, not by its type: anything naming
// a failure is red so it cannot be missed.
func (h *prettyHandler) paintValue(key string, v slog.Value) string {
	if rawValueKeys[key] {
		out := v.String()
		// a statement is the longest thing on the line and the part that has to
		// be parsed; highlighting its keywords and literals makes the shape
		// visible. A stack is already structured, so it is left alone.
		if h.color && sqlValueKeys[key] {
			return highlightSQL(out)
		}
		// otherwise unpainted on purpose: with every key dimmed, plain text is
		// the brightest thing on the line, which is where the eye should land
		return out
	}

	out := formatValue(v)

	switch key {
	case "err", "error", "cause", "panic":
		return h.paint(out, ansiRed)
	case "request_id", "trace_id", "run_id", "job":
		return h.paint(out, ansiMagenta)
	}
	return out
}

func formatValue(v slog.Value) string {
	// a payload logged whole reads as JSON here too, rather than as Go's
	// `{u-1 a@b.co}` with the field names dropped
	if encoded, ok := jsonValue(v); ok {
		return encoded
	}
	s := v.String()
	if s == "" || strings.ContainsAny(s, " \t\n\"=") {
		return strconv.Quote(s)
	}
	return s
}

func (h *prettyHandler) paint(s, color string) string {
	if !h.color || color == "" {
		return s
	}
	return color + s + ansiReset
}

// levelLabel pads to a fixed width so messages line up down the page.
func levelLabel(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO "
	case l < slog.LevelError:
		return "WARN "
	default:
		return "ERROR"
	}
}

func levelColor(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return ansiCyan
	case l < slog.LevelWarn:
		return ansiGreen
	case l < slog.LevelError:
		return ansiYellow
	default:
		return ansiBold + ansiRed
	}
}

// wantColor decides whether to emit escape codes.
//
// Getting this wrong is worse than having no colour at all: escape codes in a
// redirected file or a log pipeline turn every line into noise. So colour is on
// only for a real terminal, and any explicit signal wins over the guess.
//
//   - NO_COLOR set (any value) → off, per https://no-color.org
//   - LOG_COLOR set            → exactly that
//   - otherwise                → on when the destination is a terminal
func wantColor(w io.Writer, env IENV) bool {
	if _, forced := os.LookupEnv("NO_COLOR"); forced {
		return false
	}
	if env != nil && env.String("log_color") != "" {
		return env.Bool("log_color")
	}

	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
