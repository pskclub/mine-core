# Recipes

Complete shapes for what a service actually does with the Mongo repository.

## A list endpoint

```go
func ListOrders(c core.IHTTPContext) error {
    opts := c.GetPageOptionsWithAllowed("created_at", "total", "status")

    repo := mongorepo.New[Order](c).Eq("tenant_id", c.GetUser().TenantID)

    if status := c.QueryParam("status"); status != "" {
        repo = repo.Eq("status", status)
    }
    if opts.Q != "" {
        repo = repo.Search(opts.Q, "code", "customer.name")
    }

    page, err := repo.Sort("-created_at").Pagination(opts)
    if err != nil {
        return err
    }
    return c.JSON(http.StatusOK, page)
}
```

`Search` escapes the term, so a user typing `.*` searches for those characters
rather than scanning the collection. The chain's `Sort` is the default; the
request's `order_by` wins when it is present.

## Get one, with a 404

```go
func GetOrder(c core.IHTTPContext) error {
    order, err := mongorepo.New[Order](c).
        Eq("tenant_id", c.GetUser().TenantID).
        ByID(c.Param("id")).
        FindOne()

    switch {
    case errors.Is(err, core.ErrDocumentNotFound):
        return c.NewError(err, errmsgs.OrderNotFound)
    case err != nil:
        return err
    }
    return c.JSON(http.StatusOK, order)
}
```

The tenant condition is in the query, so another tenant's id is a 404 rather than
a 403 — and `ByID` parses the hex id, which is what stops a string filter from
silently matching nothing.

## Tenant scoping in one place

There are no GORM-style scopes here, but a base query is a value — build it once
and branch it:

```go
func orders(c core.IHTTPContext) *mongorepo.Repo[Order] {
    return mongorepo.New[Order](c).Eq("tenant_id", c.GetUser().TenantID)
}
```

```go
paid, _  := orders(c).Eq("status", "paid").Count()
recent,_ := orders(c).Sort("-created_at").Limit(10).FindAll()
```

Copy-on-write is what makes this safe: neither branch can see the other's
conditions.

## Check-then-act, without the race

```go
// ❌ two requests can both pass the check
o, _ := orders.ByID(id).FindOne()
if o.Status == "pending" {
    orders.ByID(id).Update("status", "cancelled")
}

// ✅ the precondition is in the filter, and Matched is the answer
res, err := orders.ByID(id).Eq("status", "pending").
    Updates(bson.M{"status": "cancelled", "cancelled_at": time.Now()})
if err != nil {
    return err
}
if res.Matched == 0 {
    return c.NewError(nil, errmsgs.OrderNotCancellable)
}
```

A single-document write is already atomic in Mongo, so putting the condition in
the filter needs no transaction and no lock.

## Claiming work from a queue collection

```go
job, err := mongorepo.New[Job](ctx).
    Eq("status", "queued").
    Lte("run_at", time.Now()).
    Sort("run_at").
    FindOneAndUpdate(bson.M{
        "status":     "running",
        "worker":     workerID,
        "started_at": time.Now(),
    })
switch {
case errors.Is(err, core.ErrDocumentNotFound):
    return nil            // nothing to do
case err != nil:
    return err
}
// job is the document *after* the change — the repository defaults ReturnNew
```

One operation, so exactly one worker wins. `Sort` decides which document is
claimed. Index `{status: 1, run_at: 1}` or every poll is a collection scan.

## Counters that do not lose updates

```go
users.ByID(id).Inc("login_count", 1)
users.ByID(id).Inc("credits", -amount)
users.ByID(id).AddToSet("device_ids", deviceID)      // no duplicates
users.ByID(id).Pull("device_ids", deviceID)
users.ByID(id).Unset("reset_token", "reset_sent_at")
```

Never read-modify-write a number. `$inc` is atomic; `u.Credits - n` written back
is not.

To refuse going negative, put the condition in the filter and read `Matched`:

