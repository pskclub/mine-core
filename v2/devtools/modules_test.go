package devtools

import (
	"context"
	"net/http"
	"testing"

	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noteModule is the shape a service's module has: routes and a job, plus a
// health check the framework could not have found by itself.
type noteModule struct{}

func (noteModule) Name() string { return "note" }

func (noteModule) Routes(e *core.Server) {
	e.GET("/notes", func(c core.IHTTPContext) error { return c.JSON(http.StatusOK, "ok") })
}

func (noteModule) Jobs(reg *core.JobRegistry) core.IError {
	return reg.Register(core.JobDef{Name: "note.reindex"}, func(core.ICronjobContext) error { return nil })
}

// quietModule attaches nothing — the case the panel exists to make visible.
type quietModule struct{}

func (quietModule) Name() string { return "quiet" }

// workerModule owns background work, which is the only kind of module the
// started flag says anything about.
type workerModule struct{}

func (workerModule) Name() string                     { return "worker" }
func (workerModule) Start(*core.App) core.IError      { return nil }
func (workerModule) Stop(context.Context) core.IError { return nil }

type modulesResponse struct {
	Mounted bool                `json:"mounted"`
	Items   []core.ModuleReport `json:"items"`
	Total   int                 `json:"total"`
}

func TestModules_reportsWhatEachModuleAttached(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev"})
	set, err := core.NewModules(noteModule{}, quietModule{})
	require.Nil(t, err)

	reg := core.NewJobRegistry()
	set.MountHTTP(s)
	require.Nil(t, set.MountJobs(reg))

	require.Nil(t, Mount(s, Options{Modules: set, Registry: reg}))

	rec := get(s, "/_dev/api/modules")
	require.Equal(t, http.StatusOK, rec.Code)
	body := decode[modulesResponse](t, rec)

	require.True(t, body.Mounted)
	require.Len(t, body.Items, 2)
	assert.Equal(t, "note", body.Items[0].Name)
	assert.Equal(t, []string{"GET /notes"}, body.Items[0].Routes)
	assert.Equal(t, []string{"note.reindex"}, body.Items[0].Jobs)
	assert.Empty(t, body.Items[1].Routes,
		"a module that registered nothing is the finding this panel is for, not a row to hide")

	assert.Equal(t, []string{"jobs", "routes"}, body.Items[0].Declares,
		"what a module declares is what makes an empty list mean something")
	assert.Empty(t, body.Items[1].Declares)
}

// started is false for a module with no Start at all, which is nearly every
// module — so the panel needs to know which ones have one before it can report
// on any of them.
func TestModules_startedOnlyMeansSomethingForALifecycleModule(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev"})
	set, err := core.NewModules(noteModule{}, workerModule{})
	require.Nil(t, err)
	require.Nil(t, Mount(s, Options{Modules: set}))

	before := decode[modulesResponse](t, get(s, "/_dev/api/modules"))
	assert.NotContains(t, before.Items[0].Declares, "lifecycle",
		"a module with no background work must not be reported as one that failed to start")
	assert.Equal(t, []string{"lifecycle"}, before.Items[1].Declares)
	assert.False(t, before.Items[1].Started)

	require.Nil(t, set.Start(s.App()))
	after := decode[modulesResponse](t, get(s, "/_dev/api/modules"))
	assert.True(t, after.Items[1].Started)
	assert.False(t, after.Items[0].Started, "there was nothing to start")
}

func TestModules_saysWhenNoSetWasPassed(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev"})
	require.Nil(t, Mount(s, Options{}))

	body := decode[modulesResponse](t, get(s, "/_dev/api/modules"))
	assert.False(t, body.Mounted,
		"an empty list would read as 'this service has no modules', which is a different fact")
	assert.Empty(t, body.Items)

	overview := decode[overviewResponse](t, get(s, "/_dev/api/overview"))
	assert.Equal(t, -1, overview.Modules, "-1 is what hides the tab rather than showing it empty")
}

func TestModules_overviewCountsThem(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev"})
	set, err := core.NewModules(noteModule{}, quietModule{})
	require.Nil(t, err)
	require.Nil(t, Mount(s, Options{Modules: set}))

	overview := decode[overviewResponse](t, get(s, "/_dev/api/overview"))
	assert.Equal(t, 2, overview.Modules)
}
