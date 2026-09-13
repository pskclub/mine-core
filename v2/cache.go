package core

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrCacheMiss is wrapped by the error returned when a key is absent. Compare
// with errors.Is(err, core.ErrCacheMiss).
var ErrCacheMiss = errors.New("cache: miss")

// ErrCacheDisabled is wrapped by the error returned when an operation needs a
// real cache and none is configured (subscribing to pub/sub, for instance).
// Reads and writes never return it — they degrade to a miss.
var ErrCacheDisabled = errors.New("cache: not configured")

const (
	// KeepTTL as the ttl of Set keeps whatever expiry the key already had.
	KeepTTL = redis.KeepTTL
	// NoExpiry as the ttl of Set stores the value until it is deleted. It is
	// also what TTL reports for a key that never expires.
	NoExpiry = time.Duration(0)
	// TTLNoExpiry is what TTL returns for a key that exists but has no expiry.
	TTLNoExpiry = time.Duration(-1)
)

// ICache is the cache abstraction. Same name as v1; methods no longer take a ctx
// because ctx.Cache() returns a handle already bound to the request context.
//
// ctx.Cache() is never nil: a service with no CACHE_* configuration gets a
// disabled cache whose reads miss and whose writes are dropped, so cache-aside
// code (Remember) runs unchanged in an environment that has no redis. Check
// Enabled when a code path genuinely needs one.
type ICache interface {
	// --- values ---

	// Get reads key into dest. dest may be *string, *[]byte or *json.RawMessage
	// for the raw bytes; anything else is JSON-decoded. The error wraps
	// ErrCacheMiss when the key is absent.
	Get(key string, dest any) IError
	// Set stores value under key. string/[]byte are stored as-is, everything
	// else is JSON-encoded. ttl of NoExpiry stores it forever, KeepTTL keeps the
	// key's current expiry.
	Set(key string, value any, ttl time.Duration) IError
	// SetNX stores value only when key does not exist, and reports whether it
	// did. This is the primitive behind idempotency keys and one-shot flags.
	SetNX(key string, value any, ttl time.Duration) (bool, IError)
	// GetDel reads key into dest and deletes it in the same round trip — for
	// one-time values (OTPs, single-use tokens) that must not be replayed.
	GetDel(key string, dest any) IError
	// MGet reads many keys at once. Absent keys are simply missing from the
	// result, which is therefore empty rather than an error when none exist.
	MGet(keys ...string) (map[string][]byte, IError)
	// MSet writes many keys with one ttl in a single pipeline.
	MSet(values map[string]any, ttl time.Duration) IError
	// Del removes keys. Absent keys are not an error.
	Del(keys ...string) IError
	// DelByPrefix removes every key under a prefix and returns how many it
	// deleted. It scans in batches instead of running KEYS, so it is safe to
	// call against a production instance.
	DelByPrefix(prefix string) (int64, IError)
	// Exists reports whether key is present.
	Exists(key string) (bool, IError)
	// Expire sets a key's ttl and reports whether the key existed.
	Expire(key string, ttl time.Duration) (bool, IError)
	// TTL returns a key's remaining life, TTLNoExpiry when it has none, and an
	// ErrCacheMiss error when the key is absent.
	TTL(key string) (time.Duration, IError)

	// --- counters ---

	// Incr adds delta to a counter and returns the new value; a negative delta
	// subtracts and a delta of 0 reads it (creating it at zero). When the
	// counter is created by this call and ttl is positive, the ttl is applied to
	// it — so a fixed-window rate limiter is one call, not three.
	Incr(key string, delta int64, ttl time.Duration) (int64, IError)

	// --- coordination ---

	// Lock acquires a mutual-exclusion lock held for at most ttl. It tries once;
	// the returned error wraps ErrLockNotAcquired when somebody else holds it.
	// Use LockWait to wait for a turn.
	Lock(key string, ttl time.Duration) (ILock, IError)

	// --- pub/sub ---

	// PubSub is the publish/subscribe side of the same connection. Never nil.
	PubSub() IPubSub

	// --- plumbing ---

	// Ping checks the connection is usable — for a readiness probe.
	Ping() IError
	// Enabled reports whether this is a real cache. False for the disabled cache
	// a service with no CACHE_* configuration gets.
	Enabled() bool
	// Prefix is the string every key of this handle is stored under.
	Prefix() string
	// WithPrefix returns a handle that namespaces keys further, e.g.
	// ctx.Cache().WithPrefix("otp") writes "otp:<key>". A ":" is appended when
	// the prefix does not already end in one.
	WithPrefix(prefix string) ICache
	// WithContext returns a handle bound to a different context (escape hatch
	// for work that must outlive the request).
	WithContext(ctx context.Context) ICache
	// Redis exposes the underlying client for anything this interface does not
	// cover. It is nil for the memory and disabled caches, and it does not apply
	// the prefix — build keys with Prefix() when you use it.
	Redis() redis.UniversalClient
	// Close releases the connection. Owned by App.Shutdown; call it yourself
	// only for a cache you built outside an App.
	Close() IError
}

