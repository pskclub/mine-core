# Querying

Every method here is chainable and copy-on-write: it returns a new repository and
leaves the one it was called on untouched. Nothing runs until a **finisher**
(`FindOne`, `FindAll`, `Count`, …) is called.

```go
users := repository.New[User](ctx)

list, err := users.
    Where("status = ?", "active").
    Order("created_at desc").
    Limit(20).
    FindAll()
```

## Conditions

```go
users.Where("email = ?", email)
users.Where("age >= ? AND age < ?", 18, 65)
users.Where("status IN ?", []string{"active", "trial"})
users.Where("name LIKE ?", "%"+q+"%")
users.Where(User{Status: "active"})              // struct: non-zero fields only
users.Where(map[string]any{"status": "active", "verified": false})

users.Where("status = ?", "active").Or("role = ?", "admin")
users.Not("status = ?", "banned")
```

Always use `?` placeholders. `Where(fmt.Sprintf("email = '%s'", email))` is an
injection, and the fact that it works in a test is exactly why it survives to
production.

> **struct vs map.** A struct condition ignores zero values, so
> `Where(User{Verified: false})` matches *everything* — the `false` is
> indistinguishable from "not set". A map means what it says. Use a map whenever
> a zero value is a real value.

## Finishers

```go
user, err := users.Where("id = ?", id).FindOne()   // *User, ordered by PK, NOT_FOUND if none
user, err  = users.Where("id = ?", id).Take()      // *User, no implicit order
user, err  = users.Order("id").Last()              // *User, last by PK

list, err := users.Where("status = ?", "active").FindAll()   // []User, empty is not an error
n, err    := users.Where("status = ?", "active").Count()
ok, err   := users.Where("email = ?", email).Exists()
```

Conditions can also be passed straight to a finisher, which is shorter for a
one-condition read:

```go
user, err := users.FindOne("email = ?", email)
list, err := users.FindAll("status = ?", "active")
```

`FindOne` applies an implicit `ORDER BY` on the primary key so the result is
stable; `Take` does not, which makes it marginally cheaper when the condition
already selects one row (a unique column).

## Ordering, limiting, projecting

```go
users.Order("created_at desc")
users.Order("status asc, created_at desc")
users.Limit(20).Offset(40)

users.Select("id", "email")            // only these columns
users.Select("id, email")              // same thing
users.Omit("password_hash")            // everything except
users.Distinct("status")
users.Group("status").Having("count(*) > ?", 10)
```

