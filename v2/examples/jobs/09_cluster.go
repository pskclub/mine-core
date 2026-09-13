package main

import (
	core "github.com/pskclub/mine-core/v2"
)

// --- Example 9: more than one replica ---------------------------------------
//
// Everything else in this tour is correct on a single replica. Two things stop
// being correct on two:
//
//	concurrency limits   counted in this process, so `MaxConcurrent: 1` becomes
//	                     "one per pod" — three pods, three settlement runs
//	queued runs          kept in this process's memory, so they vanish with it
//
// The second is fixed by the durable store and queue (07_store.go). The first is
// fixed here, by counting slots in redis instead of in a map. Neither changes a
// job definition or a handler.
//
// The limiter holds a slot on a lease that it renews while the run lasts, so a
// pod that is killed mid-run gives its slot back by falling silent — nothing to
// clean up, and a singleton job cannot be wedged by a bad deploy.

func clusterOptions(app *core.App) []core.JobRunnerOption {
	lim, err := core.NewRedisLimiter(app)
	if err != nil {
		// No redis configured. Counting in this process is exactly right for one
		// replica and wrong for several, so the fallback is loud rather than
		// silent — in a real service this line deserves an alert.
		app.Log().Warn("job limiter: concurrency is counted in this process only",
			"err", err)
		return nil
	}
	app.Log().Info("job limiter: concurrency is counted in redis, cluster-wide")

	return []core.JobRunnerOption{
		core.WithJobLimiter(lim),
	}
}

// Other things worth knowing once there is more than one replica:
//
//   - The scheduler runs in every replica, and each one enqueues on its own
//     schedule. Give scheduled jobs `MaxConcurrent: 1` with ConcurrencySkip so
//     the duplicates collapse into one run, or run the scheduler in a single
//     replica (a separate deployment with no HTTP server).
//
//   - An IdemKey does the same for triggers: two replicas asked to settle the
//     same invoice produce one run (06_replay.go).
//
//   - A cancel is written to the store and picked up by the replica executing
//     the run within a couple of seconds. Nothing has to know which pod that is.
//
//   - core.NewRedisLimiter(app, core.WithLimiterCache("jobs")) points the limiter
//     at a named cache, for services that keep coordination and caching apart.
//     WithLimiterPrefix makes two *services* share one limit; by default each
//     service's limits are its own, namespaced by its cache prefix.
