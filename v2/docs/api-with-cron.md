# Running API + Cron Jobs together

A single service can serve HTTP **and** run scheduled jobs in one process. Both
share one `App` (one set of connection pools), and shutdown is coordinated so
in-flight requests and jobs finish cleanly.

## Full example

::: code-group

```go [cmd/main.go]
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v5"

	core "github.com/pskclub/mine-core/v2"
)

func main() {
	env, err := core.NewEnv()
	if err != nil {
		panic(err)
	}

	// shared resources (one pool set for both HTTP handlers and jobs)
	db, err := core.NewDatabase(env)
	if err != nil {
		panic(err)
	}
	redis, err := core.NewCache(env)
	if err != nil {
		panic(err)
	}
	app, err := core.NewApp(env,
		core.WithSQL("default", db),
		core.WithCache("default", redis),
	)
	if err != nil {
		panic(err)
	}

	// --- HTTP ---
	e := core.NewHTTPServer(app, nil)
	e.GET("/health", func(c core.IHTTPContext) error { return c.JSON(200, "ok") })
	// ... more routes ...

	// --- Cron ---
	sc, err := core.NewScheduler(app)
	if err != nil {
		panic(err)
	}
	_ = sc.AddByCron("nightly-report", "0 2 * * *", NightlyReport)
	_ = sc.AddByDuration("heartbeat", 30*time.Second, Heartbeat)

	run(app, e, sc)
}

// run starts the scheduler and HTTP server, then blocks until SIGINT/SIGTERM and
// shuts everything down gracefully (drain HTTP → stop jobs → close pools).
func run(app *core.App, e *core.Server, sc *core.Scheduler) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sc.Start() // non-blocking

	addr := app.Config().Host
	if addr == "" {
		addr = ":8080"
	}

	// e.Serve, not the embedded echo Start: Shutdown below only knows about a
	// server this method started, so e.Start(addr) would leave e.Shutdown a
	// silent no-op and nothing would ever drain.
	go func() {
		cfg := echo.StartConfig{Address: addr, HideBanner: true, HidePort: true}
		if err := e.Serve(context.Background(), cfg); err != nil && !errors.Is(err, http.ErrServerClosed) {
			app.Log().Error("http server stopped", "err", err)
			stop() // trigger shutdown if the server dies
		}
	}()
	app.Log().Info("service started", "addr", addr)

	<-ctx.Done() // wait for a signal
	app.Log().Info("shutting down…")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_ = e.Shutdown(shutdownCtx)   // 1. stop accepting requests, drain in-flight
	_ = sc.Stop()                 // 2. stop the scheduler (waits for running jobs)
	_ = app.Shutdown(shutdownCtx) // 3. close pools last
}
```

```go [jobs/nightly_report.go]
// job เป็น core.JobFunc ธรรมดาที่ได้ context เต็ม (ดู scheduler.md)
func NightlyReport(c core.ICronjobContext) error {
	users, err := repository.New[User](c).Where("active = ?", true).FindAll()
	if err != nil {
		return err
	}
	// c.DB(), c.Cache(), c.MQ(), core.Requester(c) — same capabilities as HTTP handlers
	return nil
}
```

:::

## Why one `App`

- **One pool set.** HTTP handlers and jobs use the *same* DB/cache/MQ pools —
  no double the connections, no lifecycle surprises.
- **Correct shutdown.** Pools are closed once (`app.Shutdown`), after both the
  HTTP server and the scheduler have stopped.
- **Independent contexts.** Each request runs in `ModeHTTP`; each job run gets a
  fresh context in `ModeCron`. They never share request state.

## Shutdown order matters

1. `e.Shutdown(ctx)` — stop taking new requests, let in-flight ones finish.
2. `sc.Stop()` — stop scheduling; running jobs are allowed to complete.
3. `app.Shutdown(ctx)` — close DB/cache/MQ pools **last**, once nothing uses them.

The 15-second timeout bounds the whole drain; tune it to your longest expected
request/job.

## Alternative: separate API and worker processes

For independent scaling (many API replicas, few workers) run the **same binary**
in two modes and deploy them separately:

```go
func main() {
	app := bootstrap() // NewEnv + NewDatabase + NewApp ...

	switch os.Getenv("APP_ROLE") {
	case "worker":
		runWorker(app) // scheduler only, blocks until signal
	default:
		runAPI(app)    // HTTP only
	}
}
```

- `APP_ROLE=worker` → only the scheduler runs (no HTTP port).
- default / `APP_ROLE=api` → only HTTP.
- Same codebase and `App` wiring; pick per deployment.

Use the combined process (first example) for small services; split into API +
worker roles when you need to scale or isolate them.

## Notes

- `core.StartHTTPServer(e, env)` is the **HTTP-only** starter: it drains on
  SIGINT/SIGTERM and then closes the pools itself. A combined service needs the
  `run()` loop above instead, because the scheduler has to stop *between* those
  two steps.
- A panicking job never crashes the scheduler — it becomes a logged error
  (see [scheduler.md](./scheduler.md)). A panicking HTTP handler becomes a 500.
- Want only one instance in a cluster to run a job? Wrap the work in
  `core.WithLock(c.Cache(), "job:nightly", time.Minute, ...)` — the lock expires,
  so an instance that crashes mid-job does not block the next tick
  (see [Locks](./cache-locks.md)). Jobs that go through the runner get this from
  their definition instead: `MaxConcurrent: 1` plus a runner built with
  `core.WithJobLimiter(lim)`, where `lim` comes from `core.NewRedisLimiter(app)`
  and counts slots in redis rather than per process (see
  [Cluster-wide concurrency limits](./jobs-running.md#cluster-wide-limits)).

## See also

- [Lifecycle & roles](./lifecycle.md) — bootstrap, the `api`/`worker`/`all` split,
  and the shutdown sequence in full
- [Deployment](./deployment.md) — replicas per role, probes, grace periods
