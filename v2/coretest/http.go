package coretest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	gormlogger "gorm.io/gorm/logger"

	core "github.com/pskclub/mine-core/v2"
)

// gormSilent keeps a test's output to what the test itself prints. Query logging
// is available through the app logger when you want it.
func gormSilent() gormlogger.Interface {
	return gormlogger.Default.LogMode(gormlogger.Silent)
}

// Server is an HTTP server wired to a test App, plus the client for driving it.
// Requests go through the real middleware stack — request id, recovery, the
// IError renderer — so what a test asserts is what a caller receives.
type Server struct {
	*core.Server

	t   *testing.T
	app *core.App
}

// NewServer builds a server on a test App. Register routes on it as usual:
//
//	srv := coretest.NewServer(t, coretest.WithAutoMigrate(&User{}))
//	srv.POST("/users", controller.Create)
//
//	res := srv.Post("/users", `{"email":"a@b.co"}`)
//	res.RequireStatus(http.StatusCreated)
func NewServer(t *testing.T, opts ...Option) *Server {
	t.Helper()

	app := NewApp(t, opts...)
	return &Server{Server: core.NewHTTPServer(app, nil), t: t, app: app}
}

// NewServerWithApp wraps an existing App, for a test that also needs a scheduler
// or several contexts over the same pools.
func NewServerWithApp(t *testing.T, app *core.App, opts *core.HTTPOptions) *Server {
	t.Helper()
	return &Server{Server: core.NewHTTPServer(app, opts), t: t, app: app}
}

// Mount registers a ModuleSet's routes and returns the server, so a test drives
// exactly what production serves:
//
//	srv := coretest.NewServer(t, coretest.WithAutoMigrate(&Note{})).Mount(cmd.Modules(app))
//	srv.Get("/notes").RequireStatus(http.StatusOK)
//
// Assembling the same set the composition root does is the point: a module
// whose registration was forgotten fails here rather than in production, which
// is the failure the whole module system exists to move earlier.
func (s *Server) Mount(set *core.ModuleSet) *Server {
	s.t.Helper()
	set.MountHTTP(s.Server)
	return s
}

// MountJobs registers a ModuleSet's job definitions on reg, for a test that
// triggers a module's job rather than calling its handler.
func (s *Server) MountJobs(set *core.ModuleSet, reg *core.JobRegistry) *Server {
	s.t.Helper()
	if err := set.MountJobs(reg); err != nil {
		s.t.Fatalf("coretest: mount module jobs: %v", err)
	}
	return s
}

// App exposes the App the server was built on.
func (s *Server) App() *core.App { return s.app }

// Context returns a context on the same pools, for seeding rows before a request.
func (s *Server) Context() core.IContext {
	return s.app.NewContext(s.t.Context(), core.ModeTest)
}

// Do sends a request. body may be nil, a string, []byte, or any value to encode
// as JSON.
func (s *Server) Do(method, path string, body any, headers ...map[string]string) *Response {
	s.t.Helper()

	req := httptest.NewRequest(method, path, bodyReader(s.t, body))
	if body != nil {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	return &Response{t: s.t, Code: rec.Code, Body: rec.Body.Bytes(), Header: rec.Header()}
}

// Get sends a GET.
func (s *Server) Get(path string, headers ...map[string]string) *Response {
	s.t.Helper()
	return s.Do(http.MethodGet, path, nil, headers...)
}

// Post sends a POST with a JSON body.
func (s *Server) Post(path string, body any, headers ...map[string]string) *Response {
	s.t.Helper()
	return s.Do(http.MethodPost, path, body, headers...)
}

// Put sends a PUT with a JSON body.
func (s *Server) Put(path string, body any, headers ...map[string]string) *Response {
	s.t.Helper()
	return s.Do(http.MethodPut, path, body, headers...)
}

// Patch sends a PATCH with a JSON body.
func (s *Server) Patch(path string, body any, headers ...map[string]string) *Response {
	s.t.Helper()
	return s.Do(http.MethodPatch, path, body, headers...)
}

// Delete sends a DELETE.
func (s *Server) Delete(path string, headers ...map[string]string) *Response {
	s.t.Helper()
	return s.Do(http.MethodDelete, path, nil, headers...)
}

func bodyReader(t *testing.T, body any) io.Reader {
	t.Helper()

	switch v := body.(type) {
	case nil:
		return nil
	case string:
		return strings.NewReader(v)
	case []byte:
		return bytes.NewReader(v)
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("coretest: encode request body: %v", err)
		}
		return bytes.NewReader(raw)
	}
}

// Response is one recorded HTTP response.
type Response struct {
	Code   int
	Body   []byte
	Header http.Header

	t *testing.T
}

// RequireStatus fails the test unless the status matches, printing the body —
// which is where the reason usually is.
func (r *Response) RequireStatus(want int) *Response {
	r.t.Helper()

	if r.Code != want {
		r.t.Fatalf("status = %d, want %d\nbody: %s", r.Code, want, r.Body)
	}
	return r
}

// JSON decodes the body into dest.
func (r *Response) JSON(dest any) *Response {
	r.t.Helper()

	if err := json.Unmarshal(r.Body, dest); err != nil {
		r.t.Fatalf("coretest: decode response: %v\nbody: %s", err, r.Body)
	}
	return r
}

// Map decodes the body as a generic object.
func (r *Response) Map() map[string]any {
	r.t.Helper()

	out := map[string]any{}
	r.JSON(&out)
	return out
}

// String returns the raw body.
func (r *Response) String() string { return string(r.Body) }

// ErrorBody is the framework's error shape: what every failed request returns.
type ErrorBody struct {
	Code    string                `json:"code"`
	Message string                `json:"message"`
	Fields  map[string]FieldError `json:"fields"`
}

// FieldError is one field's violation. In reports where the value was bound
// from — "path", "query", "header" or "body".
type FieldError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	In      string `json:"in"`
	Data    any    `json:"data"`
}

// Error decodes the body as a framework error. Use it to assert on a rejection
// without re-declaring the response shape in every service:
//
//	body := srv.Post("/users", `{}`).RequireStatus(400).Error()
//	assert.Equal(t, "REQUIRED", body.Fields["email"].Code)
//	assert.Equal(t, "body", body.Fields["email"].In)
func (r *Response) Error() ErrorBody {
	r.t.Helper()

	var body ErrorBody
	r.JSON(&body)
	if body.Code == "" {
		r.t.Fatalf("coretest: not an error response\nbody: %s", r.Body)
	}
	return body
}

// FieldCodes reduces a validation failure to field -> code, for asserting on the
// whole set at once.
func (r *Response) FieldCodes() map[string]string {
	r.t.Helper()

	out := map[string]string{}
	for field, fe := range r.Error().Fields {
		out[field] = fe.Code
	}
	return out
}