type cache struct {
	ctx    context.Context
	rdb    redis.UniversalClient
	prefix string
}

var _ ICache = (*cache)(nil)

// CacheOption tunes a cache at construction.
type CacheOption func(*cache)

// WithCachePrefix namespaces every key of the cache being built. It overrides
// CACHE_PREFIX.
func WithCachePrefix(prefix string) CacheOption {
	return func(c *cache) { c.prefix = normalizePrefix(prefix) }
}

// NewCache connects to Redis using configuration and returns a ready ICache.
// It accepts a full URI (CACHE_CONNECTION_STRING, e.g.
// "redis://user:pass@host:6379/0" or "rediss://..."), a comma-separated node
// list (CACHE_ADDRS, for cluster), a sentinel master (CACHE_MASTER_NAME), or the
// discrete CACHE_HOST/CACHE_PORT/CACHE_USERNAME/CACHE_PASSWORD/CACHE_DB fields.
//
// It fails fast: the connection is verified with a PING before it is returned,
// so a misconfigured cache is a boot error rather than a surprise on the first
// request.
func NewCache(env IENV, opts ...CacheOption) (ICache, IError) {
	cfg := env.Config()
	rdb, err := newRedisClient(cfg)
	if err != nil {
		return nil, err
	}

	c := &cache{ctx: context.Background(), rdb: rdb, prefix: normalizePrefix(cfg.CachePrefix)}
	for _, o := range opts {
		o(c)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cacheDialTimeout(cfg))
	defer cancel()
	if pingErr := rdb.Ping(ctx).Err(); pingErr != nil {
		_ = rdb.Close()
		return nil, Wrap(pingErr, "cache: ping")
	}
	return c, nil
}

// NewCacheWithClient wraps an existing redis client (useful for tests, or for a
// client built with options this package does not expose).
func NewCacheWithClient(rdb redis.UniversalClient, opts ...CacheOption) ICache {
	c := &cache{ctx: context.Background(), rdb: rdb}
	for _, o := range opts {
		o(c)
	}
	return c
}

// newRedisClient builds a client from config without connecting (testable).
func newRedisClient(cfg *ENVConfig) (redis.UniversalClient, IError) {
	opts, err := redisOptions(cfg)
	if err != nil {
		return nil, err
	}
	return redis.NewUniversalClient(opts), nil
}

// redisOptions turns configuration into universal options, which cover a single
// node, a sentinel-managed master and a cluster with the same fields — the
// deployment shape then stops being a code decision.
func redisOptions(cfg *ENVConfig) (*redis.UniversalOptions, IError) {
	opts := &redis.UniversalOptions{}

	if cfg.CacheConnectionString != "" {
		parsed, err := redis.ParseURL(cfg.CacheConnectionString)
		if err != nil {
			return nil, Wrap(err, "cache: parse connection string")
		}
		opts.Addrs = []string{parsed.Addr}
		opts.DB = parsed.DB
		opts.Username = parsed.Username
		opts.Password = parsed.Password
		opts.TLSConfig = parsed.TLSConfig
	} else {
		opts.Addrs = cacheAddrs(cfg)
		opts.DB = cfg.CacheDB
		opts.Username = cfg.CacheUsername
		opts.Password = cfg.CachePassword
		if cfg.CacheTLS {
			opts.TLSConfig = &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: cfg.CacheTLSSkipVerify, //nolint:gosec // opt-in, for self-signed instances
			}
		}
	}

	// A master name switches the same address list from "these are nodes" to
	// "these are sentinels".
	opts.MasterName = cfg.CacheMasterName
	opts.SentinelPassword = cfg.CacheSentinelPassword

	if cfg.CacheMaxRetries != 0 {
		opts.MaxRetries = cfg.CacheMaxRetries // -1 disables, 0 keeps the driver default
	}
	if cfg.CachePoolSize > 0 {
		opts.PoolSize = cfg.CachePoolSize
	}
	if cfg.CacheMinIdleConns > 0 {
		opts.MinIdleConns = cfg.CacheMinIdleConns
	}
	if cfg.CacheDialTimeout > 0 {
		opts.DialTimeout = time.Duration(cfg.CacheDialTimeout) * time.Second
	}
	if cfg.CacheReadTimeout > 0 {
		opts.ReadTimeout = time.Duration(cfg.CacheReadTimeout) * time.Second
	}
	if cfg.CacheWriteTimeout > 0 {
		opts.WriteTimeout = time.Duration(cfg.CacheWriteTimeout) * time.Second
	}
	return opts, nil
}

