# Relations

Relations are where a repository either saves you a lot of code or quietly
generates a hundred queries per request. This page is the difference.

## Declaring them

```go
type User struct {
    ID     string `gorm:"column:id;primaryKey"`
    Name   string `gorm:"column:name"`

    Profile  Profile   `gorm:"foreignKey:UserID"`            // has one
    Orders   []Order   `gorm:"foreignKey:UserID"`            // has many
    Roles    []Role    `gorm:"many2many:user_roles"`         // many to many
}

func (User) TableName() string { return "users" }

type Profile struct {
    ID     string `gorm:"column:id;primaryKey"`
    UserID string `gorm:"column:user_id"`
    City   string `gorm:"column:city"`
}

func (Profile) TableName() string { return "profiles" }

type Order struct {
    ID     string  `gorm:"column:id;primaryKey"`
    UserID string  `gorm:"column:user_id"`
    Status string  `gorm:"column:status"`
    Total  float64 `gorm:"column:total"`

    User  *User      `gorm:"foreignKey:UserID"`   // belongs to
    Items []OrderItem `gorm:"foreignKey:OrderID"`
}

func (Order) TableName() string { return "orders" }
```

The tags describe columns a migration already created — declaring a relation does
not create a foreign key. See [Schema & migrations](./migrations.md).

## Preload: fetch the related rows

`Preload` runs a **second query** per relation and stitches the results together
in Go:

```go
users, err := repository.New[User](ctx).
    Where("status = ?", "active").
    Preload("Profile").
    Preload("Orders").
    FindAll()
```

```sql
SELECT * FROM users WHERE status = 'active';
SELECT * FROM profiles WHERE user_id IN (…);
SELECT * FROM orders   WHERE user_id IN (…);
```

Three queries, not one per user. That `IN (…)` is the whole point — it is what
makes `Preload` the cure for N+1 rather than a cause of it.

### Conditional preloads

```go
// only the paid orders
users.Preload("Orders", "status = ?", "paid").FindAll()

// with ordering and a limit expressed as a function
users.Preload("Orders", func(db *gorm.DB) *gorm.DB {
    return db.Where("status = ?", "paid").Order("created_at desc")
}).FindAll()
```

