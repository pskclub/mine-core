package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A process with no HTTP server — jobs or consumers only — waits inside
// RunContext on a context nothing else cancels. Stop has to be what ends that
// wait, or a service that decides to stop itself never returns from Run.
func TestRunner_stopEndsRunContextWithoutAnHTTPServer(t *testing.T) {
	app := newTestApp(t)
	sc, err := NewScheduler(app)
	require.NoError(t, err)
	require.NoError(t, sc.Add(JobDef{Name: "tick", Schedule: Every(time.Hour)}, func(c ICronjobContext) error {
		return nil
	}))

	r := NewRunner(app, RunScheduler(sc), RunJobs(sc.Runner()))
	done := make(chan error, 1)
	go func() { done <- r.RunContext(t.Context()) }()

	// let it start before stopping it, or Stop runs against nothing
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, r.Stop())

	select {
	case err := <-done:
		require.NoError(t, err, "Stop is an orderly shutdown, not a failure of Run")
	case <-time.After(5 * time.Second):
		t.Fatal("RunContext never returned after Stop: nothing cancelled its wait")
	}
}
