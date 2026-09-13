//go:build integration

package jobstore_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/jobstore"
	"github.com/pskclub/mine-core/v2/repository"
)

// newDurableRunner wires a runner to the SQL-backed store and queue — the setup
// a service uses in production.
func newDurableRunner(t *testing.T, app *core.App, reg *core.JobRegistry, opts ...core.JobRunnerOption) *core.JobRunner {
	t.Helper()
	opts = append([]core.JobRunnerOption{
		core.WithJobStore(jobstore.New(app)),
		core.WithJobQueue(jobstore.NewQueue(app, jobstore.WithPollInterval(20*time.Millisecond))),
		core.WithSlotBackoff(30 * time.Millisecond),
	}, opts...)
	r := core.NewJobRunner(app, reg, opts...)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Stop(stopCtx)
	})
	return r
}

func waitRun(t *testing.T, r *core.JobRunner, id string) *core.JobRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, err := r.Wait(ctx, id)
	require.NoError(t, err)
	return run
}

// The whole point of the durable backend: a run survives the process that
// created it. Runner A queues the work and dies; runner B, over the same
// database, picks it up.
func TestDurable_runSurvivesTheProcessThatQueuedIt(t *testing.T) {
	app := sqliteApp(t)
	ctx := ctxT(t)

	regA := core.NewJobRegistry()
	require.NoError(t, regA.Register(core.JobDef{Name: "handover"},
		func(c core.ICronjobContext) error { return nil }))
	runnerA := core.NewJobRunner(app, regA,
		core.WithJobStore(jobstore.New(app)),
		core.WithJobQueue(jobstore.NewQueue(app, jobstore.WithPollInterval(20*time.Millisecond))),
	)
	// deliberately never started: the run is created and left queued
	queued, err := runnerA.Trigger(ctx, "handover", map[string]any{"n": 1},
		core.TriggerOptions{By: "admin"})
	require.NoError(t, err)
	assert.Equal(t, core.RunQueued, queued.Status)

	stopCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	require.NoError(t, runnerA.Stop(stopCtx))

	// a fresh process, same database
	regB := core.NewJobRegistry()
	ran := make(chan map[string]any, 1)
	require.NoError(t, core.RegisterJob(regB, core.JobDef{Name: "handover"},
		func(c core.ICronjobContext, p map[string]any) error {
			ran <- p
			return nil
		}))
	runnerB := newDurableRunner(t, app, regB)
	runnerB.Start()

	select {
	case p := <-ran:
		assert.EqualValues(t, 1, p["n"], "the parameters survived too")
	case <-time.After(10 * time.Second):
		t.Fatal("the queued run was never picked up by the second process")
	}
	done := waitRun(t, runnerB, queued.ID)
	assert.Equal(t, core.RunSucceeded, done.Status)
	assert.Equal(t, "admin", done.TriggeredBy)
}

// Two runners against one database must not both execute the same run. This is
// the claim in repoQueue doing its job.
func TestDurable_twoRunnersNeverExecuteTheSameRunTwice(t *testing.T) {
	app := sqliteApp(t)
	ctx := ctxT(t)

	var mu sync.Mutex
	executions := map[string]int{}
	build := func() *core.JobRegistry {
		reg := core.NewJobRegistry()
		require.NoError(t, reg.Register(core.JobDef{Name: "exclusive"},
			func(c core.ICronjobContext) error {
				mu.Lock()
				executions[c.RunID()]++
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				return nil
			}))
		return reg
	}

	runnerA := newDurableRunner(t, app, build(), core.WithWorkers(2), core.WithWorkerID("A"))
	runnerB := newDurableRunner(t, app, build(), core.WithWorkers(2), core.WithWorkerID("B"))

	ids := make([]string, 0, 10)
	for range 10 {
		run, err := runnerA.Trigger(ctx, "exclusive", nil)
		require.NoError(t, err)
		ids = append(ids, run.ID)
	}

	runnerA.Start()
	runnerB.Start()

	for _, id := range ids {
		assert.Equal(t, core.RunSucceeded, waitRun(t, runnerA, id).Status)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, executions, 10)
	for id, n := range executions {
		assert.Equal(t, 1, n, "run %s executed %d times", id, n)
	}
}

// Cancelling a queued run must be atomic against the claim: it can never start
// afterwards, even with workers actively polling.
func TestDurable_cancelBeatsTheWorker(t *testing.T) {
	app := sqliteApp(t)
	ctx := ctxT(t)

	reg := core.NewJobRegistry()
	var ran int32
	require.NoError(t, reg.Register(core.JobDef{Name: "cancel-me"},
		func(c core.ICronjobContext) error {
			atomic.AddInt32(&ran, 1)
			return nil
		}))
	runner := newDurableRunner(t, app, reg)
	runner.Start()

	run, err := runner.Trigger(ctx, "cancel-me", nil, core.TriggerOptions{Delay: time.Hour})
	require.NoError(t, err)
	require.NoError(t, runner.Cancel(ctx, run.ID, "admin", "changed my mind"))

	got, gerr := runner.Run(ctx, run.ID)
	require.NoError(t, gerr)
	assert.Equal(t, core.RunCanceled, got.Status)
	assert.Equal(t, "admin", got.CanceledBy)
	assert.Equal(t, "changed my mind", got.CancelReason)

	time.Sleep(200 * time.Millisecond)
	assert.Zero(t, atomic.LoadInt32(&ran))
}

