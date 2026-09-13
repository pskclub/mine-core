package coretest

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	core "github.com/pskclub/mine-core/v2"
)

// EnvBaseURL points the end-to-end tests at a running service. Unset means
// there is nothing to talk to, and those tests skip.
const EnvBaseURL = "E2E_BASE_URL"

// Client drives a service over real HTTP — a real socket, a real listener, real
// serialisation — rather than the in-memory handler call NewServer makes.
//
// Use it for what only the running thing can prove: that main wired the routes,
// that config loaded, that migrations ran, that the process serves on the port
// it advertises. Everything cheaper to test belongs in NewServer, which needs no
// process to be up.
//
// Responses come back as the same *Response as NewServer returns, so assertions
// read identically whichever side of the socket the test is on.
type Client struct {
	t       *testing.T
	baseURL string
	http    *http.Client
	headers map[string]string
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithHeader pins a header on every request — an auth token, a tenant id.
func WithHeader(key, value string) ClientOption {
	return func(c *Client) { c.headers[key] = value }
}

// WithHTTPClient replaces the transport (a longer timeout, a cookie jar, a
// proxy).
func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *Client) { c.http = h }
}

// NewClient points at a base URL:
//
//	c := coretest.NewClient(t, "http://localhost:3000")
//	c.Get("/healthz").RequireStatus(200)
func NewClient(t *testing.T, baseURL string, opts ...ClientOption) *Client {
	t.Helper()

	c := &Client{
		t:       t,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
		headers: map[string]string{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// NewClientFromEnv builds a Client from E2E_BASE_URL, skipping the test when it
// is unset.
//
// Skipping rather than failing is deliberate: a developer running `go test ./...`
// without a service up should see the suite pass, and CI makes the coverage
// explicit by setting the variable. The skip message says what to set.
func NewClientFromEnv(t *testing.T, opts ...ClientOption) *Client {
	t.Helper()

	base := os.Getenv(EnvBaseURL)
	if base == "" {
		t.Skipf("set %s to run this against a running service", EnvBaseURL)
	}
	return NewClient(t, base, opts...)
}

// Serve starts a real listener for a server built in the test, and returns a
// Client pointed at it. The server is shut down when the test finishes.
//
// This is the middle ground: a real socket and a real client, without needing a
// deployed service or a container. It catches what NewServer cannot — a handler
// that only works because httptest never serialises, say — while still running
// entirely inside `go test`.
//
//	app := coretest.NewApp(t, coretest.WithAutoMigrate(&User{}))
//	e := core.NewHTTPServer(app, nil)
//	routes.Register(e)          // the service's own wiring
//	c := coretest.Serve(t, e)
//
//	c.Get("/healthz").RequireStatus(200)
func Serve(t *testing.T, e *core.Server, opts ...ClientOption) *Client {
	t.Helper()

	// port 0: the OS picks a free one, so parallel tests never collide
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("coretest: listen: %v", err)
	}
	// echo v5 has no Echo.Listener/Shutdown: a StartConfig owns the listener and
	// Server.Serve stops when its context is cancelled
	go func() {
		cfg := echo.StartConfig{
			Listener:        ln,
			HideBanner:      true,
			HidePort:        true,
			GracefulTimeout: 10 * time.Second,
		}
		if err := e.Serve(context.Background(), cfg); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// the test is already failing or finishing; reporting here would race
			// with its completion
			_ = err
		}
	}()

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = e.Shutdown(shutdownCtx)
	})

	c := NewClient(t, "http://"+ln.Addr().String(), opts...)
	c.WaitReady("/", 5*time.Second)
	return c
}

// WaitReady polls path until the service answers, or fails the test.
//
// Needed whenever the service starts alongside the tests — a container is
// running long before it is listening, and without this the first request fails
// on connection refused and reads like a broken service.
func (c *Client) WaitReady(path string, timeout time.Duration) *Client {
	c.t.Helper()

	deadline := time.Now().Add(timeout)
	var last error

	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(c.ctx(), http.MethodGet, c.url(path), nil)
		if err != nil {
			c.t.Fatalf("coretest: build readiness request: %v", err)
		}
		res, err := c.http.Do(req)
		if err == nil {
			res.Body.Close()
			return c
		}
		last = err
		time.Sleep(100 * time.Millisecond)
	}

	c.t.Fatalf("coretest: %s did not become ready within %s: %v", c.baseURL, timeout, last)
	return c
}

// BaseURL is the address the client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

// Do sends a request. body may be nil, a string, []byte, or any value to encode
// as JSON.
func (c *Client) Do(method, path string, body any, headers ...map[string]string) *Response {
	c.t.Helper()

	req, err := http.NewRequestWithContext(c.ctx(), method, c.url(path), bodyReader(c.t, body))
	if err != nil {
		c.t.Fatalf("coretest: build request: %v", err)
	}
	if body != nil {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}

	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("coretest: %s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatalf("coretest: read response: %v", err)
	}

	return &Response{t: c.t, Code: res.StatusCode, Body: raw, Header: res.Header}
}

// Get sends a GET.
func (c *Client) Get(path string, headers ...map[string]string) *Response {
	c.t.Helper()
	return c.Do(http.MethodGet, path, nil, headers...)
}

// Post sends a POST with a JSON body.
func (c *Client) Post(path string, body any, headers ...map[string]string) *Response {
	c.t.Helper()
	return c.Do(http.MethodPost, path, body, headers...)
}

// Put sends a PUT with a JSON body.
func (c *Client) Put(path string, body any, headers ...map[string]string) *Response {
	c.t.Helper()
	return c.Do(http.MethodPut, path, body, headers...)
}

// Patch sends a PATCH with a JSON body.
func (c *Client) Patch(path string, body any, headers ...map[string]string) *Response {
	c.t.Helper()
	return c.Do(http.MethodPatch, path, body, headers...)
}

// Delete sends a DELETE.
func (c *Client) Delete(path string, headers ...map[string]string) *Response {
	c.t.Helper()
	return c.Do(http.MethodDelete, path, nil, headers...)
}

// ctx is deliberately not t.Context().
//
// Go cancels a test's context immediately *before* its Cleanup functions run, so
// a client bound to it cannot be used in cleanup — and cleaning up is exactly
// what a test wants a client for after it has created something:
//
//	t.Cleanup(func() { c.Delete("/users/" + id) })   // "context canceled"
//
// Each request is bounded by the client's own Timeout instead, so nothing can
// hang forever either way.
func (c *Client) ctx() context.Context { return context.Background() }

func (c *Client) url(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.baseURL + path
}
