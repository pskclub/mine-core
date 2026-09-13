package core

import (
	"context"
	"time"

	"github.com/go-co-op/gocron/v2"
)

// Schedule describes when a job runs automatically. Build one with Cron,
// CronWithSeconds or Every; a job with no schedule is manual-only.
type Schedule interface {
	definition() gocron.JobDefinition
	String() string
}

type cronSchedule struct {
	expr        string
	withSeconds bool
	// loc is the zone the expression is read in; nil means the process's.
	loc *time.Location
}

func (s cronSchedule) definition() gocron.JobDefinition {
	return gocron.CronJob(s.crontab(), s.withSeconds)
}

// crontab is the expression gocron parses. A zone travels as the CRON_TZ prefix
// rather than as gocron.WithLocation because that option sets one zone for the
// whole scheduler, and a service reading its schedules out of a database has a
// zone *per job* — the column an operator edits has to mean something on its own.
// gocron reads the prefix first and ignores the scheduler's location when it is
// present, so the two can coexist.
func (s cronSchedule) crontab() string {
	if s.loc == nil {
		return s.expr
	}
	return "CRON_TZ=" + s.loc.String() + " " + s.expr
}

func (s cronSchedule) String() string {
	if s.loc == nil {
		return "cron(" + s.expr + ")"
	}
	return "cron(" + s.expr + " " + s.loc.String() + ")"
}

// Cron schedules on a standard 5-field cron expression, read in the process's
// own time zone — which in a container with no TZ set is UTC, not where the
// people reading the schedule live. Use CronIn when the hour has to mean a
// particular zone.
func Cron(expr string) Schedule { return cronSchedule{expr: expr} }

// CronIn is Cron read in loc, whatever the process's own zone is:
//
//	core.CronIn(bangkok, "0 22 * * *") // 22:00 in Bangkok, always
func CronIn(loc *time.Location, expr string) Schedule {
	return cronSchedule{expr: expr, loc: loc}
}

// CronWithSeconds schedules on a 6-field expression whose first field is seconds.
func CronWithSeconds(expr string) Schedule { return cronSchedule{expr: expr, withSeconds: true} }

// CronWithSecondsIn is CronWithSeconds read in loc.
func CronWithSecondsIn(loc *time.Location, expr string) Schedule {
	return cronSchedule{expr: expr, withSeconds: true, loc: loc}
}

type durationSchedule struct{ d time.Duration }

func (s durationSchedule) definition() gocron.JobDefinition { return gocron.DurationJob(s.d) }
func (s durationSchedule) String() string                   { return "every(" + s.d.String() + ")" }

// Every schedules a job to run at a fixed interval.
func Every(d time.Duration) Schedule { return durationSchedule{d: d} }

// Scheduler turns schedules into queued runs. It deliberately does *not*
// execute anything: a tick enqueues a run exactly like a manual trigger does, so
// scheduled and manual work share one path — and therefore one implementation of
// status, logging, retries, concurrency and cancellation.
type Scheduler struct {
	s      gocron.Scheduler
	app    *App
	runner *JobRunner
	// ownsRunner is set when the scheduler created the runner itself and must
	// therefore start and stop it.
	ownsRunner bool
	started    bool
	// loc is the zone schedules without one of their own are read in; nil means
	// the process's. Kept for the boot log — gocron holds the authoritative copy.
	loc *time.Location
	// armed keys the gocron job by name, so Start can ask each one when it
	// next fires — the question a cron expression does not answer by itself.
	armed map[string]gocron.Job
}

// SchedulerOption configures a Scheduler at construction.
//
// *JobRunner is one, which is what keeps the original form of the call —
// NewScheduler(app, runner) — meaning exactly what it always did.
type SchedulerOption interface {
	applyScheduler(*schedulerConfig)
}

type schedulerConfig struct {
	runner *JobRunner
	loc    *time.Location
}

type schedulerOptionFunc func(*schedulerConfig)

func (f schedulerOptionFunc) applyScheduler(c *schedulerConfig) { f(c) }

// applyScheduler lets a runner be passed to NewScheduler as an option.
func (r *JobRunner) applyScheduler(c *schedulerConfig) {
	if r != nil {
		c.runner = r
	}
}

// WithSchedulerLocation sets the time zone every cron expression is read in, for
// the whole scheduler:
//
//	sc, _ := core.NewScheduler(app, runner, core.WithSchedulerLocation(bangkok))
//	sc.AddByCron("nightly", "0 22 * * *", Report) // 22:00 in Bangkok
//
// Without it a schedule is read in the process's own zone, which in a container
// with no TZ set is UTC — the whole table shifts at once and nothing errors.
// A schedule built with CronIn carries its own zone and overrides this.
func WithSchedulerLocation(loc *time.Location) SchedulerOption {
	return schedulerOptionFunc(func(c *schedulerConfig) { c.loc = loc })
}

