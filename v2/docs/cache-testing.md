# Testing

Tests need no redis. `core.NewMemoryCache()` is a real cache in the same process
— same encoding, real expiry, atomic counters, working locks and pub/sub — so the
whole cache path is exercised for real rather than stubbed out.

```go
func TestCachesTheUser(t *testing.T) {
    c := core.NewMemoryCache()
    defer c.Close()

    app, err := core.NewApp(env, core.WithCache("default", c))
    require.NoError(t, err)

    ctx := app.NewContext(context.Background())

    _, err = loadUserCached(ctx, "1")     // populates
    require.NoError(t, err)

    var got User
    require.NoError(t, c.Get("user:1", &got))
    require.Equal(t, "ann", got.Name)
}
```

## Which backend for which test

| Backend | Use it for |
|---|---|
| `NewMemoryCache()` | almost every test: fast, hermetic, no container |
| `NewNoopCache()` | asserting the code still works with no cache at all |
| real redis (`make test-integration`) | the cluster/sentinel path, and anything that has to be believed |

The disabled cache is worth an explicit test on any path that has to survive
without redis — it is the environment a developer's laptop and a minimal
deployment both run in:

```go
func TestWorksWithoutCache(t *testing.T) {
    app, _ := core.NewApp(env, core.WithCache("default", core.NewNoopCache()))
    ctx := app.NewContext(context.Background())

    u, err := loadUserCached(ctx, "1")    // loader runs every time
    require.NoError(t, err)
    require.Equal(t, "ann", u.Name)
}
```

## What the memory cache does not prove

It is one process. Anything whose *point* is coordination between processes is
not being tested:

| Passes in memory, can still be wrong | Because |
|---|---|
| a distributed lock | there is only one holder to begin with |
| a rate limit | one counter, one process — the fleet has *N* |
| pub/sub fan-out to other replicas | every subscriber is in this process |
| a cluster-routed `MGet` | no slots, no cross-slot commands |
| `Redis()` | it returns **nil** on the memory backend |

A test that calls `Redis()` needs the real thing. So does anything asserting on
redis-specific behaviour — eviction policy, memory limits, `SCAN` cursor
semantics under concurrent writes.

## Asserting on what was cached

The memory cache is inspectable like any other, which is the easiest way to
assert a caching path did what it claimed:

```go
// it was stored
var u User
require.NoError(t, c.Get("user:1", &u))

// with the TTL you expect
ttl, err := c.TTL("user:1")
require.NoError(t, err)
require.InDelta(t, time.Minute.Seconds(), ttl.Seconds(), 2)

// and invalidation removed it
require.NoError(t, updateUser(ctx, &u))
require.ErrorIs(t, c.Get("user:1", &u), core.ErrCacheMiss)
```

## Counting loader calls

The assertion that actually matters for a cache is "the expensive thing ran
once", and it needs no cache internals at all:

```go
var calls int32
load := func() (User, error) {
    atomic.AddInt32(&calls, 1)
    return User{Name: "ann"}, nil
}

_, _ = core.Remember(c, "user:1", time.Minute, load)
_, _ = core.Remember(c, "user:1", time.Minute, load)

require.Equal(t, int32(1), atomic.LoadInt32(&calls))
```

The same test against `NewNoopCache()` should see **2**. Running it both ways is
what pins down that the caching is real and that the code survives without it.

## Expiry

The memory cache expires for real, so a short TTL is testable without waiting:

```go
require.NoError(t, c.Set("k", "v", 50*time.Millisecond))
time.Sleep(80 * time.Millisecond)
require.ErrorIs(t, c.Get("k", new(string)), core.ErrCacheMiss)
```

Keep these rare and keep the durations small. A suite that sleeps its way through
TTL assertions gets slow, and sleeping tests are the first ones to go flaky on a
loaded CI runner.

## Pub/Sub

The memory cache carries an in-process broker, so subscriber tests need no redis
either — see [Pub/Sub → testing](./pubsub-patterns.md#testing).

```go
c := core.NewMemoryCache()
app, _ := core.NewApp(env, core.WithCache("default", c))

sub := app.NewSubscriber()
sub.On("user.updated", handler)
require.NoError(t, sub.Start())

require.NoError(t, c.PubSub().Publish("user.updated", user))
```

## Integration tests

The real driver runs against a real redis under the `integration` tag:

```sh
docker run -p 6379:6379 redis
APP_CACHE_HOST=127.0.0.1 make test-integration
```

See [Integration tests](./testing-integration.md) and the header of
`cache_integration_test.go`.
