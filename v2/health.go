package core

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Health states, as reported in the probe body.
const (
	// HealthUp means every critical dependency answered.
	HealthUp = "up"
	// HealthDegraded means a non-critical dependency is down. The probe still
	// answers 200: the service can serve, just not everything.
	HealthDegraded = "degraded"
	// HealthDown means a critical dependency is down. The probe answers 503 and
	// the orchestrator takes the instance out of rotation.
	HealthDown = "down"
)

// DefaultHealthTimeout bounds the whole readiness probe. It is short on purpose:
// a probe that hangs is a probe that gets killed, and an instance whose database
// takes ten seconds to answer is not ready however the check eventually ends.
const DefaultHealthTimeout = 3 * time.Second

// HealthCheck is one dependency the readiness probe asks about.
type HealthCheck struct {
	// Name is what the result is keyed by in the body ("database", "cache").
	Name string
	// Check reports whether the dependency is usable. It must respect ctx.
	Check func(ctx context.Context) error
	// Critical decides what a failure means. A critical dependency failing makes
	// the service not ready (503); a non-critical one makes it degraded (200).
	//
	// Ask "can this instance still do its job without it?" A cache usually yes,
	// the primary database usually no.
	Critical bool
}

// HealthReport is the body of a readiness probe.
type HealthReport struct {
	Status  string                 `json:"status"`
	Service string                 `json:"service,omitempty"`
	Checks  map[string]CheckResult `json:"checks,omitempty"`
	TookMS  int64                  `json:"took_ms"`
}

// CheckResult is one dependency's answer.
type CheckResult struct {
	Status   string `json:"status"`
	Critical bool   `json:"critical,omitempty"`
	Error    string `json:"error,omitempty"`
	TookMS   int64  `json:"took_ms"`
}

// HealthOptions tunes the readiness probe.
type HealthOptions struct {
	// Timeout bounds the whole probe (default DefaultHealthTimeout). A check
	// that has not answered by then is reported as down.
	Timeout time.Duration
	// Details includes each dependency's error text in the body. Nil follows the
	// environment: on outside production, off in it — an error string can name a
	// host, a user or a bucket, and a probe endpoint is often the one route
	// nobody remembers to put behind the gateway.
	Details *bool
	// Checks are extra dependencies to probe, alongside the ones the App knows
	// about. Use them for things the framework cannot see: a partner API, a
	// license server, a mounted volume.
	Checks []HealthCheck
	// Only replaces the App's own checks entirely, rather than adding to them.
	// For a service that wants to name exactly what it probes.
	Only []HealthCheck
}

// LiveHandler answers 200 as long as the process is running. It touches no
// dependency on purpose.
//
// This is the liveness probe, and the only correct answer to "is this process
// wedged?" is one that cannot fail for any other reason. A liveness probe that
// pings the database restarts every instance of the service when the database
// hiccups, turning one outage into two.
func LiveHandler() HandlerFunc {
	return func(c IHTTPContext) error {
		return c.JSON(http.StatusOK, map[string]string{"status": HealthUp})
	}
}

// ReadyHandler answers whether this instance can serve traffic: it probes every
// dependency the App holds, in parallel, and reports each one.
//
//	e.GET("/healthz", core.LiveHandler())
//	e.GET("/readyz", core.ReadyHandler(app))
//
// A critical dependency being down answers 503, which is what takes the instance
// out of the load balancer without restarting it.
func ReadyHandler(app *App, opts ...HealthOptions) HandlerFunc {
	o := healthOptions(opts)
	return func(c IHTTPContext) error {
		report := CheckHealth(c, app, o)
		status := http.StatusOK
		if report.Status == HealthDown {
			status = http.StatusServiceUnavailable
		}
		return c.JSON(status, report)
	}
}

// RegisterHealthRoutes adds the two probes every deployment needs, at the paths
// Kubernetes uses by default. They are registered before any authentication —
// call it on the server, not on an authenticated group.
func RegisterHealthRoutes(e *Server, opts ...HealthOptions) {
	e.GET("/healthz", LiveHandler())
	e.GET("/readyz", ReadyHandler(e.App(), opts...))
}

// CheckHealth runs the probe and returns the report, for a caller that wants to
// answer in its own shape — a startup gate, a CLI, a different route.
func CheckHealth(ctx context.Context, app *App, opts ...HealthOptions) HealthReport {
	o := healthOptions(opts)

	checks := o.Only
	if checks == nil {
		checks = append(AppHealthChecks(app), o.Checks...)
	}

	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultHealthTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	details := o.Details != nil && *o.Details
	if o.Details == nil {
		details = app == nil || app.env == nil || !app.env.IsProd()
	}

	started := time.Now()
	results := runHealthChecks(ctx, checks)

	report := HealthReport{Status: HealthUp, Checks: make(map[string]CheckResult, len(results))}
	if app != nil && app.env != nil {
		report.Service = app.env.Config().Service
	}
	for _, r := range results {
		if !details {
			r.result.Error = ""
		}
		report.Checks[r.name] = r.result
		if r.result.Status != HealthDown {
			continue
		}
		if r.result.Critical {
			report.Status = HealthDown
		} else if report.Status == HealthUp {
			report.Status = HealthDegraded
		}
	}
	report.TookMS = time.Since(started).Milliseconds()
	return report
}

