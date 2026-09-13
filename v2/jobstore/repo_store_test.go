//go:build integration

package jobstore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/jobstore"
	"github.com/pskclub/mine-core/v2/repository"
)

// newApp builds an App over db with a test environment.
func newApp(t *testing.T, db *gorm.DB) *core.App {
	t.Helper()
	t.Setenv("APP_ENV", "test")
	t.Setenv("APP_SERVICE", "jobstore-test")
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	app, err := core.NewApp(env, core.WithSQL("default", db))
	require.NoError(t, err)
	return app
}

// newSQLite opens a scratch database. It is file-backed on purpose: GORM pools
// connections, and every connection to an ":memory:" sqlite gets its *own* empty
// database, so the migrated schema would be invisible to half the queries.
func newSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "jobs.db")), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, cerr := db.DB(); cerr == nil {
			_ = sqlDB.Close() // release the file before TempDir removes it
		}
	})
	return db
}

// sqliteApp is the always-available backend: a real SQL engine, no infrastructure.
func sqliteApp(t *testing.T, opts ...jobstore.Option) *core.App {
	t.Helper()
	db := newSQLite(t)
	require.NoError(t, jobstore.Migrate(db, opts...))
	return newApp(t, db)
}

// configuredApp connects to whatever DB test.env points at (MySQL, Postgres, …)
// so the same suite can be run against the engine a service actually uses. It
// skips when nothing is configured, rather than failing the build.
func configuredApp(t *testing.T) *core.App {
	t.Helper()
	t.Setenv("APP_ENV", "test")
	env, err := core.NewEnv()
	if err != nil {
		t.Skipf("no test.env: %v", err)
	}
	cfg := env.Config()
	if cfg.DBConnectionString == "" && cfg.DBHost == "" {
		t.Skip("no database configured in test.env (set APP_DB_* or APP_DB_CONNECTION_STRING)")
	}
	db, derr := core.NewDatabase(env)
	if derr != nil {
		t.Skipf("cannot reach the configured database: %v", derr)
	}
	require.NoError(t, jobstore.Migrate(db))
	t.Cleanup(func() {
		db.Exec("DELETE FROM job_run_logs")
		db.Exec("DELETE FROM job_runs")
	})
	app, aerr := core.NewApp(env, core.WithSQL("default", db))
	require.NoError(t, aerr)
	return app
}

// backends returns every store/queue pair the suite runs against. Adding a new
// backend means adding one line here.
func backends(t *testing.T) []backend {
	t.Helper()
	out := []backend{
		{
			name:  "memory",
			store: core.NewMemoryJobStore(),
			queue: core.NewMemoryJobQueue(),
		},
	}
	app := sqliteApp(t)
	out = append(out, backend{
		name:  "repo/sqlite",
		store: jobstore.New(app),
		queue: jobstore.NewQueue(app, jobstore.WithPollInterval(20*time.Millisecond)),
	})
	return out
}

func TestStoreConformance(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) { runStoreConformance(t, b) })
	}
}

func TestQueueConformance(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Cleanup(func() { _ = b.queue.Close() })
			runQueueConformance(t, b)
		})
	}
}

// The same suite against the database the service really uses — MySQL, Postgres
// and friends differ on types, time precision and locking, which is exactly what
// this catches.
func TestConformance_configuredDatabase(t *testing.T) {
	app := configuredApp(t)
	b := backend{
		name:  "repo/configured",
		store: jobstore.New(app),
		queue: jobstore.NewQueue(app, jobstore.WithPollInterval(50*time.Millisecond)),
	}
	t.Cleanup(func() { _ = b.queue.Close() })
	t.Run("store", func(t *testing.T) { runStoreConformance(t, b) })
	t.Run("queue", func(t *testing.T) { runQueueConformance(t, b) })
}

// ---------------------------------------------------------------------------
// Behaviour specific to the repository-backed backend
// ---------------------------------------------------------------------------

func TestRepoStore_customTablesAndConnection(t *testing.T) {
	opts := []jobstore.Option{jobstore.WithTables("ops_job_runs", "ops_job_run_logs")}
	app := sqliteApp(t, opts...)
	ctx := ctxT(t)

	store := jobstore.New(app, opts...)
	run := newRun("custom-tables")
	require.NoError(t, store.Create(ctx, run))
	require.NoError(t, store.AppendLogs(ctx, []core.JobLog{
		{RunID: run.ID, Seq: 1, At: time.Now(), Level: "info", Message: "hello"},
	}))

	db := app.NewContext(context.Background()).DB()
	var runs, logs int64
	require.NoError(t, db.Table("ops_job_runs").Count(&runs).Error)
	require.NoError(t, db.Table("ops_job_run_logs").Count(&logs).Error)
	assert.EqualValues(t, 1, runs, "rows land in the configured table, not the default one")
	assert.EqualValues(t, 1, logs)

	assert.Error(t, db.Table("job_runs").Count(&runs).Error,
		"the default table was never created")
}

