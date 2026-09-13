package devtools

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getWithLogin(s *core.Server, target, user, password string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetBasicAuth(user, password)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// The shortest path to a guarded mount: a username and a password, and the
// browser does the rest.
func TestBasicAuth_promptsAndAccepts(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "prod", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{BasicAuth: &BasicAuth{User: "ops", Password: "s3cret"}}))

	anonymous := get(s, DefaultPrefix)
	require.Equal(t, http.StatusUnauthorized, anonymous.Code)
	assert.Contains(t, anonymous.Header().Get(echo.HeaderWWWAuthenticate), `Basic realm="devtools"`,
		"without the challenge header the browser shows a bare 401 instead of a login box")

	assert.Equal(t, http.StatusOK, getWithLogin(s, DefaultPrefix, "ops", "s3cret").Code)
	assert.Equal(t, http.StatusOK, getWithLogin(s, DefaultPrefix+"/api/overview", "ops", "s3cret").Code,
		"the API is behind the same lock as the page")
}

func TestBasicAuth_rejectsWrongCredentials(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "prod", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{BasicAuth: &BasicAuth{User: "ops", Password: "s3cret"}}))

	for _, c := range []struct{ user, password string }{
		{"ops", "wrong"},
		{"someone", "s3cret"},
		{"ops", ""},
		{"", ""},
	} {
		rec := getWithLogin(s, DefaultPrefix+"/api/config", c.user, c.password)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%q/%q", c.user, c.password)
		assert.NotContains(t, rec.Body.String(), "s3cret")
	}
}

func TestBasicAuth_defaultsTheUsername(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "prod", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{BasicAuth: &BasicAuth{Password: "s3cret"}}))

	assert.Equal(t, http.StatusOK, getWithLogin(s, DefaultPrefix, DefaultBasicAuthUser, "s3cret").Code)
}

// Turning the panel on for a staging box should not need a code change.
func TestBasicAuth_fromTheEnvironment(t *testing.T) {
	s := newServer(t, map[string]string{
		"ENV": "prod", "SERVICE": "svc",
		"DEVTOOLS_USER": "ops", "DEVTOOLS_PASSWORD": "from-env",
	})
	require.Nil(t, Mount(s, Options{}), "a password in the environment is a guard, so the mount is allowed")

	assert.Equal(t, http.StatusUnauthorized, get(s, DefaultPrefix).Code)
	assert.Equal(t, http.StatusOK, getWithLogin(s, DefaultPrefix, "ops", "from-env").Code)

	o := decode[overviewResponse](t, getWithLogin(s, DefaultPrefix+"/api/overview", "ops", "from-env"))
	assert.True(t, o.Protected, "the page must not warn that it is unguarded when it is")
}

// Code wins over configuration: a caller who wrote a password gets that
// password, whatever the environment says.
func TestBasicAuth_codeBeatsTheEnvironment(t *testing.T) {
	s := newServer(t, map[string]string{
		"ENV": "prod", "SERVICE": "svc", "DEVTOOLS_PASSWORD": "from-env",
	})
	require.Nil(t, Mount(s, Options{BasicAuth: &BasicAuth{User: "ops", Password: "from-code"}}))

	assert.Equal(t, http.StatusOK, getWithLogin(s, DefaultPrefix, "ops", "from-code").Code)
	assert.Equal(t, http.StatusUnauthorized, getWithLogin(s, DefaultPrefix, "ops", "from-env").Code)
}

// A lock with no key would refuse everybody, including whoever configured it,
// and would read as a forgotten password rather than a missing one.
func TestBasicAuth_refusesToMountWithNoPassword(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})

	err := Mount(s, Options{BasicAuth: &BasicAuth{User: "ops"}})
	require.NotNil(t, err)
	assert.Equal(t, "DEVTOOLS_NO_PASSWORD", err.GetCode())
	assert.Equal(t, http.StatusNotFound, get(s, DefaultPrefix).Code)
}

// The unprotected refusal now has three ways out, and the message has to name
// all of them or somebody goes looking for the one they were told about.
func TestBasicAuth_unprotectedMessageNamesEveryWayOut(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "prod", "SERVICE": "svc"})

	err := Mount(s, Options{})
	require.NotNil(t, err)
	for _, want := range []string{"APP_DEVTOOLS_PASSWORD", "Options.BasicAuth", "Options.Auth"} {
		assert.Contains(t, err.GetMessage(), want)
	}
}

// Both guards run, in the order that keeps a wrong password away from a guard
// that hits the database.
func TestBasicAuth_composesWithAuth(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "prod", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{
		BasicAuth: &BasicAuth{User: "ops", Password: "s3cret"},
		Auth:      []echo.MiddlewareFunc{requireAdmin()},
	}))

	// right password, no admin header
	assert.Equal(t, http.StatusUnauthorized, getWithLogin(s, DefaultPrefix, "ops", "s3cret").Code)

	req := httptest.NewRequest(http.MethodGet, DefaultPrefix, nil)
	req.SetBasicAuth("ops", "s3cret")
	req.Header.Set("X-Admin", "yes")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
