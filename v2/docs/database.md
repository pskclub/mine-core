# Database (SQL)

GORM-backed SQL. One pooled connection is opened at startup and lives on the
`App`; `ctx.DB()` hands each request a `*gorm.DB` already bound to that request's
context, so cancellation, deadlines and the trace follow every query without
anybody passing a `ctx` around.

```go
db, _ := core.NewDatabase(env)              // once, at startup
app, _ := core.NewApp(env, core.WithSQL("default", db))

// ...then, anywhere a context reaches:
user, err := repository.New[User](ctx).Where("id = ?", id).FindOne()
```

## The two layers

| Layer | What it is | Use it when |
|---|---|---|
| [Repository](./repository.md) | generic, chainable, returns `core.IError` | almost always |
| `ctx.DB()` | the raw `*gorm.DB`, ctx-bound | the query does not fit the repository |

The repository is not a wall around GORM. It wraps the query builder and the
finishers, and `DB()` on any chain returns the underlying `*gorm.DB` **with the
scope you built so far** — so dropping down a level is a method call, not a
rewrite.

```go
// repository
users, err := repository.New[User](ctx).Where("status = ?", "active").FindAll()

// raw, same connection, same request context
var users []User
err := ctx.DB().Where("status = ?", "active").Find(&users).Error
```

## What this section covers

| Page | |
|---|---|
| [Connections & configuration](./database-connections.md) | drivers, DSNs, the pool, replicas and named connections |
| [Repository](./repository.md) | models, construction, copy-on-write, error semantics |
| [Repository: querying](./repository-queries.md) | conditions, joins, projections, scopes, raw SQL |
| [Repository: writing](./repository-writes.md) | create, update, delete, upsert, batches, `RowsAffected` |
| [Repository: relations](./repository-relations.md) | preload, joins, associations, and how to see an N+1 |
| [Transactions](./database-transactions.md) | scope, locking, retries, isolation, the outbox |
| [Pagination](./database-pagination.md) | `PageOptions`, `Page[T]`, ordering, keyset paging |
| [Repository: recipes](./repository-recipes.md) | complete endpoint and service shapes |
| [Repository: testing](./repository-testing.md) | fakes, sqlite, postgres, transactions, concurrency |
| [Best practices](./database-patterns.md) | index, N+1, ขอบเขต transaction, migration, checklist |
| [Query logging](./database-logging.md) | how much SQL reaches the log stream, and how to change it |
| [Schema & migrations](./migrations.md) | why the framework does not migrate for you |

## Models

A model is a plain struct with GORM tags and a `TableName`, which is all
`core.IModel` asks for:

```go
type User struct {
    ID        string    `json:"id"         gorm:"column:id;primaryKey"`
    Email     string    `json:"email"      gorm:"column:email"`
    Status    string    `json:"status"     gorm:"column:status"`
    CreatedAt *time.Time `json:"created_at" gorm:"column:created_at"`
    DeletedAt gorm.DeletedAt `json:"-"     gorm:"column:deleted_at;index"`
}

func (User) TableName() string { return "users" }
```

The struct **describes** a table that a migration already created — it does not
create one. See [Schema & migrations](./migrations.md) for why, and what owns the
schema instead.

`DeletedAt` is what makes `Delete()` a soft delete; without it, `Delete()` and
`HardDelete()` do the same thing.

## Errors

Nothing in this section returns a raw `error`. Queries return `core.IError`, so a
failure carries a status and a code all the way to the HTTP layer, and a missing
row is a named outcome rather than a driver-specific sentinel:

```go
user, err := repository.New[User](ctx).Where("id = ?", id).FindOne()
if errors.Is(err, errmsgs.NotFound) {
    return c.NewError(err, errmsgs.UserNotFound)   // a 404
}
if err != nil {
    return err                                     // already an IError
}
```

## Notes

- Timestamps default to UTC (`NowFunc`), so what is written and what is read back
  do not depend on the server's timezone.
- The pool lives on the `App`; `app.Shutdown()` closes it. Nothing per-request
  opens or closes a connection.
- Every query runs on the request's context, so a client that hangs up cancels
  the query it started.
