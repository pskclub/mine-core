package core

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testModule implements every optional interface, each one switched on by a
// field, so one type covers "module with routes only", "module with everything"
// and the combinations in between without a type per case.
type testModule struct {
	name string

	routes []string // "GET /notes"
	jobs   []string
	cron   []string
	queues []string
	health []string

	jobErr   IError
	cronErr  IError
	startErr IError
	stopErr  IError

	// the Runner starts and stops modules on its own goroutine while the test
	// watches from another, so the counters are atomic and the order shares the
	// mutex-guarded recorder the runner tests already use
	started   atomic.Int32
	stopped   atomic.Int32
	stopOrder *shutdownOrder
}

func (m *testModule) Name() string { return m.name }

func (m *testModule) Routes(e *Server) {
	for _, r := range m.routes {
		method, path := splitRoute(r)
		switch method {
		case http.MethodPost:
			e.POST(path, okHandler)
		default:
			e.GET(path, okHandler)
		}
	}
}

func (m *testModule) Jobs(reg *JobRegistry) IError {
	if m.jobErr != nil {
		return m.jobErr
	}
	for _, name := range m.jobs {
		if err := reg.Register(JobDef{Name: name}, noopJob); err != nil {
			return err
		}
	}
	return nil
}

func (m *testModule) Cron(sc *Scheduler) IError {
	if m.cronErr != nil {
		return m.cronErr
	}
	for _, name := range m.cron {
		if err := sc.Add(JobDef{Name: name, Schedule: Every(time.Hour)}, noopJob); err != nil {
			return err
		}
	}
	return nil
}

func (m *testModule) Consumers(c IMQConsumer) {
	for _, q := range m.queues {
		c.On(q, func(IMQContext, *Delivery) error { return nil })
	}
}

func (m *testModule) HealthChecks() []HealthCheck {
	out := make([]HealthCheck, 0, len(m.health))
	for _, name := range m.health {
		out = append(out, HealthCheck{Name: name, Check: func(context.Context) error { return nil }})
	}
	return out
}

func (m *testModule) Start(*App) IError {
	if m.startErr != nil {
		return m.startErr
	}
	m.started.Add(1)
	return nil
}

func (m *testModule) Stop(context.Context) IError {
	m.stopped.Add(1)
	if m.stopOrder != nil {
		m.stopOrder.add(m.name)
	}
	return m.stopErr
}

func splitRoute(r string) (method, path string) {
	for i := 0; i < len(r); i++ {
		if r[i] == ' ' {
			return r[:i], r[i+1:]
		}
	}
	return http.MethodGet, r
}

func okHandler(c IHTTPContext) error { return c.JSON(http.StatusOK, "ok") }

func noopJob(ICronjobContext) error { return nil }

// bareModule implements nothing but IModule — the case that must not panic
// anywhere, since most modules will not implement most of the interfaces.
type bareModule struct{ name string }

func (m bareModule) Name() string { return m.name }

func TestNewModules_rejectsDuplicateAndMalformedNames(t *testing.T) {
	_, err := NewModules(bareModule{name: "note"}, bareModule{name: "note"})
	require.Error(t, err, "two modules under one name would share a health prefix and a report entry")
	assert.Equal(t, "DUPLICATE_MODULE", err.GetCode())

	for _, name := range []string{"", "Note", "note.core", "-note", "note/v2"} {
		_, err := NewModules(bareModule{name: name})
		require.Error(t, err, "name %q is not usable as a health-check prefix", name)
		assert.Equal(t, "INVALID_MODULE", err.GetCode())
	}

	_, err = NewModules(nil)
	require.Error(t, err, "a nil module would panic on the first Name() call instead of at assembly")

	set, err := NewModules(bareModule{name: "note"}, bareModule{name: "user_2"}, bareModule{name: "a-b"})
	require.NoError(t, err)
	assert.Equal(t, []string{"note", "user_2", "a-b"}, set.Names(),
		"declaration order is the mount order, so it is what Names reports")
}

func TestModuleSet_mountsEveryKindAndReportsWhatItAttached(t *testing.T) {
	app := newTestApp(t)
	note := &testModule{
		name:   "note",
		routes: []string{"GET /notes", "POST /notes"},
		jobs:   []string{"note.reindex"},
		cron:   []string{"note.cleanup"},
		health: []string{"search-index"},
	}
	set, err := NewModules(note, bareModule{name: "home"})
	require.NoError(t, err)

	e := NewHTTPServer(app, nil)
	sc, serr := NewScheduler(app)
	require.NoError(t, serr)

	set.MountHTTP(e)
	require.NoError(t, set.MountJobs(sc.Runner().Registry()))
	require.NoError(t, set.MountCron(sc))
	checks := set.HealthChecks()

	report := set.Report()
	require.Len(t, report, 2)
	assert.Equal(t, "note", report[0].Name)
	assert.Equal(t, []string{"GET /notes", "POST /notes"}, report[0].Routes)
	assert.Equal(t, []string{"note.reindex"}, report[0].Jobs)
	assert.Equal(t, []string{"note.cleanup"}, report[0].Cron,
		"a job armed on a schedule is reported as cron, not as a plain definition")
	assert.Empty(t, report[1].Routes, "a module implementing nothing registers nothing")

	require.Len(t, checks, 1)
	assert.Equal(t, "note.search-index", checks[0].Name,
		"the module prefix is what keeps two modules' checks from overwriting each other")
	assert.Equal(t, []string{"note.search-index"}, report[0].Health)
}

