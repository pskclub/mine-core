package core

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLock_excludesASecondHolder(t *testing.T) {
	c := newTestCache(t)

	lock, err := c.Lock("settle:1", time.Minute)
	require.NoError(t, err)

	_, err = c.Lock("settle:1", time.Minute)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLockNotAcquired))
	assert.Equal(t, 409, err.GetStatus(), "contention is a conflict, not a server error")

	require.NoError(t, lock.Unlock())

	again, err := c.Lock("settle:1", time.Minute)
	require.NoError(t, err, "the lock is free once it is released")
	require.NoError(t, again.Unlock())
}

func TestLock_unlockIsIdempotent(t *testing.T) {
	c := newTestCache(t)
	lock, err := c.Lock("k", time.Minute)
	require.NoError(t, err)

	require.NoError(t, lock.Unlock())
	require.NoError(t, lock.Unlock(), "a deferred Unlock after an explicit one must not fail")
}

func TestLock_releasesItselfByExpiring(t *testing.T) {
	// what stops a crashed holder from wedging the section forever
	c := newTestCache(t)
	_, err := c.Lock("k", 30*time.Millisecond)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)

	lock, err := c.Lock("k", time.Minute)
	require.NoError(t, err)
	require.NoError(t, lock.Unlock())
}

func TestLock_doesNotReleaseSomebodyElsesTurn(t *testing.T) {
	c := newTestCache(t)
	first, err := c.Lock("k", 30*time.Millisecond)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond) // first's hold expires
	second, err := c.Lock("k", time.Minute)
	require.NoError(t, err)

	require.NoError(t, first.Unlock()) // the late holder tidies up

	_, err = c.Lock("k", time.Minute)
	assert.True(t, errors.Is(err, ErrLockNotAcquired), "second must still hold it")
	require.NoError(t, second.Unlock())
}

func TestLock_extendFailsOnceLost(t *testing.T) {
	c := newTestCache(t)
	lock, err := c.Lock("k", 30*time.Millisecond)
	require.NoError(t, err)

	require.NoError(t, lock.Extend(time.Minute))

	ttl, err := c.TTL("k")
	require.NoError(t, err)
	assert.Greater(t, ttl, 30*time.Second)

	require.NoError(t, lock.Unlock())
	assert.True(t, errors.Is(lock.Extend(time.Minute), ErrLockLost))
}

func TestLockWait_takesItsTurn(t *testing.T) {
	c := newTestCache(t)
	held, err := c.Lock("k", time.Minute)
	require.NoError(t, err)

	go func() {
		time.Sleep(80 * time.Millisecond)
		_ = held.Unlock()
	}()

	started := time.Now()
	lock, err := LockWait(c, "k", time.Minute, time.Second)
	require.NoError(t, err)
	assert.Greater(t, time.Since(started), 50*time.Millisecond, "it waited rather than failing")
	require.NoError(t, lock.Unlock())
}

func TestLockWait_givesUp(t *testing.T) {
	c := newTestCache(t)
	held, err := c.Lock("k", time.Minute)
	require.NoError(t, err)
	defer func() { _ = held.Unlock() }()

	_, err = LockWait(c, "k", time.Minute, 100*time.Millisecond)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLockNotAcquired), "a jammed lock fails the caller instead of blocking forever")
}

func TestWithLock_runsOneAtATime(t *testing.T) {
	c := newTestCache(t)
	var mu sync.Mutex
	var inside, maxInside int

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = WithLock(c, "section", time.Minute, func() error {
				mu.Lock()
				inside++
				if inside > maxInside {
					maxInside = inside
				}
				mu.Unlock()

				time.Sleep(10 * time.Millisecond)

				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, maxInside, "the section held one goroutine at a time")
}

func TestWithLock_releasesAfterAPanic(t *testing.T) {
	c := newTestCache(t)

	err := WithLock(c, "section", time.Minute, func() error {
		panic("boom")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic recovered")

	lock, lockErr := c.Lock("section", time.Minute)
	require.NoError(t, lockErr, "a panic must not leave the lock held")
	require.NoError(t, lock.Unlock())
}
