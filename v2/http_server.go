package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

// HTTPOptions configures the HTTP server.
type HTTPOptions struct {
	AllowOrigins []string
	AllowHeaders []string
	// DisableRequestLog turns off the per-request access line — for a service
	// behind a gateway that already logs every request, or one whose traffic is
	// health checks. Nil follows LOG_REQUEST (on by default); set it to override
	// the environment either way.
	//
	// Nothing else is affected: breadcrumbs, Sentry Logs, error reporting and
	// tracing are their own switches.
	DisableRequestLog *bool

	// BodyLimit caps how large a request body may be, in bytes. Zero takes
	// DefaultBodyLimit; a negative value turns the cap off entirely.
	//
	// Raise it on the routes that accept uploads rather than for the whole
	// server — the limit is ordinary middleware, so a group can carry its own:
	//
	//	files := e.Group("/files", core.BodyLimit(100<<20))
	BodyLimit int64

	// The four http.Server deadlines. Zero takes the framework default (see the
	// Default* constants); a negative value turns that deadline off.
	//
	// A BeforeServeFunc of your own still runs after these and wins, for the
	// settings this does not name.
	ReadTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// requestLogEnabled resolves whether the access line is written: the option
// wins, then LOG_REQUEST, then on.
func requestLogEnabled(env IENV, opts *HTTPOptions) bool {
	if opts != nil && opts.DisableRequestLog != nil {
		return !*opts.DisableRequestLog
	}
	// an unset bool field cannot be told apart from an explicit false, so a key
	// that defaults to true is read as a string first
	if env != nil && env.String("log_request") != "" {
		return env.Bool("log_request")
	}
	return true
}

// Server wraps Echo and remembers the App, so route registration takes the
// handler directly — no need to thread app through every call. It embeds
// *echo.Echo, so every Echo method (Use, Static, Pre, Logger, ...) is available.
type Server struct {
	*echo.Echo
	app *App

	// deadlines are the http.Server settings resolved from HTTPOptions at
	// construction, applied by Serve however the caller starts the listener.
	deadlines serverDeadlines

	// echo v5 dropped Echo.Start/Shutdown in favour of a StartConfig that owns
	// the listener and stops when its context is cancelled. These keep the
	// framework's own Shutdown(ctx) working on top of that.
	mu   sync.Mutex
	stop context.CancelFunc
	done chan error

	// handlers maps "METHOD /path" to the name of the function serving it,
	// captured as each route is registered. echo's RouteInfo carries only the
	// name a caller gave the route, and by the time anything asks "who serves
	// this?" the handler is a closure with no name of its own — so the answer
	// has to be taken here or not at all. Same value the access line reports.
	handlersMu sync.Mutex
	handlers   map[string]string
}

// Group is a route group that likewise remembers the App.
type Group struct {
	grp *echo.Group
	app *App
	// srv is the server the group belongs to, so routes registered through the
	// group are remembered in the same place as the server's own.
	srv *Server
}

// App returns the App the server was built on.
//
// The point of Server is that a route does not have to be handed the App; this
// is for the cases that still need it, chiefly middleware built at startup —
// authentication that resolves a token against the database, say. Without it a
// module's registration function has to take the App as a second parameter
// purely to pass it on, which is the threading Server exists to remove.
func (s *Server) App() *App { return s.app }

// App returns the App the group belongs to, as Server.App does.
func (g *Group) App() *App { return g.app }

// Use adds middleware to the group.
func (g *Group) Use(m ...echo.MiddlewareFunc) { g.grp.Use(m...) }

// Echo returns the underlying echo group (escape hatch).
func (g *Group) Echo() *echo.Group { return g.grp }

// NewHTTPServer builds a Server with the standard middleware stack (request-id,
// logger, recover, CORS) and an error handler that renders IError.
func NewHTTPServer(app *App, opts *HTTPOptions) *Server {
	if opts == nil {
		opts = &HTTPOptions{}
	}
	e := echo.New()
	// echo's own banner and "http(s) server started" line bypass the app logger:
	// no level, no colour, no JSON. StartHTTPServer silences them there and logs
	// the same fact properly.
	e.HTTPErrorHandler = httpErrorHandler(app)

	e.Use(middleware.RequestID())
	// put the id on the context itself, so every log line and every Sentry event
	// of the request carries it — whether or not error tracking is configured
	e.Use(requestIDContext())
	// sentry before recover: the hub must exist by the time a panic is caught,
	// and the transaction must span the handler *and* its recovery
	e.Use(sentryMiddleware(app))
	if requestLogEnabled(app.env, opts) {
		e.Use(requestLogger(app))
	}
	e.Use(recoverMiddleware(app))

	// v5 has no DefaultCORSConfig and refuses a config with no origin at all, so
	// the permissive default this framework has always had is spelled out here
	cors := middleware.CORSConfig{AllowOrigins: []string{"*"}}
	if len(opts.AllowOrigins) > 0 {
		cors.AllowOrigins = opts.AllowOrigins
	}
	if len(opts.AllowHeaders) > 0 {
		cors.AllowHeaders = opts.AllowHeaders
	}
	e.Use(middleware.CORSWithConfig(cors))

	// last, so it sits closest to the handler and its rejection is still logged
	// and traced like any other response
	if limit := resolveBodyLimit(opts.BodyLimit); limit > 0 {
		e.Use(BodyLimit(limit))
	}

	return &Server{
		Echo:      e,
		app:       app,
		deadlines: resolveDeadlines(opts),
		handlers:  map[string]string{},
	}
}

// ErrBodyTooLarge is wrapped by the error a request gets when its body exceeds
// the limit in force. Compare with errors.Is(err, core.ErrBodyTooLarge).
var ErrBodyTooLarge = errors.New("http: request body too large")

// bodyLimitContextKey is where the limit in force for the current request lives.
// It is read when the body is read, not when the middleware runs, which is what
// lets a route override a limit that was installed before it.
const bodyLimitContextKey = "core.body_limit"

// BodyLimit rejects a request whose body is larger than limit bytes with a 413
// REQUEST_TOO_LARGE. It checks the declared Content-Length and the bytes
// actually read, so a chunked upload that declares no size is stopped as it
// streams rather than after all of it has arrived.
//
// NewHTTPServer installs one for the whole server. Use this to give a route or a
// group a limit of its own — larger or smaller, because the limit is resolved
// when the body is read and the innermost one has set it by then:
//
//	e.POST("/avatars", h.Upload, core.BodyLimit(50<<20))
func BodyLimit(limit int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.Set(bodyLimitContextKey, limit)

			req := c.Request()
			if req.Body != nil {
				// only the outermost limit wraps: a second wrapper would count the
				// same bytes twice and reject a body that is within the limit
				if _, wrapped := req.Body.(*limitedBody); !wrapped {
					req.Body = &limitedBody{src: req.Body, c: c}
				}
			}

			err := next(c)
			// whoever read the body saw the failure first and may have labelled it
			// its own way — a binder calls a truncated body malformed JSON. The
			// cause survives, so the answer says what actually happened.
			if err != nil && errors.Is(err, ErrBodyTooLarge) {
				return bodyTooLarge(effectiveBodyLimit(c, limit))
			}
			return err
		}
	}
}

