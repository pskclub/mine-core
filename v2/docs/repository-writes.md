# Writing

Creates, updates and deletes on the same chainable repository. Writes are scoped
by whatever conditions the chain carries, which is the whole safety story of this
page: an `Updates` with no `Where` updates every row in the table.

## Creating

```go
users := repository.New[User](ctx)

u := User{Email: "ann@example.com", Name: "Ann", Status: "active"}
if err := users.Create(&u); err != nil {
    return err
}
u.ID          // filled in by the driver — pass a pointer, that is why
u.CreatedAt   // set by GORM
```

Many rows at once, in chunks so one statement does not exceed the driver's
parameter limit:

```go
err := users.CreateInBatches(rows, 500)
```

A unique-constraint collision comes back as a `DATABASE_ERROR` carrying the
driver's message. When the collision is a real answer — "this email is already
registered" — check for it before or catch it after, but do not let it reach the
client as a 500:

```go
exists, err := users.Where("email = ?", email).Exists()
if err != nil {
    return err
}
if exists {
    return c.NewError(nil, errmsgs.EmailAlreadyExists)
}
```

> A check-then-insert is a race under concurrency. When correctness matters, put
> a unique index on the column and treat the insert error as the authoritative
> answer — the index is the only thing that actually enforces it.

## Updating

Three methods, and the difference matters:

```go
// one column, on everything the chain matches
err := users.Where("id = ?", id).Update("status", "active")

// several columns — a map means exactly what it says
err = users.Where("id = ?", id).Updates(map[string]any{
    "status":   "active",
    "verified": false,          // false is written
})

// a struct: only non-zero fields are written
err = users.Where("id = ?", id).Updates(User{Status: "active"})
```

`Updates` with a **struct** skips zero values. `Verified: false`, `Count: 0`,
`Name: ""` are all indistinguishable from "not set" and are silently left alone.
Use a **map** whenever a zero value is a value you mean.

`Save` writes the whole record, by its own primary key:

```go
u, err := users.FindOne("id = ?", id)
u.Status = "active"
err = users.Save(u)          // every column, including the ones you did not touch
```

Which makes `Save` the wrong tool for a partial update loaded with `Select` — the
columns that were never read are written back as zeroes.

### Update-or-create

```go
u := User{Email: email}
err := users.Where("email = ?", email).
    Attrs(User{Status: "trial"}).      // only used if it has to create
    FindOneOrCreate(&u)

err = users.Where("email = ?", email).
    Assign(User{LastSeen: time.Now()}). // applied either way
    FindOneOrCreate(&u)

err = users.Where("email = ?", email).FindOneOrInit(&u)   // does not persist
```

For a true database-level upsert, drop to GORM's `ON CONFLICT`:

```go
err := users.DB().Clauses(clause.OnConflict{
    Columns:   []clause.Column{{Name: "email"}},
    DoUpdates: clause.AssignmentColumns([]string{"name", "updated_at"}),
}).Create(&u).Error
```

That one is atomic; `FindOneOrCreate` is a read followed by a write and can lose
a race.

### Atomic arithmetic

Read-modify-write loses updates under concurrency. Let the database do the
arithmetic:

```go
err := users.Where("id = ?", id).
    Update("login_count", gorm.Expr("login_count + ?", 1))
```

## Deleting

```go
err := users.Where("id = ?", id).Delete()          // soft, if the model has DeletedAt
err = users.Where("id = ?", id).HardDelete()       // permanent, always
err = users.Delete("status = ?", "stale")          // condition inline
```

Without a `gorm.DeletedAt` field on the model, `Delete` **is** a hard delete —
there is nowhere to record the deletion.

A soft delete keeps the row and every foreign key that points at it, which is
what makes an audit trail possible and also what makes "the email is still taken"
surprising. A unique index on a soft-deleted table usually wants to be partial:

```sql
CREATE UNIQUE INDEX users_email_live ON users (email) WHERE deleted_at IS NULL;
```

> GORM refuses a delete with no conditions at all (`ErrMissingWhereClause`), so a
> forgotten `Where` fails rather than emptying the table. Do not rely on it as
> the only guard — build the condition, then the delete.

## Associations

