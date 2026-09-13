# Cache

`core.ICache` — a Redis wrapper. `ctx.Cache()` returns a handle bound to the
request context, so methods take no `ctx`, and the JSON helpers are generic and
type-safe.

```go
c := ctx.Cache()
c.Set("user:1", user, time.Minute)

var u User
err := c.Get("user:1", &u)
```

## `ctx.Cache()` is never nil

A service with no `CACHE_*` configuration gets a **disabled** cache: every read
misses, every write is dropped. Cache-aside code therefore runs unchanged in an
environment with no redis instead of panicking.

```go
// works identically with redis, with the memory cache, and with nothing at all
user, err := core.Remember(ctx.Cache(), "user:"+id, time.Minute, func() (User, error) {
    return loadUser(ctx, id)
})
```

That choice is not free, and the places where it changes an answer are worth
knowing before you rely on one:

| On a disabled cache | What happens | Why |
|---|---|---|
| `Get` | misses | there is nothing stored |
| `Set` | dropped, no error | a cache write that fails is not a request that failed |
| `Incr` | returns 0 | without a shared counter there is nothing to count — **so a rate limit built on it never trips** |
| `Lock` | granted | refusing would stop a single-instance deployment doing the work at all |
| `Subscribe` | **error** (`PUBSUB_DISABLED`) | a subscriber that silently receives nothing forever is a service that looks healthy while doing none of its work |

Call `Enabled()` when a path genuinely needs a real one:

```go
if !ctx.Cache().Enabled() {
    return ctx.NewError(nil, errmsgs.CacheError)   // this endpoint cannot work without it
}
```

## What this section covers

| Page | |
|---|---|
| [Connection & backends](./cache-connection.md) | redis, cluster, sentinel, TLS, the memory backend, prefixes |
| [Values & keys](./cache-operations.md) | get/set, encoding, batches, TTLs, invalidation |
| [Counters & rate limits](./cache-counters.md) | `Incr` and the fixed-window limiter |
| [Locks](./cache-locks.md) | mutual exclusion that survives a crashed holder |
| [Cache-aside patterns](./cache-patterns.md) | `Remember`, stampedes, invalidation strategy |
| [Testing](./cache-testing.md) | the memory backend, and what it does and does not prove |
| [Pub/Sub](./pubsub.md) | the publish/subscribe side of the same connection |

## What a cache is for

A cache is a **copy** of something you can recompute. That is the whole contract,
and every rule below follows from it:

- Losing the cache must never lose data. If the only copy of something is in
  redis, it is not a cache — it is a database with no durability guarantee.
- A cache miss is an ordinary outcome, not an error. Code that treats one as a
  failure turns a redis restart into an outage.
- Every key expires. A key with no TTL and no owner is a leak that shows up
  months later as a memory alert.

```go
err := c.Get("user:1", &u)
if errors.Is(err, core.ErrCacheMiss) {
    // absent — recompute, do not fail
}
```

## Naming keys

Keys are a flat namespace shared by everything on the instance. Give them a
structure and stick to it:

```
user:42                    the entity
user:42:permissions        something derived from it
session:01HX…              a different kind of thing
rate:login:203.0.113.5     a counter, with what it counts in the name
lock:settle:42             a lock
```

Two habits that pay for themselves: put the **type** first so `DelByPrefix`
works on a whole class of key, and put the **version** in the key when the shape
of the value can change (`user:v2:42`), so a deploy that changes a struct does
not have to guess whether the old values are still readable.

`CACHE_PREFIX` namespaces everything on top of that — see
[Namespacing](./cache-connection.md#namespacing).

## The interface

```go
type ICache interface {
    Get(key string, dest any) IError
    Set(key string, value any, ttl time.Duration) IError
    SetNX(key string, value any, ttl time.Duration) (bool, IError)
    GetDel(key string, dest any) IError
    MGet(keys ...string) (map[string][]byte, IError)
    MSet(values map[string]any, ttl time.Duration) IError
    Del(keys ...string) IError
    DelByPrefix(prefix string) (int64, IError)
    Exists(key string) (bool, IError)
    Expire(key string, ttl time.Duration) (bool, IError)
    TTL(key string) (time.Duration, IError)
    Incr(key string, delta int64, ttl time.Duration) (int64, IError)
    Lock(key string, ttl time.Duration) (ILock, IError)
    PubSub() IPubSub
    Ping() IError                           // readiness probe
    Enabled() bool
    Prefix() string
    WithPrefix(prefix string) ICache
    WithContext(ctx context.Context) ICache // escape hatch
    Redis() redis.UniversalClient           // raw client; nil for memory/disabled
    Close() IError
}
```

`Redis()` does not apply the prefix — build keys with `Prefix()` when you use it.
