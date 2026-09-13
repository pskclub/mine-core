package core

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cacheUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func newTestCache(t *testing.T) ICache {
	t.Helper()
	c := NewMemoryCache()
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestCache_setGetRoundTrip(t *testing.T) {
	c := newTestCache(t)

	require.NoError(t, c.Set("user:1", cacheUser{ID: "1", Name: "ann"}, time.Minute))

	var got cacheUser
	require.NoError(t, c.Get("user:1", &got))
	assert.Equal(t, cacheUser{ID: "1", Name: "ann"}, got)
}

func TestCache_stringsAndBytesStayRaw(t *testing.T) {
	c := newTestCache(t)

	require.NoError(t, c.Set("token", "abc", time.Minute))
	require.NoError(t, c.Set("blob", []byte("xyz"), time.Minute))

	var s string
	require.NoError(t, c.Get("token", &s))
	assert.Equal(t, "abc", s, "a string is stored as itself, not as a JSON string")

	var b []byte
	require.NoError(t, c.Get("blob", &b))
	assert.Equal(t, []byte("xyz"), b)
}

func TestCache_timeSurvivesTheRoundTrip(t *testing.T) {
	// time.Time carries its own text form; storing that instead of JSON would
	// write something Get could not read back
	c := newTestCache(t)
	at := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)

	require.NoError(t, c.Set("at", at, time.Minute))

	var got time.Time
	require.NoError(t, c.Get("at", &got))
	assert.True(t, at.Equal(got), "want %s, got %s", at, got)
}

func TestCache_missIsIdentifiable(t *testing.T) {
	c := newTestCache(t)

	var got cacheUser
	err := c.Get("nobody", &got)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCacheMiss), "a miss must be tellable from a failure")
	assert.Equal(t, "CACHE_MISS", err.GetCode())
}

func TestCache_deleteAndExists(t *testing.T) {
	c := newTestCache(t)
	require.NoError(t, c.Set("a", 1, time.Minute))
	require.NoError(t, c.Set("b", 2, time.Minute))

	ok, err := c.Exists("a")
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, c.Del("a", "b", "never-existed"))

	ok, err = c.Exists("a")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCache_expiry(t *testing.T) {
	c := newTestCache(t)
	require.NoError(t, c.Set("short", "v", 20*time.Millisecond))

	ttl, err := c.TTL("short")
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0))

	time.Sleep(40 * time.Millisecond)

	var v string
	assert.True(t, errors.Is(c.Get("short", &v), ErrCacheMiss), "an expired key reads as a miss")
}

func TestCache_ttlOfAKeyWithoutOne(t *testing.T) {
	c := newTestCache(t)
	require.NoError(t, c.Set("forever", "v", NoExpiry))

	ttl, err := c.TTL("forever")
	require.NoError(t, err)
	assert.Equal(t, TTLNoExpiry, ttl)

	_, err = c.TTL("absent")
	assert.True(t, errors.Is(err, ErrCacheMiss))
}

func TestCache_expireSetsATTLAfterTheFact(t *testing.T) {
	c := newTestCache(t)
	require.NoError(t, c.Set("k", "v", NoExpiry))

	ok, err := c.Expire("k", time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)

	ttl, err := c.TTL("k")
	require.NoError(t, err)
	assert.InDelta(t, time.Minute.Seconds(), ttl.Seconds(), 1)

	ok, err = c.Expire("absent", time.Minute)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCache_keepTTL(t *testing.T) {
	c := newTestCache(t)
	require.NoError(t, c.Set("k", "first", time.Hour))

	require.NoError(t, c.Set("k", "second", KeepTTL))

	ttl, err := c.TTL("k")
	require.NoError(t, err)
	assert.Greater(t, ttl, 50*time.Minute, "rewriting with KeepTTL must not drop the expiry")
}

func TestCache_setNXOnlyWritesOnce(t *testing.T) {
	c := newTestCache(t)

	first, err := c.SetNX("idem:1", "a", time.Minute)
	require.NoError(t, err)
	assert.True(t, first)

	second, err := c.SetNX("idem:1", "b", time.Minute)
	require.NoError(t, err)
	assert.False(t, second)

	var v string
	require.NoError(t, c.Get("idem:1", &v))
	assert.Equal(t, "a", v, "the second write must not overwrite the first")
}

func TestCache_getDelIsSingleUse(t *testing.T) {
	c := newTestCache(t)
	require.NoError(t, c.Set("otp:1", "123456", time.Minute))

	var code string
	require.NoError(t, c.GetDel("otp:1", &code))
	assert.Equal(t, "123456", code)

	assert.True(t, errors.Is(c.GetDel("otp:1", &code), ErrCacheMiss), "a one-time value cannot be replayed")
}

func TestCache_mgetSkipsAbsentKeys(t *testing.T) {
	c := newTestCache(t)
	require.NoError(t, c.MSet(map[string]any{
		"u:1": cacheUser{ID: "1"},
		"u:2": cacheUser{ID: "2"},
	}, time.Minute))

	got, err := GetJSONMany[cacheUser](c, "u:1", "u:2", "u:3")
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, "1", got["u:1"].ID)
	assert.NotContains(t, got, "u:3")
}

