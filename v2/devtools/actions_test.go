package devtools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func post(s *core.Server, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// newRunnerFixture is a service that has jobs but runs no workers — the shape of
// an API process whose jobs execute elsewhere, which is where triggering from a
// panel is most useful.
func newRunnerFixture(t *testing.T) (*core.Server, *core.JobRunner) {
	t.Helper()
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})

	registry := core.NewJobRegistry()
	require.Nil(t, core.RegisterJob(registry, core.JobDef{Name: "sales-report"},
		func(c core.ICronjobContext, p reportParams) error { return nil }))
	require.Nil(t, registry.Register(core.JobDef{Name: "cleanup"}, func(c core.ICronjobContext) error { return nil }))

	return s, core.NewJobRunner(s.App(), registry)
}

// The read and the write halves are separate permissions: "show me what this
// process is doing" is granted widely, "re-run last night's billing job" is not.
func TestActions_refusedOnAReadOnlyMount(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner})) // AllowWrite left off

	for _, target := range []string{
		DefaultPrefix + "/api/jobs/sales-report/trigger",
		DefaultPrefix + "/api/jobs/sales-report/pause",
		DefaultPrefix + "/api/jobs/sales-report/resume",
		DefaultPrefix + "/api/runs/whatever/cancel",
		DefaultPrefix + "/api/runs/whatever/replay",
	} {
		rec := post(s, target, `{}`)
		assert.Equal(t, http.StatusForbidden, rec.Code, target)
		assert.Contains(t, rec.Body.String(), "AllowWrite",
			"the refusal should name the switch that would allow it")
	}

	// Registered rather than absent: a 404 here reads as a version mismatch and
	// sends somebody looking in the wrong place.
	assert.NotEqual(t, http.StatusNotFound, post(s, DefaultPrefix+"/api/jobs/sales-report/pause", "").Code)
}

// AllowWrite without a Runner is a wiring mistake rather than a decision, and
// the two must not report the same way.
func TestActions_writableWithoutARunnerSaysSo(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{AllowWrite: true}))

	rec := post(s, DefaultPrefix+"/api/jobs/x/trigger", `{}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "Options.Runner")
}

func TestActions_triggerCreatesARunWithItsParameters(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner, AllowWrite: true}))

	rec := post(s, DefaultPrefix+"/api/jobs/sales-report/trigger",
		`{"params":{"date":"2026-08-01"},"capture_logs":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	run := decode[core.JobRun](t, rec)
	assert.Equal(t, "sales-report", run.JobName)
	assert.Equal(t, core.RunQueued, run.Status)
	assert.Equal(t, core.TriggerManual, run.Trigger)
	assert.JSONEq(t, `{"date":"2026-08-01"}`, string(run.Params))
	assert.NotEmpty(t, run.TriggeredBy,
		"a run nobody can be traced to is the one somebody will ask about")
	require.NotNil(t, run.CaptureLogs, "capture_logs is why a job is usually triggered by hand")
	assert.Equal(t, core.LogAlways, *run.CaptureLogs)

	// and it is really in the store the worker reads
	listed := decode[core.Page[core.JobRun]](t, get(s, DefaultPrefix+"/api/runs?job=sales-report"))
	require.Len(t, listed.Items, 1)
	assert.Equal(t, run.ID, listed.Items[0].ID)
}

// A job that takes no parameters is triggered with no body at all, which must
// not read as a malformed request.
func TestActions_triggerWithNoBody(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner, AllowWrite: true}))

	rec := post(s, DefaultPrefix+"/api/jobs/cleanup/trigger", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// The idempotency key is what makes a double-clicked button one run.
func TestActions_triggerIsIdempotentWithAKey(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner, AllowWrite: true}))

	body := `{"idem_key":"nightly-2026-08-01"}`
	first := decode[core.JobRun](t, post(s, DefaultPrefix+"/api/jobs/cleanup/trigger", body))
	second := decode[core.JobRun](t, post(s, DefaultPrefix+"/api/jobs/cleanup/trigger", body))
	assert.Equal(t, first.ID, second.ID, "the second click must return the first run, not create another")
}

func TestActions_pauseStopsTriggeringAndResumeRestoresIt(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner, AllowWrite: true}))

	require.Equal(t, http.StatusOK, post(s, DefaultPrefix+"/api/jobs/cleanup/pause", "").Code)
	assert.True(t, runner.Registry().IsPaused("cleanup"))

	rec := post(s, DefaultPrefix+"/api/jobs/cleanup/trigger", "")
	assert.Equal(t, http.StatusConflict, rec.Code, "a paused job must refuse to run")
	assert.Contains(t, rec.Body.String(), "JOB_PAUSED")

	require.Equal(t, http.StatusOK, post(s, DefaultPrefix+"/api/jobs/cleanup/resume", "").Code)
	assert.Equal(t, http.StatusOK, post(s, DefaultPrefix+"/api/jobs/cleanup/trigger", "").Code)

	// the list the UI renders has to agree, or the button says the wrong thing
	jobs := decode[struct {
		Items []core.JobInfo `json:"items"`
	}](t, get(s, DefaultPrefix+"/api/jobs"))
	for _, j := range jobs.Items {
		if j.Name == "cleanup" {
			assert.False(t, j.Paused)
		}
	}
}

