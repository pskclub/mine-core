package core

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func capsByKind(caps []Capability) map[string]Capability {
	out := make(map[string]Capability, len(caps))
	for _, c := range caps {
		out[c.Kind] = c
	}
	return out
}

// Every capability is present in the list whether or not it was configured.
// Reporting only the wired ones would leave the reader to notice that an entry
// they expected is missing, which is the mistake the boot line already exists to
// prevent.
func TestApp_capabilitiesListDisabledOnesToo(t *testing.T) {
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "caps"})
	app, err := NewApp(env, WithCache("default", NewMemoryCache()))
	require.Nil(t, err)

	byKind := capsByKind(app.Capabilities())

	require.Contains(t, byKind, "cache")
	assert.True(t, byKind["cache"].Enabled)
	assert.Equal(t, "default", byKind["cache"].Name)

	for _, kind := range []string{"mq", "storage", "mailer", "pusher", "llm", "embedder", "sentry"} {
		require.Contains(t, byKind, kind, "an unconfigured capability must still be listed")
		assert.False(t, byKind[kind].Enabled, kind)
	}
	assert.NotContains(t, byKind, "sql", "no connection was registered, so there is nothing to name")
}

// The route list is only worth reading if it says who serves each route, and
// echo cannot say: the handler it holds is the closure WithHTTPContext built.
func TestServer_handlerNames(t *testing.T) {
	app := newHTTPTestApp(t)
	s := NewHTTPServer(app, nil)

	s.GET("/health", healthTestHandler)
	s.Group("/v1").Group("/users").POST("/:id/ban", healthTestHandler)

	names := s.HandlerNames()
	assert.Contains(t, names["GET /health"], "healthTestHandler")
	assert.Contains(t, names["POST /v1/users/:id/ban"], "healthTestHandler",
		"a nested group's routes are recorded under the full path echo registered")

	// A route added straight to the embedded Echo never was a HandlerFunc, so it
	// has no name to record — and must not invent one.
	s.Echo.GET("/raw", func(c *echo.Context) error { return nil })
	assert.NotContains(t, s.HandlerNames(), "GET /raw")
}

func healthTestHandler(c IHTTPContext) error { return c.NoContent(http.StatusOK) }
