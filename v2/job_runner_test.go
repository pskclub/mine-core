package core

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRunner builds a started runner over an in-memory backend.
func newTestRunner(t *testing.T, opts ...JobRunnerOption) (*JobRunner, *JobRegistry) {
	t.Helper()
	app := newTestApp(t)
	reg := NewJobRegistry()
	opts = append([]JobRunnerOption{WithSlotBackoff(20 * time.Millisecond)}, opts...)
	r := NewJobRunner(app, reg, opts...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = r.Stop(ctx)
	})
	return r, reg
}

// waitFor polls until cond holds, failing the test on timeout.
func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}

func waitRun(t *testing.T, r *JobRunner, id string) *JobRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run, err := r.Wait(ctx, id)
	require.NoError(t, err)
	return run
}

// ---------------------------------------------------------------------------
// params
// ---------------------------------------------------------------------------

type reportParams struct {
	Date  string `json:"date"`
	Force bool   `json:"force"`
}

// Valid makes the params validated on the way in, exactly like an HTTP payload.
func (p reportParams) Valid() IError {
	if p.Date == "" {
		return New(http.StatusUnprocessableEntity, "INVALID_PARAMS", "date is required")
	}
	return nil
}

func TestRunner_typedParams(t *testing.T) {
	r, reg := newTestRunner(t)
	got := make(chan reportParams, 1)
	require.NoError(t, RegisterJob(reg, JobDef{Name: "report"},
		func(c ICronjobContext, p reportParams) error {
			got <- p
			return nil
		}))
	r.Start()

	run, err := r.Trigger(context.Background(), "report", reportParams{Date: "2026-07-01", Force: true})
	require.NoError(t, err)

	select {
	case p := <-got:
		assert.Equal(t, "2026-07-01", p.Date)
		assert.True(t, p.Force)
	case <-time.After(3 * time.Second):
		t.Fatal("job did not run")
	}
	assert.Equal(t, RunSucceeded, waitRun(t, r, run.ID).Status)
}

func TestRunner_invalidParamsFailTheRun(t *testing.T) {
	r, reg := newTestRunner(t)
	require.NoError(t, RegisterJob(reg, JobDef{Name: "report"},
		func(c ICronjobContext, p reportParams) error { return nil }))
	r.Start()

	run, err := r.Trigger(context.Background(), "report", reportParams{}) // no date
	require.NoError(t, err)

	done := waitRun(t, r, run.ID)
	assert.Equal(t, RunFailed, done.Status)
	require.NotNil(t, done.Error)
	assert.Equal(t, "INVALID_PARAMS", done.Error.Code)
}

// ---------------------------------------------------------------------------
// status, result, progress, failures
// ---------------------------------------------------------------------------

func TestRunner_successRecordsResultAndProgress(t *testing.T) {
	r, reg := newTestRunner(t)
	require.NoError(t, reg.Register(JobDef{Name: "work"}, func(c ICronjobContext) error {
		c.Progress(50, "halfway")
		c.SetResult(map[string]any{"rows": 3})
		return nil
	}))
	r.Start()

	run, err := r.Trigger(context.Background(), "work", nil, TriggerOptions{By: "user-1"})
	require.NoError(t, err)
	done := waitRun(t, r, run.ID)

	assert.Equal(t, RunSucceeded, done.Status)
	assert.Equal(t, TriggerManual, done.Trigger)
	assert.Equal(t, "user-1", done.TriggeredBy)
	assert.JSONEq(t, `{"rows":3}`, string(done.Result))
	assert.NotNil(t, done.StartedAt)
	assert.NotNil(t, done.FinishedAt)
}

