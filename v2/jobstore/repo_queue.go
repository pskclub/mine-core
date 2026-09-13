package jobstore

import (
	"context"
	"sync"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/repository"
	"gorm.io/gorm"
)

// DefaultPollInterval is how often the queue looks for due runs.
const DefaultPollInterval = time.Second

// repoQueue is an IJobQueue that uses the runs table itself as the queue: a run
// is "queued" when its row says so. No extra infrastructure, and queued runs
// survive a restart — the trade-off is polling instead of a push.
type repoQueue struct {
	app  *core.App
	cfg  config
	poll time.Duration

	closeOnce sync.Once
	done      chan struct{}
}

var _ core.IStoreBackedQueue = (*repoQueue)(nil)

// SharesStore reports that this queue *is* the store: the run's row carries its
// own queue state. The runner uses this to skip the redundant Enqueue after
// writing a run — a second write could flip a run back to queued after another
// worker claimed it, running it twice.
func (q *repoQueue) SharesStore() bool { return true }

// WithPollInterval overrides how often the queue polls for due runs.
func WithPollInterval(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.poll = d
		}
	}
}

// NewQueue builds a queue over the runs table.
func NewQueue(app *core.App, opts ...Option) core.IJobQueue {
	cfg := newConfig(opts...)
	poll := cfg.poll
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	return &repoQueue{app: app, cfg: cfg, poll: poll, done: make(chan struct{})}
}

func (q *repoQueue) runs(ctx context.Context) *repository.Repo[core.JobRun] {
	c := q.app.NewContext(ctx, core.ModeCron)
	var db *gorm.DB
	if q.cfg.conn == "" {
		db = c.DB()
	} else {
		db = c.DBS(q.cfg.conn)
	}
	return repository.NewWithDB[core.JobRun](c, db).Table(q.cfg.runsTable)
}

// Enqueue makes the row eligible again. The run itself is already stored, so
// this only has to (re)assert the queued state — that is what makes retries and
// concurrency requeues idempotent.
func (q *repoQueue) Enqueue(ctx context.Context, run *core.JobRun) core.IError {
	return q.runs(ctx).Where("id = ?", run.ID).Updates(map[string]any{
		"status":       core.RunQueued,
		"queue":        run.Queue,
		"scheduled_at": run.ScheduledAt,
		"attempt":      run.Attempt,
		"trigger":      run.Trigger,
		"worker_id":    "",
		"updated_at":   time.Now(),
	})
}

// Reserve polls for a due run and claims it atomically: the UPDATE only lands if
// the row is still queued, so two workers can never take the same run.
func (q *repoQueue) Reserve(ctx context.Context, queues []string) (*core.JobRun, core.AckFunc, core.IError) {
	for {
		select {
		case <-ctx.Done():
			return nil, nil, core.Wrap(ctx.Err(), "jobstore: reserve")
		case <-q.done:
			return nil, nil, core.ErrQueueClosed
		default:
		}

		run, err := q.claim(ctx, queues)
		if err != nil {
			return nil, nil, err
		}
		if run != nil {
			return run, func(ackErr error) {
				if ackErr != nil {
					_ = q.Enqueue(context.Background(), run)
				}
			}, nil
		}

		timer := time.NewTimer(q.poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, core.Wrap(ctx.Err(), "jobstore: reserve")
		case <-q.done:
			timer.Stop()
			return nil, nil, core.ErrQueueClosed
		case <-timer.C:
		}
	}
}

// claim returns a run this worker now owns, or nil when nothing is due.
func (q *repoQueue) claim(ctx context.Context, queues []string) (*core.JobRun, core.IError) {
	candidates := q.runs(ctx).
		Where("status = ?", core.RunQueued).
		Where("scheduled_at <= ?", time.Now()).
		Order("scheduled_at ASC").
		Limit(10)
	if len(queues) > 0 {
		candidates = candidates.Where("queue IN ?", queues)
	}
	found, err := candidates.FindAll()
	if err != nil {
		return nil, err
	}

	for i := range found {
		run := found[i]

		// Compare-and-swap on the exact row version we read, not merely on
		// "still queued". Between the select and this update the run may have
		// been finished and re-queued for the *next* attempt (a retry) or pushed
		// into the future (waiting for a concurrency slot). Both leave it queued
		// again, so a status-only guard would happily claim it — and then execute
		// a stale copy, running an attempt twice and ignoring its backoff.
		//
		// attempt pins the version; scheduled_at is re-checked here rather than
		// only in the select, so a run whose backoff has not elapsed is never
		// taken. (A range check, not equality — datetime precision differs
		// between engines.)
		res := q.runs(ctx).
			Where("id = ? AND status = ? AND attempt = ? AND scheduled_at <= ?",
				run.ID, core.RunQueued, run.Attempt, time.Now()).
			DB().Updates(map[string]any{
			"status":     core.RunRunning,
			"updated_at": time.Now(),
		})
		if res.Error != nil {
			return nil, core.Wrap(res.Error, "jobstore: claim run")
		}
		if res.RowsAffected == 1 {
			return &run, nil
		}
		// someone else took it, or it moved on — try the next candidate
	}
	return nil, nil
}

// Remove cancels a run that has not started yet, atomically so it can never be
// claimed in the meantime.
func (q *repoQueue) Remove(ctx context.Context, runID string) (bool, core.IError) {
	now := time.Now()
	res := q.runs(ctx).
		Where("id = ? AND status = ?", runID, core.RunQueued).
		DB().Updates(map[string]any{
		"status":      core.RunCanceled,
		"finished_at": now,
		"updated_at":  now,
	})
	if res.Error != nil {
		return false, core.Wrap(res.Error, "jobstore: remove run")
	}
	return res.RowsAffected == 1, nil
}

func (q *repoQueue) Len(ctx context.Context) (int, core.IError) {
	n, err := q.runs(ctx).Where("status = ?", core.RunQueued).Count()
	return int(n), err
}

func (q *repoQueue) Close() core.IError {
	q.closeOnce.Do(func() { close(q.done) })
	return nil
}
