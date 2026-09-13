package core

import (
	"context"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduler_addJobs(t *testing.T) {
	app := newTestApp(t)
	sc, err := NewScheduler(app)
	require.NoError(t, err)
	defer sc.Stop()

	assert.NoError(t, sc.AddByCron("c", "0 0 * * *", func(ICronjobContext) error { return nil }))
	assert.Error(t, sc.AddByCron("bad", "not-a-cron", func(ICronjobContext) error { return nil }),
		"invalid cron expression should error at registration")
	assert.Error(t, sc.AddByCron("c", "0 0 * * *", func(ICronjobContext) error { return nil }),
		"registering the same job twice should error")
}

// A tick must not execute the job inline — it enqueues a run, which the runner
// then executes. That single path is what gives scheduled jobs status and logs.
func TestScheduler_tickEnqueuesAndRunnerExecutes(t *testing.T) {
	app := newTestApp(t)
	sc, err := NewScheduler(app)
	require.NoError(t, err)

	ran := make(chan string, 1)
	require.NoError(t, sc.AddByDuration("tick", 10*time.Millisecond, func(c ICronjobContext) error {
		select {
		case ran <- c.RunID():
		default:
		}
		return nil
	}))
	require.NoError(t, sc.Start())
	defer sc.Stop()

	var runID string
	select {
	case runID = <-ran:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled job did not run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	run, rerr := sc.Runner().Wait(ctx, runID)
	require.NoError(t, rerr)
	assert.Equal(t, RunSucceeded, run.Status)
	assert.Equal(t, TriggerSchedule, run.Trigger, "a tick is recorded as a scheduled trigger")
	assert.Equal(t, "scheduler", run.TriggeredBy)
}

func TestScheduler_pausedJobIsNotEnqueued(t *testing.T) {
	app := newTestApp(t)
	sc, err := NewScheduler(app)
	require.NoError(t, err)
	defer sc.Stop()

	require.NoError(t, sc.AddByDuration("paused-job", 10*time.Millisecond,
		func(ICronjobContext) error { return nil }))
	require.NoError(t, sc.Runner().Pause("paused-job"))
	require.NoError(t, sc.Start())

	time.Sleep(150 * time.Millisecond)
	page, lerr := sc.Runner().Runs(context.Background(), JobRunFilter{JobName: "paused-job"})
	require.NoError(t, lerr)
	assert.Zero(t, page.Total, "a paused job must not be scheduled")
}

func TestSchedule_strings(t *testing.T) {
	assert.Equal(t, "cron(0 2 * * *)", Cron("0 2 * * *").String())
	assert.Equal(t, "every(30s)", Every(30*time.Second).String())
	assert.Equal(t, "cron(0 2 * * * Asia/Bangkok)", CronIn(bangkok(t), "0 2 * * *").String(),
		"a boot log that does not name the zone cannot be checked against the intended hour")
}

func TestCronIn_carriesTheZoneAsACronTZPrefix(t *testing.T) {
	tz := bangkok(t)

	assert.Equal(t, "CRON_TZ=Asia/Bangkok 0 22 * * *", CronIn(tz, "0 22 * * *").(cronSchedule).crontab())
	assert.Equal(t, "CRON_TZ=Asia/Bangkok 30 2 * * *", CronWithSecondsIn(tz, "30 2 * * *").(cronSchedule).crontab())
	assert.Equal(t, "0 22 * * *", Cron("0 22 * * *").(cronSchedule).crontab(),
		"without a zone the expression is handed over untouched, so gocron's own location applies")
	assert.Equal(t, "0 22 * * *", CronIn(tz, "0 22 * * *").(cronSchedule).expr,
		"the bare expression survives: it is what Sentry's monitor config is built from")
}

// The end-to-end claim: gocron reads the hour in the zone the job asked for, not
// the one the process happens to run in. This is the regression that made a
// nightly 22:00 job fire at 05:00 after a service moved into a UTC container.
func TestCronIn_armsAtTheHourInThatZone(t *testing.T) {
	tz := bangkok(t)
	app := newTestApp(t)
	sc, err := NewScheduler(app)
	require.NoError(t, err)

	require.NoError(t, sc.Add(
		JobDef{Name: "nightly", Schedule: CronIn(tz, "0 22 * * *")},
		func(ICronjobContext) error { return nil }))
	require.NoError(t, sc.Add(
		JobDef{Name: "nightly-utc", Schedule: CronIn(time.UTC, "0 22 * * *")},
		func(ICronjobContext) error { return nil }))
	require.NoError(t, sc.Start())
	defer sc.Stop()

	next, ok := sc.nextRunOf("nightly")
	require.True(t, ok, "an armed job must report its next run")
	assert.Equal(t, "22:00", next.In(tz).Format("15:04"),
		"next run is at 22:00 Bangkok time, whatever the process zone is")

	// Read the same two jobs on one clock. The next runs are instants and may
	// fall on different days, but their time of day cannot be equal: a scheduler
	// that ignored the zone would put both at the same hour, and a host that
	// happens to run in one of these zones would let the assertion above pass.
	utc, ok := sc.nextRunOf("nightly-utc")
	require.True(t, ok)
	assert.Equal(t, "15:00", next.UTC().Format("15:04"), "22:00 in Bangkok is 15:00 UTC")
	assert.Equal(t, "22:00", utc.UTC().Format("15:04"), "22:00 in UTC is 22:00 UTC")
}

// One zone for the whole scheduler, set once — and a job that names its own
// still wins, so a database column an operator edits keeps its meaning.
func TestWithSchedulerLocation_appliesToEveryScheduleThatDidNotNameOne(t *testing.T) {
	tz := bangkok(t)
	app := newTestApp(t)
	sc, err := NewScheduler(app, WithSchedulerLocation(tz))
	require.NoError(t, err)

	noop := func(ICronjobContext) error { return nil }
	require.NoError(t, sc.AddByCron("plain", "0 22 * * *", noop))
	require.NoError(t, sc.Add(
		JobDef{Name: "override", Schedule: CronIn(time.UTC, "0 22 * * *")}, noop))
	require.NoError(t, sc.Start())
	defer sc.Stop()

	plain, ok := sc.nextRunOf("plain")
	require.True(t, ok)
	assert.Equal(t, "15:00", plain.UTC().Format("15:04"),
		"a plain Cron is read in the scheduler's zone: 22:00 Bangkok is 15:00 UTC")

	override, ok := sc.nextRunOf("override")
	require.True(t, ok)
	assert.Equal(t, "22:00", override.UTC().Format("15:04"),
		"CronIn names its own zone and overrides the scheduler's")

	assert.Equal(t, "Asia/Bangkok", sc.zone(), "the boot log reports the zone in force")
}

// NewScheduler(app, runner) is the form every service that shares a runner
// already uses; widening the parameter to an option must not change it.
func TestNewScheduler_takesARunnerAndOptionsInEitherForm(t *testing.T) {
	app := newTestApp(t)
	runner := NewJobRunner(app, NewJobRegistry())

	shared, err := NewScheduler(app, runner)
	require.NoError(t, err)
	defer shared.Stop()
	assert.Same(t, runner, shared.Runner(), "the runner passed in is the runner used")

	own, err := NewScheduler(app)
	require.NoError(t, err)
	defer own.Stop()
	assert.NotSame(t, runner, own.Runner(), "no runner passed: the scheduler builds its own")

	var missing *JobRunner
	both, err := NewScheduler(app, missing, WithSchedulerLocation(bangkok(t)))
	require.NoError(t, err)
	defer both.Stop()
	assert.NotNil(t, both.Runner(), "a nil runner is not a runner — build one rather than panic later")
	assert.Equal(t, "Asia/Bangkok", both.zone())
}

// LoadLocation reads the host's zone database, which a Windows dev box or a
// scratch container may not have — time/tzdata is imported by this test file so
// the answer is the same everywhere.
func bangkok(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Bangkok")
	require.NoError(t, err)
	return loc
}
