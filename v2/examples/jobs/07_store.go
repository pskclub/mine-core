package main

import (
	"context"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/jobstore"
	"github.com/pskclub/mine-core/v2/repository"
)

// --- Example 7: durable runs ------------------------------------------------
//
// The default backends keep everything in memory: zero configuration, but
// queued runs disappear with the process. When you want history and restart
// safety, swap in the repository-backed store — GORM underneath, no Redis, no
// new infrastructure. The runs table doubles as the queue.

func durableRunner(app *core.App, reg *core.JobRegistry) (*core.JobRunner, core.IError) {
	// create the tables (or take the DDL into your own migration tool)
	if err := jobstore.Migrate(app.NewContext(context.Background()).DB()); err != nil {
		return nil, err
	}

	store := jobstore.New(app)
	queue := jobstore.NewQueue(app, jobstore.WithPollInterval(time.Second))

	return core.NewJobRunner(app, reg,
		core.WithJobStore(store),
		core.WithJobQueue(queue),
		core.WithWorkers(4),
	), nil
}

// customPlacement puts job data on its own connection and its own table names —
// useful when the service already owns a schema convention.
func customPlacement(app *core.App, reg *core.JobRegistry) *core.JobRunner {
	opts := []jobstore.Option{
		jobstore.WithTables("ops_job_runs", "ops_job_run_logs"),
		jobstore.WithConnection("ops"), // core.IContext.DBS("ops")
	}
	return core.NewJobRunner(app, reg,
		core.WithJobStore(jobstore.New(app, opts...)),
		core.WithJobQueue(jobstore.NewQueue(app, opts...)),
	)
}

// Because core.JobRun is an ordinary core.IModel, job history is queryable with
// the same repository API as the rest of the service — no special client, and it
// joins with your own tables.
func failedRunsThisWeek(ctx core.IContext) ([]core.JobRun, core.IError) {
	since := time.Now().AddDate(0, 0, -7)
	return repository.New[core.JobRun](ctx).
		Where("status = ?", core.RunFailed).
		Where("created_at >= ?", since).
		Order("created_at DESC").
		FindAll()
}

// slowestJobs is the kind of report the store makes possible for free.
func slowestJobs(ctx core.IContext) ([]struct {
	JobName string
	AvgMS   float64
}, core.IError) {
	var out []struct {
		JobName string
		AvgMS   float64
	}
	err := repository.New[core.JobRun](ctx).
		Select("job_name, AVG(duration_ms) as avg_ms").
		Where("status = ?", core.RunSucceeded).
		Group("job_name").
		Order("avg_ms DESC").
		Limit(10).
		Scan(&out)
	return out, err
}

// purgeOldData keeps the tables from growing without bound. Register it on a
// schedule (see main.go) — retention for logs is deliberately shorter than for
// runs: statistics stay, chatter goes.
func purgeOldData(ctx context.Context, runner *core.JobRunner) (int64, core.IError) {
	return runner.Purge(ctx)
}