func TestRunner_panicBecomesFailedRun(t *testing.T) {
	r, reg := newTestRunner(t)
	require.NoError(t, reg.Register(JobDef{Name: "boom"}, func(c ICronjobContext) error {
		panic("job exploded")
	}))
	r.Start()

	run, err := r.Trigger(context.Background(), "boom", nil)
	require.NoError(t, err)
	done := waitRun(t, r, run.ID)

	assert.Equal(t, RunFailed, done.Status, "a panic must not take the worker down")
	require.NotNil(t, done.Error)
	assert.Contains(t, done.Error.Message, "job exploded")
}

func TestRunner_retriesUntilMaxAttempts(t *testing.T) {
	r, reg := newTestRunner(t)
	var attempts int32
	require.NoError(t, reg.Register(JobDef{
		Name:        "flaky",
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 10 * time.Millisecond },
	}, func(c ICronjobContext) error {
		if atomic.AddInt32(&attempts, 1) < 3 {
			return errors.New("transient")
		}
		return nil
	}))
	r.Start()

	run, err := r.Trigger(context.Background(), "flaky", nil)
	require.NoError(t, err)
	done := waitRun(t, r, run.ID)

	assert.Equal(t, RunSucceeded, done.Status)
	assert.Equal(t, 3, done.Attempt)
	assert.EqualValues(t, 3, atomic.LoadInt32(&attempts))
}

func TestRunner_timeoutFailsTheRun(t *testing.T) {
	r, reg := newTestRunner(t)
	require.NoError(t, reg.Register(JobDef{
		Name:      "slow",
		Timeout:   50 * time.Millisecond,
		StopGrace: 100 * time.Millisecond,
	}, func(c ICronjobContext) error {
		<-c.Stopping()
		return c.Err()
	}))
	r.Start()

	run, err := r.Trigger(context.Background(), "slow", nil)
	require.NoError(t, err)
	done := waitRun(t, r, run.ID)

	assert.Equal(t, RunFailed, done.Status, "a timeout is a failure, not a cancellation")
}

// ---------------------------------------------------------------------------
// stop
// ---------------------------------------------------------------------------

func TestRunner_cancelQueuedRunNeverStarts(t *testing.T) {
	r, reg := newTestRunner(t)
	var ran int32
	require.NoError(t, reg.Register(JobDef{Name: "later"}, func(c ICronjobContext) error {
		atomic.AddInt32(&ran, 1)
		return nil
	}))
	r.Start()

	run, err := r.Trigger(context.Background(), "later", nil, TriggerOptions{Delay: time.Hour})
	require.NoError(t, err)
	require.NoError(t, r.Cancel(context.Background(), run.ID, "admin", "not needed"))

	done, gerr := r.Run(context.Background(), run.ID)
	require.NoError(t, gerr)
	assert.Equal(t, RunCanceled, done.Status)
	assert.Equal(t, "admin", done.CanceledBy)
	assert.Zero(t, atomic.LoadInt32(&ran), "a canceled queued run must never execute")
}

