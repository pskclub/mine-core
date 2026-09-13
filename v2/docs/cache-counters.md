# Counters & rate limits

```go
n, err := c.Incr("views:"+id, 1, core.NoExpiry)
```

`Incr` adds `delta` and returns the new value. A negative delta subtracts; a delta
of `0` reads the counter, creating it at zero if it is absent.

```go
c.Incr("views:"+id, 1, core.NoExpiry)    // increment
c.Incr("stock:"+id, -1, core.NoExpiry)   // decrement
c.Incr("views:"+id, 0, core.NoExpiry)    // read
```

## The TTL applies only on creation

This is the whole reason `Incr` takes a TTL at all. It is applied **only when
this call creates the counter**, in one atomic script — so traffic inside a
window cannot extend that window:

```go
n, _ := c.Incr("rate:"+ip, 1, time.Minute)
if n > 100 {
    return ctx.NewError(nil, errmsgs.TooManyRequests)
}
```

Written by hand as `INCR` + `EXPIRE`, the second call resets the expiry on every
request, and a client that keeps knocking is never allowed through again — a
limiter that turns into a ban. One atomic operation is the fix.

## A fixed-window limiter

Middleware runs on `*echo.Context`, so it takes the `*core.App` to reach the
cache (see [Middleware](./middleware.md)):

```go
func RateLimit(app *core.App, limit int64, window time.Duration) echo.MiddlewareFunc {
    return func(next echo.HandlerFunc) echo.HandlerFunc {
        return func(ec *echo.Context) error {
            key := fmt.Sprintf("rate:%s:%s", ec.Path(), ec.RealIP())

            n, err := app.Cache().Incr(key, 1, window)
            if err != nil {
                return next(ec)     // a cache failure is not a 429
            }
            if n > limit {
                return core.New(http.StatusTooManyRequests, "TOO_MANY_REQUESTS",
                    "too many requests")
            }
            return next(ec)
        }
    }
}
```

```go
e.Use(RateLimit(app, 100, time.Minute))                  // everywhere
e.POST("/login", Login, RateLimit(app, 5, time.Minute))  // one route
```

Two decisions in there worth copying:

- **The window is in the key's TTL, not in a timestamp you compare.** No clock
  arithmetic, no cleanup.
- **A cache error lets the request through.** A rate limiter that fails closed
  turns a redis blip into a full outage. Fail open unless the thing being limited
  is more expensive than being down.

### What a fixed window is not

A fixed window resets all at once, so a client can send `limit` requests at
`0:59` and `limit` more at `1:00` — twice the intended rate across a two-second
span. That is acceptable for protecting a database and not acceptable for
metering something billable.

For the second case, use a sliding window over a sorted set on the raw client, or
a token bucket in a Lua script — both are `Redis()` territory:

```go
rdb := c.Redis()   // sorted set of request timestamps, trimmed by score
```

## Counters worth having

```go
// per-account, not just per-IP: a NAT is one IP for a whole office
c.Incr("rate:login:"+email, 1, 15*time.Minute)

// something expensive, limited per tenant
c.Incr("export:"+tenantID, 1, time.Hour)

// a metric nobody has to query a database for
c.Incr("views:post:"+id, 1, core.NoExpiry)
```

Rate-limit on the thing being protected. Limiting login attempts by IP alone lets
a distributed attempt through and locks out an office; limiting by account alone
lets one attacker spray a thousand accounts. Do both, with different limits.

## Counters are not durable

A counter in redis is lost on a flush, an eviction, or a restart without
persistence. That is fine for a rate limit — the window resets, nobody is
harmed — and not fine for anything anyone will read as a total.

| Use `Incr` for | Do not use it for |
|---|---|
| rate limits and quotas within a window | billing counters |
| "how many views, roughly" | anything reconciled against money |
| debouncing and throttling | stock levels — [use the database](./repository-writes.md#atomic-arithmetic) |

For a view count that must eventually be right, increment in redis and flush to
the database periodically with a [job](./jobs.md). The cache absorbs the write
rate; the database holds the truth.

## On a disabled cache

`Incr` answers **zero**. That is the only honest answer — without a shared
counter there is nothing to count — but it means a limit built on it never trips:

```go
if n > limit { … }   // n is always 0 with no redis: never triggers
```

An environment with no redis therefore enforces no rate limits. Usually that is
correct (a developer's laptop should not rate-limit them). When it is not, say so
out loud:

```go
if !c.Enabled() {
    ctx.Log().Warn("rate limiting is disabled: no cache configured")
}
```

## Decrementing a stock counter

The tempting version is wrong:

```go
// ❌ two requests can both see 1 remaining
n, _ := c.Incr("stock:"+sku, 0, core.NoExpiry)
if n > 0 {
    c.Incr("stock:"+sku, -1, core.NoExpiry)
}
```

Decrement **first** and check the result, because that is the part that is
atomic:

```go
// ✅ exactly one caller can be the one that took it to 0
n, _ := c.Incr("stock:"+sku, -1, core.NoExpiry)
if n < 0 {
    c.Incr("stock:"+sku, 1, core.NoExpiry)   // put it back
    return ctx.NewError(nil, errmsgs.OutOfStock)
}
```

And for real stock, do this in the database instead — a redis counter that
disagrees with the orders table is an incident, not a cache miss.
