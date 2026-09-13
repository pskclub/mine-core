package core

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCombinedService_wiring verifies the "API + Cron in one process" pattern
// from docs/api-with-cron.md compiles and its shutdown sequence runs cleanly.
func TestCombinedService_wiring(t *testing.T) {
	app := newHTTPTestApp(t)

	// HTTP
	e := NewHTTPServer(app, nil)
	e.GET("/health", func(c IHTTPContext) error { return c.JSON(200, "ok") })

	// Cron
	sc, err := NewScheduler(app)
	require.NoError(t, err)
	ran := make(chan struct{}, 1)
	require.NoError(t, sc.AddByDuration("tick", 10*time.Millisecond, func(c ICronjobContext) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}))

	sc.Start()

	// the job actually fires
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled job did not run")
	}

	// coordinated graceful shutdown (drain HTTP → stop jobs → close pools)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	assert.NoError(t, e.Shutdown(shutdownCtx))
	assert.NoError(t, sc.Stop())
	assert.NoError(t, app.Shutdown(shutdownCtx))
}
