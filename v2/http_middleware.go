package core

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
)

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// RateLimitOption tunes RateLimit.
type RateLimitOption func(*rateLimitOptions)

type rateLimitOptions struct {
	key    func(c *echo.Context) string
	skip   func(c *echo.Context) bool
	prefix string
}

// RateLimitKey chooses what is being counted. The default is the client IP
// per route, which limits "this caller on this endpoint". Limit the thing
// being protected instead when that is not the caller: an account's login
// attempts by the account, an OTP by the phone number.
//
// The client IP is echo's RealIP: the peer address unless the server has an
// IPExtractor. Behind a load balancer every request shares the balancer's
// address, so set one (echo.ExtractIPFromXFFHeader with the proxy's range)
// or the whole service shares one budget.
//
//	core.RateLimit(cache, 5, time.Minute, core.RateLimitKey(func(c *echo.Context) string {
//	    return c.FormValue("email")
//	}))
//
// An empty key is not counted, so a request the key function cannot identify
// goes through.
func RateLimitKey(fn func(c *echo.Context) string) RateLimitOption {
	return func(o *rateLimitOptions) { o.key = fn }
}

// RateLimitSkip exempts requests — a health check, an internal caller.
func RateLimitSkip(fn func(c *echo.Context) bool) RateLimitOption {
	return func(o *rateLimitOptions) { o.skip = fn }
}

// RateLimitPrefix namespaces the counters in the cache (default "ratelimit").
// Two limits on the same route with different windows need different
// prefixes, or they share a counter.
func RateLimitPrefix(prefix string) RateLimitOption {
	return func(o *rateLimitOptions) { o.prefix = prefix }
}

// RateLimit allows limit requests per window per key and answers 429
// TOO_MANY_REQUESTS to the rest. It is a fixed-window limiter on ICache.Incr:
// one atomic increment whose TTL is the window, so there is no clock
// arithmetic, no cleanup and nothing shared between replicas but the cache.
//
//	e.POST("/login", h.Login, core.RateLimit(app.Cache(), 5, time.Minute))
//	e.Use(core.RateLimit(app.Cache(), 100, time.Minute))   // per route, per IP
//
// Every answer carries X-RateLimit-Limit and X-RateLimit-Remaining; a refusal
// adds Retry-After with the window in seconds, since a fixed window cannot
// say more precisely when it turns over.
//
// It fails open. A cache that errors — or the disabled cache a service with no
// CACHE_* has, whose Incr answers zero — lets every request through, because a
// limiter that fails closed turns a redis blip into a full outage. What must
// hold even then (OTP attempts, say) has to be counted in the database.
func RateLimit(cache ICache, limit int64, window time.Duration, opts ...RateLimitOption) echo.MiddlewareFunc {
	o := rateLimitOptions{
		prefix: "ratelimit",
		key:    func(c *echo.Context) string { return c.RealIP() },
	}
	for _, opt := range opts {
		opt(&o)
	}
	store := orNoop(cache)
	limitHeader := strconv.FormatInt(limit, 10)
	retryAfter := strconv.Itoa(int((window + time.Second - 1) / time.Second))

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if limit <= 0 || window <= 0 || (o.skip != nil && o.skip(c)) {
				return next(c)
			}
			id := o.key(c)
			if id == "" {
				return next(c)
			}
			// the route pattern, not the URL: /users/1 and /users/2 are one
			// endpoint, and a limit per URL would be no limit at all
			key := o.prefix + ":" + c.Request().Method + ":" + c.Path() + ":" + id

			n, err := store.WithContext(c.Request().Context()).Incr(key, 1, window)
			if err != nil || n <= 0 {
				return next(c) // fail open: see the doc comment
			}

			h := c.Response().Header()
			h.Set("X-RateLimit-Limit", limitHeader)
			if remaining := limit - n; remaining > 0 {
				h.Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
			} else {
				h.Set("X-RateLimit-Remaining", "0")
			}
			if n > limit {
				h.Set("Retry-After", retryAfter)
				return New(http.StatusTooManyRequests, "TOO_MANY_REQUESTS",
					"too many requests, try again in "+retryAfter+" seconds")
			}
			return next(c)
		}
	}
}

// ---------------------------------------------------------------------------
// Security headers
// ---------------------------------------------------------------------------

