package main

import (
	"context"
	"fmt"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 5: logs --------------------------------------------------------
//
// Persisted logs are the most expensive part of the system: a run record is one
// row, but its logs are N. A job running every 30s that logs 20 lines writes
// ~1.7M rows a month on its own. So storing them is **off by default**.
//
// Off does not mean blind. Under LogOff you still get:
//   - the lines on stdout, tagged with job/run_id/attempt
//   - live tailing while the run is in flight
//   - the last ~50 lines attached to JobRun.Error when a run fails
//
// Turn persistence on per job, or per trigger, only where it earns its cost.

func registerLogPolicyJobs(reg *core.JobRegistry) {
	// default (LogOff): noisy, frequent, boring when it works
	_ = reg.Register(core.JobDef{
		Name:     "sync-users",
		Schedule: core.Every(5 * time.Minute),
	}, syncUsers)

	// LogOnFailure: costs nothing on success (the buffer is dropped), and gives
	// you the whole story when it breaks. Start here when you want more.
	_ = reg.Register(core.JobDef{
		Name:     "sync-partners",
		Schedule: core.Every(15 * time.Minute),
		Logs:     core.LogPolicyPtr(core.LogOnFailure),
	}, syncUsers)

	// LogAlways: important and infrequent, where the audit trail is the point.
	// Keep the runs for a year but the logs for a month.
	_ = reg.Register(core.JobDef{
		Name:       "monthly-settlement",
		Schedule:   core.Cron("0 4 1 * *"),
		Logs:       core.LogPolicyPtr(core.LogAlways),
		RetainRuns: 365 * 24 * time.Hour,
		RetainLogs: 30 * 24 * time.Hour,
	}, syncUsers)
}

func syncUsers(c core.ICronjobContext) error {
	c.Log().Info("sync started", "job", c.JobName())
	c.Log().Info("sync finished", "updated", 12)
	return nil
}

// triggerWithFullLogs is the operator's escape hatch: this one run keeps
// everything, while the scheduled ones stay silent.
func triggerWithFullLogs(ctx context.Context, runner *core.JobRunner, user string) (*core.JobRun, core.IError) {
	return runner.Trigger(ctx, "sync-users", nil, core.TriggerOptions{
		By:          user,
		CaptureLogs: core.LogPolicyPtr(core.LogAlways),
	})
}

// watchRun tails a run as it happens. It reads from an in-memory hub, not the
// store, which is why it works whatever the log policy is — the usual "click
// run, watch it go" flow needs no storage at all.
func watchRun(runner *core.JobRunner, runID string) {
	lines, unsubscribe := runner.TailLogs(runID)
	defer unsubscribe()

	timeout := time.After(5 * time.Minute)
	for {
		select {
		case e, ok := <-lines:
			if !ok {
				return
			}
			fmt.Printf("[%s] %s %s\n", e.At.Format(time.TimeOnly), e.Level, e.Message)
		case <-timeout:
			return
		}
	}
}

// readStoredLogs pages through what was persisted, using seq as the cursor.
func readStoredLogs(ctx context.Context, runner *core.JobRunner, runID string) core.IError {
	var after int64
	for {
		entries, err := runner.Logs(ctx, runID, after, 200)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		for _, e := range entries {
			fmt.Printf("%d %s %s\n", e.Seq, e.Level, e.Message)
		}
		after = entries[len(entries)-1].Seq
	}
}
