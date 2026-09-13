package core

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/getsentry/sentry-go"
	"github.com/labstack/echo/v5"
)

// sentryHeaderEventID is returned on failed responses so a support ticket can be
// matched to the Sentry issue without searching.
const sentryHeaderEventID = "X-Sentry-Id"

// sentryMiddleware gives every request its own Sentry hub, so scope (user, tags,
// breadcrumbs, request) belongs to that request and never bleeds into a
// concurrent one — the bug v1's global ConfigureScope had.
//
// It also opens a transaction when tracing is on, and reports any error the
// handler returns that is severe enough (see SentryOptions.MinStatus). Errors
// already captured on their way out of ctx.NewError are not reported twice.
func sentryMiddleware(app *App) echo.MiddlewareFunc {
	tracker := app.Sentry()
	if tracker == nil || !tracker.Enabled() {
		return func(next echo.HandlerFunc) echo.HandlerFunc { return next }
	}
	cfg := sentryOf(tracker)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			ctx, hub := withHub(req.Context(), tracker)
			if hub == nil {
				return next(c)
			}

			// Continue the caller's trace when it sent one, whether or not this
			// service records transactions of its own. Tracing decides whether we
			// time the request; it does not decide whether this request belongs to
			// the trace the caller already started, and the log lines are only
			// worth lining up with the caller's if it does.
			if pc, perr := sentry.PropagationContextFromHeaders(
				req.Header.Get(sentry.SentryTraceHeader),
				req.Header.Get(sentry.SentryBaggageHeader),
			); perr == nil {
				hub.Scope().SetPropagationContext(pc)
			}

			hub.Scope().SetRequest(req)
			hub.Scope().SetTag("http.method", req.Method)
			rid := requestIDOf(c)
			if rid != "" {
				hub.Scope().SetTag("request_id", rid)
			}
			// tags reach events; attributes reach logs and metrics. Pin both so a
			// log line is filterable by the request it belongs to even when that
			// request never fails.
			pinAttributes(hub, map[string]string{
				"mode":        ModeHTTP.String(),
				"request_id":  rid,
				"http.method": req.Method,
				"http.route":  routeOf(c),
			})
			attachRequestDetails(hub, req, recordBody(c, cfg))

			span := startRequestTransaction(ctx, hub, c, cfg)
			// A transaction with no incoming trace to continue invents its own
			// trace id and does not tell the scope about it, so anything that
			// resolves a trace from the scope rather than from the span — a log
			// line written where the span is not on the context — would report a
			// different trace for the same request.
			alignScopeWithSpan(hub, span)
			ctx = span.Context()
			c.SetRequest(req.WithContext(ctx))

			err := next(c)

			status := responseStatus(c, err)
			hub.Scope().SetTag("http.status", statusTag(status))
			span.SetTag("http.status_code", statusTag(status))
			span.Finish(errorForSpan(err, status))

			if id := captureHandlerError(tracker.WithContext(ctx), c, err, cfg); id != "" {
				c.Response().Header().Set(sentryHeaderEventID, id)
			}
			return err
		}
	}
}

// startRequestTransaction opens the performance transaction for a request,
// continuing an incoming distributed trace when the caller sent one.
func startRequestTransaction(ctx context.Context, hub *sentry.Hub, c *echo.Context, cfg sentryConfig) ISpan {
	if !cfg.tracing {
		return noopSpan{ctx: ctx}
	}
	req := c.Request()
	name := req.Method + " " + routeOf(c)
	span := sentry.StartTransaction(ctx, name,
		sentry.ContinueTrace(hub, req.Header.Get(sentry.SentryTraceHeader), req.Header.Get(sentry.SentryBaggageHeader)),
		sentry.WithOpName("http.server"),
		sentry.WithTransactionSource(sentry.SourceRoute),
	)
	if span == nil {
		return noopSpan{ctx: ctx}
	}
	return &tracedSpan{span: span}
}

// alignScopeWithSpan points the hub's propagation context at the span's trace,
// so one unit of work reports one trace id however a given line arrives at it.
func alignScopeWithSpan(hub *sentry.Hub, span ISpan) {
	s := span.Sentry()
	if hub == nil || s == nil {
		return
	}
	pc := sentry.PropagationContext{
		TraceID:      s.TraceID,
		SpanID:       s.SpanID,
		ParentSpanID: s.ParentSpanID,
	}
	// the sampling decision travels with the span itself (ToBaggage), so it is
	// not lost by leaving it off the scope
	if dsc, err := sentry.DynamicSamplingContextFromHeader([]byte(s.ToBaggage())); err == nil {
		pc.DynamicSamplingContext = dsc
	}
	hub.Scope().SetPropagationContext(pc)
}

// captureHandlerError reports the error a handler returned, unless it was
// already reported (ctx.NewError captures 500s at the point they are built,
// where the most context is available) or is below the reporting threshold.
func captureHandlerError(tracker ISentry, c *echo.Context, err error, cfg sentryConfig) string {
	if err == nil {
		return ""
	}
	if e := From(err); e != nil && e.eventID != "" {
		return e.eventID
	}
	if !captureStatus(err, cfg.minStatus) {
		return ""
	}
	return tracker.CaptureError(err,
		CaptureTag("http.method", c.Request().Method),
		CaptureTag("http.route", routeOf(c)),
		CaptureTag("http.status", statusTag(responseStatus(c, err))),
	)
}

