# Transactions

A transaction is how several statements become one outcome. `Transaction` runs
`fn` on a transaction bound to the repository's context, commits when it returns
`nil`, and rolls back when it returns an error or panics.

```go
err := repository.New[User](ctx).Transaction(func(tx *gorm.DB) error {
    users  := repository.NewWithDB[User](ctx, tx)
    orders := repository.NewWithDB[Order](ctx, tx)

    if err := users.Where("id = ?", userID).
        Update("credits", gorm.Expr("credits - ?", order.Total)); err != nil {
        return err
    }
    return orders.Create(&order)
})
if err != nil {
    return err          // already a core.IError
}
```

> ⚠️ **`Transaction` เขียนทับ `code` ของ error ที่เกิดข้างใน** — error ที่ closure คืนออกมา
> (ที่ไม่ใช่ not-found) ถูกห่อเป็น `DATABASE_ERROR` เสมอ **status เดิมยังอยู่** และ
> `errors.Is` ยังใช้ได้ผ่าน cause แต่ `errmsgs.BadRequest` ที่คืนจากในนั้นจะถึง client
> ด้วย code `DATABASE_ERROR` ไม่ใช่ `BAD_REQUEST`
>
> ถ้า frontend branch จาก `code` ให้ตรวจเงื่อนไขทางธุรกิจ **ก่อน** เข้า transaction หรือ
> คืนมันออกมาเป็นค่าแล้วค่อยแปลงเป็น error หลังปิด transaction:
>
> ```go
> var rejected core.IError
> err := repo.Transaction(func(tx *gorm.DB) error {
>     if !canCancel(order) {
>         rejected = errmsgs.BadRequest      // เก็บไว้ แล้วสั่ง rollback
>         return errors.New("rollback")
>     }
>     ...
> })
> if rejected != nil {
>     return rejected                        // code เดิมครบ
> }
> ```

## The rule that causes every bug

**Use `tx` inside.** The repository the transaction was started from is *not* in
the transaction — a write made through it commits immediately and survives the
rollback:

```go
err := users.Transaction(func(tx *gorm.DB) error {
    repository.NewWithDB[Order](ctx, tx).Create(&order)   // ✅ in the transaction
    repository.New[Audit](ctx).Create(&entry)             // ❌ committed regardless
    return errors.New("boom")                             // order rolls back, entry does not
})
```

The same applies to `ctx.DB()` and to any repository built with `New` inside the
closure. If it did not get `tx`, it is not part of the transaction.

There is no error, no warning, and nothing in the logs that distinguishes the two
lines. The only reliable defence is habit: **bind every repository at the top of
the closure, and never call `New` below that point.**

```go
err := repo.Transaction(func(tx *gorm.DB) error {
    users  := repository.NewWithDB[User](ctx, tx)
    orders := repository.NewWithDB[Order](ctx, tx)
    audit  := repository.NewWithDB[Audit](ctx, tx)
    // from here on there is nothing non-transactional in scope
    ...
})
```

### Passing the transaction down

A service method called from inside a transaction has to receive it. Take a
`*gorm.DB` rather than reaching for `ctx.DB()`:

```go
// ✅ works inside and outside a transaction
func chargeCredits(ctx core.IContext, tx *gorm.DB, userID string, n int64) core.IError {
    return repository.NewWithDB[User](ctx, tx).
        Where("id = ? AND credits >= ?", userID, n).
        Update("credits", gorm.Expr("credits - ?", n))
}

// caller inside a transaction
repo.Transaction(func(tx *gorm.DB) error { return chargeCredits(ctx, tx, id, 10) })

// caller outside one
chargeCredits(ctx, ctx.DB(), id, 10)
```

`NewWithDB` falls back to `ctx.DB()` when the handle is `nil`, so a `nil` is a
valid "no transaction" — but passing `ctx.DB()` explicitly reads better and makes
the intent visible at the call site.

## What belongs inside

Only the statements that must succeed or fail together. Everything else is better
outside, because a transaction holds locks and a connection from the pool for as
long as it runs.

| Keep out | Why |
|---|---|
| HTTP calls to another service | the remote call cannot be rolled back, and its latency is now lock-hold time |
| S3 uploads | same — and a rolled-back transaction leaves the object behind |
| Publishing to pub/sub or a queue | subscribers can receive an event for a transaction that then rolls back |
| Long computation | it is holding a connection the whole time |
| Waiting on a user | never |

The usual shape: do the external work first if it is idempotent, or write a row
inside the transaction and let something after the commit act on it.

