package core

import (
	"context"
	"log/slog"
	"runtime"
	"time"
)

// LogEntry is one log line handed to an observer, flattened out of slog's
// Record so a reader does not have to know how slog works — and, more
// importantly, so it is safe to keep: a slog.Record's attributes may not be
// iterated twice or held past the Handle call, and an observer that stores one
// would be reading memory the logger has moved on from.
type LogEntry struct {
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	// Source is the "file.go:12" the line was written at, empty when LOG_SOURCE
	// is off.
	Source string         `json:"source,omitempty"`
	Attrs  map[string]any `json:"attrs,omitempty"`
}

// LogTapFunc is called for every line the App's logger writes, with the context
// the line was written under — which is what lets an observer attribute it to a
// request (see RequestID).
//
// It runs inline, on the goroutine that logged, so it must be quick and must not
// log anything itself: a tap that writes a line writes it through the same
// logger and never returns.
type LogTapFunc func(ctx context.Context, entry LogEntry)

// WithLogTap installs an observer over the App's logger.
//
// It exists for the tools that have to see the framework's own output as
// structured events rather than as text on stdout — the devtools request trace
// is the one in the tree. Reporting to a log aggregator is not what this is for:
// that is a slog handler, configured with WithLogger.
//
//	trace := devtools.NewTrace(devtools.TraceOptions{})
//	app, err := core.NewApp(env, core.WithLogTap(trace.Log), …)
//
// A tap only sees what is actually logged: a line below LOG_LEVEL is never
// written, and the SQL, outgoing-HTTP and model loggers have levels of their own
// (DB_LOG_LEVEL and friends default to warn, so only the slow and the failed
// reach a tap until they are turned up).
func WithLogTap(tap LogTapFunc) Option {
	return func(a *App) {
		if tap == nil {
			return
		}
		a.logTaps = append(a.logTaps, tap)
	}
}

// tapLogger wraps l so every record it writes is also handed to taps. The
// wrapping is at the slog.Handler level rather than around ILogger on purpose:
// a decorated ILogger would no longer be the framework's own *logger, and
// ctx.Log() would silently stop binding the request id to the line. That is the
// same reason the Sentry bridge is a handler.
func tapLogger(l ILogger, taps []LogTapFunc) ILogger {
	if l == nil || len(taps) == 0 {
		return l
	}
	base, ok := l.(*logger)
	if !ok {
		return l
	}
	wrapped := *base
	wrapped.l = slog.New(&tapHandler{inner: base.l.Handler(), taps: taps})
	return &wrapped
}

// tapHandler forwards each record to the taps and then to the real handler.
type tapHandler struct {
	inner slog.Handler
	taps  []LogTapFunc
	// attrs and group are the pinned attributes of a With/WithGroup child, kept
	// so a tap sees them too — a line logged through ctx.Log() carries its
	// request id as one of these, not in the record.
	attrs []slog.Attr
	group string
}

func (h *tapHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *tapHandler) Handle(ctx context.Context, r slog.Record) error {
	entry := LogEntry{
		At:      r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
		Source:  recordSource(r),
	}
	if n := r.NumAttrs() + len(h.attrs); n > 0 {
		entry.Attrs = make(map[string]any, n)
		for _, a := range h.attrs {
			putAttr(entry.Attrs, a)
		}
		r.Attrs(func(a slog.Attr) bool {
			putAttr(entry.Attrs, a)
			return true
		})
	}
	for _, tap := range h.taps {
		tap(ctx, entry)
	}
	return h.inner.Handle(ctx, r)
}

func (h *tapHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := *h
	out.inner = h.inner.WithAttrs(attrs)
	out.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &out
}

func (h *tapHandler) WithGroup(name string) slog.Handler {
	out := *h
	out.inner = h.inner.WithGroup(name)
	out.group = name
	return &out
}

// putAttr flattens one attribute. A group becomes "group.key" rather than a
// nested map, because that is how the JSON output already reads and a reader
// comparing the two should not have to translate.
func putAttr(into map[string]any, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		for _, sub := range a.Value.Group() {
			if a.Key == "" {
				putAttr(into, sub)
				continue
			}
			nested := map[string]any{}
			putAttr(nested, sub)
			for k, v := range nested {
				into[a.Key+"."+k] = v
			}
		}
		return
	}
	if a.Key == "" {
		return
	}
	into[a.Key] = a.Value.Any()
}

// recordSource renders the record's program counter the same way the log output
// does, so a line in a trace and the same line on stdout name one place.
func recordSource(r slog.Record) string {
	if r.PC == 0 {
		return ""
	}
	fs := runtime.CallersFrames([]uintptr{r.PC})
	f, _ := fs.Next()
	if f.File == "" {
		return ""
	}
	return shortSource(f.File, f.Line)
}

// RequestID is the id of the request or job run ctx belongs to, or "".
//
// It is what correlates a log line, a Sentry event and a trace entry with each
// other. The framework puts it on the context in its own middleware; this is for
// application code that wants to carry it somewhere else — a downstream call, a
// row in an audit table, a support reply that says which request to look at.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithRequestID returns ctx carrying id, so everything logged under it is
// correlated with the request it came from.
//
// The HTTP middleware already does this for a request. This is for the work that
// leaves one — a goroutine started to finish something after the response, a
// message published and consumed elsewhere — where the correlation is the only
// thing connecting the two halves of one story.
func WithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return withRequestID(ctx, id)
}