// checkDeclaredBodySize refuses a request whose Content-Length already exceeds
// the limit in force, without waiting for anybody to read the body. Call it once
// every middleware has run, so the limit it reads is the final one.
func checkDeclaredBodySize(c *echo.Context) IError {
	limit := effectiveBodyLimit(c, 0)
	if limit > 0 && c.Request().ContentLength > limit {
		return bodyTooLarge(limit)
	}
	return nil
}

// effectiveBodyLimit is the limit in force, falling back to the one the caller
// was constructed with.
func effectiveBodyLimit(c *echo.Context, fallback int64) int64 {
	if v, ok := c.Get(bodyLimitContextKey).(int64); ok {
		return v
	}
	return fallback
}

func bodyTooLarge(limit int64) IError {
	return Newf(http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE",
		"request body must not exceed %d bytes", limit).WithCause(ErrBodyTooLarge)
}

// limitedBody enforces the limit as the body is read. It reads the limit from
// the request rather than closing over it, so the value a route set after this
// wrapper was installed is the one that applies.
type limitedBody struct {
	src     io.ReadCloser
	c       *echo.Context
	read    int64
	checked bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	limit := effectiveBodyLimit(b.c, 0)
	if limit <= 0 {
		return b.src.Read(p)
	}
	// the declared length is worth checking once, so an oversized upload is
	// refused before any of it is read
	if !b.checked {
		b.checked = true
		if b.c.Request().ContentLength > limit {
			return 0, bodyTooLarge(limit)
		}
	}

	n, err := b.src.Read(p)
	b.read += int64(n)
	if b.read > limit {
		return n, bodyTooLarge(limit)
	}
	return n, err
}

