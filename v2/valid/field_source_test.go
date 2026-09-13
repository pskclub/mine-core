package valid_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/valid"
)

// updateUserReq is the shape that motivated field sources: a path parameter, a
// query parameter and a body field, all reported in one flat map.
type updateUserReq struct {
	ID       *string `param:"id" json:"-"`
	Sort     *string `query:"sort" json:"-"`
	FullName *string `json:"full_name"`
}

func (r *updateUserReq) Valid(ctx core.IContext) core.IError {
	v := valid.New(ctx)
	v.Str("id", r.ID).Required().UUID()
	v.Str("sort", r.Sort).Required()
	v.Str("full_name", r.FullName).Required()
	return v.Error()
}

type errBody struct {
	Code   string `json:"code"`
	Fields map[string]struct {
		Code string `json:"code"`
		In   string `json:"in"`
	} `json:"fields"`
}

func newApp(t *testing.T) *core.App {
	t.Helper()
	t.Setenv("APP_ENV", "test")
	t.Setenv("APP_SERVICE", "field-source-test")
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	app, err := core.NewApp(env)
	require.NoError(t, err)
	return app
}

// End to end: bind a request from three different places, fail all three, and
// check the response says which part of the request each belongs to.
func TestFieldSources_overHTTP(t *testing.T) {
	e := core.NewHTTPServer(newApp(t), nil)
	e.PUT("/users/:id", func(c core.IHTTPContext) error {
		var req updateUserReq
		return c.BindWithValidate(&req)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/users/not-a-uuid", strings.NewReader(`{}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var body errBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, "INVALID_PARAMS", body.Code)
	assert.Equal(t, "path", body.Fields["id"].In, "id comes from /users/:id")
	assert.Equal(t, "query", body.Fields["sort"].In, "sort comes from ?sort=")
	assert.Equal(t, "body", body.Fields["full_name"].In)

	// the existing shape is untouched — only a key was added
	assert.Equal(t, "INVALID_UUID", body.Fields["id"].Code)
}

// Validation outside a request (a job's parameters) has no binding tags to read,
// so fields stay unlabelled rather than mislabelled.
func TestFieldSources_absentOutsideHTTP(t *testing.T) {
	ctx := newApp(t).NewContext(t.Context())

	v := valid.New(ctx)
	v.Str("email", nil).Required()
	err := v.Error()
	require.NotNil(t, err)

	raw, mErr := json.Marshal(err.JSON())
	require.NoError(t, mErr)

	var body errBody
	require.NoError(t, json.Unmarshal(raw, &body))
	assert.Empty(t, body.Fields["email"].In, "no source is better than a guessed one")
}

func TestNormalizeFieldPath(t *testing.T) {
	assert.Equal(t, "items.name", valid.NormalizeFieldPath("items.0.name"))
	assert.Equal(t, "items.name", valid.NormalizeFieldPath("items.17.name"))
	assert.Equal(t, "email", valid.NormalizeFieldPath("email"))
	assert.Equal(t, "a.b.c", valid.NormalizeFieldPath("a.b.c"))
}
