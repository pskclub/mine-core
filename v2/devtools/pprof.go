package devtools

import (
	"net/http"
	"net/http/pprof"

	core "github.com/pskclub/mine-core/v2"
)

// mountPprof adds Go's own profiling endpoints under "<prefix>/debug/pprof",
// behind whatever guards the rest of the panel.
//
// The overview says how many goroutines this process has; only pprof says what
// they are, which is the question a wedged process actually raises. The usual
// alternative is a second listener on some other port, opened in main and never
// closed again — this is the same data behind the lock that is already there.
//
// It is off by default, and not because of what it reveals (the panel already
// reveals more): a CPU or block profile costs real time on a live process, and
// /debug/pprof/profile holds the request open for thirty seconds while it runs.
// Turning that on is a decision, so it is a field.
func (d *devtools) mountPprof(g *core.Group) {
	// Index links to the other handlers by absolute path, and it builds those
	// links from the request URL — so it only works when the routes really are
	// under this prefix, which they are.
	g.GET("/debug/pprof", wrapPprof(pprof.Index))
	g.GET("/debug/pprof/", wrapPprof(pprof.Index))
	g.GET("/debug/pprof/cmdline", wrapPprof(pprof.Cmdline))
	g.GET("/debug/pprof/profile", wrapPprof(pprof.Profile))
	g.GET("/debug/pprof/symbol", wrapPprof(pprof.Symbol))
	g.POST("/debug/pprof/symbol", wrapPprof(pprof.Symbol))
	g.GET("/debug/pprof/trace", wrapPprof(pprof.Trace))

	// The named profiles (goroutine, heap, allocs, mutex, block, threadcreate)
	// are all served by Handler(name). A single ":name" route covers whatever
	// the runtime offers, including profiles a future Go version adds.
	g.GET("/debug/pprof/:name", func(c core.IHTTPContext) error {
		pprof.Handler(c.Param("name")).ServeHTTP(c.Response(), c.Request())
		return nil
	})
}

// wrapPprof adapts a net/http handler to the framework's own.
//
// The response is written by pprof directly, so nothing here may write to it
// afterwards — returning nil rather than an error is what keeps the framework's
// error renderer from appending JSON to a profile.
func wrapPprof(h http.HandlerFunc) core.HandlerFunc {
	return func(c core.IHTTPContext) error {
		h(c.Response(), c.Request())
		return nil
	}
}

// pprofTimeoutNote is the one thing worth knowing before the first attempt: a
// CPU profile runs for ?seconds= (30 by default) and the request stays open the
// whole time, so a server whose write timeout is shorter cuts it off with
// nothing to show for it.
//
// It is a constant rather than a comment so the panel can say it too.
const pprofTimeoutNote = "a CPU profile holds the request open for ?seconds= (30 by default) — " +
	"raise HTTPOptions.WriteTimeout, or ask for fewer seconds"

// pprofProfiles is what the panel offers as links. The list is what the runtime
// has always had; Handler(name) serves anything else it grows.
var pprofProfiles = []struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}{
	{"goroutine", "every goroutine and where it is blocked — the first thing to read on a wedged process"},
	{"heap", "live allocations, for a process whose memory grows"},
	{"allocs", "every allocation since start"},
	{"threadcreate", "OS threads created"},
	{"block", "where goroutines wait on synchronisation (needs runtime.SetBlockProfileRate)"},
	{"mutex", "lock contention (needs runtime.SetMutexProfileFraction)"},
}

// pprofIndex is what the panel shows about profiling — the links, and the note
// about the CPU profile's timeout.
func (d *devtools) pprofIndex(c core.IHTTPContext) error {
	if !d.opts.Pprof {
		return core.New(http.StatusNotFound, "DEVTOOLS_NO_PPROF",
			"devtools: profiling is off — set Options.Pprof to turn it on")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"base":     d.prefix + "/debug/pprof",
		"profiles": pprofProfiles,
		"note":     pprofTimeoutNote,
	})
}
