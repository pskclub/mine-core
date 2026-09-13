package devtools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newServer(t *testing.T, kv map[string]string) *core.Server {
	t.Helper()
	for k, v := range kv {
		t.Setenv("APP_"+k, v)
	}
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	app, ierr := core.NewApp(env)
	require.Nil(t, ierr)
	return core.NewHTTPServer(app, nil)
}

func get(s *core.Server, target string, headers ...map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

// requireAdmin stands in for whatever a service actually guards its admin area
// with.
func requireAdmin() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if c.Request().Header.Get("X-Admin") != "yes" {
				return core.New(http.StatusUnauthorized, "UNAUTHORIZED", "no")
			}
			return next(c)
		}
	}
}

// The panel publishes configuration keys, connection names and every route of
// the service. Mounting it unguarded outside dev has to fail loudly at startup,
// because the failure mode is silent: nothing breaks, the data is simply
// readable by whoever finds the path.
func TestMount_refusesUnprotectedOutsideDev(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "prod", "SERVICE": "svc"})

	err := Mount(s, Options{})
	require.NotNil(t, err, "mounting unprotected in prod must fail")
	assert.Equal(t, "DEVTOOLS_UNPROTECTED", err.GetCode())
	assert.Contains(t, err.GetMessage(), "prod", "the message should name the environment that refused")

	assert.Equal(t, http.StatusNotFound, get(s, DefaultPrefix).Code,
		"a refused mount must not leave routes behind")
}

// An unset APP_ENV is legal (env.validate only rejects an unknown one), and it
// is what a deployment that forgot to set it looks like. It must take the
// guarded path: the safe direction for "we do not know where this is running"
// is not dev.
func TestMount_refusesUnprotectedWithUnsetEnv(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "", "SERVICE": "svc"})

	err := Mount(s, Options{})
	require.NotNil(t, err)
	assert.Equal(t, "DEVTOOLS_UNPROTECTED", err.GetCode())
}

// With a guard supplied it mounts anywhere: a staging box is exactly where this
// is most useful, and the service decides what "authorised" means.
func TestMount_allowsProtectedOutsideDev(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "prod", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Auth: []echo.MiddlewareFunc{requireAdmin()}}))

	assert.Equal(t, http.StatusUnauthorized, get(s, DefaultPrefix+"/api/overview").Code,
		"the guard must run on the API, not only on the page")
	assert.Equal(t, http.StatusOK,
		get(s, DefaultPrefix+"/api/overview", map[string]string{"X-Admin": "yes"}).Code)
}

// A person types the prefix without a trailing slash; a bookmark keeps one.
func TestDevtools_servesPageBothSpellings(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{}))

	for _, target := range []string{DefaultPrefix, DefaultPrefix + "/"} {
		rec := get(s, target)
		require.Equal(t, http.StatusOK, rec.Code, target)
		assert.Contains(t, rec.Header().Get(echo.HeaderContentType), "text/html", target)
		assert.Contains(t, rec.Body.String(), "devtools", target)
		assert.Contains(t, rec.Body.String(), `const BASE = "`+DefaultPrefix+`"`,
			"the page must know where it was mounted")
	}
}

func TestDevtools_customPrefix(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Prefix: "internal/debug/"}))

	assert.Equal(t, http.StatusOK, get(s, "/internal/debug").Code)
	assert.Equal(t, http.StatusNotFound, get(s, DefaultPrefix).Code)
}

func TestNormalizePrefix(t *testing.T) {
	for in, want := range map[string]string{
		"":            DefaultPrefix,
		"/":           DefaultPrefix,
		"_dev":        "/_dev",
		"/_dev/":      "/_dev",
		" /admin/x/ ": "/admin/x",
	} {
		assert.Equal(t, want, normalizePrefix(in), in)
	}
}

// The question the route list exists to answer is "which function serves this?".
// echo cannot answer it — the registered handler is a closure — so the server
// records the name as each route goes in.
func TestDevtools_routesReportHandlerNames(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	s.GET("/users/:id", showUser)
	s.Group("/admin").POST("/reindex", showUser)
	require.Nil(t, Mount(s, Options{}))

	body := decode[struct {
		Items []routeEntry `json:"items"`
	}](t, get(s, DefaultPrefix+"/api/routes"))

	byPath := map[string]routeEntry{}
	for _, r := range body.Items {
		byPath[r.Method+" "+r.Path] = r
	}
	assert.Contains(t, byPath["GET /users/:id"].Handler, "showUser")
	assert.Contains(t, byPath["POST /admin/reindex"].Handler, "showUser",
		"a route registered through a group must be recorded under its full path")
	assert.Contains(t, byPath, "GET "+DefaultPrefix+"/api/routes",
		"devtools' own routes are part of what the service serves")
}

func showUser(c core.IHTTPContext) error { return c.NoContent(http.StatusOK) }