// Retries live in the same row: the attempt counter and the schedule move, and
// the history stays on one record.
func TestDurable_retriesArePersistedOnTheSameRun(t *testing.T) {
	app := sqliteApp(t)
	ctx := ctxT(t)

	reg := core.NewJobRegistry()
	var attempts int32
	require.NoError(t, reg.Register(core.JobDef{
		Name:        "flaky",
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 20 * time.Millisecond },
		Logs:        core.LogPolicyPtr(core.LogOnFailure),
	}, func(c core.ICronjobContext) error {
		c.Log().Info("trying", "attempt", c.Attempt())
		if atomic.AddInt32(&attempts, 1) < 3 {
			return errors.New("transient")
		}
		return nil
	}))
	runner := newDurableRunner(t, app, reg)
	runner.Start()

	run, err := runner.Trigger(ctx, "flaky", nil)
	require.NoError(t, err)
	done := waitRun(t, runner, run.ID)

	assert.Equal(t, core.RunSucceeded, done.Status)
	assert.Equal(t, 3, done.Attempt)

	page, lerr := runner.Runs(ctx, core.JobRunFilter{JobName: "flaky"})
	require.NoError(t, lerr)
	assert.EqualValues(t, 1, page.Total, "retries reuse the run, they do not multiply it")

	// LogOnFailure kept the failed attempts and dropped the successful one
	logs, logErr := runner.Logs(ctx, run.ID, 0, 100)
	require.NoError(t, logErr)
	require.Len(t, logs, 2, "the two failed attempts were persisted")
	assert.Equal(t, "trying", logs[0].Message)
	assert.EqualValues(t, 1, logs[0].Attrs["attempt"])
}

func TestDurable_logsAndPurgeRoundTrip(t *testing.T) {
	app := sqliteApp(t)
	ctx := ctxT(t)

	reg := core.NewJobRegistry()
	require.NoError(t, reg.Register(core.JobDef{
		Name: "chatty",
		Logs: core.LogPolicyPtr(core.LogAlways),
	}, func(c core.ICronjobContext) error {
		for i := range 3 {
			c.Log().Info("working", "i", i)
		}
		return nil
	}))
	runner := newDurableRunner(t, app, reg)
	runner.Start()

	run, err := runner.Trigger(ctx, "chatty", nil)
	require.NoError(t, err)
	waitRun(t, runner, run.ID)

	logs, lerr := runner.Logs(ctx, run.ID, 0, 100)
	require.NoError(t, lerr)
	require.Len(t, logs, 3)
	assert.Equal(t, "working", logs[0].Message)

	// purge everything and confirm both tables are emptied
	n, perr := runner.Store().Purge(ctx, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	require.NoError(t, perr)
	assert.Positive(t, n)

	logs, lerr = runner.Logs(ctx, run.ID, 0, 100)
	require.NoError(t, lerr)
	assert.Empty(t, logs)
	_, gerr := runner.Run(ctx, run.ID)
	assert.Error(t, gerr)
}

// Validation failures reach the database intact: an operator looking at a failed
// run sees which parameter was wrong, not just "it failed".
func TestDurable_validationFieldsArePersisted(t *testing.T) {
	app := sqliteApp(t)
	ctx := ctxT(t)

	reg := core.NewJobRegistry()
	require.NoError(t, core.RegisterJob(reg, core.JobDef{Name: "strict"},
		func(c core.ICronjobContext, p *strictParams) error { return nil }))
	runner := newDurableRunner(t, app, reg)
	runner.Start()

	run, err := runner.Trigger(ctx, "strict", map[string]any{"name": ""})
	require.NoError(t, err)
	done := waitRun(t, runner, run.ID)

	assert.Equal(t, core.RunFailed, done.Status)
	require.NotNil(t, done.Error)
	assert.Equal(t, "INVALID_PARAMS", done.Error.Code)
	assert.Contains(t, string(done.Error.Fields), "REQUIRED")

	// and the same is true when read straight out of the table
	fresh, gerr := repository.New[core.JobRun](app.NewContext(context.Background())).
		FindOne("id = ?", run.ID)
	require.NoError(t, gerr)
	require.NotNil(t, fresh.Error)
	assert.Contains(t, string(fresh.Error.Fields), "REQUIRED")
}

type strictParams struct {
	Name string `json:"name"`
}

func (p *strictParams) Valid(ctx core.IContext) core.IError {
	if p.Name == "" {
		return core.New(400, "INVALID_PARAMS", "invalid parameters").
			WithFields(map[string]any{"name": map[string]string{"code": "REQUIRED"}})
	}
	return nil
}

// A durable store means a scheduled job leaves a trail: the tick creates a row
// like any other trigger.
func TestDurable_scheduledRunsAreRecorded(t *testing.T) {
	app := sqliteApp(t)
	ctx := ctxT(t)

	reg := core.NewJobRegistry()
	runner := newDurableRunner(t, app, reg)
	sc, err := core.NewScheduler(app, runner)
	require.NoError(t, err)
	require.NoError(t, sc.Add(core.JobDef{
		Name:     "ticker",
		Schedule: core.Every(50 * time.Millisecond),
	}, func(c core.ICronjobContext) error { return nil }))

	runner.Start()
	require.NoError(t, sc.Start())
	t.Cleanup(func() { _ = sc.Stop() })

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		page, lerr := runner.Runs(ctx, core.JobRunFilter{
			JobName:  "ticker",
			Statuses: []core.RunStatus{core.RunSucceeded},
		})
		require.NoError(t, lerr)
		if page.Total > 0 {
			assert.Equal(t, core.TriggerSchedule, page.Items[0].Trigger)
			assert.Equal(t, "scheduler", page.Items[0].TriggeredBy)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no scheduled run was recorded")
}
