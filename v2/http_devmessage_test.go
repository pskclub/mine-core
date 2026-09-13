package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// driverErr stands in for what a database or client library actually reports —
// the text a developer needs to see and a client must never be shown.
const driverErr = `ERROR: duplicate key value violates unique constraint "users_email_key"`

func newEnvHTTPApp(t *testing.T, env string) *App {
	t.Helper()
	e := mustEnv(t, map[string]string{"ENV": env, "SERVICE": "devmsg-test"})
	app, err := NewApp(e)
	require.NoError(t, err)
	return app
}

func errBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	return body
}

// A wrapped error returned straight from a handler (the shape every repository
// and client produces) must render the underlying failure in dev — not the
// wrapper's own label.
func TestHTTP_devMessage_wrappedError(t *testing.T) {
	app := newEnvHTTPApp(t, "dev")
	e := NewHTTPServer(app, nil)
	e.GET("/boom", func(c IHTTPContext) error {
		return Wrap(errors.New(driverErr), "repository").WithCode("DATABASE_ERROR")
	})

	rec := doJSON(e, http.MethodGet, "/boom", "")
	body := errBody(t, rec.Body.Bytes())

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "DATABASE_ERROR", body["code"], "code stays stable for clients")
	assert.Equal(t, driverErr, body["message"])
}

// Outside dev the same error must expose nothing but its own message.
func TestHTTP_devMessage_hiddenOutsideDev(t *testing.T) {
	for _, env := range []string{"prod", "test", ""} {
		t.Run("ENV="+env, func(t *testing.T) {
			app := newEnvHTTPApp(t, env)
			e := NewHTTPServer(app, nil)
			e.GET("/boom", func(c IHTTPContext) error {
				return Wrap(errors.New(driverErr), "repository").WithCode("DATABASE_ERROR")
			})

			rec := doJSON(e, http.MethodGet, "/boom", "")
			body := errBody(t, rec.Body.Bytes())

			assert.Equal(t, "repository", body["message"])
			assert.NotContains(t, body["message"], "users_email_key", "internals must not leak")
		})
	}
}

// ctx.NewError already substitutes the message in dev; it must pick the root
// cause too, not the wrapper it was handed.
func TestHTTP_devMessage_viaNewError(t *testing.T) {
	app := newEnvHTTPApp(t, "dev")
	e := NewHTTPServer(app, nil)
	e.GET("/boom", func(c IHTTPContext) error {
		repoErr := Wrap(errors.New(driverErr), "repository").WithCode("DATABASE_ERROR")
		return c.NewError(repoErr, repoErr)
	})

	rec := doJSON(e, http.MethodGet, "/boom", "")
	body := errBody(t, rec.Body.Bytes())

	assert.Equal(t, driverErr, body["message"], "no stacked wrapper text")
}

// A panic is still a 500, but in dev it says what the panic was.
func TestHTTP_devMessage_panic(t *testing.T) {
	app := newEnvHTTPApp(t, "dev")
	e := NewHTTPServer(app, nil)
	e.GET("/boom", func(c IHTTPContext) error {
		panic("kaboom")
	})

	rec := doJSON(e, http.MethodGet, "/boom", "")
	body := errBody(t, rec.Body.Bytes())

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "kaboom", body["message"])
}

func TestHTTP_devMessage_panicHiddenOutsideDev(t *testing.T) {
	app := newEnvHTTPApp(t, "prod")
	e := NewHTTPServer(app, nil)
	e.GET("/boom", func(c IHTTPContext) error {
		panic("kaboom")
	})

	rec := doJSON(e, http.MethodGet, "/boom", "")
	body := errBody(t, rec.Body.Bytes())

	assert.Equal(t, "Internal server error", body["message"])
}

// Errors with no cause — validation results and sentinels — keep their own
// wording and their fields in every environment.
func TestHTTP_devMessage_leavesCauselessErrorsAlone(t *testing.T) {
	app := newEnvHTTPApp(t, "dev")
	e := NewHTTPServer(app, nil)
	e.POST("/users", func(c IHTTPContext) error {
		var req createUserReq
		return c.BindWithValidate(&req)
	})

	rec := doJSON(e, http.MethodPost, "/users", `{}`)
	body := errBody(t, rec.Body.Bytes())

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "Invalid parameters", body["message"])
	assert.NotNil(t, body["fields"], "validation detail survives the substitution")
}

func TestRootCause(t *testing.T) {
	root := errors.New("root")

	assert.Equal(t, root, RootCause(root))
	assert.Equal(t, root, RootCause(Wrap(Wrap(root, "inner"), "outer")))
	assert.Nil(t, RootCause(nil))

	// a sentinel with no cause is its own root
	assert.Equal(t, errors.New("x").Error(), RootCause(errors.New("x")).Error())
}

// A cause that points back at its own wrapper must terminate rather than spin.
func TestRootCause_cycleTerminates(t *testing.T) {
	a := New(500, "A", "a")
	b := New(500, "B", "b")
	a.cause = b
	b.cause = a

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = RootCause(a)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RootCause did not terminate on a cyclic chain")
	}
}

// A handler that returns an error writes nothing before this middleware
// unwinds, so the logged status must come from the error — not from the
// response's untouched default.
func TestHTTP_requestLog_reportsRealStatus(t *testing.T) {
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "logstatus", "LOG_SIMPLE": "true"})
	var buf bytes.Buffer
	app, err := NewApp(env, WithLogger(NewLoggerTo(&buf, env)))
	require.NoError(t, err)

	e := NewHTTPServer(app, nil)
	e.GET("/missing", func(c IHTTPContext) error { return errmsgsNotFound() })
	e.GET("/ok", func(c IHTTPContext) error { return c.JSON(http.StatusOK, "hi") })

	rec := doJSON(e, http.MethodGet, "/missing", "")
	require.Equal(t, http.StatusNotFound, rec.Code)

	out := buf.String()
	assert.Contains(t, out, "status=404", "the log must agree with what the client got")
	assert.NotContains(t, out, "status=200")
	assert.Contains(t, out, "code=NOT_FOUND")
	assert.Contains(t, out, "WARN", "a 4xx is not a routine info line")

	buf.Reset()
	doJSON(e, http.MethodGet, "/ok", "")
	assert.Contains(t, buf.String(), "status=200")
}

func errmsgsNotFound() IError { return New(http.StatusNotFound, "NOT_FOUND", "not found") }
