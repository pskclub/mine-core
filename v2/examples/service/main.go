// Command service is a runnable skeleton of one service, from boot to shutdown.
//
// It is the shape to copy when starting a project: a single binary that can be
// deployed as an API, as a worker or as both, assembled from whatever
// configuration is actually present, and stopped in the order that does not cut
// work off half-finished.
//
// Each part lives in its own file:
//
//	01_bootstrap.go      build the App from the configuration that is set
//	02_roles.go          one binary, three roles, one composition root
//	03_runner.go         start everything, stop it in the right order
//	04_health.go         liveness vs readiness, critical vs degraded
//	05_config.go         keys, defaults, and failing at boot rather than at 2am
//	06_observability.go  one request id everywhere, one report per incident
//	07_testing.go        testing the real service without a single mock
//	08_modules.go        one feature in one file, mounted by role
//
// It needs nothing installed. With no DB_*, CACHE_*, S3_* or MQ_* set, every
// capability is present but disabled, and the process still serves HTTP, runs
// its job and answers both probes:
//
//	go run .
//	curl localhost:8080/healthz
//	curl localhost:8080/readyz
//	curl 'localhost:8080/hello?name=ada'
//	APP_ROLE=worker go run .      # no HTTP; the scheduler and job runner only
//	APP_ROLE=all    go run .      # both, in one process
package main

import "os"

func main() {
	// 1. configuration first, and it either loads or the process does not start.
	//    There is no logger yet — the thing that would have built one is what
	//    failed — so this is the one place a bare panic is the honest report.
	env, err := loadConfig()
	if err != nil {
		panic(err)
	}
	cfg, err := newServiceConfig(env)
	if err != nil {
		panic(err)
	}

	// 2. the App: pools and capabilities, opened only for what is configured.
	//    Everything else is present and disabled rather than nil.
	app, err := bootstrap(env)
	if err != nil {
		panic(err)
	}
	logConfigWarnings(app)

	// 3. run until SIGTERM, then stop everything in order.
	//
	//    There is deliberately no `defer app.Shutdown(ctx)` here: Runner closes
	//    the pools itself, as the last step after the drain, and a second close
	//    from a defer would either be a no-op (Shutdown is idempotent) or — if
	//    it were reached first — exactly the bug this ordering exists to
	//    prevent. One owner of the shutdown, and it is the Runner.
	if err := run(app, cfg.Role); err != nil {
		// the Runner has already stopped and closed everything by the time it
		// returns an error, so this line is the last thing left to do
		app.Log().Error("service stopped with an error", "role", string(cfg.Role), "err", err)
		os.Exit(1)
	}
}
