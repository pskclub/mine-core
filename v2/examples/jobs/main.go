// Command jobs is a runnable tour of the v2 job runner: scheduled jobs, manual
// triggers with parameters, queueing, status, cancellation, concurrency limits,
// replay and log policies.
//
// Each example lives in its own file:
//
//	01_basic.go        the smallest thing that works (v1-style cron)
//	02_params.go       typed, validated parameters + manual triggers
//	03_concurrency.go  MaxConcurrent and the three overlap policies
//	04_stop.go         cancellable jobs, pause, graceful shutdown
//	05_logs.go         log policies — off by default, and how to debug anyway
//	06_replay.go       replay, retry and idempotency keys
//	07_store.go        durable runs in SQL through the repository
//	08_http_admin.go   an admin API over the runner
//	09_cluster.go      what changes once there is more than one replica
//
// Run it with: go run ./examples/jobs
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

func main() {
	env, err := core.NewEnv()
	if err != nil {
		panic(err)
	}
	app, err := core.NewApp(env)
	if err != nil {
		panic(err)
	}

	// 1. one registry holds every job definition of the service
	reg := core.NewJobRegistry()
	registerBasicJobs(reg)
	registerParamJobs(reg)
	registerConcurrencyJobs(reg)
	registerStoppableJobs(reg)
	registerLogPolicyJobs(reg)
	registerReplayJobs(reg)

	// 2. the runner executes runs — this is where backends and limits are set.
	//    With no options at all it still works: in-memory queue and store,
	//    in-process concurrency limits, no persisted logs.
	runner := core.NewJobRunner(app, reg,
		core.WithWorkers(4),
		core.WithQueues(core.DefaultQueue, "heavy"),
		core.WithQueueLimit("heavy", 1),
		core.WithLogPolicy(core.LogOff),
	)

	// 3. the scheduler only *enqueues*; the runner executes. Scheduled and
	//    manual runs therefore share one path, one status model, one log.
	//
	//    The location is set here, once, so every "0 3 * * *" below means 03:00
	//    where the service is operated rather than 03:00 wherever the container
	//    happens to think it is — which, with no TZ set, is UTC.
	bangkok, lerr := time.LoadLocation("Asia/Bangkok")
	if lerr != nil {
		bangkok = time.UTC
	}
	sc, err := core.NewScheduler(app, runner, core.WithSchedulerLocation(bangkok))
	if err != nil {
		panic(err)
	}

	// housekeeping: drop runs and logs past their retention
	_ = sc.Add(core.JobDef{
		Name:        "core.job.purge",
		Description: "delete job runs and logs past their retention",
		Schedule:    core.Cron("0 3 * * *"),
	}, func(c core.ICronjobContext) error {
		n, err := runner.Purge(c)
		if err != nil {
			return err
		}
		c.Log().Info("purged old job data", "rows", n)
		return nil
	})

	runner.Start()
	if err := sc.Start(); err != nil {
		panic(err)
	}

	// everything below is what an operator would do by hand — see the files
	demoManualTriggers(runner)

	// 4. shutdown: stop scheduling → drain the runner → close the pools
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.Log().Info("jobs example running — press ctrl-c to stop")
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = sc.Stop()                // no new runs are queued
	_ = runner.Stop(shutdownCtx) // in-flight runs finish, or go back on the queue
	_ = app.Shutdown(shutdownCtx)
}
