package core

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
)

// IRequester is the HTTP client. Same name as v1; backed by go-resty and reused
// across requests.
//
// You build requests with the full go-resty API via R() (headers, query, form,
// files, auth, output streaming, tracing, …) and run them with Send, which is
// the only thing the framework wraps: it binds the request context and converts
// every failure — transport error or non-2xx status — into a core.IError.
//
//	var out UserDTO
//	resp, err := r.Send(r.R().SetHeader("X-Token", tok).SetResult(&out), http.MethodGet, url)
//	if err != nil {
//	    return err // IError, ready to return from a handler
//	}
//
// Add SetError(&failure) when the upstream's error body carries more than a code
// and a message — the IError is returned either way, and the struct holds
// whatever the caller has to branch on.
type IRequester interface {
	// R returns a *resty.Request bound to the context — configure it with the
	// full go-resty API, then run it with Send.
	R() *resty.Request
	// Send executes a request built via R(), returning a core.IError on a
	// transport failure (code NETWORK_ERROR, 500) or a non-2xx status (the
	// remote error code/message are reused when the body carries them).
	Send(req *resty.Request, method, url string) (*resty.Response, IError)
	// WithContext returns a copy bound to a different context (escape hatch).
	WithContext(ctx context.Context) IRequester
	// Resty returns the shared client for startup-time configuration (retries,
	// TLS, base URL, middleware). Configure it once, not per request.
	Resty() *resty.Client
}

type requester struct {
	ctx context.Context
	rc  *resty.Client
	log ILogger
}

var _ IRequester = (*requester)(nil)

// Requester returns the application's HTTP client bound to ctx.
//
// It is a function rather than a context method because an outgoing call is not
// a capability of the request — it is a thing you do, with the request's
// deadline and trace attached:
//
//	resp, err := core.Requester(c).Send(req, http.MethodGet, url)
//
// ctx is anything that carries the App — an IContext from a handler or a job, or
// any context.Context derived from one, so a function that took only a
// context.Context can still make the call. Every context an App built carries
// it, which is what keeps the client, its connection pool and its Sentry
// instrumentation the ones configured at startup.
//
// A context from nowhere — context.Background() in a script or an early test —
// gets a plain client with the default timeout instead of nil, so a call site
// never has to nil-check. That client is not instrumented: with no App there is
// no configuration and no tracker to report to.
func Requester(ctx context.Context) IRequester {
	if app := appFrom(ctx); app != nil && app.requester != nil {
		return app.requester.WithContext(ctx)
	}
	return fallbackRequester().WithContext(ctx)
}

// fallbackRequester is built once and shared, so repeated calls outside an App
// still reuse one connection pool rather than leaking one per call.
func fallbackRequester() IRequester {
	fallbackOnce.Do(func() { fallbackClient = NewRequester(nil, nil) })
	return fallbackClient
}

var (
	fallbackOnce   sync.Once
	fallbackClient IRequester
)

// NewRequester builds the shared HTTP client (reused, pool kept alive). env is
// accepted for symmetry with the other constructors and may be nil.
func NewRequester(env IENV, log ILogger) IRequester {
	rc := resty.New().SetTimeout(30 * time.Second)
	return &requester{ctx: context.Background(), rc: rc, log: log}
}

// NewRequesterWithClient wraps a fully-configured *resty.Client — use this to
// enable any client-level go-resty feature (retries, middleware, TLS config,
// base URL, proxy, hooks) and wire it via core.WithRequester(...).
func NewRequesterWithClient(rc *resty.Client) IRequester {
	return &requester{ctx: context.Background(), rc: rc}
}

func (r *requester) WithContext(ctx context.Context) IRequester {
	cp := *r
	cp.ctx = ctx
	return &cp
}

func (r *requester) R() *resty.Request { return r.rc.R().SetContext(r.ctx) }

func (r *requester) Resty() *resty.Client { return r.rc }

func (r *requester) Send(req *resty.Request, method, url string) (*resty.Response, IError) {
	resp, err := req.Execute(method, url)
	if err != nil {
		return resp, Wrap(err, "requester").
			WithCode("NETWORK_ERROR").
			WithStatus(http.StatusInternalServerError)
	}
	if resp.IsError() {
		return resp, restyError(resp)
	}
	return resp, nil
}

// restyError maps a non-2xx response to an IError, reusing the remote error
// code/message when the JSON body provides them.
func restyError(resp *resty.Response) IError {
	code := "HTTP_ERROR"
	message := http.StatusText(resp.StatusCode())
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(resp.Body(), &body) == nil {
		if body.Code != "" {
			code = body.Code
		}
		if body.Message != "" {
			message = body.Message
		}
	}
	return New(resp.StatusCode(), code, message)
}