```go
if err := repo.Transaction(func(tx *gorm.DB) error {
    return repository.NewWithDB[Order](ctx, tx).Create(&order)
}); err != nil {
    return err
}
// after the commit — a subscriber that sees this can rely on the row existing
ctx.PubSub().Publish("order.created", order.ID)
```

### The outbox, when the publish must not be lost

Publishing after the commit can still lose the message if the process dies in
between. When that matters, write the intent **inside** the transaction and
deliver it from a [job](./jobs.md):

```go
err := repo.Transaction(func(tx *gorm.DB) error {
    if err := repository.NewWithDB[Order](ctx, tx).Create(&order); err != nil {
        return err
    }
    return repository.NewWithDB[Outbox](ctx, tx).Create(&Outbox{
        Topic:   "order.created",
        Payload: mustJSON(order),
    })
})
```

```go
// a job, every few seconds
rows, _ := repository.New[Outbox](ctx).Where("sent_at IS NULL").Limit(100).FindAll()
for _, row := range rows {
    if err := ctx.PubSub().Publish(row.Topic, row.Payload); err != nil {
        continue
    }
    repository.New[Outbox](ctx).Where("id = ?", row.ID).Update("sent_at", time.Now())
}
```

The row and the intent to publish commit together, so neither can exist without
the other. Delivery becomes at-least-once, which is what consumers should be
built for anyway.

## Two shapes for one transaction

**Closure** — the default. Commit and rollback are automatic and no path can
leave a transaction open:

```go
err := repo.Transaction(func(tx *gorm.DB) error { ... })
```

**Manual** — when the commit point is not the end of a function: a multi-step
import, a conditional rollback, a transaction whose scope is decided at runtime.

```go
tx := ctx.DB().Begin()
if tx.Error != nil {
    return ctx.NewError(tx.Error, errmsgs.DBError)
}
defer tx.Rollback()          // no-op after a successful commit

repo := repository.NewWithDB[User](ctx, tx)
if err := repo.Create(&u); err != nil {
    return err               // the deferred rollback runs
}

if err := tx.Commit().Error; err != nil {
    return ctx.NewError(err, errmsgs.DBError)
}
```

`defer tx.Rollback()` immediately after `Begin` is the line worth copying: it
means no return path — including a panic — can leave a transaction open, and
rolling back an already committed transaction is harmless.

## Nesting and savepoints

GORM implements a nested `Transaction` as a **savepoint**, so an inner failure
rolls back only the inner block:

```go
err := users.Transaction(func(tx *gorm.DB) error {
    txUsers := repository.NewWithDB[User](ctx, tx)
    if err := txUsers.Create(&u); err != nil {
        return err
    }

    // the audit entry is best-effort: its failure must not lose the user
    if err := txUsers.Transaction(func(tx2 *gorm.DB) error {
        return repository.NewWithDB[Audit](ctx, tx2).Create(&entry)
    }); err != nil {
        ctx.Log().Warn("audit failed", "err", err)
    }
    return nil
})
```

Explicit savepoints, when you want to name the point you can return to:

```go
tx := ctx.DB().Begin()
defer tx.Rollback()

tx.SavePoint("before_import")
if err := importRows(ctx, tx, rows); err != nil {
    tx.RollbackTo("before_import")     // keep everything before it
}
tx.Commit()
```

Both are occasionally exactly right and more often a sign the boundary is in the
wrong place. Prefer one transaction with a clear scope.

## Disabling the implicit transaction

GORM wraps **every single write** in its own transaction by default. For a bulk
insert of many small rows that overhead is measurable:

```go
db, _ := core.NewDatabase(env)
db = db.Session(&gorm.Session{SkipDefaultTransaction: true})
```

Two consequences worth knowing before you do it. A multi-statement `Create` (a
parent with children) is no longer atomic on its own, so it needs an explicit
transaction. And in tests, GORM's implicit transaction can mask a missing one of
yours — `coretest.WithoutDefaultTransaction()` exists for exactly that, so a test
can tell your `Transaction` from GORM's.

## Locking

Two requests reading the same row, deciding, and writing it back will lose one of
the decisions. `SELECT … FOR UPDATE` makes the second one wait:

```go
err := repo.Transaction(func(tx *gorm.DB) error {
    var account Account
    if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
        First(&account, "id = ?", id).Error; err != nil {
        return err
    }
    if account.Balance < amount {
        return errmsgs.InsufficientFunds
    }
    return tx.Model(&account).Update("balance", account.Balance-amount).Error
})
```

The lock lives and dies with the transaction, so this only works inside one.

| Clause | Effect |
|---|---|
| `clause.Locking{Strength: "UPDATE"}` | others wait for this row |
| `clause.Locking{Strength: "SHARE"}` | others may read, not write |
| `clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}` | fail immediately instead of waiting |
| `clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}` | ignore locked rows — the queue-pop pattern |

