package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os/signal"
	"sync"
	"time"
)

// Runner starts the parts of a service and, more importantly, stops them in the
// right order.
//
// Shutting a service down is not "close everything": it is a sequence, and
// getting it wrong is what turns a deploy into a burst of 500s and half-finished
// jobs. The order is always
//
//  1. stop taking new work — refuse new connections, stop the cron ticking, stop
//     pulling from the queue
//  2. let the work in flight finish, up to a deadline
//  3. only then close the pools it was using
//
// StartHTTPServer does this for a process that only serves HTTP. Runner does it
// for one that also runs jobs, a scheduler and pub/sub subscribers — where the
// sequence is easy to write by hand and easier to get wrong.
//
//	r := core.NewRunner(app,
//	    core.RunHTTP(e),
//	    core.RunScheduler(sc),
//	    core.RunJobs(sc.Runner()),
//	)
//	if err := r.Run(); err != nil {
//	    log.Fatal(err)
//	}
type Runner struct {
	app  *App
	opts runnerOptions

	http      *Server
	scheduler *Scheduler
	jobs      *JobRunner
	consumers []IMQConsumer
	services  []RunnerService
	modules   *ModuleSet

	mu      sync.Mutex
	stopped bool
}

// RunnerService is anything else with a start and a stop — a gRPC server, a
// third-party consumer, a metrics exporter. Start must not block.
type RunnerService interface {
	Start() error
	Stop(ctx context.Context) error
}

type runnerOptions struct {
	drainTimeout time.Duration
	closeTimeout time.Duration
	beforeStop   []func(context.Context) error
	afterStop    []func()
}

// RunnerOption configures a Runner.
type RunnerOption func(*Runner)

// RunHTTP serves HTTP. The server is drained before anything is closed.
func RunHTTP(e *Server) RunnerOption { return func(r *Runner) { r.http = e } }

// RunScheduler ticks cron jobs. It is stopped first, so no new run is queued
// during the drain.
func RunScheduler(sc *Scheduler) RunnerOption { return func(r *Runner) { r.scheduler = sc } }

// RunJobs works the job queue. Give the Runner the same JobRunner the scheduler
// feeds — passing both is how in-flight runs get to finish.
func RunJobs(j *JobRunner) RunnerOption { return func(r *Runner) { r.jobs = j } }

// RunMQ consumes queues. Consumers stop first in the shutdown, with the
// scheduler: both are sources of new work, and a message accepted during the
// drain is a handler racing the pools it is about to lose.
//
// It exists because a consumer cannot be passed to RunService — IMQConsumer
// returns IError where RunnerService returns error, and a nil IError handed
// back as an error is not nil.
func RunMQ(c ...IMQConsumer) RunnerOption {
	return func(r *Runner) { r.consumers = append(r.consumers, c...) }
}

// RunModules mounts a ModuleSet onto whatever this Runner was given: jobs and
// cron when it has a scheduler, routes when it has a server, consumers when it
// has one. An extension point with no target is skipped and named in the boot
// line, so "this role does not arm cron" is a thing the log says rather than a
// thing somebody works out from a job that never ran.
//
//	core.NewRunner(app,
//	    core.RunHTTP(e),
//	    core.RunScheduler(sc),
//	    core.RunJobs(sc.Runner()),
//	    core.RunModules(mods),
//	)
func RunModules(set *ModuleSet) RunnerOption {
	return func(r *Runner) { r.modules = set }
}

// RunService adds anything else with a Start and a Stop.
func RunService(s ...RunnerService) RunnerOption {
	return func(r *Runner) { r.services = append(r.services, s...) }
}

// WithDrainTimeout bounds how long in-flight requests and job runs have to
// finish once the shutdown starts (default DefaultGracefulTimeout).
//
// Set it below the orchestrator's own grace period — Kubernetes'
// terminationGracePeriodSeconds — or the process is killed mid-drain and the
// ordering this type exists for never happens.
func WithDrainTimeout(d time.Duration) RunnerOption {
	return func(r *Runner) {
		if d > 0 {
			r.opts.drainTimeout = d
		}
	}
}

// WithCloseTimeout bounds closing the pools, after the drain (default
// DefaultGracefulTimeout).
func WithCloseTimeout(d time.Duration) RunnerOption {
	return func(r *Runner) {
		if d > 0 {
			r.opts.closeTimeout = d
		}
	}
}

// BeforeStop registers a hook to run at the start of the shutdown, before
// anything is drained — deregistering from a service registry, say, so traffic
// stops arriving before the drain begins rather than during it.
func BeforeStop(fn func(context.Context) error) RunnerOption {
	return func(r *Runner) { r.opts.beforeStop = append(r.opts.beforeStop, fn) }
}

