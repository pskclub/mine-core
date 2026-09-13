package core

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// echo v5 cuts every request off after 30s. That number ends uploads, long polls
// and SSE streams mid-flight, so the framework raises it — and a caller's own
// hook still gets the last word.
func TestHTTP_readTimeoutDefaultAndOverride(t *testing.T) {
	cases := []struct {
		name     string
		override func(*http.Server) error
		want     time.Duration
	}{
		{"framework default", nil, DefaultReadTimeout},
		{"caller wins", func(srv *http.Server) error {
			srv.ReadTimeout = 42 * time.Second
			return nil
		}, 42 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(t)
			e := NewHTTPServer(app, nil)

			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)

			got := make(chan time.Duration, 1)
			cfg := echo.StartConfig{Listener: ln, HideBanner: true, HidePort: true}
			cfg.BeforeServeFunc = func(srv *http.Server) error {
				if tc.override != nil {
					if err := tc.override(srv); err != nil {
						return err
					}
				}
				got <- srv.ReadTimeout
				return nil
			}

			go func() { _ = e.Serve(context.Background(), cfg) }()
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = e.Shutdown(ctx)
			})

			select {
			case d := <-got:
				assert.Equal(t, tc.want, d)
			case <-time.After(5 * time.Second):
				t.Fatal("server never started")
			}
		})
	}
}

// The other three deadlines: headers and idle connections are bounded by
// default, writes are not (see the note next to DefaultReadTimeout), and any of
// them can be set or switched off through HTTPOptions.
func TestHTTP_serverDeadlines(t *testing.T) {
	cases := []struct {
		name string
		opts *HTTPOptions
		want serverDeadlines
	}{
		{
			"defaults", &HTTPOptions{},
			serverDeadlines{
				read:       DefaultReadTimeout,
				readHeader: DefaultReadHeaderTimeout,
				write:      0, // deliberately unset: it would cut off SSE and long polls
				idle:       DefaultIdleTimeout,
			},
		},
		{
			"explicit values win",
			&HTTPOptions{
				ReadTimeout:       time.Second,
				ReadHeaderTimeout: 2 * time.Second,
				WriteTimeout:      3 * time.Second,
				IdleTimeout:       4 * time.Second,
			},
			serverDeadlines{read: time.Second, readHeader: 2 * time.Second,
				write: 3 * time.Second, idle: 4 * time.Second},
		},
		{
			"negative turns a deadline off",
			&HTTPOptions{ReadTimeout: -1, IdleTimeout: -1},
			serverDeadlines{read: 0, readHeader: DefaultReadHeaderTimeout, write: 0, idle: 0},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveDeadlines(tc.opts))
		})
	}
}

func TestHTTP_deadlinesReachTheServer(t *testing.T) {
	app := newTestApp(t)
	e := NewHTTPServer(app, &HTTPOptions{WriteTimeout: 7 * time.Second})

	srv := &http.Server{}
	require.NoError(t, e.deadlines.apply(nil)(srv))

	assert.Equal(t, DefaultReadTimeout, srv.ReadTimeout)
	assert.Equal(t, DefaultReadHeaderTimeout, srv.ReadHeaderTimeout)
	assert.Equal(t, 7*time.Second, srv.WriteTimeout)
	assert.Equal(t, DefaultIdleTimeout, srv.IdleTimeout)
}

func TestHTTP_resolveBodyLimit(t *testing.T) {
	assert.Equal(t, DefaultBodyLimit, resolveBodyLimit(0), "unset takes the default")
	assert.Equal(t, int64(1024), resolveBodyLimit(1024))
	assert.Equal(t, int64(0), resolveBodyLimit(-1), "negative turns the limit off")
}
