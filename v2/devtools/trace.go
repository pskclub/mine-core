package devtools

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
	core "github.com/pskclub/mine-core/v2"
)

// Trace remembers the last N requests and everything the framework logged while
// each one was in flight — the SQL a repository issued, the HTTP call a client
// made, the model completion a handler asked for, alongside the service's own
// lines.
//
// It answers the question a log file answers badly: not "what happened at
// 14:03:11" but "what did *this* request do, in order". Correlating that by hand
// means grepping a request id out of interleaved output from every concurrent
// request, and the id is only there because the framework put it there — which
// is the same fact this uses to do the grouping up front.
//
// It is opt-in twice over, because it keeps request paths and log attributes in
// memory: the caller has to construct it and hand it to both the App and the
// mount.
//
//	trace := devtools.NewTrace(devtools.TraceOptions{})
//	app, err := core.NewApp(env, core.WithLogTap(trace.Log), …)
//	devtools.Mount(srv, devtools.Options{Trace: trace, …})
//
// Nothing is written anywhere. The buffer is bounded, in memory, and gone when
// the process restarts.
type Trace struct {
	opts TraceOptions

	mu      sync.Mutex
	order   []string
	entries map[string]*TraceEntry
}

// TraceOptions bounds what a Trace keeps.
type TraceOptions struct {
	// Size is how many requests are remembered (default DefaultTraceSize). The
	// oldest is dropped to make room.
	Size int
	// MaxEvents caps the log lines kept per request (default
	// DefaultTraceMaxEvents). A request that exceeds it keeps the first
	// MaxEvents and counts the rest — the beginning of a runaway loop says more
	// than its end.
	MaxEvents int
	// Only, when set, records just the requests whose path starts with one of
	// these prefixes. For a busy service where the interesting endpoint is one
	// of two hundred.
	Only []string
	// Skip never records these prefixes. The devtools prefix itself and the
	// health probes are always skipped — a panel that records itself fills the
	// buffer with itself.
	Skip []string
}

// Trace defaults.
const (
	DefaultTraceSize      = 200
	DefaultTraceMaxEvents = 200
)

// TraceEntry is one request.
type TraceEntry struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	// Route is the registered pattern ("/users/:id"), where Path is the URL
	// that was asked for. Both, because one groups and the other reproduces.
	Route   string `json:"route,omitempty"`
	Handler string `json:"handler,omitempty"`
	// User is the authenticated principal's id, when a middleware resolved one
	// before the handler ran.
	User string `json:"user,omitempty"`

	Status    int   `json:"status"`
	LatencyMS int64 `json:"latency_ms"`
	// Done is false while the request is still running, which is exactly when a
	// hung request is worth looking at.
	Done bool `json:"done"`

	Events []TraceEvent `json:"events,omitempty"`
	// Dropped counts the events past MaxEvents.
	Dropped int `json:"dropped,omitempty"`
	// EventCount is kept separately so a summary can report it without carrying
	// the events themselves.
	EventCount int `json:"event_count"`
}

// TraceEvent is one line logged during a request.
type TraceEvent struct {
	At       time.Time      `json:"at"`
	OffsetMS int64          `json:"offset_ms"`
	Level    string         `json:"level"`
	Message  string         `json:"message"`
	Source   string         `json:"source,omitempty"`
	Attrs    map[string]any `json:"attrs,omitempty"`
}

// NewTrace creates a request trace. Pass its Log method to core.WithLogTap and
// the Trace itself to Mount.
func NewTrace(opts TraceOptions) *Trace {
	if opts.Size <= 0 {
		opts.Size = DefaultTraceSize
	}
	if opts.MaxEvents <= 0 {
		opts.MaxEvents = DefaultTraceMaxEvents
	}
	return &Trace{
		opts:    opts,
		entries: make(map[string]*TraceEntry, opts.Size),
		order:   make([]string, 0, opts.Size),
	}
}

