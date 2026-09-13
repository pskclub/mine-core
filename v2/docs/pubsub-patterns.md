# Recipes & testing

Four things pub/sub is genuinely good at, and how to test any of them without
redis.

## Cache invalidation across replicas

The canonical use. Deleting a redis key already invalidates it everywhere,
because there is one redis — this is for state held **in a process**.

```go
// the writer
func UpdateSettings(ctx core.IContext, s *Settings) core.IError {
    if err := repository.New[Settings](ctx).Save(s); err != nil {
        return err
    }
    ctx.PubSub().Publish("settings.changed", s.TenantID)
    return nil
}

// every replica
sub.On("settings.changed", func(ctx core.IContext, msg *core.PubSubMessage) error {
    core.Forget(ctx.Cache(), "settings:"+msg.String())
    localCache.Delete(msg.String())     // the in-process copy
    return nil
})
```

A replica that misses the message keeps its stale copy until the TTL expires,
which is why the in-process copy still needs one. Pub/sub makes invalidation
*fast*; the TTL is what makes it *correct*.

## Feature flags and config reloads

```go
sub.On("config.reload", func(ctx core.IContext, msg *core.PubSubMessage) error {
    cfg, err := loadConfig(ctx)
    if err != nil {
        return err          // logged; the old config stays in place
    }
    store.Swap(cfg)
    ctx.Log().Info("config reloaded", "version", cfg.Version)
    return nil
})
```

Keep the old value on failure. A reload that fails and leaves the service with no
config is worse than one that leaves it with yesterday's.

Reload on a timer as well as on the message. The timer is what makes a replica
that was restarting during the publish eventually correct.

## Live updates to websockets

```go
// anywhere in the service
ctx.PubSub().Publish("notify."+userID, Notification{Text: "Your export is ready"})
```

The hub subscribes once per process and fans out in memory — see
[Raw subscriptions](./pubsub-subscriptions.md#a-websocket-hub). The channel per
user is what lets a replica ignore messages for users it is not holding
connections for.

## Presence and counters

```go
// on connect
ctx.Cache().Incr("presence:"+roomID, 1, core.NoExpiry)
ctx.PubSub().Publish("room."+roomID+".presence", "joined")

// on disconnect
ctx.Cache().Incr("presence:"+roomID, -1, core.NoExpiry)
ctx.PubSub().Publish("room."+roomID+".presence", "left")
```

The count lives in the [cache](./cache-counters.md) — a durable-enough number
anybody can read. The message is only the nudge that tells a client to look
again. Trying to keep an accurate count from the messages alone fails the first
time a replica restarts.

## What does not belong here

| Instead of pub/sub | Use |
|---|---|
| work that must happen exactly once | [jobs](./jobs.md) |
| work another service must acknowledge | [MQ](./mq.md) |
| a request/response across services | HTTP, via [Requester](./requester.md) |
| an event stream with history | a database table, or redis streams via `Redis()` |

The test: if you would be paged when a message is lost, it should not have been a
pub/sub message.

## Testing

The memory cache carries an in-process broker, so pub/sub tests need no redis:

```go
c := core.NewMemoryCache()
app, _ := core.NewApp(env, core.WithCache("default", c))

sub := app.NewSubscriber()
sub.On("user.updated", handler)
require.NoError(t, sub.Start())

require.NoError(t, c.PubSub().Publish("user.updated", user))
```

### Waiting for the handler

Delivery is asynchronous, so the assertion has to wait for it. Use a channel, not
a sleep:

```go
func TestCachesTheUpdatedUser(t *testing.T) {
    c := core.NewMemoryCache()
    defer c.Close()
    app, _ := core.NewApp(env, core.WithCache("default", c))

    done := make(chan struct{})
    sub := app.NewSubscriber()
    sub.On("user.updated", func(ctx core.IContext, msg *core.PubSubMessage) error {
        defer close(done)
        u, err := core.BindMessage[User](msg)
        if err != nil {
            return err
        }
        return core.SetJSON(ctx.Cache(), "user:"+u.ID, u, time.Minute)
    })
    require.NoError(t, sub.Start())
    t.Cleanup(func() { _ = sub.Stop(context.Background()) })

    require.NoError(t, c.PubSub().Publish("user.updated", User{ID: "1", Name: "ann"}))

    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("handler was never called")
    }

    got, err := core.GetJSON[User](c, "user:1")
    require.NoError(t, err)
    require.Equal(t, "ann", got.Name)
}
```

`time.Sleep(100*time.Millisecond)` passes locally and goes flaky on a loaded CI
runner. A channel with a generous timeout fails fast when it is broken and never
fails when it is not.

### Publish before Start receives nothing

There is no buffering and no replay: a message published before `Start` is gone.
Order the test the way the system works — subscribe, then publish.

### Testing the handler alone

A handler is an ordinary function of `(IContext, *PubSubMessage)`. The cheapest
test calls it directly, with no subscriber and no cache at all:

```go
func TestHandleUserUpdated(t *testing.T) {
    ctx := app.NewContext(context.Background())
    msg := &core.PubSubMessage{
        Channel: "user.updated",
        Payload: []byte(`{"id":"1","name":"ann"}`),
    }
    require.NoError(t, handleUserUpdated(ctx, msg))
}
```

Do that for the logic, and keep one wired test for the subscription itself. See
[Unit tests](./testing-unit.md) and [Mocks & fakes](./testing-mock.md).

### What the memory broker does not prove

It is one process. Fan-out to *other replicas*, the disconnect of a slow
subscriber, and reconnection after a redis restart are all real-redis behaviour —
`make test-integration` with a real instance is where those get exercised.

## Best practices

- **pub/sub ไม่ใช่คิว** — ไม่มี ack ไม่มี retry ไม่มีการเก็บ ถ้าการหายของ message
  เป็นบั๊ก ให้ใช้ [job](./jobs.md) หรือ [MQ](./mq.md)
- **payload เป็น id ไม่ใช่ก้อนข้อมูล** — subscriber อ่านสถานะปัจจุบันเอง จึงทนต่อการ
  ประมวลผลสลับลำดับและไม่พาข้อมูลอ่อนไหวเดินทาง
- **เขียนลง database ให้เสร็จก่อน publish** — ไม่งั้น subscriber ไปหาแล้วไม่เจอ
- **subscriber ต้องทนทั้ง message ซ้ำและ message ที่หายไป** — ทำให้ handler เป็น
  "sync สถานะให้ตรง" ไม่ใช่ "เพิ่มทีละหนึ่ง"
- **ตั้งชื่อ channel เป็นอดีตกาล** (`user.updated`) สำหรับ event และเป็นคำสั่ง
  (`cache.flush`) สำหรับคำสั่ง — channel ที่ปนกันทำให้ subscriber เถียงกันว่าใครต้องทำ
- **`CACHE_PREFIX` แยก environment ให้แล้ว** — อย่าเผลอ subscribe ด้วยชื่อ wire ตรงๆ
- **handler ต้องเร็ว** — subscriber ที่ตามไม่ทันถูกตัดการเชื่อมต่อโดย redis เอง
  งานหนักให้ publish เป็น job แล้วจบ
