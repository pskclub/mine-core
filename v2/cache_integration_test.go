//go:build integration

package core

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These exercise what only a real server can: the Lua scripts behind Incr and
// the lock, SCAN-based deletion, pipelining, and redis's own pattern matching.
// The unit tests cover the same contract against the in-memory backend.
//
//	make test-integration                    # redis on 127.0.0.1:6379
//	APP_CACHE_HOST=... make test-integration
func newRedisTestCache(t *testing.T) ICache {
	t.Helper()

	addr := os.Getenv("APP_CACHE_HOST")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		t.Skipf("no redis at %s: %v", addr, err)
	}

	// every test gets its own namespace, so a failed run leaves nothing behind
	// that the next one can trip over
	c := NewCacheWithClient(rdb, WithCachePrefix("coretest:"+t.Name()))
	t.Cleanup(func() {
		_, _ = c.DelByPrefix("")
		_ = c.Close()
	})
	return c
}

func TestRedisCache_roundTripAndMiss(t *testing.T) {
	c := newRedisTestCache(t)

	require.NoError(t, c.Set("user:1", cacheUser{ID: "1", Name: "ann"}, time.Minute))

	var got cacheUser
	require.NoError(t, c.Get("user:1", &got))
	assert.Equal(t, "ann", got.Name)

	assert.True(t, errors.Is(c.Get("user:2", &got), ErrCacheMiss))
}

func TestRedisCache_prefixIsWhatIsStored(t *testing.T) {
	c := newRedisTestCache(t)
	require.NoError(t, c.Set("k", "v", time.Minute))

	// read it back through the raw client, at the full key
	full := c.Prefix() + "k"
	v, err := c.Redis().Get(context.Background(), full).Result()
	require.NoError(t, err)
	assert.Equal(t, "v", v)
}

func TestRedisCache_ttlAndKeepTTL(t *testing.T) {
	c := newRedisTestCache(t)
	require.NoError(t, c.Set("k", "first", time.Hour))
	require.NoError(t, c.Set("k", "second", KeepTTL))

	ttl, err := c.TTL("k")
	require.NoError(t, err)
	assert.Greater(t, ttl, 50*time.Minute)

	require.NoError(t, c.Set("forever", "v", NoExpiry))
	ttl, err = c.TTL("forever")
	require.NoError(t, err)
	assert.Equal(t, TTLNoExpiry, ttl)

	_, err = c.TTL("absent")
	assert.True(t, errors.Is(err, ErrCacheMiss))
}

func TestRedisCache_incrScriptSetsTheWindowOnce(t *testing.T) {
	c := newRedisTestCache(t)

	n, err := c.Incr("rate", 1, 2*time.Second)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	time.Sleep(300 * time.Millisecond)
	_, err = c.Incr("rate", 1, 2*time.Second)
	require.NoError(t, err)

	ttl, err := c.TTL("rate")
	require.NoError(t, err)
	assert.Less(t, ttl, 1900*time.Millisecond, "the window must not slide with traffic inside it")
}

func TestRedisCache_mgetAndDelArePipelined(t *testing.T) {
	c := newRedisTestCache(t)
	require.NoError(t, c.MSet(map[string]any{"a": 1, "b": 2, "c": 3}, time.Minute))

	got, err := GetJSONMany[int](c, "a", "b", "missing")
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"a": 1, "b": 2}, got)

	require.NoError(t, c.Del("a", "b", "never-existed"))
	ok, err := c.Exists("a")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRedisCache_delByPrefixScans(t *testing.T) {
	c := newRedisTestCache(t)
	values := map[string]any{}
	for i := range 1200 { // more than one SCAN batch
		values["session:"+strconv.Itoa(i)] = i
	}
	values["user:1"] = "keep"
	require.NoError(t, c.MSet(values, time.Minute))

	n, err := c.DelByPrefix("session:")
	require.NoError(t, err)
	assert.Equal(t, int64(1200), n)

	ok, err := c.Exists("user:1")
	require.NoError(t, err)
	assert.True(t, ok, "keys outside the prefix survive")
}

func TestRedisLock_isExclusiveAndSelfReleasing(t *testing.T) {
	c := newRedisTestCache(t)

	lock, err := c.Lock("section", time.Minute)
	require.NoError(t, err)

	_, err = c.Lock("section", time.Minute)
	assert.True(t, errors.Is(err, ErrLockNotAcquired))

	require.NoError(t, lock.Extend(2*time.Minute))
	require.NoError(t, lock.Unlock())
	require.NoError(t, lock.Unlock(), "releasing twice is safe")

	again, err := c.Lock("section", time.Minute)
	require.NoError(t, err)
	require.NoError(t, again.Unlock())
}

func TestRedisLock_doesNotReleaseSomebodyElsesTurn(t *testing.T) {
	c := newRedisTestCache(t)

	first, err := c.Lock("section", 500*time.Millisecond)
	require.NoError(t, err)
	time.Sleep(700 * time.Millisecond) // first's hold expires

	second, err := c.Lock("section", time.Minute)
	require.NoError(t, err)
	defer func() { _ = second.Unlock() }()

	require.NoError(t, first.Unlock()) // the late holder tidies up

	_, err = c.Lock("section", time.Minute)
	assert.True(t, errors.Is(err, ErrLockNotAcquired), "the unlock script must compare tokens")
	assert.True(t, errors.Is(first.Extend(time.Minute), ErrLockLost))
}

func TestRedisPubSub_deliversAndMatchesPatterns(t *testing.T) {
	c := newRedisTestCache(t)
	ps := c.PubSub()

	exact, err := ps.Subscribe("order.created")
	require.NoError(t, err)
	defer func() { _ = exact.Close() }()

	glob, err := ps.PSubscribe("order.*")
	require.NoError(t, err)
	defer func() { _ = glob.Close() }()

	require.NoError(t, ps.Publish("order.created", pubsubEvent{ID: "o-1"}))

	msg := receive(t, exact)
	assert.Equal(t, "order.created", msg.Channel, "the prefix is stripped on the way out")
	event, bindErr := BindMessage[pubsubEvent](msg)
	require.NoError(t, bindErr)
	assert.Equal(t, "o-1", event.ID)

	pat := receive(t, glob)
	assert.Equal(t, "order.created", pat.Channel)
	assert.Equal(t, "order.*", pat.Pattern)
}

func TestRedisPubSub_subscriptionOutlivesItsRequestContext(t *testing.T) {
	// a subscription opened from a request handle must not die with the request
	c := newRedisTestCache(t)
	reqCtx, cancel := context.WithCancel(context.Background())

	ps := c.WithContext(reqCtx).PubSub()
	sub, err := ps.Subscribe("events")
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	cancel()
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, c.PubSub().Publish("events", "still here"))
	assert.Equal(t, "still here", receive(t, sub).String())
}

func TestRedisSubscriber_endToEnd(t *testing.T) {
	c := newRedisTestCache(t)
	app := newTestApp(t, WithCache("default", c))

	got := make(chan string, 1)
	sub := app.NewSubscriber(WithSubscriberConcurrency(4))
	sub.On("user.updated", func(ctx IContext, msg *PubSubMessage) error {
		assert.Equal(t, ModeMQ, ctx.Mode())
		got <- msg.String()
		return nil
	})
	require.NoError(t, sub.Start())

	require.NoError(t, c.PubSub().Publish("user.updated", "u-1"))

	select {
	case v := <-got:
		assert.Equal(t, "u-1", v)
	case <-time.After(3 * time.Second):
		t.Fatal("the handler never ran")
	}

	ctx, cancelStop := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelStop()
	require.NoError(t, sub.Stop(ctx))
}
