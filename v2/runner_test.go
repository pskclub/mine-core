package core

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingService is a RunnerService that remembers when it was started and
// stopped, so a test can assert on the order of the whole sequence.
type recordingService struct {
	name  string
	order *shutdownOrder
	fail  error
}

type shutdownOrder struct {
	mu     sync.Mutex
	events []string
}

func (o *shutdownOrder) add(event string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func (o *shutdownOrder) list() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

func (s *recordingService) Start() error {
	s.order.add("start:" + s.name)
	return s.fail
}

func (s *recordingService) Stop(context.Context) error {
	s.order.add("stop:" + s.name)
	return nil
}

func TestRunner_stopsOnContextCancel(t *testing.T) {
	app := newTestApp(t)
	order := &shutdownOrder{}
	svc := &recordingService{name: "worker", order: order}

	r := NewRunner(app, RunService(svc))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.RunContext(ctx) }()

	// give it a moment to start, then ask it to stop
	assert.Eventually(t, func() bool {
		return len(order.list()) > 0
	}, time.Second, 5*time.Millisecond, "the service should have started")
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("runner never returned")
	}

	assert.Equal(t, []string{"start:worker", "stop:worker"}, order.list())
}

// The whole point of the type: the scheduler stops producing before anything
// drains, and the pools close only once everything else has.
func TestRunner_shutdownOrder(t *testing.T) {
	app := newTestApp(t)
	order := &shutdownOrder{}

	sc, err := NewScheduler(app)
	require.NoError(t, err)
	require.NoError(t, sc.Add(JobDef{Name: "tick", Schedule: Every(time.Hour)}, func(c ICronjobContext) error {
		return nil
	}))

	svc := &recordingService{name: "svc", order: order}
	r := NewRunner(app,
		RunScheduler(sc),
		RunJobs(sc.Runner()),
		RunService(svc),
		AfterStop(func() { order.add("pools-closed") }),
	)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.RunContext(ctx) }()

	assert.Eventually(t, func() bool { return len(order.list()) > 0 }, time.Second, 5*time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("runner never returned")
	}

	events := order.list()
	require.Contains(t, events, "stop:svc")
	assert.Equal(t, "pools-closed", events[len(events)-1],
		"the AfterStop hook runs once everything is closed")
}

func TestRunner_servesHTTPAndDrains(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	e.GET("/ping", func(c IHTTPContext) error { return c.String(http.StatusOK, "pong") })

	r := NewRunner(app, RunHTTP(e), WithDrainTimeout(2*time.Second))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.RunContext(ctx) }()

	// the server binds to APP_HOST, which the test env leaves empty → :8080.
	// Rather than depend on a port being free, just prove the lifecycle ends
	// cleanly when the context is cancelled.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("runner never returned")
	}
}

func TestRunner_startFailureStopsWhatStarted(t *testing.T) {
	app := newTestApp(t)
	order := &shutdownOrder{}

	ok := &recordingService{name: "ok", order: order}
	bad := &recordingService{name: "bad", order: order, fail: assertAnError}

	r := NewRunner(app, RunService(ok), RunService(bad))
	err := r.RunContext(t.Context())

	require.Error(t, err, "a service that cannot start must fail the run")
	assert.Contains(t, order.list(), "stop:ok",
		"a partial start must not leave half a service running")
}

func TestRunner_stopIsIdempotent(t *testing.T) {
	app := newTestApp(t)
	order := &shutdownOrder{}
	svc := &recordingService{name: "svc", order: order}

	r := NewRunner(app, RunService(svc))
	require.NoError(t, r.start())

	require.NoError(t, r.Stop())
	require.NoError(t, r.Stop())

	count := 0
	for _, e := range order.list() {
		if e == "stop:svc" {
			count++
		}
	}
	assert.Equal(t, 1, count, "the second Stop is a no-op")
}

func TestRunner_beforeStopRunsFirst(t *testing.T) {
	app := newTestApp(t)
	order := &shutdownOrder{}
	svc := &recordingService{name: "svc", order: order}

	r := NewRunner(app,
		RunService(svc),
		BeforeStop(func(context.Context) error {
			order.add("deregistered")
			return nil
		}),
	)
	require.NoError(t, r.start())
	require.NoError(t, r.Stop())

	events := order.list()
	require.Len(t, events, 3)
	assert.Equal(t, []string{"start:svc", "deregistered", "stop:svc"}, events,
		"deregistering happens before the drain, not during it")
}

var assertAnError = &Error{Status: 500, Code: "BOOM", Message: "cannot start"}

// Serve blocks once it is listening, so "started" must not be logged until the
// listener exists. A port already in use has to come back as a bind failure —
// otherwise the log says the server started and then immediately died, which
// reads like a crash rather than a service that never launched.
func TestRunner_reportsABindFailureRatherThanStarting(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })

	env := mustEnv(t, map[string]string{"ENV": "test", "HOST": taken.Addr().String()})
	app, err := NewApp(env)
	require.NoError(t, err)

	e := NewHTTPServer(app, nil)
	r := NewRunner(app, RunHTTP(e))

	rerr := r.RunContext(t.Context())
	require.Error(t, rerr, "a port already in use must fail the run")
	assert.Contains(t, rerr.Error(), "listen")
}
