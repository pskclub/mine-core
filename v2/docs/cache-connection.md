# Connection & backends

```go
redis, err := core.NewCache(env)          // uses CACHE_* / CACHE_CONNECTION_STRING
if err != nil {
    panic(err)
}
app, _ := core.NewApp(env, core.WithCache("default", redis))
defer app.Shutdown(context.Background())
```

`NewCache` PINGs before returning, so a misconfigured cache fails at boot rather
than on the first request that touches it.

## Three backends

| Constructor | What it is | Use it for |
|---|---|---|
| `core.NewCache(env)` | redis (standalone, cluster or sentinel) | anything with more than one replica |
| `core.NewMemoryCache()` | in-process | dev, tests, single-instance tools |
| `core.NewNoopCache()` | stores nothing | what an unconfigured service gets |

The memory cache is a **real** cache — same encoding, real expiry, atomic
counters, working locks and pub/sub — but it cannot span processes. Two replicas
each get their own, so it is the wrong choice for anything that coordinates
across instances: a rate limit that allows *N* per replica, a lock that locks
nothing, an invalidation message half the fleet never hears.

```go
c := core.NewMemoryCache()
defer c.Close()
app, _ := core.NewApp(env, core.WithCache("default", c))
```

## Deployment shapes

The shape is configuration, not code — the same `ICache` comes back from all
three:

**Standalone**

```sh
CACHE_HOST=redis
CACHE_PORT=6379
CACHE_PASSWORD=secret
CACHE_DB=0
```

**Cluster** — several addresses, no master name:

```sh
CACHE_ADDRS=redis-0:6379,redis-1:6379,redis-2:6379
```

**Sentinel** — addresses *are* the sentinels once a master name is set:

```sh
CACHE_ADDRS=sentinel-0:26379,sentinel-1:26379,sentinel-2:26379
CACHE_MASTER_NAME=mymaster
CACHE_SENTINEL_PASSWORD=…
```

**A URI**, which wins over the discrete fields:

```sh
CACHE_CONNECTION_STRING=redis://:pass@host:6379/0
CACHE_CONNECTION_STRING=rediss://:pass@host:6380/0     # TLS
```

> In a **cluster**, keys touched by one command must live on one slot. `MGet`,
> `MSet` and `DelByPrefix` across arbitrary keys are the ones to watch — the
> client handles the routing, but a command spanning slots is several round
> trips, not one.

## TLS

```sh
CACHE_TLS=true                 # with discrete fields
CACHE_TLS_SKIP_VERIFY=false    # self-signed certificates only
```

A URI uses `rediss://` instead. `CACHE_TLS_SKIP_VERIFY=true` disables certificate
verification — it is for a development instance with a self-signed certificate,
and turning it on in production removes the only thing TLS was protecting against.

## Pool and timeouts

```sh
CACHE_POOL_SIZE=50
CACHE_MIN_IDLE_CONNS=5
CACHE_DIAL_TIMEOUT=5     # seconds; also bounds the PING at boot
CACHE_READ_TIMEOUT=3     # seconds
CACHE_WRITE_TIMEOUT=3    # seconds
CACHE_MAX_RETRIES=3      # -1 disables retrying
```

`CACHE_READ_TIMEOUT` is the important one. Without it, a redis that has stopped
answering — but not closed the connection — holds every request that touches the
cache until something else gives up. A cache is supposed to make requests faster;
a cache with no read timeout can make every request in the service slower than it
would be with no cache at all.

Every key and its default is in [Configuration → Cache](./env.md#cache-redis).

## Namespacing

`CACHE_PREFIX` namespaces every key **and every channel**. Set it per service
*and* per environment when instances share a redis, so a staging deploy cannot
read — or invalidate — production's keys:

```sh
CACHE_PREFIX=orders-staging
```

`WithPrefix` narrows further, which is how a per-tenant or per-feature namespace
stays one:

```go
otp := ctx.Cache().WithPrefix("otp")
otp.Set("0812345678", code, 5*time.Minute)   // "<CACHE_PREFIX>otp:0812345678"

n, _ := otp.DelByPrefix("")                  // clears only the otp namespace
```

A `:` is appended when the prefix does not already end in one. `Prefix()` returns
what is being applied, and `Redis()` does **not** apply it — build keys with
`Prefix()` when you drop to the raw client:

```go
rdb := ctx.Cache().Redis()
rdb.XAdd(ctx, &redis.XAddArgs{Stream: ctx.Cache().Prefix() + "events", …})
```

## Named connections

```go
app, _ := core.NewApp(env,
    core.WithCache("default", sessions),
    core.WithCache("events", eventsRedis),
)
```

```go
ctx.Cache()                // "default"
ctx.Caches("events")       // a named one
app.NewSubscriber(core.WithSubscriberCache("events"))
```

Worth doing when one instance holds small hot keys and another carries pub/sub
traffic — a slow consumer on the second cannot then stall the first.

## Health checks

```go
if err := ctx.Cache().Ping(); err != nil {
    // readiness, not liveness
}
```

Think twice before failing readiness on the cache. If the service degrades
correctly without it — which is what the disabled cache is designed for — taking
the pod out of rotation converts a slow cache into an outage.

## Custom context

Cache calls are bound to the request, so a cancelled request abandons them. When
work must outlive the request, bind another context:

```go
bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()
core.GetJSON[User](ctx.Cache().WithContext(bg), "user:1")
```

## Shutdown

`app.Shutdown(ctx)` closes the caches it was given, after stopping the
[subscribers](./pubsub-subscriber.md) that read from them. Handing a cache to
`core.WithCache` transfers that responsibility — do not also `Close()` it.