```go
u, _ := users.FindOne("id = ?", id)

err := users.Association(u, "Roles").Append(&adminRole)
err = users.Association(u, "Roles").Replace(&adminRole, &auditorRole)
err = users.Association(u, "Roles").Delete(&adminRole)
err = users.Association(u, "Roles").Clear()
n   := users.Association(u, "Roles").Count()
```

`Association` returns GORM's `*gorm.Association`, so its errors are raw `error`
values rather than `core.IError` — wrap them at the call site:

```go
if err := users.Association(u, "Roles").Append(&role); err != nil {
    return ctx.NewError(err, errmsgs.DBError)
}
```

## Writing inside a transaction

Anything that is more than one statement and must be all-or-nothing belongs in
a transaction — see [Transactions](./database-transactions.md):

```go
err := users.Transaction(func(tx *gorm.DB) error {
    txUsers  := repository.NewWithDB[User](ctx, tx)
    txOrders := repository.NewWithDB[Order](ctx, tx)

    if err := txUsers.Where("id = ?", id).Update("credits", gorm.Expr("credits - ?", n)); err != nil {
        return err
    }
    return txOrders.Create(&order)
})
```

## Bulk writes

```go
// one statement, no round trip per row
err := users.Where("last_seen < ?", cutoff).Updates(map[string]any{"status": "dormant"})

// or raw, when the shape does not fit
err = users.Exec(`
    UPDATE users SET status = 'dormant'
    WHERE last_seen < ? AND status = 'active'`, cutoff)
```

A bulk update holds locks for as long as it runs. On a large table, batch it by
primary key range rather than issuing one statement that blocks writers for a
minute:

```go
last := ""
for {
    var ids []string
    if err := repository.New[User](ctx).
        Where("id > ? AND last_seen < ? AND status = ?", last, cutoff, "active").
        Order("id").Limit(1000).Pluck("id", &ids); err != nil {
        return err
    }
    if len(ids) == 0 {
        return nil
    }
    if err := repository.New[User](ctx).
        Where("id IN ?", ids).
        Updates(map[string]any{"status": "dormant"}); err != nil {
        return err
    }
    last = ids[len(ids)-1]
}
```

Each batch is its own short transaction, the loop is resumable, and nothing holds
a lock for longer than a thousand rows take.

## How many rows did that touch?

The repository's write methods return only an error, because that is the right
shape for the common case. When the row count **is** the answer — a
compare-and-swap, "did this actually delete anything" — take it from `DB()`:

```go
res := repository.New[Order](ctx).
    Where("id = ? AND status = ?", id, "pending").
    DB().Update("status", "cancelled")

if res.Error != nil {
    return ctx.NewError(res.Error, errmsgs.DBError)
}
if res.RowsAffected == 0 {
    // either no such order, or it was not pending any more
    return ctx.NewError(nil, errmsgs.OrderNotCancellable)
}
```

That pattern is the single most useful thing on this page: putting the
precondition in the `WHERE` and reading `RowsAffected` makes a check-then-act
atomic, with no transaction and no lock. See
[Check-then-act](./repository-recipes.md#check-then-act-without-the-race).

## Timestamps

GORM fills `CreatedAt` and `UpdatedAt` automatically when the fields exist, in
UTC (`NowFunc` is set by `core.NewDatabase`).

`UpdatedAt` is **not** touched by a raw `Exec`, and it is touched by `Updates`
even when nothing changed. If "when did this last change" has to be true, do not
also write the column by hand from two places.

```go
// bumps updated_at
repo.Where("id = ?", id).Updates(map[string]any{"status": "active"})

// does not
repo.Exec("UPDATE users SET status = 'active' WHERE id = ?", id)
```

## Hooks

GORM calls `BeforeCreate`, `AfterUpdate` and friends on the model if they are
defined. They are useful for one thing — deriving a field from the row itself:

```go
func (u *User) BeforeCreate(tx *gorm.DB) error {
    if u.ID == "" {
        u.ID = utils.NewUUID()
    }
    return nil
}
```

Keep them to that. A hook that sends an email, publishes an event or queries
another table turns every write into an invisible side effect, runs inside
whatever transaction happens to be open, and cannot be tested without a database.
Business logic belongs in a service method where the call site can see it.

## Next

- [Relations](./repository-relations.md) — writing parents and children together
- [Transactions](./database-transactions.md)
- [Recipes](./repository-recipes.md) — imports, upserts, soft-delete restore
- [Testing](./repository-testing.md)