> A `Limit` inside a `Preload` limits the **whole** second query, not the rows per
> parent. `Preload("Orders", func(db *gorm.DB) *gorm.DB { return db.Limit(5) })`
> over 20 users returns 5 orders in total, distributed arbitrarily. "The five
> latest orders per user" needs a window function — see
> [Recipes](./repository-recipes.md#latest-n-per-group).

### Nested preloads

```go
users.Preload("Orders.Items").FindAll()
users.Preload("Orders.Items.Product").FindAll()
users.Preload("Orders", "status = ?", "paid").Preload("Orders.Items").FindAll()
```

Each level is another query. `Orders.Items.Product` on a list endpoint is four
queries — fine — but it is also potentially a very large amount of data. Preload
what the response actually renders, not what the struct happens to contain.

### Preloading everything

```go
users.Preload(clause.Associations).FindAll()
```

Convenient in a script, a bad default in a handler: it loads every relation
including ones added later by somebody else, so the query set of your endpoint
changes when a struct does.

## Joins: filter by the related table

`Joins` puts the relation in the **same** query, which is what you need when the
*parent* rows are selected by something on the child:

```go
// users who have at least one large order
users, err := repository.New[User](ctx).
    Joins("JOIN orders ON orders.user_id = users.id").
    Where("orders.total > ?", 1000).
    Distinct("users.*").
    FindAll()
```

`Distinct` matters: a join to a has-many multiplies the parent rows, so a user
with three large orders appears three times without it.

For a belongs-to or has-one, GORM can join by relation name and populate the
struct in the same query:

```go
orders, err := repository.New[Order](ctx).
    InnerJoins("User").
    Where("User.status = ?", "active").
    FindAll()
// orders[i].User is populated — one query, no second round trip
```

### Preload or Joins?

| Want | Use |
|---|---|
| the parent's related rows, for rendering | `Preload` |
| to filter parents by a child's column | `Joins` |
| a belongs-to / has-one you also want to display | `InnerJoins("Rel")` — one query |
| a has-many you also want to display | `Preload` — a join would multiply rows |

The mistake worth naming: preloading a has-many and then filtering the parents in
Go. That pulls every child row across the wire in order to throw most of them
away, and it gets slower every month.

```go
// ❌ loads every order of every user, then filters in memory
users, _ := repo.Preload("Orders").FindAll()
for _, u := range users {
    if hasLargeOrder(u.Orders) { … }
}

// ✅ the database decides, and sends only the answer
users, _ := repo.Joins("JOIN orders ON orders.user_id = users.id").
    Where("orders.total > ?", 1000).Distinct("users.*").FindAll()
```

## Aggregating a relation

Counting or summing children does not need the children:

```go
type userRow struct {
    ID     string  `json:"id"`
    Name   string  `json:"name"`
    Orders int64   `json:"orders"`
    Spent  float64 `json:"spent"`
}

var rows []userRow
err := repository.New[User](ctx).
    Select(`users.id, users.name,
            count(orders.id) as orders,
            coalesce(sum(orders.total), 0) as spent`).
    Joins("LEFT JOIN orders ON orders.user_id = users.id AND orders.status = ?", "paid").
    Group("users.id, users.name").
    Scan(&rows)
```

`LEFT JOIN` keeps users with no orders; `coalesce` turns their `NULL` sum into
`0`. Both are easy to leave out and both change the answer.

## N+1: how to see it

The classic shape is a loop that queries:

```go
// ❌ 1 + N queries
users, _ := repository.New[User](ctx).FindAll()
for i := range users {
    users[i].Profile, _ = repository.New[Profile](ctx).
        FindOne("user_id = ?", users[i].ID)
}
```

It is invisible in review and obvious in the log. Turn SQL logging on and count:

```sh
APP_DB_LOG_LEVEL=info
```

If one request produces a burst of near-identical statements, that is an N+1 —
see [Query logging](./database-logging.md). The fix is almost always a `Preload`,
or one query with `IN`:

```go
// ✅ two queries, whatever N is
users, _ := repository.New[User](ctx).Preload("Profile").FindAll()
```

Doing it by hand when the relation is not declared:

```go
ids := make([]string, len(users))
for i, u := range users {
    ids[i] = u.ID
}
profiles, _ := repository.New[Profile](ctx).Where("user_id IN ?", ids).FindAll()

byUser := make(map[string]Profile, len(profiles))
for _, p := range profiles {
    byUser[p.UserID] = p
}
```

## Writing relations

`Create` on a parent with populated children inserts the children too:

```go
u := User{
    Name:    "ann",
    Profile: Profile{City: "BKK"},
    Orders:  []Order{{Total: 100}, {Total: 250}},
}
err := repository.New[User](ctx).Create(&u)   // one transaction, all of it
```

Convenient, and easy to trigger by accident: loading a user with `Preload` and
then calling `Save` re-writes the children as well. When you mean to touch only
the parent, say so:

```go
err := repository.New[User](ctx).Omit(clause.Associations).Save(&u)
```

## The association API

For adding to or removing from a relation without loading it:

```go
u, _ := repository.New[User](ctx).FindOne("id = ?", id)
assoc := repository.New[User](ctx).Association(u, "Roles")

err := assoc.Append(&adminRole)                   // add
err = assoc.Replace(&adminRole, &auditorRole)     // set the whole set
err = assoc.Delete(&adminRole)                    // remove one
err = assoc.Clear()                               // remove all
n := assoc.Count()
```

On a `many2many` these write the join table and nothing else, which is what you
want. On a has-many, `Delete` **nulls the foreign key** rather than deleting the
child row — a detail that surprises everybody once.

`Association` returns GORM's type, so its errors are plain `error`. Wrap them:

```go
if err := assoc.Append(&role); err != nil {
    return ctx.NewError(err, errmsgs.DBError)
}
```

## Relations inside a transaction

Everything above works on a transactional repository — bind it with `NewWithDB`:

```go
err := repository.New[User](ctx).Transaction(func(tx *gorm.DB) error {
    users := repository.NewWithDB[User](ctx, tx)

    if err := users.Create(&u); err != nil {
        return err
    }
    return users.Association(&u, "Roles").Append(&defaultRole)
})
```

See [Transactions](./database-transactions.md).
