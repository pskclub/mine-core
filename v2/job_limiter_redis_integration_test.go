//go:build integration

package core

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These exercise what only a real server can: the Lua scripts behind the
// semaphore, the lease that reclaims a crashed holder's slot, and — the reason
// the type exists — one limit shared by two runners.
//
//	make test-integration                    # redis on 127.0.0.1:6379
//	APP_CACHE_HOST=... make test-integration
func newRedisTestLimiter(t *testing.T, opts ...LimiterOption) ILimiter {
	t.Helper()
	app := newTestApp(t, WithCache("default", newRedisTestCache(t)))
	lim, err := NewRedisLimiter(app, opts...)
	require.NoError(t, err)
	return lim
}

func TestRedisLimiter_capsConcurrentHolders(t *testing.T) {
	l := newRedisTestLimiter(t)
	ctx := context.Background()

	first, ok := l.Acquire(ctx, "job:settlement", 2)
	require.True(t, ok)
	second, ok := l.Acquire(ctx, "job:settlement", 2)
	require.True(t, ok)
	assert.Equal(t, 2, l.Count(ctx, "job:settlement"))

	_, ok = l.Acquire(ctx, "job:settlement", 2)
	assert.False(t, ok, "the third holder is over the limit")

	// a different key has its own slots
	other, ok := l.Acquire(ctx, "job:report", 2)
	require.True(t, ok)
	other()

	first()
	assert.Equal(t, 1, l.Count(ctx, "job:settlement"))

	third, ok := l.Acquire(ctx, "job:settlement", 2)
	require.True(t, ok, "releasing one frees a slot")
	third()
	second()
	assert.Equal(t, 0, l.Count(ctx, "job:settlement"))
}

func TestRedisLimiter_sharesTheLimitBetweenReplicas(t *testing.T) {
	// two limiters over the same redis and prefix stand in for two pods: this is
	// the whole reason to replace the in-process limiter
	cache := newRedisTestCache(t)
	app := newTestApp(t, WithCache("default", cache))
	podA, err := NewRedisLimiter(app)
	require.NoError(t, err)
	podB, err := NewRedisLimiter(app)
	require.NoError(t, err)

	ctx := context.Background()
	release, ok := podA.Acquire(ctx, "job:singleton", 1)
	require.True(t, ok)

	_, ok = podB.Acquire(ctx, "job:singleton", 1)
	assert.False(t, ok, "pod B must see pod A's slot")
	assert.Equal(t, 1, podB.Count(ctx, "job:singleton"))

	release()

	onB, ok := podB.Acquire(ctx, "job:singleton", 1)
	assert.True(t, ok, "pod B takes over once pod A is done")
	onB()
}

func TestRedisLimiter_prefixIsolatesUnrelatedServices(t *testing.T) {
	app := newTestApp(t, WithCache("default", newRedisTestCache(t)))
	mine, err := NewRedisLimiter(app)
	require.NoError(t, err)
	theirs, err := NewRedisLimiter(app, WithLimiterPrefix("someone-else:limiter:"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = app.Cache().Redis().Del(context.Background(),
			"someone-else:limiter:job:singleton").Result()
	})

	ctx := context.Background()
	release, ok := mine.Acquire(ctx, "job:singleton", 1)
	require.True(t, ok)
	defer release()

	elsewhere, ok := theirs.Acquire(ctx, "job:singleton", 1)
	assert.True(t, ok, "another namespace is another limit")
	elsewhere()
}

