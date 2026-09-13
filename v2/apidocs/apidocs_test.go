package apidocs

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	scalargo "github.com/bdpiprava/scalar-go"
	"github.com/labstack/echo/v5"
	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// specJSON is the smallest document that is still a real one: it has to survive
// the renderer, which parses it, and the servers modifier, which rewrites it.
const specJSON = `{
  "openapi": "3.1.0",
  "info": {"title": "Notes API", "version": "1.2.3"},
  "servers": [{"url": "https://api.example.com"}],
  "paths": {
    "/notes": {
      "get": {
        "operationId": "listNotes",
        "responses": {"200": {"description": "OK"}}
      }
    }
  }
}`

const specYAML = `openapi: 3.1.0
info:
  title: Notes API
  version: 1.2.3
paths:
  /notes:
    get:
      operationId: listNotes
      responses:
        "200":
          description: OK
`

func newServer(t *testing.T, kv map[string]string) *core.Server {
	t.Helper()
	for k, v := range kv {
		t.Setenv("APP_"+k, v)
	}
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	app, ierr := core.NewApp(env)
	require.Nil(t, ierr)
	return core.NewHTTPServer(app, nil)
}

func get(s *core.Server, target string, headers ...map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// pageConfig is the object the page hands Scalar, read back out of the rendered
// HTML. Asserting on the substring would pass on a key that landed in the spec
// rather than in the configuration, which is a difference the browser cares
// about and a grep does not.
func pageConfig(t *testing.T, page string) map[string]any {
	t.Helper()

	const marker = "Scalar.createApiReference('#app', "
	start := strings.Index(page, marker)
	require.GreaterOrEqual(t, start, 0, "page does not initialise Scalar")
	rest := page[start+len(marker):]

	end := strings.Index(rest, ");")
	require.GreaterOrEqual(t, end, 0, "page has no closing call")

	var config map[string]any
	require.NoError(t, json.Unmarshal([]byte(rest[:end]), &config), rest[:end])
	return config
}

func basicAuthHeader(user, password string) map[string]string {
	return map[string]string{
		echo.HeaderAuthorization: "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password)),
	}
}

// requireAdmin stands in for whatever a service actually guards its admin area
// with.
func requireAdmin() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if c.Request().Header.Get("X-Admin") != "yes" {
				return core.New(http.StatusUnauthorized, "UNAUTHORIZED", "no")
			}
			return next(c)
		}
	}
}

// The reference names every endpoint, every field and every validation rule of
// the service. Mounting it unguarded outside dev has to fail at startup,
// because the failure mode is silent: nothing breaks, the API surface is simply
// readable by whoever finds the path.
func TestMount_refusesUnprotectedOutsideDev(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "prod", "SERVICE": "svc"})

	err := Mount(s, Options{Spec: []byte(specJSON)})
	require.NotNil(t, err, "mounting unprotected in prod must fail")
	assert.Equal(t, "APIDOCS_UNPROTECTED", err.GetCode())
	assert.Contains(t, err.GetMessage(), "prod", "the message should name the environment that refused")

	assert.Equal(t, http.StatusNotFound, get(s, DefaultPrefix).Code,
		"a refused mount must not leave routes behind")
}

// An unset APP_ENV is what a deployment that forgot to set it looks like, and
// the safe reading of "we do not know where this is running" is not dev.
func TestMount_refusesUnprotectedWithUnsetEnv(t *testing.T) {
	s := newServer(t, map[string]string{"SERVICE": "svc"})

	err := Mount(s, Options{Spec: []byte(specJSON)})
	require.NotNil(t, err, "an unset APP_ENV must take the guarded path")
	assert.Equal(t, "APIDOCS_UNPROTECTED", err.GetCode())
}

func TestMount_allowsGuardedOutsideDev(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "prod", "SERVICE": "svc"})

	require.Nil(t, Mount(s, Options{
		Spec: []byte(specJSON),
		Auth: []echo.MiddlewareFunc{requireAdmin()},
	}))

	assert.Equal(t, http.StatusUnauthorized, get(s, DefaultPrefix).Code,
		"the caller's guard has to run on the page itself")
	assert.Equal(t, http.StatusOK, get(s, DefaultPrefix, map[string]string{"X-Admin": "yes"}).Code)
	assert.Equal(t, http.StatusUnauthorized, get(s, DefaultPrefix+specPath).Code,
		"the raw document is the same secret as the page and needs the same guard")
}

