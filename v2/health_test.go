package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newHealthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func decodeHealth(t *testing.T, body []byte) HealthReport {
	t.Helper()
	var out HealthReport
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// Liveness must not touch a dependency. A liveness probe that pings the database
// restarts every instance when the database hiccups, turning one outage into two.
func TestHealth_livenessTouchesNothing(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	RegisterHealthRoutes(e)

	rec := doJSON(e, http.MethodGet, "/healthz", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"up"}`, rec.Body.String())
}

func TestHealth_readyWithNoDependenciesIsUp(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	RegisterHealthRoutes(e)

	rec := doJSON(e, http.MethodGet, "/readyz", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, HealthUp, decodeHealth(t, rec.Body.Bytes()).Status)
}

// A critical dependency being down takes the instance out of rotation; a
// non-critical one only marks it degraded, and it keeps serving.
func TestHealth_criticalVersusDegraded(t *testing.T) {
	cases := []struct {
		name       string
		critical   bool
		wantStatus string
		wantHTTP   int
	}{
		{"critical", true, HealthDown, http.StatusServiceUnavailable},
		{"not critical", false, HealthDegraded, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newHTTPTestApp(t)
			e := NewHTTPServer(app, nil)
			RegisterHealthRoutes(e, HealthOptions{
				Only: []HealthCheck{{
					Name:     "widgets",
					Critical: tc.critical,
					Check:    func(context.Context) error { return errors.New("widget service is down") },
				}},
			})

			rec := doJSON(e, http.MethodGet, "/readyz", "")
			assert.Equal(t, tc.wantHTTP, rec.Code)

			report := decodeHealth(t, rec.Body.Bytes())
			assert.Equal(t, tc.wantStatus, report.Status)
			assert.Equal(t, HealthDown, report.Checks["widgets"].Status)
		})
	}
}

// The error text can name a host, a user or a bucket, and a probe route is often
// the one nobody remembers to put behind the gateway.
func TestHealth_errorTextIsHiddenInProduction(t *testing.T) {
	failing := []HealthCheck{{
		Name:  "vault",
		Check: func(context.Context) error { return errors.New("dial tcp 10.0.0.4:8200: refused") },
	}}

	prod := mustEnv(t, map[string]string{"ENV": "prod", "SERVICE": "svc"})
	app, err := NewApp(prod)
	require.NoError(t, err)

	report := CheckHealth(t.Context(), app, HealthOptions{Only: failing})
	assert.Equal(t, HealthDown, report.Checks["vault"].Status)
	assert.Empty(t, report.Checks["vault"].Error, "no internal detail in production")
	assert.Equal(t, "svc", report.Service)

	dev := mustEnv(t, map[string]string{"ENV": "dev"})
	devApp, err := NewApp(dev)
	require.NoError(t, err)

	report = CheckHealth(t.Context(), devApp, HealthOptions{Only: failing})
	assert.Contains(t, report.Checks["vault"].Error, "10.0.0.4",
		"outside production the reason is what the probe is for")

	// and the caller can override either way
	on := true
	report = CheckHealth(t.Context(), app, HealthOptions{Only: failing, Details: &on})
	assert.Contains(t, report.Checks["vault"].Error, "refused")
}

// The probe must not become the slowest of its dependencies added together.
func TestHealth_checksRunInParallelAndAreBounded(t *testing.T) {
	slow := func(context.Context) error {
		time.Sleep(80 * time.Millisecond)
		return nil
	}
	checks := []HealthCheck{
		{Name: "a", Check: slow}, {Name: "b", Check: slow},
		{Name: "c", Check: slow}, {Name: "d", Check: slow},
	}

	app := newTestApp(t)
	started := time.Now()
	report := CheckHealth(t.Context(), app, HealthOptions{Only: checks, Timeout: time.Second})
	elapsed := time.Since(started)

	assert.Equal(t, HealthUp, report.Status)
	assert.Less(t, elapsed, 250*time.Millisecond, "four 80ms checks must not take 320ms")
	assert.Len(t, report.Checks, 4)
}

func TestHealth_timeoutMakesACheckFail(t *testing.T) {
	app := newTestApp(t)

	report := CheckHealth(t.Context(), app, HealthOptions{
		Timeout: 20 * time.Millisecond,
		Only: []HealthCheck{{
			Name:     "stuck",
			Critical: true,
			Check: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
		}},
	})

	assert.Equal(t, HealthDown, report.Status)
}

// The probe is the last thing that should take the process down.
func TestHealth_panicInACheckIsContained(t *testing.T) {
	app := newTestApp(t)

	report := CheckHealth(t.Context(), app, HealthOptions{
		Only: []HealthCheck{{
			Name:     "rude",
			Critical: true,
			Check:    func(context.Context) error { panic("client exploded") },
		}},
	})

	assert.Equal(t, HealthDown, report.Status)
	assert.Equal(t, "panic in health check", report.Checks["rude"].Error)
}

// Only the dependencies this deployment actually has are probed — a service with
// no redis should not report a cache that is always down.
func TestHealth_appChecksCoverOnlyWhatIsConfigured(t *testing.T) {
	env := mustEnv(t, map[string]string{"ENV": "test"})
	app, err := NewApp(env)
	require.NoError(t, err)
	assert.Empty(t, AppHealthChecks(app))

	db := newHealthTestDB(t)
	withDB, err := NewApp(env, WithSQL("default", db), WithCache("sessions", NewMemoryCache()))
	require.NoError(t, err)

	names := make([]string, 0)
	for _, c := range AppHealthChecks(withDB) {
		names = append(names, c.Name)
	}
	assert.ElementsMatch(t, []string{"database", "cache.sessions"}, names,
		"the default connection keeps the bare name; the others are qualified")

	report := CheckHealth(t.Context(), withDB)
	assert.Equal(t, HealthUp, report.Status)
}

func TestHealth_extraChecksAddToTheAppsOwn(t *testing.T) {
	db := newHealthTestDB(t)
	app, err := NewApp(mustEnv(t, map[string]string{"ENV": "test"}), WithSQL("default", db))
	require.NoError(t, err)

	report := CheckHealth(t.Context(), app, HealthOptions{
		Checks: []HealthCheck{{Name: "partner-api", Check: func(context.Context) error { return nil }}},
	})

	assert.Contains(t, report.Checks, "database")
	assert.Contains(t, report.Checks, "partner-api")
}

// A disabled dependency returns a typed nil IError, which is not a nil error.
// Without unwrapping it every probe would report every dependency as down.
func TestHealth_typedNilIsNotAFailure(t *testing.T) {
	var err IError
	assert.NoError(t, errOrNil(err))
	assert.Error(t, errOrNil(New(500, "X", "boom")))
}
