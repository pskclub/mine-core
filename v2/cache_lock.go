package core

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrLockNotAcquired is wrapped by the error Lock returns when somebody else
// holds the lock. Compare with errors.Is(err, core.ErrLockNotAcquired).
var ErrLockNotAcquired = errors.New("cache: lock not acquired")

// ErrLockLost is wrapped by the error Extend returns when the lock has expired
// or been taken over — the work it was guarding is no longer protected.
var ErrLockLost = errors.New("cache: lock lost")

// ILock is a lock held across processes. It always expires: a holder that
// crashes releases it by doing nothing, so one bad deploy cannot wedge a queue
// forever.
type ILock interface {
	// Key is the lock's key, without the cache prefix.
	Key() string
	// Extend pushes the expiry out. It fails once the lock has been lost, which
	// is the signal to stop the work it was guarding.
	Extend(ttl time.Duration) IError
	// Unlock releases the lock, and only ever releases *this* holder's lock.
	// Safe to call twice, and safe to defer.
	Unlock() IError
}

// lockToken identifies one holder, so a lock that expired mid-work and was taken
// over by somebody else is not released by the previous holder on its way out.
func lockToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail in practice; a timestamp still gives a
		// value distinct enough to keep the compare-and-delete honest.
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b)
}

// defaultLockTTL is used when a lock is asked for without one. Short enough that
// a crashed holder is forgotten quickly, long enough for ordinary work.
const defaultLockTTL = 30 * time.Second

// ---------------------------------------------------------------------------
// Redis lock
// ---------------------------------------------------------------------------

// unlockScript deletes the key only when it still holds our token. A plain DEL
// would release a lock that had already expired and been acquired by somebody
// else — two workers in the section that was supposed to have one.
var unlockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

// extendScript is unlockScript's counterpart: refresh the expiry, but only while
// we are still the holder.
var extendScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

type redisLock struct {
	c     *cache
	key   string
	token string

	mu       sync.Mutex
	released bool
}

var _ ILock = (*redisLock)(nil)

func (c *cache) Lock(key string, ttl time.Duration) (ILock, IError) {
	if ttl <= 0 {
		ttl = defaultLockTTL
	}
	token := lockToken()
	ok, err := c.rdb.SetNX(c.ctx, c.k(key), token, ttl).Result()
	if err != nil {
		return nil, c.fail("lock", key, err)
	}
	if !ok {
		return nil, lockNotAcquired(key)
	}
	return &redisLock{c: c, key: key, token: token}, nil
}

func (l *redisLock) Key() string { return l.key }

func (l *redisLock) Extend(ttl time.Duration) IError {
	if ttl <= 0 {
		ttl = defaultLockTTL
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return Wrap(ErrLockLost, "cache: extend").WithCode("LOCK_LOST").WithStatus(409)
	}
	n, err := extendScript.Run(l.c.ctx, l.c.rdb, []string{l.c.k(l.key)}, l.token, ttl.Milliseconds()).Int64()
	if err != nil {
		return l.c.fail("extend", l.key, err)
	}
	if n == 0 {
		return Wrap(ErrLockLost, "cache: extend").WithCode("LOCK_LOST").WithStatus(409)
	}
	return nil
}

func (l *redisLock) Unlock() IError {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	if err := unlockScript.Run(l.c.ctx, l.c.rdb, []string{l.c.k(l.key)}, l.token).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return l.c.fail("unlock", l.key, err)
	}
	return nil
}

// lockNotAcquired is the ordinary "somebody else has it" outcome: a 409, not a
// server error, so a handler that returns it says the right thing to the client.
func lockNotAcquired(key string) *Error {
	return &Error{
		Status:  409,
		Code:    "LOCK_NOT_ACQUIRED",
		Message: "cache: lock is held by someone else",
		cause:   fmt.Errorf("%w: %s", ErrLockNotAcquired, key),
	}
}

// ---------------------------------------------------------------------------
// Disabled lock
// ---------------------------------------------------------------------------

type noopLock struct{ key string }

var _ ILock = noopLock{}

func (n noopLock) Key() string                 { return n.key }
func (n noopLock) Extend(time.Duration) IError { return nil }
func (n noopLock) Unlock() IError              { return nil }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// lockRetryInterval is how often LockWait tries again. Redis has no blocking
// acquire, so waiting means polling; 50ms is short enough to feel immediate and
// long enough not to be a busy loop.
const lockRetryInterval = 50 * time.Millisecond

// LockWait acquires a lock, waiting up to wait for a turn. It gives up with an
// ErrLockNotAcquired error rather than blocking forever, so a jammed lock shows
// up as a failed request instead of an exhausted goroutine pool.
func LockWait(c ICache, key string, ttl, wait time.Duration) (ILock, IError) {
	cc := orNoop(c)
	lock, err := cc.Lock(key, ttl)
	if err == nil || wait <= 0 {
		return lock, err
	}
	if !errors.Is(err, ErrLockNotAcquired) {
		return nil, err // a real failure: retrying will not help
	}

	deadline := time.Now().Add(wait)
	ticker := time.NewTicker(lockRetryInterval)
	defer ticker.Stop()
	for {
		<-ticker.C
		lock, err = cc.Lock(key, ttl)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, ErrLockNotAcquired) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, err
		}
	}
}

// WithLock runs fn while holding key, and releases it afterwards however fn
// ends — including a panic. It is the shape almost every caller wants:
//
//	err := core.WithLock(ctx.Cache(), "settle:"+id, time.Minute, func() error {
//	    return settle(ctx, id)
//	})
//
// When somebody else holds the lock it returns an ErrLockNotAcquired error
// without running fn.
func WithLock(c ICache, key string, ttl time.Duration, fn func() error) IError {
	lock, err := orNoop(c).Lock(key, ttl)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()

	var runErr error
	func() {
		defer Recover(&runErr)
		runErr = fn()
	}()
	if runErr != nil {
		return Wrap(runErr, "")
	}
	return nil
}
