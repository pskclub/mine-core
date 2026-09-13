# Recipes

Complete, copyable shapes for the things a service actually does with a
repository. Every example compiles against the API on the previous pages.

## A list endpoint

Filter, search, sort and page, with the tenant scope in one place:

```go
func ListOrders(c core.IHTTPContext) error {
    opts := c.GetPageOptionsWithAllowed("created_at", "total", "status")

    repo := repository.New[Order](c).
        Scopes(OfTenant(c.GetUser().TenantID)).
        Preload("User")

    if status := c.QueryParam("status"); status != "" {
        repo = repo.Where("status = ?", status)
    }
    if from := c.QueryParam("from"); from != "" {
        repo = repo.Where("created_at >= ?", from)
    }
    if opts.Q != "" {
        like := "%" + opts.Q + "%"
        repo = repo.Joins("JOIN users ON users.id = orders.user_id").
            Where("users.name ILIKE ? OR users.email ILIKE ?", like, like)
    }
    if len(opts.OrderBy) == 0 {
        repo = repo.Order("created_at desc")     // the endpoint's default
    }

    page, err := repo.Pagination(opts)
    if err != nil {
        return err
    }
    return c.JSON(http.StatusOK, page)
}
```

Four things worth copying: the tenant scope is a
[scope](./repository-queries.md#scopes) rather than a `Where` somebody can
forget, `order_by` is [allowlisted](./database-pagination.md#allowlisting-what-can-be-sorted),
filters are appended conditionally to the same chain, and the default ordering is
only applied when the caller did not ask for one.

## Get one, with a 404

```go
func GetOrder(c core.IHTTPContext) error {
    order, err := repository.New[Order](c).
        Scopes(OfTenant(c.GetUser().TenantID)).
        Preload("Items").
        FindOne("id = ?", c.Param("id"))

    switch {
    case errors.Is(err, errmsgs.NotFound):
        return c.NewError(err, errmsgs.OrderNotFound)
    case err != nil:
        return err
    }
    return c.JSON(http.StatusOK, order)
}
```

The tenant scope is in the **query**, not in a check after it. An order that
belongs to another tenant then comes back as a 404 rather than a 403 — which is
also the right answer, because a 403 confirms the id exists.

## Create, with a uniqueness answer

```go
func CreateUser(c core.IHTTPContext) error {
    var body struct {
        Email string `json:"email" validate:"required,email"`
        Name  string `json:"name"  validate:"required"`
    }
    if err := c.BindWithValidate(&body); err != nil {
        return err
    }

    u := User{ID: utils.NewUUID(), Email: body.Email, Name: body.Name, Status: "active"}

    if err := repository.New[User](c).Create(&u); err != nil {
        if isUniqueViolation(err) {
            return c.NewError(err, errmsgs.EmailAlreadyExists)   // 409
        }
        return err
    }
    return c.JSON(http.StatusCreated, u)
}

func isUniqueViolation(err error) bool {
    var pgErr *pgconn.PgError
    if errors.As(err, &pgErr) {
        return pgErr.Code == "23505"
    }
    return false
}
```

The unique index is what enforces it; the error is how you find out. A
check-then-insert loses the race, and under concurrent signups that race is not
theoretical.

## Partial update from a PATCH

```go
func UpdateUser(c core.IHTTPContext) error {
    var body struct {
        Name   *string `json:"name"`
        Status *string `json:"status" validate:"omitempty,oneof=active suspended"`
    }
    if err := c.BindWithValidate(&body); err != nil {
        return err
    }

    fields := map[string]any{}
    if body.Name != nil {
        fields["name"] = *body.Name
    }
    if body.Status != nil {
        fields["status"] = *body.Status
    }
    if len(fields) == 0 {
        return c.NewError(nil, errmsgs.BadRequest)
    }

    repo := repository.New[User](c).Where("id = ?", c.Param("id"))
    if err := repo.Updates(fields); err != nil {
        return err
    }

    updated, err := repo.FindOne()
    if err != nil {
        return err
    }
    return c.JSON(http.StatusOK, updated)
}
```

**Pointers in the request struct** are what separate "not sent" from "sent as
empty". A `string` field cannot express the difference, so `""` silently becomes
"clear the name". A **map** in the update is what stops `Updates` skipping a zero
value — see [Writing](./repository-writes.md#updating).

## Soft delete and restore

```go
// delete
err := repository.New[Order](ctx).Where("id = ?", id).Delete()

// list, including deleted
all, err := repository.New[Order](ctx).Unscoped().FindAll()

// restore
err = repository.New[Order](ctx).Unscoped().
    Where("id = ?", id).
    Update("deleted_at", nil)

// purge, for real
err = repository.New[Order](ctx).Where("id = ?", id).HardDelete()
```

A soft-deleted row still occupies a unique index. If "the email is free again"
after deletion, the index has to be partial:

```sql
CREATE UNIQUE INDEX users_email_live ON users (email) WHERE deleted_at IS NULL;
```

## Check-then-act, without the race

```go
// ❌ two requests can both pass the check
u, _ := repo.FindOne("id = ?", id)
if u.Credits >= n {
    repo.Where("id = ?", id).Update("credits", u.Credits-n)
}

// ✅ the condition is in the statement, and the row count is the answer
res := repo.Where("id = ? AND credits >= ?", id, n).
    DB().Update("credits", gorm.Expr("credits - ?", n))
if res.Error != nil {
    return ctx.NewError(res.Error, errmsgs.DBError)
}
if res.RowsAffected == 0 {
    return ctx.NewError(nil, errmsgs.InsufficientCredits)
}
```

`RowsAffected` turns a conditional update into a compare-and-swap: exactly one
concurrent caller sees `1`. It needs `DB()` because the repository's `Update`
returns only an error — which is the right shape for the common case and not for
this one.

## Bulk import

```go
func Import(ctx core.IContext, rows []User) core.IError {
    const batch = 500

    return repository.New[User](ctx).Transaction(func(tx *gorm.DB) error {
        users := repository.NewWithDB[User](ctx, tx)
        for i := 0; i < len(rows); i += batch {
            end := min(i+batch, len(rows))
            if err := users.CreateInBatches(rows[i:end], batch); err != nil {
                return err
            }
        }
        return nil
    })
}
```

For an import that should skip rows it has already seen, let the database decide:

```go
err := repository.New[User](ctx).DB().
    Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "email"}}, DoNothing: true}).
    CreateInBatches(rows, 500).Error
```

Very large imports do **not** want one transaction: it holds locks for the whole
run and a failure at row 900,000 discards everything. Commit per batch, record
progress, and make the import resumable.

## Streaming an export

```go
func ExportOrders(ctx core.IContext, w io.Writer, tenantID string) core.IError {
    cw := csv.NewWriter(w)
    defer cw.Flush()

    var batch []Order
    return repository.New[Order](ctx).
        Where("tenant_id = ?", tenantID).
        Order("id").
        FindInBatches(&batch, 1000, func(tx *gorm.DB, _ int) error {
            for _, o := range batch {
                if err := cw.Write([]string{o.ID, o.Status, fmt.Sprint(o.Total)}); err != nil {
                    return err
                }
            }
            cw.Flush()
            return cw.Error()
        })
}
```

Memory is bounded by the batch, not by the table. `Order("id")` matters: batching
without a stable order can skip or repeat rows.

## Latest N per group

The one preloading cannot do. A window function can:

```go
type recentOrder struct {
    UserID string  `json:"user_id"`
    ID     string  `json:"id"`
    Total  float64 `json:"total"`
}

var rows []recentOrder
err := repository.New[Order](ctx).Raw(&rows, `
    SELECT user_id, id, total FROM (
        SELECT o.*, row_number() OVER (
            PARTITION BY user_id ORDER BY created_at DESC
        ) AS rn
        FROM orders o
        WHERE o.tenant_id = ?
    ) t
    WHERE rn <= 5`, tenantID)
```

Then group them in Go by `UserID`. One query, whatever the number of users.

## Exists, cheaply

```go
ok, err := repository.New[User](ctx).Where("email = ?", email).Exists()
```

`Exists` is a `COUNT`, so it reads every matching row's index entry. When the
answer only has to be "at least one", cap it:

```go
ok, err := repository.New[User](ctx).Where("email = ?", email).Limit(1).Exists()
```

## Reusable scopes worth having

```go
package scopes

func Active(db *gorm.DB) *gorm.DB {
    return db.Where("status = ?", "active")
}

func OfTenant(id string) func(*gorm.DB) *gorm.DB {
    return func(db *gorm.DB) *gorm.DB { return db.Where("tenant_id = ?", id) }
}

func CreatedBetween(from, to time.Time) func(*gorm.DB) *gorm.DB {
    return func(db *gorm.DB) *gorm.DB {
        return db.Where("created_at >= ? AND created_at < ?", from, to)
    }
}

func Search(q string, cols ...string) func(*gorm.DB) *gorm.DB {
    return func(db *gorm.DB) *gorm.DB {
        if q == "" {
            return db
        }
        like := "%" + q + "%"
        conds := db.Session(&gorm.Session{NewDB: true})
        for i, col := range cols {
            if i == 0 {
                conds = conds.Where(col+" ILIKE ?", like)
            } else {
                conds = conds.Or(col+" ILIKE ?", like)
            }
        }
        return db.Where(conds)
    }
}
```

```go
repo.Scopes(scopes.Active, scopes.OfTenant(id), scopes.Search(q, "name", "email"))
```

Column names in `Search` come from your code, never from the request — a scope
that interpolates a caller-supplied column is an injection with extra steps.

## A service that is easy to test

Depend on the narrow interfaces, not on `*Repo[M]`:

```go
type Users interface {
    repository.Reader[User]
    repository.Writer[User]
}

type UserService struct {
    users Users
}

func NewUserService(ctx core.IContext) *UserService {
    return &UserService{users: repository.New[User](ctx)}
}

func (s *UserService) Suspend(id string) core.IError {
    u, err := s.users.FindOne("id = ?", id)
    if err != nil {
        return err
    }
    if u.Status == "suspended" {
        return nil                       // already done — not an error
    }
    return s.users.Updates(map[string]any{"status": "suspended"})
}
```

See [Repository testing](./repository-testing.md) for what to hand it in a test,
and [Service errors](./service-errors.md) for the error values.

## Where the query belongs

| Put it | When |
|---|---|
| in the handler | it is one chain and the endpoint is the only caller |
| in a service method | there is logic around the query, or two callers |
| in a scope | the same condition appears in several queries |
| in raw SQL | the shape does not fit the builder — a window, a CTE, a recursive query |

What not to do is spread one entity's conditions across nine handlers. The first
time a rule changes ("suspended users count as inactive"), the ones nobody
remembered are the bug.
