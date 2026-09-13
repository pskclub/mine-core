package core

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"
)

// AckFunc finalises a reserved run. Pass nil when the runner has dealt with the
// run (its final status is already persisted); pass an error when delivery
// itself failed, so the queue can make the run available again.
type AckFunc func(err error)

// IJobQueue is the delivery mechanism between whatever creates a run (the
// scheduler, a manual trigger, a retry) and the workers that execute it.
//
// The queue only moves runs around; statuses and history live in IJobStore.
// Keeping them apart is what lets you run an in-memory queue over a durable
// store, or swap in Redis later without touching a single handler.
type IJobQueue interface {
	// Enqueue makes a run available, respecting run.ScheduledAt for delayed and
	// retried work.
	Enqueue(ctx context.Context, run *JobRun) IError
	// Reserve blocks until a run from one of queues is due, or ctx is done.
	Reserve(ctx context.Context, queues []string) (*JobRun, AckFunc, IError)
	// Remove drops a not-yet-reserved run, reporting whether it was there. It is
	// how a queued run is canceled without ever executing.
	Remove(ctx context.Context, runID string) (bool, IError)
	// Len reports how many runs are waiting (for metrics/tests).
	Len(ctx context.Context) (int, IError)
	Close() IError
}

// IStoreBackedQueue is implemented by a queue whose storage *is* the store — the
// run's own row is the queue (see the jobstore package). For those the runner
// skips Enqueue entirely: writing the run is already what makes it available,
// and a second write is a race waiting to happen, because it can flip a run back
// to queued after another worker has claimed it — running it twice.
//
// A queue that keeps its own storage (in-memory, Redis, RabbitMQ) does not
// implement this and gets the normal Enqueue call.
type IStoreBackedQueue interface {
	IJobQueue
	// SharesStore reports that Enqueue is redundant with writing the run.
	SharesStore() bool
}

// ErrQueueClosed is returned by a closed queue.
var ErrQueueClosed = New(http.StatusServiceUnavailable, "QUEUE_CLOSED", "job queue is closed")

// memoryJobQueue is an in-process queue ordered by ScheduledAt. Default for
// dev/test and for single-process services that accept losing queued runs on
// restart.
type memoryJobQueue struct {
	mu     sync.Mutex
	items  []*JobRun
	notify chan struct{}
	done   chan struct{}
	closed bool
}

var _ IJobQueue = (*memoryJobQueue)(nil)

// NewMemoryJobQueue creates an in-process queue.
func NewMemoryJobQueue() IJobQueue {
	return &memoryJobQueue{notify: make(chan struct{}, 1), done: make(chan struct{})}
}

func (q *memoryJobQueue) Enqueue(_ context.Context, run *JobRun) IError {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrQueueClosed
	}
	q.items = append(q.items, run.clone())
	q.mu.Unlock()
	q.wake()
	return nil
}

func (q *memoryJobQueue) wake() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *memoryJobQueue) Reserve(ctx context.Context, queues []string) (*JobRun, AckFunc, IError) {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return nil, nil, ErrQueueClosed
		}
		now := time.Now()
		wait := time.Second
		best := -1
		for i, item := range q.items {
			if len(queues) > 0 && !slices.Contains(queues, item.Queue) {
				continue
			}
			if item.ScheduledAt.After(now) {
				if d := time.Until(item.ScheduledAt); d < wait {
					wait = d
				}
				continue
			}
			if best == -1 || item.ScheduledAt.Before(q.items[best].ScheduledAt) {
				best = i
			}
		}
		if best >= 0 {
			run := q.items[best]
			q.items = append(q.items[:best], q.items[best+1:]...)
			q.mu.Unlock()
			return run, func(err error) {
				if err != nil {
					_ = q.Enqueue(context.Background(), run)
				}
			}, nil
		}
		q.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, Wrap(ctx.Err(), "job queue: reserve")
		case <-q.done:
			timer.Stop()
			return nil, nil, ErrQueueClosed
		case <-q.notify:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (q *memoryJobQueue) Remove(_ context.Context, runID string) (bool, IError) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, item := range q.items {
		if item.ID == runID {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (q *memoryJobQueue) Len(context.Context) (int, IError) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items), nil
}

func (q *memoryJobQueue) Close() IError {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	close(q.done)
	return nil
}
