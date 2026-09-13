package core

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// linesFor returns the captured lines with the given message.
func linesFor(lines []capturedLine, msg string) []capturedLine {
	out := make([]capturedLine, 0)
	for _, l := range lines {
		if l.msg == msg {
			out = append(out, l)
		}
	}
	return out
}

// field reads a key from a captured line's alternating key/value args.
func logField(t *testing.T, l capturedLine, key string) any {
	t.Helper()
	for i := 0; i+1 < len(l.args); i += 2 {
		if k, ok := l.args[i].(string); ok && k == key {
			return l.args[i+1]
		}
	}
	t.Fatalf("no field %q in %v", key, l.args)
	return nil
}

func appWithCapture(t *testing.T, opts ...Option) (*App, *[]capturedLine) {
	t.Helper()
	lines := &[]capturedLine{}
	opts = append(opts, WithLogger(captureLogger{lines: lines}))
	app, err := NewApp(mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "svc"}), opts...)
	require.NoError(t, err)
	return app, lines
}

// Every capability degrades instead of refusing to boot, so a missing
// connection is invisible until the first request that needed it. One line at
// startup says which ones this process actually has.
func TestStartupLog_capabilities(t *testing.T) {
	app, lines := appWithCapture(t,
		WithSQL("default", newHealthTestDB(t)),
		WithSQL("replica", newHealthTestDB(t)),
		WithCache("default", NewMemoryCache()),
		WithMailer(NewMemoryMailer()),
	)

	app.LogCapabilities()

	got := linesFor(*lines, "app ready")
	require.Len(t, got, 1)

	assert.Equal(t, []string{"default", "replica"}, logField(t, got[0], "sql"),
		"named connections are listed by name, sorted")
	assert.Equal(t, []string{"default"}, logField(t, got[0], "cache"))
	assert.Equal(t, []string{}, logField(t, got[0], "mongo"), "an absent connection reads as empty, not missing")
	assert.Equal(t, true, logField(t, got[0], "mailer"))
	assert.Equal(t, false, logField(t, got[0], "mq"))
	assert.Equal(t, false, logField(t, got[0], "storage"))
	assert.Equal(t, "test", logField(t, got[0], "env"))
}

// Building the container is not the same event as starting the process, and
// every test builds one.
func TestStartupLog_newAppIsSilent(t *testing.T) {
	_, lines := appWithCapture(t)
	assert.Empty(t, linesFor(*lines, "app ready"),
		"NewApp is a constructor; the line belongs to whatever starts the process")
}

// "job runner started, workers=4" says a worker is up. It does not say what it
// is a worker for, which is the thing nobody can recover from the code at 3am.
func TestStartupLog_jobRegistry(t *testing.T) {
	app, lines := appWithCapture(t)

	reg := NewJobRegistry()
	require.NoError(t, reg.Register(JobDef{
		Name:          "nightly-report",
		Schedule:      Cron("0 2 * * *"),
		Timeout:       90 * time.Second,
		MaxAttempts:   3,
		MaxConcurrent: 1,
		Concurrency:   ConcurrencySkip,
	}, func(ICronjobContext) error { return nil }))
	require.NoError(t, reg.Register(JobDef{Name: "backfill"}, func(ICronjobContext) error { return nil }))

	r := NewJobRunner(app, reg)
	r.Start()
	t.Cleanup(func() { _ = r.Stop(t.Context()) })

	got := linesFor(*lines, "job registered")
	require.Len(t, got, 2)

	// sorted by name: backfill first
	assert.Equal(t, "backfill", logField(t, got[0], "job"))
	assert.Equal(t, "manual", logField(t, got[0], "trigger"), "a job with no schedule says so")
	assert.Equal(t, DefaultJobTimeout.String(), logField(t, got[0], "timeout"),
		"the effective default, not the zero the struct carries")
	assert.Equal(t, DefaultQueue, logField(t, got[0], "queue"))

	assert.Equal(t, "nightly-report", logField(t, got[1], "job"))
	assert.Equal(t, "cron(0 2 * * *)", logField(t, got[1], "schedule"))
	assert.Equal(t, "1m30s", logField(t, got[1], "timeout"))
	assert.Equal(t, 3, logField(t, got[1], "attempts"))
	assert.Equal(t, 1, logField(t, got[1], "max_concurrent"))
	assert.Equal(t, "skip", logField(t, got[1], "on_conflict"))
}

// A worker with an empty registry starts perfectly happily and then does
// nothing forever.
func TestStartupLog_emptyRegistryIsAWarning(t *testing.T) {
	app, lines := appWithCapture(t)

	r := NewJobRunner(app, NewJobRegistry())
	r.Start()
	t.Cleanup(func() { _ = r.Stop(t.Context()) })

	got := linesFor(*lines, "job runner has no jobs registered")
	require.Len(t, got, 1)
	assert.Equal(t, "warn", got[0].level)
}

