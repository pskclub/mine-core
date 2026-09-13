package core

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis limiter defaults.
const (
	// DefaultLimiterLease is how long a slot stays held without a heartbeat.
	// It is the window in which a replica that died mid-run still counts against
	// the limit: long enough that an ordinary network hiccup does not hand the
	// slot away, short enough that a crash does not wedge a singleton job.
	DefaultLimiterLease = 30 * time.Second
	// DefaultLimiterPrefix namespaces the limiter's keys under the cache's own
	// prefix.
	DefaultLimiterPrefix = "limiter:"
	// limiterRenewFactor divides the lease to get the heartbeat interval, so a
	// holder has to miss several renewals in a row before it loses its slot.
	limiterRenewFactor = 3
	// limiterOpTimeout bounds the redis calls the heartbeat and release make.
	// Neither has a caller context to inherit — release takes no arguments and
	// the heartbeat outlives the acquiring call — so they need their own.
	limiterOpTimeout = 5 * time.Second
)

// limiterAcquireScript takes a slot if one is free. Expired holders are evicted
// first, which is what makes a crashed replica stop counting.
//
// The clock comes from redis (TIME), never from the caller: with several
// replicas, client clocks disagree, and a limiter that trusted them would evict
// live holders on the fast replica and keep dead ones on the slow one. Needs
// redis 5+, where a script that writes after a non-deterministic command is
// replicated by effects.
//
// KEYS[1] set  ARGV: 1 limit, 2 token, 3 lease ms
var limiterAcquireScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local lease = tonumber(ARGV[3])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[1]) then
  return 0
end
redis.call('ZADD', KEYS[1], now + lease, ARGV[2])
redis.call('PEXPIRE', KEYS[1], lease * 2)
return 1
`)

// limiterRenewScript pushes our expiry out. It re-adds the token even when the
// lease had already lapsed and reports that as 0: the work really is still
// running, so counting it is more honest than leaving the slot free — but the
// caller deserves to know the limit was briefly overshootable.
//
// KEYS[1] set  ARGV: 1 token, 2 lease ms
var limiterRenewScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local lease = tonumber(ARGV[2])
local held = redis.call('ZSCORE', KEYS[1], ARGV[1])
redis.call('ZADD', KEYS[1], now + lease, ARGV[1])
redis.call('PEXPIRE', KEYS[1], lease * 2)
if held then return 1 end
return 0
`)

