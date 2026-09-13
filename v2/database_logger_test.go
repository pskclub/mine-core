package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type capturedLine struct {
	level string
	msg   string
	args  []any
}

// captureLogger records what the gorm bridge emits.
type captureLogger struct{ lines *[]capturedLine }

func (c captureLogger) Debug(msg string, args ...any) { c.add("debug", msg, args) }
func (c captureLogger) Info(msg string, args ...any)  { c.add("info", msg, args) }
func (c captureLogger) Warn(msg string, args ...any)  { c.add("warn", msg, args) }
func (c captureLogger) Error(msg string, args ...any) { c.add("error", msg, args) }
func (c captureLogger) With(...any) ILogger           { return c }

func (c captureLogger) Slog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (c captureLogger) add(level, msg string, args []any) {
	*c.lines = append(*c.lines, capturedLine{level: level, msg: msg, args: args})
}

func (c captureLogger) sql() []capturedLine {
	out := make([]capturedLine, 0)
	for _, l := range *c.lines {
		if strings.Contains(l.msg, "query") {
			out = append(out, l)
		}
	}
	return out
}

func newCapture() (captureLogger, *[]capturedLine) {
	lines := make([]capturedLine, 0)
	return captureLogger{lines: &lines}, &lines
}

// LOG_LEVEL is the knob everyone reaches for; it must reach gorm too.
func TestGormLogLevel_followsLogLevel(t *testing.T) {
	for _, tc := range []struct {
		logLevel string
		want     gormlogger.LogLevel
	}{
		{"debug", gormlogger.Info}, // every statement
		{"info", gormlogger.Warn},
		{"warn", gormlogger.Warn},
		{"error", gormlogger.Error},
		{"", gormlogger.Warn},
	} {
		t.Run("LOG_LEVEL="+tc.logLevel, func(t *testing.T) {
			env := mustEnv(t, map[string]string{"ENV": "test", "LOG_LEVEL": tc.logLevel})
			assert.Equal(t, tc.want, gormLogLevel(env))
		})
	}

	assert.Equal(t, gormlogger.Warn, gormLogLevel(nil), "a nil env must not panic")
}

func TestGormLogger_logsEveryStatementAtInfo(t *testing.T) {
	rec, _ := newCapture()
	gl := NewGormLogger(rec, gormlogger.Info, 0)

	gl.Trace(context.Background(), time.Now(),
		func() (string, int64) { return "SELECT * FROM users", 3 }, nil)

	require.Len(t, rec.sql(), 1)
	assert.Equal(t, "debug", rec.sql()[0].level, "ordinary SQL is debug-level detail")
	assert.Contains(t, rec.sql()[0].args, "SELECT * FROM users")
}

func TestGormLogger_quietAtWarn(t *testing.T) {
	rec, _ := newCapture()
	gl := NewGormLogger(rec, gormlogger.Warn, 0)

	gl.Trace(context.Background(), time.Now(),
		func() (string, int64) { return "SELECT * FROM users", 3 }, nil)

	assert.Empty(t, rec.sql(), "a fast, successful query is not news at warn level")
}

func TestGormLogger_reportsSlowQuery(t *testing.T) {
	rec, _ := newCapture()
	gl := NewGormLogger(rec, gormlogger.Warn, 10*time.Millisecond)

	gl.Trace(context.Background(), time.Now().Add(-50*time.Millisecond),
		func() (string, int64) { return "SELECT pg_sleep(1)", 1 }, nil)

	require.Len(t, rec.sql(), 1)
	assert.Equal(t, "warn", rec.sql()[0].level)
	assert.Equal(t, "slow query", rec.sql()[0].msg)
}

func TestGormLogger_reportsFailure(t *testing.T) {
	rec, _ := newCapture()
	gl := NewGormLogger(rec, gormlogger.Warn, 0)

	cause := errors.New("syntax error")
	gl.Trace(context.Background(), time.Now(),
		func() (string, int64) { return "SELECT bad", 0 }, cause)

	require.Len(t, rec.sql(), 1)
	assert.Equal(t, "error", rec.sql()[0].level)
	assert.Contains(t, rec.sql()[0].args, "SELECT bad", "the statement that failed")
	assert.Contains(t, rec.sql()[0].args, cause, "and why it failed")
}

