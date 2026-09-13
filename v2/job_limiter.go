package core

import (
	"context"
	"sync"
)

// ILimiter caps how many runs may hold a given key at once. Two keys are used:
// "job:<name>" for JobDef.MaxConcurrent and "queue:<name>" for a queue limit.
//
// The default implementation counts in process, which is exactly right while
// the service runs as a single replica. Scaling to several replicas is a matter
// of supplying a Redis-backed implementation here — the runner and the handlers
// do not change.
type ILimiter interface {
	// Acquire takes a slot for key. It returns ok=false immediately when the
	// limit is reached; release must be called when the work is done.
	// limit <= 0 means unlimited.
	Acquire(ctx context.Context, key string, limit int) (release func(), ok bool)
	// Count reports how many slots of key are held right now.
	Count(ctx context.Context, key string) int
}

type inProcessLimiter struct {
	mu   sync.Mutex
	held map[string]int
	noop func()
}

var _ ILimiter = (*inProcessLimiter)(nil)

// NewInProcessLimiter creates a limiter that counts within this process.
func NewInProcessLimiter() ILimiter {
	return &inProcessLimiter{held: map[string]int{}, noop: func() {}}
}

func (l *inProcessLimiter) Acquire(_ context.Context, key string, limit int) (func(), bool) {
	if limit <= 0 {
		return l.noop, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[key] >= limit {
		return nil, false
	}
	l.held[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.held[key] <= 1 {
				delete(l.held, key)
				return
			}
			l.held[key]--
		})
	}, true
}

func (l *inProcessLimiter) Count(_ context.Context, key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held[key]
}

// limiterKeyJob / limiterKeyQueue build the keys used by the runner.
func limiterKeyJob(name string) string   { return "job:" + name }
func limiterKeyQueue(name string) string { return "queue:" + name }