// AfterStop registers a hook to run once everything is closed — the last thing
// before the process exits.
func AfterStop(fn func()) RunnerOption {
	return func(r *Runner) { r.opts.afterStop = append(r.opts.afterStop, fn) }
}

// NewRunner builds a Runner. Nothing starts until Run.
func NewRunner(app *App, opts ...RunnerOption) *Runner {
	r := &Runner{
		app: app,
		opts: runnerOptions{
			drainTimeout: DefaultGracefulTimeout,
			closeTimeout: DefaultGracefulTimeout,
		},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Run starts everything and blocks until a shutdown signal arrives, then stops
// it all in order. It returns the first error of the whole lifecycle.
func (r *Runner) Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), ShutdownSignals...)
	defer stop()
	return r.RunContext(ctx)
}

// RunContext is Run bounded by a context of the caller's own — for a test, or a
// process whose shutdown is triggered by something other than a signal.
func (r *Runner) RunContext(ctx context.Context) error {
	log := r.app.Log()

	if err := r.start(); err != nil {
		// something failed to start: stop whatever did, so a partial start does
		// not leave a scheduler ticking into a service that will never serve
		r.shutdown(log)
		return err
	}

	var serveErr error
	if r.http != nil {
		serveErr = r.serveHTTP(ctx, log)
	} else {
		<-ctx.Done()
		log.Info("shutdown signal received")
	}

	if err := r.shutdown(log); err != nil && serveErr == nil {
		serveErr = err
	}
	return serveErr
}

// start brings up everything that does not block.
func (r *Runner) start() error {
	// first line of the boot log: what this process is wired with, before
	// anything reports what it is doing with it
	r.app.LogCapabilities()

	// before anything starts: the scheduler reports what it armed as it starts,
	// and the job runner lists what is registered — both would be reporting a
	// half-built service if the modules had not attached yet
	if err := r.mountModules(); err != nil {
		return err
	}

	// the job runner before the scheduler, so the first tick has workers to be
	// picked up by rather than a queue nobody is reading
	if r.jobs != nil {
		r.jobs.Start()
	}
	if r.scheduler != nil {
		// no line here: Scheduler.Start reports what it armed and when each job
		// next fires, which is the same fact in more useful form
		if err := r.scheduler.Start(); err != nil {
			return err
		}
	}
	for _, s := range r.services {
		if err := s.Start(); err != nil {
			return Wrap(err, "runner: start service")
		}
	}
	// last: consuming begins only once everything a handler might reach —
	// workers, scheduler, services — is up. A message delivered before that is
	// work arriving at a service that cannot yet do it.
	for _, c := range r.consumers {
		if err := errOrNil(c.Start()); err != nil {
			return Wrap(err, "runner: start mq consumer")
		}
	}
	return nil
}

// mountModules attaches the ModuleSet to the pieces this Runner holds, in the
// order the parts depend on each other: background work first, then job
// definitions, then the schedules that fire them, then routes and consumers.
func (r *Runner) mountModules() error {
	set := r.modules
	if set == nil {
		return nil
	}

	if err := errOrNil(set.Start(r.app)); err != nil {
		return err
	}

	skipped := make([]string, 0, 4)
	skip := func(kind string, present bool, is func(IModule) bool) {
		if !present && set.implements(is) {
			skipped = append(skipped, kind)
		}
	}

	// definitions before schedules: a job an API role triggers has to be in the
	// registry of a process that has no scheduler at all
	reg := r.jobRegistry()
	if reg != nil {
		if err := errOrNil(set.MountJobs(reg)); err != nil {
			return err
		}
	}
	skip("jobs", reg != nil, func(m IModule) bool { _, ok := m.(IJobModule); return ok })

	if r.scheduler != nil {
		if err := errOrNil(set.MountCron(r.scheduler)); err != nil {
			return err
		}
	}
	skip("cron", r.scheduler != nil, func(m IModule) bool { _, ok := m.(ICronModule); return ok })

	if r.http != nil {
		set.MountHTTP(r.http)
	}
	skip("routes", r.http != nil, func(m IModule) bool { _, ok := m.(IHTTPModule); return ok })

	for _, c := range r.consumers {
		set.MountConsumers(c)
	}
	skip("consumers", len(r.consumers) > 0, func(m IModule) bool { _, ok := m.(IMQModule); return ok })

	r.logModules(set, skipped)
	return nil
}

// jobRegistry is the registry this process runs jobs out of, whichever of the
// two ways it was given one.
func (r *Runner) jobRegistry() *JobRegistry {
	if r.jobs != nil {
		return r.jobs.Registry()
	}
	if r.scheduler != nil && r.scheduler.Runner() != nil {
		return r.scheduler.Runner().Registry()
	}
	return nil
}

