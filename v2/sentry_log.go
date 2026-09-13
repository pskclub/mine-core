package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/getsentry/sentry-go"
)

// errorAttrKeys are the attribute names a log line carries its error under.
// `ctx.Log().Error("charge failed", "err", err)` is how the whole codebase
// already writes it, so nothing new has to be learned.
var errorAttrKeys = map[string]bool{"err": true, "error": true}

// componentAttrKey turns `ctx.Log().With("component", "billing")` into the
// breadcrumb's category, so grouping the trail costs nothing extra.
const componentAttrKey = "component"

// traceAttrs returns the correlation attributes carried by ctx, so a log line,
// the span it happened in and the Sentry event it caused all share one id and
// you can jump between the three.
func traceAttrs(ctx context.Context) []any {
	span := sentry.SpanFromContext(ctx)
	if span == nil {
		return nil
	}
	return []any{
		slog.String("trace_id", span.TraceID.String()),
		slog.String("span_id", span.SpanID.String()),
	}
}

// sentryLogHandler is the bridge that removes the need to ever write
// ctx.Sentry() in application code:
//
//   - every log line becomes a breadcrumb on the context's hub, so an issue
//     arrives with the twenty lines that led to it;
//   - a line that carries a *reportable* error (status >= MinStatus) becomes an
//     event, so an error you handled and only logged is still an incident.
//
// The threshold is the framework's single one: a failure is reported when its
// status says so, never because of the log level it was written at. A 4xx, a
// message with no error attached, or an error some other layer already reported
// stays a breadcrumb.
//
// With SENTRY_ENABLE_LOGS it also streams every line to Sentry Logs, where it
// is searchable on its own — a breadcrumb only exists attached to an event, so
// a service that never fails leaves no trace of what it did.
//
// It wraps the real handler rather than replacing it — logs still go to stdout
// exactly as before.
type sentryLogHandler struct {
	inner     slog.Handler
	hub       *sentry.Hub
	min       slog.Level
	crumbsOff bool
	minStatus int
	logs      bool
	logLevel  slog.Level
	scrubber  *scrubber
	attrs     []slog.Attr
}

var _ slog.Handler = (*sentryLogHandler)(nil)

// withSentryLogging wraps l so its records reach Sentry as breadcrumbs, as
// events when they carry a server failure, and as log lines when Sentry Logs is
// enabled.
func withSentryLogging(l ILogger, tracker ISentry) ILogger {
	if l == nil || tracker == nil {
		return l
	}
	base, ok := l.(*logger)
	if !ok {
		return l
	}
	cfg := sentryOf(tracker)
	if cfg.scrubber == nil {
		cfg.scrubber = newScrubber(nil, nil)
	}
	wrapped := *base
	wrapped.l = slog.New(&sentryLogHandler{
		inner:     base.l.Handler(),
		hub:       tracker.Hub(),
		min:       cfg.breadcrumbLevel,
		crumbsOff: cfg.breadcrumbsOff,
		minStatus: cfg.minStatus,
		logs:      cfg.logs,
		logLevel:  cfg.logLevel,
		scrubber:  cfg.scrubber,
	})
	return &wrapped
}

func (h *sentryLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// ctxKeyNoCapture marks a context whose log lines must stay log lines.
type ctxKeyNoCapture struct{}

// withoutCapture returns ctx where a log line still reaches everything a line
// reaches — the request's breadcrumbs, its trace, Sentry Logs — but never
// becomes an event of its own.
//
// It is for the framework's own middleware, which logs *about* a failure that
// another layer is already reporting properly: the access line of a request
// that returned 500, the "panic recovered" line sitting next to the panic's own
// issue. Without it, binding those lines to the request would file a second,
// worse issue for every failure — one with no stack and a title of "request".
func withoutCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyNoCapture{}, true)
}

func capturesDisabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(ctxKeyNoCapture{}).(bool)
	return disabled
}

// ctxKeyNoCrumb marks a context whose log lines must not also become breadcrumbs.
type ctxKeyNoCrumb struct{}

// withoutBreadcrumb returns ctx where a log line still goes to stdout, to Sentry
// Logs and to its trace, but leaves no breadcrumb.
//
// For a line that describes something the SDK already records in a richer form:
// an outgoing HTTP call leaves a typed `http` breadcrumb from the client hook,
// with its own rendering in an issue, so the log line about it would be a second
// entry for one event — and a trail with everything in it twice is a trail
// nobody reads.
func withoutBreadcrumb(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyNoCrumb{}, true)
}

func breadcrumbsDisabled(ctx context.Context) bool {
	off, _ := ctx.Value(ctxKeyNoCrumb{}).(bool)
	return off
}

