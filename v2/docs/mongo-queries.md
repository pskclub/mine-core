# Queries (driver layer)

`core.IMongoDB` is a set of helpers over the official driver: filters are
`bson.M`, results decode into your structs, and errors come back as
`core.IError`. It is what [`mongorepo`](./mongo-repository.md) is built on, and
what to use when a query does not fit a repository.

```go
import "go.mongodb.org/mongo-driver/v2/bson"

m := ctx.DBMongo()
```

## Reading

```go
var user User
err := m.FindOne(&user, "users", bson.M{"email": "a@b.com"})
if errors.Is(err, core.ErrDocumentNotFound) {
    // nothing matched — a 404, code NOT_FOUND
}

var users []User
err = m.Find(&users, "users", bson.M{"status": "active"}, core.MongoFindOptions{
    Sort:       []string{"-created_at", "name"},   // "-" is descending
    Limit:      20,
    Skip:       40,
    Projection: bson.M{"password": 0},
})

n, err := m.Count("users", bson.M{"status": "active"})
n, err = m.EstimatedCount("users")                  // metadata, no scan
ok, err := m.Exists("users", bson.M{"email": email}) // no document transferred

var statuses []string
err = m.Distinct(&statuses, "users", "status", nil)  // nil filter = everything
```

`Count` runs a real count and is exact. `EstimatedCount` reads collection
metadata — instant, and approximate right after a bulk write. Use it for "about
how many documents are there", never for a total the caller will do arithmetic
on. `MongoPaginate` picks between the two for you — see below.

### Find options

| Field | What it does |
|---|---|
| `Sort` | `[]string`, `-` prefix for descending — the same shape as `PageOptions.OrderBy` |
| `Limit` / `Skip` | the page |
| `Projection` | passed to the driver as-is, so the full projection language works |
| `Hint` | force an index when the planner picks the wrong one |
| `Collation` | how strings compare — the way to get a case-insensitive query that can still use an index |
| `MaxTime` | bounds the query **on the server**, so a slow one is killed there |
| `BatchSize` | documents per round trip |

`Collation` deserves the attention. A case-insensitive search written as a regex
(`{"$regex": "^ann", "$options": "i"}`) cannot use an index; the same query with
a case-insensitive collation can.

### Large result sets

`Find` without a `Limit` reads the whole result set into memory. Pass one, page,
or stream:

```go
// page — one query for the items, one for the total
page, err := core.MongoPaginate[User](m, "users", bson.M{"status": "active"},
    c.GetPageOptions())
// → *core.Page[User]{Items, Total, Count, Page, Limit}

// no filter — the total comes from the collection's metadata, not a scan
page, err = core.MongoPaginate[User](m, "users", nil, c.GetPageOptions())

// stream — never holds more than a batch
cur, err := m.FindCursor("users", bson.M{"status": "active"},
    core.MongoFindOptions{BatchSize: 500})
if err != nil {
    return err
}
defer cur.Close(m.Context())

for cur.Next(m.Context()) {
    var u User
    if err := cur.Decode(&u); err != nil {
        return ctx.NewError(err, errmsgs.DBError)
    }
    if err := export(u); err != nil {
        return err
    }
}
return ctx.NewError(cur.Err(), errmsgs.DBError)
```

`MongoPaginate` counts with `Count` when the filter narrows anything, and with
`EstimatedCount` when it does not (`nil`, `bson.M{}`, `bson.D{}`). Counting a
whole collection walks it — on a large one that is the slowest part of the first
page, and the metadata already knows the number. The price is a `Total` that can
drift after an unclean shutdown, or on a sharded collection still holding
orphans. If a caller needs an exact unfiltered total, call `m.Count(collection,
nil)` and build the `Page` with `core.NewPage`.

A cursor is a server-side resource. `defer cur.Close(...)`, and pass
`m.Context()` — a cursor iterated on a different context can outlive, or leak
out of, the operation that owns it.

## Writing

