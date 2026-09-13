package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// cancelPollInterval is how often a running job re-checks the store for a
// cancellation request. In-process cancels are immediate; the poll is what makes
// the same code work with a shared store later.
const cancelPollInterval = 2 * time.Second

// JobRunner executes runs taken from the queue. It is the single path every run
// goes through — scheduled, manual, retried or replayed — which is why status,
// logs, concurrency and cancellation only had to be written once.
type JobRunner struct {
	app *App
	reg *JobRegistry

	queue   IJobQueue
	store   IJobStore
	limiter ILimiter
	hub     *logHub

	workers     int
	queues      []string
	queueLimits map[string]int
	logPolicy   LogPolicy
	logLimits   LogLimits
	// queueSharesStore is true when the queue's storage is the store itself.
	queueSharesStore bool
	slotBackoff      time.Duration
	workerID         string

	mu       sync.Mutex
	inflight map[string]context.CancelFunc
	started  bool
	stopped  bool

	// nextRun is set by NewScheduler when a scheduler is feeding this runner. It
	// answers when a job fires next — which only exists once gocron is ticking,
	// so its presence is also how Start knows to leave the job list to the
	// scheduler rather than print it early and without the times.
	nextRun func(job string) (time.Time, bool)
	// schedLoc is that scheduler's time zone, set alongside nextRun. The runner
	// never schedules anything itself; it carries the zone so a cron check-in can
	// tell Sentry which one the hour was meant in.
	schedLoc     *time.Location
	describeOnce sync.Once

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// JobRunnerOption configures a runner.
type JobRunnerOption func(*JobRunner)

// WithJobQueue sets the queue backend (default: in-memory).
func WithJobQueue(q IJobQueue) JobRunnerOption { return func(r *JobRunner) { r.queue = q } }

// WithJobStore sets the store backend (default: in-memory).
func WithJobStore(s IJobStore) JobRunnerOption { return func(r *JobRunner) { r.store = s } }

// WithJobLimiter sets the concurrency limiter (default: in-process).
func WithJobLimiter(l ILimiter) JobRunnerOption { return func(r *JobRunner) { r.limiter = l } }

// WithWorkers sets how many runs this process executes at once.
func WithWorkers(n int) JobRunnerOption {
	return func(r *JobRunner) {
		if n > 0 {
			r.workers = n
		}
	}
}

// WithQueues restricts the runner to the named queues (default: "default").
func WithQueues(names ...string) JobRunnerOption {
	return func(r *JobRunner) {
		if len(names) > 0 {
			r.queues = names
		}
	}
}

// WithQueueLimit caps how many runs of a whole queue may execute at once.
func WithQueueLimit(queue string, limit int) JobRunnerOption {
	return func(r *JobRunner) { r.queueLimits[queue] = limit }
}

// WithLogPolicy sets the default log policy for every job (default: LogOff,
// because persisted logs are the most expensive part of the system).
func WithLogPolicy(p LogPolicy) JobRunnerOption { return func(r *JobRunner) { r.logPolicy = p } }

// WithLogLimits bounds what a single run may persist.
func WithLogLimits(l LogLimits) JobRunnerOption { return func(r *JobRunner) { r.logLimits = l } }

// WithSlotBackoff sets how long a run waits before retrying for a concurrency
// slot under ConcurrencyEnqueue.
func WithSlotBackoff(d time.Duration) JobRunnerOption {
	return func(r *JobRunner) {
		if d > 0 {
			r.slotBackoff = d
		}
	}
}

// WithWorkerID labels the runs this process executes.
func WithWorkerID(id string) JobRunnerOption { return func(r *JobRunner) { r.workerID = id } }

// NewJobRunner builds a runner over reg. With no options it is fully functional
// out of the box: in-memory queue and store, in-process concurrency limits and
// no persisted logs.
func NewJobRunner(app *App, reg *JobRegistry, opts ...JobRunnerOption) *JobRunner {
	r := &JobRunner{
		app: app, reg: reg,
		hub:         newLogHub(),
		workers:     4,
		queues:      []string{DefaultQueue},
		queueLimits: map[string]int{},
		logPolicy:   LogOff,
		slotBackoff: DefaultSlotBackoff,
		workerID:    uuid.NewString()[:8],
		inflight:    map[string]context.CancelFunc{},
		stopCh:      make(chan struct{}),
	}
	for _, o := range opts {
		o(r)
	}
	if r.queue == nil {
		r.queue = NewMemoryJobQueue()
	}
	if r.store == nil {
		r.store = NewMemoryJobStore()
	}
	if r.limiter == nil {
		r.limiter = NewInProcessLimiter()
	}
	if q, ok := r.queue.(IStoreBackedQueue); ok {
		r.queueSharesStore = q.SharesStore()
	}
	return r
}

// setSchedulerLocation records the zone of the scheduler feeding this runner.
// Under the lock because the documented bootstrap starts the runner *before*
// building a scheduler over it, so a worker may already be executing a manual
// run — and reading the zone — while this is written.
func (r *JobRunner) setSchedulerLocation(loc *time.Location) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.schedLoc = loc
}

