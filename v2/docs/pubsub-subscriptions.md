# Raw subscriptions

When the [subscriber](./pubsub-subscriber.md)'s shape does not fit — a websocket
hub, a one-off listener, something whose lifetime is a connection rather than the
process — take the channel directly.

```go
sub, err := ctx.PubSub().Subscribe("user.updated", "user.deleted")
if err != nil {
    return err
}
defer sub.Close()

for msg := range sub.C() {
    var user User
    if err := msg.Bind(&user); err != nil {
        continue
    }
    hub.Broadcast(user)
}
```

`sub.C()` is closed by `Close`, so the `range` ends cleanly.

## Closing is yours

A subscription deliberately does **not** die with the request context it was
opened from. `Close` is what ends it, and it is the caller's job.

That is on purpose: a websocket hub opened from an HTTP request has to outlive
the request that created it. The cost is that a forgotten `Close` leaks a
goroutine and a redis connection for the life of the process.

```go
sub, err := ctx.PubSub().Subscribe("user.updated")
if err != nil {
    return err
}
defer sub.Close()          // put it here, immediately
```

## Patterns

Patterns use redis globs: `*`, `?`, `[abc]`, `[a-c]`, `[^a]`, and `\` to escape.

```go
sub, _ := ctx.PubSub().PSubscribe("order.*")
msg := <-sub.C()
msg.Channel   // "order.created" — the actual channel
msg.Pattern   // "order.*" — the glob that matched
```

| Pattern | Matches | Does not match |
|---|---|---|
| `order.*` | `order.created`, `order.status.changed` | `orders.created` |
| `user.?` | `user.a` | `user.ab` |
| `order.[cd]*` | `order.created`, `order.deleted` | `order.updated` |

`*` crosses dots — `order.*` catches `order.status.changed` too. Redis globs have
no concept of a segment, so a hierarchy is a naming convention rather than
something the matcher enforces.

A pattern subscription costs more on the server than an exact one: redis matches
every published channel against every registered pattern. A handful is nothing;
hundreds on a busy instance is a real cost.

## Which channels a subscription holds

```go
sub.Channels()   // the names or patterns it was opened with, without the prefix
```

## Consuming with a deadline

A `range` over `sub.C()` blocks until the subscription closes. When the loop also
has to notice something else — a shutdown signal, a client disconnecting — select:

```go
for {
    select {
    case msg, ok := <-sub.C():
        if !ok {
            return nil          // closed
        }
        handle(msg)
    case <-ctx.Done():
        return sub.Close()      // the request or the process is going away
    }
}
```

## A websocket hub

The shape raw subscriptions exist for: one redis subscription per **process**,
fanned out in memory to however many sockets are connected.

```go
type Hub struct {
    mu      sync.RWMutex
    clients map[*websocket.Conn]string   // conn → user id
}

func (h *Hub) Run(app *core.App) error {
    sub, err := app.PubSub().PSubscribe("notify.*")
    if err != nil {
        return err
    }
    defer sub.Close()

    for msg := range sub.C() {
        userID := strings.TrimPrefix(msg.Channel, "notify.")

        h.mu.RLock()
        for conn, id := range h.clients {
            if id == userID {
                _ = conn.WriteMessage(websocket.TextMessage, msg.Payload)
            }
        }
        h.mu.RUnlock()
    }
    return nil
}
```

One subscription for the whole process, not one per connected client. Ten
thousand sockets is then ten thousand map entries rather than ten thousand redis
connections — which redis would refuse long before you got there.

## Backpressure

Each subscription buffers a bounded number of messages. A consumer slower than
the publisher fills that buffer, and then falls behind the server's own output
buffer — at which point **redis disconnects it**. That is redis's design, not
this framework's: a slow subscriber is dropped rather than allowed to consume the
server's memory.

So the loop body must be fast. Anything slow belongs somewhere else:

```go
for msg := range sub.C() {
    select {
    case work <- msg:        // hand off to a worker pool
    default:
        ctx.Log().Warn("dropping message: workers are saturated", "channel", msg.Channel)
    }
}
```

Dropping deliberately, with a log line, is better than being disconnected
silently — you get to choose what is lost, and you find out that it happened.

The [subscriber](./pubsub-subscriber.md) already does this properly: a sequential
read loop, handlers behind a semaphore. Prefer it unless you need the channel.

## Custom context

```go
pub := ctx.PubSub().WithContext(context.Background())
sub, _ := pub.Subscribe("user.updated")
```

For a subscription opened during a request that must outlive it. The subscription
itself already survives the request context — this is for the *operations* on the
handle.
