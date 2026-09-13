package core

import (
	"context"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingCache is a pool that only reports whether it was closed, and how often.
type countingCache struct {
	ICache
	closes atomic.Int32
}

func (c *countingCache) Close() IError {
	c.closes.Add(1)
	return nil
}

func (c *countingCache) Redis() redis.UniversalClient { return nil }

// A `defer app.Shutdown(ctx)` in main and StartHTTPServer's own close are both
// correct, and both run. The second must do nothing rather than close a redis
// client twice and log an error that means nothing to whoever reads it.
func TestApp_shutdownIsIdempotent(t *testing.T) {
	c := &countingCache{}
	app := newTestApp(t, WithCache("default", c))

	require.Nil(t, app.Shutdown(context.Background()))
	require.Nil(t, app.Shutdown(context.Background()))

	assert.Equal(t, int32(1), c.closes.Load(), "pools must be closed exactly once")
}

// SIGTERM is what `docker stop` and Kubernetes send, and it is the signal a
// server must not miss: a process that listens only for SIGINT is killed
// outright there, taking every in-flight request with it.
func TestShutdownSignals_includeSIGTERM(t *testing.T) {
	assert.Contains(t, ShutdownSignals, syscall.SIGTERM)
	assert.Contains(t, ShutdownSignals, os.Interrupt)
}

// The whole shutdown sequence: the server stops accepting, the request that was
// already running still completes, and the pools close only afterwards — a pool
// closed while a request still holds it turns a clean shutdown into 500s.
//
// Driven by cancelling the context rather than by a real signal, which is what
// signal.NotifyContext does with one and is the same path StartHTTPServer takes.
func TestServeUntil_drainsThenClosesPools(t *testing.T) {
	addr := freeAddr(t)
	c := &countingCache{}
	app := newTestApp(t, WithCache("default", c))

	// APP_ENV=test, so IsDev() is false and this takes the production path.
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "test-svc", "HOST": addr})

	released := make(chan struct{})
	e := NewHTTPServer(app, nil)
	e.GET("/slow", func(c IHTTPContext) error {
		close(released)
		time.Sleep(300 * time.Millisecond) // still running when the signal arrives
		return c.JSON(http.StatusOK, "done")
	})

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	returned := make(chan struct{})
	go func() {
		assert.NoError(t, serveUntil(e, env, ctx, stop))
		close(returned)
	}()

	waitListening(t, addr)

	inflight := make(chan *http.Response, 1)
	go func() {
		res, err := http.Get("http://" + addr + "/slow")
		if err == nil {
			inflight <- res
		}
	}()

	<-released // the handler is in the middle of the request
	stop()     // what a SIGTERM does

	select {
	case res := <-inflight:
		defer res.Body.Close()
		assert.Equal(t, http.StatusOK, res.StatusCode, "an in-flight request must be allowed to finish")
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request was cut off by the shutdown")
	}

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the server kept running after it was asked to stop")
	}

	assert.Equal(t, int32(1), c.closes.Load(), "pools are closed once the drain is over")
}

// freeAddr reserves a port and hands it back. A gap between closing this
// listener and the server binding is unavoidable; on a test machine nothing
// else is racing for it.
func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	return addr
}

func waitListening(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("server never listened on %s", addr)
}