func (h *sentryLogHandler) Handle(ctx context.Context, r slog.Record) error {
	if hub := sentry.GetHubFromContext(ctx); hub != nil {
		// capture first: the line that reports the failure is the event's
		// title, not one of its own breadcrumbs
		if !capturesDisabled(ctx) {
			h.capture(hub, r)
		}
		if !h.crumbsOff && !breadcrumbsDisabled(ctx) && r.Level >= h.min {
			hub.AddBreadcrumb(h.crumb(r), nil)
		}
	}
	h.stream(ctx, r)
	return h.inner.Handle(ctx, r)
}

// stream sends the line to Sentry Logs. Unlike a breadcrumb it stands on its
// own: it is searchable whether or not anything ever failed, and Sentry pairs it
// with the trace of the request or run it belongs to.
//
// A line written outside any request or run still gets there — it falls back to
// the process hub and only loses the trace correlation. Nothing is written to
// that hub's scope, so no state is shared between goroutines the way v1 did.
func (h *sentryLogHandler) stream(ctx context.Context, r slog.Record) {
	if !h.logs || r.Level < h.logLevel {
		return
	}
	if sentry.GetHubFromContext(ctx) == nil {
		if h.hub == nil {
			return
		}
		ctx = sentry.SetHubOnContext(ctx, h.hub)
	}
	entry := logEntryFor(sentry.NewLogger(ctx), r.Level)
	for _, a := range recordAttrs(h.attrs, r) {
		h.attach(entry, "", a)
	}
	// the error's own attribute is its message; the code is what you filter on
	if err := errorOfRecord(h.attrs, r); err != nil {
		var ierr IError
		if errors.As(err, &ierr) && ierr.GetCode() != "" {
			entry.String("error.code", ierr.GetCode())
		}
	}
	entry.Emit(r.Message)
}

// attach puts one slog attribute on the log entry, typed so Sentry can filter
// and aggregate on it (a number stays a number), and masked by the same
// scrubber every other outgoing channel uses.
func (h *sentryLogHandler) attach(e sentry.LogEntry, prefix string, a slog.Attr) {
	key := a.Key
	if key == "" {
		return
	}
	if prefix != "" {
		key = prefix + "." + key
	}
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		for _, g := range v.Group() {
			h.attach(e, key, g)
		}
		return
	}
	if h.scrubber.sensitive(key) {
		e.String(key, redacted)
		return
	}
	switch v.Kind() {
	case slog.KindBool:
		e.Bool(key, v.Bool())
	case slog.KindInt64:
		e.Int64(key, v.Int64())
	case slog.KindUint64:
		e.Int64(key, int64(v.Uint64()))
	case slog.KindFloat64:
		e.Float64(key, v.Float64())
	case slog.KindDuration:
		e.String(key, v.Duration().String())
	case slog.KindTime:
		e.String(key, v.Time().Format(time.RFC3339Nano))
	case slog.KindString:
		e.String(key, h.scrubber.maskSecrets(v.String()))
	default:
		// a struct or a map goes as JSON, and is scrubbed *through* that JSON —
		// the key of the attribute says nothing about the password field inside
		// it, so masking by key alone would send the whole payload in the clear
		if encoded, ok := jsonValue(v); ok {
			e.String(key, string(h.scrubber.scrubJSON([]byte(encoded))))
			return
		}
		e.String(key, h.scrubber.maskSecrets(stringOfAttr(v.Any())))
	}
}

