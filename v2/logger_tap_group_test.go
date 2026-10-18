package core

import (
	"io"
	"log/slog"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The JSON handler nests attributes logged under WithGroup; a tap flattens them
// to "group.key". A tap that dropped the group would report "status" for what
// the written line calls "http.status", and a reader comparing the two would
// have to guess which status that was.
//
// ILogger has no WithGroup of its own, so this drives the slog handler the way
// a library that was handed the *slog.Logger would.
func TestWithLogTap_prefixesGroupedAttributes(t *testing.T) {
	tap := &collectTaps{}
	h := &tapHandler{inner: slog.NewJSONHandler(io.Discard, nil), taps: []LogTapFunc{tap.tap}}

	slog.New(h).WithGroup("http").With("method", "GET").Info("served", "status", 200)

	entry, ok := tap.find("served")
	require.True(t, ok)
	assert.EqualValues(t, 200, entry.Attrs["http.status"], "the record's own attribute sits in the group")
	assert.Equal(t, "GET", entry.Attrs["http.method"], "so does one pinned after the group was opened")
	assert.NotContains(t, entry.Attrs, "status", "nothing escapes the group unprefixed")
}

// Byte-indexed truncation splits a multi-byte character; Thai text makes this
// the common case rather than a corner.
func TestCutUTF8_neverSplitsARune(t *testing.T) {
	thai := "สวัสดีครับ" // every rune is three bytes
	for n := 0; n <= len(thai); n++ {
		cut := cutUTF8(thai, n)
		assert.LessOrEqual(t, len(cut), n)
		assert.True(t, utf8.ValidString(cut), "cut at %d produced a broken character", n)
	}
	assert.Equal(t, "abc", cutUTF8("abc", 10), "a string within the limit is returned whole")
	assert.Equal(t, "", cutUTF8("abc", 0))
}
