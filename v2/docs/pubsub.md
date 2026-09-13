# Pub/Sub

`core.IPubSub` — redis publish/subscribe: fan-out to every listener, right now.
It rides on the cache connection, so anything that has a cache has pub/sub:
`ctx.PubSub()`, or `ctx.Cache().PubSub()`.

```go
ctx.PubSub().Publish("user.updated", user)
```

## When *not* to use it

Pub/sub is **not a queue**. A message published while nobody is listening is
gone. There is no acknowledgement, no redelivery, no persistence, and a
subscriber that falls too far behind is disconnected by the server.

| Use it for | Use something else for |
|---|---|
| cache invalidation across replicas | work that must not be lost → [jobs](./jobs.md) |
| config / feature-flag reloads | anything needing retries or ordering → [MQ](./mq.md) |
| live updates pushed to websockets | anything a consumer must ack |
| presence, "someone is typing", counters | cross-service contracts |

If losing a message would be a bug, publish a job instead — or write the fact
down durably first and publish only the notification.

That last shape is the one to reach for most often:

```go
// the row is the truth; the message is only a hint that it is there
if err := repository.New[Order](ctx).Create(&order); err != nil {
    return err
}
ctx.PubSub().Publish("order.created", order.ID)
```

A subscriber that misses the message still finds the row. A subscriber that
receives it just finds it sooner.

## What this section covers

| Page | |
|---|---|
| [Publishing](./pubsub-publishing.md) | payloads, channel naming, what a publish does not guarantee |
| [Subscribers](./pubsub-subscriber.md) | the handler-based API, options, lifecycle |
| [Raw subscriptions](./pubsub-subscriptions.md) | channels, patterns, and owning the loop yourself |
| [Recipes & testing](./pubsub-patterns.md) | invalidation, websockets, flags — and how to test them |

## Two ways to receive

**A subscriber** turns channels into handlers, the way the HTTP server turns
routes into handlers. It owns the subscription, the per-message `IContext`, panic
recovery, the concurrency limit and the drain on shutdown. Use it by default.

```go
sub := app.NewSubscriber()
sub.On("user.updated", func(ctx core.IContext, msg *core.PubSubMessage) error {
    user, err := core.BindMessage[User](msg)
    if err != nil {
        return err
    }
    return core.SetJSON(ctx.Cache(), "user:"+user.ID, user, time.Minute)
})
if err := sub.Start(); err != nil {
    panic(err)
}
```

**A raw subscription** hands you the channel and nothing else — for a websocket
hub, or a listener whose lifetime is not the process's.

```go
sub, _ := ctx.PubSub().Subscribe("user.updated")
defer sub.Close()
for msg := range sub.C() { … }
```

## Namespacing

`CACHE_PREFIX` namespaces channels as well as keys, so staging and production on
one redis cannot hear each other. Subscribers always see the name they subscribed
with; `Channel(name)` reveals the wire name for a publisher that is not built on
this framework:

```go
ctx.PubSub().Channel("user.updated")   // "myservice:user.updated"
```

## Reconnection

The driver re-subscribes by itself after a reconnect, so a redis restart
interrupts delivery without ending the subscription — messages published during
the outage are lost, which is pub/sub's contract, not a failure mode to code
around.

## Interfaces

```go
type IPubSub interface {
    Publish(channel string, msg any) IError
    Subscribe(channels ...string) (ISubscription, IError)
    PSubscribe(patterns ...string) (ISubscription, IError)
    Channel(name string) string
    Enabled() bool
    WithContext(ctx context.Context) IPubSub
    Close() IError
}

type ISubscription interface {
    C() <-chan *PubSubMessage
    Channels() []string
    Close() IError
}

type PubSubMessage struct {
    Channel string   // without the prefix
    Pattern string   // the glob that matched, for a pattern subscription
    Payload []byte
}
```

`msg.Bind(&dest)`, `core.BindMessage[T](msg)` and `msg.String()` read the
payload.

## With no cache configured

Publishing is dropped silently, the same way a write to a disabled cache is.
**Subscribing is an error** (`PUBSUB_DISABLED`) — a subscriber that silently
receives nothing forever is a service that looks healthy while doing none of its
work.
