package core

import (
	"bytes"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pretty(color bool) (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(newPrettyHandler(&buf, slog.LevelDebug, color, false)), &buf
}

func TestPretty_plainOutputHasNoEscapeCodes(t *testing.T) {
	log, buf := pretty(false)
	log.Info("request", "method", "GET", "status", 200)

	out := buf.String()
	assert.NotContains(t, out, "\x1b[", "a non-terminal must never receive escape codes")
	assert.Contains(t, out, "INFO")
	assert.Contains(t, out, "request")
	assert.Contains(t, out, "method=GET")
	assert.Contains(t, out, "status=200")
}

func TestPretty_endsTheLineWithTheCallSite(t *testing.T) {
	env := mustEnv(t, map[string]string{"LOG_SIMPLE": "true"})
	var buf bytes.Buffer
	log := NewLoggerTo(&buf, env)

	_, file, line, _ := runtime.Caller(0)
	log.Info("request", "method", "GET")

	out := strings.TrimRight(buf.String(), "\n")
	assert.True(t, strings.HasSuffix(out, sourceOfLine(file, line, 1)),
		"the call site closes the line, after the attributes: %q", out)
}

func TestPretty_colourWrapsLevelAndMessage(t *testing.T) {
	log, buf := pretty(true)
	log.Error("boom", "err", "db down")

	out := buf.String()
	assert.Contains(t, out, "\x1b[", "colour was requested")
	assert.Contains(t, out, ansiRed, "errors are red")
	assert.True(t, strings.HasSuffix(out, ansiReset+"\n") || strings.Contains(out, ansiReset),
		"every colour run must be reset, or it bleeds into the next line")

	// the content is still all there under the escapes
	assert.Contains(t, stripANSI(out), "boom")
	assert.Contains(t, stripANSI(out), "err=")
}

func TestPretty_levelsAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		level slog.Level
		label string
	}{
		{slog.LevelDebug, "DEBUG"},
		{slog.LevelInfo, "INFO"},
		{slog.LevelWarn, "WARN"},
		{slog.LevelError, "ERROR"},
	} {
		log, buf := pretty(false)
		log.Log(t.Context(), tc.level, "msg")
		assert.Contains(t, buf.String(), tc.label)
	}
}

func TestPretty_respectsLevel(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(newPrettyHandler(&buf, slog.LevelWarn, false, false))

	log.Debug("hidden")
	log.Info("hidden too")
	log.Warn("shown")

	out := buf.String()
	assert.NotContains(t, out, "hidden")
	assert.Contains(t, out, "shown")
}

func TestPretty_withAttrsAndGroups(t *testing.T) {
	log, buf := pretty(false)
	log.With("request_id", "req-1").WithGroup("db").Info("query", "rows", 3)

	out := buf.String()
	assert.Contains(t, out, "request_id=req-1", "pinned attrs survive")
	assert.Contains(t, out, "db.rows=3", "groups flatten into dotted keys")
}

// Values that would break a key=value scan get quoted, as slog's own text
// handler does.
func TestPretty_quotesAwkwardValues(t *testing.T) {
	log, buf := pretty(false)
	log.Info("msg", "note", "two words", "empty", "")

	out := buf.String()
	assert.Contains(t, out, `note="two words"`)
	assert.Contains(t, out, `empty=""`)
}

// A quoted SQL statement is mostly backslashes once identifiers are quoted:
// `\"users\".\"deleted_at\"`. Payload keys are rendered verbatim so the
// statement stays legible.
func TestPretty_sqlIsNotEscaped(t *testing.T) {
	log, buf := pretty(false)
	stmt := `SELECT * FROM "users" WHERE "users"."deleted_at" IS NULL`
	log.Debug("sql", "sql", stmt, "rows", 1)

	out := buf.String()
	assert.Contains(t, out, stmt, "the statement must appear as written")
	assert.NotContains(t, out, `\"`, "no escaped quotes")
}

// The record's own fields come first so the payload starts at a predictable
// column; pinned context ids trail at the end of the line.
func TestPretty_recordAttrsBeforePinnedContext(t *testing.T) {
	log, buf := pretty(false)
	log.With("request_id", "req-1").Debug("sql", "sql", "SELECT 1")

	out := buf.String()
	assert.Less(t, strings.Index(out, "sql=SELECT 1"), strings.Index(out, "request_id="),
		"the statement should precede the correlation id")
}

func TestWantColor(t *testing.T) {
	t.Run("a buffer is not a terminal", func(t *testing.T) {
		assert.False(t, wantColor(&bytes.Buffer{}, nil))
	})

	t.Run("NO_COLOR wins over everything", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		env := mustEnv(t, map[string]string{"ENV": "dev", "LOG_COLOR": "true"})
		assert.False(t, wantColor(os.Stdout, env))
	})

	t.Run("LOG_COLOR forces it on for a non-terminal", func(t *testing.T) {
		os.Unsetenv("NO_COLOR")
		env := mustEnv(t, map[string]string{"ENV": "dev", "LOG_COLOR": "true"})
		assert.True(t, wantColor(&bytes.Buffer{}, env))
	})

	t.Run("LOG_COLOR forces it off", func(t *testing.T) {
		os.Unsetenv("NO_COLOR")
		env := mustEnv(t, map[string]string{"ENV": "dev", "LOG_COLOR": "false"})
		assert.False(t, wantColor(os.Stdout, env))
	})
}

// JSON output must stay machine-readable: escape codes would break every parser
// downstream.
func TestLogger_jsonIsNeverColoured(t *testing.T) {
	t.Setenv("LOG_COLOR", "true")
	env := mustEnv(t, map[string]string{"ENV": "dev", "LOG_SIMPLE": "false"})

	var buf bytes.Buffer
	NewLoggerTo(&buf, env).Info("hello", "k", "v")

	out := buf.String()
	require.Contains(t, out, "{", "expected JSON")
	assert.NotContains(t, out, "\x1b[")
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // the 'm'
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// A payload logged whole has to keep its field names. Go renders a struct as
// `{u-1 a@b.co}`, which says nothing about which value is which.
func TestPretty_structsRenderAsJSON(t *testing.T) {
	type user struct {
		ID   string   `json:"id"`
		Tags []string `json:"tags"`
	}

	var out strings.Builder
	log := &logger{l: slog.New(newPrettyHandler(&out, slog.LevelInfo, false, false))}
	log.Info("user loaded", "user", user{ID: "u-1", Tags: []string{"admin"}})

	assert.Contains(t, out.String(), `user={"id":"u-1","tags":["admin"]}`)
}

// An error knows how it reads; JSON would throw that away.
func TestPretty_valuesThatRenderThemselvesAreLeftAlone(t *testing.T) {
	var out strings.Builder
	log := &logger{l: slog.New(newPrettyHandler(&out, slog.LevelInfo, false, false))}
	log.Error("failed", "err", New(500, "DB_DOWN", "db down"))

	assert.Contains(t, out.String(), "DB_DOWN")
	assert.NotContains(t, out.String(), `{"`)
}
