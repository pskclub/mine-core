# Mongo repository

`mongorepo.Repo[D]` — the generic, Mongo-backed repository. Same shape as the SQL
[repository](./repository.md): the context is bound once at `New`, query methods
take no `ctx`, and the fluent chain is copy-on-write so a base query can be
branched safely.

```go
import "github.com/pskclub/mine-core/v2/mongorepo"

users := mongorepo.New[User](ctx)
user, err := users.Eq("email", email).FindOne()
```

It is a layer over [`core.IMongoDB`](./mongo.md), not a replacement. Everything it
does can still be done by hand, and anything it does not wrap is reachable
through `Collection()`.

Four pages cover it: this one is construction and the rules, then
[querying](./mongo-repository-queries.md),
[writing](./mongo-repository-writes.md) and
[pipelines](./mongo-repository-aggregation.md).

## The document

```go
type User struct {
    ID     string    `bson:"_id,omitempty" json:"id"`
    Email  string    `bson:"email"         json:"email"`
    Name   string    `bson:"name"          json:"name"`
    Status string    `bson:"status"        json:"status"`
    Age    int64       `bson:"age"           json:"age"`
    Joined *time.Time `bson:"joined"       json:"joined"`
}

func (User) CollectionName() string { return "users" }
```

::: warning `_id` ที่ Mongo สร้างเอง อ่านกลับเข้า `ID string` ไม่ได้
ตั้งแต่ driver v2 การ decode `ObjectID` ลง field ชนิด `string` **ถูกปฏิเสธ** เว้นแต่จะเปิด
`Decoder.ObjectIDAsHexString` ซึ่ง core ยังไม่ได้เปิด — เขียนได้ปกติ แต่จะ error ตอนอ่านกลับ:

```
error decoding key _id: decoding an object ID into a string is not supported by default
```

`ID string` จึงใช้ได้เมื่อ **แอปเป็นคนกำหนด id เอง** (uuid/ulid ที่เซ็ตก่อน `Create` —
`_id` ที่เก็บจะเป็น string) ส่วน collection ที่ปล่อยให้ Mongo สร้าง `_id` ให้ ต้องประกาศเป็น
`bson.ObjectID`:

```go
type User struct {
    ID bson.ObjectID `bson:"_id,omitempty" json:"id"`
    // …
}
```
:::

`CollectionName` on the **value** (not the pointer) is what satisfies
`core.IDocument`, so `New[User]` works rather than `New[*User]`. The `_id` tag
with `omitempty` is what lets Mongo generate the id and the repository write it
back.

Two tag sets, two audiences: `bson` is how the document is stored, `json` is what
the API returns. Keeping them separate is what lets a field be renamed on the
wire without a migration — and what stops `_id` from leaking into a response as
`_id`.

## Creating one

```go
users := mongorepo.New[User](ctx)                    // ctx = core.IContext
users := mongorepo.NewIn[User](ctx.DBSMongo("audit"))  // a named connection
users := mongorepo.NewIn[User](tx)                   // inside a transaction
```

`New` takes the connection from `ctx.DBMongo()` and the deadline from `ctx`.
`NewIn` binds a specific `core.IMongoDB` — which is how a transaction handle, or
a second cluster, gets a typed repository.

## Copy-on-write

Every chainable method returns a **new** repository. A base query can be shared,
branched and reused without one branch seeing another's conditions:

```go
base := mongorepo.New[User](ctx).Eq("tenant_id", tenantID)

active, _   := base.Eq("status", "active").Count()
inactive, _ := base.Eq("status", "inactive").Count()
```

A `Repo[D]` value is a *query*, not a connection. Nothing to close, and safe to
hold on a struct.

## Conditions accumulate

Conditions AND together, and two conditions on the **same field** become an
explicit `$and` rather than the second silently replacing the first:

```go
users.Gte("age", 18).Lte("age", 65)
// → {"$and": [{"age": {"$gte": 18}}, {"age": {"$lte": 65}}]}
```

Which is worth knowing because the naive `bson.M` version of the same thing
silently keeps only the last one.

## Errors

Finishers return `core.IError`. Nothing matched is `core.ErrDocumentNotFound`:

```go
user, err := users.Eq("email", email).FindOne()
switch {
case errors.Is(err, core.ErrDocumentNotFound):
    return c.NewError(err, errmsgs.UserNotFound)      // 404
case err != nil:
    return err
}
```

`Count`, `Exists` and `FindAll` never report emptiness as an error — zero
documents is an answer.

## Escape hatches

```go
users.DB()              // core.IMongoDB
users.Collection()      // *mongo.Collection for this document
users.Filter()          // the bson.M built so far
users.FindOptions()     // the sort/limit/skip/projection built so far
users.BulkWrite(models, ordered)
```

`Filter()` is the one to reach for when a query has to be handed to something the
repository does not wrap — a `$lookup` sub-pipeline, a `Watch`, a driver call —
because it means the conditions were still written once.

## Mocking

Depend on the narrow interfaces rather than the struct:

```go
type Users interface {
    mongorepo.Reader[User]
    mongorepo.Writer[User]
}
```

For a unit test, embed `core.IMongoDB` in a fake and implement only the methods
under test — anything else panics rather than quietly returning a zero value.

There is no in-memory Mongo, and there will not be: a fake query engine passes
tests that a real server fails, which is worse than no test. Run against a real
one with `make test-integration` — see [Testing](./testing-integration.md).
