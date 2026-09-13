package devtools

import (
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTracedServer wires the trace the way a service does: the tap on the App so
// it sees the lines, the Trace on the mount so the tab can read them.
func newTracedServer(t *testing.T, opts TraceOptions) (*core.Server, *Trace) {
	t.Helper()
	for k, v := range map[string]string{"ENV": "dev", "SERVICE": "svc"} {
		t.Setenv("APP_"+k, v)
	}
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)

	trace := NewTrace(opts)
	app, ierr := core.NewApp(env, core.WithLogTap(trace.Log))
	require.Nil(t, ierr)

	s := core.NewHTTPServer(app, nil)
	s.GET("/orders/:id", func(c core.IHTTPContext) error {
		// what a handler's own logging looks like from the trace's side
		c.Log().Info("loading order", "order_id", c.Param("id"))
		return c.JSON(http.StatusOK, map[string]string{"id": c.Param("id")})
	})
	s.GET("/broken", func(c core.IHTTPContext) error {
		return core.New(http.StatusTeapot, "TEAPOT", "no")
	})
	require.Nil(t, Mount(s, Options{Trace: trace}))
	return s, trace
}

// The point of the trace is that a request's log lines are grouped by the
// request rather than interleaved with every other one in flight.
func TestTrace_groupsTheLinesOfARequest(t *testing.T) {
	s, _ := newTracedServer(t, TraceOptions{})

	require.Equal(t, http.StatusOK, get(s, "/orders/42").Code)

	list := decode[struct {
		Items []TraceEntry `json:"items"`
	}](t, get(s, DefaultPrefix+"/api/trace"))
	require.Len(t, list.Items, 1)

	entry := list.Items[0]
	assert.Equal(t, http.MethodGet, entry.Method)
	assert.Equal(t, "/orders/42", entry.Path)
	assert.Equal(t, "/orders/:id", entry.Route, "the pattern is what groups; the URL is what reproduces")
	assert.Contains(t, entry.Handler, "newTracedServer", "the handler is named as the access line names it")
	assert.Equal(t, http.StatusOK, entry.Status)
	assert.True(t, entry.Done)
	assert.Nil(t, entry.Events, "the list is a summary — events are fetched per entry")

	full := decode[TraceEntry](t, get(s, DefaultPrefix+"/api/trace/"+entry.ID))
	require.NotEmpty(t, full.Events)

	var found bool
	for _, ev := range full.Events {
		if ev.Message == "loading order" {
			found = true
			assert.Equal(t, "info", ev.Level)
			assert.Equal(t, "42", ev.Attrs["order_id"], "the line's own fields have to survive")
			assert.NotEmpty(t, ev.Source, "where a line came from is the first thing asked of it")
		}
	}
	assert.True(t, found, "the handler's own line must be in its request's timeline")
}

// A handler that returns an error has not written a status when the middleware
// unwinds. Recording the 200 the recorder still holds would make every failed
// request look like a success — the same bug the access line has to avoid.
func TestTrace_recordsTheStatusOfAFailedRequest(t *testing.T) {
	s, _ := newTracedServer(t, TraceOptions{})

	require.Equal(t, http.StatusTeapot, get(s, "/broken").Code)

	list := decode[struct {
		Items []TraceEntry `json:"items"`
	}](t, get(s, DefaultPrefix+"/api/trace"))
	require.Len(t, list.Items, 1)
	assert.Equal(t, http.StatusTeapot, list.Items[0].Status)
}

// A panel that records itself fills the buffer with itself.
func TestTrace_neverRecordsItsOwnRequests(t *testing.T) {
	s, _ := newTracedServer(t, TraceOptions{})

	get(s, DefaultPrefix+"/api/overview")
	get(s, DefaultPrefix+"/api/routes")
	get(s, "/healthz")

	list := decode[struct {
		Items []TraceEntry `json:"items"`
	}](t, get(s, DefaultPrefix+"/api/trace"))
	assert.Empty(t, list.Items)
}

func TestTrace_onlyAndSkip(t *testing.T) {
	s, trace := newTracedServer(t, TraceOptions{Only: []string{"/orders"}})

	get(s, "/orders/1")
	get(s, "/broken")

	entries := trace.List()
	require.Len(t, entries, 1)
	assert.Equal(t, "/orders/1", entries[0].Path)
}

// The buffer is bounded on purpose; what matters is that the eviction takes the
// map with it, or the process leaks one entry per request forever.
func TestTrace_dropsTheOldestPastItsSize(t *testing.T) {
	s, trace := newTracedServer(t, TraceOptions{Size: 3})

	for _, id := range []string{"1", "2", "3", "4", "5"} {
		require.Equal(t, http.StatusOK, get(s, "/orders/"+id).Code)
	}

	entries := trace.List()
	require.Len(t, entries, 3, "the buffer must not grow past its size")
	assert.Equal(t, "/orders/5", entries[0].Path, "newest first")
	assert.Equal(t, "/orders/3", entries[2].Path)

	// and the evicted ones are really gone, not merely hidden from the listing
	assert.Equal(t, http.StatusNotFound, get(s, DefaultPrefix+"/api/trace/whatever").Code)
}

// A request that logs in a loop must not be able to exhaust memory.
func TestTrace_capsTheEventsOfOneRequest(t *testing.T) {
	trace := NewTrace(TraceOptions{MaxEvents: 2})
	trace.begin("req-1", "GET", "/x", "/x", "")

	ctx := core.WithRequestID(t.Context(), "req-1")
	for range 5 {
		trace.Log(ctx, core.LogEntry{Level: "INFO", Message: "spam"})
	}

	entry, ok := trace.Get("req-1")
	require.True(t, ok)
	assert.Len(t, entry.Events, 2, "the first lines are kept: the start of a runaway loop says more than its end")
	assert.Equal(t, 3, entry.Dropped)
	assert.Equal(t, 5, entry.EventCount, "the count is what says the cap was hit")
}

// A line written outside any request — a job run, the scheduler, boot — belongs
// to no entry and must not create one.
func TestTrace_ignoresLinesWithNoRequest(t *testing.T) {
	trace := NewTrace(TraceOptions{})
	trace.Log(t.Context(), core.LogEntry{Level: "INFO", Message: "app ready"})
	assert.Empty(t, trace.List())
}

func TestTrace_clear(t *testing.T) {
	s, trace := newTracedServer(t, TraceOptions{})
	get(s, "/orders/1")
	require.Len(t, trace.List(), 1)

	req := httptest.NewRequest(http.MethodDelete, DefaultPrefix+"/api/trace", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, trace.List())
}

// With no Trace wired the tab has to say what turns it on, rather than showing
// an empty list that looks like a service nobody is calling.
func TestTrace_offSaysHowToTurnItOn(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{}))

	rec := get(s, DefaultPrefix+"/api/trace")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "WithLogTap")

	o := decode[overviewResponse](t, get(s, DefaultPrefix+"/api/overview"))
	assert.False(t, o.Tracing)
}
