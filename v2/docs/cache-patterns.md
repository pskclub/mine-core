# Cache-aside patterns

Cache-aside is the pattern behind almost every cache: look in the cache, and on a
miss compute the value and store it. `Remember` is that, in one call.

```go
user, err := core.Remember(ctx.Cache(), "user:"+id, time.Minute, func() (User, error) {
    return loadUser(ctx, id)
})
```

## What Remember guarantees

A cache that is **down**, **disabled**, or holding a value written by an **older
version of the struct** is not an error: the loader runs and the result is
served. Only the loader failing fails the call.

That is the property that makes it safe to sprinkle around. Adding a cache cannot
introduce a new way for the request to fail — the worst case is the speed you had
before.

```go
// with redis:     one GET
// without redis:  one loadUser, every time
// with a stale-shaped value: one loadUser, and the key is overwritten
```

## Stampedes

When a hot key expires, every request that wanted it computes the same value at
the same moment. If the loader is a 200ms query, a thousand concurrent requests
become a thousand concurrent 200ms queries, and the database that was fine a
second ago is not.

`RememberOnce` puts a lock around the loader: one caller computes, the rest wait
for it to publish, and fall back to loading themselves rather than failing.

```go
report, err := core.RememberOnce(ctx.Cache(), "report:"+id,
    time.Hour,          // how long the result is cached
    3*time.Second,      // how long to wait for the winner
    func() (Report, error) { return buildReport(ctx, id) })
```

| Use | When |
|---|---|
| `Remember` | the loader is cheap, or the key is not hot |
| `RememberOnce` | the loader is expensive **and** many callers want the same key at once |

The wait should be a little longer than the loader's p99. Too short and everybody
falls through to computing it anyway; too long and a slow loader holds a thousand
requests instead of failing them.

### Jittering the TTL

A thousand keys written in the same second expire in the same second. Spread
them:

```go
ttl := 5*time.Minute + time.Duration(rand.Int63n(int64(30*time.Second)))
core.Remember(c, key, ttl, load)
```

Cheap, and it turns a periodic cliff into a flat line.

## Invalidation

Two strategies, and mixing them badly is where stale data comes from.

**Expire and forget.** Give the key a short TTL and let it go stale for that
long. Simplest, and correct for anything where "up to a minute old" is fine.

**Write-through invalidation.** Delete the key whenever the underlying data
changes:

```go
func UpdateUser(ctx core.IContext, u *User) core.IError {
    if err := repository.New[User](ctx).Where("id = ?", u.ID).Updates(u); err != nil {
        return err
    }
    core.Forget(ctx.Cache(), "user:"+u.ID, "user:"+u.ID+":permissions")
    return nil
}
```

Order matters: **write first, then invalidate.** Invalidating before the write
lets a concurrent read repopulate the cache with the old value, and the cache is
then wrong until the TTL saves it.

Both derived keys are listed there on purpose. The thing that actually breaks
cache invalidation is a value derived from another value, cached under a key
nobody remembers to delete. Two habits help:

```go
// 1. put derived keys under a prefix, and clear the prefix
core.Forget(c, "user:"+id)
c.DelByPrefix("user:" + id + ":")

// 2. or version the entity, and never delete anything
v, _ := c.Incr("ver:user:"+id, 0, core.NoExpiry)
key := fmt.Sprintf("user:%s:v%d:profile", id, v)
// bumping ver:user:<id> makes every derived key unreachable at once
```

The second is worth the trouble for entities with many derived views: there is no
list of keys to keep in sync, and no window where half of them are invalidated.

## Invalidating across replicas

Deleting a key in redis invalidates it for everyone, because there is one redis.
Invalidating something held **in a process** — a config struct, a feature flag
map — needs a message, which is what [pub/sub](./pubsub.md) is for:

```go
// the writer
core.Forget(ctx.Cache(), "config")
ctx.PubSub().Publish("config.changed", cfg.Version)

// every replica
sub.On("config.changed", func(ctx core.IContext, msg *core.PubSubMessage) error {
    return reloadConfig(ctx)
})
```

## What to cache

| Good | Bad |
|---|---|
| expensive reads with many more reads than writes | anything written more often than it is read |
| results of an external API call | data that must be exactly current |
| computed aggregates and reports | large blobs — [storage](./storage.md) is for those |
| session and token lookups | the only copy of anything |

The measurement worth doing before adding a cache: how long does the uncached
path actually take, and how often is it called? A cache in front of a 2ms query
called twice a minute adds a class of bug and saves nothing.

## Negative caching

A key that is looked up constantly and does not exist hits the database every
time. Cache the absence too — briefly:

```go
type cachedUser struct {
    User  *User `json:"user"`
    Found bool  `json:"found"`
}

res, err := core.Remember(c, "user:"+id, 30*time.Second, func() (cachedUser, error) {
    u, err := repository.New[User](ctx).FindOne("id = ?", id)
    if errors.Is(err, errmsgs.NotFound) {
        return cachedUser{Found: false}, nil
    }
    if err != nil {
        return cachedUser{}, err
    }
    return cachedUser{User: u, Found: true}, nil
})
```

Give the negative entry a much shorter TTL than the positive one. A user who
signs up should not be told they do not exist for the next five minutes.

## Warming

A cache that is empty after every deploy hands the first minute of traffic
straight to the database. When that matters, populate the hot keys at startup or
on a [schedule](./scheduler.md):

```go
sc.AddByCron("warm-cache", "*/5 * * * *", func(c core.ICronjobContext) error {
    for _, id := range hotTenants {
        if _, err := core.Remember(c.Cache(), "tenant:"+id, 10*time.Minute, load(c, id)); err != nil {
            return err
        }
    }
    return nil
})
```

Warming is a workaround for a loader that is too slow to be hit cold. If the
uncached path cannot serve traffic at all, the cache is not a cache any more —
it is a dependency, and it should be one you can lose.

## Best practices

- **cache คือ optimization ไม่ใช่แหล่งความจริง** — ทุกเส้นทางต้องทำงานได้เมื่อ cache miss
  หรือเมื่อไม่มี redis เลย (มันถูกออกแบบให้ degrade เงียบ)
- **key ต้องมีทุกอย่างที่ทำให้ค่าต่างกัน** โดยเฉพาะ **id ของผู้ใช้เมื่อค่าขึ้นกับสิทธิ์** —
  คำตอบของคนอื่นโผล่ข้ามบัญชีคือ incident ไม่ใช่ bug
- **ใส่เวอร์ชันไว้ใน key** (`user:v2:<id>`) เพื่อให้เปลี่ยนรูปแบบค่าได้โดยไม่ต้องไล่ลบ
- **TTL สั้นกว่าที่คิดไว้หนึ่งขั้นเสมอ** และ invalidate ตอนเขียนด้วย — พึ่ง TTL อย่างเดียว
  แปลว่าข้อมูลเก่าค้างได้นานเท่า TTL
- **`Remember` แทนการเขียน get-miss-set เอง** — มันกัน stampede ให้ด้วย
- **negative caching สำหรับ id ที่ไม่มีจริงแต่ถูกยิงบ่อย** ด้วย TTL สั้นมาก
- **อย่า cache สิ่งที่คำนวณเร็วกว่าการอ่าน cache** — round trip ก็มีราคา
- **อย่าเก็บสิ่งที่หายไม่ได้ไว้ใน cache** (session ที่ไม่มีที่อื่น, counter ที่ต้องแม่น) —
  redis restart แล้วมันหายจริง
