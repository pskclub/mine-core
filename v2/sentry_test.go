package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newSentryApp builds an App whose tracker records events in memory, so every
// assertion below runs the real capture path (scope, scrubbing, fingerprints)
// and only the network is replaced.
func newSentryApp(t *testing.T, env map[string]string, opts ...SentryOptions) (*App, *SentryRecorder) {
	t.Helper()
	base := map[string]string{"ENV": "test", "SERVICE": "sentry-test"}
	for k, v := range env {
		base[k] = v
	}
	ienv := mustEnv(t, base)
	tracker, rec, err := NewRecordingSentry(ienv, opts...)
	require.NoError(t, err)
	app, aerr := NewApp(ienv, WithSentry(tracker))
	require.NoError(t, aerr)
	return app, rec
}

func TestSentry_disabledWithoutDSN(t *testing.T) {
	env := mustEnv(t, map[string]string{"ENV": "test"})
	tracker, err := NewSentry(env)
	require.NoError(t, err)

	assert.False(t, tracker.Enabled())
	// every call is safe on the no-op tracker
	assert.Empty(t, tracker.CaptureError(errors.New("boom")))
	tracker.Breadcrumb(Breadcrumb{Message: "nothing"})
	span := tracker.StartTransaction("t", "op")
	span.Finish(nil)
	assert.NoError(t, tracker.Close())
}

func TestSentry_captureErrorCarriesCodeAndScope(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	ctx := app.NewContext(context.Background(), ModeCron)
	ctx.SetUser(&ContextUser{ID: "u-1", Email: "a@b.co"})
	ctx.SetData("order_id", "o-9")

	id := ctx.Sentry().CaptureError(New(http.StatusInternalServerError, "PAYMENT_FAILED", "gateway said no"))
	require.NotEmpty(t, id)
	require.Equal(t, 1, rec.Len())

	e := rec.Last()
	assert.Equal(t, LevelError, e.Level)
	assert.Equal(t, "cron", e.Tags["mode"])
	assert.Equal(t, "sentry-test", e.Tags["service"])
	assert.Equal(t, "u-1", e.User.ID)
	assert.Equal(t, "o-9", e.Contexts["context-data"]["order_id"])
	// the issue is titled with the code, not with "*core.Error"
	require.NotEmpty(t, e.Exception)
	last := e.Exception[len(e.Exception)-1]
	assert.Equal(t, "PAYMENT_FAILED", last.Type)
	assert.Equal(t, "gateway said no", last.Value)
	// grouping stays stack-based but splits per code
	assert.Equal(t, []string{"{{ default }}", "PAYMENT_FAILED"}, e.Fingerprint)
}

func TestSentry_capturesOwnStackNotTheCapturePoint(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	ctx := app.NewContext(context.Background())

	err := deepError()
	ctx.Sentry().CaptureError(err)

	require.Equal(t, 1, rec.Len())
	frames := rec.Last().Exception[0].Stacktrace
	require.NotNil(t, frames)
	var found bool
	for _, f := range frames.Frames {
		if strings.Contains(f.Function, "deepError") {
			found = true
		}
	}
	assert.True(t, found, "stack should start where the error was built")
}

func deepError() error { return New(500, "DEEP", "made further down") }

func TestSentry_sentinelErrorReportsTheCallSite(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	ctx := app.NewContext(context.Background())

	// the shape that made every service report the same issue: one package-level
	// sentinel, whose init() stack used to be cloned onto each error built from it
	ctx.NewError(errors.New("upstream said no"), testSentinel)

	require.Equal(t, 1, rec.Len())
	// the chain is reported cause-first, so the *core.Error built here is last
	exceptions := rec.Last().Exception
	require.NotEmpty(t, exceptions)
	stack := exceptions[len(exceptions)-1].Stacktrace
	require.NotNil(t, stack)
	require.NotEmpty(t, stack.Frames)
	// Sentry orders frames oldest first, so the culprit it displays is the last one
	culprit := stack.Frames[len(stack.Frames)-1]
	assert.Contains(t, culprit.Function, "TestSentry_sentinelErrorReportsTheCallSite",
		"the reported frame must be where NewError was called, not errmsgs.init")
}

func TestSentry_newErrorCapturesServerFailuresOnly(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	ctx := app.NewContext(context.Background())

	bad := ctx.NewError(errors.New("cause"), New(http.StatusBadRequest, "INVALID", "bad input"))
	assert.Equal(t, 0, rec.Len(), "client mistakes are not incidents")
	assert.Empty(t, From(bad).EventID())

	boom := ctx.NewError(errors.New("cause"), New(http.StatusInternalServerError, "DB_DOWN", "db down"))
	assert.Equal(t, 1, rec.Len())
	assert.NotEmpty(t, From(boom).EventID(), "the event id rides on the error")
	assert.Equal(t, "DB_DOWN", rec.Codes()[0])
}

func TestSentry_minStatusIsConfigurable(t *testing.T) {
	app, rec := newSentryApp(t, nil, SentryOptions{MinStatus: 400})
	ctx := app.NewContext(context.Background())

	ctx.NewError(errors.New("cause"), New(http.StatusBadRequest, "INVALID", "bad input"))
	assert.Equal(t, 1, rec.Len())
}

