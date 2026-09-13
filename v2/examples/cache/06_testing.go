package main

import (
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 6: testing, and code that survives no cache --------------------
//
// Tests need no redis. core.NewMemoryCache() is a real cache in the same
// process — same encoding, real expiry, atomic counters, working locks and
// pub/sub — so the caching path is exercised rather than stubbed out.
//
// What it cannot do is span processes, so anything whose *point* is
// coordination between processes is not being tested by it: a distributed lock
// has one holder to begin with, a rate limit counts one process out of N,
// pub/sub fans out only to this binary, and Redis() returns nil. Those need a
// real redis under the integration tag.
//
// The test this file is really about is the one below. Running the same code
// against the memory cache and against the disabled cache — and expecting 1
// call and then 2 — pins down both halves at once: that the caching is real,
// and that the code still works without it.
//
//	func TestCachesTheUser(t *testing.T) {
//	    c := core.NewMemoryCache()
//	    defer c.Close()
//	    app, err := core.NewApp(env, core.WithCache("default", c))
//	    require.NoError(t, err)
//	    ctx := app.NewContext(context.Background(), core.ModeTest)
//
//	    var calls int32
//	    load := func() (user, error) {
//	        atomic.AddInt32(&calls, 1)
//	        return user{ID: "1", Name: "ann"}, nil
//	    }
//	    _, _ = core.Remember(ctx.Cache(), "user:v1:1", time.Minute, load)
//	    _, _ = core.Remember(ctx.Cache(), "user:v1:1", time.Minute, load)
//
//	    // the assertion that matters needs no cache internals at all
//	    require.Equal(t, int32(1), atomic.LoadInt32(&calls))
//
//	    // and the memory cache is inspectable when the internals do matter
//	    ttl, err := c.TTL("user:v1:1")
//	    require.NoError(t, err)
//	    require.InDelta(t, time.Minute.Seconds(), ttl.Seconds(), 2)
//	}

func runTesting(ctx core.IContext) {
	// The same assertion as running code: 1 against a real cache, 2 against
	// none. Anything else means the caching is not doing what it claims.
	ctx.Log().Info("loader calls",
		"memory_cache", countLoads(core.NewMemoryCache()),
		"no_cache", countLoads(core.NewNoopCache()))
}

func countLoads(c core.ICache) int {
	defer func() { _ = c.Close() }()

	calls := 0
	load := func() (user, error) {
		calls++
		return user{ID: "1", Name: "ann"}, nil
	}
	_, _ = core.Remember(c, "user:v1:1", time.Minute, load)
	_, _ = core.Remember(c, "user:v1:1", time.Minute, load)
	return calls
}

// requireRealCache is for the few paths that genuinely cannot work without a
// cache. Everything else should not ask: the whole point of the disabled cache
// is that cache-aside code runs unchanged in an environment that has none.
//
// The three answers a disabled cache gives that change *behaviour* rather than
// just speed, and are therefore worth a check:
//
//	Incr   returns 0        a limit built on it never trips
//	SetNX  returns true     every caller believes it is the first
//	Lock   is granted       every replica holds every lock
//
// On a laptop all three are the right answers. On a multi-replica deployment
// that lost its CACHE_* configuration by accident, all three are silent
// correctness bugs.
func requireRealCache(ctx core.IContext) core.IError {
	if !ctx.Cache().Enabled() {
		return core.New(503, "CACHE_REQUIRED", "this endpoint needs a cache")
	}
	return nil
}

// logCacheMode belongs at boot, where somebody reads it once, rather than in a
// handler where it would be noise. "Which cache did this process actually get"
// is the first question of every stale-data and every double-charge
// investigation, and it costs one line to have the answer already written down.
func logCacheMode(app *core.App) {
	c := app.Cache()
	app.Log().Info("cache",
		"enabled", c.Enabled(), // false => the disabled backend
		"distributed", c.Redis() != nil, // false => memory or disabled: this process only
		"prefix", c.Prefix())
}

// Which backend for which test:
//
//	NewMemoryCache()                   almost every test — fast, hermetic, no
//	                                   container, and a real cache
//	NewNoopCache()                     asserting the code works with no cache
//	real redis (make test-integration) the cluster and sentinel paths, and
//	                                   anything that has to be believed
//
// Expiry is real in the memory cache, so a 50ms TTL is testable without
// waiting. Keep those tests rare and the durations small: a suite that sleeps
// its way through TTL assertions gets slow, and sleeping tests are the first
// ones to go flaky on a loaded CI runner.