func TestActions_cancelAQueuedRun(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner, AllowWrite: true}))

	run := decode[core.JobRun](t, post(s, DefaultPrefix+"/api/jobs/cleanup/trigger", ""))
	rec := post(s, DefaultPrefix+"/api/runs/"+run.ID+"/cancel", `{"reason":"wrong day"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	canceled := decode[core.JobRun](t, rec)
	assert.Equal(t, core.RunCanceled, canceled.Status, "a queued run is canceled outright — it never starts")
	assert.Equal(t, "wrong day", canceled.CancelReason)
	assert.NotEmpty(t, canceled.CanceledBy)
}

func TestActions_replayAFinishedRun(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner, AllowWrite: true}))

	finished := time.Now().Add(-time.Hour)
	require.Nil(t, runner.Store().Create(t.Context(), &core.JobRun{
		ID: "old-run", JobName: "sales-report", Queue: core.DefaultQueue,
		Params: []byte(`{"date":"2026-07-31"}`),
		Status: core.RunFailed, Trigger: core.TriggerSchedule,
		Attempt: 1, MaxAttempts: 1, FinishedAt: &finished, CreatedAt: finished,
	}))

	rec := post(s, DefaultPrefix+"/api/runs/old-run/replay", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	replay := decode[core.JobRun](t, rec)
	assert.NotEqual(t, "old-run", replay.ID, "history is never overwritten — a replay is a new run")
	assert.Equal(t, "old-run", replay.ReplayOf)
	assert.Equal(t, core.TriggerReplay, replay.Trigger)
	assert.JSONEq(t, `{"date":"2026-07-31"}`, string(replay.Params),
		"a replay reruns what the original ran")

	// the original is untouched
	original := decode[core.JobRun](t, get(s, DefaultPrefix+"/api/runs/old-run"))
	assert.Equal(t, core.RunFailed, original.Status)
}

// A replay exists because something about the original was wrong, and half the
// time that something is a parameter.
func TestActions_replayWithDifferentParameters(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner, AllowWrite: true}))

	finished := time.Now().Add(-time.Hour)
	require.Nil(t, runner.Store().Create(t.Context(), &core.JobRun{
		ID: "old-run", JobName: "sales-report", Queue: core.DefaultQueue,
		Params: []byte(`{"date":"2026-07-31"}`),
		Status: core.RunFailed, Trigger: core.TriggerSchedule,
		Attempt: 1, MaxAttempts: 1, FinishedAt: &finished, CreatedAt: finished,
	}))

	rec := post(s, DefaultPrefix+"/api/runs/old-run/replay", `{"params":{"date":"2026-08-01"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"date":"2026-08-01"}`, string(decode[core.JobRun](t, rec).Params))

	// and an empty form still means "as it ran", never "with nothing"
	again := post(s, DefaultPrefix+"/api/runs/old-run/replay", `{"params":{}}`)
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	assert.JSONEq(t, `{"date":"2026-07-31"}`, string(decode[core.JobRun](t, again).Params),
		"an untouched form must not blank the parameters the run was created with")
}

// A job's parameter type is any type. Accepting only a JSON object here made a
// job taking a list impossible to trigger from the panel at all.
func TestActions_triggerWithNonObjectParameters(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, core.RegisterJob(runner.Registry(), core.JobDef{Name: "reindex"},
		func(c core.ICronjobContext, ids []string) error { return nil }))
	require.Nil(t, Mount(s, Options{Runner: runner, AllowWrite: true}))

	rec := post(s, DefaultPrefix+"/api/jobs/reindex/trigger", `{"params":["a","b"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `["a","b"]`, string(decode[core.JobRun](t, rec).Params))
}

// Leaving every field of the form blank means "run it the way the scheduler
// would", which is not the same as overriding each parameter with its zero
// value — the job's own defaults have to survive it.
func TestActions_triggerWithAnEmptyFormKeepsTheJobsDefaults(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner, AllowWrite: true}))

	rec := post(s, DefaultPrefix+"/api/jobs/sales-report/trigger", `{"params":{}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, decode[core.JobRun](t, rec).Params)
}

// Replaying a run that has not finished is the wrong verb for what the operator
// wants, and answering "queued a second copy" would be worse than refusing.
func TestActions_replayRefusesAnUnfinishedRun(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner, AllowWrite: true}))

	run := decode[core.JobRun](t, post(s, DefaultPrefix+"/api/jobs/cleanup/trigger", ""))
	rec := post(s, DefaultPrefix+"/api/runs/"+run.ID+"/replay", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "cancel it instead")
}