// cacheAddrs is the node list: CACHE_ADDRS when set, otherwise the single
// host/port pair, defaulted so a local redis needs no configuration at all.
func cacheAddrs(cfg *ENVConfig) []string {
	if cfg.CacheAddrs != "" {
		parts := strings.Split(cfg.CacheAddrs, ",")
		addrs := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				addrs = append(addrs, p)
			}
		}
		if len(addrs) > 0 {
			return addrs
		}
	}
	host := cfg.CacheHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.CachePort
	if port == "" {
		port = "6379"
	}
	return []string{net.JoinHostPort(host, port)}
}

func cacheDialTimeout(cfg *ENVConfig) time.Duration {
	if cfg != nil && cfg.CacheDialTimeout > 0 {
		return time.Duration(cfg.CacheDialTimeout) * time.Second
	}
	return 5 * time.Second
}

// normalizePrefix makes "svc" and "svc:" mean the same thing, so a key never
// silently lands in a different namespace than the one that was intended.
func normalizePrefix(prefix string) string {
	if prefix == "" || strings.HasSuffix(prefix, ":") {
		return prefix
	}
	return prefix + ":"
}

// ---------------------------------------------------------------------------
// Handle plumbing
// ---------------------------------------------------------------------------

func (c *cache) WithContext(ctx context.Context) ICache {
	if ctx == nil {
		ctx = context.Background()
	}
	cp := *c
	cp.ctx = ctx
	return &cp
}

func (c *cache) WithPrefix(prefix string) ICache {
	cp := *c
	cp.prefix += normalizePrefix(prefix)
	return &cp
}

func (c *cache) Prefix() string { return c.prefix }

func (c *cache) Enabled() bool { return true }

func (c *cache) Redis() redis.UniversalClient { return c.rdb }

func (c *cache) PubSub() IPubSub {
	return &redisPubSub{ctx: c.ctx, rdb: c.rdb, prefix: c.prefix}
}

func (c *cache) Ping() IError {
	if err := c.rdb.Ping(c.ctx).Err(); err != nil {
		return c.fail("ping", "", err)
	}
	return nil
}

func (c *cache) Close() IError {
	if err := c.rdb.Close(); err != nil {
		return Wrap(err, "cache: close")
	}
	return nil
}

// k is the stored key: the caller's key under this handle's namespace.
func (c *cache) k(key string) string { return c.prefix + key }

// fail wraps a driver error and leaves a breadcrumb. Cache failures are the kind
// of thing that explains a later timeout, so they are worth recording even
// though the caller usually degrades and carries on.
func (c *cache) fail(op, key string, err error) IError {
	breadcrumbTo(c.ctx, Breadcrumb{
		Type: "error", Category: "cache." + op, Level: LevelError,
		Message: op + " " + key,
		Data:    map[string]any{"error": err.Error()},
	})
	return Wrap(err, "cache: "+op)
}

// ---------------------------------------------------------------------------
// Values
// ---------------------------------------------------------------------------