// A configuration panel that prints secrets is a credential dump. It still has
// to say whether a secret is *set*, which is the actual question behind "why
// does signing fail here and not locally".
func TestDevtools_configWithholdsSecrets(t *testing.T) {
	s := newServer(t, map[string]string{
		"ENV": "dev", "SERVICE": "svc",
		"JWT_SECRET": "super-secret-value",
		"DB_HOST":    "db.internal",
	})
	require.Nil(t, Mount(s, Options{}))

	body := decode[struct {
		Items []configEntry `json:"items"`
	}](t, get(s, DefaultPrefix+"/api/config"))
	byKey := map[string]configEntry{}
	for _, e := range body.Items {
		byKey[e.Key] = e
	}

	secret := byKey["jwt_secret"]
	assert.True(t, secret.Secret)
	assert.True(t, secret.Set, "a set secret must be reported as set")
	assert.Empty(t, secret.Value, "the value itself must never be serialized")
	assert.NotContains(t, get(s, DefaultPrefix+"/api/config").Body.String(), "super-secret-value")

	assert.Equal(t, "db.internal", byKey["db_host"].Value, "ordinary keys are the point of the tab")
}

func TestIsSecret(t *testing.T) {
	for _, key := range []string{
		"jwt_secret", "db_password", "cache_password", "s3_access_key",
		"ai_api_key", "firebase_credential", "sentry_dsn", "db_connection_string",
	} {
		assert.True(t, isSecret(key), key)
	}
	for _, key := range []string{"db_host", "service", "env", "s3_bucket", "log_level"} {
		assert.False(t, isSecret(key), key)
	}
}

// Capabilities is what makes "the cache is not caching" answerable: a service
// with no CACHE_* runs perfectly well on a cache that misses every read.
func TestDevtools_overviewReportsDisabledCapabilities(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{}))

	o := decode[overviewResponse](t, get(s, DefaultPrefix+"/api/overview"))
	assert.Equal(t, "svc", o.Service)
	assert.Equal(t, "dev", o.Env)
	assert.False(t, o.Protected, "a dev mount with no Auth must say so, so the page can warn")

	kinds := map[string]bool{}
	for _, c := range o.Caps {
		kinds[c.Kind] = c.Enabled
	}
	require.Contains(t, kinds, "storage")
	assert.False(t, kinds["storage"], "no S3_* configuration means disabled storage, not a missing entry")
	assert.False(t, kinds["mq"])
	assert.Positive(t, o.Runtime.Goroutines)
}

func TestDevtools_jobsAndRuns(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})

	registry := core.NewJobRegistry()
	require.Nil(t, core.RegisterJob(registry, core.JobDef{
		Name:        "sales-report",
		Description: "daily sales",
		Queue:       "reports",
		MaxAttempts: 3,
	}, func(c core.ICronjobContext, p reportParams) error { return nil }))

	store := core.NewMemoryJobStore()
	started := time.Now().Add(-time.Minute)
	require.Nil(t, store.Create(t.Context(), &core.JobRun{
		ID: "run-1", JobName: "sales-report", Queue: "reports",
		Status: core.RunFailed, Trigger: core.TriggerManual,
		Attempt: 2, MaxAttempts: 3, StartedAt: &started, DurationMS: 1500,
		Error: &core.RunError{Code: "BOOM", Message: "exploded"},
	}))
	require.Nil(t, store.AppendLogs(t.Context(), []core.JobLog{
		{RunID: "run-1", Seq: 1, At: started, Level: "error", Message: "exploded"},
	}))

	require.Nil(t, Mount(s, Options{Registry: registry, Store: store}))

	jobs := decode[struct {
		Items []core.JobInfo `json:"items"`
	}](t, get(s, DefaultPrefix+"/api/jobs"))
	require.Len(t, jobs.Items, 1)
	assert.Equal(t, "reports", jobs.Items[0].Queue)
	require.NotEmpty(t, jobs.Items[0].Params, "the parameter schema is what a trigger form is built from")
	assert.Equal(t, "date", jobs.Items[0].Params[0].Name)

	runs := decode[core.Page[core.JobRun]](t, get(s, DefaultPrefix+"/api/runs?status=failed"))
	require.Len(t, runs.Items, 1)
	assert.Equal(t, "run-1", runs.Items[0].ID)

	assert.Empty(t, decode[core.Page[core.JobRun]](t,
		get(s, DefaultPrefix+"/api/runs?status=succeeded")).Items,
		"the status filter has to actually filter, or the panel lies about what ran")

	run := decode[core.JobRun](t, get(s, DefaultPrefix+"/api/runs/run-1"))
	require.NotNil(t, run.Error)
	assert.Equal(t, "BOOM", run.Error.Code)

	logs := decode[struct {
		Items []core.JobLog `json:"items"`
	}](t, get(s, DefaultPrefix+"/api/runs/run-1/logs"))
	require.Len(t, logs.Items, 1)
	assert.Equal(t, "exploded", logs.Items[0].Message)
}

type reportParams struct {
	Date string `json:"date"`
}

// An empty jobs tab has two very different causes, and the panel must not make
// them look alike.
func TestDevtools_missingJobWiringIsNamed(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{}))

	rec := get(s, DefaultPrefix+"/api/jobs")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "Options.Registry")

	o := decode[overviewResponse](t, get(s, DefaultPrefix+"/api/overview"))
	assert.False(t, o.Jobs.Registry)
	assert.Equal(t, -1, o.Jobs.Queued, "no queue to ask is not a depth of zero")
}

func TestDevtools_healthProbesDependencies(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{}))

	report := decode[core.HealthReport](t, get(s, DefaultPrefix+"/api/health"))
	assert.Equal(t, core.HealthUp, report.Status, "a service with no dependencies is healthy")
	assert.Equal(t, "svc", report.Service)
}
