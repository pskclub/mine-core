package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestApp(t *testing.T, opts ...Option) *App {
	t.Helper()
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "test-svc"})
	app, err := NewApp(env, opts...)
	require.NoError(t, err)
	return app
}

func TestContext_basics(t *testing.T) {
	app := newTestApp(t)
	ctx := app.NewContext(context.Background(), ModeCron)

	assert.Equal(t, ModeCron, ctx.Mode())
	assert.Equal(t, "test-svc", ctx.ENV().Config().Service)
	assert.NotNil(t, ctx.Log())
	// an unconfigured database is nil — a *gorm.DB has no disabled form
	assert.Nil(t, ctx.DB())
	// the cache degrades instead: a handle that misses every read is what keeps
	// cache-aside code running without a redis
	require.NotNil(t, ctx.Cache())
	assert.False(t, ctx.Cache().Enabled())
	assert.False(t, ctx.PubSub().Enabled())
	// the queue refuses instead: a message dropped in silence is work nobody
	// will ever pick up
	require.NotNil(t, ctx.MQ())
	assert.False(t, ctx.MQ().Enabled())
	assert.ErrorIs(t, ctx.MQ().Publish("x", "y", "z"), ErrMQDisabled)
	// requester is created by default
	assert.NotNil(t, Requester(ctx))
}

func TestContext_typedData(t *testing.T) {
	app := newTestApp(t)
	ctx := app.NewContext(context.Background())

	Set(ctx, "user_id", "u-42")
	Set(ctx, "count", 7)

	id, ok := Get[string](ctx, "user_id")
	assert.True(t, ok)
	assert.Equal(t, "u-42", id)

	n, ok := Get[int](ctx, "count")
	assert.True(t, ok)
	assert.Equal(t, 7, n)

	_, ok = Get[bool](ctx, "count")
	assert.False(t, ok, "wrong type should report not-ok, not panic")

	_, ok = Get[string](ctx, "missing")
	assert.False(t, ok)
}

func TestContext_user(t *testing.T) {
	app := newTestApp(t)
	ctx := app.NewContext(context.Background())
	assert.Nil(t, ctx.GetUser())
	ctx.SetUser(&ContextUser{ID: "u1", Email: "a@b.com"})
	require.NotNil(t, ctx.GetUser())
	assert.Equal(t, "u1", ctx.GetUser().ID)
}

func TestContext_NewError(t *testing.T) {
	app := newTestApp(t)
	ctx := app.NewContext(context.Background())

	cause := errors.New("db down")
	err := ctx.NewError(cause, New(500, "DATABASE_ERROR", "database internal error"))

	assert.Equal(t, 500, err.GetStatus())
	assert.Equal(t, "DATABASE_ERROR", err.GetCode())
	assert.ErrorIs(t, err, cause, "NewError should wrap the cause")
}

func TestContext_NewError_stackPointsAtTheCallSite(t *testing.T) {
	app := newTestApp(t)
	ctx := app.NewContext(context.Background())

	// the sentinel is the shape of the failure, not its location: two unrelated
	// call sites passing it must not report the same trace
	err := ctx.NewError(errors.New("db down"), testSentinel)

	var e *Error
	require.ErrorAs(t, err, &e)
	frame := topFrame(t, e)
	assert.Contains(t, frame.Function, "TestContext_NewError_stackPointsAtTheCallSite",
		"the trace must start where NewError was called, not at the sentinel's declaration or inside the framework")
	assert.Contains(t, frame.File, "context_test.go")
}

func TestContext_NewError_devSurfacesCause(t *testing.T) {
	env := mustEnv(t, map[string]string{"ENV": "dev", "SERVICE": "svc"})
	app, err := NewApp(env)
	require.NoError(t, err)
	ctx := app.NewContext(context.Background())

	ierr := ctx.NewError(errors.New("secret detail"), New(400, "BAD_REQUEST", "bad request"))
	assert.Equal(t, "secret detail", ierr.GetMessage(), "dev should surface the original message")
}

func TestContext_NewError_logBlamesTheCallSite(t *testing.T) {
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "svc"})
	var buf bytes.Buffer
	app, err := NewApp(env, WithLogger(NewLoggerTo(&buf, env)))
	require.NoError(t, err)
	ctx := app.NewContext(context.Background())

	_, file, line, _ := runtime.Caller(0)
	ctx.NewError(errors.New("upstream said 401"), InternalServerError())

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	assert.Equal(t, sourceOfLine(file, line, 1), path.Base(rec["source"].(string)),
		"a 5xx must name the line that gave up, not the context.go line every service shares")
}

func TestContext_WithContext(t *testing.T) {
	app := newTestApp(t)
	ctx := app.NewContext(context.Background())
	type k struct{}
	child := ctx.WithContext(context.WithValue(context.Background(), k{}, "v"))
	assert.Equal(t, "v", child.Value(k{}))
}