A `Select` that omits a column leaves that field at its zero value in the
returned struct — which is fine for a list endpoint and a trap if the value is
then written back with `Save`. Use `Updates` for partial writes
([writing](./repository-writes.md#updating)).

## Relations

```go
type User struct {
    // ...
    Profile  Profile  `gorm:"foreignKey:UserID"`
    Orders   []Order  `gorm:"foreignKey:UserID"`
}
```

**`Preload` loads relations in separate queries:**

```go
users.Preload("Profile").FindAll()
users.Preload("Orders", "status = ?", "paid").FindAll()   // conditional
users.Preload("Orders.Items").FindAll()                   // nested
```

**`Joins` joins in the same query** — use it when you need to *filter* on the
related table:

```go
users.Joins("JOIN orders ON orders.user_id = users.id").
    Where("orders.total > ?", 1000).
    Distinct("users.*").
    FindAll()

users.InnerJoins("Profile").Where("Profile.city = ?", "BKK").FindAll()
```

The rule of thumb: `Preload` to *fetch* related rows, `Joins` to *filter* by
them. Preloading a has-many and then filtering the parents in Go pulls the whole
table across the wire to throw most of it away.

## Scopes

A scope is a named, reusable piece of a query. It is the cure for the same three
conditions being spelled slightly differently in nine places:

```go
func Active(db *gorm.DB) *gorm.DB {
    return db.Where("status = ?", "active").Where("deleted_at IS NULL")
}

func OfTenant(id string) func(*gorm.DB) *gorm.DB {
    return func(db *gorm.DB) *gorm.DB { return db.Where("tenant_id = ?", id) }
}

list, err := users.Scopes(Active, OfTenant(tenantID)).Order("id desc").FindAll()
```

Tenant scoping in particular belongs in a scope rather than in each call site:
one forgotten `Where` is a cross-tenant data leak, and it will not look like a
bug in review.

`Repo.Scopes` runs the functions there and then, unlike gorm's own `Scopes`,
which keeps them on the statement until the query executes. The difference shows
up behind `Pagination`: it counts before it finds, and `Count` strips `ORDER BY`
and rewrites `SELECT` while building the count query — so a *deferred* scope adds
its clause afterwards, and the count comes out as

```sql
SELECT count(*) FROM users ORDER BY name desc
```

which sqlite runs and postgres rejects with 42803. Applied on the spot, a scope
is indistinguishable from calling the builder yourself, and the count query is
the plain count it should be.

Two things follow from that. A scope's clauses land where the call sits in the
chain rather than after everything else, so mixing `Order()` on the chain with a
scope that also orders puts the columns in call order. And a scope that reads
`db.Statement.Dest` sees it at build time — `Statement.Model` is unaffected,
`New` sets it before any scope can run. A `nil` in the list is skipped, so a
scope slice assembled from optional filters does not have to be compacted first.

## Projections

`Scan` reads into any struct — it does not have to be the model:

```go
type statusCount struct {
    Status string `json:"status"`
    Total  int64  `json:"total"`
}

var stats []statusCount
err := users.
    Select("status, count(*) as total").
    Group("status").
    Scan(&stats)
```

`Pluck` reads one column into a slice:

```go
var emails []string
err := users.Where("status = ?", "active").Pluck("email", &emails)
```

## Large result sets

`FindAll` materialises everything. Past a few thousand rows that is a memory
spike waiting for a bad day; read in batches instead:

```go
var batch []User
err := users.Where("status = ?", "active").
    FindInBatches(&batch, 500, func(tx *gorm.DB, n int) error {
        for _, u := range batch {
            if err := export(u); err != nil {
                return err     // stops the iteration
            }
        }
        return nil
    })
```

`Rows` and `Row` return the `database/sql` handles for streaming a query the
repository has no shape for. Close what you open.

## Raw SQL

```go
var n int64
err := users.Raw(&n, "SELECT count(*) FROM users WHERE age > ?", 18)

type row struct {
    Month string
    Total int64
}
var rows []row
err = users.Raw(&rows, `
    SELECT to_char(created_at, 'YYYY-MM') AS month, count(*) AS total
    FROM users
    WHERE created_at >= ?
    GROUP BY 1 ORDER BY 1`, since)

err = users.Exec("UPDATE users SET status = ? WHERE last_seen < ?", "dormant", cutoff)
```

`Raw` scans into `dest`; `Exec` runs a statement that returns nothing. Both take
placeholders, and both still run on the request's context.

## Soft deletes and `Unscoped`

A model with a `gorm.DeletedAt` field is soft-deleted, and every query silently
excludes deleted rows. `Unscoped` turns that off:

```go
all, err := users.Unscoped().FindAll()                        // including deleted
gone, err := users.Unscoped().Where("deleted_at IS NOT NULL").FindAll()
```

## Going below the repository

```go
err := users.
    Where("status = ?", "active").
    DB().                                     // *gorm.DB, ctx-bound and scoped
    Clauses(clause.Locking{Strength: "UPDATE"}).
    Find(&list).Error
```

Anything GORM can do is one `DB()` away — window functions, `ON CONFLICT`,
`FOR UPDATE`, dialect-specific clauses. The repository does not try to own them.

## Reading part of a row

Three ways to avoid materialising whole models, in increasing order of how little
they transfer:

```go
// the model, minus some columns
users.Select("id", "email").FindAll()          // []User, other fields zeroed

// a projection struct — does not have to be the model at all
var rows []struct {
    Email string
    City  string
}
users.Select("users.email, profiles.city").
    Joins("JOIN profiles ON profiles.user_id = users.id").
    Scan(&rows)

// one column
var emails []string
users.Pluck("email", &emails)
```

`Scan` does not apply the model's soft-delete scope when you also change the
table with `Table()`, so a projection over `Table("users")` sees deleted rows.
Add the condition yourself when that matters.

## Common query shapes

```go
// IN, from a slice
users.Where("status IN ?", []string{"active", "trial"})

// NOT IN, safely handling an empty slice
if len(excluded) > 0 {
    users = users.Where("id NOT IN ?", excluded)
}

// a nullable column
users.Where("deleted_at IS NULL")
users.Where("verified_at IS NOT NULL")

// a date range — half-open, so a row is never in two ranges
users.Where("created_at >= ? AND created_at < ?", from, to)

// case-insensitive match (postgres)
users.Where("lower(email) = lower(?)", email)

// JSON column (postgres)
users.Where("meta->>'plan' = ?", "pro")

// an OR group that does not leak out of its parentheses
users.Where("tenant_id = ?", id).
    Where(ctx.DB().Where("role = ?", "admin").Or("role = ?", "owner"))
```

That last one is worth reading twice. A bare `.Or()` on the outer chain applies to
the **whole** condition, so `Where(tenant).Or(role)` matches every admin in every
tenant. Nesting the alternatives in their own `Where` is what produces
`tenant_id = ? AND (role = ? OR role = ?)`.

An empty slice is the other trap: `Where("id IN ?", []string{})` becomes
`IN (NULL)` and matches nothing, which is right for `IN` and wrong for `NOT IN`.
Guard it.

## Counting

```go
n, err := users.Where("status = ?", "active").Count()
```

`Count` ignores `Limit`, `Offset` and `Order`, so it can be called on the same
chain a listing uses. It does **not** ignore `Select` — a `Select` with an
aggregate turns the count into something else. Branch before projecting:

```go
base := users.Where("status = ?", "active")
total, _ := base.Count()
rows, _  := base.Select("id", "email").Limit(20).FindAll()
```

Copy-on-write is what makes those two independent.

## Next

- [Writing](./repository-writes.md) — create, update, delete, upsert
- [Relations](./repository-relations.md) — preload, joins, N+1
- [Transactions](./database-transactions.md)
- [Pagination](./database-pagination.md)
- [Recipes](./repository-recipes.md) — complete endpoint and service shapes
