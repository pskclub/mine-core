package coretest_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/coretest"
	"github.com/pskclub/mine-core/v2/repository"
)

// Serve runs the real listener, so this exercises serialisation and the socket
// — not just the handler function.
func TestServe_overARealSocket(t *testing.T) {
	app := coretest.NewApp(t, coretest.WithAutoMigrate(&user{}))
	e := core.NewHTTPServer(app, nil)
	e.GET("/healthz", func(c core.IHTTPContext) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	client := coretest.Serve(t, e)

	assert.Contains(t, client.BaseURL(), "http://127.0.0.1:")
	assert.Equal(t, "ok", client.Get("/healthz").RequireStatus(http.StatusOK).Map()["status"])
}

// The assertions are the same ones the in-memory server uses — only the
// transport changed.
func TestServe_errorsDecodeIdentically(t *testing.T) {
	app := coretest.NewApp(t, coretest.WithAutoMigrate(&user{}))
	e := core.NewHTTPServer(app, nil)
	e.POST("/users", func(c core.IHTTPContext) error {
		var req createUser
		return c.BindWithValidate(&req)
	})

	client := coretest.Serve(t, e)

	body := client.Post("/users", `{"email":"nope"}`).RequireStatus(http.StatusBadRequest).Error()

	assert.Equal(t, "INVALID_PARAMS", body.Code)
	assert.Equal(t, "INVALID_EMAIL", body.Fields["email"].Code)
	assert.Equal(t, "body", body.Fields["email"].In)
}

// A test can seed through the App and read the result back over HTTP.
func TestServe_sharesTheDatabaseWithTheTest(t *testing.T) {
	app := coretest.NewApp(t, coretest.WithAutoMigrate(&user{}))
	e := core.NewHTTPServer(app, nil)
	e.GET("/count", func(c core.IHTTPContext) error {
		n, err := repository.New[user](c).Count()
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]int64{"count": n})
	})

	client := coretest.Serve(t, e)

	ctx := app.NewContext(t.Context(), core.ModeTest)
	require.Nil(t, repository.New[user](ctx).Create(&user{Email: "over-http@b.co"}))

	assert.Equal(t, float64(1), client.Get("/count").RequireStatus(http.StatusOK).Map()["count"])
}

func TestClient_pinsHeaders(t *testing.T) {
	app := coretest.NewApp(t)
	e := core.NewHTTPServer(app, nil)
	e.GET("/echo", func(c core.IHTTPContext) error {
		return c.JSON(http.StatusOK, map[string]string{"auth": c.Request().Header.Get("Authorization")})
	})

	client := coretest.Serve(t, e, coretest.WithHeader("Authorization", "Bearer t0ken"))

	assert.Equal(t, "Bearer t0ken", client.Get("/echo").Map()["auth"])
}

// Against a deployed service. Skips when E2E_BASE_URL is unset so a normal
// `go test ./...` stays green without anything running.
func TestClientFromEnv(t *testing.T) {
	client := coretest.NewClientFromEnv(t)

	client.WaitReady("/", 10*time.Second)
	client.Get("/").RequireStatus(http.StatusOK)
}

// Go cancels a test's context just before its Cleanup functions run, so a
// client bound to it could not be used to undo what the test created — which is
// the main reason a test holds a client at the end.
func TestClient_usableFromCleanup(t *testing.T) {
	app := coretest.NewApp(t)
	e := core.NewHTTPServer(app, nil)
	e.DELETE("/thing/:id", func(c core.IHTTPContext) error { return c.NoContent(http.StatusNoContent) })

	client := coretest.Serve(t, e)

	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			t.Error("cleanup did not run")
		}
	})
	t.Cleanup(func() {
		client.Delete("/thing/42").RequireStatus(http.StatusNoContent)
		cleaned = true
	})
}
