# Repository

`repository.Repo[M]` — a generic, GORM-backed repository. The context is bound
once at `New`, so query methods never take a `ctx`, and the fluent chain is
copy-on-write so a base query can be branched safely.

```go
import "github.com/pskclub/mine-core/v2/repository"

user, err := repository.New[User](ctx).Where("email = ?", email).FindOne()
```

This page is construction and the rules that apply everywhere. The rest of the
API is split by what you are doing:

| Page | |
|---|---|
| [Querying](./repository-queries.md) | conditions, joins, projections, scopes, raw SQL |
| [Writing](./repository-writes.md) | create, update, delete, upsert, batches, `RowsAffected` |
| [Relations](./repository-relations.md) | preload, joins, associations, N+1 |
| [Transactions](./database-transactions.md) | scope, locking, retries, the outbox |
| [Pagination](./database-pagination.md) | `PageOptions`, `Page[T]`, keyset paging |
| [Recipes](./repository-recipes.md) | complete endpoint and service shapes |
| [Testing](./repository-testing.md) | fakes, sqlite, postgres, transactions |

## The model

`M` must satisfy `core.IModel`, which is one method:

```go
type User struct {
    ID        string         `json:"id"         gorm:"column:id;primaryKey"`
    Email     string         `json:"email"      gorm:"column:email"`
    Name      string         `json:"name"       gorm:"column:name"`
    Status    string         `json:"status"     gorm:"column:status"`
    CreatedAt *time.Time     `json:"created_at" gorm:"column:created_at"`
    UpdatedAt *time.Time     `json:"updated_at" gorm:"column:updated_at"`
    DeletedAt gorm.DeletedAt `json:"-"          gorm:"column:deleted_at;index"`
}

func (User) TableName() string { return "users" }
```

`TableName` on the **value** is what makes `New[User]` work rather than
`New[*User]`. Everything else is ordinary GORM tagging — the struct describes a
table a migration already created, it does not create one
([why](./migrations.md)).

## Creating one

```go
repo := repository.New[User](ctx)                 // ctx = core.IContext

repo := repository.NewWithDB[User](ctx, tx)       // inside a transaction
repo := repository.NewWithDB[User](ctx, ctx.DBS("readonly"))   // a named connection
```

`New` takes the connection from `ctx.DB()` and the deadline from `ctx`.
`NewWithDB` swaps the connection but keeps the context — which is what makes it
the right way to bind a transaction, since the transaction handle still has to
respect the request being cancelled.

Constructing a repository is cheap: it allocates a struct and a GORM session. It
is not a resource, so there is nothing to close and no reason to keep one on a
service struct rather than making one where it is used.

## Copy-on-write

Every chainable method returns a **new** repository over an isolated GORM
session. Branches never contaminate each other:

```go
base := repository.New[User](ctx).Where("tenant_id = ?", tenantID)

active, _   := base.Where("status = ?", "active").Count()
inactive, _ := base.Where("status = ?", "inactive").Count()
// both scoped to the tenant; neither sees the other's condition
```

In v1 the same code needed `NewSession()` in the right places, and forgetting one
leaked conditions into the next query — silently, and usually only under load.
Here it is the default.

The practical consequence: a `Repo[M]` value is a *query*, not a connection. Pass
one around, store one on a struct, reuse one across goroutines — a chain built
from it cannot change it.

```go
// a shared base query is safe as a field
type UserService struct{ users *repository.Repo[User] }
```

## Errors

Every finisher returns `core.IError`, so nothing has to be translated on the way
to the HTTP layer. A missing row is `errmsgs.NotFound`:

```go
user, err := repo.Where("id = ?", id).FindOne()
switch {
case errors.Is(err, errmsgs.NotFound):
    return c.NewError(err, errmsgs.UserNotFound)     // 404, your message
case err != nil:
    return err                                        // already an IError
}
```

`Count`, `Exists` and `FindAll` do **not** report emptiness as an error — zero
rows is an answer. Only `FindOne`, `Take` and `Last` can miss.

## The method surface

**Chainable** (copy-on-write): `Where`, `Or`, `Not`, `Order`, `Group`, `Having`,
`Limit`, `Offset`, `Select`, `Omit`, `Distinct`, `Preload`, `Joins`,
`InnerJoins`, `Table`, `Unscoped`, `Attrs`, `Assign`, `Clauses`, `Scopes`.

`Scopes` applies its functions immediately rather than deferring them to
execution the way gorm's own does — see [Scopes](repository-queries.md#scopes)
for why, and for the two places the difference is visible.

**Read**: `FindOne`, `Take`, `Last`, `FindAll`, `FindInBatches`, `Count`,
`Exists`, `Pluck`, `Scan`, `Row`, `Rows`, `Pagination`.

**Write**: `Create`, `CreateInBatches`, `Save`, `Update`, `Updates`, `Delete`,
`HardDelete`, `FindOneOrInit`, `FindOneOrCreate`, `Association`.

**Raw & tx**: `Raw`, `Exec`, `Transaction`.

**Escape hatches**: `DB`, `WithContext`.

## The escape hatch — `DB()`

For any GORM feature the repository does not wrap, `DB()` returns the underlying
`*gorm.DB` bound to the context **and** carrying the scope built so far:

```go
err := repository.New[User](ctx).
    Where("status = ?", "active").
    DB().                                    // *gorm.DB, ctx-bound and scoped
    Clauses(clause.OnConflict{DoNothing: true}).
    CreateInBatches(rows, 100).Error
```

That is the design: the repository covers the common path and hands the rest back
rather than becoming a second, worse GORM.

## Custom timeout

Long work started from a request context dies when the request does. When that is
wrong — an export that must finish, a job kicked off from a handler — bind
another context:

```go
bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

rows, err := repo.WithContext(bg).FindAll()
```

This is an escape hatch, not a habit. Detaching a query from the request means a
client that hangs up no longer stops the work it paid for.

## Interface segregation

Depend on the narrow interfaces rather than the concrete `*Repo[M]`, and a unit
test needs no database:

```go
type Users interface {
    repository.Reader[User]
    repository.Writer[User]
}

type UserService struct{ users Users }   // *repository.Repo[User] satisfies it
```

See [Mocks & fakes](./testing-mock.md) for what to substitute in a test, and
[Testing: Database](./testing-database.md) for running the real thing against
sqlite or postgres.
