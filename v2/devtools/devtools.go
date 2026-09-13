// Package devtools mounts a small inspector on a running service: what this
// process was wired with, which routes exist and who serves them, what
// configuration it actually loaded, and what its jobs have been doing.
//
// It exists because every one of those questions is answerable from inside the
// process and from nowhere else — a capability that silently degraded, a route
// registered by a module nobody remembers importing, an APP_ variable that was
// spelled differently in the deployment than in the code. The alternative is
// reading the boot log and guessing.
//
// It is opt-in, and it is off unless it is mounted:
//
//	srv := core.NewHTTPServer(app, nil)
//	devtools.Mount(srv, devtools.Options{Registry: registry, Store: store})
//
// Outside dev it refuses to mount without Options.Auth. Everything it serves —
// configuration keys, connection names, job parameters — is exactly what an
// attacker would like to read first, and a debug endpoint is the route nobody
// remembers to put behind the gateway.
//
// Every endpoint is read-only unless Options.AllowWrite says otherwise — a
// second switch, separate from Auth, because "look at what this process is
// doing" and "re-run last night's billing job" are not the same permission.
package devtools

import (
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	core "github.com/pskclub/mine-core/v2"
)

// DefaultPrefix is where the inspector is mounted when Options.Prefix is empty.
// The underscore keeps it out of the way of any real API path.
const DefaultPrefix = "/_dev"

// Options configures a mount.
type Options struct {
	// Prefix is the base path (default DefaultPrefix). The UI is served there
	// and the JSON API under "<prefix>/api/...".
	Prefix string

	// BasicAuth guards the panel with a username and password the browser asks
	// for — the shortest path to a protected mount, and what makes turning this
	// on outside dev a matter of one environment variable rather than a code
	// change. See BasicAuth.
	//
	// It runs before Auth, and the two compose.
	BasicAuth *BasicAuth

	// Auth guards every devtools route. It is ordinary echo middleware, so
	// whatever a service already uses to protect an admin area works here.
	//
	// One of this and BasicAuth is required outside dev: Mount fails rather than
	// exposing the panel.
	Auth []echo.MiddlewareFunc

	// Runner turns on the jobs tab, and is what the write actions act through.
	// Pass the runner the service already built; the API process of a service
	// whose jobs run elsewhere can pass its own, since triggering only writes to
	// the shared queue.
	//
	// Registry and Store are taken from it, so they need not be set as well.
	Runner *core.JobRunner

	// Registry, Store and Queue are the read-only half of the jobs tab, for a
	// caller that has them without a Runner. Any of them may be nil — the panel
	// then reports what it cannot see instead of pretending the service has no
	// jobs.
	Registry *core.JobRegistry
	Store    core.IJobStore
	Queue    core.IJobQueue

	// AllowWrite turns on the actions that change something: triggering a job,
	// cancelling and replaying a run, pausing and resuming. Off by default, and
	// a separate switch from Auth on purpose — "let the on-call engineer read
	// what this process is doing" and "let them re-run last night's billing job"
	// are not the same permission, and the first is the one that gets granted
	// widely.
	//
	// Every action taken through it is logged with who asked for it (see
	// core.ContextUserOf), because an untraceable manual trigger is the thing
	// nobody can explain afterwards.
	AllowWrite bool

	// Pprof adds Go's profiling endpoints under "<prefix>/debug/pprof", behind
	// the same guard as the rest. The overview says how many goroutines there
	// are; only this says what they are.
	//
	// Off by default because a CPU or block profile costs real time on a live
	// process — not because of what it reveals, which is less than the config
	// tab already does.
	Pprof bool

	// Trace records the last N requests and what each of them logged. It has to
	// be handed to the App as well, so it sees the lines:
	//
	//	trace := devtools.NewTrace(devtools.TraceOptions{})
	//	app, _ := core.NewApp(env, core.WithLogTap(trace.Log), …)
	//	devtools.Mount(srv, devtools.Options{Trace: trace})
	//
	// Nil leaves the trace tab off, which is the default: it holds request paths
	// and log attributes in memory.
	Trace *Trace

	// Health tunes the readiness probe the health tab runs. Nil probes exactly
	// what the App holds, with details on.
	Health *core.HealthOptions

	// Modules turns on the modules tab: which module registered which route,
	// job, schedule, queue and health check.
	//
	// It is the only place that answers the question a module system otherwise
	// makes harder rather than easier — not "what does this module do" but
	// "what did it attach to *this* process". A worker role that never mounted
	// a module's routes and an api role that never armed its cron both look
	// perfectly healthy from every other tab.
	//
	// Pass the same set the Runner was given.
	Modules *core.ModuleSet
}

// registry is the job registry this mount reads, from whichever field supplied
// it.
func (o Options) registry() *core.JobRegistry {
	if o.Registry != nil {
		return o.Registry
	}
	if o.Runner != nil {
		return o.Runner.Registry()
	}
	return nil
}

// protected reports whether anything at all stands in front of the panel. It is
// what the page warns about when it is false, which can only happen in dev.
func (o Options) protected() bool {
	return len(o.Auth) > 0 || o.BasicAuth != nil
}

