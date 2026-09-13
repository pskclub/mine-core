package main

import (
	"context"
	"net/http"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 8: modules — one feature, one file, one line in the root --------
//
// 02_roles.go registers this service's features by kind: routes in
// registerModules, jobs in registerJobs. That works, and it is what most
// services do. It has one failure mode, and it is silent: a feature's routes
// and its cron are registered in two different functions, so a feature can be
// half-wired and nothing says so. A route nobody registered is a 404 found in
// QA; a cron nobody armed is a job that simply never runs, and no log line is
// missing because none was ever written.
//
// core.IModule turns those scattered registrations into one type. The only
// method it requires is Name; everything else is an optional interface, so a
// module implements exactly what it has:
//
//	IHTTPModule       Routes(e *core.Server)
//	IJobModule        Jobs(reg *core.JobRegistry) core.IError
//	ICronModule       Cron(sc *core.Scheduler) core.IError
//	IMQModule         Consumers(c core.IMQConsumer)
//	IHealthModule     HealthChecks() []core.HealthCheck
//	ILifecycleModule  Start(app *core.App) / Stop(ctx)
//
// What runs is then the *role's* decision rather than the module's: a worker has
// no HTTP server, so Routes is never called, and the same module list serves
// every role. That is the property this buys — with two registration functions,
// an api and a worker each keep their own list, and nothing keeps the two in
// step.

// tokenModule is the sweep job from 02_roles.go, plus the routes and the health
// check that belong to the same feature, declared together.
//
// Its dependencies arrive through the constructor, exactly as before: a module
// still declares what it needs as an interface of its own and the composition
// root supplies it. That is what keeps the arrows pointing one way, and it is
// why there is no global registry and no init()-time registration — an init
// function takes no arguments, so a module registered by one could never be
// given anything.
type tokenModule struct {
	// upstream is the dependency, held as the narrowest interface this module
	// actually uses rather than as somebody else's service type.
	upstream tokenStore
}

// tokenStore is declared here, by the consumer, so this module compiles without
// importing whichever module implements it.
type tokenStore interface {
	SweepExpired(ctx core.IContext) (int, core.IError)
}

func newTokenModule(upstream tokenStore) *tokenModule {
	return &tokenModule{upstream: upstream}
}

// Name must match [a-z0-9][a-z0-9_-]* and be unique in the set: it prefixes this
// module's health checks and keys its entry in the devtools panel.
func (*tokenModule) Name() string { return "token" }

// Routes is the same signature the registration functions above already have,
// so moving one here is a rename rather than a rewrite.
func (m *tokenModule) Routes(e *core.Server) {
	e.GET("/tokens/stats", m.stats)
}

func (m *tokenModule) stats(c core.IHTTPContext) error {
	return c.JSON(http.StatusOK, map[string]any{"swept": 0})
}

// Cron arms the schedule. Scheduler.Add both registers and arms, so a job armed
// here must not also be registered in Jobs — that is a duplicate name, and the
// error says which module caused it.
func (m *tokenModule) Cron(sc *core.Scheduler) core.IError {
	return sc.Add(core.JobDef{
		Name:          "token.sweep-expired",
		Description:   "delete tokens past their expiry",
		Schedule:      core.Every(30 * time.Second),
		Timeout:       time.Minute,
		MaxConcurrent: 1,
		Concurrency:   core.ConcurrencySkip,
	}, func(c core.ICronjobContext) error {
		_, err := m.upstream.SweepExpired(c)
		return err
	})
}

// Jobs holds what is triggered rather than scheduled. It is separate from Cron
// because an API role has no scheduler and still has to be able to trigger this
// — the definition must reach a process that arms nothing.
func (m *tokenModule) Jobs(reg *core.JobRegistry) core.IError {
	return reg.Register(core.JobDef{
		Name:        "token.purge-all",
		Description: "drop every token, on request only",
	}, func(c core.ICronjobContext) error { return nil })
}

// HealthChecks names dependencies the framework cannot see by itself. The check
// is reported as "token.upstream": the module prefix is added automatically, and
// it is what keeps two modules that both call a check "upstream" from
// overwriting each other in a report keyed by name.
func (m *tokenModule) HealthChecks() []core.HealthCheck {
	return []core.HealthCheck{{
		Name:  "upstream",
		Check: func(ctx context.Context) error { return nil },
	}}
}

// --- assembling them --------------------------------------------------------

// modulesOf is the composition root's one list. It is still the only place that
// names every module — that is deliberate, and it is what lets a test assemble
// the real service minus one module.
func modulesOf(app *core.App) (*core.ModuleSet, core.IError) {
	return core.NewModules(
		newTokenModule(stubTokenStore{}),
		// note.New(usersFor), user.New(), …
	)
}

type stubTokenStore struct{}

func (stubTokenStore) SweepExpired(core.IContext) (int, core.IError) { return 0, nil }

// runWithModules is 03_runner.go's sequence with the module set added. Compare
// it with newAPI/newWorker above: adding a module changes modulesOf and nothing
// else, whichever role this process is.
func runWithModules(app *core.App) error {
	mods, err := modulesOf(app)
	if err != nil {
		return err
	}

	e := core.NewHTTPServer(app, &core.HTTPOptions{AllowOrigins: []string{"*"}})

	// the probes go on before anything authenticated, and they are the one place
	// the modules' checks have to be passed by hand: a probe that reported fewer
	// checks than the service has would say nothing about being incomplete
	core.RegisterHealthRoutes(e, core.HealthOptions{Checks: mods.HealthChecks()})

	// mounting here as well as through RunModules is the normal shape, not a
	// mistake: this function is what a test calls, and a test builds no Runner.
	// A second mount onto the same server is a no-op.
	mods.MountHTTP(e)

	sc, sErr := core.NewScheduler(app)
	if sErr != nil {
		return sErr
	}

	return core.NewRunner(app,
		core.RunHTTP(e),
		core.RunScheduler(sc),
		core.RunJobs(sc.Runner()),
		core.RunModules(mods), // ← adding a module never touches this line again
	).Run()
}

// What the boot log says, and why it is worth reading:
//
//	modules mounted  modules=[token] routes=1 jobs=1 cron=1 queues=0
//
// and in an api role, where there is no scheduler:
//
//	modules mounted  modules=[token] routes=1 jobs=1 cron=0 queues=0 skipped=[cron]
//
// `skipped=[cron]` is the line that matters. In a worker it is normal; in a
// process that was supposed to arm those schedules it is the misconfiguration,
// and nothing else in the log reports it.
//
// devtools.Mount(srv, devtools.Options{Modules: mods}) adds the same answer as
// a panel: which module registered which route, job, schedule and check.