func TestRunner_cancelRunningRunStopsHandler(t *testing.T) {
	r, reg := newTestRunner(t)
	started := make(chan struct{})
	require.NoError(t, reg.Register(JobDef{Name: "long"}, func(c ICronjobContext) error {
		close(started)
		for {
			if c.IsStopping() {
				return c.Err() // cooperative: this is what makes stop work
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	r.Start()

	run, err := r.Trigger(context.Background(), "long", nil)
	require.NoError(t, err)
	<-started
	require.NoError(t, r.Cancel(context.Background(), run.ID, "admin", "stop it"))

	done := waitRun(t, r, run.ID)
	assert.Equal(t, RunCanceled, done.Status)
	assert.Equal(t, "stop it", done.CancelReason)
}

// A handler that ignores cancellation must not pin a worker forever: after the
// grace period the runner gives up on it and moves on.
func TestRunner_handlerIgnoringCancelIsAbandoned(t *testing.T) {
	r, reg := newTestRunner(t)
	started := make(chan struct{})
	release := make(chan struct{})
	require.NoError(t, reg.Register(JobDef{
		Name:      "stubborn",
		StopGrace: 50 * time.Millisecond,
	}, func(c ICronjobContext) error {
		close(started)
		<-release // deliberately ignores c.Stopping()
		return nil
	}))
	r.Start()

	run, err := r.Trigger(context.Background(), "stubborn", nil)
	require.NoError(t, err)
	<-started
	require.NoError(t, r.Cancel(context.Background(), run.ID, "admin", ""))

	done := waitRun(t, r, run.ID)
	assert.Equal(t, RunCanceled, done.Status)
	close(release)
}

func TestRunner_cancelFinishedRunIsRejected(t *testing.T) {
	r, reg := newTestRunner(t)
	require.NoError(t, reg.Register(JobDef{Name: "quick"}, func(c ICronjobContext) error { return nil }))
	r.Start()

	run, err := r.Trigger(context.Background(), "quick", nil)
	require.NoError(t, err)
	waitRun(t, r, run.ID)

	err = r.Cancel(context.Background(), run.ID, "admin", "")
	require.Error(t, err)
	assert.Equal(t, http.StatusConflict, err.GetStatus())
}

func TestRunner_pauseBlocksTriggering(t *testing.T) {
	r, reg := newTestRunner(t)
	require.NoError(t, reg.Register(JobDef{Name: "p"}, func(c ICronjobContext) error { return nil }))
	r.Start()

	require.NoError(t, r.Pause("p"))
	_, err := r.Trigger(context.Background(), "p", nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusConflict, err.GetStatus())

	require.NoError(t, r.Resume("p"))
	_, err = r.Trigger(context.Background(), "p", nil)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// concurrency
// ---------------------------------------------------------------------------

func TestRunner_maxConcurrentIsNeverExceeded(t *testing.T) {
	r, reg := newTestRunner(t, WithWorkers(8))
	var running, peak int32
	var mu sync.Mutex
	require.NoError(t, reg.Register(JobDef{
		Name:          "limited",
		MaxConcurrent: 2,
		Concurrency:   ConcurrencyEnqueue,
	}, func(c ICronjobContext) error {
		cur := atomic.AddInt32(&running, 1)
		mu.Lock()
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(60 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		return nil
	}))
	r.Start()

	ids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		run, err := r.Trigger(context.Background(), "limited", nil)
		require.NoError(t, err)
		ids = append(ids, run.ID)
	}
	for _, id := range ids {
		assert.Equal(t, RunSucceeded, waitRun(t, r, id).Status, "nothing is lost under ConcurrencyEnqueue")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.LessOrEqual(t, peak, int32(2), "MaxConcurrent must hold at all times")
}

func TestRunner_concurrencySkip(t *testing.T) {
	r, reg := newTestRunner(t, WithWorkers(4))
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	require.NoError(t, reg.Register(JobDef{
		Name:          "singleton",
		MaxConcurrent: 1,
		Concurrency:   ConcurrencySkip,
	}, func(c ICronjobContext) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-block
		return nil
	}))
	r.Start()

	first, err := r.Trigger(context.Background(), "singleton", nil)
	require.NoError(t, err)
	<-started

	second, err := r.Trigger(context.Background(), "singleton", nil)
	require.NoError(t, err)
	done := waitRun(t, r, second.ID)
	assert.Equal(t, RunSkipped, done.Status, "an overlapping run is skipped, not queued up")

	close(block)
	assert.Equal(t, RunSucceeded, waitRun(t, r, first.ID).Status)
}

func TestRunner_concurrencyReplaceCancelsTheOlderRun(t *testing.T) {
	r, reg := newTestRunner(t, WithWorkers(4))
	started := make(chan struct{}, 1)
	require.NoError(t, reg.Register(JobDef{
		Name:          "rebuild",
		MaxConcurrent: 1,
		Concurrency:   ConcurrencyReplace,
		StopGrace:     time.Second,
	}, func(c ICronjobContext) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-c.Stopping()
		return c.Err()
	}))
	r.Start()

	first, err := r.Trigger(context.Background(), "rebuild", nil)
	require.NoError(t, err)
	<-started

	second, err := r.Trigger(context.Background(), "rebuild", nil)
	require.NoError(t, err)

	assert.Equal(t, RunCanceled, waitRun(t, r, first.ID).Status, "latest wins")
	waitFor(t, 3*time.Second, "the replacement to start", func() bool {
		run, _ := r.Run(context.Background(), second.ID)
		return run != nil && run.Status == RunRunning
	})
	require.NoError(t, r.Cancel(context.Background(), second.ID, "test", "cleanup"))
	waitRun(t, r, second.ID)
}

// ---------------------------------------------------------------------------
// replay & idempotency
// ---------------------------------------------------------------------------

func TestRunner_replayCreatesANewLinkedRun(t *testing.T) {
	r, reg := newTestRunner(t)
	seen := make(chan string, 2)
	require.NoError(t, RegisterJob(reg, JobDef{Name: "report"},
		func(c ICronjobContext, p reportParams) error {
			seen <- p.Date
			return nil
		}))
	r.Start()

	first, err := r.Trigger(context.Background(), "report", reportParams{Date: "2026-07-01"})
	require.NoError(t, err)
	waitRun(t, r, first.ID)
	assert.Equal(t, "2026-07-01", <-seen)

	// same params
	replay, err := r.Replay(context.Background(), first.ID, ReplayOptions{By: "admin"})
	require.NoError(t, err)
	waitRun(t, r, replay.ID)
	assert.Equal(t, "2026-07-01", <-seen)
	assert.Equal(t, first.ID, replay.ReplayOf)
	assert.Equal(t, first.ID, replay.RootID)
	assert.Equal(t, TriggerReplay, replay.Trigger)
	assert.Equal(t, 1, replay.Attempt, "a replay starts a fresh attempt chain")

	// overridden params, and the chain still points at the original run
	third, err := r.Replay(context.Background(), replay.ID, ReplayOptions{
		Params: reportParams{Date: "2026-06-01"},
	})
	require.NoError(t, err)
	waitRun(t, r, third.ID)
	assert.Equal(t, "2026-06-01", <-seen)
	assert.Equal(t, first.ID, third.RootID)

	// the original is untouched — history is never rewritten
	original, gerr := r.Run(context.Background(), first.ID)
	require.NoError(t, gerr)
	assert.Empty(t, original.ReplayOf)
	assert.JSONEq(t, `{"date":"2026-07-01","force":false}`, string(original.Params))
}

func TestRunner_replayRejectsUnfinishedAndNonReplayableRuns(t *testing.T) {
	r, reg := newTestRunner(t)
	block := make(chan struct{})
	require.NoError(t, reg.Register(JobDef{Name: "long"}, func(c ICronjobContext) error {
		<-block
		return nil
	}))
	require.NoError(t, reg.Register(JobDef{
		Name:       "payout",
		Replayable: BoolPtr(false),
	}, func(c ICronjobContext) error { return nil }))
	r.Start()

	running, err := r.Trigger(context.Background(), "long", nil)
	require.NoError(t, err)
	_, err = r.Replay(context.Background(), running.ID)
	require.Error(t, err)
	assert.Equal(t, http.StatusConflict, err.GetStatus())
	close(block)
	waitRun(t, r, running.ID)

	payout, err := r.Trigger(context.Background(), "payout", nil)
	require.NoError(t, err)
	waitRun(t, r, payout.ID)
	_, err = r.Replay(context.Background(), payout.ID)
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, err.GetStatus(),
		"jobs that are dangerous to repeat opt out of replay")
}

func TestRunner_idemKeyReturnsTheExistingRun(t *testing.T) {
	r, reg := newTestRunner(t)
	require.NoError(t, reg.Register(JobDef{Name: "settle"}, func(c ICronjobContext) error { return nil }))
	// runner not started: the run stays queued, which is when the key applies

	first, err := r.Trigger(context.Background(), "settle", nil, TriggerOptions{IdemKey: "2026-07"})
	require.NoError(t, err)
	second, err := r.Trigger(context.Background(), "settle", nil, TriggerOptions{IdemKey: "2026-07"})
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID, "a double click must not create a second run")

	page, lerr := r.Runs(context.Background(), JobRunFilter{JobName: "settle"})
	require.NoError(t, lerr)
	assert.EqualValues(t, 1, page.Total)
}

// ---------------------------------------------------------------------------
// logs
// ---------------------------------------------------------------------------

func TestRunner_logOffPersistsNothingButKeepsTheFailureTail(t *testing.T) {
	r, reg := newTestRunner(t) // LogOff is the default
	require.NoError(t, reg.Register(JobDef{Name: "noisy"}, func(c ICronjobContext) error {
		c.Log().Info("step one")
		c.Log().Info("step two")
		return errors.New("kaboom")
	}))
	r.Start()

	run, err := r.Trigger(context.Background(), "noisy", nil)
	require.NoError(t, err)
	done := waitRun(t, r, run.ID)

	logs, lerr := r.Logs(context.Background(), run.ID, 0, 100)
	require.NoError(t, lerr)
	assert.Empty(t, logs, "LogOff must not write a single row")

	require.NotNil(t, done.Error)
	require.Len(t, done.Error.Tail, 2, "the tail is kept even under LogOff, so failures stay debuggable")
	assert.Contains(t, done.Error.Tail[0], "step one")
}

func TestRunner_logOnFailurePersistsOnlyFailures(t *testing.T) {
	r, reg := newTestRunner(t)
	require.NoError(t, reg.Register(JobDef{
		Name: "ok",
		Logs: LogPolicyPtr(LogOnFailure),
	}, func(c ICronjobContext) error {
		c.Log().Info("all good")
		return nil
	}))
	require.NoError(t, reg.Register(JobDef{
		Name: "bad",
		Logs: LogPolicyPtr(LogOnFailure),
	}, func(c ICronjobContext) error {
		c.Log().Info("about to fail")
		return errors.New("nope")
	}))
	r.Start()

	ok, err := r.Trigger(context.Background(), "ok", nil)
	require.NoError(t, err)
	waitRun(t, r, ok.ID)
	logs, lerr := r.Logs(context.Background(), ok.ID, 0, 100)
	require.NoError(t, lerr)
	assert.Empty(t, logs, "a successful run costs no storage")

	bad, err := r.Trigger(context.Background(), "bad", nil)
	require.NoError(t, err)
	waitRun(t, r, bad.ID)
	logs, lerr = r.Logs(context.Background(), bad.ID, 0, 100)
	require.NoError(t, lerr)
	require.NotEmpty(t, logs, "the run that failed is the one worth keeping")
	assert.Equal(t, "about to fail", logs[0].Message)
}

func TestRunner_perTriggerLogOverride(t *testing.T) {
	r, reg := newTestRunner(t) // runner default: LogOff
	require.NoError(t, reg.Register(JobDef{Name: "sync"}, func(c ICronjobContext) error {
		c.Log().Info("syncing", "n", 1)
		return nil
	}))
	r.Start()

	run, err := r.Trigger(context.Background(), "sync", nil,
		TriggerOptions{CaptureLogs: LogPolicyPtr(LogAlways)})
	require.NoError(t, err)
	waitRun(t, r, run.ID)

	logs, lerr := r.Logs(context.Background(), run.ID, 0, 100)
	require.NoError(t, lerr)
	require.Len(t, logs, 1, "one operator-triggered run can ask for full logs")
	assert.Equal(t, "syncing", logs[0].Message)
	assert.EqualValues(t, 1, logs[0].Attrs["n"])
}

func TestRunner_logLimitsTruncate(t *testing.T) {
	r, reg := newTestRunner(t, WithLogPolicy(LogAlways), WithLogLimits(LogLimits{MaxLines: 3}))
	require.NoError(t, reg.Register(JobDef{Name: "loop"}, func(c ICronjobContext) error {
		for i := 0; i < 20; i++ {
			c.Log().Info("line")
		}
		return nil
	}))
	r.Start()

	run, err := r.Trigger(context.Background(), "loop", nil)
	require.NoError(t, err)
	waitRun(t, r, run.ID)

	logs, lerr := r.Logs(context.Background(), run.ID, 0, 100)
	require.NoError(t, lerr)
	assert.Less(t, len(logs), 20, "a job logging in a loop must not blow up the store")
	assert.Contains(t, logs[len(logs)-1].Message, "truncated")
}

// Live tailing works without the store, which is why LogOff is a safe default:
// an operator triggering a run by hand can still watch it.
func TestRunner_tailLogsWorksUnderLogOff(t *testing.T) {
	r, reg := newTestRunner(t)
	proceed := make(chan struct{})
	require.NoError(t, reg.Register(JobDef{Name: "watch"}, func(c ICronjobContext) error {
		<-proceed
		c.Log().Info("hello from the job")
		return nil
	}))
	r.Start()

	run, err := r.Trigger(context.Background(), "watch", nil)
	require.NoError(t, err)
	lines, unsubscribe := r.TailLogs(run.ID)
	defer unsubscribe()
	close(proceed)

	select {
	case e := <-lines:
		assert.Equal(t, "hello from the job", e.Message)
	case <-time.After(3 * time.Second):
		t.Fatal("live tail delivered nothing")
	}
}

// ---------------------------------------------------------------------------
// shutdown & retention
// ---------------------------------------------------------------------------

// Draining must not mark unfinished work as failed — it never got the chance to
// fail. It goes back on the queue instead.
func TestRunner_stopRequeuesUnfinishedRuns(t *testing.T) {
	app := newTestApp(t)
	reg := NewJobRegistry()
	r := NewJobRunner(app, reg)
	started := make(chan struct{})
	release := make(chan struct{})
	require.NoError(t, reg.Register(JobDef{Name: "hung", StopGrace: 20 * time.Millisecond},
		func(c ICronjobContext) error {
			close(started)
			<-release
			return nil
		}))
	r.Start()

	run, err := r.Trigger(context.Background(), "hung", nil)
	require.NoError(t, err)
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = r.Stop(ctx)
	close(release)

	after, gerr := r.Store().Get(context.Background(), run.ID)
	require.NoError(t, gerr)
	assert.Equal(t, RunQueued, after.Status, "an interrupted run is requeued, never failed")
}

func TestRunner_purgeDropsOldRuns(t *testing.T) {
	r, reg := newTestRunner(t)
	require.NoError(t, reg.Register(JobDef{Name: "old"}, func(c ICronjobContext) error { return nil }))
	r.Start()

	run, err := r.Trigger(context.Background(), "old", nil)
	require.NoError(t, err)
	waitRun(t, r, run.ID)

	// nothing is old enough yet
	n, perr := r.Purge(context.Background())
	require.NoError(t, perr)
	assert.Zero(t, n)

	// everything finished before "now" goes
	n, perr = r.Store().Purge(context.Background(), time.Now().Add(time.Minute), time.Now())
	require.NoError(t, perr)
	assert.EqualValues(t, 1, n)
	_, gerr := r.Run(context.Background(), run.ID)
	assert.Error(t, gerr)
}

func TestRunner_triggerUnknownJob(t *testing.T) {
	r, _ := newTestRunner(t)
	r.Start()
	_, err := r.Trigger(context.Background(), "nope", nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, err.GetStatus())
}