```go
id, err := m.InsertOne("users", User{Name: "alice"})
// id is a hex string — usable as a filter, which is the point
err = m.FindOne(&user, "users", core.MongoByID(id))

ids, err := m.InsertMany("users", []any{a, b, c})

res, err := m.UpdateOne("users", core.MongoByID(id),
    bson.M{"$set": bson.M{"name": "bob"}})
if res.Matched == 0 {
    // no such document — the caller decides whether that is a 404
}

res, err = m.UpdateOne("settings", bson.M{"user_id": id},
    bson.M{"$set": bson.M{"theme": "dark"}}, core.MongoUpdateOptions{Upsert: true})
res.UpsertedID    // the id it created, if it created one

res, err = m.UpdateMany("users", bson.M{"status": "new"},
    bson.M{"$set": bson.M{"status": "active"}})
res, err = m.ReplaceOne("users", core.MongoByID(id), user)

n, err := m.DeleteOne("users", core.MongoByID(id))
n, err = m.DeleteMany("users", bson.M{"status": "stale"})
```

An update takes **operators**, not a document. `bson.M{"name": "bob"}` without a
`$set` is a replace in disguise, and the driver rejects it — which is better than
what happens with `ReplaceOne`, where the missing fields are simply gone.

```go
type MongoUpdateResult struct {
    Matched    int64
    Modified   int64
    Upserted   int64
    UpsertedID string
}
```

`Matched > 0 && Modified == 0` means the document was found and already had those
values. That is a successful no-op, not a failure — and telling it apart from
"no such document" is why writes return a result rather than only an error.

### Duplicate keys

A collision with a unique index comes back as a **409** wrapping
`core.ErrDuplicateKey` — the "this email is already registered" case, which is an
answer rather than a server error:

```go
if _, err := m.InsertOne("users", user); errors.Is(err, core.ErrDuplicateKey) {
    return c.NewError(err, errmsgs.EmailAlreadyExists)
}
```

## Atomic find-and-modify

A `Find` followed by an `Update` is a race: two workers can read the same
document and both decide they own it. `FindOneAndUpdate` does both in one
operation, so exactly one wins:

```go
var job Job
err := m.FindOneAndUpdate(&job, "jobs",
    bson.M{"status": "queued"},
    bson.M{"$set": bson.M{"status": "running", "worker": workerID}},
    core.MongoFindModifyOptions{
        Sort:      []string{"created_at"},   // which one, when several match
        ReturnNew: true,                     // decode the document after the change
    })
if errors.Is(err, core.ErrDocumentNotFound) {
    // the queue is empty
}
```

`ReturnNew` defaults to **false** — Mongo's own default, which decodes the
document as it was *before*. That is what you want when the old value is the
thing being claimed, and a surprise otherwise.

`FindOneAndReplace` and `FindOneAndDelete` are the same shape.

## Bulk writes

Many different operations in one round trip:

```go
res, err := m.BulkWrite("users", []mongo.WriteModel{
    mongo.NewUpdateOneModel().
        SetFilter(bson.M{"email": "a@b.com"}).
        SetUpdate(bson.M{"$set": bson.M{"status": "active"}}).
        SetUpsert(true),
    mongo.NewDeleteOneModel().SetFilter(bson.M{"email": "old@b.com"}),
}, true)   // ordered

res.Inserted, res.Modified, res.Deleted, res.Upserted
```

`ordered: true` stops at the first failure; `false` runs the rest and reports
what failed. Unordered is faster and is the right default for independent
operations — a single bad document should not abandon the other nine hundred.

## Object ids

A filter built from a hex **string** instead of an `ObjectID` matches nothing,
silently. It is the most common Mongo bug there is, and two helpers prevent it:

```go
core.MongoByID(id)                    // bson.M{"_id": ObjectID(id)}, or the raw
                                      //   string for ids of your own making
oid, err := core.MongoObjectID(id)    // parse explicitly; 400 INVALID_ID if not hex
```

`MongoByID` falls back to the raw string when the value is not a valid hex id, so
a collection keyed by ULIDs or slugs works the same way.

## Anything else

```go
coll := m.Collection("users")                  // *mongo.Collection
cur, err := coll.Watch(m.Context(), pipeline)  // change streams, GridFS, raw bulk
```

Use `m.Context()` with it — inside a `Transaction` that is the session's context,
and a driver call made on any other context silently lands outside the
transaction.

## Next

- [Aggregation](./mongo-aggregation.md)
- [Indexes & change streams](./mongo-indexes.md)
- [Transactions](./mongo-transactions.md)
- [The repository](./mongo-repository.md) — the typed layer over all of this