func TestRedisLimiter_reclaimsTheSlotOfACrashedHolder(t *testing.T) {
	// a pod killed mid-run cannot release its slot; the lease is what stops it
	// from wedging a singleton job forever. Written through the script directly
	// because a holder acquired the ordinary way keeps its lease alive.
	l := newRedisTestLimiter(t, WithLimiterLease(300*time.Millisecond)).(*redisLimiter)
	ctx := context.Background()
	key := l.key("job:singleton")

	held, err := limiterAcquireScript.Run(ctx, l.rdb, []string{key},
		1, "token-of-a-dead-pod", l.lease.Milliseconds()).Int64()
	require.NoError(t, err)
	require.Equal(t, int64(1), held)

	assert.Equal(t, 1, l.Count(ctx, "job:singleton"))
	_, ok := l.Acquire(ctx, "job:singleton", 1)
	require.False(t, ok, "while the lease is alive the dead pod still counts")

	time.Sleep(l.lease + 100*time.Millisecond)

	assert.Equal(t, 0, l.Count(ctx, "job:singleton"), "a lapsed lease stops counting")
	release, ok := l.Acquire(ctx, "job:singleton", 1)
	require.True(t, ok, "the slot is reclaimed without anyone cleaning up")
	release()
}

func TestRedisLimiter_heartbeatKeepsALongRunsSlot(t *testing.T) {
	// the counterpart: a run that outlives its lease must not lose its slot
	l := newRedisTestLimiter(t, WithLimiterLease(300*time.Millisecond))
	ctx := context.Background()

	release, ok := l.Acquire(ctx, "job:slow", 1)
	require.True(t, ok)

	time.Sleep(900 * time.Millisecond) // three leases

	assert.Equal(t, 1, l.Count(ctx, "job:slow"), "the heartbeat renewed it")
	_, ok = l.Acquire(ctx, "job:slow", 1)
	assert.False(t, ok, "and it still excludes a second holder")

	release()
	assert.Equal(t, 0, l.Count(ctx, "job:slow"))
}

func TestRedisLimiter_releaseIsIdempotent(t *testing.T) {
	l := newRedisTestLimiter(t)
	ctx := context.Background()

	release, ok := l.Acquire(ctx, "job:once", 1)
	require.True(t, ok)
	release()
	release() // a deferred release after an explicit one must not free somebody else's slot

	other, ok := l.Acquire(ctx, "job:once", 1)
	require.True(t, ok)
	release()
	assert.Equal(t, 1, l.Count(ctx, "job:once"), "the stale release did not touch the new holder")
	other()
}

func TestRedisLimiter_holdsMaxConcurrentAcrossTwoRunners(t *testing.T) {
	// end to end: one singleton job, two runners sharing a store, a queue and the
	// limiter. With the in-process limiter this test observes 2 concurrent runs.
	app := newTestApp(t, WithCache("default", newRedisTestCache(t)))
	lim, err := NewRedisLimiter(app)
	require.NoError(t, err)

	store, queue := NewMemoryJobStore(), NewMemoryJobQueue()
	reg := NewJobRegistry()

	var live, peak int64
	var mu sync.Mutex
	require.NoError(t, reg.Register(
		JobDef{Name: "settlement", MaxConcurrent: 1, Concurrency: ConcurrencyEnqueue},
		func(c ICronjobContext) error {
			n := atomic.AddInt64(&live, 1)
			mu.Lock()
			if n > peak {
				peak = n
			}
			mu.Unlock()
			time.Sleep(80 * time.Millisecond)
			atomic.AddInt64(&live, -1)
			return nil
		}))

	runners := make([]*JobRunner, 2)
	for i := range runners {
		r := NewJobRunner(app, reg,
			WithJobStore(store), WithJobQueue(queue), WithJobLimiter(lim),
			WithWorkers(2), WithSlotBackoff(20*time.Millisecond))
		r.Start()
		runners[i] = r
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = r.Stop(ctx)
		})
	}

	ids := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		run, terr := runners[i%2].Trigger(context.Background(), "settlement", nil)
		require.NoError(t, terr)
		ids = append(ids, run.ID)
	}

	waitFor(t, 10*time.Second, "all runs to finish", func() bool {
		for _, id := range ids {
			run, gerr := store.Get(context.Background(), id)
			if gerr != nil || !run.Status.IsTerminal() {
				return false
			}
		}
		return true
	})

	for _, id := range ids {
		run, gerr := store.Get(context.Background(), id)
		require.NoError(t, gerr)
		assert.Equal(t, RunSucceeded, run.Status, "run %s: %+v", id, run.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, int64(1), peak, "MaxConcurrent 1 must mean one run in the whole cluster")
}