// stringOfAttr renders a value Sentry has no type for. An error becomes its
// message — that is what the line was written to say.
func stringOfAttr(v any) string {
	switch t := v.(type) {
	case error:
		return t.Error()
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

// logEntryFor picks the Sentry severity for a record. Fatal and Panic are never
// used: the SDK's Fatal() calls os.Exit and Panic() panics, and a logger must
// not decide the process is over.
func logEntryFor(l sentry.Logger, level slog.Level) sentry.LogEntry {
	switch {
	case level >= slog.LevelError:
		return l.Error()
	case level >= slog.LevelWarn:
		return l.Warn()
	case level >= slog.LevelInfo:
		return l.Info()
	default:
		return l.Debug()
	}
}

func (h *sentryLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.inner = h.inner.WithAttrs(attrs)
	next.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &next
}

func (h *sentryLogHandler) WithGroup(name string) slog.Handler {
	next := *h
	next.inner = h.inner.WithGroup(name)
	return &next
}

// capture reports what a log line carries, if it is worth reporting and nobody
// has reported it yet.
//
// A status is a judgement someone already made about a failure: 404 means the
// caller asked for the wrong thing, 400 means they sent nonsense. SENTRY_MIN_STATUS
// filters on that judgement, and it works because the judgement exists.
//
// No status means nobody made that judgement — a driver error, a marshalling
// failure, a plain errors.New from a library. There is nothing to compare against
// a threshold, and treating "unknown" as "not worth reporting" hides exactly the
// failures nobody anticipated. Those are always sent.
//
// A bare Error() line with no error attached is the same situation: no status,
// and code that stopped to write Error() had a reason. Sent as a message event.
// Below Error, a line with no error is just narration and stays quiet.
func (h *sentryLogHandler) capture(hub *sentry.Hub, r slog.Record) {
	err := errorOfRecord(h.attrs, r)

	switch {
	case err == nil:
		if r.Level < slog.LevelError {
			return // narration, not an incident
		}
	case !hasStatus(err):
		// unclassified failure: always reported, whatever the threshold says
	case !captureStatus(err, h.minStatus):
		return // judged a client-side failure, below the reporting threshold
	}

	if err != nil && capturedID(err) != "" {
		return // already reported where it was built or returned
	}

	message := r.Message
	hub.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(recordLevel(r, err))
		if fp := defaultFingerprint(err); fp != nil {
			scope.SetFingerprint(fp)
		}
		// what the line was carrying, minus the error itself — that is the
		// exception. sentry-go dropped "extra" in 0.46, so it is a context block
		if attrs := recordAttrs(h.attrs, r); len(attrs) > 0 {
			data := make(map[string]any, len(attrs))
			for _, a := range attrs {
				if errorAttrKeys[a.Key] {
					continue
				}
				data[a.Key] = a.Value.Any()
			}
			if len(data) > 0 {
				scope.SetContext("log", sentry.Context(data))
			}
		}

		// whatever shape the event takes, it is about the line that logged — an
		// error that arrived without a stack of its own (a driver error, a
		// marshalling failure) would otherwise be reported against this bridge
		withReporterStacktrace(scope)

		// nothing to raise as an exception: the line itself is the report, and a
		// message event keeps it grouped by its text rather than by a stack it
		// does not have
		if err == nil {
			hub.CaptureMessage(message)
			return
		}

		// the log message says what the code was doing; the exception says what
		// went wrong. Sentry shows both.
		scope.AddEventProcessor(func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			event.Message = message
			return event
		})
		markCaptured(err, eventID(hub.CaptureException(err)))
	})
}

// recordLevel is the severity Sentry shows. An explicit Error() outranks what the
// status would imply — a handler that logs Error about a 404 means it, and
// filing that as a warning buries it.
func recordLevel(r slog.Record, err error) Level {
	if r.Level >= slog.LevelError {
		return LevelError
	}
	if err != nil {
		return levelForError(err)
	}
	return LevelInfo
}

// crumb renders a log record as a breadcrumb, keeping the pinned attributes
// (job, run_id, request_id …) so a crumb is readable on its own.
func (h *sentryLogHandler) crumb(r slog.Record) *sentry.Breadcrumb {
	attrs := recordAttrs(h.attrs, r)
	data := make(map[string]any, len(attrs))
	category := "log"
	for _, a := range attrs {
		if a.Key == componentAttrKey {
			if s, ok := a.Value.Any().(string); ok && s != "" {
				category = s
			}
		}
		data[a.Key] = a.Value.Any()
	}
	if len(data) == 0 {
		data = nil
	}
	return &sentry.Breadcrumb{
		Type:      "default",
		Category:  category,
		Message:   r.Message,
		Level:     breadcrumbLevel(r.Level),
		Data:      data,
		Timestamp: r.Time,
	}
}

// recordAttrs flattens the logger's pinned attributes and the record's own.
func recordAttrs(pinned []slog.Attr, r slog.Record) []slog.Attr {
	out := make([]slog.Attr, 0, len(pinned)+r.NumAttrs())
	out = append(out, pinned...)
	r.Attrs(func(a slog.Attr) bool {
		out = append(out, a)
		return true
	})
	return out
}

// errorOfRecord finds the error a log line carries under "err"/"error". The
// record's own attributes win over the logger's pinned ones.
func errorOfRecord(pinned []slog.Attr, r slog.Record) error {
	var found error
	r.Attrs(func(a slog.Attr) bool {
		if !errorAttrKeys[a.Key] {
			return true
		}
		if err, ok := a.Value.Any().(error); ok && err != nil {
			found = err
			return false
		}
		return true
	})
	if found != nil {
		return found
	}
	for _, a := range pinned {
		if !errorAttrKeys[a.Key] {
			continue
		}
		if err, ok := a.Value.Any().(error); ok && err != nil {
			return err
		}
	}
	return nil
}

// capturedID returns the Sentry event an error has already been reported as.
func capturedID(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.eventID
	}
	return ""
}

// markCaptured records that err (or any *Error it wraps) has been reported, so
// no layer above reports the same failure again. One failure, one issue.
func markCaptured(err error, id string) {
	if id == "" {
		return
	}
	var e *Error
	if errors.As(err, &e) && e.eventID == "" {
		e.eventID = id
	}
}

func breadcrumbLevel(l slog.Level) Level {
	switch {
	case l >= slog.LevelError:
		return LevelError
	case l >= slog.LevelWarn:
		return LevelWarning
	case l <= slog.LevelDebug:
		return LevelDebug
	default:
		return LevelInfo
	}
}
