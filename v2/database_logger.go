package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// defaultSlowQuery is the threshold above which a statement is logged as slow
// even when SQL logging is otherwise off.
const defaultSlowQuery = 200 * time.Millisecond

// gormLogLevel decides how much SQL is logged: DB_LOG_LEVEL when it is set,
// otherwise it follows the app's LOG_LEVEL — debug means every statement,
// anything else keeps the log to slow queries and failures.
//
// The separate key exists because the two questions are separate. Turning the
// application up to debug to read one flow should not have to bury it under
// every SELECT the ORM makes, and a service that is happy at info may still
// want the statements while it is being written.
func gormLogLevel(env IENV) gormlogger.LogLevel {
	if env == nil {
		return gormlogger.Warn
	}
	if level, ok := parseGormLogLevel(env.Config().DBLogLevel); ok {
		return level
	}
	switch parseLevel(env.Config().LogLevel) {
	case slog.LevelDebug:
		return gormlogger.Info
	case slog.LevelError:
		return gormlogger.Error
	default:
		return gormlogger.Warn
	}
}

// parseGormLogLevel reads DB_LOG_LEVEL. "off"/"false" are accepted alongside
// "silent" because the setting is reached for as a switch, and being told the
// value is invalid — silently, by having it ignored — is not a useful answer.
func parseGormLogLevel(s string) (gormlogger.LogLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "silent", "off", "none", "false":
		return gormlogger.Silent, true
	case "error":
		return gormlogger.Error, true
	case "warn", "warning":
		return gormlogger.Warn, true
	case "info", "debug", "all", "true":
		return gormlogger.Info, true
	default:
		return 0, false
	}
}

// gormLogger adapts gorm's logger to ILogger, so SQL joins the one log stream
// instead of gorm's own writer: same JSON/text format, same level, and — because
// gorm hands each statement its context — the same request_id and trace as the
// request that issued it.
type gormLogger struct {
	log       ILogger
	level     gormlogger.LogLevel
	slowQuery time.Duration
}

var _ gormlogger.Interface = (*gormLogger)(nil)

// NewGormLogger builds the gorm logger used by NewDatabase. Pass it to
// gorm.Open directly when opening a connection by hand.
func NewGormLogger(log ILogger, level gormlogger.LogLevel, slowQuery time.Duration) gormlogger.Interface {
	if slowQuery <= 0 {
		slowQuery = defaultSlowQuery
	}
	// blameApp: the statement was issued by a repository call in the service, and
	// "database_logger.go:155" is the same answer for every query ever logged
	return &gormLogger{log: blameApp(log), level: level, slowQuery: slowQuery}
}

func (g *gormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	cp := *g
	cp.level = level
	return &cp
}

func (g *gormLogger) Info(ctx context.Context, msg string, args ...any) {
	if g.level >= gormlogger.Info {
		g.at(ctx).Info(gormMessage(msg, args))
	}
}

func (g *gormLogger) Warn(ctx context.Context, msg string, args ...any) {
	if g.level >= gormlogger.Warn {
		g.at(ctx).Warn(gormMessage(msg, args))
	}
}

func (g *gormLogger) Error(ctx context.Context, msg string, args ...any) {
	if g.level >= gormlogger.Error {
		g.at(ctx).Error(gormMessage(msg, args))
	}
}

// gormMessage renders one of GORM's log calls into a line somebody can read.
//
// GORM's logger interface is Printf-shaped — Error(ctx, "…got error %v", err) —
// so passing the format string through as a structured message prints the verb
// literally and buries the actual failure in an args array:
//
//	ERROR failed to initialize database, got error %v args="[dial tcp ... refused]"
//
// That is the first line anybody sees when a service will not boot, and it
// should say what went wrong rather than describe the shape of a message that
// was never formatted.
//
// With no arguments the string is used as it is: GORM logs some plain messages
// too, and running those through Sprintf would corrupt any "%" they contain.
func gormMessage(msg string, args []any) string {
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// Trace is called once per statement. It reports failures, then slow queries,
// then — only at Info — every remaining statement.
func (g *gormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if g.level <= gormlogger.Silent {
		return
	}

	elapsed := time.Since(begin)
	sql, rows := fc()
	// the statement first: it is what the line is about, and putting it at a
	// fixed position lets the eye run down a column of queries
	fields := []any{
		"sql", collapseSpace(sql),
		"rows", rows,
		"duration_ms", elapsed.Milliseconds(),
	}

	switch {
	// "no rows" is an answer, not a failure — the caller decides what it means,
	// so it must not show up as an error in the log.
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound) && g.level >= gormlogger.Error:
		g.at(ctx).Error("query failed", append(fields, "err", err)...)
	case elapsed >= g.slowQuery && g.level >= gormlogger.Warn:
		g.at(ctx).Warn("slow query", append(fields, "threshold_ms", g.slowQuery.Milliseconds())...)
	case g.level >= gormlogger.Info:
		// "query", not "sql": the sql= attribute follows immediately, and
		// "sql sql=SELECT …" makes the reader's eye skip the stutter every line
		g.at(ctx).Debug("query", fields...)
	}
}

// collapseSpace folds newlines and runs of whitespace into single spaces, so a
// statement written across several lines in Go still logs as one line and does
// not break up the log it lands in.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// at binds the statement's context so the line carries the request it belongs
// to. A background query (no request) simply logs without those attributes.
func (g *gormLogger) at(ctx context.Context) ILogger {
	if l, ok := g.log.(*logger); ok && ctx != nil {
		return l.withContext(ctx)
	}
	return g.log
}
