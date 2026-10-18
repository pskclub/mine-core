package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The field-source labels already name "header" as a place a value comes from;
// BindOnly has to actually read it, or a `header:` tag is documentation only.
func TestHTTP_bindOnly_readsHeaders(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	e.GET("/whoami", func(c IHTTPContext) error {
		var req struct {
			Tenant string `header:"X-Tenant"`
			Q      string `query:"q"`
		}
		if err := c.BindOnly(&req); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"tenant": req.Tenant, "q": req.Q})
	})

	r := httptest.NewRequest(http.MethodGet, "/whoami?q=x", nil)
	r.Header.Set("X-Tenant", "acme")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "acme", body["tenant"])
	assert.Equal(t, "x", body["q"], "the other sources are still bound")
}

// An allowlist with nothing on it allows nothing. Reading it as "anything goes"
// would make the safest-looking call the least safe one.
func TestHTTP_getPageOptionsWithAllowed_noColumnsMeansNoOrdering(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	var got *PageOptions
	e.GET("/list", func(c IHTTPContext) error {
		got = c.GetPageOptionsWithAllowed()
		return c.JSON(http.StatusOK, "ok")
	})

	doJSON(e, http.MethodGet, "/list?order_by=name%20asc", "")
	require.NotNil(t, got)
	assert.Empty(t, got.OrderBy, "a client cannot order by a column nobody allowed")
}
