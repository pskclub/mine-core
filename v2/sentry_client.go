package core

import (
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/go-resty/resty/v2"
)

// instrumentRequester makes every outgoing HTTP call leave a breadcrumb on the
// calling request's trail, and opens a child span when tracing is on. That is
// how an issue answers "what did we ask the payment gateway, and what did it
// say" without anyone having to log it by hand.
//
// It also propagates the trace headers, so a downstream service that reports to
// the same Sentry org shows up inside the same trace.
func instrumentRequester(r IRequester, tracker ISentry) {
	if r == nil || tracker == nil || !tracker.Enabled() {
		return
	}
	client, ok := r.(interface{ Resty() *resty.Client })
	if !ok || client.Resty() == nil {
		return
	}
	rc := client.Resty()
	tracing := sentryOf(tracker).tracing

	rc.OnBeforeRequest(func(_ *resty.Client, req *resty.Request) error {
		ctx := req.Context()
		if !tracing {
			return nil
		}
		parent := sentry.SpanFromContext(ctx)
		if parent == nil {
			return nil
		}
		span := parent.StartChild("http.client", sentry.WithDescription(req.Method+" "+scrubURL(req.URL)))
		req.SetContext(span.Context())
		req.SetHeader(sentry.SentryTraceHeader, span.ToSentryTrace())
		if baggage := span.ToBaggage(); baggage != "" {
			req.SetHeader(sentry.SentryBaggageHeader, baggage)
		}
		outgoingSpans.put(req, span)
		return nil
	})

	rc.OnAfterResponse(func(_ *resty.Client, resp *resty.Response) error {
		finishOutgoing(resp.Request, resp.StatusCode(), resp.Time(), nil)
		return nil
	})

	rc.OnError(func(req *resty.Request, err error) {
		status := 0
		if respErr, ok := err.(*resty.ResponseError); ok && respErr.Response != nil {
			status = respErr.Response.StatusCode()
		}
		finishOutgoing(req, status, 0, err)
	})
}

// finishOutgoing closes the client span (when there is one) and records the
// call as a breadcrumb.
func finishOutgoing(req *resty.Request, status int, elapsed time.Duration, err error) {
	if req == nil {
		return
	}
	ctx := req.Context()
	if span := outgoingSpans.take(req); span != nil {
		span.SetData("http.status_code", status)
		if err != nil || status >= 400 {
			span.Status = sentry.SpanStatusInternalError
		} else {
			span.Status = sentry.SpanStatusOK
		}
		span.Finish()
	}

	level := LevelInfo
	if err != nil || status >= 400 {
		level = LevelError
	}
	data := map[string]any{
		"method": req.Method,
		"url":    scrubURL(req.URL),
	}
	if status > 0 {
		data["status_code"] = status
	}
	if elapsed > 0 {
		data["duration_ms"] = elapsed.Milliseconds()
	}
	if err != nil {
		data["error"] = err.Error()
	}
	breadcrumbTo(ctx, Breadcrumb{
		Type:     "http",
		Category: "http.client",
		Message:  req.Method + " " + scrubURL(req.URL),
		Level:    level,
		Data:     data,
	})
}

// outgoingSpans keeps the span of an in-flight request between the before- and
// after-hooks. resty has no per-request storage, and stuffing the span into the
// request context is not enough because the after-hook needs to find it again.
var outgoingSpans = newSpanRegistry()

type spanRegistry struct {
	mu    sync.Mutex
	spans map[*resty.Request]*sentry.Span
}

func newSpanRegistry() *spanRegistry {
	return &spanRegistry{spans: map[*resty.Request]*sentry.Span{}}
}

func (r *spanRegistry) put(req *resty.Request, span *sentry.Span) {
	r.mu.Lock()
	r.spans[req] = span
	r.mu.Unlock()
}

func (r *spanRegistry) take(req *resty.Request) *sentry.Span {
	r.mu.Lock()
	span := r.spans[req]
	delete(r.spans, req)
	r.mu.Unlock()
	return span
}

// scrubURL strips credentials and masks sensitive query parameters, so a URL
// with an api_key in it does not become a permanent record in Sentry. The query
// is rewritten in place rather than re-encoded, so the mask stays readable.
func scrubURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	hadUser := u.User != nil
	u.User = nil
	u.RawQuery = newScrubber(nil, nil).scrubQuery(u.RawQuery)

	out := strings.TrimSuffix(u.String(), "?")
	if hadUser {
		out = strings.Replace(out, "://", "://"+redacted+"@", 1)
	}
	return out
}