func (b *limitedBody) Close() error { return b.src.Close() }

// Serve runs the server with a StartConfig of your own — a listener you already
// hold, TLS, a different graceful timeout — and blocks until ctx is cancelled or
// Shutdown is called. StartHTTPServer is this with the framework's defaults.
func (s *Server) Serve(ctx context.Context, cfg echo.StartConfig) error {
	cfg.BeforeServeFunc = s.deadlines.apply(cfg.BeforeServeFunc)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)

	s.mu.Lock()
	s.stop, s.done = cancel, done
	s.mu.Unlock()

	err := cfg.Start(ctx, s.Echo)
	done <- err
	close(done)
	return err
}

// Shutdown stops a running server and waits for in-flight requests to finish,
// or until ctx expires. It is a no-op when the server was never started, so the
// same shutdown sequence is safe in a service that only runs jobs.
//
// echo v5 removed Echo.Shutdown; this is the framework's replacement and keeps
// the call that services already make working.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.mu.Unlock()
	if stop == nil {
		return nil
	}
	stop()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --- Server route helpers (handler takes no app) ---

func (s *Server) GET(path string, h HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo {
	return s.remember(s.Echo.GET(path, WithHTTPContext(s.app, h), m...), h)
}
func (s *Server) POST(path string, h HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo {
	return s.remember(s.Echo.POST(path, WithHTTPContext(s.app, h), m...), h)
}
func (s *Server) PUT(path string, h HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo {
	return s.remember(s.Echo.PUT(path, WithHTTPContext(s.app, h), m...), h)
}
func (s *Server) PATCH(path string, h HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo {
	return s.remember(s.Echo.PATCH(path, WithHTTPContext(s.app, h), m...), h)
}
func (s *Server) DELETE(path string, h HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo {
	return s.remember(s.Echo.DELETE(path, WithHTTPContext(s.app, h), m...), h)
}

// remember records which function serves a route. It takes the RouteInfo echo
// returned rather than the path that was registered, so a route inside a group
// is recorded under its full path without this having to know the prefix.
func (s *Server) remember(ri echo.RouteInfo, h HandlerFunc) echo.RouteInfo {
	if s == nil || s.handlers == nil {
		return ri
	}
	name := handlerName(h)
	if name == "" {
		return ri
	}
	s.handlersMu.Lock()
	defer s.handlersMu.Unlock()
	s.handlers[ri.Method+" "+ri.Path] = name
	return ri
}

// HandlerNames maps "METHOD /path" to the name of the function serving it, for
// every route registered through the framework's own helpers — what the devtools
// route list shows, and what an operator asks when a path answers something
// unexpected. Routes registered straight on the embedded Echo are absent: their
// handler was never an mine-core HandlerFunc.
func (s *Server) HandlerNames() map[string]string {
	s.handlersMu.Lock()
	defer s.handlersMu.Unlock()
	out := make(map[string]string, len(s.handlers))
	for k, v := range s.handlers {
		out[k] = v
	}
	return out
}

// Group creates a route group that also carries the App.
func (s *Server) Group(prefix string, m ...echo.MiddlewareFunc) *Group {
	return &Group{grp: s.Echo.Group(prefix, m...), app: s.app, srv: s}
}

// --- Group route helpers (handler takes no app) ---

func (g *Group) GET(path string, h HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo {
	return g.srv.remember(g.grp.GET(path, WithHTTPContext(g.app, h), m...), h)
}
func (g *Group) POST(path string, h HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo {
	return g.srv.remember(g.grp.POST(path, WithHTTPContext(g.app, h), m...), h)
}
func (g *Group) PUT(path string, h HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo {
	return g.srv.remember(g.grp.PUT(path, WithHTTPContext(g.app, h), m...), h)
}
func (g *Group) PATCH(path string, h HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo {
	return g.srv.remember(g.grp.PATCH(path, WithHTTPContext(g.app, h), m...), h)
}
func (g *Group) DELETE(path string, h HandlerFunc, m ...echo.MiddlewareFunc) echo.RouteInfo {
	return g.srv.remember(g.grp.DELETE(path, WithHTTPContext(g.app, h), m...), h)
}

// Group creates a nested group.
func (g *Group) Group(prefix string, m ...echo.MiddlewareFunc) *Group {
	return &Group{grp: g.grp.Group(prefix, m...), app: g.app, srv: g.srv}
}

// requestIDContext copies the request id echo generated onto the request's
// context, where ctx.Log() and the error tracker can find it.
func requestIDContext() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			id := requestIDOf(c)
			if id == "" {
				return next(c)
			}
			c.SetRequest(c.Request().WithContext(withRequestID(c.Request().Context(), id)))
			return next(c)
		}
	}
}

// requestLogger logs each request with method/status/latency and the request id.
func requestLogger(app *App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			start := time.Now()
			err := next(c)

			// A handler that returns an error has written nothing yet: echo runs
			// the error handler after this middleware unwinds, so
			// c.Response().Status is still the untouched default 200. Read the
			// status from the error instead, or every failed request is logged
			// as a success and the ones worth finding become invisible.
			status := writtenStatus(c)
			latency := time.Since(start)
			fields := []any{
				"method", c.Request().Method,
				"path", c.Request().URL.Path,
				"latency_ms", latency.Milliseconds(),
				"request_id", c.Response().Header().Get(echo.HeaderXRequestID),
			}
			// which handler served it. The line's own source is this file and is
			// the same for every request ever logged; this is the part of "where
			// did this come from" that an access line can actually answer, since
			// the handler's frame is gone by the time it is written.
			if name, ok := c.Get(handlerContextKey).(string); ok && name != "" {
				fields = append(fields, "handler", name)
			}
			code := ""
			if err != nil && !responseCommitted(c) {
				ie := toIError(err, app)
				status = ie.GetStatus()
				code = ie.GetCode()
				fields = append(fields, "code", code)
			}
			fields = append([]any{"status", status}, fields...)

			// The message has to say what happened on its own. A JSON pipeline
			// shows it as the headline and Sentry Logs shows nothing else until
			// the line is opened, and "request" answers none of the questions you
			// are scanning for: which endpoint, did it work, how slow was it.
			// The same values stay in the fields, which is what you filter on.
			msg := fmt.Sprintf("%s %s %d %dms",
				c.Request().Method, c.Request().URL.Path, status, latency.Milliseconds())
			if code != "" {
				msg += " " + code
			}

			// Bound to the request, so the access line shares its trace and its
			// breadcrumbs with everything the handler logged — read on its own it
			// would be a line about a request nobody can find. Captures stay off:
			// the failure it describes is reported by the layer that has the
			// stack and the scope for it.
			log := boundTo(app.Log(), withoutCapture(c.Request().Context()))

			// The line's level should match what happened: a 5xx is not routine.
			switch {
			case status >= 500:
				log.Error(msg, fields...)
			case status >= 400:
				log.Warn(msg, fields...)
			default:
				log.Info(msg, fields...)
			}
			return err
		}
	}
}

// recoverMiddleware turns a panic into a 500 IError instead of crashing, and
// reports it to Sentry at fatal level with the request's own scope — a panic is
// the one failure that never passes through ctx.NewError.
func recoverMiddleware(app *App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) (err error) {
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				if r == http.ErrAbortHandler {
					panic(r) // the server's own signal, not an application error
				}
				// same trace as the request that panicked; the panic itself is
				// reported below, with a stack, so this line is not an event
				boundTo(app.Log(), withoutCapture(c.Request().Context())).
					Error("panic recovered", "panic", r, "path", c.Request().URL.Path)
				fail := InternalServerError()
				// carry the panic value as the cause so a dev response shows the
				// panic text; outside dev the generic 500 message stands
				if perr, ok := r.(error); ok {
					fail = fail.WithCause(perr)
				} else {
					fail = fail.WithCause(fmt.Errorf("%v", r))
				}
				fail.eventID = app.Sentry().
					WithContext(c.Request().Context()).
					Recover(r, CaptureTag("http.route", routeOf(c)))
				if fail.eventID != "" {
					c.Response().Header().Set(sentryHeaderEventID, fail.eventID)
				}
				err = fail
			}()
			return next(c)
		}
	}
}

// httpErrorHandler renders any error as the framework's JSON error body.
func httpErrorHandler(app *App) echo.HTTPErrorHandler {
	return func(c *echo.Context, err error) {
		if responseCommitted(c) {
			return
		}
		ie := toIError(err, app)
		if app.env.IsDev() {
			ie = withDevMessage(ie)
		}
		if writeErr := c.JSON(ie.GetStatus(), ie.JSON()); writeErr != nil {
			app.Log().Error("failed writing error response", "err", writeErr)
		}
	}
}

// withDevMessage replaces the rendered message with the underlying failure, so
// a dev response says what actually broke instead of the label the framework
// put on it. Only errors carrying a cause are touched — validation errors and
// sentinels keep their own wording — and the caller gates this on dev, so no
// internal detail reaches a real client.
func withDevMessage(ie IError) IError {
	e, ok := ie.(*Error)
	if !ok || e.cause == nil {
		return ie
	}
	msg := devMessage(e.cause)
	if msg == "" {
		return ie
	}
	return e.WithMessage(msg)
}

func toIError(err error, app *App) IError {
	if ie, ok := err.(IError); ok {
		return ie
	}
	var he *echo.HTTPError
	if errors.As(err, &he) {
		msg := http.StatusText(he.Code)
		if he.Message != "" {
			msg = he.Message
		}
		return New(he.Code, "HTTP_ERROR", msg)
	}
	// echo v5 reports routing failures (no such route, wrong method) with its own
	// unexported sentinels — echo.ErrNotFound and friends are *not*
	// *echo.HTTPError any more. Without this a request for a URL that does not
	// exist is answered "500 internal server error", which is both wrong and
	// alarming. Everything echo considers an HTTP failure answers StatusCode.
	var status interface{ StatusCode() int }
	if errors.As(err, &status) {
		code := status.StatusCode()
		return New(code, "HTTP_ERROR", http.StatusText(code))
	}
	return From(err)
}

// ShutdownSignals is what a running service is asked to stop with.
//
// SIGTERM is the one that matters in production: it is what `docker stop`,
// Kubernetes and systemd send, and a process that does not listen for it is
// killed outright after the grace period — in-flight requests included. SIGINT
// is Ctrl-C, which is the same event from a terminal.
var ShutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// DefaultGracefulTimeout bounds how long in-flight requests have to finish once
// a shutdown signal arrives.
const DefaultGracefulTimeout = 10 * time.Second

// StartHTTPServer runs the server and blocks.
//
// Outside dev it drains on SIGINT/SIGTERM — new connections refused, in-flight
// requests given DefaultGracefulTimeout to finish — and then closes the App's
// pools, in that order, because a pool closed while a request still holds it
// turns a clean shutdown into a burst of 500s.
//
// It is the HTTP-only starter. A process that also runs jobs owns its own
// shutdown, so that the scheduler stops between the two steps above: see
// [Lifecycle & roles](./docs/lifecycle.md).
func StartHTTPServer(e *Server, env IENV) {
	log := e.app.Log()
	e.app.LogCapabilities()

	if env.IsDev() {
		// No draining and no pool close in dev: a reload should be immediate, and
		// the process is about to exit anyway.
		addr, cfg := serveConfig(env)
		log.Info("http server started", "addr", addr)

		if err := e.Serve(context.Background(), cfg); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server stopped", "err", err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), ShutdownSignals...)
	defer stop()

	if err := serveUntil(e, env, ctx, stop); err != nil {
		os.Exit(1)
	}
}

// serveUntil serves until ctx is cancelled, then drains and closes the pools.
//
// Split out of StartHTTPServer so the shutdown path is testable: delivering a
// real signal to the test process is not portable, cancelling a context is.
// stop restores default signal handling and is called as soon as the drain
// begins — a second Ctrl-C should kill a server that is taking too long rather
// than be swallowed by the handler that is already shutting down.
func serveUntil(e *Server, env IENV, ctx context.Context, stop func()) error {
	log := e.app.Log()

	addr, cfg := serveConfig(env)
	cfg.GracefulTimeout = DefaultGracefulTimeout
	cfg.OnShutdownError = func(err error) { log.Error("http server shutdown", "err", err) }

	// echo's startup banner is off so that every line comes from one logger with
	// one format; this is the line it used to print.
	log.Info("http server started", "addr", addr)

	err := e.Serve(ctx, cfg)
	stop()

	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server stopped", "err", err)
		e.closeApp(log)

		return err
	}

	log.Info("http server stopped")
	e.closeApp(log)

	return nil
}

// serveConfig is the listener the process was configured with.
//
// echo v5 moved server settings out of the Echo instance: one StartConfig
// describes the listener, the banner and the shutdown, and Serve blocks until
// its context is cancelled — so the signal handling above *is* the graceful
// shutdown.
func serveConfig(env IENV) (string, echo.StartConfig) {
	addr := env.Config().Host
	if addr == "" {
		addr = ":8080"
	}

	return addr, echo.StartConfig{Address: addr, HideBanner: true, HidePort: true}
}

// closeApp releases the pools once nothing can still be using them. Shutdown is
// idempotent, so a caller that also defers it loses nothing.
func (s *Server) closeApp(log ILogger) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultGracefulTimeout)
	defer cancel()

	if err := s.app.Shutdown(ctx); err != nil {
		log.Error("app shutdown", "err", err)
	}
}

