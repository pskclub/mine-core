package devtools

import (
	"net/http"
	"strings"
	"testing"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Off by default: a CPU profile costs real time on a live process, so turning it
// on is a decision somebody makes rather than something a mount carries along.
func TestPprof_offByDefault(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{}))

	assert.Equal(t, http.StatusNotFound, get(s, DefaultPrefix+"/debug/pprof/goroutine").Code)

	rec := get(s, DefaultPrefix+"/api/pprof")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "Options.Pprof", "the refusal names the switch")

	o := decode[overviewResponse](t, get(s, DefaultPrefix+"/api/overview"))
	assert.False(t, o.Profiling)
}

// The overview says how many goroutines there are; this is the endpoint that
// says what they are.
func TestPprof_servesTheNamedProfiles(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Pprof: true}))

	rec := get(s, DefaultPrefix+"/debug/pprof/goroutine?debug=1")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "goroutine profile",
		"the goroutine dump is the first thing read on a wedged process")

	assert.Equal(t, http.StatusOK, get(s, DefaultPrefix+"/debug/pprof/heap?debug=1").Code)
	assert.Equal(t, http.StatusOK, get(s, DefaultPrefix+"/debug/pprof/").Code)
	assert.Equal(t, http.StatusOK, get(s, DefaultPrefix+"/debug/pprof/cmdline").Code)
}

// Profiling is behind the same lock as everything else — it is the endpoint most
// worth not leaving open, since it will happily hold a connection for thirty
// seconds of CPU time.
func TestPprof_isBehindTheGuard(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "prod", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Pprof: true, BasicAuth: &BasicAuth{Password: "s3cret"}}))

	assert.Equal(t, http.StatusUnauthorized, get(s, DefaultPrefix+"/debug/pprof/goroutine").Code)
	assert.Equal(t, http.StatusOK,
		getWithLogin(s, DefaultPrefix+"/debug/pprof/goroutine?debug=1", DefaultBasicAuthUser, "s3cret").Code)
}

func TestPprof_indexListsTheProfilesAndTheTimeoutNote(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Pprof: true, Prefix: "/inspect"}))

	body := decode[struct {
		Base     string `json:"base"`
		Note     string `json:"note"`
		Profiles []struct {
			Name string `json:"name"`
		} `json:"profiles"`
	}](t, get(s, "/inspect/api/pprof"))

	assert.Equal(t, "/inspect/debug/pprof", body.Base, "the links have to follow the mount")
	assert.Contains(t, body.Note, "seconds",
		"the CPU profile's timeout is the one thing worth knowing before the first attempt")
	require.NotEmpty(t, body.Profiles)
	assert.Equal(t, "goroutine", body.Profiles[0].Name)
}

// --- next run ---

// "why has this job not run?" splits three ways, and an empty cell answers none
// of them. The API has to distinguish them.
func TestJobs_reportsWhetherAnythingIsArmedHere(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner}))

	body := decode[struct {
		Items     []jobEntry `json:"items"`
		Scheduled bool       `json:"scheduled"`
	}](t, get(s, DefaultPrefix+"/api/jobs"))

	assert.False(t, body.Scheduled,
		"this runner has no scheduler, so no next-run time is missing data — it is the answer")
	for _, j := range body.Items {
		assert.Nil(t, j.NextRun)
	}
}

func TestJobs_reportsTheNextRunWhenASchedulerIsFeedingTheRunner(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})

	sc, err := core.NewScheduler(s.App())
	require.Nil(t, err)
	require.Nil(t, sc.AddByDuration("heartbeat", time.Hour, func(c core.ICronjobContext) error { return nil }))
	require.Nil(t, sc.Add(core.JobDef{Name: "manual-only"}, func(c core.ICronjobContext) error { return nil }))
	require.Nil(t, sc.Start())
	t.Cleanup(func() { _ = sc.Stop() })

	require.Nil(t, Mount(s, Options{Runner: sc.Runner()}))

	body := decode[struct {
		Items     []jobEntry `json:"items"`
		Scheduled bool       `json:"scheduled"`
	}](t, get(s, DefaultPrefix+"/api/jobs"))
	require.True(t, body.Scheduled)

	byName := map[string]jobEntry{}
	for _, j := range body.Items {
		byName[j.Name] = j
	}

	scheduled := byName["heartbeat"]
	require.NotNil(t, scheduled.NextRun, "an armed job knows when it fires next")
	assert.True(t, scheduled.NextRun.After(time.Now()))
	assert.Positive(t, scheduled.InS)

	assert.Nil(t, byName["manual-only"].NextRun, "a manual job has no next run to report")
}

// --- tail ---

// The default log policy persists nothing, so the stored lines are empty for
// exactly the run somebody is watching. The tail is what fills that panel.
func TestTail_streamsAndEndsWhenTheRunIsAlreadyFinished(t *testing.T) {
	s, runner := newRunnerFixture(t)
	require.Nil(t, Mount(s, Options{Runner: runner, AllowWrite: true}))

	finished := time.Now()
	require.Nil(t, runner.Store().Create(t.Context(), &core.JobRun{
		ID: "done", JobName: "cleanup", Status: core.RunSucceeded,
		FinishedAt: &finished, CreatedAt: finished,
	}))

	rec := get(s, DefaultPrefix+"/api/runs/done/tail")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"),
		"a buffering proxy turns a stream into one delivery at the end, which is the failure this avoids")

	body := rec.Body.String()
	assert.Contains(t, body, "event: end")
	assert.Contains(t, body, "already finished",
		"a stream that stays open and never emits reads as a job doing nothing")
	assert.False(t, strings.Contains(body, "event: log"))
}

func TestTail_needsARunner(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{}))

	rec := get(s, DefaultPrefix+"/api/runs/whatever/tail")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "Options.Runner")
}
