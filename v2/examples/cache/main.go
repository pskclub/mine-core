// Command cache is a runnable tour of the v2 cache: values, counters, locks,
// cache-aside, invalidation and testing.
//
// Each example lives in its own file:
//
//	01_values.go        get/set/delete, encoding, TTLs, key naming, prefixes
//	02_counters.go      Incr as a one-call rate limiter, and counters worth having
//	03_locks.go         Lock/LockWait, lock TTLs, and what a crashed holder leaves
//	04_remember.go      cache-aside with Remember, stampedes, negative caching
//	05_invalidation.go  invalidate on write, DelByPrefix, telling other replicas
//	06_testing.go       NewMemoryCache in tests, and code that survives no cache
//
// It runs with no redis. With no CACHE_* configuration — or with one that will
// not answer — it falls back to the in-process memory cache, which is a real
// cache in every way except that it cannot span processes. So `go run .` works
// on a laptop with nothing installed, and the code below is unchanged either
// way, which is the point the whole package is built around.
//
// Run it with: go run ./examples/cache
package main

import (
	"context"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

func main() {
	env, err := core.NewEnv()
	if err != nil {
		panic(err)
	}

	// 1. one cache handle for the process. app.Shutdown closes it, so nothing
	//    here calls Close on the cache it handed over.
	cache, reason := openCache(env)
	app, err := core.NewApp(env, core.WithCache("default", cache))
	if err != nil {
		panic(err)
	}
	app.Log().Info("cache backend chosen", "why", reason)
	logCacheMode(app)

	// 2. ctx.Cache() returns a handle already bound to this context, which is
	//    why none of the cache calls in the examples take a ctx of their own.
	//    A cancelled request abandons its cache calls with it.
	ctx := app.NewContext(context.Background(), core.ModeTest)

	runValues(ctx)
	runCounters(ctx)
	runLocks(ctx)
	runRemember(ctx)
	runInvalidation(app, ctx)
	runTesting(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = app.Shutdown(shutdownCtx)
}

// openCache picks a backend the way a service should: use redis when it is
// configured and reachable, and degrade to something that still works when it
// is not.
//
// The fallback here is the *memory* cache rather than the disabled one, because
// this is a demo and the examples are more interesting when values come back.
// A real service usually wants the opposite default — NewApp with no cache at
// all gives the disabled backend, whose reads miss and whose writes are
// dropped, so the code path stays identical in every environment. Falling back
// to memory in a multi-replica deployment is worse than having no cache: rate
// limits count per pod, locks exclude nothing, and invalidation messages reach
// one replica out of N.
func openCache(env core.IENV) (core.ICache, string) {
	cfg := env.Config()
	if cfg.CacheHost == "" && cfg.CacheAddrs == "" && cfg.CacheConnectionString == "" {
		return core.NewMemoryCache(), "no CACHE_* configuration: in-process memory cache"
	}

	// NewCache PINGs before returning, so a misconfigured cache is a boot error
	// rather than a surprise on the first request that touches it. A service
	// would panic here; the tour prefers to keep running.
	c, err := core.NewCache(env)
	if err != nil {
		return core.NewMemoryCache(), "redis is configured but unreachable (" + err.Error() + ")"
	}
	return c, "redis"
}