// DefaultReadTimeout is how long a client has to send its request, headers and
// body included.
//
// echo v5 sets 30 seconds, which is a fine number for a JSON API and a wrong one
// for the rest of what services do: a file upload on a phone connection, a
// long-poll, an SSE stream. Those would be cut off mid-request with no error
// worth reading. Five minutes still closes the slow-loris hole echo's default
// exists for, while leaving normal work alone.
//
// Override it per server with your own BeforeServeFunc — it runs after this one,
// so it wins:
//
//	e.Serve(ctx, echo.StartConfig{
//	    Address: ":8080",
//	    BeforeServeFunc: func(srv *http.Server) error {
//	        srv.ReadTimeout = 30 * time.Second
//	        return nil
//	    },
//	})
const DefaultReadTimeout = 5 * time.Minute

// DefaultReadHeaderTimeout is how long a client has to send its request line and
// headers.
//
// This is the deadline that closes the slow-loris hole properly. ReadTimeout has
// to be generous because a legitimate upload takes minutes; headers never do, so
// a connection that has not finished them in twenty seconds is not a client with
// a poor connection, it is a connection held open to occupy a worker.
const DefaultReadHeaderTimeout = 20 * time.Second

// DefaultIdleTimeout is how long an idle keep-alive connection is kept.
//
// Without it a connection that has finished its request stays open until the
// client drops it, and a load balancer that opens connections faster than it
// closes them slowly exhausts the file descriptors of the process.
const DefaultIdleTimeout = 120 * time.Second

