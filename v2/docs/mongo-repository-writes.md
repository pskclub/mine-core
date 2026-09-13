# Repository: writing

Writes are scoped by whatever conditions the chain carries. That is the whole
safety story of this page: `UpdateAll` with no conditions updates the whole
collection.

## Creating

```go
users := mongorepo.New[User](ctx)

u := User{Email: "ann@example.com", Name: "ann"}
if err := users.Create(&u); err != nil {
    return err
}
u.ID   // filled in — the repository writes the generated id back, as GORM does
```

```go
ids, err := users.CreateMany([]User{a, b, c})   // ids in order
```

`Create` takes a pointer for exactly one reason: so the id it just generated is
usable without a second read.

## Updating

```go
res, err := users.ByID(id).Updates(bson.M{"name": "bob"})     // $set
res, err = users.ByID(id).Update("name", "bob")               // one field
res, err = users.ByID(id).Update("profile.city", "CNX")       // a nested field
res, err = users.Eq("status", "new").UpdateAll(bson.M{"status": "active"})
res, err = users.Eq("email", email).Upsert(bson.M{"name": "ann"})

if res.Matched == 0 {
    // nothing matched — the caller decides whether that is a 404
}
```

A plain map is wrapped in `$set`. A map that already speaks in operators is
passed through untouched, so `Updates(bson.M{"$inc": …})` does what it says:

```go
users.ByID(id).Updates(bson.M{"$inc": bson.M{"seats": -1}, "$set": bson.M{"updated_at": now}})
```

`Updates` and `Update` touch the **first** match; `UpdateAll` touches every one.
The naming is deliberate — the plural that means "many documents" is the one that
reads as a bulk operation.

### What the result tells you

```go
type MongoUpdateResult struct {
    Matched    int64
    Modified   int64
    Upserted   int64
    UpsertedID string
}
```

`Matched > 0 && Modified == 0` means the document was found and already had those
values — a successful no-op, not a failure. Telling that apart from "no such
document" is why writes return a result at all.

## Replacing

```go
err = users.Save(&u)          // replace the whole document
```

`Save` replaces the document matching the scope, and inserts it when there is
none. With no scope it matches on the document's **own** `_id` — and with no id
either, it is a `Create`.

Which makes `Save` the wrong tool after a `Select`: fields that were never read
are written back as zeroes. Use `Updates` for a partial write.

## Deleting

```go
n, err := users.ByID(id).DeleteOne()
n, err = users.Eq("status", "stale").Delete()
n, err = users.DeleteAll()                  // the deliberate "empty it" version
```

`Delete` **without a filter is refused** with a 400 (`UNSCOPED_DELETE`). Emptying
a collection is almost always a filter that was forgotten rather than one that
was meant, and the moment to find out is before it runs, not after.

## Atomic operators

Read-modify-write loses updates when two requests do it at once. These do the
arithmetic in the database:

```go
users.ByID(id).Inc("login_count", 1)
users.ByID(id).Inc("credits", -amount)
users.ByID(id).Push("tags", "beta")
users.ByID(id).Push("tags", "beta", "vip")   // $each
users.ByID(id).AddToSet("tags", "beta")      // no duplicates
users.ByID(id).Pull("tags", "beta")
users.ByID(id).Unset("reset_token", "reset_sent_at")
```

`Push` appends unconditionally; `AddToSet` skips values already present. A "tags"
array that grows a duplicate every time a form is submitted twice is the bug
`AddToSet` exists to prevent.

## Claiming a document

A `FindOne` followed by an `Updates` is a race — two workers read the same queued
job and both start it. `FindOneAndUpdate` is one operation, so exactly one wins:

```go
job, err := jobs.Eq("status", "queued").Sort("created_at").
    FindOneAndUpdate(bson.M{"status": "running", "worker": workerID})
if errors.Is(err, core.ErrDocumentNotFound) {
    // the queue is empty
}
job.Status   // "running" — the repository returns the document *after* the change
```

`Sort` decides which document is claimed when several match, which is what turns
this into a queue pop. The chain's `Sort` is used when the options do not carry
one.

> The repository defaults `ReturnNew` to **true** (the document after the
> change), which is the useful answer when claiming. The driver layer
> ([`IMongoDB.FindOneAndUpdate`](./mongo-queries.md#atomic-find-and-modify))
> keeps Mongo's own default of *before*. Pass
> `core.MongoFindModifyOptions{ReturnNew: false}` when you want the previous
> value.

```go
job, err = jobs.Eq("status", "queued").FindOneAndDelete()
```

## Find-or-create

```go
u := User{Email: email, Status: "trial"}
found, err := users.Eq("email", email).FindOneOrCreate(&u)
```

It is a read followed by a write, so it can lose a race. When the uniqueness
actually matters, put a unique index on the field and let the insert error be the
answer — the index is the only thing that enforces it:

```go
users.EnsureIndexes(core.MongoIndex{Keys: []string{"email"}, Unique: true})

if err := users.Create(&u); errors.Is(err, core.ErrDuplicateKey) {
    return c.NewError(err, errmsgs.EmailAlreadyExists)   // a 409
}
```

## Updating inside an array

The positional operators need to know which elements they apply to, which is what
`ArrayFilters` is for:

```go
// every element
users.ByID(id).Updates(bson.M{"items.$[].seen": true})

// only the elements matching a filter
users.ByID(id).Updates(
    bson.M{"items.$[low].qty": 1},
    core.MongoUpdateOptions{ArrayFilters: []any{bson.M{"low.qty": bson.M{"$lt": 1}}}},
)

// the one element the *query* matched
users.ByID(id).ElemMatch("items", bson.M{"sku": "A-1"}).
    Updates(bson.M{"items.$.qty": 5})
```

The three are genuinely different: `$[]` is all, `$[name]` is those matching the
named filter, and `$` is the single element the query's `ElemMatch` selected. The
last needs the query to have matched an element — a plain `Eq("items.sku", …)`
does not qualify.

Whole-array operations:

```go
users.ByID(id).Push("items", Item{SKU: "A-1", Qty: 1})
users.ByID(id).Pull("items", bson.M{"sku": "A-1"})
users.ByID(id).Inc("items.$[cur].qty", 1)
```

## Bulk writes

Many different operations in one round trip:

```go
res, err := users.BulkWrite([]mongo.WriteModel{
    mongo.NewUpdateOneModel().
        SetFilter(bson.M{"email": "a@b.com"}).
        SetUpdate(bson.M{"$set": bson.M{"status": "active"}}).
        SetUpsert(true),
    mongo.NewDeleteOneModel().SetFilter(bson.M{"email": "old@b.com"}),
}, false)   // unordered
```

Unordered is faster and keeps going past a failure — the right default when the
operations are independent.

## Transactions

```go
err := users.Transaction(func(tx *mongorepo.Repo[User]) error {
    if err := tx.Create(&user); err != nil {
        return err
    }
    _, err := tx.ByID(id).Inc("seats", -1)
    return err
})
```

Use the `tx` repository inside — the outer one is not in the transaction. Across
collections, take the handle from `core.IMongoDB`. See
[Transactions](./mongo-transactions.md).