```go
res, _ := users.ByID(id).Gte("credits", n).Inc("credits", -n)
if res.Matched == 0 {
    return c.NewError(nil, errmsgs.InsufficientCredits)
}
```

## Bulk import

```go
func Import(ctx core.IContext, rows []User) core.IError {
    const batch = 1000
    users := mongorepo.New[User](ctx)

    for i := 0; i < len(rows); i += batch {
        end := min(i+batch, len(rows))
        if _, err := users.CreateMany(rows[i:end]); err != nil {
            return err
        }
    }
    return nil
}
```

To skip rows that already exist, upsert them unordered in one round trip:

```go
models := make([]mongo.WriteModel, 0, len(rows))
for _, r := range rows {
    models = append(models, mongo.NewUpdateOneModel().
        SetFilter(bson.M{"email": r.Email}).
        SetUpdate(bson.M{"$setOnInsert": r}).
        SetUpsert(true))
}
res, err := mongorepo.New[User](ctx).BulkWrite(models, false)
```

`$setOnInsert` writes only when the document is created, so re-running the import
does not overwrite edits made since. Unordered keeps going past a failure.

## Streaming an export

```go
err := mongorepo.New[Order](ctx).
    Eq("tenant_id", tenantID).
    Sort("_id").
    Each(func(o Order) error {
        return cw.Write([]string{o.ID, o.Status, fmt.Sprint(o.Total)})
    })
```

`Each` uses a cursor and holds one batch, so memory does not grow with the
collection. `FindAll` would materialise all of it.

## A dashboard in one round trip

```go
type dashboard struct {
    ByStatus []struct {
        Status string `bson:"_id"`
        N      int64    `bson:"n"`
    } `bson:"by_status"`
    Newest []Order `bson:"newest"`
    Total  []struct {
        N int64 `bson:"n"`
    } `bson:"total"`
}

var out dashboard
err := mongorepo.Aggregate[bson.M](orders(c)).
    Facet(map[string][]bson.M{
        "by_status": {{"$group": bson.M{"_id": "$status", "n": bson.M{"$sum": 1}}}},
        "newest":    {{"$sort": bson.M{"created_at": -1}}, {"$limit": 5}},
        "total":     {{"$count": "n"}},
    }).
    Into(&out)
```

Three answers, one pass over the data, one round trip. See
[Pipelines](./mongo-repository-aggregation.md).

## Embedding versus referencing

The modelling decision that decides how much of this page you need:

| Embed | Reference |
|---|---|
| the child is only ever read with the parent | the child is queried on its own |
| the pair must stay consistent | the child is large, or unbounded in number |
| the child list is bounded | the child is shared by several parents |

Embedding is what makes a single-document write atomic — no transaction needed.
An order with its line items embedded updates consistently by construction; the
same data in two collections needs
[a transaction](./mongo-transactions.md) and a replica set.

A 16MB document limit is the hard ceiling, and an array that grows without bound
hits problems long before it.

## Indexes at startup

```go
func ensureIndexes(ctx core.IContext) error {
    if err := mongorepo.New[User](ctx).EnsureIndexes(
        core.MongoIndex{Keys: []string{"email"}, Unique: true,
            Partial: bson.M{"deleted_at": nil}},
        core.MongoIndex{Keys: []string{"tenant_id", "-created_at"}},
    ); err != nil {
        return err
    }
    return mongorepo.New[Job](ctx).EnsureIndexes(
        core.MongoIndex{Keys: []string{"status", "run_at"}},
        core.MongoIndex{Keys: []string{"finished_at"}, TTL: 7 * 24 * time.Hour},
    )
}
```

Every query in this page's recipes has an index behind it. Adding them at the
same time as the query is the only reliable moment — see
[Indexes](./mongo-indexes.md).

## A service that is easy to test

```go
type Orders interface {
    mongorepo.Reader[Order]
    mongorepo.Writer[Order]
}

type OrderService struct{ orders Orders }
```

Fakes cover the logic; a real Mongo covers the queries. There is no in-memory
Mongo on purpose — see [the repository page](./mongo-repository.md#mocking).
