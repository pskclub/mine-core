package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bracket spelling is v1's. A client that already sends asc(name) keeps
// working against v2 rather than silently losing its ordering.
func TestParseOrderBy_bracketForm(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"asc", "asc(name)", []string{"name asc"}},
		{"desc", "desc(name)", []string{"name desc"}},
		{"any other function means desc, as in v1", "abc(name)", []string{"name desc"}},
		{"case insensitive", "ASC(name)", []string{"name asc"}},
		{"table qualified", "desc(users.created_at)", []string{"users.created_at desc"}},
		{"mixed with the space form", "desc(xxx),yyy asc", []string{"xxx desc", "yyy asc"}},
		{"doubled brackets are not unwrapped", "desc((name))", nil},
		{"no column", "asc()", nil},
		{"unclosed", "asc(name", nil},
		{"a real function call is still not a column", "sleep(10)", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, emptyToNil(ParseOrderBy(tc.input, nil)))
		})
	}
}

func TestParseOrderBy_allowlistAppliesToTheBracketForm(t *testing.T) {
	got := ParseOrderBy("asc(name),desc(secret)", []string{"name"})
	assert.Equal(t, []string{"name asc"}, got, "the allowlist sees the column, not the spelling")
}

func TestNewPage_carriesTheOptionsItWasPagedWith(t *testing.T) {
	opts := &PageOptions{Page: 3, Limit: 20, Q: "ann", OrderBy: []string{"name asc"}}

	page := NewPage([]string{"a", "b"}, 57, opts)

	assert.Equal(t, []string{"a", "b"}, page.Items)
	assert.Equal(t, int64(57), page.Total)
	assert.Equal(t, int64(2), page.Count, "count is this page, total is the whole result")
	assert.Equal(t, int64(3), page.Page)
	assert.Equal(t, int64(20), page.Limit)
	assert.Equal(t, "ann", page.Q)
	assert.Equal(t, []string{"name asc"}, page.OrderBy)
}

func TestNewPage_normalisesAndNeverReportsNullItems(t *testing.T) {
	page := NewPage[string](nil, 0, nil)

	assert.NotNil(t, page.Items, "an empty page serialises as [] rather than null")
	assert.Empty(t, page.Items)
	assert.Equal(t, PageLimitDefault, page.Limit)
	assert.Equal(t, int64(1), page.Page)

	clamped := NewPage([]int{1}, 1, &PageOptions{Limit: PageLimitMax * 5, Page: 0})
	assert.Equal(t, PageLimitMax, clamped.Limit, "the page reports the limit the query should have used")
	assert.Equal(t, int64(1), clamped.Page)
}

func TestMapPage_keepsTheMetadataAndConvertsTheItems(t *testing.T) {
	page := &Page[int]{
		Items: []int{1, 2, 3}, Total: 42, Count: 3, Page: 2, Limit: 3,
		Q: "ann", OrderBy: []string{"id desc"},
	}

	mapped := MapPage(page, func(n int) string { return string(rune('a' + n - 1)) })

	assert.Equal(t, []string{"a", "b", "c"}, mapped.Items)
	assert.Equal(t, int64(42), mapped.Total, "mapping a page does not change what was counted")
	assert.Equal(t, int64(3), mapped.Count)
	assert.Equal(t, int64(2), mapped.Page)
	assert.Equal(t, int64(3), mapped.Limit)
	assert.Equal(t, "ann", mapped.Q)
	assert.Equal(t, []string{"id desc"}, mapped.OrderBy)
}

func TestMapPage_onAnEmptyPage(t *testing.T) {
	mapped := MapPage(&Page[int]{Items: nil, Total: 0}, func(n int) string { return "" })

	require.NotNil(t, mapped)
	assert.NotNil(t, mapped.Items, "an empty page serialises as [] rather than null")
	assert.Empty(t, mapped.Items)

	assert.Nil(t, MapPage[int, string](nil, nil), "nothing to map")
}
