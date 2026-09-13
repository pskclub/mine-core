package main

import (
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 1: bootstrap — build the App out of what is configured ----------
//
// The App is built once and holds everything that lives as long as the process:
// connection pools, the base logger, the Sentry client. Contexts are minted from
// it per request and per job run and own nothing — which is the v1 bug this
// split exists to fix, where IContext.Close() tore down shared pools on every
// request.
//
// A connection is opened only when its configuration is present. That is not
// tidiness: it is what lets the same binary run in an environment with no redis
// and no bucket without a single `if cache != nil` anywhere above, because an
// absent capability is a disabled implementation rather than a nil.

func bootstrap(env core.IENV) (*core.App, core.IError) {
	cfg := env.Config()
	opts := make([]core.Option, 0, 4)

	// SQL. Pool sizes are deliberately not environment keys: they belong to a
	// deployment's capacity plan, which is a decision worth reviewing in a diff
	// rather than one worth changing from a dashboard at 2am.
	if cfg.DBConnectionString != "" || cfg.DBHost != "" {
		db, err := core.NewDatabase(env,
			core.WithMaxOpenConns(20),
			core.WithMaxIdleConns(5),
			core.WithConnMaxLifetime(time.Hour),
		)
		if err != nil {
			return nil, err
		}
		opts = append(opts, core.WithSQL("default", db))
		// a read replica would be core.WithSQL("replica", replicaDB), reached
		// with ctx.DBS("replica") — and it gets its own readiness check
	}

	// Cache. Missing CACHE_* is not an error: ctx.Cache() then reads as a miss
	// and drops writes, so cache-aside code runs unchanged with and without
	// redis. Silent degradation is safe here precisely because a miss can always
	// be recomputed.
	if cfg.CacheConnectionString != "" || cfg.CacheHost != "" || cfg.CacheAddrs != "" {
		cache, err := core.NewCache(env)
		if err != nil {
			return nil, err
		}
		opts = append(opts, core.WithCache("default", cache))
	}

	// Storage and MQ are the opposite choice: unconfigured, every call fails
	// with STORAGE_DISABLED / MQ_DISABLED instead of degrading quietly. A file
	// that was silently dropped and a message nobody ever received cannot be
	// recomputed, so the failure has to be loud at the call site.
	if cfg.S3Bucket != "" {
		storage, err := core.NewStorage(env)
		if err != nil {
			return nil, err
		}
		opts = append(opts, core.WithStorage(storage))
	}
	if cfg.MQConnectionString != "" || cfg.MQHost != "" {
		mq, err := core.NewMQ(env)
		if err != nil {
			return nil, err
		}
		opts = append(opts, core.WithMQ(mq))
	}

	// Sentry is not in this list on purpose: NewApp builds it from SENTRY_DSN,
	// and with no DSN every method is a no-op. Error reporting is therefore
	// wired identically in every environment, so no reporting path is exercised
	// for the first time in production.
	return core.NewApp(env, opts...)
}

// The boot log states what the process was actually assembled from:
//
//	{"level":"INFO","msg":"app ready","env":"dev","service":"example-service",
//	 "sql":[],"mongo":[],"cache":[],"mq":false,"storage":false,
//	 "mailer":false,"pusher":false,"sentry":false}
//
// It is the counterweight to everything above: because a missing connection
// degrades rather than refusing to boot, one absent line of configuration is
// otherwise invisible until the first request that needed it — and by then the
// symptom ("nothing is being cached") is several layers from the cause.
//
// core.Runner emits it as the first thing it does, so 03_runner.go gets it for
// free. A process that starts itself some other way calls app.LogCapabilities()
// directly; NewApp deliberately does not, because building the container is not
// the same event as starting the process, and every test builds one.