// Log is the core.LogTapFunc. It attaches the line to the request it was written
// under, and drops it when there is none — a line from a job run, from the
// scheduler or from boot belongs to no request, and one from a devtools route
// would only ever describe the panel looking at itself.
func (t *Trace) Log(ctx context.Context, e core.LogEntry) {
	id := core.RequestID(ctx)
	if id == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[id]
	if !ok {
		// No entry means the middleware chose not to record this request. The
		// tap must not create one, or Skip and Only would apply to the request
		// line and not to what the request did.
		return
	}
	entry.EventCount++
	if len(entry.Events) >= t.opts.MaxEvents {
		entry.Dropped++
		return
	}
	entry.Events = append(entry.Events, TraceEvent{
		At:       e.At,
		OffsetMS: e.At.Sub(entry.At).Milliseconds(),
		Level:    strings.ToLower(e.Level),
		Message:  e.Message,
		Source:   e.Source,
		Attrs:    e.Attrs,
	})
}

// begin opens an entry for a request that is starting.
func (t *Trace) begin(id, method, path, route, handler string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, exists := t.entries[id]; exists {
		return
	}
	// evict before inserting, so the buffer never holds Size+1
	for len(t.order) >= t.opts.Size {
		oldest := t.order[0]
		t.order = t.order[1:]
		delete(t.entries, oldest)
	}
	t.entries[id] = &TraceEntry{
		ID: id, At: time.Now(), Method: method, Path: path, Route: route, Handler: handler,
	}
	t.order = append(t.order, id)
}

// end closes an entry. The entry may already have been evicted by a burst of
// later requests, which is not an error — it is the buffer doing its job.
func (t *Trace) end(id string, status int, latency time.Duration, user string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[id]
	if !ok {
		return
	}
	entry.Status, entry.LatencyMS, entry.Done = status, latency.Milliseconds(), true
	if user != "" {
		entry.User = user
	}
}

// List returns the entries newest first, without their events.
func (t *Trace) List() []TraceEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TraceEntry, 0, len(t.order))
	for i := len(t.order) - 1; i >= 0; i-- {
		entry, ok := t.entries[t.order[i]]
		if !ok {
			continue
		}
		summary := *entry
		summary.Events = nil
		out = append(out, summary)
	}
	return out
}

// Get returns one entry with its events, or false.
func (t *Trace) Get(id string) (TraceEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[id]
	if !ok {
		return TraceEntry{}, false
	}
	out := *entry
	out.Events = append([]TraceEvent(nil), entry.Events...)
	return out, true
}

// Clear empties the buffer.
func (t *Trace) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries = make(map[string]*TraceEntry, t.opts.Size)
	t.order = t.order[:0]
}

// records decides whether a path is traced at all.
func (t *Trace) records(path, devPrefix string) bool {
	if strings.HasPrefix(path, devPrefix) || path == "/healthz" || path == "/readyz" {
		return false
	}
	for _, skip := range t.opts.Skip {
		if skip != "" && strings.HasPrefix(path, skip) {
			return false
		}
	}
	if len(t.opts.Only) == 0 {
		return true
	}
	for _, only := range t.opts.Only {
		if only != "" && strings.HasPrefix(path, only) {
			return true
		}
	}
	return false
}

// middleware opens an entry before the handler and closes it after.
//
// It is installed with Use, so it runs for every route including the ones
// registered before Mount was called — and after the framework's own request-id
// middleware, which is what makes the id available to group by.
func (d *devtools) traceMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			path := req.URL.Path
			if !d.opts.Trace.records(path, d.prefix) {
				return next(c)
			}
			id := core.RequestID(req.Context())
			if id == "" {
				// Nothing to group by. Rather than invent an id — which would
				// make the trace disagree with every log line and Sentry event
				// about what this request is called — the request goes
				// unrecorded.
				return next(c)
			}

			route := c.RouteInfo().Path
			d.opts.Trace.begin(id, req.Method, path, route, d.srv.HandlerNames()[req.Method+" "+route])

			started := time.Now()
			err := next(c)

			status := http.StatusOK
			if r, uerr := echo.UnwrapResponse(c.Response()); uerr == nil && r != nil {
				status = r.Status
			}
			// A handler that returned an error has not written a status yet: the
			// error handler runs after this unwinds, so the recorder still holds
			// the untouched 200. The error carries the real one.
			if err != nil {
				if ie, ok := err.(core.IError); ok {
					status = ie.GetStatus()
				}
			}
			d.opts.Trace.end(id, status, time.Since(started), traceUser(c))
			return err
		}
	}
}

// traceUser is the authenticated principal, when the service's auth middleware
// left one on the context.
func traceUser(c *echo.Context) string {
	user := core.ContextUserOf(c)
	if user == nil {
		return ""
	}
	if user.ID != "" {
		return user.ID
	}
	return user.Email
}
