package core

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
)

// SentryRecorder captures the events a tracker would have sent, instead of
// sending them. It is the job store's in-memory twin, for error reporting: it
// lets a service assert that a failure is actually reported — and with what —
// without a network, a DSN or a Sentry account.
//
//	tracker, rec := core.NewRecordingSentry(env)
//	app, _ := core.NewApp(env, core.WithSentry(tracker))
//	...
//	if got := rec.Codes(); !slices.Contains(got, "DATABASE_ERROR") { t.Fatal(got) }
type SentryRecorder struct {
	mu     sync.Mutex
	events []*sentry.Event
}

var _ sentry.Transport = (*SentryRecorder)(nil)

// NewSentryRecorder builds an empty recorder.
func NewSentryRecorder() *SentryRecorder { return &SentryRecorder{} }

// NewRecordingSentry builds a fully working tracker whose events are kept in
// memory. Everything else — scrubbing, scope, fingerprints, breadcrumbs — runs
// exactly as it does in production, which is what makes the assertions worth
// anything.
func NewRecordingSentry(env IENV, opts ...SentryOptions) (ISentry, *SentryRecorder, IError) {
	var o SentryOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	rec := NewSentryRecorder()
	o.Transport = rec
	if o.DSN == "" {
		// a syntactically valid DSN; the transport never uses it
		o.DSN = "http://recorder@localhost/1"
	}
	tracker, err := NewSentry(env, o)
	if err != nil {
		return nil, nil, err
	}
	return tracker, rec, nil
}

// --- sentry.Transport ---

func (r *SentryRecorder) Configure(sentry.ClientOptions) {}

func (r *SentryRecorder) SendEvent(event *sentry.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *SentryRecorder) Flush(time.Duration) bool { return true }

func (r *SentryRecorder) FlushWithContext(context.Context) bool { return true }

func (r *SentryRecorder) Close() {}

// --- assertions ---

// Events returns every recorded event, oldest first.
func (r *SentryRecorder) Events() []*sentry.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*sentry.Event(nil), r.events...)
}

// Errors returns the recorded error events, leaving out transactions and
// cron check-ins.
func (r *SentryRecorder) Errors() []*sentry.Event {
	out := make([]*sentry.Event, 0, len(r.events))
	for _, e := range r.Events() {
		if e.Type == "" || e.Type == "event" {
			out = append(out, e)
		}
	}
	return out
}

// Logs returns every line streamed to Sentry Logs, oldest first. Log lines are
// batched by the SDK, so call ISentry.Flush before asserting on them:
//
//	app.Sentry().Flush(time.Second)
//	require.True(t, rec.HasLog("charge failed"))
func (r *SentryRecorder) Logs() []sentry.Log {
	out := make([]sentry.Log, 0)
	for _, e := range r.Events() {
		if e.Type != "log" {
			continue
		}
		out = append(out, e.Logs...)
	}
	return out
}

// Metrics returns every measurement the tracker recorded, oldest first. Like
// logs they are batched, so call ISentry.Flush before asserting on them.
func (r *SentryRecorder) Metrics() []sentry.Metric {
	out := make([]sentry.Metric, 0)
	for _, e := range r.Events() {
		if e.Type != "trace_metric" {
			continue
		}
		out = append(out, e.Metrics...)
	}
	return out
}

// Metric returns the last measurement recorded under name, or nil.
func (r *SentryRecorder) Metric(name string) *sentry.Metric {
	metrics := r.Metrics()
	for i := len(metrics) - 1; i >= 0; i-- {
		if metrics[i].Name == name {
			return &metrics[i]
		}
	}
	return nil
}

// HasLog reports whether any streamed log line's body contains substr.
func (r *SentryRecorder) HasLog(substr string) bool {
	for _, l := range r.Logs() {
		if strings.Contains(l.Body, substr) {
			return true
		}
	}
	return false
}

// Last returns the most recent error event, or nil.
func (r *SentryRecorder) Last() *sentry.Event {
	errs := r.Errors()
	if len(errs) == 0 {
		return nil
	}
	return errs[len(errs)-1]
}

// Codes returns the error code of every recorded error event, in order.
func (r *SentryRecorder) Codes() []string {
	events := r.Errors()
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, eventCode(e))
	}
	return out
}

// Len is how many error events were recorded.
func (r *SentryRecorder) Len() int { return len(r.Errors()) }

// Reset drops everything recorded so far.
func (r *SentryRecorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// eventCode is the framework error code an event was titled with (see retitle),
// falling back to the exception type.
func eventCode(e *sentry.Event) string {
	if e == nil {
		return ""
	}
	if code, ok := e.Tags["error.code"]; ok && code != "" {
		return code
	}
	if len(e.Exception) > 0 {
		return e.Exception[len(e.Exception)-1].Type
	}
	return e.Message
}

// HasBreadcrumb reports whether any recorded event carries a breadcrumb whose
// message contains substr.
func (r *SentryRecorder) HasBreadcrumb(substr string) bool {
	for _, e := range r.Events() {
		for _, c := range e.Breadcrumbs {
			if c != nil && strings.Contains(c.Message, substr) {
				return true
			}
		}
	}
	return false
}