func TestSentry_scrubsSecretsFromConfigAndData(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{
		"DB_PASSWORD": "sup3r-secret-value",
		"JWT_SECRET":  "another-secret-value",
	})
	ctx := app.NewContext(context.Background())
	ctx.SetData("api_token", "tok-123")
	ctx.SetData("note", "connecting with sup3r-secret-value")

	ctx.Sentry().CaptureError(New(500, "X", "x"))
	e := rec.Last()

	assert.Equal(t, redacted, e.Contexts["config"]["db_password"])
	assert.Equal(t, redacted, e.Contexts["config"]["jwt_secret"])
	assert.Equal(t, redacted, e.Contexts["context-data"]["api_token"])
	// a secret pasted into a free-form value is masked wherever it appears
	assert.Equal(t, "connecting with "+redacted, e.Contexts["context-data"]["note"])
}

func TestSentry_scrubsRequestBodyAndHeaders(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	e := NewHTTPServer(app, nil)
	e.POST("/login", func(c IHTTPContext) error {
		var body map[string]any
		if err := c.BindOnly(&body); err != nil {
			return err
		}
		return New(http.StatusInternalServerError, "LOGIN_BROKEN", "boom")
	})

	req := httptest.NewRequest(http.MethodPost, "/login?token=abc&user=jane",
		strings.NewReader(`{"email":"a@b.co","password":"hunter2"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer abcdef")
	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, req)

	require.Equal(t, 1, rec.Len())
	ev := rec.Last()
	require.NotNil(t, ev.Request)
	assert.Contains(t, ev.Request.Data, `"email":"a@b.co"`)
	assert.NotContains(t, ev.Request.Data, "hunter2")
	assert.Equal(t, redacted, ev.Request.Headers["Authorization"])
	assert.Equal(t, "application/json", ev.Request.Headers["Content-Type"])
	assert.Contains(t, ev.Request.QueryString, "token="+redacted)
	assert.Contains(t, ev.Request.QueryString, "user=jane")
	assert.Empty(t, ev.Request.Cookies)
}

func TestSentry_httpErrorTaggedAndHeaderReturned(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	e := NewHTTPServer(app, nil)
	e.GET("/users/:id", func(c IHTTPContext) error {
		return c.NewError(errors.New("cause"), New(http.StatusInternalServerError, "USER_LOOKUP", "boom"))
	})

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/users/42", nil))

	require.Equal(t, 1, rec.Len(), "reported once, not once per layer")
	ev := rec.Last()
	assert.Equal(t, "USER_LOOKUP", eventCode(ev))
	assert.Equal(t, "GET", ev.Tags["http.method"])
	assert.Equal(t, "500", ev.Tags["error.status"])
	assert.Equal(t, "http", ev.Tags["mode"])
	assert.NotEmpty(t, rr.Header().Get(sentryHeaderEventID))
}

func TestSentry_capturesPanicAsFatal(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	e := NewHTTPServer(app, nil)
	e.GET("/boom", func(c IHTTPContext) error { panic("kaboom") })

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/boom", nil))

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Equal(t, 1, rec.Len())
	ev := rec.Last()
	assert.Equal(t, LevelFatal, ev.Level)
	assert.Equal(t, "true", ev.Tags["panic"])
	assert.Equal(t, "/boom", ev.Tags["http.route"])
	assert.NotEmpty(t, rr.Header().Get(sentryHeaderEventID))
}

func TestSentry_logLinesBecomeBreadcrumbs(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	e := NewHTTPServer(app, nil)
	e.GET("/trail", func(c IHTTPContext) error {
		c.Log().Info("loading order", "order_id", "o-7")
		c.Log().Warn("cache miss")
		return New(http.StatusInternalServerError, "TRAIL", "boom")
	})

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/trail", nil))

	require.Equal(t, 1, rec.Len())
	ev := rec.Last()
	messages := make([]string, 0, len(ev.Breadcrumbs))
	for _, c := range ev.Breadcrumbs {
		messages = append(messages, c.Message)
	}
	assert.Contains(t, messages, "loading order")
	assert.Contains(t, messages, "cache miss")
}

func TestSentry_debugLinesAreNotBreadcrumbsByDefault(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{"LOG_LEVEL": "debug"})
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))
	ctx.Log().Debug("noisy detail")
	ctx.Log().Info("worth keeping")
	ctx.Sentry().CaptureError(New(500, "X", "x"))

	require.Equal(t, 1, rec.Len())
	assert.False(t, rec.HasBreadcrumb("noisy detail"))
	assert.True(t, rec.HasBreadcrumb("worth keeping"))
}

func TestSentry_logWithServerErrorBecomesEvent(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	e := NewHTTPServer(app, nil)
	e.GET("/degraded", func(c IHTTPContext) error {
		// handled, not returned — before this, the failure vanished from Sentry
		c.Log().Error("cache refresh failed, serving stale",
			"err", New(http.StatusInternalServerError, "CACHE_DOWN", "redis unreachable"),
			"key", "users:1")
		return c.JSON(200, "ok")
	})

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/degraded", nil))

	assert.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, 1, rec.Len())
	ev := rec.Last()
	assert.Equal(t, "CACHE_DOWN", eventCode(ev))
	assert.Equal(t, "cache refresh failed, serving stale", ev.Message)
	assert.Equal(t, "users:1", ev.Contexts["log"]["key"])
}

// A status is a judgement about the failure, and the threshold filters on it.
func TestSentry_logIgnoresJudgedClientFailures(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	// a client mistake is not an incident, however loudly it is logged
	ctx.Log().Error("rejected", "err", New(http.StatusBadRequest, "INVALID", "bad input"))
	ctx.Log().Warn("rejected", "err", New(http.StatusNotFound, "NOT_FOUND", "gone"))
	// narration below Error is not an incident either
	ctx.Log().Info("starting the nightly run")
	ctx.Log().Warn("cache is cold")

	assert.Equal(t, 0, rec.Len())
}

// No status means nobody judged the failure, so there is nothing to compare
// against the threshold — those are always reported.
func TestSentry_logAlwaysReportsWhatCarriesNoStatus(t *testing.T) {
	// a threshold high enough that no status could ever clear it
	app, rec := newSentryApp(t, map[string]string{"SENTRY_MIN_STATUS": "599"})
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	ctx.Log().Error("marshalling the payload", "err", errors.New("unexpected end of JSON input"))

	require.Equal(t, 1, rec.Len(), "a plain error has no status to judge")
	assert.Equal(t, sentry.LevelError, rec.Events()[0].Level)
}

// An IError left at status 0 is in the same position as a plain error.
func TestSentry_logAlwaysReportsZeroStatus(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{"SENTRY_MIN_STATUS": "599"})
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	ctx.Log().Error("no answer", "err", New(0, "UNKNOWN", "no status"))

	assert.Equal(t, 1, rec.Len())
}

// A bare Error() has no status either, and the code stopped to write it.
func TestSentry_logReportsErrorLineWithNoError(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{"SENTRY_MIN_STATUS": "599"})
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	ctx.Log().Error("ledger and gateway disagree")
	// a value under err that is not an error leaves the line without one
	ctx.Log().Error("weird", "err", "not-an-error")

	require.Equal(t, 2, rec.Len())
	assert.Equal(t, "ledger and gateway disagree", rec.Events()[0].Message)
}

func TestSentry_logThenReturnIsOneIssue(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	e := NewHTTPServer(app, nil)
	e.GET("/pay", func(c IHTTPContext) error {
		err := New(http.StatusInternalServerError, "GATEWAY_DOWN", "no answer")
		c.Log().Error("charge failed", "err", err) // reports it
		return c.NewError(err, errFromTest)        // must not report it again
	})

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/pay", nil))

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Equal(t, 1, rec.Len(), "one failure is one issue, whoever reports it first")
	assert.NotEmpty(t, rr.Header().Get(sentryHeaderEventID))
}

var errFromTest = New(http.StatusInternalServerError, "PAYMENT_FAILED", "payment failed")

func TestSentry_newErrorLogLineDoesNotDoubleReport(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	// the framework logs "request error" with the error it just reported
	ctx.NewError(errors.New("driver said no"), New(500, "DB_DOWN", "db down"))
	assert.Equal(t, 1, rec.Len())
}

func TestSentry_everyCapturePathCarriesTheSameScope(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{"DB_PASSWORD": "top-secret-value"})
	e := NewHTTPServer(app, nil)
	e.GET("/raw", func(c IHTTPContext) error {
		c.SetData("tenant", "acme")
		// built with core.New, never through c.NewError: the middleware reports
		// it, and it must still know the tenant and the config
		return New(http.StatusInternalServerError, "RAW", "boom")
	})

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/raw", nil))

	require.Equal(t, 1, rec.Len())
	ev := rec.Last()
	assert.Equal(t, "acme", ev.Contexts["context-data"]["tenant"])
	assert.Equal(t, redacted, ev.Contexts["config"]["db_password"])
	assert.Equal(t, "test", ev.Contexts["config"]["env"])
}

func TestSentry_breadcrumbCategoryFromComponent(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	ctx.Log().With("component", "billing").Info("เริ่มตัดบัตร", "order_id", "o-1")
	ctx.Sentry().CaptureError(New(500, "X", "x"))

	require.Equal(t, 1, rec.Len())
	crumbs := rec.Last().Breadcrumbs
	require.NotEmpty(t, crumbs)
	last := crumbs[len(crumbs)-1]
	assert.Equal(t, "billing", last.Category)
	assert.Equal(t, "เริ่มตัดบัตร", last.Message)
	assert.Equal(t, "o-1", last.Data["order_id"])
}

// --- Sentry Logs -----------------------------------------------------------

func TestSentry_logsAreOffByDefault(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	ctx.Log().Info("nothing to stream")
	app.Sentry().Flush(time.Second)

	assert.Empty(t, rec.Logs(), "logs have their own quota — opt in")
}

func TestSentry_logLinesStreamToSentryLogs(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{
		"SENTRY_ENABLE_LOGS": "true",
		"DB_PASSWORD":        "top-secret-value",
	})
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	ctx.Log().Info("charging card 50% of the time",
		"order_id", "o-7",
		"attempt", 2,
		"paid", true,
		"took", 150*time.Millisecond,
		"password", "hunter2",
		"conn", "postgres://u:top-secret-value@db/app")
	require.True(t, app.Sentry().Flush(2*time.Second))

	logs := rec.Logs()
	require.Len(t, logs, 1)
	l := logs[0]
	assert.Equal(t, sentry.LogLevelInfo, l.Level)
	// a literal % survives the SDK's formatting
	assert.Equal(t, "charging card 50% of the time", l.Body)
	// values keep their type, so Sentry can filter and aggregate on them
	assert.Equal(t, attribute.StringValue("o-7"), l.Attributes["order_id"])
	assert.Equal(t, attribute.Int64Value(2), l.Attributes["attempt"])
	assert.Equal(t, attribute.BoolValue(true), l.Attributes["paid"])
	assert.Equal(t, "150ms", l.Attributes["took"].AsString())
	// the same scrubbing every other channel gets: by key, and by secret value
	assert.Equal(t, redacted, l.Attributes["password"].AsString())
	assert.Equal(t, "postgres://u:"+redacted+"@db/app", l.Attributes["conn"].AsString())
}

func TestSentry_logStreamHasItsOwnFloor(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{
		"SENTRY_ENABLE_LOGS": "true",
		"SENTRY_LOG_LEVEL":   "warn",
	})
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	ctx.Log().Info("narration")
	ctx.Log().Warn("cache is cold")
	require.True(t, app.Sentry().Flush(2*time.Second))

	require.Len(t, rec.Logs(), 1)
	assert.Equal(t, "cache is cold", rec.Logs()[0].Body)
	assert.Equal(t, sentry.LogLevelWarn, rec.Logs()[0].Level)
}

// A line written outside any request or run — the scheduler starting up, a
// worker's own goroutine — has no hub on its context. It still reaches Sentry
// Logs; only the trace correlation is missing.
func TestSentry_logsWithoutAContextStillStream(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{"SENTRY_ENABLE_LOGS": "true"})

	app.Log().Error("scheduler could not start", "err", New(500, "SCHED_DOWN", "no lock"))
	require.True(t, app.Sentry().Flush(2*time.Second))

	require.Len(t, rec.Logs(), 1)
	l := rec.Logs()[0]
	assert.Equal(t, sentry.LogLevelError, l.Level)
	assert.Equal(t, "scheduler could not start", l.Body)
	// the code is what you filter an issue's logs by
	assert.Equal(t, "SCHED_DOWN", l.Attributes["error.code"].AsString())
	assert.Equal(t, 0, rec.Len(), "no hub, no event — the stream is the only report")
}

func TestSentry_streamedLogsCarryRequestAndTrace(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{
		"SENTRY_ENABLE_LOGS":    "true",
		"SENTRY_ENABLE_TRACING": "true",
	})
	e := NewHTTPServer(app, nil)
	e.GET("/pay", func(c IHTTPContext) error {
		c.Log().Info("charging card")
		return c.JSON(http.StatusOK, "ok")
	})

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/pay", nil))
	require.True(t, app.Sentry().Flush(2*time.Second))

	logs := rec.Logs()
	require.NotEmpty(t, logs)
	l := logs[0]
	assert.NotEmpty(t, l.Attributes["request_id"].AsString())
	// the log, the transaction and any event of this request share one trace id
	assert.NotEqual(t, sentry.TraceID{}, l.TraceID)
	assert.Equal(t, l.TraceID.String(), l.Attributes["trace_id"].AsString())
}

// --- Database ---------------------------------------------------------------

type sentryWidget struct {
	ID   uint `gorm:"primarykey"`
	Name string
}

func (sentryWidget) TableName() string { return "widgets" }

// A breadcrumb says a query happened. A span says how long the request spent
// waiting for it — without them a trace shows time going somewhere and never
// says where.
func TestSentry_queriesBecomeSpansInTheTrace(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{"SENTRY_ENABLE_TRACING": "true"})
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&sentryWidget{}))
	require.Nil(t, InstrumentGorm(db))

	e := NewHTTPServer(app, nil)
	e.GET("/widgets", func(c IHTTPContext) error {
		tx := db.WithContext(c.Request().Context())
		if err := tx.Create(&sentryWidget{Name: "hinge"}).Error; err != nil {
			return Wrap(err, "create")
		}
		var out []sentryWidget
		if err := tx.Find(&out).Error; err != nil {
			return Wrap(err, "find")
		}
		return c.JSON(http.StatusOK, out)
	})

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/widgets", nil))
	require.Equal(t, http.StatusOK, rr.Code)

	var tx *sentry.Event
	for _, ev := range rec.Events() {
		if ev.Type == "transaction" {
			tx = ev
		}
	}
	require.NotNil(t, tx, "tracing is on, so the request is a transaction")

	ops := map[string]*sentry.Span{}
	for _, s := range tx.Spans {
		ops[s.Op] = s
	}
	require.Contains(t, ops, "db.create")
	require.Contains(t, ops, "db.query")
	// the statement itself is the description, and the bind variables are never
	// interpolated into it
	assert.Contains(t, ops["db.create"].Description, "INSERT INTO")
	assert.NotContains(t, ops["db.create"].Description, "hinge")
	assert.Equal(t, "widgets", ops["db.query"].Data["db.table"])
	assert.Equal(t, sentry.SpanStatusOK, ops["db.query"].Status)
}

func TestSentry_queriesAreNotSpansWithoutTracing(t *testing.T) {
	app, rec := newSentryApp(t, nil) // tracing off
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&sentryWidget{}))
	require.Nil(t, InstrumentGorm(db))

	e := NewHTTPServer(app, nil)
	e.GET("/widgets", func(c IHTTPContext) error {
		var out []sentryWidget
		db.WithContext(c.Request().Context()).Find(&out)
		return c.JSON(http.StatusOK, out)
	})

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/widgets", nil))

	for _, ev := range rec.Events() {
		assert.NotEqual(t, "transaction", ev.Type, "no tracing, no transaction to hang spans on")
	}
}

// --- Metrics ---------------------------------------------------------------

func TestSentry_metricsAreOffByDefault(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	ctx := app.NewContext(context.Background())

	assert.False(t, ctx.Meter().Enabled())
	ctx.Meter().Count("orders.paid", 1)
	app.Sentry().Flush(time.Second)

	assert.Empty(t, rec.Metrics(), "metrics have their own quota — opt in")
}

func TestSentry_metricsRecordEveryKind(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{
		"SENTRY_ENABLE_METRICS": "true",
		"DB_PASSWORD":           "top-secret-value",
	})
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	require.True(t, ctx.Meter().Enabled())
	ctx.Meter().Count("orders.paid", 2, MetricAttr("gateway", "scb"))
	ctx.Meter().Gauge("queue.depth", 17, MetricUnit(""))
	ctx.Meter().Distribution("upload.size", 2048, MetricUnit(UnitByte))
	ctx.Meter().Duration("gateway.latency", 250*time.Millisecond,
		MetricAttrs(map[string]any{
			"retried":  true,
			"attempt":  2,
			"password": "hunter2",
			"conn":     "postgres://u:top-secret-value@db/app",
		}))
	require.True(t, app.Sentry().Flush(2*time.Second))

	require.Len(t, rec.Metrics(), 4)

	paid := rec.Metric("orders.paid")
	require.NotNil(t, paid)
	assert.Equal(t, "scb", paid.Attributes["gateway"].AsString())
	// the process-wide attributes ride along on measurements too
	assert.Equal(t, "sentry-test", paid.Attributes["service"].AsString())

	size := rec.Metric("upload.size")
	require.NotNil(t, size)
	assert.Equal(t, UnitByte, size.Unit)

	// Duration is a distribution in milliseconds, whatever unit the caller thinks in
	latency := rec.Metric("gateway.latency")
	require.NotNil(t, latency)
	assert.Equal(t, UnitMillisecond, latency.Unit)
	assert.Equal(t, true, latency.Attributes["retried"].AsBool())
	assert.Equal(t, int64(2), latency.Attributes["attempt"].AsInt64())
	// dimensions are scrubbed exactly like log attributes and event fields
	assert.Equal(t, redacted, latency.Attributes["password"].AsString())
	assert.Equal(t, "postgres://u:"+redacted+"@db/app", latency.Attributes["conn"].AsString())
}

// A measurement taken inside a request belongs to that request's trace, so a
// spike on the chart opens the trace that caused it.
func TestSentry_metricsCarryTheRequestTrace(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{
		"SENTRY_ENABLE_METRICS": "true",
		"SENTRY_ENABLE_TRACING": "true",
	})
	e := NewHTTPServer(app, nil)
	e.GET("/pay", func(c IHTTPContext) error {
		c.Meter().Count("orders.paid", 1)
		return c.JSON(http.StatusOK, "ok")
	})

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/pay", nil))
	require.True(t, app.Sentry().Flush(2*time.Second))

	m := rec.Metric("orders.paid")
	require.NotNil(t, m)
	assert.NotEqual(t, sentry.TraceID{}, m.TraceID)
	assert.Equal(t, "/pay", m.Attributes["http.route"].AsString())
}

// Scope attributes are what logs and metrics carry — a tag only ever reaches an
// event. Pinning them once per request means no call site has to repeat itself.
func TestSentry_scopeAttributesReachEveryLogLine(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{"SENTRY_ENABLE_LOGS": "true"})
	e := NewHTTPServer(app, nil)
	e.GET("/orders/:id", func(c IHTTPContext) error {
		c.Log().Info("loading order")
		return c.JSON(http.StatusOK, "ok")
	})

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/orders/7", nil))
	require.True(t, app.Sentry().Flush(2*time.Second))

	var handlerLine *sentry.Log
	for i, l := range rec.Logs() {
		// every line carries the process-wide attributes, whether or not it was
		// written inside a request
		assert.Equal(t, "sentry-test", l.Attributes["service"].AsString())
		if l.Body == "loading order" {
			handlerLine = &rec.Logs()[i]
		}
	}
	require.NotNil(t, handlerLine, "the handler's line should have been streamed")

	// and a line written through the request's context carries that request's
	// identity, without the handler passing any of it
	assert.Equal(t, "http", handlerLine.Attributes["mode"].AsString())
	// the route pattern, never the concrete path — /orders/7 would make every
	// order its own value to filter by
	assert.Equal(t, "/orders/:id", handlerLine.Attributes["http.route"].AsString())
	assert.Equal(t, "GET", handlerLine.Attributes["http.method"].AsString())
	assert.NotEmpty(t, handlerLine.Attributes["request_id"].AsString())
}

func TestSentry_jobFailureIsReportedOnceWithJobScope(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	reg := NewJobRegistry()
	require.NoError(t, reg.Register(JobDef{Name: "nightly", MaxAttempts: 2,
		Backoff: func(int) time.Duration { return time.Millisecond }},
		func(c ICronjobContext) error {
			c.Log().Info("starting nightly")
			return New(http.StatusInternalServerError, "REPORT_FAILED", "cannot build report")
		}))

	runner := NewJobRunner(app, reg)
	runner.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Stop(ctx)
	}()

	run, err := runner.TriggerAndWait(context.Background(), "nightly", nil)
	require.NoError(t, err)
	require.Equal(t, RunFailed, run.Status)

	// two attempts, one incident: a retry that also failed is the same problem
	require.Equal(t, 1, rec.Len())
	ev := rec.Last()
	assert.Equal(t, "REPORT_FAILED", eventCode(ev))
	assert.Equal(t, "nightly", ev.Tags["job"])
	assert.Equal(t, "cron", ev.Tags["mode"])
	assert.Equal(t, run.ID, ev.Tags["run_id"])
	assert.Equal(t, []string{"job", "nightly", "REPORT_FAILED"}, ev.Fingerprint)
	assert.True(t, rec.HasBreadcrumb("starting nightly"))
}

func TestSentry_jobRetriesReportedWhenAsked(t *testing.T) {
	app, rec := newSentryApp(t, nil, SentryOptions{CaptureRetries: boolPtr(true)})
	reg := NewJobRegistry()
	require.NoError(t, reg.Register(JobDef{Name: "flaky", MaxAttempts: 2,
		Backoff: func(int) time.Duration { return time.Millisecond }},
		func(c ICronjobContext) error { return New(500, "FLAKY", "nope") }))

	runner := NewJobRunner(app, reg)
	runner.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Stop(ctx)
	}()

	_, err := runner.TriggerAndWait(context.Background(), "flaky", nil)
	require.NoError(t, err)
	assert.Equal(t, 2, rec.Len())
}

func TestSentry_checkInsOnlyForScheduledRuns(t *testing.T) {
	app, rec := newSentryApp(t, nil, SentryOptions{EnableCrons: boolPtr(true)})
	reg := NewJobRegistry()
	require.NoError(t, reg.Register(JobDef{Name: "hourly", Schedule: Cron("0 * * * *")},
		func(c ICronjobContext) error { return nil }))

	runner := NewJobRunner(app, reg)
	runner.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Stop(ctx)
	}()

	_, err := runner.TriggerAndWait(context.Background(), "hourly", nil)
	require.NoError(t, err)
	assert.Empty(t, checkIns(rec), "a manual run is not the schedule reporting in")

	_, err = runner.TriggerAndWait(context.Background(), "hourly", nil,
		TriggerOptions{Trigger: TriggerSchedule})
	require.NoError(t, err)
	statuses := checkIns(rec)
	require.Len(t, statuses, 2)
	assert.Equal(t, CheckInProgress, statuses[0])
	assert.Equal(t, CheckInOK, statuses[1])
}

func checkIns(rec *SentryRecorder) []CheckInStatus {
	var out []CheckInStatus
	for _, e := range rec.Events() {
		if e.CheckIn != nil {
			out = append(out, e.CheckIn.Status)
		}
	}
	return out
}

func TestSentry_transactionsOnlyWhenTracingIsOn(t *testing.T) {
	app, rec := newSentryApp(t, nil)
	e := NewHTTPServer(app, nil)
	e.GET("/ok", func(c IHTTPContext) error { return c.JSON(200, "ok") })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ok", nil))
	assert.Empty(t, transactions(rec))

	traced, trec := newSentryApp(t, nil, SentryOptions{EnableTracing: boolPtr(true)})
	te := NewHTTPServer(traced, nil)
	te.GET("/ok", func(c IHTTPContext) error { return c.JSON(200, "ok") })
	te.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ok", nil))

	names := transactions(trec)
	require.Len(t, names, 1)
	assert.Equal(t, "GET /ok", names[0])
}

func transactions(rec *SentryRecorder) []string {
	var out []string
	for _, e := range rec.Events() {
		if e.Type == "transaction" {
			out = append(out, e.Transaction)
		}
	}
	return out
}

func TestSentry_monitorSlug(t *testing.T) {
	assert.Equal(t, "nightly-report", monitorSlug("Nightly Report"))
	assert.Equal(t, "sync-users", monitorSlug("sync_users"))
	assert.Equal(t, "job", monitorSlug("  "))
}

func TestSentry_scrubURL(t *testing.T) {
	assert.Equal(t, "https://api.test/v1/pay", scrubURL("https://api.test/v1/pay"))
	assert.Contains(t, scrubURL("https://api.test/v1?api_key=abc&id=7"), "api_key="+redacted)
	assert.Contains(t, scrubURL("https://api.test/v1?api_key=abc&id=7"), "id=7")
	assert.Contains(t, scrubURL("https://user:pw@api.test/v1"), redacted)
}

func TestSentry_scrubJSONKeepsShape(t *testing.T) {
	sc := newScrubber(nil, nil)
	out := sc.scrubJSON([]byte(`{"user":{"email":"a@b.co","password":"x"},"items":[{"token":"t"}]}`))
	assert.Contains(t, string(out), `"email":"a@b.co"`)
	assert.NotContains(t, string(out), `"x"`)
	assert.NotContains(t, string(out), `"t"`)
}

func boolPtr(b bool) *bool { return &b }

// --- Trace isolation ---------------------------------------------------------

// A cloned hub inherits the propagation context of the hub it came from, and the
// process hub's is generated once at boot. Nothing but the transaction used to
// replace it, so with tracing off — the default — every request in the process
// reported the same trace id and Sentry showed one endless trace.
func TestSentry_eachRequestGetsItsOwnTrace(t *testing.T) {
	for _, tracing := range []string{"false", "true"} {
		t.Run("tracing="+tracing, func(t *testing.T) {
			app, rec := newSentryApp(t, map[string]string{
				"SENTRY_ENABLE_LOGS":    "true",
				"SENTRY_ENABLE_TRACING": tracing,
			})
			e := NewHTTPServer(app, nil)
			e.GET("/a", func(c IHTTPContext) error {
				c.Log().Info("handling")
				return c.JSON(http.StatusOK, "ok")
			})

			for range 2 {
				rr := httptest.NewRecorder()
				e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/a", nil))
			}
			require.True(t, app.Sentry().Flush(2*time.Second))

			seen := map[string]bool{}
			for _, l := range rec.Logs() {
				if l.Body == "handling" {
					seen[l.TraceID.String()] = true
				}
			}
			assert.Len(t, seen, 2, "two requests, two traces")
		})
	}
}

// Tracing decides whether we time the request; it does not decide whether the
// request belongs to the trace its caller already started.
func TestSentry_incomingTraceIsContinuedWithoutTracing(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{"SENTRY_ENABLE_LOGS": "true"})
	e := NewHTTPServer(app, nil)
	e.GET("/a", func(c IHTTPContext) error {
		c.Log().Info("handling")
		return c.JSON(http.StatusOK, "ok")
	})

	const caller = "d49d9bf66f13450b81f65bc51cf49c03"
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	req.Header.Set(sentry.SentryTraceHeader, caller+"-1cc4b26ab9094ef0-1")
	e.ServeHTTP(httptest.NewRecorder(), req)
	require.True(t, app.Sentry().Flush(2*time.Second))

	var found bool
	for _, l := range rec.Logs() {
		if l.Body == "handling" {
			found = true
			assert.Equal(t, caller, l.TraceID.String())
		}
	}
	assert.True(t, found, "the handler's line should have been streamed")
}

// Job runs are units of work exactly like requests, and get the same treatment.
func TestSentry_eachJobRunGetsItsOwnTrace(t *testing.T) {
	app, rec := newSentryApp(t, map[string]string{"SENTRY_ENABLE_LOGS": "true"})
	reg := NewJobRegistry()
	require.NoError(t, reg.Register(JobDef{Name: "nightly"},
		func(c ICronjobContext) error {
			c.Log().Info("working")
			return nil
		}))

	runner := NewJobRunner(app, reg)
	runner.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Stop(ctx)
	}()

	for range 2 {
		_, err := runner.TriggerAndWait(context.Background(), "nightly", nil)
		require.NoError(t, err)
	}
	require.True(t, app.Sentry().Flush(2*time.Second))

	seen := map[string]bool{}
	for _, l := range rec.Logs() {
		if l.Body == "working" {
			seen[l.TraceID.String()] = true
		}
	}
	assert.Len(t, seen, 2, "two runs, two traces")
}

// Everything one request writes belongs to one trace — the handler's lines, the
// framework's own access line, the "panic recovered" line, the issue and the
// transaction. Lines written through app.Log() used to fall back to the process
// hub, so half of a request landed on a trace of its own.
func TestSentry_oneRequestIsOneTrace(t *testing.T) {
	for _, tracing := range []string{"false", "true"} {
		t.Run("tracing="+tracing, func(t *testing.T) {
			app, rec := newSentryApp(t, map[string]string{
				"SENTRY_ENABLE_LOGS":    "true",
				"SENTRY_ENABLE_TRACING": tracing,
			})
			e := NewHTTPServer(app, nil)
			e.GET("/boom", func(c IHTTPContext) error {
				c.Log().Info("before the panic")
				panic("test eiei")
			})

			rr := httptest.NewRecorder()
			e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/boom", nil))
			require.Equal(t, http.StatusInternalServerError, rr.Code)
			require.True(t, app.Sentry().Flush(2*time.Second))

			traces := map[string]bool{}
			var handler, recovered, access bool
			for _, l := range rec.Logs() {
				traces[l.TraceID.String()] = true
				switch {
				case l.Body == "before the panic":
					handler = true
				case l.Body == "panic recovered":
					recovered = true
				case strings.HasPrefix(l.Body, "GET /boom 500"):
					access = true
				}
			}
			// the handler's line, the recovery line and the access line
			assert.True(t, handler)
			assert.True(t, recovered)
			assert.True(t, access, "the access line says what happened without being opened")
			require.Len(t, traces, 1, "one request writes one trace")

			// and the issue is on that same trace
			require.Equal(t, 1, rec.Len(), "a panic is one issue, not three")
			ev := rec.Last()
			require.Contains(t, ev.Contexts, "trace")
			eventTrace, ok := ev.Contexts["trace"]["trace_id"].(sentry.TraceID)
			require.True(t, ok)
			for id := range traces {
				assert.Equal(t, id, eventTrace.String())
			}
		})
	}
}

// A struct logged whole reaches Sentry as JSON — and is scrubbed through that
// JSON. The attribute is called "user"; nothing about that name says there is a
// password inside it, so masking by key alone would send the payload in the
// clear.
func TestSentry_structAttributesAreJSONAndScrubbed(t *testing.T) {
	type account struct {
		ID       string `json:"id"`
		Password string `json:"password"`
		Card     struct {
			Number string `json:"card_number"`
		} `json:"card"`
	}
	a := account{ID: "u-1", Password: "hunter2"}
	a.Card.Number = "4111111111111111"

	app, rec := newSentryApp(t, map[string]string{"SENTRY_ENABLE_LOGS": "true"})
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))
	ctx.Log().Error("charge failed", "account", a)
	require.True(t, app.Sentry().Flush(2*time.Second))

	require.NotEmpty(t, rec.Logs())
	attr := rec.Logs()[0].Attributes["account"].AsString()
	assert.Contains(t, attr, `"id":"u-1"`, "the payload survives")
	assert.Contains(t, attr, `"password":"`+redacted+`"`)
	assert.Contains(t, attr, `"card_number":"`+redacted+`"`, "nested fields too")
	assert.NotContains(t, attr, "hunter2")
	assert.NotContains(t, attr, "4111111111111111")

	// and the same on the event the line raised
	require.Equal(t, 1, rec.Len())
	logCtx, ok := rec.Last().Contexts["log"]["account"].(map[string]any)
	require.True(t, ok, "a struct arrives as structured data, not as a Go dump")
	assert.Equal(t, redacted, logCtx["password"])
}

// A cron monitor whose schedule says 22:00 but whose timezone says UTC alerts on
// a missed run every day the job runs exactly on time.
func TestMonitorConfig_takesTheTimezoneFromTheSchedule(t *testing.T) {
	tz := bangkok(t)

	cfg := monitorConfig(CheckIn{Schedule: CronIn(tz, "0 22 * * *")})
	require.NotNil(t, cfg)
	assert.Equal(t, sentry.CrontabSchedule("0 22 * * *"), cfg.Schedule,
		"Sentry gets the bare expression — it would read a CRON_TZ prefix as an extra field")
	assert.Equal(t, "Asia/Bangkok", cfg.Timezone)

	// Timezone is the scheduler-wide default the runner passes in; the zone the
	// schedule names is the one gocron actually fires on, so it has to win.
	override := monitorConfig(CheckIn{Schedule: CronIn(tz, "0 22 * * *"), Timezone: "UTC"})
	require.NotNil(t, override)
	assert.Equal(t, "Asia/Bangkok", override.Timezone)

	fallback := monitorConfig(CheckIn{Schedule: Cron("0 22 * * *"), Timezone: "Asia/Bangkok"})
	require.NotNil(t, fallback)
	assert.Equal(t, "Asia/Bangkok", fallback.Timezone,
		"a schedule with no zone of its own runs in the scheduler's")

	plain := monitorConfig(CheckIn{Schedule: Cron("0 22 * * *")})
	require.NotNil(t, plain)
	assert.Empty(t, plain.Timezone, "no zone anywhere, none claimed to Sentry")
}

// sentry-go v0.49 removed ClientOptions.DisableLogs and DisableMetrics: the SDK
// now sends logs and metrics as soon as anything asks for a logger or a meter.
// The framework's own gates are therefore all that honours an opt-out, and this
// pins them. Each case also runs enabled, as the control that shows the recorder
// would have seen what the disabled run must not send.
func TestSentry_logsAndMetricsStayOffUnlessEnabled(t *testing.T) {
	for _, tc := range []struct {
		name, flag string
		enabled    bool
	}{
		{name: "disabled", flag: "false", enabled: false},
		{name: "enabled", flag: "true", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, rec := newSentryApp(t, map[string]string{
				"SENTRY_ENABLE_LOGS":    tc.flag,
				"SENTRY_ENABLE_METRICS": tc.flag,
			})
			ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

			ctx.Log().Warn("cache is cold")
			app.Log().Error("scheduler could not start")
			ctx.Meter().Count("orders.created", 1)
			app.Meter().Gauge("queue.depth", 3)
			require.True(t, app.Sentry().Flush(2*time.Second))

			if tc.enabled {
				assert.NotEmpty(t, rec.Logs(), "control: with logs on, the recorder sees them")
				assert.NotEmpty(t, rec.Metrics(), "control: with metrics on, the recorder sees them")
				return
			}
			assert.Empty(t, rec.Logs(),
				"logs were not enabled, and the SDK no longer has a switch of its own to stop them")
			assert.Empty(t, rec.Metrics(),
				"metrics were not enabled, and the SDK no longer has a switch of its own to stop them")
		})
	}
}