// bindSentryScope pins the context's principal and scoped data on the hub of
// the unit of work, so an event carries them whichever layer reported it — the
// handler through ctx.NewError, the middleware, a panic, or a log line. Before
// this, only captures that went through an IContext had them.
//
// The data is read at send time, not now, so values set later in the handler
// still make it onto the event.
func bindSentryScope(ctx context.Context, ic IContext, ip string) {
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil || ic == nil {
		return
	}
	if user := sentryUserOf(ic.GetUser(), ip); user != nil {
		hub.Scope().SetUser(*user)
	}
	hub.Scope().AddEventProcessor(func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
		data := ic.GetAllData()
		if len(data) == 0 {
			return event
		}
		if event.Contexts == nil {
			event.Contexts = map[string]sentry.Context{}
		}
		if _, ok := event.Contexts["context-data"]; !ok {
			event.Contexts["context-data"] = data // scrubbed in BeforeSend
		}
		return event
	})
}

// routeOf prefers the registered route pattern ("/users/:id") over the raw path,
// so one endpoint is one transaction instead of one per id.
func routeOf(c *echo.Context) string {
	if p := c.Path(); p != "" {
		return p
	}
	return c.Request().URL.Path
}

// responseStatus is the status the client will see: the error's when the
// handler returned one, otherwise what was written.
func responseStatus(c *echo.Context, err error) int {
	if err != nil {
		if status, ok := statusOf(err); ok {
			return status
		}
	}
	return writtenStatus(c)
}

// errorForSpan decides whether the transaction counts as failed, by the same
// single rule the rest of the framework uses: status ≥ 500. A 404 or a rejected
// payload is the request being handled correctly, so marking those transactions
// "internal error" would put the failure rate of a working service at whatever
// share of its traffic is people asking for the wrong thing.
//
// It reports a failure even when the handler wrote the status itself instead of
// returning an error.
func errorForSpan(err error, status int) error {
	if status > 0 && status < http.StatusInternalServerError {
		return nil
	}
	if err != nil {
		return err
	}
	if status >= http.StatusInternalServerError {
		return Newf(status, "HTTP_ERROR", "request failed with status %d", status)
	}
	return nil
}

func requestIDOf(c *echo.Context) string {
	if id := c.Response().Header().Get(echo.HeaderXRequestID); id != "" {
		return id
	}
	return c.Request().Header.Get(echo.HeaderXRequestID)
}

// ---------------------------------------------------------------------------
// Request body
// ---------------------------------------------------------------------------

// bodyRecorder tees the request body as the handler reads it, keeping at most
// limit bytes. Nothing is read that the handler would not have read anyway, so
// an unread body costs nothing and a 2 GB upload costs `limit`.
type bodyRecorder struct {
	rc    io.ReadCloser
	limit int

	mu  sync.Mutex
	buf []byte
}

func (b *bodyRecorder) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.mu.Lock()
		if room := b.limit - len(b.buf); room > 0 {
			if n < room {
				room = n
			}
			b.buf = append(b.buf, p[:room]...)
		}
		b.mu.Unlock()
	}
	return n, err
}

func (b *bodyRecorder) Close() error { return b.rc.Close() }

func (b *bodyRecorder) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}

// recordBody wraps the request body so it can be attached to an event later.
// Binary and multipart payloads are skipped — a truncated upload tells nobody
// anything.
func recordBody(c *echo.Context, cfg sentryConfig) *bodyRecorder {
	if !cfg.captureBody {
		return nil
	}
	req := c.Request()
	if req.Body == nil || req.Body == http.NoBody {
		return nil
	}
	switch ctype := req.Header.Get(echo.HeaderContentType); {
	case ctype == "",
		strings.HasPrefix(ctype, echo.MIMEApplicationJSON),
		strings.HasPrefix(ctype, echo.MIMEApplicationForm),
		strings.HasPrefix(ctype, echo.MIMEApplicationXML),
		strings.HasPrefix(ctype, echo.MIMETextXML),
		strings.HasPrefix(ctype, echo.MIMETextPlain):
	default:
		return nil
	}
	rec := &bodyRecorder{rc: req.Body, limit: cfg.maxBodyBytes}
	req.Body = rec
	return rec
}

// attachRequestDetails fills in what the SDK leaves out: the request headers
// (it drops them unless PII is enabled globally, which is not something a
// library should decide) and the body read so far.
//
// Both are added at send time, so the body holds whatever the handler had
// actually read when it failed — and both are masked afterwards, in BeforeSend,
// which is why sending them is safe.
func attachRequestDetails(hub *sentry.Hub, req *http.Request, rec *bodyRecorder) {
	headers := make(map[string]string, len(req.Header)+1)
	for k, v := range req.Header {
		headers[k] = strings.Join(v, ",")
	}
	headers["Host"] = req.Host

	hub.Scope().AddEventProcessor(func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
		if event.Request == nil {
			return event
		}
		if event.Request.Headers == nil {
			event.Request.Headers = map[string]string{}
		}
		for k, v := range headers {
			if _, ok := event.Request.Headers[k]; !ok {
				event.Request.Headers[k] = v
			}
		}
		if rec != nil && event.Request.Data == "" {
			if body := rec.bytes(); len(body) > 0 {
				event.Request.Data = string(body)
			}
		}
		return event
	})
}
