package main

import (
	"context"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/repository"
)

// --- Example 4: stopping ----------------------------------------------------
//
// Go cannot kill a goroutine, so stopping is always *cooperative*: the runner
// cancels the run's context and the handler has to notice. Everything that goes
// through the context — DB queries, HTTP calls, cache reads — is cancelled for
// you; only your own loops need the explicit check.
//
// A handler that never checks is not a hung service: after StopGrace the runner
// records the run as canceled and moves on (it logs a warning, and the goroutine
// is left to finish on its own). Still, write stoppable jobs.

func registerStoppableJobs(reg *core.JobRegistry) {
	_ = reg.Register(core.JobDef{
		Name:        "migrate-users",
		Description: "long, resumable batch job",
		Queue:       "heavy",
		Timeout:     2 * time.Hour,
		StopGrace:   30 * time.Second,
	}, migrateUsers)
}

type userRow struct {
	ID string `gorm:"column:id"`
}

func (userRow) TableName() string { return "users" }

func migrateUsers(c core.ICronjobContext) error {
	const batch = 500
	for offset := 0; ; offset += batch {
		// the one line that makes this job stoppable
		if c.IsStopping() {
			c.Log().Info("stopping cleanly", "processed", offset)
			return c.Err()
		}

		rows, err := repository.New[userRow](c).Limit(batch).Offset(offset).FindAll()
		if err != nil {
			return err // the query is cancelled with the run — no orphaned work
		}
		if len(rows) == 0 {
			break
		}
		c.Progress(offset*100/50000, "migrating")
	}
	return nil
}

// stopExamples shows the three levels of "stop", which are *not* the same thing.
func stopExamples(ctx context.Context, runner *core.JobRunner, sc *core.Scheduler, runID string) {
	// 1. stop one run. Queued → it never starts. Running → it is asked to stop.
	_ = runner.Cancel(ctx, runID, "admin@example.com", "wrong parameters")

	// 2. pause a whole job: the schedule stops firing and manual triggers are
	//    rejected, without touching anything already queued.
	_ = runner.Pause("migrate-users")
	_ = runner.Resume("migrate-users")

	// 3. stop the process. Runs that do not finish in time go back on the
	//    queue — they are not failures, they never got the chance to fail.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = sc.Stop()
	_ = runner.Stop(shutdownCtx)
}