// DefaultBodyLimit is how large a request body may be before it is rejected with
// 413 REQUEST_TOO_LARGE.
//
// Without a limit the body is read into memory by whatever binds it, so the
// largest request anybody sends is the memory footprint of the process — one
// client can end the service for everyone. Ten megabytes is far above any JSON
// payload; routes that take uploads set their own (see BodyLimit).
const DefaultBodyLimit int64 = 10 << 20

// There is deliberately no default WriteTimeout. It is an absolute deadline on
// the whole exchange, so any value large enough for a slow download is too large
// to protect anything, and any value small enough to protect something cuts off
// the SSE stream and the long-poll that DefaultReadTimeout was widened for. Set
// HTTPOptions.WriteTimeout on a server that serves neither.

// serverDeadlines are the resolved http.Server settings of one Server.
type serverDeadlines struct {
	read       time.Duration
	readHeader time.Duration
	write      time.Duration
	idle       time.Duration
}

// resolveDeadlines reads the options: zero takes the framework default, negative
// turns the deadline off. Without the negative case a caller could not say "no
// read timeout", because the zero value already means "give me the default".
func resolveDeadlines(opts *HTTPOptions) serverDeadlines {
	pick := func(v, def time.Duration) time.Duration {
		switch {
		case v > 0:
			return v
		case v < 0:
			return 0
		default:
			return def
		}
	}
	return serverDeadlines{
		read:       pick(opts.ReadTimeout, DefaultReadTimeout),
		readHeader: pick(opts.ReadHeaderTimeout, DefaultReadHeaderTimeout),
		write:      pick(opts.WriteTimeout, 0),
		idle:       pick(opts.IdleTimeout, DefaultIdleTimeout),
	}
}

