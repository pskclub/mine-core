// This file is deliberately an external test package. The trimming it checks
// drops frames belonging to the framework's own package, and an internal test is
// *in* that package — every frame it produced would be trimmed along with the
// ones under test, and the assertion would prove nothing.
package core_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getsentry/sentry-go"
	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// corePackage is the import path a frame of the framework carries.
const corePackage = "github.com/pskclub/mine-core/v2"

func newStacktraceApp(t *testing.T) (*core.App, *core.SentryRecorder) {
	t.Helper()

	t.Setenv("APP_ENV", "test")
	t.Setenv("APP_SERVICE", "stacktrace-test")
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	tracker, rec, serr := core.NewRecordingSentry(env)
	require.NoError(t, serr)
	app, aerr := core.NewApp(env, core.WithSentry(tracker))
	require.NoError(t, aerr)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	return app, rec
}

// framesOf is the trace the issue is named after: the exception's, or the
// thread's for an event that carries no exception (a message, or a panic whose
// value was not an error).
func framesOf(t *testing.T, event *sentry.Event) []sentry.Frame {
	t.Helper()
	if n := len(event.Exception); n > 0 {
		require.NotNil(t, event.Exception[n-1].Stacktrace, "the exception should carry a stack trace")
		return event.Exception[n-1].Stacktrace.Frames
	}
	require.NotEmpty(t, event.Threads, "an event with no exception should carry a thread")
	require.NotNil(t, event.Threads[0].Stacktrace)
	return event.Threads[0].Stacktrace.Frames
}

// assertReportedAt checks that the innermost frame is the code that failed.
func assertReportedAt(t *testing.T, function string, frames []sentry.Frame) {
	t.Helper()
	require.NotEmpty(t, frames)
	// frames run oldest first: the innermost is the last
	assert.Equal(t, function, frames[len(frames)-1].Function,
		"the trace should start at the code that failed, not at the framework that reported it")
}

// assertNoFrameworkFrames checks that nothing of the framework is left on a
// trace that should be entirely the caller's. It does not hold for a panic,
// whose stack runs through the middleware that was serving the request.
func assertNoFrameworkFrames(t *testing.T, frames []sentry.Frame) {
	t.Helper()
	for _, frame := range frames {
		assert.NotEqual(t, corePackage, frame.Module,
			"no frame of the reporting path belongs on the trace: %s", frame.Function)
	}
}

// uploadReceipt stands in for service code logging a failure it got from a
// library — the error carries no stack, so sentry-go builds one, and it used to
// build it inside the logger's Sentry bridge.
func uploadReceipt(ctx core.IContext) {
	ctx.Log().Error("upload failed", "err", errors.New("connection reset by peer"))
}

func TestStacktrace_logBridgeReportsAtTheLine(t *testing.T) {
	app, rec := newStacktraceApp(t)
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	uploadReceipt(ctx)

	require.Equal(t, 1, rec.Len())
	frames := framesOf(t, rec.Last())
	assertReportedAt(t, "uploadReceipt", frames)
	assertNoFrameworkFrames(t, frames)
}

// A line that stopped to write Error() with no error attached is reported as a
// message, whose stack lives on the thread rather than on an exception.
func reconcileLedger(ctx core.IContext) {
	ctx.Log().Error("ledger and gateway disagree")
}

func TestStacktrace_messageEventReportsAtTheLine(t *testing.T) {
	app, rec := newStacktraceApp(t)
	ctx := app.NewContext(sentry.SetHubOnContext(context.Background(), app.Sentry().Hub().Clone()))

	reconcileLedger(ctx)

	require.Equal(t, 1, rec.Len())
	frames := framesOf(t, rec.Last())
	assertReportedAt(t, "reconcileLedger", frames)
	assertNoFrameworkFrames(t, frames)
}

// A panic is reported from a deferred call, which runs on the stack that
// panicked — so once the recovery's own frames are gone, the panicking line is
// what is left at the top.
func TestStacktrace_panicReportsAtTheLineThatPanicked(t *testing.T) {
	app, rec := newStacktraceApp(t)
	srv := core.NewHTTPServer(app, nil)
	srv.GET("/boom", panicHandler)

	srv.Echo.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

	require.Equal(t, 1, rec.Len())
	assertReportedAt(t, "panicHandler", framesOf(t, rec.Last()))
}

func panicHandler(_ core.IHTTPContext) error {
	panic("kaboom")
}

// An *Error captured its own stack where it was built, and that one points at
// the failure — nothing taken at the report should replace it.
func chargeCard(_ core.IContext) core.IError {
	return core.New(http.StatusInternalServerError, "GATEWAY_DOWN", "gateway said no")
}

func TestStacktrace_errorKeepsTheStackItWasBuiltWith(t *testing.T) {
	app, rec := newStacktraceApp(t)
	ctx := app.NewContext(context.Background())

	err := chargeCard(ctx) // built here, reported below: two different stacks
	ctx.Sentry().CaptureError(err)

	require.Equal(t, 1, rec.Len())
	var named bool
	for _, frame := range framesOf(t, rec.Last()) {
		named = named || frame.Function == "chargeCard"
	}
	assert.True(t, named,
		"the stack the error was born with is the one that reaches Sentry — that frame is gone by the time it is reported")
}
