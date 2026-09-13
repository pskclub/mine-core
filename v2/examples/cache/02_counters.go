package main

import (
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 2: counters and rate limits ------------------------------------
//
// Incr adds delta and returns the new value; a negative delta subtracts and a
// delta of 0 reads it, creating it at zero. The interesting argument is the
// TTL: it is applied only when *this call* created the counter, in one atomic
// script.
//
// That is the whole reason Incr takes a TTL at all. Written by hand as INCR
// followed by EXPIRE, the second call pushes the expiry out on every request,
// and a client that keeps knocking is never let through again — a rate limiter
// that quietly became a ban. One operation, one window.

func runCounters(ctx core.IContext) {
	c := ctx.Cache()

	// A fixed-window limiter is one call, not three. The window lives in the
	// key's TTL, so there is no clock arithmetic and nothing to clean up.
	n, err := c.Incr("rate:login:ann@example.com", 1, 15*time.Minute)
	switch {
	case err != nil:
		// A cache failure is not a 429. A limiter that fails closed turns a
		// redis blip into a full outage, so fail open — unless the thing being
		// limited is more expensive than being down.
		ctx.Log().Warn("rate limiter unavailable, letting the request through", "err", err)
	case n > 5:
		ctx.Log().Info("too many attempts", "count", n)
	}

	// On a disabled cache Incr answers zero, which is the only honest answer —
	// without a shared counter there is nothing to count. It also means the
	// comparison above never trips: an environment with no redis enforces no
	// rate limits at all. On a laptop that is right; on a deployment that lost
	// its CACHE_* configuration by accident it is a silent hole.
	if !c.Enabled() {
		ctx.Log().Warn("rate limiting is disabled: no cache configured")
	}

	countersWorthHaving(ctx)
	_ = verifyOTP(ctx, "0812345678", "999999")
	_ = takeStock(ctx, "sku-1")
}

// countersWorthHaving: rate-limit the thing being protected. By IP alone, a
// distributed attempt walks straight through and one office NAT gets everybody
// locked out; by account alone, one attacker sprays a thousand accounts. Do
// both, with different limits.
func countersWorthHaving(ctx core.IContext) {
	c := ctx.Cache()

	_, _ = c.Incr("rate:login:203.0.113.5", 1, 15*time.Minute)     // per source
	_, _ = c.Incr("rate:login:ann@example.com", 1, 15*time.Minute) // per account
	_, _ = c.Incr("export:tenant-1", 1, time.Hour)                 // per tenant, expensive work
	_, _ = c.Incr("views:post:7", 1, 24*time.Hour)                 // a metric nobody queries the DB for

	views, _ := c.Incr("views:post:7", 0, core.NoExpiry) // delta 0 reads it
	ctx.Log().Info("counters", "views", views)

	// Two things this shape is not:
	//
	//   - Durable. A counter is lost on a flush, an eviction or a restart with
	//     no persistence. Fine for a rate limit (the window resets, nobody is
	//     harmed) and not fine for anything anyone will read as a total. For a
	//     view count that must eventually be right, count in redis and flush to
	//     the database on a schedule: the cache absorbs the write rate, the
	//     database holds the truth.
	//
	//   - Smooth. A fixed window resets all at once, so a client can spend its
	//     whole budget at 0:59 and again at 1:00 — twice the intended rate
	//     across two seconds. Acceptable for protecting a database, not
	//     acceptable for metering anything billable. That wants a sliding
	//     window over a sorted set on Redis(), or a token bucket in Lua.
}

// verifyOTP is the counter shape most services actually need: count the
// failures, not the successes, and let the window clear itself.
func verifyOTP(ctx core.IContext, phone, entered string) core.IError {
	c := ctx.Cache()
	attemptsKey := "otp:attempts:" + phone

	attempts, _ := c.Incr(attemptsKey, 0, core.NoExpiry)
	if attempts >= 5 {
		return core.New(429, "OTP_LOCKED", "too many attempts, request a new code")
	}

	// Get rather than GetDel: a typo should not burn the code. What bounds the
	// guessing is the attempt counter, not the code being single-use — and the
	// counter outlives the code on purpose, so re-requesting one cannot reset
	// the budget.
	var code string
	if err := c.Get("otp:code:"+phone, &code); err != nil {
		return core.New(400, "OTP_EXPIRED", "the code has expired")
	}
	if code != entered {
		_, _ = c.Incr(attemptsKey, 1, 15*time.Minute)
		return core.New(400, "OTP_INVALID", "wrong code")
	}

	_ = c.Del("otp:code:"+phone, attemptsKey) // success consumes both
	return nil
}

// takeStock is the decrement that is easy to get wrong. Reading and then
// decrementing lets two callers both see one remaining; decrementing first and
// checking the result is the part that is atomic, so exactly one of them can be
// the caller that took it to zero.
//
// And for real stock, do this in the database. A redis counter that disagrees
// with the orders table is an incident, not a cache miss.
func takeStock(ctx core.IContext, sku string) bool {
	c := ctx.Cache()

	left, err := c.Incr("stock:"+sku, -1, core.NoExpiry)
	if err != nil {
		return false // nothing was taken, so nothing to put back
	}
	if left < 0 {
		_, _ = c.Incr("stock:"+sku, 1, core.NoExpiry)
		ctx.Log().Info("out of stock", "sku", sku)
		return false
	}
	return true
}