// limiterCountScript counts holders whose lease has not lapsed. It uses ZCOUNT
// rather than evict-then-ZCARD so that reading a count never writes.
//
// KEYS[1] set
var limiterCountScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
return redis.call('ZCOUNT', KEYS[1], '(' .. now, '+inf')
`)

// limiterConfig is what the options fill in before the limiter is built.
type limiterConfig struct {
	cache     string
	prefix    string
	prefixSet bool
	lease     time.Duration
	log       ILogger
}

// LimiterOption configures a Redis-backed limiter.
type LimiterOption func(*limiterConfig)

// WithLimiterCache picks a named cache registered on the App ("default" when not
// set), for services that keep their coordination redis apart from their cache.
func WithLimiterCache(name string) LimiterOption {
	return func(c *limiterConfig) { c.cache = name }
}

// WithLimiterLease overrides how long a slot is held without a heartbeat.
func WithLimiterLease(d time.Duration) LimiterOption {
	return func(c *limiterConfig) {
		if d > 0 {
			c.lease = d
		}
	}
}

// WithLimiterPrefix replaces the whole key namespace — cache prefix included.
// Give two services the same prefix and they share one limit; that is the only
// way to make a job that runs in both count as one.
func WithLimiterPrefix(prefix string) LimiterOption {
	return func(c *limiterConfig) { c.prefix, c.prefixSet = prefix, true }
}

// WithLimiterLogger overrides the logger the limiter reports redis trouble on.
func WithLimiterLogger(l ILogger) LimiterOption {
	return func(c *limiterConfig) { c.log = l }
}

// redisLimiter counts slots in redis, so MaxConcurrent means the same thing to
// every replica.
//
// A slot is a member of a sorted set scored with its expiry. Acquiring evicts
// lapsed members and adds one if there is room; a heartbeat pushes the expiry
// out while the run lasts; releasing removes the member. The expiry is what
// makes it safe: a replica that is killed mid-run frees its slot by falling
// silent, so nothing has to be cleaned up by hand.
type redisLimiter struct {
	rdb    redis.UniversalClient
	log    ILogger
	prefix string
	lease  time.Duration
}

var _ ILimiter = (*redisLimiter)(nil)

// NewRedisLimiter builds a limiter shared by every replica, so JobDef
// MaxConcurrent and queue limits hold across the cluster instead of per process:
//
//	lim, err := core.NewRedisLimiter(app)
//	runner := core.NewJobRunner(app, reg, core.WithJobLimiter(lim))
//
// It fails when the App has no redis-backed cache, rather than quietly counting
// in one process: a `MaxConcurrent: 1` job that silently runs once per replica is
// the bug this type exists to prevent. A single-replica service wants
// NewInProcessLimiter (the default) and needs no redis at all.
func NewRedisLimiter(app *App, opts ...LimiterOption) (ILimiter, IError) {
	cfg := &limiterConfig{lease: DefaultLimiterLease}
	for _, o := range opts {
		o(cfg)
	}
	if app == nil {
		return nil, New(http.StatusInternalServerError, "LIMITER_NO_APP",
			"job limiter: an App is required")
	}
	c := app.Cache(cfg.cache)
	rdb := c.Redis()
	if rdb == nil {
		name := cfg.cache
		if name == "" {
			name = defaultConn
		}
		return nil, Newf(http.StatusInternalServerError, "LIMITER_NO_REDIS",
			"job limiter: cache %q is not redis-backed, so limits cannot be shared between replicas "+
				"(configure CACHE_* or use core.NewInProcessLimiter for a single replica)", name)
	}
	if !cfg.prefixSet {
		cfg.prefix = c.Prefix() + DefaultLimiterPrefix
	}
	if cfg.log == nil {
		cfg.log = app.Log()
	}
	return newRedisLimiter(rdb, cfg), nil
}

// NewRedisLimiterWithClient wraps an existing redis client, for a client built
// with options this package does not expose — or for a test. rdb is required;
// prefer NewRedisLimiter, which takes it from the cache the App already has.
func NewRedisLimiterWithClient(rdb redis.UniversalClient, opts ...LimiterOption) ILimiter {
	cfg := &limiterConfig{lease: DefaultLimiterLease, prefix: DefaultLimiterPrefix}
	for _, o := range opts {
		o(cfg)
	}
	return newRedisLimiter(rdb, cfg)
}

func newRedisLimiter(rdb redis.UniversalClient, cfg *limiterConfig) *redisLimiter {
	lease := cfg.lease
	if lease <= 0 {
		lease = DefaultLimiterLease
	}
	return &redisLimiter{rdb: rdb, log: cfg.log, prefix: cfg.prefix, lease: lease}
}

// key is the sorted set holding the slots of one limiter key.
func (l *redisLimiter) key(key string) string { return l.prefix + key }

// Acquire takes a slot for key, or reports that the limit is reached.
//
// A redis failure counts as "no slot", never as "go ahead": under the default
// ConcurrencyEnqueue the run comes back after the slot backoff, so an outage
// delays work instead of running a singleton job twice. Worth knowing for
// ConcurrencySkip, where the same outage skips runs — the log line says so.
func (l *redisLimiter) Acquire(ctx context.Context, key string, limit int) (func(), bool) {
	if limit <= 0 {
		return func() {}, true
	}
	if ctx == nil {
		ctx = context.Background()
	}

	k, token := l.key(key), lockToken()
	held, err := limiterAcquireScript.Run(ctx, l.rdb, []string{k},
		limit, token, l.lease.Milliseconds()).Int64()
	if err != nil {
		l.error("job limiter: cannot acquire slot, treating it as unavailable",
			"key", key, "limit", limit, "err", err)
		return nil, false
	}
	if held == 0 {
		return nil, false
	}

	stop := make(chan struct{})
	go l.heartbeat(k, key, token, stop)

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			rctx, cancel := context.WithTimeout(context.Background(), limiterOpTimeout)
			defer cancel()
			if err := l.rdb.ZRem(rctx, k, token).Err(); err != nil {
				// not fatal: the slot frees itself when the lease lapses, which
				// is the same path a crashed holder takes
				l.warn("job limiter: cannot release slot; it will expire with its lease",
					"key", key, "lease", l.lease, "err", err)
			}
		})
	}, true
}

// heartbeat keeps this holder's lease alive until release is called. It runs on
// its own context rather than the caller's: a run that is being canceled still
// occupies its slot until the handler has actually returned, and dropping the
// lease early would let a second replica start while the first is still winding
// down.
func (l *redisLimiter) heartbeat(k, key, token string, stop <-chan struct{}) {
	interval := l.lease / limiterRenewFactor
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), limiterOpTimeout)
			held, err := limiterRenewScript.Run(ctx, l.rdb, []string{k},
				token, l.lease.Milliseconds()).Int64()
			cancel()
			switch {
			case err != nil:
				l.warn("job limiter: cannot renew slot lease", "key", key, "err", err)
			case held == 0:
				l.warn("job limiter: slot lease lapsed while the run was still going; re-registered it",
					"key", key, "lease", l.lease)
			}
		}
	}
}

// Count reports how many slots of key are held right now, ignoring holders whose
// lease has lapsed. A redis failure answers zero, the same thing the disabled
// cache answers for a counter: the alternative is an error this interface has
// nowhere to put.
func (l *redisLimiter) Count(ctx context.Context, key string) int {
	if ctx == nil {
		ctx = context.Background()
	}
	n, err := limiterCountScript.Run(ctx, l.rdb, []string{l.key(key)}).Int64()
	if err != nil {
		l.warn("job limiter: cannot count slots", "key", key, "err", err)
		return 0
	}
	return int(n)
}

// warn and error report through the configured logger, if there is one. The
// limiter is usable without one (NewRedisLimiterWithClient in a test), and a nil
// logger must not turn a redis hiccup into a panic.
func (l *redisLimiter) warn(msg string, args ...any) {
	if l.log != nil {
		l.log.Warn(msg, args...)
	}
}

func (l *redisLimiter) error(msg string, args ...any) {
	if l.log != nil {
		l.log.Error(msg, args...)
	}
}
