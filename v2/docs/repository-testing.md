# Testing repository code

Three levels, and most code wants the middle one.

| Level | Backend | Proves |
|---|---|---|
| substitute the interface | none | the *logic* around the query |
| run the query | sqlite in memory | the query compiles and returns what you expect |
| run the query | real postgres + real migrations | the schema, constraints and dialect agree |

## Substituting the repository

`Reader[M]` and `Writer[M]` exist so a service can be tested with no database at
all:

```go
type Users interface {
    repository.Reader[User]
    repository.Writer[User]
}

type UserService struct{ users Users }
```

```go
type fakeUsers struct {
    repository.Reader[User]      // embedded: anything not implemented panics
    repository.Writer[User]

    found   *User
    findErr core.IError
    updated any
}

func (f *fakeUsers) FindOne(conds ...any) (*User, core.IError) {
    return f.found, f.findErr
}

func (f *fakeUsers) Updates(values any) core.IError {
    f.updated = values
    return nil
}

func TestSuspendIsIdempotent(t *testing.T) {
    f := &fakeUsers{found: &User{ID: "1", Status: "suspended"}}
    svc := &UserService{users: f}

    require.NoError(t, svc.Suspend("1"))
    require.Nil(t, f.updated, "an already-suspended user should not be written")
}
```

**Embedding the interface rather than implementing it** is the trick worth
keeping: a method the test does not implement panics, loudly, instead of
returning a zero value that makes an assertion pass for the wrong reason.

Use this for branching logic, error mapping and guard clauses. Do **not** use it
to test the query itself — a fake cannot tell you whether the SQL is right.

## Running against sqlite

The fast suite. `coretest` builds an `App` with an in-memory database, so the
whole repository path runs for real:

```go
func TestFindsActiveUsers(t *testing.T) {
    ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&User{}, &Order{}))

    seed(t, ctx,
        User{ID: "1", Email: "a@b.com", Status: "active"},
        User{ID: "2", Email: "c@d.com", Status: "suspended"},
    )

    got, err := repository.New[User](ctx).Where("status = ?", "active").FindAll()
    require.NoError(t, err)
    require.Len(t, got, 1)
    require.Equal(t, "1", got[0].ID)
}
```

Every test gets its own database, so nothing has to be cleaned up between them
and tests can run in parallel. See
[Testing: Database](./testing-database.md) for the exact options and how the
schema is built.

### What sqlite will not catch

⚠️ sqlite has no partial indexes, no `ILIKE`, no UUID type, and `AutoMigrate`
builds the schema from your **structs** rather than from your migrations. A suite
that only ever sees sqlite passes on constraints postgres rejects.

| Written for postgres | On sqlite |
|---|---|
| `ILIKE` | a syntax error |
| partial unique index | silently skipped by `AutoMigrate` |
| `now() AT TIME ZONE` | not a function |
| `ON CONFLICT … WHERE` | different support |
| `FOR UPDATE` | accepted and meaningless — writers are serialised anyway |

So: sqlite for the loop you run while writing code, postgres before you push.

## Running against postgres

```sh
make test-integration
```

```go
func options(extra ...coretest.Option) []coretest.Option {
    return append([]coretest.Option{
        coretest.WithAutoMigrate(allModels()...),   // sqlite: fast
        coretest.WithMigrations(migrationsDir()),   // postgres: the real schema
    }, extra...)
}
```

`WithMigrations` is what makes the postgres round worth running: it applies the
**same SQL production runs**, so a constraint your code violates fails here
rather than in staging. Without it you are testing a schema nobody deploys.

Anything that must be tested on postgres:

- unique and check constraints, and the error your code maps them to
- partial indexes and soft-delete uniqueness
- `ILIKE`, full-text search, window functions, CTEs
- `FOR UPDATE` and anything about concurrent writers
- migrations themselves

## Testing a transaction

The two things worth asserting are that it **commits everything** and that it
**rolls back everything**:

```go
func TestTransferRollsBackOnFailure(t *testing.T) {
    ctx := coretest.NewContext(t,
        coretest.WithAutoMigrate(&Account{}, &Entry{}),
        coretest.WithoutDefaultTransaction(),
    )

    seed(t, ctx, Account{ID: "a", Balance: 100}, Account{ID: "b", Balance: 0})

    err := Transfer(ctx, "a", "b", 500)     // more than the balance
    require.Error(t, err)

    // nothing moved, and no entry was written
    a, _ := repository.New[Account](ctx).FindOne("id = ?", "a")
    require.EqualValues(t, 100, a.Balance)

    n, _ := repository.New[Entry](ctx).Count()
    require.Zero(t, n)
}
```

The failure this catches is the common one: a write that used `ctx.DB()` instead
of `tx` and therefore survived the rollback. It only shows up when you assert on
the *other* tables, not just the one that failed.

```go
func TestTransferCommits(t *testing.T) {
    // ... seed ...
    require.NoError(t, Transfer(ctx, "a", "b", 40))

    a, _ := repository.New[Account](ctx).FindOne("id = ?", "a")
    b, _ := repository.New[Account](ctx).FindOne("id = ?", "b")
    require.EqualValues(t, 60, a.Balance)
    require.EqualValues(t, 40, b.Balance)

    n, _ := repository.New[Entry](ctx).Count()
    require.EqualValues(t, 2, n)
}
```

### Concurrency

Lock behaviour needs postgres — sqlite serialises writers, so a test for
`FOR UPDATE` passes there whether or not the lock is taken:

```go
//go:build integration

func TestConcurrentWithdrawalsDoNotOverdraw(t *testing.T) {
    // ... postgres app, account with 100 ...

    var wg sync.WaitGroup
    errs := make([]error, 10)
    for i := range errs {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            errs[i] = Withdraw(ctx, "a", 20)
        }(i)
    }
    wg.Wait()

    var ok int
    for _, err := range errs {
        if err == nil {
            ok++
        }
    }
    require.Equal(t, 5, ok, "exactly five withdrawals of 20 fit in 100")

    a, _ := repository.New[Account](ctx).FindOne("id = ?", "a")
    require.EqualValues(t, 0, a.Balance)
}
```

That test fails loudly on a read-decide-write implementation and passes on one
that puts the condition in the statement — see
[Check-then-act](./repository-recipes.md#check-then-act-without-the-race).

## Asserting on the queries, not the rows

Sometimes the thing under test is *how many* queries ran — an N+1 regression.
Turn the SQL log on and count it:

```sh
APP_DB_LOG_LEVEL=info go test -run TestListOrders -v ./...
```

There is no assertion helper for this; reading the log once when you write the
endpoint, and again when it gets slow, catches almost everything. See
[Query logging](./database-logging.md).

## Seeding

Keep it boring and explicit. A helper that inserts and fails the test is enough:

```go
func seed[T core.IModel](t *testing.T, ctx core.IContext, rows ...T) {
    t.Helper()
    for i := range rows {
        require.NoError(t, repository.New[T](ctx).Create(&rows[i]))
    }
}
```

Shared fixtures that every test depends on become the thing nobody dares change.
Prefer each test creating exactly the rows it asserts on — see
[Fixtures](./testing-fixtures.md).
