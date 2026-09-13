# Repository: pipelines

`mongorepo.Aggregate[T](repo)` is a typed pipeline builder. `T` is the row it
produces, the repository's chain becomes the leading `$match`, and every stage
returns a new pipeline so a base can be branched.

```go
type revenue struct {
    Status string  `bson:"_id"`
    Total  float64 `bson:"total"`
    Count  int64     `bson:"count"`
}

rows, err := mongorepo.Aggregate[revenue](users.Eq("status", "active")).
    Group("$status", bson.M{
        "total": bson.M{"$sum": "$amount"},
        "count": bson.M{"$sum": 1},
    }).
    Sort("-total").
    All()
```

Two things follow from the chain becoming the `$match`: the conditions are
written once and reused, and the filter is the **first** stage — which is the
only position from which it can use an index.

## Stages

| Builder | Stage |
|---|---|
| `Match` `Group` `Project` `AddFields` `Set` `Unset` | the basics |
| `Sort` `Limit` `Skip` `Sample` `CountAs` | ordering and slicing |
| `Lookup` / `LookupOne` | `$lookup`; `LookupOne` unwinds a belongs-to join |
| `GraphLookup` | recursive joins — trees, chains |
| `Unwind` | one row per array element |
| `Facet` | several sub-pipelines in one pass |
| `Bucket` `BucketAuto` `SortByCount` | histograms and tallies |
| `ReplaceRoot` `UnionWith` | reshaping and appending |
| `Out` `Merge` | writing the result to a collection |
| `Stage` / `StagesOf` | anything else, raw |

`Stage` takes a raw `bson.M`, so `$setWindowFields`, `$densify` or an Atlas
`$search` are one call away — nothing is off limits.

```go
p := mongorepo.Aggregate[row](orders.Gte("created_at", since)).
    Stage(bson.M{"$setWindowFields": bson.M{
        "partitionBy": "$user_id",
        "sortBy":      bson.M{"created_at": 1},
        "output":      bson.M{"running": bson.M{"$sum": "$amount", "window": …}},
    }})
```

## Joins

```go
rows, err := mongorepo.Aggregate[revenue](users.Eq("status", "active")).
    Lookup(mongorepo.Lookup{
        From: "orders",
        Let:  bson.M{"uid": "$_id"},
        Pipeline: []bson.M{{"$match": bson.M{"$expr": bson.M{"$and": []bson.M{
            {"$eq": []any{"$user_id", "$$uid"}},
            {"$eq": []any{"$status", "paid"}},
        }}}}},
        As: "orders",
    }).
    AddFields(bson.M{"revenue": bson.M{"$sum": "$orders.total"}}).
    Group("$status", bson.M{
        "total": bson.M{"$sum": "$revenue"},
        "count": bson.M{"$sum": 1},
    }).
    Sort("-total").
    AllowDiskUse().
    All()
```

The correlated form (`Let` + `Pipeline`) filters the joined side on the server.
The simple form (`LocalField`/`ForeignField`) reads every related document and
throws most of them away — fine for a belongs-to, wasteful for a has-many.

`LookupOne` is the belongs-to case: it joins and unwinds, so the result is one
document rather than a one-element array.

## Unwinding

```go
type itemRow struct {
    SKU   string `bson:"_id"`
    Total int64    `bson:"total"`
}

rows, err := mongorepo.Aggregate[itemRow](users.Eq("status", "active")).
    Unwind("items", false).                  // one row per item
    Group("$items.sku", bson.M{"total": bson.M{"$sum": "$items.qty"}}).
    Sort("-total").
    All()
```

`Unwind`'s second argument keeps documents whose array is empty or missing. Pass
`false` when you only want rows that have elements — pass `true` when dropping
them would under-count the result (a "orders per user" report that silently omits
users with no orders is a report that lies).

## Finishers

```go
rows, err := p.All()               // []T
row, err  := p.One()               // *T, appends $limit 1
err        = p.Each(fn)            // streams, never holds the result
err        = p.Into(&anything)     // decode a shape that is not T ($facet rows)
n, err    := p.Count()             // $count, without decoding
page, err := p.Page(opts)          // *core.Page[T] — list + total in one pass
```

`Page` is the one worth knowing about: it runs the list and the total in a single
`$facet`, so a paginated aggregation costs one round trip instead of two
pipelines that can disagree with each other.

That holds for an ordinary page. `$facet` returns everything it produces inside
one document, and a document is capped at 16MB — so a page above 100 rows, which
`PageLimitMax` allows, is fetched with a separate `$count` instead. Two round
trips, and a total read from a collection that may have moved in between. Nothing
to configure; it just stops being a single pass at that size.

```go
page, err := mongorepo.Aggregate[revenue](orders.Gte("created_at", since)).
    Group("$user_id", bson.M{"total": bson.M{"$sum": "$amount"}}).
    Sort("-total").
    Page(c.GetPageOptions())
```

`Each` streams with a cursor. Use it for an export; `All` materialises everything.

## Facets

Several answers from one pass over the data:

```go
type dashboard struct {
    ByStatus []struct {
        Status string `bson:"_id"`
        N      int64    `bson:"n"`
    } `bson:"by_status"`
    Newest []User `bson:"newest"`
}

var out dashboard
err := mongorepo.Aggregate[bson.M](users.Eq("tenant_id", id)).
    Facet(map[string][]bson.M{
        "by_status": {{"$group": bson.M{"_id": "$status", "n": bson.M{"$sum": 1}}}},
        "newest":    {{"$sort": bson.M{"joined": -1}}, {"$limit": 5}},
    }).
    Into(&out)
```

`Into` is what a `$facet` needs: the pipeline's row type no longer describes the
result, because `$facet` produces exactly one document holding several arrays.

## Options

```go
p.AllowDiskUse()
p.Comment("monthly-revenue")
p.Options(core.MongoAggregateOptions{MaxTime: 30 * time.Second, BatchSize: 500})
```

`AllowDiskUse()` is what a `$group` or `$sort` over a large collection needs;
without it Mongo fails at 100MB with "Sort exceeded memory limit" — reliably, the
first time production data outgrows the fixture.

`Comment` shows up in `db.currentOp()`, which is how a long-running pipeline gets
identified by whoever has to decide whether to kill it.

## Materialising a result

```go
mongorepo.Aggregate[bson.M](events.Gte("at", since)).
    Group("$day", bson.M{"n": bson.M{"$sum": 1}}).
    Merge("daily_events", []string{"_id"}, "replace", "insert").
    All()
```

`Out(collection)` replaces a collection wholesale. `Merge(into, on, whenMatched,
whenNotMatched)` updates it in place: `on` names the fields that identify an
existing document, and the last two say what to do when one is found
(`replace`, `merge`, `keepExisting`, `fail`) and when it is not (`insert`,
`discard`, `fail`). Both must be the last stage. A nightly [job](./jobs.md) that refreshes a rollup collection is
usually cheaper — and much more predictable — than a dashboard that aggregates
the raw events on every load.
