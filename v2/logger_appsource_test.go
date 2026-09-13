// This file is deliberately in package core_test rather than core.
//
// blameApp answers "which line of the service caused this?" by looking for the
// first frame under the main module that is not mine-core itself — and when
// mine-core's own tests run, mine-core *is* the main module. An in-package test
// would therefore be indistinguishable from the framework and could never stand
// in for a service. The external test package has its own import path
// ("…/v2_test"), so its frames look exactly like a consumer's do.
package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	gormlogger "gorm.io/gorm/logger"

	core "github.com/pskclub/mine-core/v2"
)

// sourceOf decodes the JSON line in buf and returns its source, reduced to
// "file.go:line".
func sourceOf(t *testing.T, buf *bytes.Buffer) string {
	t.Helper()
	require.NotEmpty(t, buf.Bytes(), "expected a log line to have been written")
	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec), "expected exactly one line: %s", buf.String())
	src, ok := rec["source"].(string)
	require.True(t, ok, "the line carries no source: %s", buf.String())
	return path.Base(src)
}

// here returns "file.go:line" for the line offset lines below the caller. The
// call being measured must sit on a single line for this to be exact.
func here(offset int) string {
	_, file, line, _ := runtime.Caller(1)
	return fmt.Sprintf("%s:%d", path.Base(file), line+offset)
}

func TestSQLLog_blamesTheCallerNotTheFramework(t *testing.T) {
	var buf bytes.Buffer
	// gorm reaches Trace through a stack of driver frames whose depth differs
	// per statement, which is why no fixed frame count could find this test
	log := core.NewGormLogger(core.NewLoggerTo(&buf, nil), gormlogger.Info, time.Second)
	statement := func() (string, int64) { return "SELECT 1", 1 }

	want := here(1)
	log.Trace(context.Background(), time.Now(), statement, assert.AnError)

	assert.Equal(t, want, sourceOf(t, &buf),
		"a query line must name the repository call that issued it, not database_logger.go")
}

func TestHTTPClientLog_blamesTheCallerNotTheFramework(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	app, err := core.NewApp(testEnv(t), core.WithLogger(core.NewLoggerTo(&buf, nil)))
	require.NoError(t, err)
	ctx := app.NewContext(context.Background())
	req := core.Requester(ctx)
	buf.Reset() // drop whatever booting the app wrote

	want := here(1)
	_, _ = req.Send(req.R(), http.MethodGet, srv.URL)

	assert.Equal(t, want, sourceOf(t, &buf),
		"an outgoing call must name the service code that made it, not requester_log.go")
}

func TestMongoLog_blamesTheCallerNotTheFramework(t *testing.T) {
	var buf bytes.Buffer
	// the driver runs the command monitor on the goroutine that issued the
	// command, so the call is still on the stack — as it is here
	monitor := core.NewMongoMonitor(core.NewLoggerTo(&buf, nil), core.MongoLogError, time.Minute)
	cmd, err := bson.Marshal(bson.D{{Key: "find", Value: "users"}})
	require.NoError(t, err)

	monitor.Started(context.Background(), &event.CommandStartedEvent{Command: cmd, DatabaseName: "db", CommandName: "find", RequestID: 1})

	// the line is written when the command finishes, so that is the call it
	// must be attributed to
	want := here(1)
	monitor.Failed(context.Background(), &event.CommandFailedEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{Duration: time.Millisecond, CommandName: "find", RequestID: 1},
		Failure:              assert.AnError,
	})

	assert.Equal(t, want, sourceOf(t, &buf),
		"a mongo command must name the repository call behind it, not database_mongo_logger.go")
}

func TestLLMLog_blamesTheCallerNotTheFramework(t *testing.T) {
	// a completion that worked is logged at debug, so the logger has to be built
	// from an env that lets it through
	t.Setenv("APP_LOG_LEVEL", "debug")
	env := testEnv(t)
	var buf bytes.Buffer
	app, err := core.NewApp(env,
		core.WithLogger(core.NewLoggerTo(&buf, env)), core.WithLLM(core.NewMemoryLLM("hi")))
	require.NoError(t, err)
	ctx := app.NewContext(context.Background())
	llm := core.LLM(ctx)
	req := core.LLMRequest{Messages: []core.LLMMessage{core.LLMUser("hello")}}
	buf.Reset()

	want := here(1)
	_, _ = llm.Generate(req)

	assert.Equal(t, want, sourceOf(t, &buf),
		"a completion must name the handler that asked for it, not llm.go")
}

// testEnv builds an IENV the way a service would, from APP_ variables.
func testEnv(t *testing.T) core.IENV {
	t.Helper()
	t.Setenv("APP_ENV", "test")
	t.Setenv("APP_SERVICE", "test-svc")
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	return env
}
