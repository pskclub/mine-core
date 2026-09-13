package core

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOfflineRedisCache is a redis-backed cache whose client is never used. It is
// enough to build a limiter and inspect how it is configured — the behaviour
// itself needs a server and lives in job_limiter_redis_integration_test.go.
func newOfflineRedisCache(t *testing.T, prefix string) ICache {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewCacheWithClient(rdb, WithCachePrefix(prefix))
}

func TestRedisLimiter_refusesToBuildWithoutRedis(t *testing.T) {
	// the whole point of this limiter is that limits hold across replicas; a
	// per-process fallback would silently run a singleton job once per pod
	t.Run("no cache configured", func(t *testing.T) {
		_, err := NewRedisLimiter(newTestApp(t))
		require.Error(t, err)
		assert.Equal(t, "LIMITER_NO_REDIS", err.GetCode())
	})

	t.Run("memory cache", func(t *testing.T) {
		app := newTestApp(t, WithCache("default", NewMemoryCache()))
		_, err := NewRedisLimiter(app)
		require.Error(t, err)
		assert.Equal(t, "LIMITER_NO_REDIS", err.GetCode())
	})

	t.Run("named cache that is not redis", func(t *testing.T) {
		app := newTestApp(t,
			WithCache("default", newOfflineRedisCache(t, "svc")),
			WithCache("jobs", NewMemoryCache()))
		_, err := NewRedisLimiter(app, WithLimiterCache("jobs"))
		require.Error(t, err)
		assert.Contains(t, err.GetMessage(), `"jobs"`)
	})
}

func TestRedisLimiter_keysLiveUnderTheCachePrefix(t *testing.T) {
	app := newTestApp(t, WithCache("default", newOfflineRedisCache(t, "svc")))

	lim, err := NewRedisLimiter(app)
	require.NoError(t, err)
	// two services sharing one redis db must not share their limits by accident
	assert.Equal(t, "svc:limiter:job:settlement", lim.(*redisLimiter).key("job:settlement"))

	// ...and must be able to share them on purpose
	shared, err := NewRedisLimiter(app, WithLimiterPrefix("shared:limiter:"))
	require.NoError(t, err)
	assert.Equal(t, "shared:limiter:job:settlement", shared.(*redisLimiter).key("job:settlement"))
}

func TestRedisLimiter_leaseDefaultsAndOverrides(t *testing.T) {
	app := newTestApp(t, WithCache("default", newOfflineRedisCache(t, "svc")))

	lim, err := NewRedisLimiter(app)
	require.NoError(t, err)
	assert.Equal(t, DefaultLimiterLease, lim.(*redisLimiter).lease)

	lim, err = NewRedisLimiter(app, WithLimiterLease(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, lim.(*redisLimiter).lease)

	// a nonsensical lease falls back to the default rather than spinning the
	// heartbeat at zero
	lim, err = NewRedisLimiter(app, WithLimiterLease(-time.Second))
	require.NoError(t, err)
	assert.Equal(t, DefaultLimiterLease, lim.(*redisLimiter).lease)
}

func TestRedisLimiter_unlimitedNeverTouchesRedis(t *testing.T) {
	// limit <= 0 means "no limit" for every ILimiter, and it is the common case:
	// most jobs set no MaxConcurrent at all, so it must not cost a round trip.
	// The client here has nothing behind it — a limiter that tried to talk to it
	// would refuse the slot.
	l := NewRedisLimiterWithClient(newOfflineRedisCache(t, "svc").Redis())

	release, ok := l.Acquire(context.Background(), "job:free", 0)
	require.True(t, ok)
	require.NotNil(t, release)
	release()
	release() // and releasing an unlimited slot twice is still a no-op
}