// SecureHeadersOptions are the response headers SecureHeaders sets. Every
// field has a default chosen for a JSON API; set one to "" to drop that header,
// or to another value to override it.
type SecureHeadersOptions struct {
	// XFrameOptions stops the response being framed (default "DENY").
	XFrameOptions string
	// ContentTypeNosniff stops a browser second-guessing Content-Type
	// (default on). Off only when you have a reason.
	DisableContentTypeNosniff bool
	// ReferrerPolicy (default "strict-origin-when-cross-origin").
	ReferrerPolicy string
	// ContentSecurityPolicy is empty by default: an API has no page to
	// protect, and a wrong policy on a page breaks it in ways that are hard
	// to see. Set it on a server that renders HTML.
	ContentSecurityPolicy string
	// PermissionsPolicy is empty by default for the same reason.
	PermissionsPolicy string
	// HSTSMaxAge is the Strict-Transport-Security max-age (default one year).
	// The header is sent only on a request that arrived over TLS — directly or
	// through a proxy that set X-Forwarded-Proto — because a browser that
	// receives it over plain HTTP ignores it, and sending it from a dev server
	// would pin a developer's localhost to HTTPS. Negative turns it off.
	HSTSMaxAge time.Duration
	// HSTSIncludeSubdomains extends HSTS to subdomains (default off: it is the
	// one setting here that can take down a sibling service).
	HSTSIncludeSubdomains bool
	// HSTSPreload adds the preload directive (default off; it is a commitment).
	HSTSPreload bool
}

// DefaultHSTSMaxAge is one year, the minimum most audits ask for.
const DefaultHSTSMaxAge = 365 * 24 * time.Hour

// SecureHeaders sets the response headers that keep a browser from doing
// something unwise with an API's responses: no framing, no MIME sniffing, a
// conservative referrer policy and HSTS on TLS. It is not in the default stack
// because a server that renders HTML wants its own policy; add it where the
// defaults fit:
//
//	e.Use(core.SecureHeaders())
//	e.Use(core.SecureHeaders(core.SecureHeadersOptions{XFrameOptions: "SAMEORIGIN"}))
//
// A header the handler already set is left alone, so a route can override one.
func SecureHeaders(opts ...SecureHeadersOptions) echo.MiddlewareFunc {
	o := SecureHeadersOptions{
		XFrameOptions:  "DENY",
		ReferrerPolicy: "strict-origin-when-cross-origin",
		HSTSMaxAge:     DefaultHSTSMaxAge,
	}
	if len(opts) > 0 {
		given := opts[0]
		// explicit values win; a zero value keeps the default, and "" has to
		// be spelled as a value that is then dropped below, so a field left
		// unset in a literal does not silently remove a header
		if given.XFrameOptions != "" {
			o.XFrameOptions = given.XFrameOptions
		}
		if given.ReferrerPolicy != "" {
			o.ReferrerPolicy = given.ReferrerPolicy
		}
		o.DisableContentTypeNosniff = given.DisableContentTypeNosniff
		o.ContentSecurityPolicy = given.ContentSecurityPolicy
		o.PermissionsPolicy = given.PermissionsPolicy
		if given.HSTSMaxAge != 0 {
			o.HSTSMaxAge = given.HSTSMaxAge
		}
		o.HSTSIncludeSubdomains = given.HSTSIncludeSubdomains
		o.HSTSPreload = given.HSTSPreload
	}

	hsts := ""
	if o.HSTSMaxAge > 0 {
		hsts = "max-age=" + strconv.FormatInt(int64(o.HSTSMaxAge/time.Second), 10)
		if o.HSTSIncludeSubdomains {
			hsts += "; includeSubDomains"
		}
		if o.HSTSPreload {
			hsts += "; preload"
		}
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			h := c.Response().Header()
			setIfAbsent(h, "X-Frame-Options", strings.TrimSpace(o.XFrameOptions))
			if !o.DisableContentTypeNosniff {
				setIfAbsent(h, "X-Content-Type-Options", "nosniff")
			}
			setIfAbsent(h, "Referrer-Policy", o.ReferrerPolicy)
			setIfAbsent(h, "Content-Security-Policy", o.ContentSecurityPolicy)
			setIfAbsent(h, "Permissions-Policy", o.PermissionsPolicy)
			if hsts != "" && c.Scheme() == "https" {
				setIfAbsent(h, "Strict-Transport-Security", hsts)
			}
			return next(c)
		}
	}
}

func setIfAbsent(h http.Header, name, value string) {
	if value == "" || h.Get(name) != "" {
		return
	}
	h.Set(name, value)
}