// "No rows" is an answer, not a failure — logging it as an error would bury the
// real ones.
func TestGormLogger_recordNotFoundIsNotAnError(t *testing.T) {
	rec, _ := newCapture()
	gl := NewGormLogger(rec, gormlogger.Warn, 0)

	gl.Trace(context.Background(), time.Now(),
		func() (string, int64) { return "SELECT * FROM users WHERE id = 1", 0 }, gorm.ErrRecordNotFound)

	assert.Empty(t, rec.sql())
}

func TestGormLogger_silentLogsNothing(t *testing.T) {
	rec, lines := newCapture()
	gl := NewGormLogger(rec, gormlogger.Silent, 0)

	gl.Trace(context.Background(), time.Now().Add(-time.Hour),
		func() (string, int64) { return "SELECT 1", 1 }, errors.New("boom"))

	assert.Empty(t, *lines)
}

// A statement written across several lines in Go must still log as one line.
func TestGormLogger_collapsesMultilineSQL(t *testing.T) {
	rec, _ := newCapture()
	gl := NewGormLogger(rec, gormlogger.Info, 0)

	gl.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT *\n  FROM users\n  WHERE id = 1", 1
	}, nil)

	require.Len(t, rec.sql(), 1)
	assert.Contains(t, rec.sql()[0].args, "SELECT * FROM users WHERE id = 1")
}

// SQL logging is its own question: turning the application up to debug to read
// one flow should not bury it under every SELECT, and DB_LOG_LEVEL=silent has
// to stop even the slow-query and failure lines.
func TestGormLogLevel_fromEnv(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		expected gormlogger.LogLevel
	}{
		{"defaults to slow queries and failures", nil, gormlogger.Warn},
		{"follows LOG_LEVEL=debug", map[string]string{"LOG_LEVEL": "debug"}, gormlogger.Info},
		{"DB_LOG_LEVEL wins over LOG_LEVEL",
			map[string]string{"LOG_LEVEL": "debug", "DB_LOG_LEVEL": "warn"}, gormlogger.Warn},
		{"silent", map[string]string{"DB_LOG_LEVEL": "silent"}, gormlogger.Silent},
		{"off is silent too", map[string]string{"DB_LOG_LEVEL": "off"}, gormlogger.Silent},
		{"false is silent too", map[string]string{"DB_LOG_LEVEL": "false"}, gormlogger.Silent},
		{"info without touching LOG_LEVEL",
			map[string]string{"DB_LOG_LEVEL": "info"}, gormlogger.Info},
		{"a value nobody understands falls back to LOG_LEVEL",
			map[string]string{"DB_LOG_LEVEL": "loud"}, gormlogger.Warn},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, gormLogLevel(mustEnv(t, tc.env)))
		})
	}
}

// GORM's logger interface is Printf-shaped, so the format string must be
// rendered rather than passed through. It used to reach the log verbatim:
//
//	ERROR failed to initialize database, got error %v args="[dial tcp ... refused]"
//
// which is the first line anybody sees when a service will not boot.
func TestGormLogger_formatsPrintfStyleMessages(t *testing.T) {
	var lines []capturedLine
	g := NewGormLogger(captureLogger{lines: &lines}, gormlogger.Info, 0)

	cause := errors.New("dial tcp 127.0.0.1:5432: connection refused")
	g.Error(context.Background(), "failed to initialize database, got error %v", cause)
	g.Warn(context.Background(), "slow: %s took %dms", "SELECT 1", 250)
	g.Info(context.Background(), "replaying %d migrations", 3)

	require.Len(t, lines, 3)

	assert.Equal(t, "failed to initialize database, got error "+cause.Error(), lines[0].msg)
	assert.NotContains(t, lines[0].msg, "%v", "the verb must be substituted, not printed")
	assert.Empty(t, lines[0].args, "the error belongs in the message, not in an args array")

	assert.Equal(t, "slow: SELECT 1 took 250ms", lines[1].msg)
	assert.Equal(t, "replaying 3 migrations", lines[2].msg)
}

// A message with no arguments is used as it is — running it through Sprintf
// would corrupt any "%" it happens to contain.
func TestGormLogger_leavesAnUnformattedMessageAlone(t *testing.T) {
	var lines []capturedLine
	g := NewGormLogger(captureLogger{lines: &lines}, gormlogger.Info, 0)

	g.Info(context.Background(), "cache hit rate is 95% today")

	require.Len(t, lines, 1)
	assert.Equal(t, "cache hit rate is 95% today", lines[0].msg)
}
