# Aggregation

An aggregation is a list of stages, each one transforming the stream of documents
the previous one produced. `Aggregate` runs the pipeline and decodes the result;
`AggregateCursor` streams it.

```go
var totals []struct {
    Status string `bson:"_id"`
    Total  int64    `bson:"total"`
}

err := ctx.DBMongo().Aggregate(&totals, "users", []bson.M{
    {"$match": bson.M{"status": bson.M{"$ne": "deleted"}}},
    {"$group": bson.M{"_id": "$status", "total": bson.M{"$sum": "$age"}}},
    {"$sort":  bson.M{"total": -1}},
})
```

> For a **typed** builder over the same thing —
> `mongorepo.Aggregate[Row](repo).Lookup(...).Group(...).All()` — see
> [Repository: pipelines](./mongo-repository-aggregation.md). This page is the
> layer underneath, and the one to reach for when the pipeline is not attached to
> a document type.

## Decoding

The `_id` of a `$group` is the grouping key, so a result struct tags it as
`bson:"_id"`:

```go
type revenueRow struct {
    Month string  `bson:"_id"`
    Total float64 `bson:"total"`
    Count int64     `bson:"count"`
}

var rows []revenueRow
err := m.Aggregate(&rows, "orders", []bson.M{
    {"$match": bson.M{"status": "paid", "created_at": bson.M{"$gte": since}}},
    {"$group": bson.M{
        "_id":   bson.M{"$dateToString": bson.M{"format": "%Y-%m", "date": "$created_at"}},
        "total": bson.M{"$sum": "$amount"},
        "count": bson.M{"$sum": 1},
    }},
    {"$sort": bson.M{"_id": 1}},
})
```

A field with no matching `bson` tag decodes to its zero value, silently. When a
result comes back empty-looking, the tag is the first thing to check.

## Order matters

`$match` first, always. A pipeline that filters after a `$group` has already read
and grouped everything it is about to throw away — and a leading `$match` is the
only stage that can use an index.

```go
// ✅ the index on status does the work
{"$match": bson.M{"status": "paid"}}, {"$group": ...}

// ❌ every document is grouped, then most of the result is discarded
{"$group": ...}, {"$match": bson.M{"_id": "paid"}}
```

The same is true of `$project` and `$limit`: move them as early as the logic
allows.

## Joins

`$lookup` reads documents from another collection into an array field:

```go
type userWithOrders struct {
    ID     string  `bson:"_id"`
    Email  string  `bson:"email"`
    Orders []Order `bson:"orders"`
}

var rows []userWithOrders
err := m.Aggregate(&rows, "users", []bson.M{
    {"$match": bson.M{"status": "active"}},
    {"$lookup": bson.M{
        "from":         "orders",
        "localField":   "_id",
        "foreignField": "user_id",
        "as":           "orders",
    }},
    {"$limit": 100},
})
```

The correlated form takes a sub-pipeline, which is how the joined side gets
filtered instead of being read whole:

```go
{"$lookup": bson.M{
    "from": "orders",
    "let":  bson.M{"uid": "$_id"},
    "pipeline": []bson.M{
        {"$match": bson.M{"$expr": bson.M{"$and": []bson.M{
            {"$eq": []any{"$user_id", "$$uid"}},
            {"$eq": []any{"$status", "paid"}},
        }}}},
        {"$project": bson.M{"amount": 1}},
    },
    "as": "orders",
}}
```

`$lookup` is a join Mongo executes per input document. It has no join optimiser,
so the foreign field wants an index and the input set wants to be small — which
is another way of saying `$match` first.

## Unwinding arrays

`$unwind` turns one document with an *N*-element array into *N* documents. It is
how you count, group or sort by something inside an array:

```go
type skuRow struct {
    SKU   string `bson:"_id"`
    Total int64    `bson:"total"`
}

var rows []skuRow
err := m.Aggregate(&rows, "orders", []bson.M{
    {"$match": bson.M{"status": "paid"}},
    {"$unwind": "$items"},
    {"$group": bson.M{"_id": "$items.sku", "total": bson.M{"$sum": "$items.qty"}}},
    {"$sort": bson.M{"total": -1}},
    {"$limit": 20},
})
```

A document whose array is empty or missing **disappears** at `$unwind`. When
dropping it would under-count the result, keep it:

```go
{"$unwind": bson.M{"path": "$items", "preserveNullAndEmptyArrays": true}}
```

## Several results in one pass

`$facet` runs sub-pipelines over the same input, which is how a list and its
total come back from one query:

```go
var out []struct {
    Rows  []User `bson:"rows"`
    Total []struct {
        N int64 `bson:"n"`
    } `bson:"total"`
}

err := m.Aggregate(&out, "users", []bson.M{
    {"$match": filter},
    {"$facet": bson.M{
        "rows":  []bson.M{{"$sort": bson.M{"created_at": -1}}, {"$skip": 0}, {"$limit": 20}},
        "total": []bson.M{{"$count": "n"}},
    }},
})
```

`$facet` always produces exactly one document, so the result decodes into a
one-element slice with arrays inside it — including `total`, which is an empty
array when nothing matched.

## Options

```go
err := m.Aggregate(&rows, "events", pipeline, core.MongoAggregateOptions{
    AllowDiskUse: true,                  // $group/$sort past the 100MB limit
    MaxTime:      30 * time.Second,      // killed on the server
    Comment:      "monthly-revenue",     // findable in currentOp / the profiler
    Hint:         "status_1_created_at_1",
    BatchSize:    500,
    Let:          bson.M{"since": since},   // read as "$$since"
})
```

`AllowDiskUse` is what a report over a large collection needs. Without it, a
`$group` or `$sort` fails at 100MB with "Sort exceeded memory limit" — reliably,
the first time production data gets bigger than the test fixture.

`Comment` is worth setting on anything long-running: it is how a pipeline is
identified in `db.currentOp()` when somebody has to decide whether to kill it.

## Streaming a large pipeline

```go
cur, err := m.AggregateCursor("events", pipeline,
    core.MongoAggregateOptions{AllowDiskUse: true, BatchSize: 1000})
if err != nil {
    return err
}
defer cur.Close(m.Context())

for cur.Next(m.Context()) {
    var row eventRow
    if err := cur.Decode(&row); err != nil {
        return ctx.NewError(err, errmsgs.DBError)
    }
    if err := write(row); err != nil {
        return err
    }
}
return ctx.NewError(cur.Err(), errmsgs.DBError)
```

An export that decodes into a slice first has the whole report in memory twice.
The cursor form holds one batch.

## Writing the result somewhere

`$merge` and `$out` end a pipeline by writing to a collection — a materialised
view, refreshed by a nightly [job](./jobs.md):

```go
{"$merge": bson.M{"into": "daily_revenue", "whenMatched": "replace"}}
```

`$out` replaces the target collection wholesale; `$merge` updates it in place.
Both must be the **last** stage.

## When not to aggregate

A pipeline that ends up doing the work of a query is slower than the query, and
much harder to read. Reach for aggregation when you need grouping, joins, or a
shape the documents do not have. For "find these documents, sorted", use
[`Find`](./mongo-queries.md) or the [repository](./mongo-repository-queries.md).