func TestModuleSet_healthChecksArePrefixedAndCollisionFree(t *testing.T) {
	a := &testModule{name: "payment", health: []string{"api", ""}}
	b := &testModule{name: "kyc", health: []string{"api"}}
	set, err := NewModules(a, b)
	require.NoError(t, err)

	names := make([]string, 0, 3)
	for _, hc := range set.HealthChecks() {
		names = append(names, hc.Name)
	}
	assert.Equal(t, []string{"payment.api", "payment", "kyc.api"}, names,
		"an unnamed check is reported as the module itself; two named 'api' stay distinct")

	// calling twice must not double the report — unlike the mount methods this
	// one has no claim, and a probe plus a startup gate both call it
	set.HealthChecks()
	assert.Equal(t, []string{"payment.api", "payment"}, set.Report()[0].Health)
}

func TestModuleSet_mountingTheSameTargetTwiceIsANoOp(t *testing.T) {
	app := newTestApp(t)
	note := &testModule{name: "note", routes: []string{"GET /notes"}, jobs: []string{"note.reindex"}}
	set, err := NewModules(note)
	require.NoError(t, err)

	e := NewHTTPServer(app, nil)
	reg := NewJobRegistry()

	set.MountHTTP(e)
	set.MountHTTP(e)
	require.NoError(t, set.MountJobs(reg))
	require.NoError(t, set.MountJobs(reg),
		"a second mount must not report the job name as a duplicate registration")

	assert.Equal(t, []string{"GET /notes"}, set.Report()[0].Routes,
		"NewAPI mounts and then RunModules mounts again — the normal case, not a mistake")
	assert.Len(t, reg.List(), 1)
}

func TestModuleSet_mountingADifferentTargetStillWorks(t *testing.T) {
	app := newTestApp(t)
	set, err := NewModules(&testModule{name: "note", routes: []string{"GET /notes"}})
	require.NoError(t, err)

	first, second := NewHTTPServer(app, nil), NewHTTPServer(app, nil)
	set.MountHTTP(first)
	set.MountHTTP(second)

	assert.Len(t, routeKeys(second), len(routeKeys(first)),
		"a second server is a new target, not a repeat of the first")
}

func TestModuleSet_mountConsumersRecordsTheQueues(t *testing.T) {
	app := newTestApp(t)
	set, err := NewModules(&testModule{name: "note", queues: []string{"note.created", "note.deleted"}})
	require.NoError(t, err)

	// no broker is needed: a consumer registers handlers up front and only
	// connects at Start
	c := app.NewMQConsumer()
	set.MountConsumers(c)

	assert.Equal(t, []string{"note.created", "note.deleted"}, set.Report()[0].Queues,
		"IMQConsumer cannot be asked what it consumes, so the names are taken as they are registered")

	set.MountConsumers(c)
	assert.Len(t, set.Report()[0].Queues, 2, "the same consumer twice is a no-op, as for every other target")
}

func TestModuleSet_mountErrorNamesTheModule(t *testing.T) {
	app := newTestApp(t)
	set, err := NewModules(
		&testModule{name: "note", jobs: []string{"shared"}},
		&testModule{name: "user", jobs: []string{"shared"}},
	)
	require.NoError(t, err)

	mErr := set.MountJobs(NewJobRegistry())
	require.Error(t, mErr)
	assert.Equal(t, "DUPLICATE_JOB", mErr.GetCode(), "the underlying code survives the wrap")
	assert.Contains(t, mErr.GetMessage(), "module user",
		"which module collided is the whole question a duplicate job name raises")

	sc, serr := NewScheduler(app)
	require.NoError(t, serr)
	broken, err := NewModules(&testModule{name: "note", cronErr: New(500, "BOOM", "no")})
	require.NoError(t, err)
	cErr := broken.MountCron(sc)
	require.Error(t, cErr)
	assert.Contains(t, cErr.GetMessage(), "module note: cron")
}

func TestModuleSet_startAndStop(t *testing.T) {
	app := newTestApp(t)
	order := &shutdownOrder{}
	a := &testModule{name: "first", stopOrder: order}
	b := &testModule{name: "second", stopOrder: order}
	set, err := NewModules(a, b)
	require.NoError(t, err)

	require.NoError(t, set.Start(app))
	assert.EqualValues(t, 1, a.started.Load())
	assert.True(t, set.Report()[0].Started)

	require.NoError(t, set.Stop(t.Context()))
	assert.Equal(t, []string{"second", "first"}, order.list(),
		"stopping in reverse means a module is gone before the one it was declared after")
	assert.False(t, set.Report()[0].Started)
}