// Job history is an ordinary model, so a service can query and aggregate it with
// the repository it already uses — that is the whole point of building the store
// on top of the repository rather than on raw GORM.
func TestRepoStore_historyIsQueryableWithTheRepository(t *testing.T) {
	app := sqliteApp(t)
	ctx := ctxT(t)
	store := jobstore.New(app)

	finished := time.Now()
	for i, status := range []core.RunStatus{core.RunSucceeded, core.RunFailed, core.RunSucceeded} {
		run := newRun("reportable", func(r *core.JobRun) {
			r.Status, r.FinishedAt = status, &finished
			r.DurationMS = int64((i + 1) * 100)
		})
		require.NoError(t, store.Create(ctx, run))
	}

	c := app.NewContext(context.Background())

	failed, err := repository.New[core.JobRun](c).
		Where("job_name = ? AND status = ?", "reportable", core.RunFailed).
		FindAll()
	require.NoError(t, err)
	assert.Len(t, failed, 1)

	var stats []struct {
		JobName string
		AvgMS   float64
	}
	require.NoError(t, repository.New[core.JobRun](c).
		Select("job_name, AVG(duration_ms) as avg_ms").
		Where("status = ?", core.RunSucceeded).
		Group("job_name").
		Scan(&stats))
	require.Len(t, stats, 1)
	assert.InDelta(t, 200, stats[0].AvgMS, 0.001, "(100 + 300) / 2")
}

func TestRepoQueue_enqueueIsIdempotentForRetries(t *testing.T) {
	app := sqliteApp(t)
	ctx := ctxT(t)
	store := jobstore.New(app)
	queue := jobstore.NewQueue(app, jobstore.WithPollInterval(20*time.Millisecond))
	t.Cleanup(func() { _ = queue.Close() })

	run := newRun("retryable")
	require.NoError(t, store.Create(ctx, run))
	require.NoError(t, queue.Enqueue(ctx, run))

	// a retry re-enqueues the *same* row with a later schedule and a new attempt
	run.Attempt, run.Trigger = 2, core.TriggerRetry
	run.ScheduledAt = time.Now().Add(50 * time.Millisecond)
	require.NoError(t, queue.Enqueue(ctx, run))

	n, err := queue.Len(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "re-enqueueing must not duplicate the run")

	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	got, ack, rerr := queue.Reserve(rctx, []string{core.DefaultQueue})
	require.NoError(t, rerr)
	ack(nil)
	assert.Equal(t, 2, got.Attempt)
	assert.Equal(t, core.TriggerRetry, got.Trigger)
}

// Regression: a run that is re-queued with a backoff (a retry, or waiting for a
// concurrency slot) must not be claimable until that backoff has elapsed. The
// claim re-checks scheduled_at at update time, not only when it lists
// candidates — otherwise a worker holding a stale candidate list runs the job
// immediately, and with the previous attempt's data.
func TestRepoQueue_claimRespectsABackoffAddedAfterTheSelect(t *testing.T) {
	app := sqliteApp(t)
	ctx := ctxT(t)
	store := jobstore.New(app)
	queue := jobstore.NewQueue(app, jobstore.WithPollInterval(10*time.Millisecond))
	t.Cleanup(func() { _ = queue.Close() })

	run := newRun("backoff")
	require.NoError(t, store.Create(ctx, run))

	first, ack, err := queue.Reserve(mustCtx(ctx, 5*time.Second), []string{core.DefaultQueue})
	require.NoError(t, err)
	ack(nil)
	require.Equal(t, run.ID, first.ID)

	// the attempt failed and is retried in two seconds
	run.Attempt, run.Trigger = 2, core.TriggerRetry
	run.ScheduledAt = time.Now().Add(2 * time.Second)
	require.NoError(t, queue.Enqueue(ctx, run))

	got, _, rerr := queue.Reserve(mustCtx(ctx, 500*time.Millisecond), []string{core.DefaultQueue})
	assert.Error(t, rerr, "nothing is due yet")
	assert.Nil(t, got)
}

func mustCtx(parent context.Context, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(parent, d)
	_ = cancel // the deadline does the work; the parent is already bounded
	return ctx
}

// A run that is queued when the process dies must still be there afterwards —
// the reason to use a durable backend at all.
func TestRepoQueue_queuedRunsSurviveARestart(t *testing.T) {
	db := newSQLite(t)
	require.NoError(t, jobstore.Migrate(db))
	app := newApp(t, db)
	ctx := ctxT(t)

	// "process one" queues the work and goes away
	store := jobstore.New(app)
	queue := jobstore.NewQueue(app, jobstore.WithPollInterval(20*time.Millisecond))
	run := newRun("survivor")
	require.NoError(t, store.Create(ctx, run))
	require.NoError(t, queue.Enqueue(ctx, run))
	require.NoError(t, queue.Close())

	// "process two" starts against the same database and finds it
	queue2 := jobstore.NewQueue(app, jobstore.WithPollInterval(20*time.Millisecond))
	t.Cleanup(func() { _ = queue2.Close() })
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	got, ack, rerr := queue2.Reserve(rctx, []string{core.DefaultQueue})
	require.NoError(t, rerr)
	ack(nil)
	assert.Equal(t, run.ID, got.ID)
}
