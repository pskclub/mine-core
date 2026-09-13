package core

import (
	"context"
	"time"
)

// jobTrace is everything Sentry needs to know about one job run: the hub the run
// captures on, the transaction it is timed by, and the cron check-in it opened.
type jobTrace struct {
	tracker  ISentry
	span     ISpan
	checkIn  string
	monitor  string
	schedule Schedule
	// timezone is the scheduler's, for schedules that did not name one of their
	// own — without it Sentry reads the hour as UTC and calls every on-time run
	// of a job scheduled elsewhere a missed one.
	timezone string
	def      JobDef
	run      *JobRun
	started  time.Time
}

// beginJobTrace gives a run its own Sentry hub — tagged with the job, the run
// and the attempt — so anything the handler reports lands on that run and not on
// a neighbouring one. It returns the context the handler must use.
//
// A run is a unit of work exactly like a request is, and it is treated as one:
// same scope isolation, same breadcrumbs, same transaction, plus the cron
// check-in an HTTP request has no equivalent of.
func (r *JobRunner) beginJobTrace(ctx context.Context, run *JobRun, def JobDef) (context.Context, *jobTrace) {
	tracker := r.app.Sentry()
	if !tracker.Enabled() {
		return ctx, nil
	}
	ctx, hub := withHub(ctx, tracker)
	if hub == nil {
		return ctx, nil
	}

	hub.Scope().SetTags(map[string]string{
		"mode":        ModeCron.String(),
		"job":         run.JobName,
		"run_id":      run.ID,
		"queue":       run.Queue,
		"trigger":     string(run.Trigger),
		"attempt":     statusTag(run.Attempt),
		"worker_id":   r.workerID,
		"replay_of":   run.ReplayOf,
		"triggeredby": run.TriggeredBy,
	})
	// the same identity as attributes, which is what logs and metrics carry
	pinAttributes(hub, map[string]string{
		"mode":    ModeCron.String(),
		"job":     run.JobName,
		"run_id":  run.ID,
		"queue":   run.Queue,
		"trigger": string(run.Trigger),
		"attempt": statusTag(run.Attempt),
	})
	hub.Scope().SetContext("job", map[string]any{
		"name":         run.JobName,
		"run_id":       run.ID,
		"queue":        run.Queue,
		"trigger":      string(run.Trigger),
		"attempt":      run.Attempt,
		"max_attempts": run.MaxAttempts,
		"scheduled_at": run.ScheduledAt,
		"triggered_by": run.TriggeredBy,
		"params":       string(run.Params),
		"timeout":      def.timeout().String(),
	})

	bound := tracker.WithContext(ctx)
	trace := &jobTrace{
		tracker:  bound,
		def:      def,
		run:      run,
		monitor:  run.JobName,
		schedule: def.Schedule,
		timezone: r.schedulerZone(),
		started:  time.Now(),
	}
	trace.span = bound.StartTransaction("job "+run.JobName, "queue.task")
	ctx = trace.span.Context()
	trace.tracker = tracker.WithContext(ctx)

	// A check-in only makes sense for runs Sentry expects: a scheduled tick.
	// Manual triggers and replays would look like unscheduled noise on the
	// monitor's timeline.
	if sentryOf(tracker).crons && def.Schedule != nil && run.Trigger == TriggerSchedule {
		trace.checkIn = trace.tracker.SendCheckIn(CheckIn{
			Monitor:    trace.monitor,
			Status:     CheckInProgress,
			Schedule:   trace.schedule,
			Timezone:   trace.timezone,
			MaxRuntime: def.timeout(),
		})
	}
	return ctx, trace
}

// finish closes the transaction and the check-in with the run's outcome. It is
// safe on a nil trace, so the call site never branches.
func (t *jobTrace) finish(status RunStatus, err error) {
	if t == nil {
		return
	}
	if t.span != nil {
		t.span.SetTag("job.status", string(status))
		t.span.Finish(err)
	}
	if t.checkIn == "" {
		return
	}
	checkStatus := CheckInOK
	if status == RunFailed {
		checkStatus = CheckInError
	}
	t.tracker.SendCheckIn(CheckIn{
		ID:       t.checkIn,
		Monitor:  t.monitor,
		Status:   checkStatus,
		Duration: time.Since(t.started),
		Schedule: t.schedule,
		Timezone: t.timezone,
	})
}

// captureJobFailure reports a failed run. Retries are silent by default — an
// incident is a job that ran out of attempts, not a blip that recovered — but
// SentryOptions.CaptureRetries reports every attempt when you need to see them.
func (t *jobTrace) captureJobFailure(run *JobRun, err error, final bool) {
	if t == nil || err == nil {
		return
	}
	cfg := sentryOf(t.tracker)
	if !final && !cfg.captureRetries {
		return
	}
	if !captureStatus(err, cfg.minStatus) {
		return
	}
	opts := []CaptureOption{
		CaptureTag("job", run.JobName),
		CaptureTag("job.final", boolTag(final)),
		CaptureExtra("run", map[string]any{
			"id":           run.ID,
			"attempt":      run.Attempt,
			"max_attempts": run.MaxAttempts,
			"duration_ms":  run.DurationMS,
			"progress":     run.Progress,
		}),
		// group by job + code: one broken job is one issue, however many runs
		// it fails on
		CaptureFingerprint("job", run.JobName, errorCodeOf(err)),
	}
	if run.Error != nil && len(run.Error.Tail) > 0 {
		opts = append(opts, CaptureExtra("log_tail", run.Error.Tail))
	}
	if e := From(err); e != nil && e.eventID != "" {
		return // already reported where it was built
	}
	t.tracker.CaptureError(err, opts...)
}

func errorCodeOf(err error) string {
	if e := From(err); e != nil && e.GetCode() != "" {
		return e.GetCode()
	}
	return "ERROR"
}

func boolTag(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
