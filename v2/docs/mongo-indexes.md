# Indexes & change streams

## Creating indexes

Run them at startup. Creating an index that already exists is a no-op, so it is
safe on every boot:

```go
func ensureIndexes(ctx core.IContext) error {
    return mongorepo.New[User](ctx).EnsureIndexes(
        core.MongoIndex{Keys: []string{"email"}, Unique: true},
        core.MongoIndex{Keys: []string{"status", "-joined"}},
        core.MongoIndex{Keys: []string{"created_at"}, TTL: 24 * time.Hour},
    )
}
```

Or through the driver layer, for a collection with no document type:

```go
m := ctx.DBMongo()
m.EnsureIndex("sessions", core.MongoIndex{Keys: []string{"token"}, Unique: true})
m.EnsureIndexes("events", idx1, idx2, idx3)
```

> Unlike SQL, where [migrations own the schema](./migrations.md), Mongo indexes
> are declared in code and applied at boot. That works because creating one is
> idempotent and Mongo has no schema to drift from — but it also means a
> **removed** `MongoIndex` is not dropped. Use `DropIndex` deliberately when an
> index is retired.

## The index spec

```go
type MongoIndex struct {
    Keys      []string       // "-" first for descending; several = compound, in order
    Unique    bool
    Sparse    bool
    TTL       time.Duration  // expire documents this long after the (date) key
    Name      string         // generated from the keys when empty
    Partial   any            // bson.M — restrict which documents are indexed
    Collation *options.Collation
}
```

A non-numeric direction goes after a colon:

```go
core.MongoIndex{Keys: []string{"name:text"}}          // text search
core.MongoIndex{Keys: []string{"loc:2dsphere"}}       // geospatial
core.MongoIndex{Keys: []string{"tenant_id:hashed"}}   // sharding
```

## Compound indexes and order

The order of the keys is the whole design. An index on `{status, joined}` serves
a query filtering on `status` and sorting by `joined`; it does **not** serve one
filtering on `joined` alone.

The rule that gets you most of the way: **equality, then sort, then range.**

```go
// serves: Eq("status", …).Sort("-joined")
// serves: Eq("status", …)
// does not serve: Sort("-joined") alone
core.MongoIndex{Keys: []string{"status", "-joined"}}
```

Check what the planner actually did rather than guessing:

```go
var plan bson.M
err := users.DB().Collection("users").Database().
    RunCommand(users.DB().Context(), bson.D{
        {Key: "explain", Value: bson.M{
            "find":   "users",
            "filter": users.Eq("status", "active").Filter(),
        }},
        {Key: "verbosity", Value: "executionStats"},
    }).Decode(&plan)
```

A `COLLSCAN` in the winning plan means no index was used.

## Unique indexes

A unique index is the only thing that actually enforces uniqueness. A
check-then-insert in application code loses the race that matters:

```go
users.EnsureIndexes(core.MongoIndex{Keys: []string{"email"}, Unique: true})

if err := users.Create(&u); errors.Is(err, core.ErrDuplicateKey) {
    return c.NewError(err, errmsgs.EmailAlreadyExists)   // a 409
}
```

### Partial: unique among the live documents

Soft deletes and a unique index disagree: a deleted user still holds the email
address. `Partial` indexes only the documents that matter:

```go
core.MongoIndex{
    Keys:    []string{"email"},
    Unique:  true,
    Partial: bson.M{"deleted_at": nil},   // only live rows
}
```

`Sparse` is the older, blunter version — it skips documents where the key is
missing entirely. Prefer `Partial`, which says what it means.

## TTL: documents that expire

```go
core.MongoIndex{Keys: []string{"created_at"}, TTL: 24 * time.Hour}
core.MongoIndex{Keys: []string{"expires_at"}, TTL: 0}   // expire *at* that time
```

A TTL index needs a single, date-typed key. Mongo's background task sweeps about
once a minute, so "expired" and "gone" are up to a minute apart — do not build a
security boundary on the deletion timing. Check the timestamp too.

TTL is the right answer for sessions, one-time tokens, rate-limit rows and
anything else whose value is time-boxed. It is the wrong answer for data with a
retention *policy*, because it deletes silently and leaves no record.

## Multikey: indexing inside arrays

An index on an array path is a multikey index — Mongo makes one entry per
element:

```go
users.EnsureIndexes(
    core.MongoIndex{Keys: []string{"profile.city"}},
    core.MongoIndex{Keys: []string{"items.sku"}},              // multikey
    core.MongoIndex{Keys: []string{"profile.city", "-profile.age"}},
    core.MongoIndex{Keys: []string{"profile.loc:2dsphere"}},
)
```

Only **one** array field per compound index. `{items.sku, tags}` is rejected,
because the number of index entries would be the product of the two arrays.

## Managing indexes

```go
list, err := m.ListIndexes("users")     // []map[string]any
err = m.DropIndex("users", "email_1")
```

Dropping an index that a query depends on turns that query into a collection
scan, quietly, under production load. Confirm what uses it with `explain` before
removing it.

## Collection management

```go
names, err := m.ListCollections()
err = m.DropCollection("temp_import")
```

## Change streams

A change stream is a live feed of the writes to a collection. It needs a
**replica set** — a standalone server refuses to open one.

```go
stream, err := mongorepo.New[User](ctx).Eq("status", "active").Watch()
if err != nil {
    return err
}
defer stream.Close(ctx)

for stream.Next(ctx) {
    var event struct {
        OperationType string `bson:"operationType"`
        FullDocument  User   `bson:"fullDocument"`
    }
    if err := stream.Decode(&event); err != nil {
        return ctx.NewError(err, errmsgs.DBError)
    }
    ctx.Log().Info("user changed", "op", event.OperationType, "id", event.FullDocument.ID)
}
return ctx.NewError(stream.Err(), errmsgs.DBError)
```

The repository re-points its filter at `fullDocument` when it builds the
pipeline, so `Eq("status", "active").Watch()` means what it reads like — the
change events for active users, not events whose top level has a `status` field.

Through the driver layer, with your own pipeline:

```go
stream, err := m.Watch("users", []bson.M{
    {"$match": bson.M{"operationType": bson.M{"$in": []string{"insert", "update"}}}},
})
```

### What a change stream is not

| | |
|---|---|
| It **is** | a live feed for cache invalidation, search indexing, projections, websockets |
| It is **not** | a queue: no acknowledgement, no retry, no dead letter |

A consumer that goes away loses what happened while it was gone unless it stores
a resume token and reopens from it. If losing an event would be a bug, write the
fact down durably and let a [job](./jobs.md) act on it — the same reasoning that
applies to [pub/sub](./pubsub.md#when-not-to-use-it).

Also: `fullDocument` is only present on updates when the stream asks for it
(`fullDocument: "updateLookup"`), and that lookup reads the document as it is
*now*, not as it was at the moment of the change.