func TestCache_delByPrefix(t *testing.T) {
	c := newTestCache(t)
	require.NoError(t, c.MSet(map[string]any{
		"session:a": 1, "session:b": 2, "user:a": 3,
	}, time.Minute))

	n, err := c.DelByPrefix("session:")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	ok, err := c.Exists("user:a")
	require.NoError(t, err)
	assert.True(t, ok, "keys outside the prefix are untouched")
}

func TestCache_incrKeepsTheWindowItCreated(t *testing.T) {
	c := newTestCache(t)

	n, err := c.Incr("rate:ip", 1, 50*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	time.Sleep(20 * time.Millisecond)
	n, err = c.Incr("rate:ip", 1, 50*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	ttl, err := c.TTL("rate:ip")
	require.NoError(t, err)
	assert.Less(t, ttl, 40*time.Millisecond, "traffic inside a window must not extend it")

	time.Sleep(50 * time.Millisecond)
	n, err = c.Incr("rate:ip", 1, 50*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "the window resets")
}

func TestCache_incrIsAtomicUnderConcurrency(t *testing.T) {
	c := newTestCache(t)

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Incr("counter", 1, time.Minute)
		}()
	}
	wg.Wait()

	n, err := c.Incr("counter", 0, time.Minute) // a delta of 0 reads it
	require.NoError(t, err)
	assert.Equal(t, int64(50), n, "no increment was lost to a race")
}

func TestCache_prefixNamespacesKeys(t *testing.T) {
	c := newTestCache(t)
	otp := c.WithPrefix("otp")

	assert.Equal(t, "otp:", otp.Prefix(), "a missing separator is added")
	require.NoError(t, otp.Set("1", "aaa", time.Minute))

	var v string
	require.NoError(t, otp.Get("1", &v))
	assert.Equal(t, "aaa", v)

	assert.True(t, errors.Is(c.Get("1", &v), ErrCacheMiss), "the unprefixed handle must not see it")
	ok, err := c.Exists("otp:1")
	require.NoError(t, err)
	assert.True(t, ok, "the key really is stored under the prefix")
}

// --- helpers -------------------------------------------------------------

func TestRemember_cachesTheComputedValue(t *testing.T) {
	c := newTestCache(t)
	calls := 0
	load := func() (cacheUser, error) {
		calls++
		return cacheUser{ID: "1", Name: "ann"}, nil
	}

	first, err := Remember(c, "user:1", time.Minute, load)
	require.NoError(t, err)
	second, err := Remember(c, "user:1", time.Minute, load)
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Equal(t, 1, calls, "the second call is served from the cache")
}

func TestRemember_failsWhenTheLoaderDoes(t *testing.T) {
	c := newTestCache(t)

	_, err := Remember(c, "user:1", time.Minute, func() (cacheUser, error) {
		return cacheUser{}, errors.New("db down")
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache: load")
}

func TestRemember_worksWithoutACache(t *testing.T) {
	// the bug this guards: ctx.Cache() used to be nil in a service with no
	// redis, and every helper here panicked on it
	ctx := newTestApp(t).NewContext(t.Context())

	calls := 0
	for range 2 {
		v, err := Remember(ctx.Cache(), "user:1", time.Minute, func() (cacheUser, error) {
			calls++
			return cacheUser{ID: "1"}, nil
		})
		require.NoError(t, err)
		assert.Equal(t, "1", v.ID)
	}
	assert.Equal(t, 2, calls, "with no cache every call loads — but no call panics")
}

func TestHelpers_surviveANilCache(t *testing.T) {
	var c ICache // never assigned: a hand-built context, an unset field

	assert.NotPanics(t, func() {
		_, _ = GetJSON[cacheUser](c, "k")
		_ = SetJSON(c, "k", cacheUser{}, time.Minute)
		_, _ = GetJSONMany[cacheUser](c, "k")
		Forget(c, "k")
		_, _ = Remember(c, "k", time.Minute, func() (cacheUser, error) { return cacheUser{}, nil })
	})
}

func TestRememberOnce_loadsOnceUnderAStampede(t *testing.T) {
	c := newTestCache(t)
	var mu sync.Mutex
	calls := 0

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = RememberOnce(c, "hot", time.Minute, time.Second, func() (cacheUser, error) {
				mu.Lock()
				calls++
				mu.Unlock()
				time.Sleep(30 * time.Millisecond)
				return cacheUser{ID: "1"}, nil
			})
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, calls, "one caller computes, the rest read what it published")
}

func TestNoopCache_readsMissAndWritesAreDropped(t *testing.T) {
	c := NewNoopCache()

	assert.False(t, c.Enabled())
	require.NoError(t, c.Set("k", "v", time.Minute))

	var v string
	assert.True(t, errors.Is(c.Get("k", &v), ErrCacheMiss))
	assert.Nil(t, c.Redis())

	// a lock is granted: without a shared cache there is nothing to coordinate
	// with, and refusing would stop the work from running at all
	lock, err := c.Lock("job", time.Minute)
	require.NoError(t, err)
	require.NoError(t, lock.Unlock())
}
