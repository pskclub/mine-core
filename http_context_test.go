package core

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestGetPageOptionsFieldWithBlankString(t *testing.T) {
	c := &HTTPContext{}
	orderBy := c.genOrderBy("")
	assert.Equal(t, 0, len(orderBy))
}

func TestGetPageOptionsFieldWithoutSortingParameter(t *testing.T) {
	c := &HTTPContext{}
	orderBy := c.genOrderBy("xxx")
	assert.Equal(t, "xxx desc", orderBy[0])
}

func TestGetPageOptionsWithSpaceParameter(t *testing.T) {
	c := &HTTPContext{}
	orderBy := c.genOrderBy("xxx desc")
	assert.Equal(t, "xxx desc", orderBy[0])

	orderBy = c.genOrderBy("xxx asc")
	assert.Equal(t, "xxx asc", orderBy[0])

	orderBy = c.genOrderBy("xxx abc")
	assert.Equal(t, "xxx desc", orderBy[0])
}

func TestGetPageOptionsWithBracketParameter(t *testing.T) {
	c := &HTTPContext{}
	orderBy := c.genOrderBy("desc(xxx)")
	assert.Equal(t, "xxx desc", orderBy[0])

	orderBy = c.genOrderBy("asc(xxx)")
	assert.Equal(t, "xxx asc", orderBy[0])

	orderBy = c.genOrderBy("abc(xxx)")
	assert.Equal(t, "xxx desc", orderBy[0])
}

func TestGetPageOptionsWithMixingParameter(t *testing.T) {
	c := &HTTPContext{}

	orderBy := c.genOrderBy("desc(xxx),yyy asc")
	assert.Equal(t, 2, len(orderBy))
	assert.Equal(t, "xxx desc", orderBy[0])
	assert.Equal(t, "yyy asc", orderBy[1])
}

func TestGetPageOptionsWithMalformParameter(t *testing.T) {
	c := &HTTPContext{}
	orderBy := c.genOrderBy("two_spaced  desc,desc((two_pairs))")
	assert.Equal(t, 0, len(orderBy))
}