type namedResult struct {
	name   string
	result CheckResult
}

// runHealthChecks probes every dependency at once. In series the probe would
// take as long as the sum of its checks, and the timeout that protects it would
// have to be loose enough to be useless.
func runHealthChecks(ctx context.Context, checks []HealthCheck) []namedResult {
	out := make([]namedResult, len(checks))
	var wg sync.WaitGroup

	for i, hc := range checks {
		wg.Add(1)
		go func(i int, hc HealthCheck) {
			defer wg.Done()
			out[i] = namedResult{name: hc.Name, result: runHealthCheck(ctx, hc)}
		}(i, hc)
	}
	wg.Wait()

	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// runHealthCheck runs one check, surviving a dependency whose client panics —
// the probe is the last thing that should take the process down.
func runHealthCheck(ctx context.Context, hc HealthCheck) (res CheckResult) {
	started := time.Now()
	res = CheckResult{Status: HealthUp, Critical: hc.Critical}

	defer func() {
		res.TookMS = time.Since(started).Milliseconds()
		if r := recover(); r != nil {
			res.Status = HealthDown
			res.Error = "panic in health check"
		}
	}()

	if hc.Check == nil {
		return res
	}
	if err := hc.Check(ctx); err != nil {
		res.Status = HealthDown
		res.Error = err.Error()
	}
	return res
}

// AppHealthChecks is what the App can probe on its own: every registered SQL and
// Mongo connection, every cache, the queue and object storage.
//
// Only configured dependencies appear. A service with no redis has no cache
// check, rather than a cache check that always fails — the probe reports what
// this deployment actually depends on.
//
// The database connections are critical and the rest are not, which is the
// common case rather than a rule; replace the list with HealthOptions.Only when
// it is wrong for a service.
func AppHealthChecks(app *App) []HealthCheck {
	if app == nil {
		return nil
	}
	checks := make([]HealthCheck, 0, 8)

	for name, db := range app.dbs {
		if db == nil {
			continue
		}
		checks = append(checks, HealthCheck{
			Name:     healthName("database", name),
			Critical: true,
			Check: func(ctx context.Context) error {
				sqlDB, err := db.DB()
				if err != nil {
					return err
				}
				return sqlDB.PingContext(ctx)
			},
		})
	}

	for name, m := range app.mongos {
		if m == nil {
			continue
		}
		checks = append(checks, HealthCheck{
			Name:     healthName("mongo", name),
			Critical: true,
			Check:    func(ctx context.Context) error { return errOrNil(m.WithContext(ctx).Ping()) },
		})
	}

	for name, ch := range app.caches {
		if ch == nil || !ch.Enabled() {
			continue
		}
		checks = append(checks, HealthCheck{
			Name:  healthName("cache", name),
			Check: func(ctx context.Context) error { return errOrNil(ch.WithContext(ctx).Ping()) },
		})
	}

	if app.mq != nil && app.mq.Enabled() {
		checks = append(checks, HealthCheck{
			Name:  "mq",
			Check: func(ctx context.Context) error { return errOrNil(app.mq.WithContext(ctx).Ping()) },
		})
	}

	if app.storage != nil && app.storage.Enabled() {
		checks = append(checks, HealthCheck{
			Name:  "storage",
			Check: func(ctx context.Context) error { return errOrNil(app.storage.WithContext(ctx).Ping()) },
		})
	}

	if app.mailer != nil && app.mailer.Enabled() {
		checks = append(checks, HealthCheck{
			Name:  "mailer",
			Check: func(ctx context.Context) error { return errOrNil(app.mailer.WithContext(ctx).Ping()) },
		})
	}

	for name, ch := range app.chats {
		if ch == nil || !ch.Enabled() {
			continue
		}
		checks = append(checks, HealthCheck{
			Name:  healthName("chat", name),
			Check: func(ctx context.Context) error { return errOrNil(ch.WithContext(ctx).Ping()) },
		})
	}

	return checks
}

// healthName keeps the default connection's check called "database" rather than
// "database.default", and names the others.
func healthName(kind, conn string) string {
	if conn == "" || conn == defaultConn {
		return kind
	}
	return kind + "." + conn
}

// errOrNil unwraps a typed nil: an IError-valued nil is not a nil error, and a
// check that returned one would report a healthy dependency as down.
func errOrNil(err IError) error {
	if err == nil {
		return nil
	}
	return err
}

func healthOptions(opts []HealthOptions) HealthOptions {
	if len(opts) > 0 {
		return opts[0]
	}
	return HealthOptions{}
}