// logModules states what the modules attached, and — the part worth the line —
// what they could not, because this role has nowhere to put it.
func (r *Runner) logModules(set *ModuleSet, skipped []string) {
	var routes, jobs, cron, queues int
	for _, rep := range set.Report() {
		routes += len(rep.Routes)
		jobs += len(rep.Jobs)
		cron += len(rep.Cron)
		queues += len(rep.Queues)
	}
	fields := []any{
		"modules", set.Names(),
		"routes", routes, "jobs", jobs, "cron", cron, "queues", queues,
	}
	if len(skipped) > 0 {
		// not a warning: a worker with no HTTP server is the normal shape of a
		// worker. It is logged because the same list in an api role that meant
		// to serve those routes is a misconfiguration nothing else reports.
		fields = append(fields, "skipped", skipped)
	}
	r.app.Log().Info("modules mounted", fields...)
}

// serveHTTP runs the server until ctx is cancelled. It does not close the pools
// — that is the shutdown sequence's job, after everything else has drained.
func (r *Runner) serveHTTP(ctx context.Context, log ILogger) error {
	addr, cfg := serveConfig(r.app.env)
	cfg.GracefulTimeout = r.opts.drainTimeout
	cfg.OnShutdownError = func(err error) { log.Error("http server shutdown", "err", err) }

	// Bind before announcing. Serve blocks once it is listening, so logging
	// "started" ahead of it claims a success the listener has not achieved: a
	// port already in use would print "started" and then the bind error, which
	// reads like a crash rather than a failure to launch. Opening the listener
	// here is what lets the two be told apart.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("http server cannot bind", "addr", addr, "err", err)
		return Wrapf(err, "runner: listen on %s", addr)
	}
	cfg.Listener = ln
	// the count, not the table: the routing table is discoverable from the code
	// and from the Postman collection, and printing it buries every other line
	// of the boot log — in dev most of all, where debug is the level people run
	log.Info("http server started",
		"addr", ln.Addr().String(), "routes", len(r.http.Router().Routes()))

	if err := r.http.Serve(ctx, cfg); err != nil && !isServerClosed(err) {
		log.Error("http server stopped", "err", err)
		return err
	}
	log.Info("http server stopped")
	return nil
}

// shutdown stops everything, in the order that keeps work from being cut off.
// It is idempotent.
func (r *Runner) shutdown(log ILogger) error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.stopped = true
	r.mu.Unlock()

	var firstErr error
	fail := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), r.opts.drainTimeout)
	defer cancelDrain()

	for _, hook := range r.opts.beforeStop {
		fail(hook(drainCtx))
	}

	// 1. stop producing work. The scheduler goes first: a tick during the drain
	// queues a run that will be requeued or cancelled seconds later, which is
	// noise in the store and, for a job that is not idempotent, worse.
	if r.scheduler != nil {
		log.Info("stopping scheduler")
		fail(errOrNil(r.scheduler.Stop()))
	}
	// consumers are the other source of new work. App.Shutdown would stop them
	// too — every consumer is a tracked subscriber — but that happens in step 3,
	// which is after the drain this is meant to precede.
	for _, c := range r.consumers {
		log.Info("stopping mq consumer")
		fail(errOrNil(c.Stop(drainCtx)))
	}

	// 2. drain what is already running. The job runner waits for its in-flight
	// runs; the HTTP server is already draining, because cancelling its context
	// is what got us here.
	if r.jobs != nil {
		log.Info("draining jobs")
		fail(errOrNil(r.jobs.Stop(drainCtx)))
	}
	for _, s := range r.services {
		fail(s.Stop(drainCtx))
	}
	if r.http != nil {
		fail(r.http.Shutdown(drainCtx))
	}
	// modules last of the drain: a module's background work is what a request
	// or a job may have been depending on, so it outlives both — but it still
	// runs before the pools close, so stopping it may touch the database.
	if r.modules != nil {
		log.Info("stopping modules")
		fail(errOrNil(r.modules.Stop(drainCtx)))
	}

	// 3. only now the pools. App.Shutdown stops the pub/sub subscribers first,
	// then closes every connection — nothing above is still holding one.
	closeCtx, cancelClose := context.WithTimeout(context.Background(), r.opts.closeTimeout)
	defer cancelClose()

	log.Info("closing connections")
	fail(errOrNil(r.app.Shutdown(closeCtx)))

	for _, hook := range r.opts.afterStop {
		hook()
	}

	log.Info("stopped")
	return firstErr
}

// Stop triggers the shutdown from outside Run — for a test, or a service that
// decides to stop itself.
func (r *Runner) Stop() error { return r.shutdown(r.app.Log()) }

// isServerClosed reports whether the listener stopped because it was asked to,
// rather than because something went wrong. A cancelled context is the ordinary
// end of Serve here: cancelling it is how the shutdown begins.
func isServerClosed(err error) bool {
	return errors.Is(err, http.ErrServerClosed) || errors.Is(err, context.Canceled)
}