// A password in the environment is what turns the reference on for a staging
// deployment without a code change, which is the whole reason it is read there.
func TestMount_basicAuthFromEnvironment(t *testing.T) {
	s := newServer(t, map[string]string{
		"ENV":              "prod",
		"SERVICE":          "svc",
		"APIDOCS_PASSWORD": "s3cret",
	})

	require.Nil(t, Mount(s, Options{Spec: []byte(specJSON)}))

	unauthorized := get(s, DefaultPrefix)
	assert.Equal(t, http.StatusUnauthorized, unauthorized.Code)
	assert.Contains(t, unauthorized.Header().Get(echo.HeaderWWWAuthenticate), "Basic",
		"without the challenge header the browser never shows a login box")

	assert.Equal(t, http.StatusOK,
		get(s, DefaultPrefix, basicAuthHeader(DefaultBasicAuthUser, "s3cret")).Code)
	assert.Equal(t, http.StatusUnauthorized,
		get(s, DefaultPrefix, basicAuthHeader(DefaultBasicAuthUser, "wrong")).Code)
}

// A BasicAuth with a user and no password refuses everybody, including whoever
// configured it, and the failure looks like a forgotten password rather than a
// missing setting. Refusing at mount says which it is.
func TestMount_refusesBasicAuthWithoutPassword(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})

	err := Mount(s, Options{Spec: []byte(specJSON), BasicAuth: &BasicAuth{User: "sa"}})
	require.NotNil(t, err)
	assert.Equal(t, "APIDOCS_NO_PASSWORD", err.GetCode())
}

func TestMount_requiresADocument(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})

	err := Mount(s, Options{})
	require.NotNil(t, err, "a reference with no document is a blank page nobody can debug")
	assert.Equal(t, "APIDOCS_NO_SPEC", err.GetCode())
}

// A path that does not exist is the usual mistake — the generated file was not
// committed, or the deployment did not copy it — and it has to stop the boot
// rather than serve an empty page.
func TestMount_reportsAnUnreadableSpecFile(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})

	err := Mount(s, Options{SpecFile: filepath.Join(t.TempDir(), "missing.json")})
	require.NotNil(t, err)
	assert.Equal(t, "APIDOCS_SPEC_UNREADABLE", err.GetCode())
	assert.Contains(t, err.GetMessage(), "missing.json", "the message has to name the file it looked for")
}

func TestMount_readsASpecFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "openapi.json")
	require.NoError(t, os.WriteFile(path, []byte(specJSON), 0o600))

	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{SpecFile: path}))

	rec := get(s, DefaultPrefix+specPath)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, specJSON, rec.Body.String(),
		"the raw route must serve the document unchanged, since tools read it rather than the page")
}

// The document is read once at mount. A file edited afterwards must not change
// what is served, or two readers of the same page disagree about the API and
// neither knows why.
func TestMount_serves_theDocumentReadAtMount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openapi.json")
	require.NoError(t, os.WriteFile(path, []byte(specJSON), 0o600))

	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{SpecFile: path}))
	require.NoError(t, os.WriteFile(path, []byte(`{"openapi":"3.1.0"}`), 0o600))

	assert.JSONEq(t, specJSON, get(s, DefaultPrefix+specPath).Body.String())
}

func TestMount_bothPrefixSpellingsResolve(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Spec: []byte(specJSON), Prefix: "docs"}))

	assert.Equal(t, http.StatusOK, get(s, "/docs").Code)
	assert.Equal(t, http.StatusOK, get(s, "/docs/").Code)
	assert.Equal(t, http.StatusOK, get(s, "/docs/openapi").Code)
}

// The page is the product: if the document did not reach the browser, the
// reference is a blank frame and the reader has no way to tell that from an
// outage.
func TestMount_pageCarriesTheDocument(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Spec: []byte(specJSON)}))

	rec := get(s, DefaultPrefix)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assert.Contains(t, rec.Header().Get(echo.HeaderContentType), echo.MIMETextHTML)
	assert.Contains(t, body, "Notes API", "the page has to carry the document, not just a frame for it")
	assert.Contains(t, body, "listNotes")
	assert.Contains(t, body, scalargo.DefaultCDN, "the bundle is loaded from the default CDN unless one is named")
	assert.Contains(t, body, DefaultPrefix+specPath,
		"the fallback has to link somewhere the reader can still reach when the bundle does not load")
}