`SKIP LOCKED` is how several workers pull from one table without stepping on each
other:

```go
var jobs []Job
err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
    Where("status = ?", "queued").
    Order("created_at").
    Limit(10).
    Find(&jobs).Error
```

### Two cheaper alternatives, first

- **Atomic arithmetic** — `Update("credits", gorm.Expr("credits - ?", n))` with a
  `WHERE credits >= ?`, and `RowsAffected` as the answer. No read, no lock, no
  transaction. See
  [Check-then-act](./repository-recipes.md#check-then-act-without-the-race).
- **A distributed lock** — [`core.WithLock`](./cache-locks.md), when the thing
  being serialised is not a row: an external API, a file, a whole job.

### Deadlocks

Two transactions that lock the same rows **in different orders** deadlock; the
database kills one of them. The cure is not retrying — it is locking in a
consistent order:

```go
// ✅ always lock the lower id first, whichever direction the transfer goes
first, second := from, to
if first > second {
    first, second = second, first
}
```

## Retrying

Serialisation failures and deadlocks are *transient*: the same transaction, run
again, usually succeeds. Retry only those, only when the work is safe to repeat,
and with a bound:

```go
func withRetry(ctx core.IContext, attempts int, fn func(tx *gorm.DB) error) core.IError {
    var err core.IError
    for i := 0; i < attempts; i++ {
        err = repository.New[Account](ctx).Transaction(fn)
        if err == nil || !isSerializationFailure(err) {
            return err
        }
        time.Sleep(time.Duration(1<<i) * 10 * time.Millisecond)   // backoff
    }
    return err
}

func isSerializationFailure(err error) bool {
    var pgErr *pgconn.PgError
    if errors.As(err, &pgErr) {
        return pgErr.Code == "40001" || pgErr.Code == "40P01"   // serialization / deadlock
    }
    return false
}
```

Retrying a *business* failure — insufficient funds, a duplicate key — just does
the same wrong thing again more slowly. Match the error code.

## Isolation levels

The default is what postgres and mysql give you (`READ COMMITTED` and
`REPEATABLE READ` respectively), and it is right for almost everything. When a
transaction must see a consistent snapshot across several reads:

```go
tx := ctx.DB().Begin(&sql.TxOptions{Isolation: sql.LevelSerializable})
defer tx.Rollback()
// ... use repository.NewWithDB[T](ctx, tx) ...
tx.Commit()
```

Serializable trades throughput for correctness and makes serialisation failures
*expected* — pair it with the retry above, or do not use it.

A read-only transaction lets the database skip work and makes the intent explicit:

```go
tx := ctx.DB().Begin(&sql.TxOptions{ReadOnly: true})
defer tx.Rollback()
```

## Timeouts and cancellation

A transaction started from a request context is cancelled when the client hangs
up, and the driver rolls it back. That is usually right. When it is not — an
import that must complete — bind another context *before* starting:

```go
bg, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()

err := repo.WithContext(bg).Transaction(func(tx *gorm.DB) error { ... })
```

Give it a deadline anyway. A transaction with no deadline that hits a lock wait
holds its connection until something else notices — and on postgres, an idle
transaction also holds back vacuum.

Bounding it on the server too, so it cannot outlive its usefulness even if the
client vanishes:

```go
tx.Exec("SET LOCAL lock_timeout = '3s'")
tx.Exec("SET LOCAL idle_in_transaction_session_timeout = '30s'")
```

## Pool exhaustion

A transaction holds a connection for its whole life. `MaxOpenConns` transactions
running at once means every other query in the process waits — including the ones
the transactions themselves are about to make.

The failure looks like the database being slow and is actually the pool being
empty. Two rules keep it away: **keep transactions short**, and **never start a
transaction and then wait on something that needs the pool** (an errgroup of
queries, a channel fed by another handler).

## Testing

Assert on both directions — that a commit wrote everything, and that a rollback
wrote nothing. The second is what catches a write that used `ctx.DB()` instead of
`tx`, and it only shows up if you assert on the *other* tables too.

`coretest.WithoutDefaultTransaction()` turns off GORM's implicit transaction, so
the test is measuring yours. Lock behaviour needs postgres — sqlite serialises
writers, so a `FOR UPDATE` test passes there whether or not the lock is taken.

See [Testing repository code → Testing a transaction](./repository-testing.md#testing-a-transaction).

## MongoDB

Mongo transactions have the same shape and one extra requirement — a replica set.
See [Mongo transactions](./mongo-transactions.md).
