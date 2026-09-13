package main

import (
	"context"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/utils"
)

// --- Example 6: running the same thing again --------------------------------
//
// Four different ideas people call "re-run". Keeping them apart is most of the
// design:
//
//	Cancel   stop something that has not finished
//	Retry    the runner's own doing, after a failure, while attempts remain
//	Replay   an operator re-running a *finished* run — a new run, linked back
//	IdemKey  stopping a duplicate run from being created in the first place

func registerReplayJobs(reg *core.JobRegistry) {
	// safe to replay: recomputing a report just recomputes it
	_ = reg.Register(core.JobDef{
		Name:        "recalculate-balances",
		MaxAttempts: 3,
		Backoff:     core.ExponentialBackoff(time.Second, 2*time.Minute),
	}, recalculateBalances)

	// NOT safe to replay: this moves real money. Replay is refused with 403,
	// so nobody can re-send a payout from the admin UI by accident.
	_ = reg.Register(core.JobDef{
		Name:       "payout-vendors",
		Replayable: core.BoolPtr(false),
	}, payoutVendors)
}

func recalculateBalances(c core.ICronjobContext) error {
	c.Log().Info("recalculating", "attempt", c.Attempt(), "trigger", c.Trigger())
	return nil
}

func payoutVendors(c core.ICronjobContext) error {
	// At-least-once delivery: a worker can die mid-run and the run comes back.
	// Handlers must be idempotent — key the side effect on something stable.
	c.Log().Info("paying out", "run_id", c.RunID())
	return nil
}

// replayFailedRuns re-runs everything that failed today — the "fix the bug,
// then replay" flow.
func replayFailedRuns(ctx context.Context, runner *core.JobRunner, by string) core.IError {
	since := time.Now().Add(-24 * time.Hour)
	page, err := runner.Runs(ctx, core.JobRunFilter{
		JobName:  "recalculate-balances",
		Statuses: []core.RunStatus{core.RunFailed},
		From:     &since,
		Page:     &core.PageOptions{Limit: 100},
	})
	if err != nil {
		return err
	}
	for _, old := range page.Items {
		// a new run each time: the original record is never overwritten, and
		// ReplayOf/RootID keep the whole chain traceable
		if _, rerr := runner.Replay(ctx, old.ID, core.ReplayOptions{By: by}); rerr != nil {
			return rerr
		}
	}
	return nil
}

// replayWithDifferentParams fixes the input rather than the code.
func replayWithDifferentParams(ctx context.Context, runner *core.JobRunner, runID string) (*core.JobRun, core.IError) {
	return runner.Replay(ctx, runID, core.ReplayOptions{
		By:     "admin@example.com",
		Params: &ReportParams{Date: utils.ToPointer("2026-07-01"), Force: true},
	})
}

// demoManualTriggers is called from main: it is the "operator does things"
// tour — trigger, watch, cancel, replay.
func demoManualTriggers(runner *core.JobRunner) {
	ctx := context.Background()

	// run it now, with parameters, safe against double clicks
	run, err := triggerReport(ctx, runner, "admin@example.com", "2026-07-01")
	if err != nil {
		return
	}

	// watch it live (works even though logs are not persisted)
	go watchRun(runner, run.ID)

	// ... and if it turns out to be wrong, stop it
	_ = runner.Cancel(ctx, run.ID, "admin@example.com", "wrong date")
}
