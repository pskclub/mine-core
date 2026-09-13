package core

import (
	"errors"
	"time"

	"github.com/getsentry/sentry-go"
	"gorm.io/gorm"
)

// gormStartKey is where the instrumentation stashes a statement's start time,
// gormSpanKey the span it opened.
const (
	gormStartKey = "sentry:started_at"
	gormSpanKey  = "sentry:span"
)

// SentryGormOptions tunes the database instrumentation.
type SentryGormOptions struct {
	// SlowQuery is the threshold above which a query is recorded at warning
	// level. 0 = 200ms.
	SlowQuery time.Duration
	// AllQueries records every query, not only the slow and the failing ones.
	// Off by default: a busy request would spend its whole breadcrumb budget on
	// SELECTs and push out the crumbs that explain the failure.
	AllQueries bool
	// ExcludeSQL leaves the statement out of the breadcrumb, keeping only the
	// table, row count and timing. Bind variables are never interpolated, so the
	// statement is safe by default — this is for teams whose SQL is itself
	// sensitive.
	ExcludeSQL bool
}

func (o SentryGormOptions) slowQuery() time.Duration {
	if o.SlowQuery <= 0 {
		return 200 * time.Millisecond
	}
	return o.SlowQuery
}

// InstrumentGorm makes every query leave a Sentry breadcrumb, so an issue shows
// the statements that ran before the failure — usually the one that caused it.
//
// It is opt-in because it touches every query:
//
//	db, _ := core.NewDatabase(env)
//	_ = core.InstrumentGorm(db)
//	app, _ := core.NewApp(env, core.WithSQL("default", db))
//
// A query whose context carries no hub (a background task outside any request
// or run) records nothing, so the cost is one map lookup.
func InstrumentGorm(db *gorm.DB, opts ...SentryGormOptions) IError {
	if db == nil {
		return New(500, "INVALID_ARGUMENT", "gorm: db is nil")
	}
	var o SentryGormOptions
	if len(opts) > 0 {
		o = opts[0]
	}

	before := func(op string) func(*gorm.DB) {
		return func(tx *gorm.DB) {
			tx.InstanceSet(gormStartKey, time.Now())
			startQuerySpan(tx, op)
		}
	}
	after := func(op string) func(*gorm.DB) {
		return func(tx *gorm.DB) {
			finishQuerySpan(tx, op, o)
			recordQuery(tx, op, elapsedOf(tx), o)
		}
	}

	// gorm has no "every callback" hook and its processor type is unexported, so
	// each operation is wired explicitly.
	for _, op := range []string{"query", "create", "update", "delete", "raw"} {
		anchor := "gorm:" + op
		var err error
		switch op {
		case "query":
			p := db.Callback().Query()
			if err = p.Before(anchor).Register("sentry:query:before", before(op)); err == nil {
				err = p.After(anchor).Register("sentry:query:after", after(op))
			}
		case "create":
			p := db.Callback().Create()
			if err = p.Before(anchor).Register("sentry:create:before", before(op)); err == nil {
				err = p.After(anchor).Register("sentry:create:after", after(op))
			}
		case "update":
			p := db.Callback().Update()
			if err = p.Before(anchor).Register("sentry:update:before", before(op)); err == nil {
				err = p.After(anchor).Register("sentry:update:after", after(op))
			}
		case "delete":
			p := db.Callback().Delete()
			if err = p.Before(anchor).Register("sentry:delete:before", before(op)); err == nil {
				err = p.After(anchor).Register("sentry:delete:after", after(op))
			}
		case "raw":
			p := db.Callback().Raw()
			if err = p.Before(anchor).Register("sentry:raw:before", before(op)); err == nil {
				err = p.After(anchor).Register("sentry:raw:after", after(op))
			}
		}
		if err != nil {
			return Wrapf(err, "gorm: instrument %s", op)
		}
	}
	return nil
}

func elapsedOf(tx *gorm.DB) time.Duration {
	started, ok := tx.InstanceGet(gormStartKey)
	if !ok {
		return 0
	}
	t, ok := started.(time.Time)
	if !ok {
		return 0
	}
	return time.Since(t)
}

// startQuerySpan opens the span a query occupies in the trace of the request or
// run that issued it. Without tracing there is no parent span and nothing is
// started, so the cost is one context lookup.
//
// A breadcrumb says a query happened; a span says how long the request spent
// waiting for it. A trace without them shows the time going somewhere and
// never says where.
func startQuerySpan(tx *gorm.DB, op string) {
	if tx.Statement == nil || tx.Statement.Context == nil {
		return
	}
	parent := sentry.SpanFromContext(tx.Statement.Context)
	if parent == nil {
		return
	}
	if child := parent.StartChild("db." + op); child != nil {
		tx.InstanceSet(gormSpanKey, child)
	}
}

// finishQuerySpan closes it and describes what actually ran — gorm only builds
// the statement while the operation executes, so the description cannot be set
// when the span opens.
func finishQuerySpan(tx *gorm.DB, op string, o SentryGormOptions) {
	v, ok := tx.InstanceGet(gormSpanKey)
	if !ok {
		return
	}
	span, ok := v.(*sentry.Span)
	if !ok || span == nil {
		return
	}
	span.Description = op
	if !o.ExcludeSQL {
		if sql := tx.Statement.SQL.String(); sql != "" {
			span.Description = sql
		}
	}
	if table := tx.Statement.Table; table != "" {
		span.SetData("db.table", table)
	}
	span.SetData("db.rows_affected", tx.Statement.RowsAffected)
	// "no rows" is an answer, not a failure — same rule the breadcrumb uses
	if tx.Error != nil && !errors.Is(tx.Error, gorm.ErrRecordNotFound) {
		span.Status = sentry.SpanStatusInternalError
		span.SetData("db.error", tx.Error.Error())
	} else {
		span.Status = sentry.SpanStatusOK
	}
	span.Finish()
}

// recordQuery leaves the breadcrumb for one statement.
func recordQuery(tx *gorm.DB, op string, elapsed time.Duration, o SentryGormOptions) {
	ctx := tx.Statement.Context
	if ctx == nil || sentry.GetHubFromContext(ctx) == nil {
		return
	}
	// "no rows" is an answer, not a failure — the caller decides what it means
	failed := tx.Error != nil && !errors.Is(tx.Error, gorm.ErrRecordNotFound)
	slow := elapsed >= o.slowQuery()
	if !failed && !slow && !o.AllQueries {
		return
	}

	level := LevelInfo
	switch {
	case failed:
		level = LevelError
	case slow:
		level = LevelWarning
	}
	data := map[string]any{
		"table":       tx.Statement.Table,
		"rows":        tx.Statement.RowsAffected,
		"duration_ms": elapsed.Milliseconds(),
	}
	if failed {
		data["error"] = tx.Error.Error()
	}
	message := op
	if !o.ExcludeSQL {
		if sql := tx.Statement.SQL.String(); sql != "" {
			message = sql
		}
	}
	breadcrumbTo(ctx, Breadcrumb{
		Type:     "query",
		Category: "db." + op,
		Message:  message,
		Level:    level,
		Data:     data,
	})
}
