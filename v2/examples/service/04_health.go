package main

import (
	"context"
	"fmt"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 4: liveness, readiness, and why they are not the same ----------
//
//	           asks                          a wrong answer causes
//	/healthz   is this process wedged?       the pod is RESTARTED
//	/readyz    can this instance serve?      the pod leaves the load balancer
//
// /healthz touches no dependency at all, on purpose. A liveness probe that pings
// the database restarts every instance of the service the moment the database
// hiccups, turning one outage into two — the only correct answer to "is this
// process wedged?" is one that cannot fail for any other reason.
//
// /readyz probes every dependency the App holds, in parallel, and reports each
// one. Only configured dependencies appear: a service with no redis has no cache
// check rather than a cache check that always fails, so the probe describes what
// this deployment actually depends on.

func registerHealth(e *core.Server) {
	// registered on the server, never on an authenticated group — the
	// orchestrator has no token
	core.RegisterHealthRoutes(e, core.HealthOptions{
		// short on purpose: a probe that hangs is a probe that gets killed, and
		// an instance whose database takes ten seconds to answer is not ready
		// however the check eventually ends
		Timeout: 2 * time.Second,

		// Checks are added to the ones the App can see for itself (every SQL and
		// Mongo connection, every cache, mq, storage, mailer). Only replaces
		// that list instead, for a service that wants to name exactly what it
		// probes.
		Checks: []core.HealthCheck{{
			Name: "partner-api",
			// ★ NOT critical. Ask "can this instance still do its job without
			// it?" — if the answer is "most of it", a failure must report
			// degraded (200, stays in rotation), not down (503). Marking a
			// shared third party critical takes every replica of every service
			// out of rotation at the same instant, which is an outage this
			// service caused rather than one it suffered.
			Critical: false,
			Check:    pingPartner,
		}},

		// Details includes each dependency's error text in the body. Nil follows
		// the environment — on outside production, off in it — because an error
		// string names hosts, users and buckets, and the probe route is the one
		// nobody remembers to put behind the gateway. Force it with
		// core.BoolPtr(true) while debugging a staging deploy.
		Details: nil,
	})
}

// pingPartner stands in for a real dependency check. A real one calls the
// dependency's own health endpoint through core.Requester(ctx) — and must
// respect ctx, because the probe's timeout is the only thing bounding it.
func pingPartner(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// gateOnDependencies refuses to serve until every critical dependency answers,
// using the same checks the probe runs. core.CheckHealth is the probe without
// the HTTP around it, so a startup gate, a CLI and the route all agree.
//
// It is deliberately NOT called from main. The trade-off is real and usually
// goes the other way: a gate turns a database that is thirty seconds late into a
// crash loop, whereas readiness turns it into an instance that joins the load
// balancer thirty seconds late. Use it only where starting up wrong is worse
// than starting up slow — a migration runner, a one-shot job.
func gateOnDependencies(ctx context.Context, app *core.App) error {
	report := core.CheckHealth(ctx, app)
	if report.Status == core.HealthDown {
		return fmt.Errorf("dependencies are not ready: %+v", report.Checks)
	}
	app.Log().Info("dependencies ready",
		"status", report.Status, "took_ms", report.TookMS)
	return nil
}

// The probes can also be mounted by hand, at paths of your own:
//
//	e.GET("/internal/live", core.LiveHandler())
//	e.GET("/internal/ready", core.ReadyHandler(app))
//
// In Kubernetes the readiness probe should be the more frequent and the more
// sensitive of the two: leaving rotation and coming back costs far less than a
// restart.
//
//	livenessProbe:  { httpGet: {path: /healthz}, periodSeconds: 10, failureThreshold: 3 }
//	readinessProbe: { httpGet: {path: /readyz},  periodSeconds: 5,  failureThreshold: 2 }
