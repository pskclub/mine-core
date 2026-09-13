package main

import (
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 3: how many run at once ----------------------------------------
//
// Three independent limits, each answering a different question:
//
//	WithWorkers(n)             how much this *process* will do at once
//	WithQueueLimit("heavy", 1) how much a *queue* may do at once
//	JobDef.MaxConcurrent       how many runs of *this job* may overlap
//
// and one policy that decides what happens when a run hits the limit.

func registerConcurrencyJobs(reg *core.JobRegistry) {
	// Skip — the right choice for anything on a short schedule. If the previous
	// run is still going, this one is dropped as "skipped" and life goes on.
	// With Enqueue here, a job slower than its interval would pile up forever.
	_ = reg.Register(core.JobDef{
		Name:          "poll-inbox",
		Schedule:      core.Every(time.Minute),
		MaxConcurrent: 1,
		Concurrency:   core.ConcurrencySkip,
	}, pollInbox)

	// Enqueue (the default) — nothing is lost. Runs wait their turn, at most two
	// at a time. Good for work that must all happen eventually.
	_ = reg.Register(core.JobDef{
		Name:          "send-invoice",
		MaxConcurrent: 2,
		Concurrency:   core.ConcurrencyEnqueue,
		MaxAttempts:   5,
		Backoff:       core.ExponentialBackoff(2*time.Second, time.Minute),
	}, sendInvoice)

	// Replace — latest wins. Triggering it again cancels the run in progress,
	// which only makes sense when the newer run supersedes the older one.
	_ = reg.Register(core.JobDef{
		Name:          "rebuild-cache",
		Queue:         "heavy",
		MaxConcurrent: 1,
		Concurrency:   core.ConcurrencyReplace,
		StopGrace:     10 * time.Second,
	}, rebuildCache)
}

func pollInbox(c core.ICronjobContext) error {
	c.Log().Debug("polling")
	return nil
}

func sendInvoice(c core.ICronjobContext) error {
	c.Log().Info("sending invoice", "attempt", c.Attempt())
	return nil
}

func rebuildCache(c core.ICronjobContext) error {
	for i := range 10 {
		// a Replace-able job must be stoppable, or "latest wins" cannot work
		if c.IsStopping() {
			c.Log().Info("superseded by a newer run")
			return c.Err()
		}
		c.Progress(i*10, "rebuilding")
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}
