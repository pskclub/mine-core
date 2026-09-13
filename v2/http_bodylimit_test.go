package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Without a limit the largest request anybody sends is the memory footprint of
// the process. The limit is on by default, and it answers in the framework's own
// error shape so a client parses this rejection like any other.
func TestHTTP_bodyLimitRejectsOversizedBody(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, &HTTPOptions{BodyLimit: 32})
	e.POST("/echo", func(c IHTTPContext) error {
		var payload map[string]any
		if err := c.BindOnly(&payload); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, payload)
	})

	rec := doJSON(e, http.MethodPost, "/echo", `{"note":"`+strings.Repeat("x", 200)+`"}`)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())

	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "REQUEST_TOO_LARGE", body.Code)
	assert.Contains(t, body.Message, "32")
}

func TestHTTP_bodyLimitAllowsNormalBodies(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil) // the default limit
	e.POST("/echo", func(c IHTTPContext) error {
		var payload map[string]any
		if err := c.BindOnly(&payload); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, payload)
	})

	rec := doJSON(e, http.MethodPost, "/echo", `{"note":"hello"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// A chunked request declares no Content-Length, so the limit can only be applied
// to the bytes actually read. That failure surfaces from inside the handler,
// where it must still be reported as 413 rather than as a malformed body.
func TestHTTP_bodyLimitCatchesUndeclaredLength(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, &HTTPOptions{BodyLimit: 16})
	e.POST("/echo", func(c IHTTPContext) error {
		var payload map[string]any
		if err := c.BindOnly(&payload); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, payload)
	})

	req := httptest.NewRequest(http.MethodPost, "/echo",
		io.NopCloser(strings.NewReader(`{"note":"`+strings.Repeat("y", 200)+`"}`)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.ContentLength = -1 // chunked: the size is not known up front

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
}

func TestHTTP_bodyLimitCanBeTurnedOff(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, &HTTPOptions{BodyLimit: -1})
	e.POST("/echo", func(c IHTTPContext) error {
		var payload map[string]any
		if err := c.BindOnly(&payload); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, "ok")
	})

	rec := doJSON(e, http.MethodPost, "/echo", `{"note":"`+strings.Repeat("x", 64<<10)+`"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// A route that accepts uploads raises the limit for itself, without the whole
// server having to accept bodies that size.
func TestHTTP_bodyLimitPerRoute(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, &HTTPOptions{BodyLimit: 32})

	handler := func(c IHTTPContext) error { return c.JSON(http.StatusOK, "ok") }
	e.POST("/small", handler)
	e.POST("/large", handler, BodyLimit(1<<20))

	big := `{"note":"` + strings.Repeat("z", 500) + `"}`
	assert.Equal(t, http.StatusRequestEntityTooLarge, doJSON(e, http.MethodPost, "/small", big).Code)
	assert.Equal(t, http.StatusOK, doJSON(e, http.MethodPost, "/large", big).Code,
		"a route middleware runs inside the server-wide one, so its own limit applies")
}
