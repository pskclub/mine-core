# Subscribers

A subscriber turns channels into handlers, the way the HTTP server turns routes
into handlers. It owns the subscription, the per-message `IContext`, panic
recovery, the concurrency limit and the drain on shutdown.

```go
func main() {
    app, _ := core.NewApp(env, core.WithCache("default", redis))
    defer app.Shutdown(context.Background())

    sub := app.NewSubscriber()

    sub.On("user.updated", func(ctx core.IContext, msg *core.PubSubMessage) error {
        user, err := core.BindMessage[User](msg)
        if err != nil {
            return err
        }
        return core.SetJSON(ctx.Cache(), "user:"+user.ID, user, time.Minute)
    })

    sub.OnPattern("order.*", func(ctx core.IContext, msg *core.PubSubMessage) error {
        ctx.Log().Info("order event", "channel", msg.Channel)
        return nil
    })

    if err := sub.Start(); err != nil {
        panic(err)
    }
    core.StartHTTPServer(server)   // the subscriber runs alongside
}
```

## The handler

```go
type PubSubHandler func(ctx IContext, msg *PubSubMessage) error
```

Each delivery gets a **full `IContext`** in `ModeMQ` — logger, database, cache,
Sentry scope and its own transaction. So a handler is written the same way a
handler or a job is, and everything reachable from a request is reachable here.

```go
sub.On("order.created", func(ctx core.IContext, msg *core.PubSubMessage) error {
    id := msg.String()

    order, err := repository.New[Order](ctx).FindOne("id = ?", id)
    if errors.Is(err, errmsgs.NotFound) {
        ctx.Log().Warn("order not found", "id", id)
        return nil            // nothing to retry against — do not fail
    }
    if err != nil {
        return err
    }
    return notify(ctx, order)
})
```

### What returning an error does

It is logged and reported to Sentry. **It does not redeliver** — there is no
redelivery in pub/sub, so an error is a record of what went wrong, not a request
to try again.

A panic ends that message, not the process.

Which means the error return is for *observability*, and the decision "is this
worth an alert" is yours. A missing row because the message raced the commit is
noise; a failing downstream call is not.

## Reading the payload

```go
user, err := core.BindMessage[User](msg)   // typed, generic

var user User
err := msg.Bind(&user)                     // into an existing value

id := msg.String()                         // raw, for a plain-string payload

msg.Channel   // "order.created" — without the prefix
msg.Pattern   // "order.*" — the glob that matched, for a pattern subscription
msg.Payload   // []byte
```

## Exact channels and patterns

```go
sub.On("user.updated", handler)       // exact
sub.OnPattern("order.*", handler)     // glob
```

Registering the same channel twice **replaces** the first handler — one handler
per channel, not a list. If two things need to happen, call both from one
handler, or run two subscribers.

Under the hood a subscriber opens at most **two** subscriptions — one for all the
exact names, one for all the globs — rather than one connection per channel.

## Options

```go
app.NewSubscriber(
    core.WithSubscriberConcurrency(8),          // default 1 (keeps arrival order)
    core.WithSubscriberTimeout(10*time.Second), // default 30s per message
    core.WithSubscriberCache("events"),         // default "default"
)
```

### Concurrency

Concurrency is **1** by default, so messages are handled in the order they
arrived. Raise it when the handlers are independent:

```go
core.WithSubscriberConcurrency(8)
```

The read loop stays sequential either way; only the handlers fan out. So raising
it trades ordering for throughput and nothing else. Keep it at 1 when two
messages about the *same* entity would race each other — two `user.updated` for
one user, handled out of order, cache the older one.

### Timeout

The default is 30 seconds. A handler that overruns has its context cancelled,
which is what stops a stuck message from holding the only worker forever:

```go
core.WithSubscriberTimeout(5 * time.Second)
```

Cancellation is cooperative. The context is cancelled; the handler still has to
notice — which it does automatically for anything using `ctx` (database queries,
`Requester`, cache calls) and not at all for a tight loop that never checks.

### A dedicated cache

```go
core.WithSubscriberCache("events")
```

Worth doing when pub/sub traffic is heavy: a subscriber that falls behind is
disconnected by redis, and separating it from the instance holding hot keys means
that pressure does not land on the cache the request path depends on.

## Lifecycle

```go
sub.Start()      // returns as soon as the subscriptions are live
sub.Running()    // has Start run and Stop not
sub.Stop(ctx)    // idempotent; waits for in-flight handlers up to the deadline
```

`Start` is not blocking — the work happens in the background, so a service can
run an HTTP server and a subscriber in one process.

`app.Shutdown` stops every subscriber it handed out **before** closing the pools
they read from, waiting for in-flight handlers within the context's deadline.
That ordering is the point: a subscriber still dispatching against a closed
connection is a burst of errors on the way out.

```go
defer app.Shutdown(context.Background())
```

### Errors from Start

| Error | Meaning |
|---|---|
| `SUBSCRIBER_EMPTY` | no handlers registered — `Start` before `On` |
| `SUBSCRIBER_RUNNING` | already started |
| `PUBSUB_DISABLED` | no cache configured |

`PUBSUB_DISABLED` is deliberately fatal rather than a silent no-op. A subscriber
that receives nothing forever is a service that looks healthy while doing none of
its work — the failure everybody discovers a week later.

## Running one as its own process

A subscriber does not need an HTTP server. A consumer-only deployment is a
`main` that starts one and waits:

```go
func main() {
    app, err := core.NewApp(env, core.WithCache("default", redis), core.WithSQL("default", db))
    if err != nil {
        panic(err)
    }

    sub := app.NewSubscriber(core.WithSubscriberConcurrency(4))
    sub.On("order.created", handleOrderCreated)
    if err := sub.Start(); err != nil {
        panic(err)
    }

    quit := make(chan os.Signal, 1)
    signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
    <-quit

    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()
    _ = app.Shutdown(ctx)
}
```

See [Lifecycle & roles](./lifecycle.md) for running the same binary in several
roles.
