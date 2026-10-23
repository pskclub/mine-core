package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rateLimitedServer(t *testing.T, cache ICache, limit int64, opts ...RateLimitOption) *Server {
	t.Helper()
	e := NewHTTPServer(newHTTPTestApp(t), nil)
	e.GET("/ping", func(c IHTTPContext) error { return c.JSON(http.StatusOK, "pong") },
		RateLimit(cache, limit, time.Minute, opts...))
	e.GET("/other", func(c IHTTPContext) error { return c.JSON(http.StatusOK, "ok") },
		RateLimit(cache, limit, time.Minute, opts...))
	return e
}

// getFrom sends a request from the given address. Without an IPExtractor echo
// reads the peer address, not a header — which is the safe default, since a
// header is what a client wanting a fresh budget would forge.
func getFrom(e *Server, path, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = ip + ":40000"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestRateLimit_allowsTheLimitThenRefuses(t *testing.T) {
	cache := NewMemoryCache()
	t.Cleanup(func() { _ = cache.Close() })
	e := rateLimitedServer(t, cache, 2)

	first := getFrom(e, "/ping", "10.0.0.1")
	assert.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, "2", first.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "1", first.Header().Get("X-RateLimit-Remaining"))

	second := getFrom(e, "/ping", "10.0.0.1")
	assert.Equal(t, http.StatusOK, second.Code)
	assert.Equal(t, "0", second.Header().Get("X-RateLimit-Remaining"))

	third := getFrom(e, "/ping", "10.0.0.1")
	require.Equal(t, http.StatusTooManyRequests, third.Code, third.Body.String())
	assert.Equal(t, "60", third.Header().Get("Retry-After"), "the window, in seconds")
	assert.Contains(t, third.Body.String(), "TOO_MANY_REQUESTS")

	assert.Equal(t, http.StatusOK, getFrom(e, "/ping", "10.0.0.2").Code, "another caller has its own budget")
	assert.Equal(t, http.StatusOK, getFrom(e, "/other", "10.0.0.1").Code, "another route has its own budget")
}

// A service with no cache configured has the disabled cache, whose Incr
// answers zero. The limiter must let everything through rather than refuse
// everything — failing open is the documented choice.
func TestRateLimit_failsOpenWithoutACache(t *testing.T) {
	e := rateLimitedServer(t, nil, 1)
	for range 5 {
		assert.Equal(t, http.StatusOK, getFrom(e, "/ping", "10.0.0.1").Code)
	}
}

func TestRateLimit_keyAndSkipOptions(t *testing.T) {
	cache := NewMemoryCache()
	t.Cleanup(func() { _ = cache.Close() })
	e := rateLimitedServer(t, cache, 1,
		RateLimitKey(func(c *echo.Context) string { return c.QueryParam("account") }),
		RateLimitSkip(func(c *echo.Context) bool { return c.QueryParam("internal") == "1" }),
	)

	assert.Equal(t, http.StatusOK, getFrom(e, "/ping?account=a", "10.0.0.1").Code)
	assert.Equal(t, http.StatusTooManyRequests, getFrom(e, "/ping?account=a", "10.0.0.9").Code,
		"the key is the account, not the address")
	assert.Equal(t, http.StatusOK, getFrom(e, "/ping?account=b", "10.0.0.1").Code)
	assert.Equal(t, http.StatusOK, getFrom(e, "/ping?account=a&internal=1", "10.0.0.1").Code, "skipped")
	assert.Equal(t, http.StatusOK, getFrom(e, "/ping", "10.0.0.1").Code, "no key, not counted")
}

func TestSecureHeaders_defaultsForAnAPI(t *testing.T) {
	e := NewHTTPServer(newHTTPTestApp(t), nil)
	e.Use(SecureHeaders())
	e.GET("/", func(c IHTTPContext) error {
		c.Response().Header().Set("X-Frame-Options", "SAMEORIGIN") // a route's own say
		return c.JSON(http.StatusOK, "ok")
	})

	plain := httptest.NewRecorder()
	e.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/", nil))
	h := plain.Header()
	assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	assert.Equal(t, "strict-origin-when-cross-origin", h.Get("Referrer-Policy"))
	assert.Equal(t, "SAMEORIGIN", h.Get("X-Frame-Options"), "a header the handler set is kept")
	assert.Empty(t, h.Get("Strict-Transport-Security"), "not over plain HTTP: a dev box must not get pinned to HTTPS")
	assert.Empty(t, h.Get("Content-Security-Policy"), "no policy by default: an API has no page")

	// echo trusts the forwarding headers only from a private or loopback peer
	// — the proxy in front — so the request has to look like it came from one
	tls := httptest.NewRequest(http.MethodGet, "/", nil)
	tls.RemoteAddr = "127.0.0.1:40000"
	tls.Header.Set("X-Forwarded-Proto", "https")
	secure := httptest.NewRecorder()
	e.ServeHTTP(secure, tls)
	assert.Equal(t, "max-age=31536000", secure.Header().Get("Strict-Transport-Security"),
		"behind a TLS-terminating proxy the request is HTTPS")
}

func TestSecureHeaders_options(t *testing.T) {
	e := NewHTTPServer(newHTTPTestApp(t), nil)
	e.Use(SecureHeaders(SecureHeadersOptions{
		XFrameOptions:         "SAMEORIGIN",
		ContentSecurityPolicy: "default-src 'self'",
		HSTSMaxAge:            -1,
	}))
	e.GET("/", func(c IHTTPContext) error { return c.JSON(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, "SAMEORIGIN", rec.Header().Get("X-Frame-Options"))
	assert.Equal(t, "default-src 'self'", rec.Header().Get("Content-Security-Policy"))
	assert.Empty(t, rec.Header().Get("Strict-Transport-Security"), "a negative max-age turns HSTS off")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"), "an unset field keeps its default")
}