// schedulerZone names the zone the scheduler feeding this runner reads schedules
// in, or nothing when no scheduler set one.
func (r *JobRunner) schedulerZone() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.schedLoc == nil {
		return ""
	}
	return r.schedLoc.String()
}

// setNextRun records where next-run times come from, under the lock for the same
// reason setSchedulerLocation is: the scheduler is built after the runner may
// already be executing, and the times are now read by anything holding the
// runner — an admin endpoint, the devtools job list — not only by the boot line.
func (r *JobRunner) setNextRun(fn func(job string) (time.Time, bool)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextRun = fn
}

// nextRunFn is the current source of next-run times, or nil.
func (r *JobRunner) nextRunFn() func(job string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nextRun
}

// enqueue makes a run available. When the queue is the store (the run's row is
// the queue), writing the run has already done it — enqueueing again could
// resurrect a run another worker has just claimed and execute it twice.
func (r *JobRunner) enqueue(ctx context.Context, run *JobRun) IError {
	if r.queueSharesStore {
		return nil
	}
	return r.queue.Enqueue(ctx, run)
}

// Registry returns the registry the runner executes.
func (r *JobRunner) Registry() *JobRegistry { return r.reg }

// Store returns the run store (for admin queries).
func (r *JobRunner) Store() IJobStore { return r.store }

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start launches the worker goroutines. It does not block.
func (r *JobRunner) Start() {
	r.mu.Lock()
	if r.started || r.stopped {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()

	for i := 0; i < r.workers; i++ {
		r.wg.Add(1)
		go r.worker()
	}
	r.app.Log().Info("job runner started", "workers", r.workers, "queues", r.queues)

	// With a scheduler attached the list waits for it: a job's next run only
	// exists once gocron is ticking, and printing the same jobs twice — once
	// without the times and once with — is two lines per job saying one thing.
	if r.nextRunFn() == nil {
		r.describe()
	}
}

// describe names every job this runner can execute, with the settings that will
// actually apply to it and, when a scheduler is feeding it, when each next runs.
//
// "job runner started, workers=4" says a worker is up; it does not say what it
// is a worker *for*. Which jobs a deployment actually carries is the first thing
// anybody wants when one did not fire, and answering it from the code means
// knowing which modules registered what — which is exactly the knowledge missing
// at 3am. It is one line each, at boot, once.
//
// The effective values are resolved rather than printed raw: a def that left
// Timeout at zero runs with DefaultJobTimeout, and logging "timeout=0s" would
// describe the struct instead of the behaviour.
//
// Once, because both the runner and the scheduler have a reason to call it and
// only one of them should win.
func (r *JobRunner) describe() {
	r.describeOnce.Do(func() {
		defs := r.reg.List()
		if len(defs) == 0 {
			// worth saying out loud: a worker with an empty registry starts
			// perfectly happily and then does nothing forever
			r.app.Log().Warn("job runner has no jobs registered")
			return
		}
		for _, def := range defs {
			r.app.Log().Info("job registered", r.describeJob(def)...)
		}
	})
}

// describeJob is the one line a job gets at boot.
func (r *JobRunner) describeJob(def JobDef) []any {
	fields := []any{
		"job", def.Name,
		"queue", def.queue(),
		"timeout", def.timeout().String(),
		"attempts", def.maxAttempts(),
	}

	if def.Schedule == nil {
		fields = append(fields, "trigger", "manual")
	} else {
		fields = append(fields, "schedule", def.Schedule.String())
		// the times are the half a cron expression cannot tell you: "did it not
		// run, or was it never due?" is where every unexpected silence starts
		if nextRun := r.nextRunFn(); nextRun != nil {
			if next, ok := nextRun(def.Name); ok {
				fields = append(fields,
					"next_run", next.Format(time.RFC3339),
					"in", time.Until(next).Round(time.Second).String())
			}
		}
	}

	if def.MaxConcurrent > 0 {
		fields = append(fields,
			"max_concurrent", def.MaxConcurrent,
			"on_conflict", def.Concurrency.String())
	}
	if r.reg.IsPaused(def.Name) {
		fields = append(fields, "paused", true)
	}
	return fields
}

// Stop drains the runner: no new runs are picked up and in-flight ones are given
// until ctx expires to finish. Whatever is still running then is canceled and
// put back on the queue — never marked failed, because it never got to fail.
func (r *JobRunner) Stop(ctx context.Context) IError {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.stopped = true
	close(r.stopCh)
	r.mu.Unlock()

	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-ctx.Done():
		r.app.Log().Warn("job runner: draining timed out, asking in-flight runs to stop")
		r.cancelInflight()
		// each handler is bounded by its stop grace, so the workers do return;
		// they put their unfinished run back on the queue as they go
		<-done
	}
	return r.queue.Close()
}

