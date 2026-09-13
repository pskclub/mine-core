package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type createUserReq struct {
	Email *string `json:"email"`
	Age   *int64  `json:"age"`
}

func (r *createUserReq) Valid(ctx IContext) IError {
	fields := map[string]any{}
	if r.Email == nil || *r.Email == "" {
		fields["email"] = map[string]string{"code": "REQUIRED", "message": "email is required"}
	}
	if len(fields) > 0 {
		return New(http.StatusBadRequest, "INVALID_PARAMS", "Invalid parameters").WithFields(fields)
	}
	return nil
}

func newHTTPTestApp(t *testing.T) *App {
	t.Helper()
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "http-test"})
	app, err := NewApp(env)
	require.NoError(t, err)
	return app
}

func TestHTTP_bindValidate_ok(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	e.POST("/users", func(c IHTTPContext) error {
		var req createUserReq
		if err := c.BindWithValidate(&req); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"email": *req.Email})
	})

	rec := doJSON(e, http.MethodPost, "/users", `{"email":"a@b.com","age":30}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestHTTP_bindValidate_validationError(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	e.POST("/users", func(c IHTTPContext) error {
		var req createUserReq
		if err := c.BindWithValidate(&req); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, "ok")
	})

	rec := doJSON(e, http.MethodPost, "/users", `{"age":30}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	var body struct {
		Code   string         `json:"code"`
		Fields map[string]any `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "INVALID_PARAMS", body.Code)
	assert.NotNil(t, body.Fields["email"])
}

func TestHTTP_invalidJSON(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	e.POST("/users", func(c IHTTPContext) error {
		var req createUserReq
		if err := c.BindWithValidate(&req); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, "ok")
	})

	rec := doJSON(e, http.MethodPost, "/users", `{not json`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHTTP_panicRecovered(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	e.GET("/boom", func(c IHTTPContext) error {
		panic("kaboom")
	})

	rec := doJSON(e, http.MethodGet, "/boom", "")
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "panic must be recovered as 500")
}

func TestHTTP_pageOptions(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	var got *PageOptions
	e.GET("/list", func(c IHTTPContext) error {
		got = c.GetPageOptionsWithAllowed("name", "created_at")
		return c.JSON(http.StatusOK, "ok")
	})

	doJSON(e, http.MethodGet, "/list?limit=5&page=2&order_by=name%20asc,evil_col%20desc", "")
	require.NotNil(t, got)
	assert.Equal(t, int64(5), got.Limit)
	assert.Equal(t, int64(2), got.Page)
	assert.Equal(t, []string{"name asc"}, got.OrderBy, "order_by allowlist should drop evil_col")
}

// Without an allowlist order_by still may not become SQL: GORM passes the string
// to the driver verbatim, so anything that is not a column identifier is dropped.
func TestHTTP_pageOptionsRejectsNonColumnOrderBy(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"plain column", "name", []string{"name desc"}},
		{"direction kept", "name asc", []string{"name asc"}},
		{"table qualified", "users.created_at asc", []string{"users.created_at asc"}},
		{"injected statement", "id; DROP TABLE users", nil},
		{"injected subquery", "(SELECT 1)", nil},
		{"function call", "sleep(10)", nil},
		{"quoted", `"id"`, nil},
		{"leading digit", "1", nil},
		{"good and evil mixed", "name asc,id;DROP TABLE users", []string{"name asc"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newHTTPTestApp(t)
			e := NewHTTPServer(app, nil)
			var got *PageOptions
			e.GET("/list", func(c IHTTPContext) error {
				got = c.GetPageOptions()
				return c.JSON(http.StatusOK, "ok")
			})

			doJSON(e, http.MethodGet, "/list?order_by="+url.QueryEscape(tc.input), "")
			require.NotNil(t, got)
			assert.Equal(t, tc.want, emptyToNil(got.OrderBy))
		})
	}
}

// emptyToNil lets a case say "nothing survived" as nil rather than []string{}.
func emptyToNil(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

type multiReq struct {
	ID   string  `param:"id" json:"-"`
	Q    string  `query:"q"`
	Name *string `json:"name"`
}

func (r *multiReq) Valid(ctx IContext) IError { return nil }

func TestHTTP_bindAllSources(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	var got multiReq
	e.POST("/items/:id", func(c IHTTPContext) error {
		if err := c.BindWithValidate(&got); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, "ok")
	})

	rec := doJSON(e, http.MethodPost, "/items/42?q=search", `{"name":"widget"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "42", got.ID, "path param")
	assert.Equal(t, "search", got.Q, "query param")
	require.NotNil(t, got.Name)
	assert.Equal(t, "widget", *got.Name, "json body")
}

type formReq struct {
	Name string `form:"name"`
	Age  int    `form:"age"`
}

func (r *formReq) Valid(ctx IContext) IError { return nil }

func TestHTTP_bindFormData(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	var got formReq
	e.POST("/form", func(c IHTTPContext) error {
		if err := c.BindWithValidate(&got); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, "ok")
	})

	rec := doForm(e, http.MethodPost, "/form", "name=bob&age=25")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "bob", got.Name)
	assert.Equal(t, 25, got.Age)
}

type queryOnlyReq struct {
	Page int    `query:"page"`
	Sort string `query:"sort"`
}

func (r *queryOnlyReq) Valid(ctx IContext) IError { return nil }

func TestHTTP_bindQueryOnGET(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	var got queryOnlyReq
	e.GET("/search", func(c IHTTPContext) error {
		if err := c.BindWithValidate(&got); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, "ok")
	})

	doJSON(e, http.MethodGet, "/search?page=3&sort=name", "")
	assert.Equal(t, 3, got.Page)
	assert.Equal(t, "name", got.Sort)
}

func doJSON(e *Server, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func doForm(e *Server, method, target, form string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(form))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func doWithHeaders(e *Server, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// Middleware built at startup — authentication resolving a token against the
// database, say — needs the App. Without an accessor a module's registration
// function has to take it as a second parameter purely to pass it on, which is
// the threading Server exists to remove.
func TestHTTP_serverExposesItsApp(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)

	assert.Same(t, app, e.App())
	assert.Same(t, app, e.Group("/x").App(), "a group belongs to the same App")
	assert.Same(t, app, e.Group("/x").Group("/y").App(), "and so does a nested one")
}
