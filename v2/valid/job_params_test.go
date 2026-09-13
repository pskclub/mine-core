package valid_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/valid"
)

// ReportParams validates exactly like an HTTP request payload: the job's run
// context is a core.IContext, so the same builder — including the DB-backed
// rules — works unchanged.
type ReportParams struct {
	Date  *string `json:"date"`
	Email *string `json:"email"`
	Limit *int64  `json:"limit"`
}

func (p *ReportParams) Valid(ctx core.IContext) core.IError {
	v := valid.New(ctx)
	v.Str("date", p.Date).Required().Date("2006-01-02")
	v.Str("email", p.Email).Email().Unique("users", "email")
	v.Int("limit", p.Limit).Between(1, 100)
	return v.Error()
}

// newJobApp builds an App over an in-memory database, seeded with the given
// user emails (for the Unique rule).
func newJobApp(t *testing.T, seed ...string) *core.App {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT)`)
	for _, e := range seed {
		db.Exec(`INSERT INTO users (email) VALUES (?)`, e)
	}
	t.Setenv("APP_ENV", "test")
	t.Setenv("APP_SERVICE", "test")
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	app, err := core.NewApp(env, core.WithSQL("default", db))
	require.NoError(t, err)
	return app
}

// runJobWithParams registers a job, triggers it with params and waits.
func runJobWithParams(t *testing.T, app *core.App, params any) (*core.JobRun, *ReportParams) {
	t.Helper()
	reg := core.NewJobRegistry()
	seen := make(chan *ReportParams, 1)
	require.NoError(t, core.RegisterJob(reg, core.JobDef{Name: "report"},
		func(c core.ICronjobContext, p *ReportParams) error {
			seen <- p
			return nil
		}))

	runner := core.NewJobRunner(app, reg)
	runner.Start()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = runner.Stop(stopCtx)
	})

	run, err := runner.Trigger(context.Background(), "report", params)
	require.NoError(t, err)

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done, werr := runner.Wait(waitCtx, run.ID)
	require.NoError(t, werr)

	select {
	case p := <-seen:
		return done, p
	default:
		return done, nil
	}
}

func TestJobParams_validBuilderPasses(t *testing.T) {
	app := newJobApp(t)
	run, params := runJobWithParams(t, app, map[string]any{
		"date":  "2026-07-01",
		"email": "new@example.com",
		"limit": 50,
	})

	assert.Equal(t, core.RunSucceeded, run.Status)
	require.NotNil(t, params)
	assert.Equal(t, "2026-07-01", *params.Date)
}

func TestJobParams_violationsFailTheRunWithFields(t *testing.T) {
	app := newJobApp(t)
	run, params := runJobWithParams(t, app, map[string]any{
		"date":  "01-07-2026", // wrong layout
		"email": "not-an-email",
		"limit": 500, // out of range
	})

	assert.Nil(t, params, "an invalid run must never reach the handler")
	assert.Equal(t, core.RunFailed, run.Status)
	require.NotNil(t, run.Error)
	assert.Equal(t, "INVALID_PARAMS", run.Error.Code)
	assert.Equal(t, 400, run.Error.Status)

	// the per-field violations survive onto the run, in the same shape the HTTP
	// layer returns — so an operator sees *which* parameter was wrong
	require.NotEmpty(t, run.Error.Fields)
	var fields map[string]struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(run.Error.Fields, &fields))
	assert.Equal(t, "INVALID_DATE", fields["date"].Code)
	assert.Equal(t, "INVALID_EMAIL", fields["email"].Code)
	assert.Equal(t, "INVALID_NUMBER_BETWEEN", fields["limit"].Code)
	assert.NotEmpty(t, fields["date"].Message)
}

func TestJobParams_requiredIsEnforcedOnScheduledRuns(t *testing.T) {
	app := newJobApp(t)
	// a scheduled tick passes no params at all: the zero value must still be
	// validated, not silently accepted
	run, params := runJobWithParams(t, app, nil)

	assert.Nil(t, params)
	assert.Equal(t, core.RunFailed, run.Status)
	require.NotNil(t, run.Error)
	assert.Equal(t, "INVALID_PARAMS", run.Error.Code)
}

// DB-backed rules work inside a job because the run context carries the same
// connections as an HTTP request.
func TestJobParams_dbRuleRunsInsideAJob(t *testing.T) {
	app := newJobApp(t, "taken@example.com")
	run, params := runJobWithParams(t, app, map[string]any{
		"date":  "2026-07-01",
		"email": "taken@example.com",
	})

	assert.Nil(t, params)
	assert.Equal(t, core.RunFailed, run.Status)
	require.NotNil(t, run.Error)

	var fields map[string]struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(run.Error.Fields, &fields))
	assert.Equal(t, "UNIQUE", fields["email"].Code)
}
