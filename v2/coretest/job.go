package coretest

import (
	"context"
	"testing"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// jobWait bounds how long RunJob waits for a run to finish before calling it
// stuck. Generous: a slow query on a cold database should not fail a test.
const jobWait = 30 * time.Second

// Job runs job handlers against a real runner, so a test exercises the same path
// a schedule or a manual trigger would: parameters are validated, panics become
// errors, and the run is recorded with a status.
type Job struct {
	t      *testing.T
	app    *core.App
	runner *core.JobRunner
}

// NewJob builds a runner for the given handlers.
//
//	j := coretest.NewJob(t, coretest.WithAutoMigrate(&Report{}))
//	j.Register("nightly-report", jobs.NightlyReport)
//
//	run := j.Run("nightly-report", nil)
//	require.Equal(t, core.JobSucceeded, run.Status)
func NewJob(t *testing.T, opts ...Option) *Job {
	t.Helper()

	app := NewApp(t, opts...)
	return NewJobWithApp(t, app)
}

// NewJobWithApp reuses an App, so jobs and HTTP handlers in one test share a
// database.
func NewJobWithApp(t *testing.T, app *core.App) *Job {
	t.Helper()

	runner := core.NewJobRunner(app, core.NewJobRegistry())
	runner.Start()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), jobWait)
		defer cancel()
		_ = runner.Stop(stopCtx)
	})

	return &Job{t: t, app: app, runner: runner}
}

// Register adds a handler under a name.
func (j *Job) Register(name string, fn core.JobFunc) *Job {
	j.t.Helper()

	if err := j.runner.Registry().Register(core.JobDef{Name: name}, fn); err != nil {
		j.t.Fatalf("coretest: register job %q: %v", name, err)
	}
	return j
}

// RegisterDef adds a handler with a full definition — retries, timeout,
// concurrency limits — for tests that exercise those.
func (j *Job) RegisterDef(def core.JobDef, fn core.JobFunc) *Job {
	j.t.Helper()

	if err := j.runner.Registry().Register(def, fn); err != nil {
		j.t.Fatalf("coretest: register job %q: %v", def.Name, err)
	}
	return j
}

// Run triggers a job and waits for it to finish, returning the recorded run.
// params is encoded like a real trigger, so a job's Params/validation runs.
//
// A job that fails does not fail the test: assert on run.Status and run.Error,
// since a failing run is often exactly what is being tested.
func (j *Job) Run(name string, params any) *core.JobRun {
	j.t.Helper()

	ctx, cancel := context.WithTimeout(j.t.Context(), jobWait)
	defer cancel()

	run, err := j.runner.TriggerAndWait(ctx, name, params)
	if err != nil {
		j.t.Fatalf("coretest: run job %q: %v", name, err)
	}
	return run
}

// Runner exposes the runner for anything this wrapper does not cover — queue
// inspection, cancellation, replay.
func (j *Job) Runner() *core.JobRunner { return j.runner }

// App exposes the App the runner was built on.
func (j *Job) App() *core.App { return j.app }

// Context returns a context on the same pools, for seeding or asserting outside
// the job.
func (j *Job) Context() core.IContext {
	return j.app.NewContext(j.t.Context(), core.ModeTest)
}
