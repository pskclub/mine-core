package utils_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pskclub/mine-core/v2/utils"
)

// enum types like a consuming service would declare
type ProjectType string
type Priority int

// mirrors the user's real enum
type PMOProjectStatus string

const (
	PMOProjectStatusIntro PMOProjectStatus = "INTRO"
	PMOProjectStatusDraft PMOProjectStatus = "DRAFT"
)

// a distinct custom string type on the input/DTO side
type PMOProjectStatusInput string

func TestPtrConvert_customTypeToCustomType(t *testing.T) {
	// source is a *custom* string type (not plain *string), like a GraphQL input enum
	in := utils.ToPointer(PMOProjectStatusInput("DRAFT"))

	got := utils.PtrConvert[PMOProjectStatus](in) // *PMOProjectStatusInput → *PMOProjectStatus
	require.NotNil(t, got)
	assert.Equal(t, PMOProjectStatusDraft, *got)

	// plain *string source works too
	fromString := utils.PtrConvert[PMOProjectStatus](utils.ToPointer("INTRO"))
	assert.Equal(t, PMOProjectStatusIntro, *fromString)

	// nil-safe
	assert.Nil(t, utils.PtrConvert[PMOProjectStatus]((*string)(nil)))
}

func TestConvert_value(t *testing.T) {
	assert.Equal(t, PMOProjectStatusDraft, utils.Convert[PMOProjectStatus]("DRAFT"))
	assert.Equal(t, Priority(3), utils.ConvertNum[Priority](3))
}

func TestToPointerAndBack(t *testing.T) {
	p := utils.ToPointer("hello")
	require.NotNil(t, p)
	assert.Equal(t, "hello", *p)
	assert.Equal(t, "hello", utils.ToNonPointer(p))

	assert.Equal(t, "", utils.ToNonPointer[string](nil), "nil → zero value")
	assert.Equal(t, "def", utils.ToNonPointerOr(nil, "def"))
	assert.Equal(t, "x", utils.ToNonPointerOr(utils.ToPointer("x"), "def"))
}

func TestPtrConvert_stringEnum(t *testing.T) {
	in := utils.ToPointer("internal") // *string, e.g. from a request DTO

	got := utils.PtrConvert[ProjectType](in) // *ProjectType
	require.NotNil(t, got)
	assert.Equal(t, ProjectType("internal"), *got)

	// nil-safe
	assert.Nil(t, utils.PtrConvert[ProjectType]((*string)(nil)))

	// reverse direction (enum → *string), for output DTOs
	back := utils.PtrConvert[string](got)
	require.NotNil(t, back)
	assert.Equal(t, "internal", *back)
}

func TestPtrConvertNum_intEnum(t *testing.T) {
	in := utils.ToPointer(2)
	got := utils.PtrConvertNum[Priority](in)
	require.NotNil(t, got)
	assert.Equal(t, Priority(2), *got)
	assert.Nil(t, utils.PtrConvertNum[Priority]((*int)(nil)))
}

func TestMapPtr(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	got := utils.MapPtr(&now, func(t time.Time) string { return t.Format(time.RFC3339) })
	require.NotNil(t, got)
	assert.Equal(t, "2026-07-15T10:00:00Z", *got)
	assert.Nil(t, utils.MapPtr[time.Time, string](nil, func(t time.Time) string { return "" }))
}
