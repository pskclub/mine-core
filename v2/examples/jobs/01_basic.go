package main

import (
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 1: the smallest thing that works -------------------------------
//
// A job is a plain function of ICronjobContext, exactly as in v1. What changed
// is what happens around it: every tick now creates a JobRun you can inspect,
// instead of an anonymous function call that leaves no trace.

func registerBasicJobs(reg *core.JobRegistry) {
	// scheduled, no parameters
	_ = reg.Register(core.JobDef{
		Name:        "heartbeat",
		Description: "prove the worker is alive",
		Schedule:    core.Every(30 * time.Second),
		// a job that fires this often must never queue up behind itself
		MaxConcurrent: 1,
		Concurrency:   core.ConcurrencySkip,
	}, heartbeat)

	// scheduled with a cron expression, retried a few times
	_ = reg.Register(core.JobDef{
		Name:        "nightly-report",
		Description: "build yesterday's report",
		Schedule:    core.Cron("0 2 * * *"),
		Timeout:     10 * time.Minute,
		MaxAttempts: 3,
	}, nightlyReport)

	// the two above are read in the scheduler's zone (main.go sets it once with
	// core.WithSchedulerLocation). A job whose hour belongs to somewhere else
	// says so itself, and CronIn wins over the scheduler's setting:
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		tokyo = time.UTC
	}
	_ = reg.Register(core.JobDef{
		Name:        "tokyo-market-open",
		Description: "runs at 09:00 in Tokyo, whatever the scheduler's zone is",
		Schedule:    core.CronIn(tokyo, "0 9 * * 1-5"),
		Timeout:     5 * time.Minute,
	}, nightlyReport)

	// no Schedule at all: the job exists only to be triggered by hand
	_ = reg.Register(core.JobDef{
		Name:        "rebuild-search-index",
		Description: "manual-only maintenance task",
		Queue:       "heavy",
	}, rebuildSearchIndex)
}

func heartbeat(c core.ICronjobContext) error {
	c.Log().Debug("still alive", "job", c.JobName())
	return nil
}

func nightlyReport(c core.ICronjobContext) error {
	// the full context is available, exactly like in an HTTP handler:
	// c.DB(), c.Cache(), c.MQ(), core.Requester(c) — all bound to this run, so a
	// cancelled run cancels its queries and its outgoing calls too.
	c.Log().Info("building report", "run_id", c.RunID(), "attempt", c.Attempt())
	c.SetResult(map[string]any{"rows": 0})
	return nil
}

func rebuildSearchIndex(c core.ICronjobContext) error {
	c.Progress(0, "starting")
	time.Sleep(50 * time.Millisecond)
	c.Progress(100, "done")
	return nil
}
