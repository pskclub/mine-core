# Publishing

```go
ctx.PubSub().Publish("user.updated", user)   // struct → JSON
ctx.PubSub().Publish("cache.flush", "all")   // string/[]byte → raw
```

The encoding is the cache's: `string` and `[]byte` go on the wire unchanged,
everything else is JSON.

## What a publish does not guarantee

`Publish` returning `nil` means redis accepted the message. It does **not** mean:

- anybody was listening (nobody may have been — the message is then gone)
- anybody handled it successfully
- anybody will ever handle it

Publishing on a service with **no cache** is dropped silently, the same way a
write to a disabled cache is. So a `nil` from `Publish` carries almost no
information, and code should never treat it as confirmation that something
happened.

```go
// ❌ the publish is not the work
ctx.PubSub().Publish("order.created", order)
return c.JSON(http.StatusOK, echo.Map{"status": "processing"})

// ✅ the row is the work; the publish is a hint
if err := repository.New[Order](ctx).Create(&order); err != nil {
    return err
}
ctx.PubSub().Publish("order.created", order.ID)
return c.JSON(http.StatusCreated, order)
```

## Publish after the commit

A message published inside a [transaction](./database-transactions.md) escapes
immediately — redis knows nothing about your transaction. A subscriber can
therefore receive `order.created`, go looking for the order, and not find it,
because the transaction has not committed yet (or never will).

```go
// ❌ the subscriber can win the race against the commit
repo.Transaction(func(tx *gorm.DB) error {
    if err := repository.NewWithDB[Order](ctx, tx).Create(&order); err != nil {
        return err
    }
    ctx.PubSub().Publish("order.created", order.ID)   // already gone
    return nil
})

// ✅ publish once the row is definitely there
if err := repo.Transaction(func(tx *gorm.DB) error {
    return repository.NewWithDB[Order](ctx, tx).Create(&order)
}); err != nil {
    return err
}
ctx.PubSub().Publish("order.created", order.ID)
```

## What to put in the payload

Two styles, and the choice has consequences:

**The id only.** The subscriber reads the current state itself.

```go
ctx.PubSub().Publish("user.updated", user.ID)
```

Small, always current, and it survives being handled out of order — the
subscriber reads whatever is true when it runs. It costs a read per message.

**The whole document.** The subscriber gets everything it needs.

```go
ctx.PubSub().Publish("user.updated", user)
```

No read, but the payload is a snapshot: two updates in quick succession can be
handled in the other order, and the subscriber ends up caching the older one.
Include a version or a timestamp if that would matter.

Prefer the id for anything that mutates, and the document for events that are
facts about a moment ("this happened") rather than a state ("this is how it is
now").

## Naming channels

```
user.updated              entity.past-tense-verb
order.created
order.status.changed
cache.flush               an instruction, not an event
config.changed
```

Two conventions worth holding to:

- **Dots, hierarchically, most general first.** It is what makes
  [patterns](./pubsub-subscriptions.md#patterns) useful: `order.*` catches
  everything about orders precisely because the entity comes first.
- **Past tense for events, imperative for commands.** `user.updated` is a
  statement anybody may act on; `cache.flush` is an instruction. A channel that
  mixes the two ends up with subscribers that disagree about whose job it is to
  act.

Avoid putting ids in channel names (`user.42.updated`). Redis handles it, but
every subscriber then needs a pattern subscription, and the id belongs in the
payload where it can be typed.

## Prefixes

`CACHE_PREFIX` is applied to channels as well as keys, so a staging deploy cannot
publish into production's channels. Subscribers on the same service see the plain
name — the prefix is invisible on both ends.

It stops being invisible when the other end is **not** this framework: a Node
service, `redis-cli`, or a Grafana dashboard subscribing directly. `Channel`
gives the wire name:

```go
ctx.PubSub().Channel("user.updated")   // "myservice:user.updated"
```

```sh
redis-cli SUBSCRIBE myservice:user.updated
```

## From outside a request

Publishing from a background goroutine that outlives the request needs a context
that does too, or the publish is cancelled with the request:

```go
pub := ctx.PubSub().WithContext(context.Background())
go func() {
    pub.Publish("import.finished", id)
}()
```

## Publishing a lot

Each `Publish` is a round trip. A loop publishing per row in a batch import is
*N* round trips, and redis is fast enough that this usually goes unnoticed until
the batch is large.

```go
// ❌ 50,000 round trips
for _, u := range users {
    ctx.PubSub().Publish("user.updated", u.ID)
}

// ✅ one message the subscriber can expand
ctx.PubSub().Publish("users.bulk-updated", ids)
```

The second is also better for the subscriber: one handler invocation that can do
one bulk invalidation, rather than 50,000 that each do one.