// store is the job store this mount reads.
func (o Options) store() core.IJobStore {
	if o.Store != nil {
		return o.Store
	}
	if o.Runner != nil {
		return o.Runner.Store()
	}
	return nil
}

// devtools holds what the handlers read. It is per-mount state, not package
// state: two servers in one process (an API and an admin listener) get their own.
type devtools struct {
	app     *core.App
	srv     *core.Server
	opts    Options
	prefix  string
	started time.Time
	page    []byte
}

// Mount registers the inspector on srv and returns the error that stopped it,
// or nil.
//
// The error is worth checking rather than ignoring: the case it reports is a
// service that would otherwise have published its configuration to the internet.
func Mount(srv *core.Server, opts Options) core.IError {
	if srv == nil {
		return core.New(http.StatusInternalServerError, "DEVTOOLS_NO_SERVER",
			"devtools: a server is required")
	}
	app := srv.App()
	if app == nil {
		return core.New(http.StatusInternalServerError, "DEVTOOLS_NO_APP",
			"devtools: the server has no app")
	}
	env := app.ENV()
	prefix := normalizePrefix(opts.Prefix)

	// A BasicAuth with a user and no password is a lock with no key: it would
	// refuse everybody, including whoever set it, and the failure would look
	// like a forgotten password rather than a missing configuration.
	if opts.BasicAuth != nil && opts.BasicAuth.Password == "" && env.String(EnvBasicAuthPassword) == "" {
		return core.New(http.StatusInternalServerError, "DEVTOOLS_NO_PASSWORD",
			"devtools: BasicAuth needs a password — set it, or set APP_DEVTOOLS_PASSWORD")
	}
	basic := resolveBasicAuth(opts, env)

	// The guard is here rather than in the handlers so the refusal happens at
	// startup, where somebody reads it, instead of on the first request nobody
	// makes until it is too late.
	if !env.IsDev() && len(opts.Auth) == 0 && basic == nil {
		return core.Newf(http.StatusForbidden, "DEVTOOLS_UNPROTECTED",
			"devtools: refusing to mount at %s with APP_ENV=%s and no guard — "+
				"set APP_DEVTOOLS_PASSWORD, or pass Options.BasicAuth or Options.Auth",
			prefix, env.Config().ENV)
	}

	// Basic auth first: a wrong password should not reach a guard of the
	// service's own, which may do a database lookup per request.
	guards := opts.Auth
	if basic != nil {
		guards = append([]echo.MiddlewareFunc{basic.middleware()}, opts.Auth...)
	}
	opts.BasicAuth = basic

	d := &devtools{
		app:     app,
		srv:     srv,
		opts:    opts,
		prefix:  prefix,
		started: time.Now(),
		page:    renderPage(prefix),
	}

	// Before the routes, so it wraps every one of them — including the ones
	// registered before this call, since echo resolves middleware at request
	// time rather than at registration.
	if opts.Trace != nil {
		srv.Use(d.traceMiddleware())
	}

	g := srv.Group(prefix, guards...)
	// both spellings: a person types "/_dev", a link inside the page resolves
	// against "/_dev/", and neither should 404
	g.GET("", d.ui)
	g.GET("/", d.ui)
	g.GET("/api/overview", d.overview)
	g.GET("/api/routes", d.routes)
	g.GET("/api/config", d.config)
	g.GET("/api/modules", d.modules)
	g.GET("/api/health", d.health)
	g.GET("/api/jobs", d.jobs)
	g.GET("/api/runs", d.runs)
	g.GET("/api/runs/:id", d.run)
	g.GET("/api/runs/:id/logs", d.runLogs)
	g.GET("/api/runs/:id/tail", d.tailRun)
	g.GET("/api/trace", d.traceList)
	g.GET("/api/trace/:id", d.traceGet)
	g.GET("/api/pprof", d.pprofIndex)
	if opts.Pprof {
		d.mountPprof(g)
	}

	// The write half. Registered whatever AllowWrite says, so a request to one
	// of them answers "this mount is read-only" instead of "no such route" —
	// the second reads as a version mismatch and sends somebody looking in the
	// wrong place.
	g.POST("/api/jobs/:name/trigger", d.triggerJob)
	g.POST("/api/jobs/:name/pause", d.pauseJob)
	g.POST("/api/jobs/:name/resume", d.resumeJob)
	g.POST("/api/runs/:id/cancel", d.cancelRun)
	g.POST("/api/runs/:id/replay", d.replayRun)
	g.DELETE("/api/trace", d.traceClear)

	app.Log().Info("devtools mounted",
		"prefix", prefix,
		"protected", opts.protected(),
		"basic_auth", basic != nil,
		"writable", opts.AllowWrite,
		"trace", opts.Trace != nil,
		"pprof", opts.Pprof)
	return nil
}

// normalizePrefix makes any of "_dev", "/_dev" and "/_dev/" mean the same thing.
func normalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return DefaultPrefix
	}
	p = "/" + strings.Trim(p, "/")
	if p == "/" {
		return DefaultPrefix
	}
	return p
}

// ui serves the page itself.
func (d *devtools) ui(c core.IHTTPContext) error {
	return c.Blob(http.StatusOK, echo.MIMETextHTMLCharsetUTF8, d.page)
}