func (c *cache) Get(key string, dest any) IError {
	b, err := c.rdb.Get(c.ctx, c.k(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return cacheMiss(key)
	}
	if err != nil {
		return c.fail("get", key, err)
	}
	return decodeCacheValue(b, dest)
}

func (c *cache) Set(key string, value any, ttl time.Duration) IError {
	raw, err := encodeCacheValue(value)
	if err != nil {
		return err
	}
	if setErr := c.rdb.Set(c.ctx, c.k(key), raw, ttl).Err(); setErr != nil {
		return c.fail("set", key, setErr)
	}
	return nil
}

func (c *cache) SetNX(key string, value any, ttl time.Duration) (bool, IError) {
	raw, err := encodeCacheValue(value)
	if err != nil {
		return false, err
	}
	ok, setErr := c.rdb.SetNX(c.ctx, c.k(key), raw, ttl).Result()
	if setErr != nil {
		return false, c.fail("setnx", key, setErr)
	}
	return ok, nil
}

func (c *cache) GetDel(key string, dest any) IError {
	b, err := c.rdb.GetDel(c.ctx, c.k(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return cacheMiss(key)
	}
	if err != nil {
		return c.fail("getdel", key, err)
	}
	return decodeCacheValue(b, dest)
}

func (c *cache) MGet(keys ...string) (map[string][]byte, IError) {
	out := make(map[string][]byte, len(keys))
	if len(keys) == 0 {
		return out, nil
	}

	// MGET is a single-slot command: on a cluster the keys may live on different
	// nodes, so ask for them one by one through a pipeline instead.
	pipe := c.rdb.Pipeline()
	cmds := make([]*redis.StringCmd, len(keys))
	for i, key := range keys {
		cmds[i] = pipe.Get(c.ctx, c.k(key))
	}
	if _, err := pipe.Exec(c.ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, c.fail("mget", "", err)
	}
	for i, cmd := range cmds {
		b, err := cmd.Bytes()
		if errors.Is(err, redis.Nil) {
			continue // absent keys are simply not in the result
		}
		if err != nil {
			return nil, c.fail("mget", keys[i], err)
		}
		out[keys[i]] = b
	}
	return out, nil
}

func (c *cache) MSet(values map[string]any, ttl time.Duration) IError {
	if len(values) == 0 {
		return nil
	}
	pipe := c.rdb.Pipeline()
	for key, value := range values {
		raw, err := encodeCacheValue(value)
		if err != nil {
			return err
		}
		pipe.Set(c.ctx, c.k(key), raw, ttl)
	}
	if _, err := pipe.Exec(c.ctx); err != nil {
		return c.fail("mset", "", err)
	}
	return nil
}

func (c *cache) Del(keys ...string) IError {
	if len(keys) == 0 {
		return nil
	}
	// One pipeline entry per key: DEL with several keys spans slots on a cluster.
	pipe := c.rdb.Pipeline()
	for _, key := range keys {
		pipe.Del(c.ctx, c.k(key))
	}
	if _, err := pipe.Exec(c.ctx); err != nil {
		return c.fail("del", "", err)
	}
	return nil
}

// scanBatch is how many keys one SCAN round asks for. Small enough not to block
// the server, large enough not to spend a round trip per key.
const scanBatch = 500

func (c *cache) DelByPrefix(prefix string) (int64, IError) {
	pattern := c.k(prefix) + "*"
	var deleted int64

	del := func(ctx context.Context, client redis.UniversalClient) error {
		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, pattern, scanBatch).Result()
			if err != nil {
				return err
			}
			for _, key := range keys {
				n, err := client.Del(ctx, key).Result()
				if err != nil && !errors.Is(err, redis.Nil) {
					return err
				}
				deleted += n
			}
			cursor = next
			if cursor == 0 {
				return nil
			}
		}
	}

	// A cluster keeps the keyspace on many masters, and a scan of one of them
	// sees only its own shard.
	if cluster, ok := c.rdb.(*redis.ClusterClient); ok {
		err := cluster.ForEachMaster(c.ctx, func(ctx context.Context, node *redis.Client) error {
			return del(ctx, node)
		})
		if err != nil {
			return deleted, c.fail("delbyprefix", prefix, err)
		}
		return deleted, nil
	}
	if err := del(c.ctx, c.rdb); err != nil {
		return deleted, c.fail("delbyprefix", prefix, err)
	}
	return deleted, nil
}

func (c *cache) Exists(key string) (bool, IError) {
	n, err := c.rdb.Exists(c.ctx, c.k(key)).Result()
	if err != nil {
		return false, c.fail("exists", key, err)
	}
	return n > 0, nil
}

func (c *cache) Expire(key string, ttl time.Duration) (bool, IError) {
	ok, err := c.rdb.Expire(c.ctx, c.k(key), ttl).Result()
	if err != nil {
		return false, c.fail("expire", key, err)
	}
	return ok, nil
}

func (c *cache) TTL(key string) (time.Duration, IError) {
	ttl, err := c.rdb.TTL(c.ctx, c.k(key)).Result()
	if err != nil {
		return 0, c.fail("ttl", key, err)
	}
	// redis answers -2 for a key that is gone and -1 for one that never expires;
	// the driver hands both back verbatim, as nanoseconds.
	switch ttl {
	case -2:
		return 0, cacheMiss(key)
	case -1:
		return TTLNoExpiry, nil
	}
	return ttl, nil
}

