package core

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type csvFloatRow struct {
	Ratio float32 `csv:"ratio"`
	Total float64 `csv:"total"`
}

// A float32 widened to float64 prints the noise of the conversion
// (0.1 → 0.10000000149011612); it has to be formatted at its own precision.
func TestCSV_float32PrintsAtItsOwnPrecision(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, CSVMarshal(&buf, []csvFloatRow{{Ratio: 0.1, Total: 0.1}}))
	assert.Contains(t, buf.String(), "0.1,0.1")
	assert.NotContains(t, buf.String(), "0.10000000149011612")
}
