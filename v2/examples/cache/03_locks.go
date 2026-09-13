package main

import (
	"errors"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 3: locks -------------------------------------------------------
//
// A cache lock makes one piece of work happen once across every replica of a
// service. It is what a sync.Mutex cannot do, because your process is not the
// only one.
//
// Two properties make it a lock rather than a SETNX, and neither is optional:
//
//	it always expires     a holder that crashes releases it by doing nothing,
//	                      so one bad deploy cannot wedge a queue forever
//	release compares a    a lock that expired mid-work and was taken over is
//	token                 never released by its previous holder — which would
//	                      otherwise put two workers in a section meant for one

func runLocks(ctx core.IContext) {
	// WithLock is the shape almost every caller wants: acquire, run, and release
	// however fn ends — including a panic.
	err := core.WithLock(ctx.Cache(), "settle:invoice-1", time.Minute, func() error {
		return settleInvoice(ctx, "invoice-1")
	})
	if errors.Is(err, core.ErrLockNotAcquired) {
		// Somebody else is already doing it. That is a 409, not a failure.
		ctx.Log().Info("settlement is already running elsewhere")
	}

	// Lock tries once; LockWait polls for a turn (redis has no blocking
	// acquire) and gives up at the deadline rather than blocking forever, so a
	// jammed lock shows up as a failed request instead of an exhausted
	// goroutine pool. Keep the wait short: a queue of requests waiting on one
	// lock is a queue of held connections, and past a second or two, returning
	// 409 and letting the client retry is cheaper for everyone.
	lock, err := core.LockWait(ctx.Cache(), "import:tenant-1", time.Minute, 2*time.Second)
	if err != nil {
		ctx.Log().Info("gave up waiting for the import lock")
	} else {
		defer func() { _ = lock.Unlock() }() // idempotent, safe to defer
	}

	_ = longRunningExport(ctx, "tenant-1")
	_ = chargeOnce(ctx, "order-1")

	// Without a shared cache a lock is *granted* — refusing would stop a
	// single-instance deployment doing the work at all. Which means every
	// replica holds every lock. On a laptop that is right; on a multi-replica
	// deployment with no CACHE_* configured it is a silent correctness bug, so
	// make the boot log say which cache the service actually got.
	if !ctx.Cache().Enabled() {
		ctx.Log().Warn("no cache configured: locks exclude nothing outside this process")
	}
}

// longRunningExport shows the TTL for what it is: a bet on how long the work
// takes. Too short and the lock expires mid-work and a second worker starts;
// too long and a crashed holder blocks the work for that long. Estimate the p99
// of the work, double it, and Extend when the work can outrun the bet. The
// default, when no TTL is given, is 30 seconds.
func longRunningExport(ctx core.IContext, tenantID string) core.IError {
	lock, err := ctx.Cache().Lock("export:"+tenantID, 2*time.Minute)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()

	for chunk := range 3 {
		if err := lock.Extend(2 * time.Minute); err != nil {
			// core.ErrLockLost: the work is no longer protected and another
			// worker may already be doing it. Stop — this is not a hiccup to
			// retry past.
			ctx.Log().Error("lock lost mid-export", "chunk", chunk, "err", err)
			return err
		}
		// … process one chunk …
	}
	return nil
}

// chargeOnce is the honest version of "do this exactly once".
//
// This is an ordinary single-instance lock, not Redlock. Under a redis failover
// two holders are possible: the primary grants the lock, dies before
// replicating it, and the promoted replica grants it again. The window is
// small, and it is not small enough to be the only thing standing between you
// and double-charging a customer.
//
// So the lock makes the duplicate rare and the idempotency key makes the
// duplicate harmless. Systems that must not double-do something use both.
func chargeOnce(ctx core.IContext, orderID string) core.IError {
	return core.WithLock(ctx.Cache(), "charge:"+orderID, time.Minute, func() error {
		first, _ := ctx.Cache().SetNX("charged:"+orderID, "1", 24*time.Hour)
		if !first {
			return nil // already charged
		}
		return charge(ctx, orderID)
	})
}

func settleInvoice(_ core.IContext, _ string) error { return nil }
func charge(_ core.IContext, _ string) error        { return nil }

// What locks are for — work that must happen once across the fleet:
//
//	core.WithLock(c, "job:daily-settlement", 10*time.Minute, run)  a singleton job
//	core.WithLock(c, "sync:"+accountID, 30*time.Second, sync)      one call per entity
//	core.WithLock(c, "export:"+tenantID, time.Minute, build)       a resource with no
//	                                                              locking of its own
//
// And what they are not for:
//
//	protecting a row while you read-modify-write   a database transaction with
//	                                               FOR UPDATE
//	incrementing a number                          Incr, already atomic
//	"process this request once"                    SetNX
//	protecting a single Mongo document             FindOneAndUpdate, one operation
//
// The database already has locks that are transactional, deadlock-detected and
// released on disconnect. A cache lock is for the things the database cannot
// see. For "should this job run while the last run is still going", the job
// runner's own overlap policy is the better tool — reach for a cache lock when
// the exclusion spans *different* jobs, different services, or a resource
// outside the runner.
