package main

import (
	"net/http"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 1: the server, its options and its routes ----------------------
//
// NewHTTPServer returns a *core.Server that remembers the App, so a route takes
// its handler directly instead of every registration function threading `app`
// through purely to pass it on. It embeds *echo.Echo, so anything the framework
// does not wrap (Use, Static, Pre, …) is still one call away.
//
// A server built with nil options is already safe to deploy — the stack
// (request id, Sentry, access log, recover, CORS, body limit) and the deadlines
// are installed either way. Everything below is an override.

func newServer(app *core.App) *core.Server {
	return core.NewHTTPServer(app, &core.HTTPOptions{
		// CORS defaults to "*", which is right for a public read-only API and
		// wrong for one a browser sends credentials to. Naming the origins is
		// the whole of the fix and costs nothing to do on day one.
		AllowOrigins: []string{"https://app.example.com"},

		// 1 MB is generous for JSON. The routes that take files raise their own
		// ceiling (07_upload.go) — raising it here would hand the upload limit
		// to every endpoint that only ever receives a form, and the largest
		// request anybody sends is the memory this process uses.
		BodyLimit: 1 << 20,

		// Zero means "take the framework default", a negative value means "no
		// deadline at all" — an unset field cannot be told apart from an
		// explicit zero, so the two intentions need two spellings.
		//
		// ReadHeaderTimeout is what actually closes the slow-loris hole, which
		// is why it can be short while ReadTimeout stays at five minutes: a
		// legitimate upload over a phone connection takes minutes, headers
		// never do. They are two settings because they do two jobs.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,

		// WriteTimeout is deliberately left unset. It is an absolute deadline on
		// the whole response, so any value large enough for a slow download
		// protects nothing, and any value small enough to protect something cuts
		// an SSE stream in half. A service with neither can set one.
	})
}

// mountSystem registers what every service has, and returns the group the rest
// of the examples hang off.
func mountSystem(e *core.Server) *core.Group {
	// No group and no auth: a load balancer has to be able to ask.
	e.GET("/healthz", healthz)

	// A group is a path prefix plus middleware every route under it inherits,
	// and groups nest. The nested one carries the App too, so its routes take
	// plain handlers exactly like the server's.
	api := e.Group("/api")

	return api.Group("/v1")
}

func healthz(c core.IHTTPContext) error {
	// Report what this process can actually reach, not that it is running — a
	// handler that answers "ok" unconditionally proves only that the port is
	// open, which the load balancer already knew.
	return c.JSON(http.StatusOK, map[string]any{
		"service": c.ENV().Config().Service,
		"storage": c.Storage().Enabled(),
		"cache":   c.Cache().Enabled(),
		"db":      c.DB() != nil,
	})
}

// startServer is the HTTP-only starter: it blocks, drains in-flight requests on
// SIGTERM, and only then closes the App's pools — a pool closed while a request
// still holds it turns a clean deploy into a burst of 500s.
func startServer(e *core.Server, env core.IENV) {
	core.StartHTTPServer(e, env)
}

// startWithRunner is the same server in a process that also runs jobs, a
// scheduler or a subscriber. The ordering rule is the reason to switch: the
// scheduler must stop ticking *before* the drain starts, and Runner is where
// that sequence lives instead of in a hand-written shutdown nobody re-reads.
func startWithRunner(app *core.App, e *core.Server) error {
	return core.NewRunner(app,
		core.RunHTTP(e),
		// Keep the drain under the orchestrator's own grace period, or the
		// process is killed mid-drain and none of this ordering happens.
		core.WithDrainTimeout(20*time.Second),
	).Run()
}
