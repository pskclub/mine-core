# MongoDB

`core.IMongoDB` — a MongoDB abstraction over the official driver. `ctx.DBMongo()`
returns a handle bound to the request context, so methods take no `ctx` and a
cancelled request cancels the query it started.

```go
mongo, _ := core.NewMongoDB(env)
app, _   := core.NewApp(env, core.WithMongo("default", mongo))

// ...then, anywhere a context reaches:
var user User
err := ctx.DBMongo().FindOne(&user, "users", bson.M{"email": email})
```

## The two layers

| Layer | What it is | Use it when |
|---|---|---|
| [`mongorepo`](./mongo-repository.md) | generic, typed, chainable | almost always |
| [`core.IMongoDB`](./mongo-queries.md) | thin helpers over the driver, `bson.M` filters | the query does not fit the repository |

```go
// repository — typed, chainable
users, err := mongorepo.New[User](ctx).Eq("status", "active").Sort("-joined").FindAll()

// driver layer — same connection, same request context
var list []User
err := ctx.DBMongo().Find(&list, "users", bson.M{"status": "active"})
```

Neither layer hides the driver. Filters are `bson.M` either way, `Collection()`
is part of the interface, and anything the helpers do not wrap — GridFS, an
Atlas `$search`, a raw cursor — is one method call below.

## Driver v2

The driver is `go.mongodb.org/mongo-driver/**v2**`. Because core deliberately
does not hide it, the import path is yours too — a service on the v1 driver does
not compile against this, and the two drivers' types are not interchangeable
even where the names match.

What a service has to change:

```go
// imports
-import "go.mongodb.org/mongo-driver/bson"
-import "go.mongodb.org/mongo-driver/bson/primitive"
+import "go.mongodb.org/mongo-driver/v2/bson"

// the package `primitive` is gone; its types live in `bson`
-ID primitive.ObjectID `bson:"_id,omitempty"`
+ID bson.ObjectID      `bson:"_id,omitempty"`

-oid := primitive.NewObjectID()
+oid := bson.NewObjectID()
```

`bson.M`, `bson.D` and `bson.A` keep their names and their meaning, so filters
and pipelines written against them need only the import change.

Two things behave differently rather than just moving:

- **`MaxTime` is a context deadline now.** `MongoFindOptions.MaxTime` and friends
  still work — core applies them by bounding the operation's context, which the
  driver still sends to the server as `maxTimeMS`. Nothing to change.
- **`Distinct` decodes.** It no longer goes through `[]any`, so a typed
  destination (`&[]string{}`) is decoded directly.

## What this section covers

| Page | |
|---|---|
| [Connection & configuration](./mongo-connection.md) | replica sets, TLS, pools, timeouts, named connections |
| [Queries (driver layer)](./mongo-queries.md) | find, count, distinct, writes, object ids, duplicate keys |
| [Aggregation](./mongo-aggregation.md) | pipelines through `IMongoDB` |
| [Repository](./mongo-repository.md) | documents, construction, the fluent chain |
| [Repository: querying](./mongo-repository-queries.md) | conditions, nested fields, arrays, shaping, paging |
| [Repository: writing](./mongo-repository-writes.md) | create, update, atomic operators, array updates |
| [Repository: pipelines](./mongo-repository-aggregation.md) | the typed `$lookup`/`$group`/`$facet` builder |
| [Repository: recipes](./mongo-repository-recipes.md) | complete endpoint and service shapes |
| [Indexes & change streams](./mongo-indexes.md) | `EnsureIndex`, TTL, partial, multikey, `Watch` |
| [Transactions](./mongo-transactions.md) | what a replica set buys you |
| [Query logging](./mongo-logging.md) | how much of the driver's traffic reaches the log |

## The document

```go
type User struct {
    ID     string    `bson:"_id,omitempty" json:"id"`
    Email  string    `bson:"email"         json:"email"`
    Name   string    `bson:"name"          json:"name"`
    Status string    `bson:"status"        json:"status"`
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

`CollectionName` on the **value** satisfies `core.IDocument`, which is what makes
`mongorepo.New[User]` work rather than `New[*User]`. The `_id` tag with
`omitempty` is what lets Mongo generate the id and the repository write it back.

The driver layer needs no `CollectionName` — it takes the collection as a string
argument — but a document used with both wants one anyway, so the name lives in
one place.

## No configuration, no silence

Without any `DB_MONGO_*`, `ctx.DBMongo()` is still not nil: every call fails with
`MONGO_DISABLED`. Unlike the cache it does **not** degrade quietly, because a read
that silently returns nothing and a write that silently goes nowhere are both
worse than a clear failure.

```go
if !ctx.DBMongo().Enabled() {
    // an honest branch, when a path can genuinely work without Mongo
}
```

## Errors

| Condition | What comes back |
|---|---|
| nothing matched | error wrapping `core.ErrDocumentNotFound` (404, `NOT_FOUND`) |
| unique index collision | error wrapping `core.ErrDuplicateKey` (**409**) |
| a hex id that is not one | 400, `INVALID_ID` |
| no configuration | error wrapping `core.ErrMongoDisabled` (`MONGO_DISABLED`) |

```go
if _, err := m.InsertOne("users", user); errors.Is(err, core.ErrDuplicateKey) {
    return c.NewError(err, errmsgs.EmailAlreadyExists)   // an answer, not a 500
}
```

Writes report **what they did** rather than only whether they failed — `Matched`,
`Modified`, `UpsertedID` — so "matched nothing" is a case the caller can answer
instead of a silence.

## Best practices

- **ออกแบบ schema ถึงแม้มันจะ schemaless** — collection ที่แต่ละ document หน้าตาไม่
  เหมือนกัน คือ collection ที่ query ไม่ได้ในอีกหกเดือน
- **index ตาม query ที่มีจริง** และตรวจด้วย `explain` — Mongo ยินดีสแกนทั้ง collection
  ให้เงียบๆ จนกว่าข้อมูลจะโต
- **อย่าให้ document โตไม่จำกัด** — array ที่ append ไปเรื่อยๆ ชนเพดาน 16 MB วันหนึ่ง
  แน่นอน แยกเป็น collection ลูกตั้งแต่ตอนที่ยังเลือกได้
- **`*time.Time` และตัวเลข 64 บิตเป็นค่าตั้งต้นของ document** เหมือนฝั่ง SQL —
  field ที่ยังไม่มีค่าใน Mongo คือ field ที่ไม่มีอยู่จริง pointer จึงตรงกับความจริงมากกว่า
- **projection เสมอเมื่อ document ใหญ่** — อ่านเฉพาะ field ที่ใช้
- **ใช้ [`mongorepo`](./mongo-repository.md) แทนการประกอบ `bson.M` เอง** — typed operator
  ทำให้ field ที่พิมพ์ผิดเป็น compile error แทนที่จะเป็น query ที่คืน 0 แถว
- **pipeline ที่คืนข้อมูลมากต้องระวังเพดาน BSON** — pagination ของ core สลับกลยุทธ์ให้
  ตามขนาดหน้าแล้ว แต่ pipeline ที่เขียนเองต้องคิดเอง
- **transaction ต้องมี replica set** — และควรใช้เมื่อจำเป็นจริง ไม่ใช่เป็นนิสัยติดมาจาก SQL
- **mongo ไม่ degrade เงียบ** — ไม่ได้ตั้งค่าแล้วทุกคำสั่ง fail พร้อมบอกว่า config ไหนหาย