// isStopping reports whether Stop has been called.
func (r *JobRunner) isStopping() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// cancelInflight asks every running job to stop.
func (r *JobRunner) cancelInflight() {
	r.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.inflight))
	for _, cancel := range r.inflight {
		cancels = append(cancels, cancel)
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// requeueForShutdown returns an interrupted run to the queue. It is deliberately
// not a failure: the run never got the chance to fail.
func (r *JobRunner) requeueForShutdown(ctx context.Context, run *JobRun) {
	run.Status, run.StartedAt, run.WorkerID = RunQueued, nil, ""
	run.ScheduledAt, run.UpdatedAt = time.Now(), time.Now()
	if err := r.store.Update(ctx, run); err != nil {
		r.app.Log().Error("job runner: cannot requeue interrupted run", "run_id", run.ID, "err", err)
		return
	}
	if err := r.enqueue(ctx, run); err != nil && !errors.Is(err, ErrQueueClosed) {
		r.app.Log().Error("job runner: cannot enqueue interrupted run", "run_id", run.ID, "err", err)
	}
	r.app.Log().Info("job run interrupted by shutdown, returned to the queue",
		"job", run.JobName, "run_id", run.ID)
}

// ---------------------------------------------------------------------------
// Triggering
// ---------------------------------------------------------------------------

// TriggerOptions tunes a single manual trigger.
type TriggerOptions struct {
	// By records who asked for the run (a user id, "scheduler", …).
	By string
	// IdemKey makes triggering idempotent: while a run with the same key is
	// still active, triggering again returns that run instead of creating a
	// second one. This is what stops a double-clicked button from running twice.
	IdemKey string
	// Delay postpones the run.
	Delay time.Duration
	// Trigger overrides the recorded cause (default TriggerManual).
	Trigger Trigger
	// CaptureLogs overrides the log policy for this run only.
	CaptureLogs *LogPolicy
	// MaxAttempts overrides the job's retry count for this run.
	MaxAttempts int
}

// Trigger queues a run of name with params (any JSON-serialisable value, or nil
// for none) and returns the created run.
func (r *JobRunner) Trigger(ctx context.Context, name string, params any, opts ...TriggerOptions) (*JobRun, IError) {
	var o TriggerOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	job, ok := r.reg.get(name)
	if !ok {
		return nil, Newf(http.StatusNotFound, "JOB_NOT_FOUND", "job %s is not registered", name)
	}
	if job.paused {
		return nil, Newf(http.StatusConflict, "JOB_PAUSED", "job %s is paused", name)
	}
	if o.IdemKey != "" {
		if existing, err := r.store.FindActive(ctx, name, o.IdemKey); err == nil && existing != nil {
			return existing, nil
		}
	}
	raw, err := encodeParams(params)
	if err != nil {
		return nil, err
	}

	maxAttempts := job.def.maxAttempts()
	if o.MaxAttempts > 0 {
		maxAttempts = o.MaxAttempts
	}
	trigger := o.Trigger
	if trigger == "" {
		trigger = TriggerManual
	}
	now := time.Now()
	run := &JobRun{
		ID:          uuid.NewString(),
		JobName:     name,
		Queue:       job.def.queue(),
		Params:      raw,
		Status:      RunQueued,
		Trigger:     trigger,
		TriggeredBy: o.By,
		IdemKey:     o.IdemKey,
		Attempt:     1,
		MaxAttempts: maxAttempts,
		ScheduledAt: now.Add(o.Delay),
		CaptureLogs: o.CaptureLogs,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	run.RootID = run.ID
	if err := r.store.Create(ctx, run); err != nil {
		return nil, err
	}
	if err := r.enqueue(ctx, run); err != nil {
		return nil, err
	}
	return run, nil
}

// TriggerAndWait triggers a run and waits for it to finish, for the callers who
// need the outcome (an admin clicking "run now" and watching).
func (r *JobRunner) TriggerAndWait(ctx context.Context, name string, params any, opts ...TriggerOptions) (*JobRun, IError) {
	run, err := r.Trigger(ctx, name, params, opts...)
	if err != nil {
		return nil, err
	}
	return r.Wait(ctx, run.ID)
}

// Wait blocks until the run reaches a terminal status or ctx is done.
func (r *JobRunner) Wait(ctx context.Context, runID string) (*JobRun, IError) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err := r.store.Get(ctx, runID)
		if err != nil {
			return nil, err
		}
		if run.Status.IsTerminal() {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return run, Wrap(ctx.Err(), "job runner: wait")
		case <-ticker.C:
		}
	}
}