// The schedule and the job list used to be two lines per job saying one thing:
// "job registered … schedule=every(1m0s)" then "job scheduled … schedule=…
// next_run=…". They are one line now, and the runner waits for the scheduler so
// that line can carry the times.
func TestStartupLog_scheduledJobIsOneLineWithItsNextRun(t *testing.T) {
	app, lines := appWithCapture(t)

	sc, err := NewScheduler(app)
	require.NoError(t, err)
	require.NoError(t, sc.AddByDuration("heartbeat", time.Hour, func(ICronjobContext) error { return nil }))
	require.NoError(t, sc.Add(JobDef{Name: "manual-only"}, func(ICronjobContext) error { return nil }))

	require.NoError(t, sc.Start())
	t.Cleanup(func() { _ = sc.Stop() })

	assert.Empty(t, linesFor(*lines, "job scheduled"),
		"the separate schedule line is gone; its content moved into job registered")

	got := linesFor(*lines, "job registered")
	require.Len(t, got, 2, "one line per job, scheduled or not")

	assert.Equal(t, "heartbeat", logField(t, got[0], "job"))
	assert.Equal(t, "every(1h0m0s)", logField(t, got[0], "schedule"))

	next, ok := logField(t, got[0], "next_run").(string)
	require.True(t, ok, "a scheduled job carries when it fires next")
	when, perr := time.Parse(time.RFC3339, next)
	require.NoError(t, perr)
	assert.True(t, when.After(time.Now()))

	assert.Equal(t, "manual-only", logField(t, got[1], "job"))
	assert.Equal(t, "manual", logField(t, got[1], "trigger"),
		"a manual job has no next run to report")

	summary := linesFor(*lines, "scheduler started")
	require.Len(t, summary, 1)
	assert.Equal(t, 1, logField(t, summary[0], "scheduled"))
}

// Both the runner and the scheduler have a reason to print the list; only one of
// them should win.
func TestStartupLog_jobListIsPrintedOnce(t *testing.T) {
	app, lines := appWithCapture(t)

	sc, err := NewScheduler(app)
	require.NoError(t, err)
	require.NoError(t, sc.AddByDuration("heartbeat", time.Hour, func(ICronjobContext) error { return nil }))

	// the scheduler owns this runner, so Start starts both
	require.NoError(t, sc.Start())
	t.Cleanup(func() { _ = sc.Stop() })

	assert.Len(t, linesFor(*lines, "job registered"), 1,
		"the runner defers to the scheduler rather than printing early without the times")
}

// Without a scheduler there is nothing to wait for, so the runner prints the
// list itself — with no next_run to add.
func TestStartupLog_runnerWithoutASchedulerStillLists(t *testing.T) {
	app, lines := appWithCapture(t)

	reg := NewJobRegistry()
	require.NoError(t, reg.Register(JobDef{Name: "backfill"}, func(ICronjobContext) error { return nil }))

	r := NewJobRunner(app, reg)
	r.Start()
	t.Cleanup(func() { _ = r.Stop(t.Context()) })

	got := linesFor(*lines, "job registered")
	require.Len(t, got, 1)
	assert.Equal(t, "manual", logField(t, got[0], "trigger"))
}

// A boot log that reorders itself between restarts cannot be diffed.
func TestStartupLog_subscriberListsAreSorted(t *testing.T) {
	app, lines := appWithCapture(t, WithCache("default", NewMemoryCache()))

	sub := app.NewSubscriber()
	for _, ch := range []string{"zeta", "alpha", "mid"} {
		sub.On(ch, func(IContext, *PubSubMessage) error { return nil })
	}
	require.NoError(t, sub.Start())
	t.Cleanup(func() { _ = sub.Stop(t.Context()) })

	got := linesFor(*lines, "pubsub subscriber started")
	require.Len(t, got, 1)
	assert.Equal(t, []string{"alpha", "mid", "zeta"}, logField(t, got[0], "channels"))
	assert.Equal(t, defaultSubscriberTimeout.String(), logField(t, got[0], "handler_timeout"))
}

func TestStartupLog_capabilitiesNamesAreStable(t *testing.T) {
	names := connNames(map[string]int{"z": 1, "a": 1, "m": 1})
	assert.Equal(t, []string{"a", "m", "z"}, names)
	assert.Equal(t, []string{}, connNames(map[string]int{}),
		"an empty list must still render, so the absence is visible")
}

func TestStartupLog_capabilitiesLineIsOneLine(t *testing.T) {
	app, lines := appWithCapture(t)
	app.LogCapabilities()

	got := linesFor(*lines, "app ready")
	require.Len(t, got, 1)
	assert.False(t, strings.Contains(got[0].msg, "\n"))
}