// ---------------------------------------------------------------------------
// Counters
// ---------------------------------------------------------------------------

// incrScript adds delta and, only when it created the counter, gives it a ttl.
// Doing it in one script is what makes a fixed-window limiter correct: an INCR
// followed by a separate EXPIRE can lose the expiry to a crash in between and
// leave a counter that never resets.
var incrScript = redis.NewScript(`
local v = redis.call('INCRBY', KEYS[1], ARGV[1])
if tonumber(ARGV[2]) > 0 and redis.call('TTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return v
`)

func (c *cache) Incr(key string, delta int64, ttl time.Duration) (int64, IError) {
	n, err := incrScript.Run(c.ctx, c.rdb, []string{c.k(key)}, delta, ttl.Milliseconds()).Int64()
	if err != nil {
		return 0, c.fail("incr", key, err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Encoding
// ---------------------------------------------------------------------------

// cacheMiss is the error a read returns for an absent key. It is built without a
// stack trace on purpose: a miss is an ordinary outcome on a hot path, not a
// failure worth unwinding 32 frames for.
func cacheMiss(key string) *Error {
	return &Error{
		Status:  404,
		Code:    "CACHE_MISS",
		Message: "cache: miss",
		cause:   fmt.Errorf("%w: %s", ErrCacheMiss, key),
	}
}

// encodeCacheValue renders a value for storage: strings and bytes go in as they
// are (so a token read back with *string is the token), everything else is JSON.
func encodeCacheValue(value any) ([]byte, IError) {
	switch v := value.(type) {
	case nil:
		return []byte("null"), nil
	case string:
		return []byte(v), nil
	case []byte:
		return v, nil
	case json.RawMessage:
		return v, nil
	default:
		// everything else goes through JSON — including types with their own
		// text form (time.Time): storing the text form would write something
		// json.Unmarshal cannot read back.
		b, err := json.Marshal(value)
		if err != nil {
			return nil, Wrap(err, "cache: marshal")
		}
		return b, nil
	}
}

// decodeCacheValue is the inverse of encodeCacheValue.
func decodeCacheValue(raw []byte, dest any) IError {
	switch d := dest.(type) {
	case nil:
		return New(500, "CACHE_DECODE", "cache: nil destination")
	case *string:
		*d = string(raw)
		return nil
	case *[]byte:
		*d = raw
		return nil
	case *json.RawMessage:
		*d = append((*d)[:0], raw...)
		return nil
	default:
		if err := json.Unmarshal(raw, dest); err != nil {
			return Wrap(err, "cache: unmarshal")
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// Disabled cache
// ---------------------------------------------------------------------------

// noopCache is what ctx.Cache() returns when no cache is configured. Every read
// misses and every write is dropped, so cache-aside code keeps working — and
// keeps being correct, since a miss always ends in the loader being called.
type noopCache struct{ prefix string }

var _ ICache = noopCache{}

// NewNoopCache returns a cache that stores nothing. It is what a service with no
// CACHE_* configuration gets, so the call path is identical in every
// environment.
func NewNoopCache() ICache { return noopCache{} }

func (n noopCache) Get(key string, _ any) IError                    { return cacheMiss(key) }
func (n noopCache) Set(string, any, time.Duration) IError           { return nil }
func (n noopCache) SetNX(string, any, time.Duration) (bool, IError) { return true, nil }
func (n noopCache) GetDel(key string, _ any) IError                 { return cacheMiss(key) }
func (n noopCache) MGet(...string) (map[string][]byte, IError)      { return map[string][]byte{}, nil }
func (n noopCache) MSet(map[string]any, time.Duration) IError       { return nil }
func (n noopCache) Del(...string) IError                            { return nil }
func (n noopCache) DelByPrefix(string) (int64, IError)              { return 0, nil }
func (n noopCache) Exists(string) (bool, IError)                    { return false, nil }
func (n noopCache) Expire(string, time.Duration) (bool, IError)     { return false, nil }
func (n noopCache) TTL(key string) (time.Duration, IError)          { return 0, cacheMiss(key) }

// Incr answers zero, which means a rate limiter built on it never trips. That
// is the only honest answer — without a shared counter there is nothing to
// count — but it is worth knowing that a limit is not enforced in an
// environment with no cache.
func (n noopCache) Incr(string, int64, time.Duration) (int64, IError) {
	return 0, nil
}

// Lock is granted: without a shared cache there is nothing to coordinate with,
// and refusing would stop a single-instance deployment from running the work at
// all. It is the same trade-off as a read that misses.
func (n noopCache) Lock(key string, _ time.Duration) (ILock, IError) {
	return noopLock{key: key}, nil
}

func (n noopCache) PubSub() IPubSub { return noopPubSub{} }
func (n noopCache) Ping() IError    { return nil }
func (n noopCache) Enabled() bool   { return false }
func (n noopCache) Prefix() string  { return n.prefix }
func (n noopCache) WithPrefix(p string) ICache {
	return noopCache{prefix: n.prefix + normalizePrefix(p)}
}
func (n noopCache) WithContext(context.Context) ICache { return n }
func (n noopCache) Redis() redis.UniversalClient       { return nil }
func (n noopCache) Close() IError                      { return nil }

// orNoop keeps the package-level helpers safe against a nil ICache — a handle
// from a context built by hand, or a struct field nobody filled in.
func orNoop(c ICache) ICache {
	if c == nil {
		return noopCache{}
	}
	return c
}

// ---------------------------------------------------------------------------
// Typed helpers
// ---------------------------------------------------------------------------

// GetJSON reads and decodes a value into T. The error wraps ErrCacheMiss on absence.
func GetJSON[T any](c ICache, key string) (T, IError) {
	var out T
	err := orNoop(c).Get(key, &out)
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// SetJSON stores v as JSON under key.
func SetJSON[T any](c ICache, key string, v T, ttl time.Duration) IError {
	return orNoop(c).Set(key, v, ttl)
}

// GetJSONMany reads many keys in one round trip and decodes each into T. Keys
// that are absent — or hold something that is not a T — are left out of the
// result rather than failing the batch, which is what a cache read should do.
func GetJSONMany[T any](c ICache, keys ...string) (map[string]T, IError) {
	raw, err := orNoop(c).MGet(keys...)
	if err != nil {
		return nil, err
	}
	out := make(map[string]T, len(raw))
	for key, b := range raw {
		var v T
		if decodeCacheValue(b, &v) == nil {
			out[key] = v
		}
	}
	return out, nil
}

// Remember returns the cached value for key, or computes it with load, caches it
// (best-effort) and returns it.
//
// A cache that is down, disabled or holding a value written by an older version
// of the struct is not an error: load runs and the result is served. Only load
// failing fails the call.
func Remember[T any](c ICache, key string, ttl time.Duration, load func() (T, error)) (T, IError) {
	cc := orNoop(c)
	if v, err := GetJSON[T](cc, key); err == nil {
		return v, nil
	}
	v, err := load()
	if err != nil {
		var zero T
		return zero, Wrap(err, "cache: load")
	}
	_ = SetJSON(cc, key, v, ttl)
	return v, nil
}

// RememberOnce is Remember with a lock around the loader, so a key that expires
// under load is recomputed by one caller instead of by all of them. The others
// wait up to wait for the winner to publish, then fall back to loading it
// themselves rather than failing.
//
// Use it when load is expensive enough that a stampede would hurt; Remember is
// cheaper and right for everything else.
func RememberOnce[T any](c ICache, key string, ttl, wait time.Duration, load func() (T, error)) (T, IError) {
	cc := orNoop(c)
	if v, err := GetJSON[T](cc, key); err == nil {
		return v, nil
	}

	lock, lockErr := LockWait(cc, "lock:"+key, lockTTLFor(wait), wait)
	if lockErr != nil {
		// Somebody else is loading and did not finish in time: do the work
		// ourselves rather than making the caller wait any longer.
		return Remember(cc, key, ttl, load)
	}
	defer func() { _ = lock.Unlock() }()

	// The winner may have published while we waited for the lock.
	if v, err := GetJSON[T](cc, key); err == nil {
		return v, nil
	}
	v, err := load()
	if err != nil {
		var zero T
		return zero, Wrap(err, "cache: load")
	}
	_ = SetJSON(cc, key, v, ttl)
	return v, nil
}

// lockTTLFor bounds how long a crashed loader can block the others: long enough
// to cover the wait, never unbounded.
func lockTTLFor(wait time.Duration) time.Duration {
	if wait <= 0 {
		return 10 * time.Second
	}
	return wait + 10*time.Second
}

// Forget removes keys, ignoring a cache that is unavailable. It is the
// invalidation half of Remember, for use in a write path that must not fail
// because the cache is down.
func Forget(c ICache, keys ...string) {
	_ = orNoop(c).Del(keys...)
}