// NewScheduler builds a scheduler. Called with no runner — as in v1 — it creates
// a self-contained one (in-memory queue and store) and manages its lifecycle,
// so a service that only wants cron jobs needs nothing else:
//
//	sc, _ := core.NewScheduler(app)
//	sc.AddByCron("nightly", "0 2 * * *", NightlyReport)
//	sc.Start()
//
// Pass an existing runner to share it with the rest of the service (manual
// triggers, an admin API, a durable store), and WithSchedulerLocation to fix the
// zone every schedule is read in.
func NewScheduler(app *App, opts ...SchedulerOption) (*Scheduler, IError) {
	cfg := &schedulerConfig{}
	for _, o := range opts {
		if o != nil {
			o.applyScheduler(cfg)
		}
	}

	// the location goes to gocron rather than onto each expression here: gocron
	// applies it only to schedules that did not bring a zone of their own, which
	// is what makes CronIn an override rather than a conflict
	var gopts []gocron.SchedulerOption
	if cfg.loc != nil {
		gopts = append(gopts, gocron.WithLocation(cfg.loc))
	}
	s, err := gocron.NewScheduler(gopts...)
	if err != nil {
		return nil, Wrap(err, "scheduler: new")
	}
	sc := &Scheduler{s: s, app: app, loc: cfg.loc, armed: map[string]gocron.Job{}}
	if cfg.runner != nil {
		sc.runner = cfg.runner
	} else {
		sc.runner = NewJobRunner(app, NewJobRegistry())
		sc.ownsRunner = true
	}
	// tell the runner where next-run times come from. It is also how the runner
	// knows a scheduler exists, and so leaves listing the jobs until the times
	// are real rather than printing them twice.
	sc.runner.setNextRun(sc.nextRunOf)
	// and the zone those times are in, which only a cron check-in needs
	sc.runner.setSchedulerLocation(cfg.loc)
	return sc, nil
}

// zone names the time zone schedules are read in, for the boot log. The
// process's own zone is reported as it will actually behave ("UTC", "+07")
// rather than as time.Local names it ("Local"), which says nothing.
func (sc *Scheduler) zone() string {
	if sc.loc != nil {
		return sc.loc.String()
	}
	name, _ := time.Now().Zone()
	return name
}

// nextRunOf reports when a job fires next, once the scheduler is ticking.
func (sc *Scheduler) nextRunOf(name string) (time.Time, bool) {
	job, ok := sc.armed[name]
	if !ok {
		return time.Time{}, false
	}
	next, err := job.NextRun()
	if err != nil || next.IsZero() {
		return time.Time{}, false
	}
	return next, true
}

// Runner returns the runner the scheduler feeds.
func (sc *Scheduler) Runner() *JobRunner { return sc.runner }

// Add registers a job and, when it has a Schedule, arms it.
func (sc *Scheduler) Add(def JobDef, fn JobFunc) IError {
	if err := sc.runner.Registry().Register(def, fn); err != nil {
		return err
	}
	if def.Schedule == nil {
		return nil
	}
	// arm straight away (gocron accepts jobs before Start) so an invalid cron
	// expression is reported here, at the call site that wrote it
	return sc.arm(def)
}

// AddByCron schedules fn on a cron expression (v1-compatible shorthand).
func (sc *Scheduler) AddByCron(name, cronExpr string, fn JobFunc) IError {
	return sc.Add(JobDef{Name: name, Schedule: Cron(cronExpr)}, fn)
}

// AddByDuration schedules fn to run every d (v1-compatible shorthand).
func (sc *Scheduler) AddByDuration(name string, d time.Duration, fn JobFunc) IError {
	return sc.Add(JobDef{Name: name, Schedule: Every(d)}, fn)
}

// arm creates the gocron job that enqueues runs of def, once.
func (sc *Scheduler) arm(def JobDef) IError {
	name := def.Name
	if _, ok := sc.armed[name]; ok {
		return nil
	}
	job, err := sc.s.NewJob(
		def.Schedule.definition(),
		gocron.NewTask(func() { sc.enqueue(name) }),
		gocron.WithName(name),
	)
	if err != nil {
		return Wrapf(err, "scheduler: add job %s", name)
	}
	sc.armed[name] = job
	return nil
}

// enqueue is what a tick does: create a run. Failures are logged, never
// propagated — a scheduler must not die because one job could not be queued.
func (sc *Scheduler) enqueue(name string) {
	if sc.runner.Registry().IsPaused(name) {
		return
	}
	run, err := sc.runner.Trigger(context.Background(), name, nil, TriggerOptions{
		Trigger: TriggerSchedule,
		By:      "scheduler",
	})
	if err != nil {
		sc.app.Log().Error("scheduler: cannot enqueue run", "job", name, "err", err)
		// a tick that never became a run is invisible everywhere else — the run
		// it would have created does not exist to carry the failure
		sc.app.Sentry().CaptureError(err,
			CaptureTag("job", name),
			CaptureTag("scheduler", "enqueue"),
			CaptureFingerprint("scheduler", "enqueue", name))
		return
	}
	sc.app.Log().Debug("scheduler: run queued", "job", name, "run_id", run.ID)
}

// Start arms every scheduled job in the registry and begins ticking. It is
// non-blocking. When the scheduler owns its runner, that is started too.
func (sc *Scheduler) Start() IError {
	if sc.started {
		return nil
	}
	for _, def := range sc.runner.Registry().List() {
		if def.Schedule == nil {
			continue
		}
		if err := sc.arm(def); err != nil {
			return err
		}
	}
	if sc.ownsRunner {
		sc.runner.Start()
	}
	sc.s.Start()
	sc.started = true

	// after s.Start(), so the next-run times exist: the runner's job list is
	// what carries them, and this is the moment it can be complete.
	//
	// The zone is on this line because a schedule with no zone of its own is read
	// in it, and where it comes from the process is a container with no TZ set,
	// it is UTC — a whole cron table silently seven hours off looks exactly like
	// a cron table that is fine.
	sc.app.Log().Info("scheduler started", "scheduled", len(sc.armed), "timezone", sc.zone())
	sc.runner.describe()
	return nil
}

// Stop halts scheduling. In-flight runs are the runner's business — stop it
// separately (or let Stop do it when the scheduler owns the runner).
func (sc *Scheduler) Stop() IError {
	if err := sc.s.Shutdown(); err != nil {
		return Wrap(err, "scheduler: stop")
	}
	sc.started = false
	if sc.ownsRunner {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return sc.runner.Stop(ctx)
	}
	return nil
}
