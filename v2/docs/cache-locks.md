# Locks

A distributed lock makes sure that, across every replica of a service, one piece
of work happens once. It is what a `sync.Mutex` cannot do, because your process
is not the only one.

```go
err := core.WithLock(ctx.Cache(), "settle:"+id, time.Minute, func() error {
    return settle(ctx, id)
})
if errors.Is(err, core.ErrLockNotAcquired) {
    // somebody else is doing it — a 409, not a failure
}
```

`WithLock` acquires, runs, and releases however `fn` ends — including a panic.
It is the shape almost every caller wants.

## Two properties that matter

**A lock always expires.** A holder that crashes releases it by doing nothing, so
one bad deploy cannot wedge a queue forever.

**Release compares a token.** A lock that expired mid-work and was taken over by
somebody else is never released by the previous holder — which would otherwise
put two workers in a section meant to hold one.

Neither is optional, and both are why this is a lock rather than a `SETNX`.

## Acquiring

```go
lock, err := ctx.Cache().Lock("settle:"+id, time.Minute)   // tries once
if errors.Is(err, core.ErrLockNotAcquired) {
    return ctx.NewError(err, errmsgs.AlreadyRunning)
}
defer lock.Unlock()          // idempotent, safe to defer
```

Waiting for a turn instead of failing immediately:

```go
lock, err := core.LockWait(ctx.Cache(), "import:"+id, time.Minute, 5*time.Second)
```

`LockWait` polls (redis has no blocking acquire) every 50ms and **gives up** at
the deadline with `ErrLockNotAcquired`, rather than blocking forever. A jammed
lock therefore shows up as a failed request rather than an exhausted goroutine
pool — which is the failure you can diagnose.

Keep the wait short. A queue of requests all waiting on one lock is a queue of
held connections; past a second or two, returning 409 and letting the client
retry is cheaper for everyone.

## Choosing the TTL

The TTL is a bet on how long the work takes:

| Too short | Too long |
|---|---|
| the lock expires mid-work and a second worker starts | a crashed holder blocks the work for that long |

Estimate the p99 of the work, double it, and `Extend` if the work can outrun it.
The default when no TTL is given is 30 seconds.

```go
lock, _ := ctx.Cache().Lock("report:"+id, 2*time.Minute)
defer lock.Unlock()

for _, chunk := range chunks {
    if err := lock.Extend(2 * time.Minute); err != nil {
        // core.ErrLockLost — somebody else owns it now. Stop.
        return ctx.NewError(err, errmsgs.LockLost)
    }
    if err := process(chunk); err != nil {
        return err
    }
}
```

`Extend` failing with `core.ErrLockLost` is not a hiccup to retry past. It means
the work is no longer protected and another worker may already be doing it —
stop, and let whoever holds the lock finish.

## What locks are actually for

```go
// a cron job that must not run twice when two pods fire at the same second
core.WithLock(ctx.Cache(), "job:daily-settlement", 10*time.Minute, run)

// one outbound call per entity, not one per request
core.WithLock(ctx.Cache(), "sync:"+accountID, 30*time.Second, syncAccount)

// serialising work on a resource with no locking of its own — a file, an API
core.WithLock(ctx.Cache(), "export:"+tenantID, time.Minute, buildExport)
```

And what they are **not** for:

| Instead of a lock | Use |
|---|---|
| protecting a row while you read-modify-write it | a database [transaction with `FOR UPDATE`](./database-transactions.md#locking) |
| incrementing a number | [`Incr`](./cache-counters.md), which is already atomic |
| "process this request once" | [`SetNX`](./cache-operations.md#write-once-idempotency-keys-and-one-shot-flags) |
| protecting a single Mongo document | `FindOneAndUpdate`, which is one operation |

The database already has locks that are transactional, deadlock-detected and
released on disconnect. A redis lock is for the things the database cannot see.

## The honest caveat

This is a single-instance lock — the ordinary one, not Redlock. Under a redis
failover it is possible for two holders to exist: the primary grants a lock,
fails before replicating it, and the promoted replica grants it again.

That window is small and the failure mode is worth stating plainly: **do not use
this as the only thing standing between you and double-charging a customer.**
Locks reduce duplicate work; idempotency makes duplicate work harmless. Systems
that must not double-do something use both.

```go
// the lock makes the duplicate rare; the idempotency key makes it harmless
core.WithLock(ctx.Cache(), "charge:"+orderID, time.Minute, func() error {
    first, _ := ctx.Cache().SetNX("charged:"+orderID, "1", 24*time.Hour)
    if !first {
        return nil          // already done
    }
    return charge(ctx, orderID)
})
```

## On a disabled cache

A lock is **granted**. Without a shared cache there is nothing to coordinate
with, and refusing would stop a single-instance deployment from doing the work at
all.

Which means: in an environment with no redis, every replica holds every lock. On
a developer's machine that is right. On a multi-replica deployment that
accidentally has no `CACHE_*` configured, it is a silent correctness bug — so
check `Enabled()` on the paths where it would matter, and make the boot log say
which cache the service got.

## Locks and jobs

The [job runner](./jobs.md) has its own overlap policy, which is the better tool
when the question is "should this job run while the last run is still going".
Reach for a cache lock when the exclusion spans *different* jobs, different
services, or a resource outside the runner.