func TestModuleSet_startFailureLeavesLaterModulesAlone(t *testing.T) {
	app := newTestApp(t)
	first := &testModule{name: "first"}
	broken := &testModule{name: "broken", startErr: New(500, "BOOM", "no")}
	later := &testModule{name: "later"}
	set, err := NewModules(first, broken, later)
	require.NoError(t, err)

	sErr := set.Start(app)
	require.Error(t, sErr)
	assert.Contains(t, sErr.GetMessage(), "module broken: start")
	assert.EqualValues(t, 0, later.started.Load(), "a half-started service is worse than one that refuses to boot")

	// only what started is stopped — the shutdown that follows a failed start
	// must not call Stop on a module whose Start never returned
	require.NoError(t, set.Stop(t.Context()))
	assert.EqualValues(t, 1, first.stopped.Load())
	assert.EqualValues(t, 0, broken.stopped.Load())
	assert.EqualValues(t, 0, later.stopped.Load())
}

func TestModuleSet_stopReportsTheFirstErrorButStopsEverything(t *testing.T) {
	app := newTestApp(t)
	a := &testModule{name: "first"}
	b := &testModule{name: "second", stopErr: New(500, "BOOM", "no")}
	set, err := NewModules(a, b)
	require.NoError(t, err)
	require.NoError(t, set.Start(app))

	sErr := set.Stop(t.Context())
	require.Error(t, sErr)
	assert.EqualValues(t, 1, a.stopped.Load(), "a module that fails to stop must not strand the ones after it")
}

func TestModuleSet_nilSetIsInert(t *testing.T) {
	var set *ModuleSet
	assert.NotPanics(t, func() {
		set.MountHTTP(nil)
		_ = set.MountJobs(nil)
		_ = set.MountCron(nil)
		set.MountConsumers(nil)
		_ = set.HealthChecks()
		_ = set.Report()
		_ = set.Names()
		_ = set.Start(nil)
		_ = set.Stop(context.Background())
	}, "an unwired Runner holds a nil set, and every method it calls must survive that")
}

func TestRunner_mountsModulesForTheRoleItIsGiven(t *testing.T) {
	app := newTestApp(t)
	note := &testModule{
		name:   "note",
		routes: []string{"GET /notes"},
		jobs:   []string{"note.reindex"},
		cron:   []string{"note.cleanup"},
	}
	set, err := NewModules(note)
	require.NoError(t, err)

	// a worker role: no HTTP server, so Routes is never called
	sc, serr := NewScheduler(app)
	require.NoError(t, serr)
	r := NewRunner(app, RunScheduler(sc), RunJobs(sc.Runner()), RunModules(set))
	require.NoError(t, r.mountModules())

	report := set.Report()[0]
	assert.Empty(t, report.Routes, "a worker has nowhere to put routes, and must not pretend otherwise")
	assert.Equal(t, []string{"note.reindex"}, report.Jobs)
	assert.Equal(t, []string{"note.cleanup"}, report.Cron)
	assert.EqualValues(t, 1, note.started.Load())
}

func TestRunner_mountsRoutesWhenItHasAServer(t *testing.T) {
	app := newTestApp(t)
	set, err := NewModules(&testModule{name: "note", routes: []string{"GET /notes"}})
	require.NoError(t, err)

	e := NewHTTPServer(app, nil)
	r := NewRunner(app, RunHTTP(e), RunModules(set))
	require.NoError(t, r.mountModules())

	assert.Equal(t, []string{"GET /notes"}, set.Report()[0].Routes)
	assert.True(t, routeKeys(e)["GET /notes"])
}

func TestRunner_mountFailureStopsTheStart(t *testing.T) {
	app := newTestApp(t)
	set, err := NewModules(&testModule{name: "note", jobErr: New(500, "BOOM", "no")})
	require.NoError(t, err)

	sc, serr := NewScheduler(app)
	require.NoError(t, serr)
	r := NewRunner(app, RunScheduler(sc), RunJobs(sc.Runner()), RunModules(set))

	require.Error(t, r.mountModules(), "a module that cannot register must not reach a serving process")
}

func TestRunner_stopsModulesDuringTheDrain(t *testing.T) {
	app := newTestApp(t)
	mod := &testModule{name: "note"}
	set, err := NewModules(mod)
	require.NoError(t, err)

	r := NewRunner(app, RunModules(set))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.RunContext(ctx) }()

	require.Eventually(t, func() bool { return mod.started.Load() == 1 }, time.Second, 5*time.Millisecond)
	cancel()

	select {
	case rErr := <-done:
		require.NoError(t, rErr)
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop")
	}
	assert.EqualValues(t, 1, mod.stopped.Load(), "modules stop with the drain, before the pools they may still be using")
}
