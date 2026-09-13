package main

import (
	"context"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 3: start everything, stop it in the right order -----------------
//
// Shutting a service down is not "close everything", it is a sequence — and
// getting it wrong is what turns a deploy into a burst of 500s and half-finished
// jobs. Runner owns that sequence:
//
//	1. BeforeStop hooks       leave the load balancer before draining, not during
//	2. stop the scheduler     a tick now would queue a run about to be cancelled
//	3. drain the job runner   runs already in flight get to finish
//	4. drain services + HTTP  in-flight requests get to answer
//	5. app.Shutdown()         ★ the only place pools are closed
//	6. AfterStop hooks        the last thing before the process exits
//
// The order cannot be reversed: a pool closed while a request still holds it
// turns a clean shutdown into exactly the errors it was meant to avoid.

const (
	// drainTimeout bounds steps 2–4. It MUST be lower than the orchestrator's
	// own grace period — Kubernetes' terminationGracePeriodSeconds, which
	// defaults to 30s — or the process is killed mid-drain and none of the
	// ordering above ever happens. Set it above the longest request you expect
	// and below that ceiling; 20s against a 30s grace period leaves room for
	// step 5.
	drainTimeout = 20 * time.Second
	// closeTimeout bounds step 5 only. Closing pools is fast; this exists so a
	// broker that has already gone away cannot hold the process open forever.
	closeTimeout = 5 * time.Second
)

func run(app *core.App, r role) error {
	opts := []core.RunnerOption{
		core.WithDrainTimeout(drainTimeout),
		core.WithCloseTimeout(closeTimeout),

		// BeforeStop runs while everything is still serving. That is the point:
		// deregistering here means traffic stops arriving *before* the drain
		// begins rather than throughout it, so the drain has a finite amount of
		// work to finish instead of a moving target.
		core.BeforeStop(func(ctx context.Context) error {
			app.Log().Info("deregistering from service discovery")
			return nil // a real one would call the registry, respecting ctx
		}),

		// AfterStop runs once the pools are closed, so it must not touch any of
		// them. It is for flushing something the framework does not own.
		core.AfterStop(func() {
			app.Log().Info("goodbye")
		}),
	}

	if r == roleAPI || r == roleAll {
		opts = append(opts, core.RunHTTP(newAPI(app)))
	}

	if r == roleWorker || r == roleAll {
		sc, err := newWorker(app)
		if err != nil {
			return err
		}
		// both, and RunJobs gets the same JobRunner the scheduler feeds. Passing
		// only the scheduler would stop the ticking and then close the pools
		// underneath runs that were still executing; passing both is what makes
		// step 3 wait for them.
		opts = append(opts, core.RunScheduler(sc), core.RunJobs(sc.Runner()))
	}

	// Run starts everything, logs what the process was assembled from, and
	// blocks until SIGINT or SIGTERM. SIGTERM is the one that matters: docker
	// stop, Kubernetes and systemd all send it, and a process that ignores it is
	// killed outright with every in-flight request.
	//
	// RunContext(ctx) is the same thing bounded by a context of your own — for a
	// test, or a process that decides to stop itself.
	return core.NewRunner(app, opts...).Run()
}

// Anything else with a Start and a Stop — a gRPC server, a metrics exporter, a
// third-party consumer — joins the sequence with core.RunService(s):
//
//	opts = append(opts, core.RunService(grpcServer))
//
// Start must not block. MQ consumers and pub/sub subscribers are the exception
// that needs nothing here: the App remembers them as it hands them out and stops
// them inside step 5, before it closes the connections they are reading from.
