package main

import (
	"strings"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 2: one binary, three roles --------------------------------------
//
// An API replica and a worker replica are the same image started with a
// different APP_ROLE. Two binaries would mean two Dockerfiles, two CI pipelines
// and two versions free to drift apart; one binary means the worker is provably
// running the same code the API is.
//
//	APP_ROLE     runs                        replicas
//	(unset)/api  HTTP only                   many
//	worker       scheduler + job runner      exactly one
//	all          both, in one process        exactly one
//
// ⚠ never scale `all` past one replica. The default job queue lives in the
// process's memory, so every replica fires on every cron tick: three replicas
// means the nightly report runs three times. To scale, run many `api` next to a
// single `worker` (or move the queue into the database — see Jobs).

type role string

const (
	roleAPI    role = "api"
	roleWorker role = "worker"
	roleAll    role = "all"
)

// roleFrom reads the role from configuration rather than os.Getenv, so both
// APP_ROLE=worker (compose, Kubernetes) and ROLE=worker in a .env work — the
// former is how it is deployed, the latter how it is run on a laptop.
//
// ⚠ inside .env the key carries no APP_ prefix. Writing APP_ROLE=worker *there*
// binds the key "app_role", matches no field, raises no error, and leaves the
// process running as an API — which is the failure this indirection invites and
// 05_config.go rejects at boot.
func roleFrom(env core.IENV) role {
	switch role(strings.ToLower(strings.TrimSpace(env.String("role")))) {
	case roleWorker:
		return roleWorker
	case roleAll:
		return roleAll
	default:
		// unset means api: the safe default is the role that may be scaled
		return roleAPI
	}
}

// newAPI is the composition root for HTTP — the one place that knows every
// module the service is made of. Nothing below it imports anything above it, so
// a module can be deleted by deleting its line here.
func newAPI(app *core.App) *core.Server {
	e := core.NewHTTPServer(app, &core.HTTPOptions{
		AllowOrigins: []string{"*"},
	})
	registerModules(e)
	return e
}

// registerModules is split out of newAPI so a test can mount the exact same set
// of routes on coretest's server (see 07_testing.go). A module registered with a
// dependency it never got then fails in a test rather than in production, which
// is the whole reason the root is a function instead of a chunk of main().
func registerModules(e *core.Server) {
	registerHealth(e)   // 04_health.go — before anything authenticated
	registerGreeting(e) // 06_observability.go
}

// newWorker is the composition root for background work. Its counterpart to the
// rule above: adding a job to an existing module touches that module only — just
// the module's *first* job touches this function.
func newWorker(app *core.App) (*core.Scheduler, core.IError) {
	sc, err := core.NewScheduler(app)
	if err != nil {
		return nil, err
	}
	if err := registerJobs(sc); err != nil {
		return nil, err
	}
	return sc, nil
}

func registerJobs(sc *core.Scheduler) core.IError {
	return sc.Add(core.JobDef{
		Name:        "sweep-expired-tokens",
		Description: "delete tokens past their expiry",
		Schedule:    core.Every(30 * time.Second),
		Timeout:     time.Minute,
		// a job on a short schedule must never queue up behind itself: skipping
		// a tick loses nothing here, whereas a backlog of sweeps is unbounded
		MaxConcurrent: 1,
		Concurrency:   core.ConcurrencySkip,
	}, sweepExpired) // 06_observability.go
}