// Replay re-runs a finished run as a *new* run, keeping the original intact —
// history must never be overwritten.
type ReplayOptions struct {
	By string
	// Params overrides the original parameters when non-nil.
	Params any
	// CaptureLogs overrides the log policy for the replay.
	CaptureLogs *LogPolicy
}

// Replay creates a new run from a finished one.
func (r *JobRunner) Replay(ctx context.Context, runID string, opts ...ReplayOptions) (*JobRun, IError) {
	var o ReplayOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	old, err := r.store.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if !old.Status.IsTerminal() {
		return nil, Newf(http.StatusConflict, "JOB_RUN_NOT_FINISHED",
			"run %s is %s — cancel it instead of replaying it", runID, old.Status)
	}
	job, ok := r.reg.get(old.JobName)
	if !ok {
		return nil, Newf(http.StatusNotFound, "JOB_NOT_FOUND", "job %s is not registered", old.JobName)
	}
	if !job.def.replayable() {
		return nil, Newf(http.StatusForbidden, "JOB_NOT_REPLAYABLE", "job %s cannot be replayed", old.JobName)
	}

	raw := old.Params
	if o.Params != nil {
		if raw, err = encodeParams(o.Params); err != nil {
			return nil, err
		}
	}
	root := old.RootID
	if root == "" {
		root = old.ID
	}
	now := time.Now()
	run := &JobRun{
		ID:          uuid.NewString(),
		JobName:     old.JobName,
		Queue:       job.def.queue(),
		Params:      raw,
		Status:      RunQueued,
		Trigger:     TriggerReplay,
		TriggeredBy: o.By,
		Attempt:     1,
		MaxAttempts: job.def.maxAttempts(),
		ScheduledAt: now,
		CaptureLogs: o.CaptureLogs,
		ReplayOf:    old.ID,
		RootID:      root,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := r.store.Create(ctx, run); err != nil {
		return nil, err
	}
	if err := r.enqueue(ctx, run); err != nil {
		return nil, err
	}
	return run, nil
}

// Cancel stops a run. A queued run never starts; a running one is asked to stop
// and its context is canceled — the handler has to cooperate (see
// ICronjobContext.Stopping).
func (r *JobRunner) Cancel(ctx context.Context, runID, by, reason string) IError {
	run, err := r.store.Get(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status.IsTerminal() {
		return Newf(http.StatusConflict, "JOB_RUN_FINISHED",
			"run %s already finished as %s", runID, run.Status)
	}
	if err := r.store.RequestCancel(ctx, runID, by, reason); err != nil {
		return err
	}

	// queued: pull it out of the queue and finish it without ever starting
	if removed, _ := r.queue.Remove(ctx, runID); removed {
		fresh, gerr := r.store.Get(ctx, runID)
		if gerr != nil {
			return gerr
		}
		now := time.Now()
		fresh.Status, fresh.FinishedAt, fresh.UpdatedAt = RunCanceled, &now, now
		return r.store.Update(ctx, fresh)
	}

	// running here (or on another process, which will see the flag when polling)
	r.mu.Lock()
	cancel := r.inflight[runID]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Pause stops a job being scheduled or triggered; Resume undoes it.
func (r *JobRunner) Pause(name string) IError  { return r.reg.Pause(name) }
func (r *JobRunner) Resume(name string) IError { return r.reg.Resume(name) }

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// Run returns a single run.
func (r *JobRunner) Run(ctx context.Context, id string) (*JobRun, IError) {
	return r.store.Get(ctx, id)
}

// NextRun is when a job fires next, and whether it is armed at all in this
// process.
//
// False covers two cases that look identical from outside and are not: a
// manual-only job, and a scheduled job in a process whose role does not arm the
// cron — an API replica whose worker runs elsewhere. Both are correct silence.
// What it is worth for is the third case: a job that is supposed to be armed
// here and is not, which is where every "why has it not run?" starts.
//
// Only a runner a Scheduler was built on can answer at all; the times come from
// gocron, so they exist once the scheduler is ticking.
func (r *JobRunner) NextRun(name string) (time.Time, bool) {
	nextRun := r.nextRunFn()
	if nextRun == nil {
		return time.Time{}, false
	}
	return nextRun(name)
}

// Scheduled reports whether a scheduler is feeding this runner, which is what
// tells "no next run because nothing is armed here" apart from "no next run
// because this job is manual".
func (r *JobRunner) Scheduled() bool { return r.nextRunFn() != nil }

// Runs lists runs.
func (r *JobRunner) Runs(ctx context.Context, f JobRunFilter) (*Page[JobRun], IError) {
	return r.store.List(ctx, f)
}

// Logs returns persisted log lines after afterSeq. It is empty for runs whose
// policy did not persist anything — use TailLogs while a run is in flight.
func (r *JobRunner) Logs(ctx context.Context, runID string, afterSeq int64, limit int) ([]JobLog, IError) {
	return r.store.Logs(ctx, runID, afterSeq, limit)
}

// TailLogs streams the lines of a run *while it is running*, without touching
// the store — live debugging keeps working under LogOff. The returned function
// unsubscribes.
func (r *JobRunner) TailLogs(runID string) (<-chan JobLog, func()) {
	return r.hub.subscribe(runID, 0)
}

// Purge deletes runs and logs past their retention. Register it as a job to run
// it on a schedule.
func (r *JobRunner) Purge(ctx context.Context) (int64, IError) {
	retainRuns, retainLogs := DefaultRetainRuns, DefaultRetainLogs
	for _, def := range r.reg.List() {
		if def.RetainRuns > retainRuns {
			retainRuns = def.RetainRuns
		}
		if def.RetainLogs > retainLogs {
			retainLogs = def.RetainLogs
		}
	}
	now := time.Now()
	return r.store.Purge(ctx, now.Add(-retainRuns), now.Add(-retainLogs))
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

func (r *JobRunner) worker() {
	defer r.wg.Done()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-r.stopCh
		cancel()
	}()

	for {
		// never pick up new work once draining has begun — including work this
		// runner just put back on the queue
		select {
		case <-r.stopCh:
			return
		default:
		}

		run, ack, err := r.queue.Reserve(ctx, r.queues)
		if err != nil {
			select {
			case <-r.stopCh:
				return
			default:
			}
			if errors.Is(err, ErrQueueClosed) {
				return
			}
			r.app.Log().Error("job runner: reserve failed", "err", err)
			time.Sleep(time.Second)
			continue
		}
		r.execute(run, ack)
	}
}

// execute takes a reserved run all the way to a terminal status.
func (r *JobRunner) execute(run *JobRun, ack AckFunc) {
	ctx := context.Background()
	job, ok := r.reg.get(run.JobName)
	if !ok {
		r.finish(ctx, run, nil, Newf(http.StatusNotFound, "JOB_NOT_FOUND",
			"job %s is not registered in this process", run.JobName), nil, nil)
		ack(nil)
		return
	}
	def := job.def

	// canceled while it was sitting in the queue
	if canceled, _ := r.store.IsCancelRequested(ctx, run.ID); canceled {
		r.markCanceled(ctx, run)
		ack(nil)
		return
	}

	release, ok := r.acquireSlots(ctx, run, def)
	if !ok {
		ack(nil)
		return
	}
	defer release()

	now := time.Now()
	run.Status, run.StartedAt, run.WorkerID, run.UpdatedAt = RunRunning, &now, r.workerID, now
	if err := r.store.Update(ctx, run); err != nil {
		r.app.Log().Error("job runner: cannot mark run as running", "run_id", run.ID, "err", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, def.timeout())
	// a run is a unit of work like a request: give it its own Sentry hub,
	// transaction and cron check-in before anything else happens on it
	runCtx, trace := r.beginJobTrace(runCtx, run, def)
	r.mu.Lock()
	r.inflight[run.ID] = cancel
	r.mu.Unlock()

	stopWatch := r.watchCancel(runCtx, run.ID, cancel)

	sink := newRunLogSink(run.ID, run.Attempt, r.store, r.hub, r.effectiveLogPolicy(def, run), r.logLimits)
	runContext := r.app.NewContext(runCtx, ModeCron)
	// same scope for every capture path of this run, exactly as for a request
	bindSentryScope(runCtx, runContext, "")
	// the run's own context logger, so every line is a breadcrumb on this run
	base := runContext.Log().With("job", run.JobName, "run_id", run.ID, "attempt", run.Attempt)
	jc := &jobContext{
		IContext: runContext,
		runner:   r,
		log:      newRunLogger(base, sink),
		name:     run.JobName,
		runID:    run.ID,
		attempt:  run.Attempt,
		trigger:  run.Trigger,
		params:   run.Params,
	}

	err := r.callHandler(jc, job.fn, def, run.ID)

	stopWatch()
	cancel()
	r.mu.Lock()
	delete(r.inflight, run.ID)
	r.mu.Unlock()

	canceled, _ := r.store.IsCancelRequested(ctx, run.ID)
	r.finish(ctx, run, jc, err, sink, trace)
	_ = canceled
	ack(nil)
}

// callHandler runs fn with panic recovery, honouring the stop grace period: a
// handler that ignores cancellation must not pin a worker forever.
func (r *JobRunner) callHandler(jc *jobContext, fn JobFunc, def JobDef, runID string) error {
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() { done <- err }()
		defer Recover(&err)
		err = fn(jc)
	}()

	select {
	case err := <-done:
		return err
	case <-jc.Done():
		// asked to stop (cancel, timeout or shutdown) — give it the grace period
		grace := time.NewTimer(def.stopGrace())
		defer grace.Stop()
		select {
		case err := <-done:
			return err
		case <-grace.C:
			r.app.Log().Warn("job ignored cancellation; abandoning it",
				"job", def.Name, "run_id", runID, "grace", def.stopGrace())
			return Wrap(context.Canceled, "job did not stop within its grace period")
		}
	}
}

// acquireSlots takes the queue and job concurrency slots, applying the job's
// policy when they are not available.
func (r *JobRunner) acquireSlots(ctx context.Context, run *JobRun, def JobDef) (func(), bool) {
	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}

	if limit := r.queueLimits[run.Queue]; limit > 0 {
		rel, ok := r.limiter.Acquire(ctx, limiterKeyQueue(run.Queue), limit)
		if !ok {
			r.onSlotUnavailable(ctx, run, def, "queue is at its concurrency limit")
			return releaseAll, false
		}
		releases = append(releases, rel)
	}
	if def.MaxConcurrent > 0 {
		rel, ok := r.limiter.Acquire(ctx, limiterKeyJob(def.Name), def.MaxConcurrent)
		if !ok {
			releaseAll()
			r.onSlotUnavailable(ctx, run, def, "job is at its concurrency limit")
			return func() {}, false
		}
		releases = append(releases, rel)
	}
	return releaseAll, true
}

// onSlotUnavailable applies ConcurrencyPolicy to a run that could not start.
func (r *JobRunner) onSlotUnavailable(ctx context.Context, run *JobRun, def JobDef, reason string) {
	switch def.Concurrency {
	case ConcurrencySkip:
		now := time.Now()
		run.Status, run.FinishedAt, run.UpdatedAt = RunSkipped, &now, now
		run.Error = &RunError{Code: "SKIPPED", Message: reason}
		_ = r.store.Update(ctx, run)
		r.app.Log().Info("job run skipped", "job", def.Name, "run_id", run.ID, "reason", reason)

	case ConcurrencyReplace:
		// latest wins: ask whatever is running to stop, then queue behind it
		ids, _ := r.store.RunningIDs(ctx, def.Name)
		for _, id := range ids {
			if id == run.ID {
				continue
			}
			_ = r.Cancel(ctx, id, "runner", "replaced by a newer run")
		}
		r.requeueForSlot(ctx, run)

	default: // ConcurrencyEnqueue
		r.requeueForSlot(ctx, run)
	}
}

// requeueForSlot puts the run back with a short delay so it can try again.
func (r *JobRunner) requeueForSlot(ctx context.Context, run *JobRun) {
	run.ScheduledAt = time.Now().Add(r.slotBackoff)
	run.Status, run.UpdatedAt = RunQueued, time.Now()
	_ = r.store.Update(ctx, run)
	if err := r.enqueue(ctx, run); err != nil {
		r.app.Log().Error("job runner: cannot requeue run", "run_id", run.ID, "err", err)
	}
}

// finish writes the terminal status, retrying first when the job has attempts
// left.
func (r *JobRunner) finish(ctx context.Context, run *JobRun, jc *jobContext, err error, sink *runLogSink, trace *jobTrace) {
	now := time.Now()
	run.UpdatedAt = now
	if run.StartedAt != nil {
		run.DurationMS = now.Sub(*run.StartedAt).Milliseconds()
	}
	if jc != nil {
		run.Result = jc.takeResult()
		jc.mu.Lock()
		run.Progress, run.ProgressMessage = jc.progress, jc.progMsg
		jc.mu.Unlock()
	}

	switch {
	case err == nil:
		run.Status, run.FinishedAt = RunSucceeded, &now
		if run.Progress == 0 {
			run.Progress = 100
		}
		r.app.Log().Info("job run succeeded",
			"job", run.JobName, "run_id", run.ID, "duration_ms", run.DurationMS)
		trace.finish(RunSucceeded, nil)

	case r.wasCanceled(ctx, run, err):
		// a cancel nobody asked for, during shutdown, is an interruption: put
		// the run back rather than burying it as canceled
		if requested, _ := r.store.IsCancelRequested(ctx, run.ID); !requested && r.isStopping() {
			if sink != nil {
				sink.close(ctx, false)
			}
			trace.finish(RunCanceled, nil)
			r.requeueForShutdown(ctx, run)
			return
		}
		run.Status, run.FinishedAt = RunCanceled, &now
		run.Error = r.runError(err, sink)
		r.app.Log().Info("job run canceled", "job", run.JobName, "run_id", run.ID)
		trace.finish(RunCanceled, nil)

	case run.Attempt < run.MaxAttempts:
		// retry: same chain, next attempt, backoff
		def, _ := r.reg.Def(run.JobName)
		delay := def.backoff()(run.Attempt + 1)
		run.Attempt++
		run.Status, run.Trigger = RunQueued, TriggerRetry
		run.StartedAt, run.WorkerID = nil, ""
		run.ScheduledAt = now.Add(delay)
		run.Error = r.runError(err, sink)
		if uerr := r.store.Update(ctx, run); uerr != nil {
			r.app.Log().Error("job runner: cannot record retry", "run_id", run.ID, "err", uerr)
		}
		r.app.Log().Warn("job run failed, retrying",
			"job", run.JobName, "run_id", run.ID, "attempt", run.Attempt, "in", delay, "err", err)
		trace.captureJobFailure(run, err, false)
		trace.finish(RunFailed, err)
		if sink != nil {
			sink.close(ctx, false)
		}
		if eerr := r.enqueue(ctx, run); eerr != nil {
			r.app.Log().Error("job runner: cannot enqueue retry", "run_id", run.ID, "err", eerr)
		}
		return

	default:
		run.Status, run.FinishedAt = RunFailed, &now
		run.Error = r.runError(err, sink)
		r.app.Log().Error("job run failed",
			"job", run.JobName, "run_id", run.ID, "attempt", run.Attempt, "err", err)
		trace.captureJobFailure(run, err, true)
		trace.finish(RunFailed, err)
	}

	if uerr := r.store.Update(ctx, run); uerr != nil {
		r.app.Log().Error("job runner: cannot record result", "run_id", run.ID, "err", uerr)
	}
	if sink != nil {
		sink.close(ctx, run.Status == RunSucceeded)
	}
}

// wasCanceled distinguishes "someone stopped it" from "it failed".
func (r *JobRunner) wasCanceled(ctx context.Context, run *JobRun, err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false // a timeout is a failure, not a cancellation
	}
	canceled, cerr := r.store.IsCancelRequested(ctx, run.ID)
	return cerr == nil && canceled
}

// runError builds the stored error, always attaching the tail of the log buffer
// so a failed run is debuggable even when logs are not persisted.
func (r *JobRunner) runError(err error, sink *runLogSink) *RunError {
	if err == nil {
		return nil
	}
	out := &RunError{Message: err.Error()}

	// any IError — not just *core.Error — keeps its code, status and field
	// violations, so a params failure from the valid package reads on a run
	// exactly as it would on an HTTP response
	var ierr IError
	if errors.As(err, &ierr) {
		out.Code, out.Status = ierr.GetCode(), ierr.GetStatus()
		if msg, ok := ierr.GetMessage().(string); ok && msg != "" {
			out.Message = msg
		}
		out.Fields = errorFields(ierr)
	}
	if e := From(err); e != nil {
		out.Stack = e.StackString()
	}
	if sink != nil {
		out.Tail = sink.tail()
	}
	return out
}

// errorFields pulls the per-field violations out of an error's JSON body. It
// works for core.Error and for the valid package's error alike, without core
// having to import valid (which imports core).
func errorFields(ierr IError) json.RawMessage {
	body, err := json.Marshal(ierr.JSON())
	if err != nil {
		return nil
	}
	var shape struct {
		Fields json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(body, &shape); err != nil {
		return nil
	}
	if len(shape.Fields) == 0 || string(shape.Fields) == "null" {
		return nil
	}
	return shape.Fields
}

// markCanceled finishes a run that was canceled before it ever started.
func (r *JobRunner) markCanceled(ctx context.Context, run *JobRun) {
	now := time.Now()
	run.Status, run.FinishedAt, run.UpdatedAt = RunCanceled, &now, now
	_ = r.store.Update(ctx, run)
}

// watchCancel polls the store so a cancellation recorded elsewhere still stops
// this run. It returns a function that stops watching.
func (r *JobRunner) watchCancel(ctx context.Context, runID string, cancel context.CancelFunc) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(cancelPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if canceled, err := r.store.IsCancelRequested(context.Background(), runID); err == nil && canceled {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stop) }) }
}

// effectiveLogPolicy resolves runner default → job → per-run override.
func (r *JobRunner) effectiveLogPolicy(def JobDef, run *JobRun) LogPolicy {
	policy := r.logPolicy
	if def.Logs != nil {
		policy = *def.Logs
	}
	if run.CaptureLogs != nil {
		policy = *run.CaptureLogs
	}
	return policy
}

// reportProgress persists a progress update from a running job.
func (r *JobRunner) reportProgress(runID string, percent int, message string) {
	ctx := context.Background()
	run, err := r.store.Get(ctx, runID)
	if err != nil {
		return
	}
	run.Progress, run.ProgressMessage, run.UpdatedAt = percent, message, time.Now()
	_ = r.store.Update(ctx, run)
}

// encodeParams turns trigger params into stored JSON.
func encodeParams(params any) (json.RawMessage, IError) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, New(http.StatusBadRequest, "INVALID_PARAMS", err.Error())
	}
	return b, nil
}

// LogPolicyPtr is a helper for the pointer fields on JobDef/TriggerOptions.
func LogPolicyPtr(p LogPolicy) *LogPolicy { return &p }

// BoolPtr is a helper for JobDef.Replayable.
func BoolPtr(b bool) *bool { return &b }