func TestMount_usesTheNamedCDN(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{
		Spec: []byte(specJSON),
		CDN:  "https://assets.internal/scalar.js",
	}))

	body := get(s, DefaultPrefix).Body.String()
	assert.Contains(t, body, "https://assets.internal/scalar.js")
	assert.NotContains(t, body, scalargo.DefaultCDN,
		"a deployment that named its own bundle has no route to the public one")
}

// The panel sends real requests. A document generated against another host
// would send them there, which is the one mistake in this package that reaches
// outside the process.
func TestMount_serversOverrideReachesThePage(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{
		Spec:    []byte(specJSON),
		Servers: []Server{{URL: "/", Description: "This deployment"}},
	}))

	body := get(s, DefaultPrefix).Body.String()
	assert.Contains(t, body, "This deployment")
	assert.NotContains(t, body, "api.example.com",
		"the overridden server must not survive into the page, or requests go to the wrong host")
}

// A document naming no server gives Scalar nothing to send to, and the button
// the reader came for does nothing.
func TestMount_addsSameOriginServerWhenTheDocumentNamesNone(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Spec: []byte(specYAML)}))

	body := get(s, DefaultPrefix).Body.String()
	assert.Contains(t, body, "Same origin as this page")
}

func TestMount_servesYAMLUnchanged(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Spec: []byte(specYAML)}))

	rec := get(s, DefaultPrefix+specPath)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, specYAML, rec.Body.String())
	assert.Contains(t, rec.Header().Get(echo.HeaderContentType), "yaml",
		"a YAML document served as JSON is one a downstream tool refuses to parse")
}

// The page exists to be sent from, so the sidebar defaults to a request list:
// tags open, models out. Both are Scalar keys scalar-go has no option for, and
// a typo in either is invisible on the page — hence pinning them here.
func TestMount_defaultsToARequestList(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Spec: []byte(specJSON)}))

	config := pageConfig(t, get(s, DefaultPrefix).Body.String())
	assert.Equal(t, true, config[configOpenAllTags],
		"every operation has to be listed without opening its group")
	assert.Equal(t, true, config[configHideModels],
		"the sidebar should list what can be sent, not what can be read")
	assert.Equal(t, true, config["persistAuth"],
		"a token typed once per visit is what makes the panel usable at all")
}

// Documentation for readers is the other job the same page can do, and the
// defaults above are wrong for it.
func TestMount_referenceModeLeavesTheDocumentAsWritten(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Spec: []byte(specJSON), Reference: true}))

	config := pageConfig(t, get(s, DefaultPrefix).Body.String())
	assert.NotContains(t, config, configOpenAllTags)
	assert.NotContains(t, config, configHideModels)
}

// ScalarConfig is the way past both this package's opinions and scalar-go's
// option list, so a caller must be able to undo what Mount decided.
func TestScalarConfig_overridesTheDefaults(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{
		Spec: []byte(specJSON),
		Scalar: []scalargo.Option{
			ScalarConfig(configHideModels, false),
			ScalarConfig("hideSearch", true),
		},
	}))

	config := pageConfig(t, get(s, DefaultPrefix).Body.String())
	assert.Equal(t, false, config[configHideModels],
		"an option passed by the caller runs last and has to win")
	assert.Equal(t, true, config["hideSearch"])
}

func TestMount_titleOverridesTheDocument(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	require.Nil(t, Mount(s, Options{Spec: []byte(specJSON), Title: "Staging — Notes"}))

	assert.Contains(t, get(s, DefaultPrefix).Body.String(), "Staging")
}

// A document the renderer cannot parse has to stop the boot. The alternative is
// a page that renders empty, which reads as "this service has no endpoints".
func TestMount_reportsAnUnparsableDocument(t *testing.T) {
	s := newServer(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})

	err := Mount(s, Options{Spec: []byte("{ not a spec")})
	require.NotNil(t, err)
	assert.Equal(t, "APIDOCS_RENDER", err.GetCode())
}

func TestNormalizePrefix(t *testing.T) {
	cases := map[string]string{
		"":       DefaultPrefix,
		"/":      DefaultPrefix,
		"_docs":  "/_docs",
		"/_docs": "/_docs",
		"/docs/": "/docs",
		"docs/v": "/docs/v",
	}
	for in, want := range cases {
		assert.Equal(t, want, normalizePrefix(in), "prefix %q", in)
	}
}

func TestSpecContentType(t *testing.T) {
	assert.Equal(t, echo.MIMEApplicationJSON, specContentType([]byte("  \n{\"a\":1}")))
	assert.True(t, strings.Contains(specContentType([]byte("openapi: 3.1.0")), "yaml"))
}
