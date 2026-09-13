package main

import (
	"context"
	"net/http"
	"time"

	"github.com/go-resty/resty/v2"

	core "github.com/pskclub/mine-core/v2"
)

// errPaymentUnavailable is one of our own error values, not the upstream's.
// Reusable values like this belong in the service's own errmsgs package: the
// framework only ships the generic ones.
var errPaymentUnavailable = core.New(
	http.StatusServiceUnavailable, "PAYMENT_UNAVAILABLE", "payment provider is unavailable")

// --- Example 1: calling somebody else's API ---------------------------------
//
// core.Requester(ctx), not ctx.Requester(). An outgoing call is not a capability
// the request *has* — it is something the code goes and does, carrying that
// request's deadline and trace. Taking only a context.Context is what lets a
// domain service deep in the call stack make one without changing its signature
// to core.IContext.
//
// The API is deliberately thin: build the request with the full go-resty API via
// R(), run it with Send. Send is the only thing the framework wraps — it binds
// the context and turns every failure, transport error or non-2xx status, into a
// core.IError you can return straight from a handler.

type Rate struct {
	Pair  string  `json:"pair"`
	Value float64 `json:"value"`
}

// fetchRate takes a plain context.Context and still gets the client configured at
// startup: same pool, same middleware, same instrumentation. A context that came
// from nowhere (context.Background() in a script) gets a plain default client
// rather than nil, so the call site never has to nil-check — but that one is not
// instrumented, because there is no App to ask how it was configured.
func fetchRate(ctx context.Context, baseURL, pair string) (*Rate, core.IError) {
	r := core.Requester(ctx)

	var out Rate
	if _, err := r.Send(
		r.R().SetQueryParam("pair", pair).SetResult(&out),
		http.MethodGet, baseURL+"/v1/rates",
	); err != nil {
		return nil, err
	}
	return &out, nil
}

// chargeError is the upstream's error body — the fields their {code, message}
// does not carry.
type chargeError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	DeclineCode string `json:"decline_code"`
	Retryable   bool   `json:"retryable"`
}

// charge shows SetResult and SetError together. SetResult binds the success
// body, SetError the failure one; resty decodes whichever applies.
//
// Most of the time SetError is unnecessary: an upstream that answers with
// {code, message} is already translated by Send. Reach for it when you need a
// field to *decide* something — whether to retry, what to tell the user, which
// of your own error codes this maps to.
func charge(ctx context.Context, baseURL string, amount int64) core.IError {
	r := core.Requester(ctx)

	var out struct {
		ID string `json:"id"`
	}
	var fail chargeError

	_, err := r.Send(
		r.R().
			SetBody(map[string]any{"amount": amount}).
			SetResult(&out).
			SetError(&fail),
		http.MethodPost, baseURL+"/v1/charges",
	)
	if err == nil {
		return nil
	}

	// err is already an IError carrying the upstream's status and code; fail is
	// the detail that made the branch possible. Note that `out` is untouched on
	// failure — reading it would give zero values, not half a charge.
	if fail.Retryable {
		return errPaymentUnavailable
	}
	// never hand an upstream's raw wording to our own users
	return core.Wrap(err, "charge declined: "+fail.DeclineCode).
		WithStatus(http.StatusPaymentRequired).
		WithCode("CARD_DECLINED")
}

// callWithDeadline bounds one call without touching the shared client's timeout.
//
// Derive from the request context, never from context.Background(): background
// loses the App (so you get an unconfigured, uninstrumented client) and, worse,
// detaches the call from the request's cancellation — a request the client
// abandoned would keep holding the upstream open for the full timeout. Deriving
// also means the shorter deadline always wins, so a request with two seconds
// left does not wait five for this.
func callWithDeadline(parent core.IContext, url string) core.IError {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()

	r := core.Requester(ctx)
	_, err := r.Send(r.R(), http.MethodGet, url)
	return err
}

// bootRequester is where client-level configuration belongs: once, at startup,
// not rebuilt per request.
//
// The retry condition is the part worth reading. Retrying is safe for a request
// the upstream can receive twice — GET, PUT, DELETE — and unsafe for a POST that
// creates something, unless the upstream honours an idempotency key. A blanket
// SetRetryCount(3) on a payments client is how one charge becomes three.
func bootRequester(baseURL string) core.IRequester {
	rc := resty.New().
		// per-destination, not a global 30s: an API that normally answers in
		// 200ms should not be given thirty seconds before we give up on it
		SetTimeout(5 * time.Second).
		SetBaseURL(baseURL).
		SetRetryCount(3).
		SetRetryWaitTime(200 * time.Millisecond).
		SetRetryMaxWaitTime(2 * time.Second).
		AddRetryCondition(func(resp *resty.Response, err error) bool {
			if resp == nil || resp.Request == nil {
				return false // transport failure: safety depends on the method
			}
			switch resp.Request.Method {
			case http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodHead:
			default:
				return false
			}
			return resp.StatusCode() == http.StatusTooManyRequests || resp.StatusCode() >= 500
		})

	return core.NewRequesterWithClient(rc)
}

// uploadRequester is a second client for a different job. SetTimeout is a
// ceiling on the *whole* request including sending the body, so the 30-second
// default cuts off a large file on a slow connection — and a longer context does
// not help, because whichever deadline expires first wins.
func uploadRequester() core.IRequester {
	return core.NewRequesterWithClient(resty.New().SetTimeout(10 * time.Minute))
}