// resolveBodyLimit reads the option the same way: zero is the default, negative
// is off.
func resolveBodyLimit(limit int64) int64 {
	switch {
	case limit > 0:
		return limit
	case limit < 0:
		return 0
	default:
		return DefaultBodyLimit
	}
}

// apply installs the framework's http.Server settings, then hands the server to
// whatever hook the caller supplied — so the caller always has the last word.
func (d serverDeadlines) apply(next func(*http.Server) error) func(*http.Server) error {
	return func(srv *http.Server) error {
		srv.ReadTimeout = d.read
		srv.ReadHeaderTimeout = d.readHeader
		srv.WriteTimeout = d.write
		srv.IdleTimeout = d.idle
		if next != nil {
			return next(srv)
		}
		return nil
	}
}

// writtenStatus is the status code actually written to the wire, read from
// echo's response recorder — v5's Context.Response() hands out the bare
// http.ResponseWriter, with the recorder underneath it.
func writtenStatus(c *echo.Context) int {
	if r := echoResponse(c); r != nil {
		return r.Status
	}
	return http.StatusOK
}

// responseCommitted reports whether the response has already been written, so
// no layer tries to write a second one over it.
func responseCommitted(c *echo.Context) bool {
	r := echoResponse(c)
	return r != nil && r.Committed
}

func echoResponse(c *echo.Context) *echo.Response {
	r, err := echo.UnwrapResponse(c.Response())
	if err != nil {
		return nil
	}
	return r
}
