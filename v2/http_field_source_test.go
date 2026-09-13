package core

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sourceSpy stands in for a validation error, so this package can test the
// wiring without importing valid (which imports core).
type sourceSpy struct {
	IError // embedded as an interface so Error() stays a method, not a field
	got    map[string]string
}

func (s *sourceSpy) SetFieldSources(m map[string]string) { s.got = m }

func sourcesOf(v any) map[string]string {
	out := map[string]string{}
	collectFieldSources(reflect.TypeOf(v), "", 0, out)
	return out
}

// The shape that motivated this: one request binding a path param, a query
// param and a body field, previously indistinguishable in the response.
func TestFieldSource_pathQueryAndBody(t *testing.T) {
	type req struct {
		ID       *string `param:"id" json:"-"`
		Sort     *string `query:"sort" json:"-"`
		FullName *string `json:"full_name"`
	}

	got := sourcesOf(&req{})

	assert.Equal(t, SourcePath, got["id"])
	assert.Equal(t, SourceQuery, got["sort"])
	assert.Equal(t, SourceBody, got["full_name"])
}

// A field bound from the path *and* named in the body is a path parameter:
// that is where the value actually comes from, so that is what to correct.
func TestFieldSource_mostSpecificTagWins(t *testing.T) {
	type req struct {
		ID *string `param:"id" json:"id"`
	}
	assert.Equal(t, SourcePath, sourcesOf(&req{})["id"])
}

func TestFieldSource_nestedAndSliceFields(t *testing.T) {
	type item struct {
		Name *string `json:"name"`
	}
	type addr struct {
		Zip *string `json:"zip"`
	}
	type req struct {
		Page    *int64  `query:"page"`
		Billing *addr   `json:"billing"`
		Items   []*item `json:"items"`
	}

	got := sourcesOf(&req{})

	assert.Equal(t, SourceQuery, got["page"])
	assert.Equal(t, SourceBody, got["billing.zip"], "nested body fields keep their path")
	assert.Equal(t, SourceBody, got["items.name"], "slice element fields carry no index")
}

// A path or query parameter is always scalar; recursing into one would invent
// names no binder ever produces.
func TestFieldSource_doesNotDescendIntoNonBodyFields(t *testing.T) {
	type inner struct {
		Deep *string `json:"deep"`
	}
	type req struct {
		Filter *inner `query:"filter"`
	}

	got := sourcesOf(&req{})

	assert.Equal(t, SourceQuery, got["filter"])
	assert.NotContains(t, got, "filter.deep")
}

// An embedded struct contributes its fields at the parent's level, matching how
// the binder fills them.
func TestFieldSource_embeddedStruct(t *testing.T) {
	type req struct {
		Paging
		Name *string `json:"name"`
	}

	got := sourcesOf(&req{})

	assert.Equal(t, SourceQuery, got["limit"], "embedded fields are bound as if declared here")
	assert.Equal(t, SourceBody, got["name"])
}

// Paging is embedded by the test above. It has to be exported: reflection
// cannot set through an unexported embedded field, so the binder could not fill
// one either — and labelling a field nothing can populate would be a lie.
type Paging struct {
	Limit *int64 `query:"limit"`
}

func TestFieldSource_skipsIgnoredAndUnexported(t *testing.T) {
	type req struct {
		Skipped *string `json:"-"`
		hidden  *string //nolint:unused // deliberately unexported
		Kept    *string `json:"kept"`
	}

	got := sourcesOf(&req{})

	assert.NotContains(t, got, "-")
	assert.NotContains(t, got, "hidden")
	assert.Equal(t, SourceBody, got["kept"])
}

// A recursive payload must not send the walk into an endless descent.
func TestFieldSource_recursiveTypeTerminates(t *testing.T) {
	type node struct {
		Name  *string `json:"name"`
		Child *node   `json:"child"`
	}

	done := make(chan map[string]string, 1)
	go func() { done <- sourcesOf(&node{}) }()

	select {
	case got := <-done:
		assert.Equal(t, SourceBody, got["name"])
		assert.Equal(t, SourceBody, got["child.name"])
	case <-time.After(5 * time.Second):
		t.Fatal("collectFieldSources did not terminate on a recursive type")
	}
}

func TestMarkFieldSources_onlyTouchesSourcedErrors(t *testing.T) {
	type req struct {
		ID *string `param:"id"`
	}

	spy := &sourceSpy{IError: New(400, "INVALID_PARAMS", "bad")}
	markFieldSources(&req{}, spy)
	require.NotNil(t, spy.got)
	assert.Equal(t, SourcePath, spy.got["id"])

	// an error that cannot record sources is left alone, not wrapped or replaced
	plain := New(400, "INVALID_PARAMS", "bad")
	markFieldSources(&req{}, plain)
	assert.Equal(t, "INVALID_PARAMS", plain.GetCode())

	// and a nil payload is a no-op rather than a panic
	spy2 := &sourceSpy{IError: New(400, "X", "x")}
	markFieldSources(nil, spy2)
	assert.Nil(t, spy2.got)
}
