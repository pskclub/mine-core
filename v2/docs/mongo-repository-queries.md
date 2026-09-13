# Repository: querying

Everything on this page is chainable and copy-on-write. Nothing runs until a
finisher.

```go
users := mongorepo.New[User](ctx)

list, err := users.Eq("status", "active").Sort("-joined").Limit(20).FindAll()
first, err := users.Eq("status", "active").First()   // lowest _id
latest, err := users.Eq("status", "active").Last()   // highest _id
user, err := users.Eq("email", email).FindOne()
```

## Conditions

```go
users.Eq("status", "active")
users.Ne("status", "banned")
users.In("role", "admin", "owner")
users.NotIn("role", "guest")
users.Gt("age", 18)      // Gte / Lt / Lte
users.Between("joined", from, to)
users.HasField("deleted_at", false)
users.Regex("email", "^ann", "i")
users.Search(q, "name", "email")   // case-insensitive substring, term escaped
users.ByID(id)                     // parses a hex id into an ObjectID
users.ElemMatch("items", bson.M{"sku": "A-1", "qty": 2})
users.HasAll("tags", "beta", "vip")
users.HasSize("tags", 3)
users.Or(bson.M{"role": "admin"}, bson.M{"role": "owner"})
users.Not(bson.M{"status": "banned"})
users.Where(bson.M{"$expr": ...})  // anything else, as it is
```

Conditions AND together, and two conditions on the **same field** become an
explicit `$and` rather than the second silently replacing the first:

```go
users.Gte("age", 18).Lte("age", 65)
// → {"$and": [{"age": {"$gte": 18}}, {"age": {"$lte": 65}}]}
```

`Search` escapes the term before building the regex, so a user typing `.*` gets
a search for the literal characters rather than a scan of the collection. A
hand-written `Regex` does not — escape it yourself, or use `Search`.

## Nested fields and arrays

Mongo addresses anything inside a document with a dotted path, and every method
that takes a field name takes one — filters, sorts, projections, updates,
`Pluck`, index keys. Given:

```go
type User struct {
    ID      string   `bson:"_id,omitempty"`
    Profile Profile  `bson:"profile"`
    Items   []Item   `bson:"items"`
    Tags    []string `bson:"tags"`
}

type Profile struct {
    City string `bson:"city"`
    Age  int64    `bson:"age"`
}

type Item struct {
    SKU string `bson:"sku"`
    Qty int64    `bson:"qty"`
}
```

```go
users.Eq("profile.city", "BKK")          // subdocument field
users.Gte("profile.age", 18)
users.Eq("items.sku", "A-1")             // matches if *any* element has it
users.Eq("tags", "beta")                 // an array contains a value
users.Sort("-profile.age")
users.Select("profile.city", "items.sku")
users.Omit("profile.national_id")
users.Update("profile.city", "CNX")      // $set on the nested field only
```

### The trap

Two dotted conditions on the same array are satisfied by two **different**
elements. `ElemMatch` demands that one element satisfy all of them:

```go
// matches a user whose items contain SKU "A-1" *and* (some other) item of qty 2
users.Eq("items.sku", "A-1").Eq("items.qty", 2)

// matches a user with one item that is both
users.ElemMatch("items", bson.M{"sku": "A-1", "qty": 2})
```

This is not a quirk of the repository — it is how Mongo matches arrays, and it is
the single most common source of "the query returns documents that obviously do
not match".

Other array conditions:

```go
users.HasAll("tags", "beta", "vip")   // contains every one of them
users.HasSize("tags", 3)              // exactly three elements
users.HasField("profile.city", true)  // the path is present
```

### Reading values out of arrays

`Pluck` follows a path across arrays the way Mongo does — one value per element,
flattened:

```go
var cities []string
err := users.Eq("status", "active").Pluck("profile.city", &cities)

var skus []string
err = users.Pluck("items.sku", &skus)   // every sku of every matching user
```

For anything shaped differently — the items themselves, one row per element,
grouped or counted — unwind in a
[pipeline](./mongo-repository-aggregation.md#unwinding):

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

## Shaping

```go
users.Sort("-joined", "name")   // "-" is descending; entries accumulate in order
users.Limit(20).Skip(40)
users.Select("name", "email")   // only these fields
users.Omit("password")          // everything but these
users.Hint("email_1")           // force an index
users.Options(core.MongoFindOptions{Collation: …, MaxTime: …})
```

`Select` and `Omit` are the same `$project` from opposite ends — do not use both.
A `Select` that leaves out a field returns it as its zero value, which matters if
the struct is then written back with `Save`.

## Counting, plucking, paging, streaming

```go
n, err := users.Eq("status", "active").Count()
ok, err := users.Eq("email", email).Exists()

var emails []string
err = users.Eq("status", "active").Pluck("email", &emails)

var statuses []string
err = users.Distinct("status", &statuses)

page, err := users.Eq("status", "active").Sort("-joined").
    Pagination(c.GetPageOptions())     // → *core.Page[User]

err = users.Eq("status", "active").Each(func(u User) error {
    return export(u)                   // streams; never holds the result set
})
```

The chain's `Sort` is the **default** ordering for `Pagination`; an `OrderBy` on
the request wins over it. So an endpoint sets a sensible default and the caller
can still change it:

```go
page, err := users.Eq("tenant_id", id).
    Sort("-joined").                              // used when order_by is absent
    Pagination(c.GetPageOptionsWithAllowed("joined", "name"))
```

`Each` streams with a cursor and holds one batch at a time, so an export over a
million documents costs the same memory as one over a thousand. `FindAll` does
not — it materialises everything.

## Escape hatches

```go
users.Filter()          // the bson.M built so far
users.FindOptions()     // the sort/limit/skip/projection built so far
users.Collection()      // *mongo.Collection
users.DB()              // core.IMongoDB
```

`Filter()` is what lets a chain be reused by something that is not the repository
— a `$lookup` sub-pipeline, a `Watch`, a raw driver call — so the conditions
still live in one place:

```go
cur, err := users.Eq("status", "active").Collection().
    Find(users.DB().Context(), users.Filter())
```
