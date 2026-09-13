//go:build integration

// Package jobstore's integration tests are built around a single conformance
// suite: one set of assertions describing what an IJobStore and an IJobQueue
// must do, run against every backend. A new backend (Redis, Mongo, your own) is
// correct when it passes this file — the contract lives in one place instead of
// being re-invented per implementation.
package jobstore_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "github.com/pskclub/mine-core/v2"
)

// backend is one store/queue pair under test.
type backend struct {
	name  string
	store core.IJobStore
	queue core.IJobQueue
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// newRun builds a queued run with sane defaults.
func newRun(name string, opts ...func(*core.JobRun)) *core.JobRun {
	now := time.Now()
	run := &core.JobRun{
		ID:          uuid.NewString(),
		JobName:     name,
		Queue:       core.DefaultQueue,
		Status:      core.RunQueued,
		Trigger:     core.TriggerManual,
		Attempt:     1,
		MaxAttempts: 1,
		ScheduledAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	run.RootID = run.ID
	for _, o := range opts {
		o(run)
	}
	return run
}

// copyRun is the stale copy a worker holds while it executes.
func copyRun(r *core.JobRun) *core.JobRun {
	cp := *r
	return &cp
}

// ---------------------------------------------------------------------------
// Store contract
// ---------------------------------------------------------------------------

func runStoreConformance(t *testing.T, b backend) {
	ctx := ctxT(t)
	s := b.store

	t.Run("create and read back every field", func(t *testing.T) {
		finished := time.Now().Truncate(time.Second)
		run := newRun("round-trip", func(r *core.JobRun) {
			r.Params = json.RawMessage(`{"date":"2026-07-01"}`)
			r.Result = json.RawMessage(`{"rows":3}`)
			r.Status = core.RunFailed
			r.TriggeredBy = "admin@example.com"
			r.IdemKey = "round-trip-1"
			r.Attempt, r.MaxAttempts = 2, 3
			r.DurationMS = 1234
			r.Progress, r.ProgressMessage = 75, "almost"
			r.FinishedAt = &finished
			r.WorkerID = "worker-a"
			r.CaptureLogs = core.LogPolicyPtr(core.LogAlways)
			r.Error = &core.RunError{
				Code: "INVALID_PARAMS", Status: 400, Message: "bad params",
				Fields: json.RawMessage(`{"date":{"code":"INVALID_DATE"}}`),
				Tail:   []string{"line one", "line two"},
			}
		})
		require.NoError(t, s.Create(ctx, run))

		got, err := s.Get(ctx, run.ID)
		require.NoError(t, err)
		assert.Equal(t, run.JobName, got.JobName)
		assert.JSONEq(t, string(run.Params), string(got.Params))
		assert.JSONEq(t, string(run.Result), string(got.Result))
		assert.Equal(t, core.RunFailed, got.Status)
		assert.Equal(t, "admin@example.com", got.TriggeredBy)
		assert.Equal(t, 2, got.Attempt)
		assert.EqualValues(t, 1234, got.DurationMS)
		assert.Equal(t, 75, got.Progress)
		require.NotNil(t, got.CaptureLogs)
		assert.Equal(t, core.LogAlways, *got.CaptureLogs)
		require.NotNil(t, got.FinishedAt)
		assert.WithinDuration(t, finished, *got.FinishedAt, time.Second)

		// the error — including its field violations and log tail — must survive
		require.NotNil(t, got.Error)
		assert.Equal(t, "INVALID_PARAMS", got.Error.Code)
		assert.Equal(t, 400, got.Error.Status)
		assert.JSONEq(t, `{"date":{"code":"INVALID_DATE"}}`, string(got.Error.Fields))
		assert.Equal(t, []string{"line one", "line two"}, got.Error.Tail)
	})

	t.Run("missing run is a typed not-found", func(t *testing.T) {
		_, err := s.Get(ctx, "does-not-exist")
		require.Error(t, err)
		assert.Equal(t, "JOB_RUN_NOT_FOUND", err.GetCode())
		assert.Equal(t, 404, err.GetStatus())
	})

	t.Run("update writes the new state", func(t *testing.T) {
		run := newRun("update")
		require.NoError(t, s.Create(ctx, run))

		started := time.Now()
		run.Status, run.StartedAt, run.WorkerID = core.RunRunning, &started, "worker-b"
		require.NoError(t, s.Update(ctx, run))

		got, err := s.Get(ctx, run.ID)
		require.NoError(t, err)
		assert.Equal(t, core.RunRunning, got.Status)
		assert.Equal(t, "worker-b", got.WorkerID)
		require.NotNil(t, got.StartedAt)
	})

	// A worker holds a stale copy of the run while it executes. When it writes
	// the result, it must not erase a cancellation somebody recorded meanwhile —
	// otherwise "who stopped this and why" is lost exactly when it matters.
	t.Run("update never clobbers a cancellation", func(t *testing.T) {
		run := newRun("cancel-race")
		require.NoError(t, s.Create(ctx, run))
		stale := copyRun(run)

		require.NoError(t, s.RequestCancel(ctx, run.ID, "admin", "wrong input"))

		finished := time.Now()
		stale.Status, stale.FinishedAt = core.RunCanceled, &finished
		require.NoError(t, s.Update(ctx, stale))

		got, err := s.Get(ctx, run.ID)
		require.NoError(t, err)
		assert.Equal(t, core.RunCanceled, got.Status)
		assert.Equal(t, "admin", got.CanceledBy)
		assert.Equal(t, "wrong input", got.CancelReason)
		require.NotNil(t, got.CancelRequestedAt)
	})

	t.Run("cancel flag", func(t *testing.T) {
		run := newRun("cancel-flag")
		require.NoError(t, s.Create(ctx, run))

		requested, err := s.IsCancelRequested(ctx, run.ID)
		require.NoError(t, err)
		assert.False(t, requested)

		require.NoError(t, s.RequestCancel(ctx, run.ID, "ops", "stop"))
		requested, err = s.IsCancelRequested(ctx, run.ID)
		require.NoError(t, err)
		assert.True(t, requested)

		_, err = s.IsCancelRequested(ctx, "nope")
		assert.Error(t, err)
	})

	t.Run("find active by idempotency key", func(t *testing.T) {
		active := newRun("idem", func(r *core.JobRun) { r.IdemKey = "key-1" })
		require.NoError(t, s.Create(ctx, active))

		got, err := s.FindActive(ctx, "idem", "key-1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, active.ID, got.ID)

		// an empty key never matches anything
		got, err = s.FindActive(ctx, "idem", "")
		require.NoError(t, err)
		assert.Nil(t, got)

		// once the run is finished the key is free again
		finished := time.Now()
		active.Status, active.FinishedAt = core.RunSucceeded, &finished
		require.NoError(t, s.Update(ctx, active))
		got, err = s.FindActive(ctx, "idem", "key-1")
		require.NoError(t, err)
		assert.Nil(t, got, "a finished run must not block the next one")
	})

	t.Run("running ids", func(t *testing.T) {
		running := newRun("busy", func(r *core.JobRun) { r.Status = core.RunRunning })
		queued := newRun("busy")
		require.NoError(t, s.Create(ctx, running))
		require.NoError(t, s.Create(ctx, queued))

		ids, err := s.RunningIDs(ctx, "busy")
		require.NoError(t, err)
		assert.Equal(t, []string{running.ID}, ids)
	})

	t.Run("list filters, orders and pages", func(t *testing.T) {
		for i := range 5 {
			run := newRun("listing", func(r *core.JobRun) {
				r.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
				if i%2 == 0 {
					r.Status = core.RunFailed
				}
			})
			require.NoError(t, s.Create(ctx, run))
		}
		other := newRun("other-job")
		require.NoError(t, s.Create(ctx, other))

		page, err := s.List(ctx, core.JobRunFilter{
			JobName: "listing",
			Page:    &core.PageOptions{Limit: 2, Page: 1},
		})
		require.NoError(t, err)
		assert.EqualValues(t, 5, page.Total, "total counts every match, not just this page")
		require.Len(t, page.Items, 2)
		assert.True(t, !page.Items[0].CreatedAt.Before(page.Items[1].CreatedAt), "newest first")

		page2, err := s.List(ctx, core.JobRunFilter{
			JobName: "listing",
			Page:    &core.PageOptions{Limit: 2, Page: 2},
		})
		require.NoError(t, err)
		require.Len(t, page2.Items, 2)
		assert.NotEqual(t, page.Items[0].ID, page2.Items[0].ID)

		failed, err := s.List(ctx, core.JobRunFilter{
			JobName:  "listing",
			Statuses: []core.RunStatus{core.RunFailed},
			Page:     &core.PageOptions{Limit: 50},
		})
		require.NoError(t, err)
		assert.EqualValues(t, 3, failed.Total)
		for _, item := range failed.Items {
			assert.Equal(t, core.RunFailed, item.Status)
		}
	})

	t.Run("logs are appended, cursored and limited", func(t *testing.T) {
		run := newRun("logging")
		require.NoError(t, s.Create(ctx, run))

		entries := make([]core.JobLog, 0, 5)
		for i := range 5 {
			entries = append(entries, core.JobLog{
				RunID: run.ID, Seq: int64(i + 1), At: time.Now(),
				Level: "info", Message: "line", Attrs: map[string]any{"i": i},
			})
		}
		require.NoError(t, s.AppendLogs(ctx, entries))
		require.NoError(t, s.AppendLogs(ctx, nil), "an empty batch is a no-op")

		all, err := s.Logs(ctx, run.ID, 0, 100)
		require.NoError(t, err)
		require.Len(t, all, 5)
		assert.EqualValues(t, 1, all[0].Seq, "ascending by seq")
		assert.NotNil(t, all[0].Attrs)

		after, err := s.Logs(ctx, run.ID, 3, 100)
		require.NoError(t, err)
		require.Len(t, after, 2, "seq is the cursor")
		assert.EqualValues(t, 4, after[0].Seq)

		limited, err := s.Logs(ctx, run.ID, 0, 2)
		require.NoError(t, err)
		assert.Len(t, limited, 2)
	})

	t.Run("purge drops finished runs and old logs", func(t *testing.T) {
		old := time.Now().Add(-48 * time.Hour)
		done := newRun("purge-me", func(r *core.JobRun) {
			r.Status, r.FinishedAt = core.RunSucceeded, &old
		})
		alive := newRun("keep-me", func(r *core.JobRun) { r.Status = core.RunRunning })
		require.NoError(t, s.Create(ctx, done))
		require.NoError(t, s.Create(ctx, alive))
		require.NoError(t, s.AppendLogs(ctx, []core.JobLog{
			{RunID: alive.ID, Seq: 1, At: old, Level: "info", Message: "ancient"},
		}))

		n, err := s.Purge(ctx, time.Now().Add(-24*time.Hour), time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Positive(t, n)

		_, gerr := s.Get(ctx, done.ID)
		assert.Error(t, gerr, "a finished, expired run is gone")
		_, gerr = s.Get(ctx, alive.ID)
		assert.NoError(t, gerr, "an unfinished run is never purged")

		logs, lerr := s.Logs(ctx, alive.ID, 0, 10)
		require.NoError(t, lerr)
		assert.Empty(t, logs, "logs expire on their own, shorter schedule")
	})
}

// ---------------------------------------------------------------------------
// Queue contract
// ---------------------------------------------------------------------------

func runQueueConformance(t *testing.T, b backend) {
	ctx := ctxT(t)

	// enqueue puts a run in both the store and the queue, which is what the
	// runner does — the repo queue needs the row to exist.
	enqueue := func(t *testing.T, run *core.JobRun) *core.JobRun {
		t.Helper()
		require.NoError(t, b.store.Create(ctx, run))
		require.NoError(t, b.queue.Enqueue(ctx, run))
		return run
	}

	reserve := func(t *testing.T, timeout time.Duration, queues ...string) *core.JobRun {
		t.Helper()
		rctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		run, ack, err := b.queue.Reserve(rctx, queues)
		if err != nil {
			return nil
		}
		ack(nil)
		return run
	}

	t.Run("a due run is delivered", func(t *testing.T) {
		run := enqueue(t, newRun("deliver"))
		got := reserve(t, 5*time.Second, core.DefaultQueue)
		require.NotNil(t, got)
		assert.Equal(t, run.ID, got.ID)
		assert.Equal(t, run.JobName, got.JobName)
	})

	t.Run("a delayed run is not delivered early", func(t *testing.T) {
		enqueue(t, newRun("later", func(r *core.JobRun) {
			r.ScheduledAt = time.Now().Add(time.Hour)
		}))
		assert.Nil(t, reserve(t, 500*time.Millisecond, core.DefaultQueue),
			"ScheduledAt is what makes retries and backoff work")
	})

	t.Run("only the requested queues are consumed", func(t *testing.T) {
		enqueue(t, newRun("heavy-job", func(r *core.JobRun) { r.Queue = "heavy" }))
		assert.Nil(t, reserve(t, 500*time.Millisecond, "other"))

		got := reserve(t, 5*time.Second, "heavy")
		require.NotNil(t, got)
		assert.Equal(t, "heavy", got.Queue)
	})

	t.Run("remove takes a queued run out, but not a reserved one", func(t *testing.T) {
		run := enqueue(t, newRun("removable"))
		removed, err := b.queue.Remove(ctx, run.ID)
		require.NoError(t, err)
		assert.True(t, removed)
		assert.Nil(t, reserve(t, 300*time.Millisecond, core.DefaultQueue),
			"a canceled queued run must never be delivered")

		taken := enqueue(t, newRun("already-taken"))
		require.NotNil(t, reserve(t, 5*time.Second, core.DefaultQueue))
		removed, err = b.queue.Remove(ctx, taken.ID)
		require.NoError(t, err)
		assert.False(t, removed, "a run in flight cannot be pulled out of the queue")
	})

	// The property everything else depends on: two workers must never get the
	// same run.
	t.Run("concurrent reserves never hand out the same run twice", func(t *testing.T) {
		const runs = 8
		ids := map[string]bool{}
		for range runs {
			r := enqueue(t, newRun("contended"))
			ids[r.ID] = true
		}

		var mu sync.Mutex
		got := map[string]int{}
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					run := reserve(t, 700*time.Millisecond, core.DefaultQueue)
					if run == nil {
						return
					}
					mu.Lock()
					got[run.ID]++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		for id, n := range got {
			assert.Equal(t, 1, n, "run %s was delivered %d times", id, n)
		}
		assert.Len(t, got, runs, "every run was delivered exactly once")
	})

	t.Run("length reflects what is waiting", func(t *testing.T) {
		before, err := b.queue.Len(ctx)
		require.NoError(t, err)
		enqueue(t, newRun("counted"))
		after, err := b.queue.Len(ctx)
		require.NoError(t, err)
		assert.Equal(t, before+1, after)
	})
}
