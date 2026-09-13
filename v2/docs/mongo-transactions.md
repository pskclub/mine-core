# Transactions

```go
err := ctx.DBMongo().Transaction(func(tx core.IMongoDB) error {
    if _, err := tx.InsertOne("orders", order); err != nil {
        return err
    }
    _, err := tx.UpdateOne("stock", bson.M{"sku": sku},
        bson.M{"$inc": bson.M{"qty": -1}})
    return err
})
```

Returning an error or panicking aborts it; returning `nil` commits.

## You need a replica set

Mongo only offers transactions on a replica set or a sharded cluster. A
standalone server fails with a message saying so — including the single-node
`docker run mongo` most people develop against.

A single-node replica set is enough, and is what a compose file for local
development should use:

```yaml
mongo:
  image: mongo:7
  command: ["--replSet", "rs0", "--bind_ip_all"]
```

```sh
mongosh --eval 'rs.initiate()'
```

```sh
DB_MONGO_REPLICA_NAME=rs0
```

Without it, code that works in production fails locally with an error nobody
recognises — and, worse, the reverse: a test suite on a standalone server never
exercises the transactional path at all.

## The rule that causes every bug

**Use the `tx` handle inside.** The outer handle is not in the transaction, and a
write made through it commits immediately and survives the abort:

```go
err := m.Transaction(func(tx core.IMongoDB) error {
    tx.InsertOne("orders", order)          // ✅ in the transaction
    m.InsertOne("audit", entry)            // ❌ committed regardless
    return errors.New("boom")              // order rolls back, entry does not
})
```

The same applies to `ctx.DBMongo()` and to any repository built with `New` inside
the closure. If it did not get `tx`, it is not part of the transaction.

## With repositories

One collection:

```go
err := users.Transaction(func(tx *mongorepo.Repo[User]) error {
    if err := tx.Create(&user); err != nil {
        return err
    }
    _, err := tx.ByID(id).Inc("seats", -1)
    return err
})
```

Several collections — take the handle from `core.IMongoDB` and bind each
repository to it:

```go
err := ctx.DBMongo().Transaction(func(tx core.IMongoDB) error {
    users  := mongorepo.NewIn[User](tx)
    orders := mongorepo.NewIn[Order](tx)

    if err := orders.Create(&order); err != nil {
        return err
    }
    _, err := users.ByID(order.UserID).Inc("order_count", 1)
    return err
})
```

## The context trap

Every helper on the `tx` handle already uses the session's context. A **raw
driver call** does not, unless you give it one:

```go
err := m.Transaction(func(tx core.IMongoDB) error {
    coll := tx.Collection("users")

    coll.InsertOne(tx.Context(), doc)              // ✅ in the transaction
    coll.InsertOne(context.Background(), doc)      // ❌ silently outside it
    return nil
})
```

There is no error for the second one. It succeeds, it is not rolled back, and
nothing in the logs distinguishes it. `tx.Context()` inside a transaction is the
session's context — use it for every driver call.

## What belongs inside

Only the writes that must succeed or fail together. A Mongo transaction holds a
session and locks on the documents it touches, and it has a **60-second** server
limit by default.

| Keep out | Why |
|---|---|
| HTTP calls to another service | cannot be rolled back, and its latency is now lock-hold time |
| S3 uploads | same — an aborted transaction leaves the object behind |
| Publishing to pub/sub or a queue | subscribers can see an event for a transaction that then aborts |
| Long loops over many documents | the 60s limit, and the write conflicts |

The usual shape: commit first, then act on it.

```go
if err := ctx.DBMongo().Transaction(func(tx core.IMongoDB) error {
    _, err := tx.InsertOne("orders", order)
    return err
}); err != nil {
    return err
}
ctx.PubSub().Publish("order.created", order)   // after the commit
```

## Write conflicts

Two transactions writing the same document give one of them a **transient
transaction error**. Mongo's driver retries the commit for some of these, but a
conflict inside the callback surfaces to you.

Prefer designs that do not conflict:

- **Atomic operators** — `$inc`, `$push`, `$addToSet` need no transaction at all,
  and no read-modify-write.
- **`FindOneAndUpdate`** — claim a document in one operation instead of reading
  it, deciding, and writing it back.
- **One document** — a write to a single document is already atomic. Embedding
  the thing that must stay consistent is often better than a transaction across
  two collections; that is the modelling advantage Mongo has, and a transaction
  is what you reach for when the model could not take it.

## Testing

Transactional behaviour cannot be faked. Run the integration suite against a real
replica set:

```sh
docker run -p 27017:27017 mongo --replSet rs0
mongosh --eval 'rs.initiate()'
APP_DB_MONGO_HOST=127.0.0.1 APP_DB_MONGO_NAME=coretest \
  APP_DB_MONGO_REPLICA_NAME=rs0 make test-integration
```

See [Integration tests](./testing-integration.md).
