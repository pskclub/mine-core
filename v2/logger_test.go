package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogger_writesStructuredJSON(t *testing.T) {
	var buf bytes.Buffer
	log := NewLoggerTo(&buf, nil)

	log.Info("user created", "user_id", "u1", "count", 3)

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec), "log line should be json")
	assert.Equal(t, "user created", rec["msg"])
	assert.Equal(t, "u1", rec["user_id"])
}

func TestLogger_levelFiltering(t *testing.T) {
	env := mustEnv(t, map[string]string{"LOG_LEVEL": "warn"})
	var buf bytes.Buffer
	log := NewLoggerTo(&buf, env)

	log.Debug("debug msg")
	log.Info("info msg")
	log.Warn("warn msg")

	out := buf.String()
	assert.NotContains(t, out, "debug msg", "debug filtered at warn level")
	assert.NotContains(t, out, "info msg", "info filtered at warn level")
	assert.Contains(t, out, "warn msg")
}

func TestLogger_simpleUsesText(t *testing.T) {
	env := mustEnv(t, map[string]string{"LOG_SIMPLE": "true"})
	var buf bytes.Buffer
	log := NewLoggerTo(&buf, env)
	log.Info("hi")
	assert.NotContains(t, buf.String(), "{", "LOG_SIMPLE should produce text, not json")
}

func TestLogger_withContextAddsRequestID(t *testing.T) {
	var buf bytes.Buffer
	base := NewLoggerTo(&buf, nil).(*logger)
	ctx := context.WithValue(context.Background(), requestIDKey, "req-123")

	base.withContext(ctx).Info("hello")

	assert.Contains(t, buf.String(), "req-123")
}

// sourceOfLine returns the "file.go:line" a log line should be attributed to,
// given the result of runtime.Caller taken n lines above the logging call.
func sourceOfLine(file string, callerLine, offset int) string {
	return fmt.Sprintf("%s:%d", path.Base(file), callerLine+offset)
}

func TestLogger_recordsTheCallSite(t *testing.T) {
	var buf bytes.Buffer
	log := NewLoggerTo(&buf, nil)

	_, file, line, _ := runtime.Caller(0)
	log.Info("user created")

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	src, _ := rec["source"].(string)
	assert.Equal(t, sourceOfLine(file, line, 1), path.Base(src),
		"the line blamed must be the caller's, not one inside logger.go")
	assert.Contains(t, src, "/", "the last directory is kept so file names from different packages do not collide")
}

func TestLogger_callSiteSurvivesWithAndContext(t *testing.T) {
	// With and withContext clone the logger; a clone that lost the source
	// setting or the frame count would blame the framework instead
	var buf bytes.Buffer
	base := NewLoggerTo(&buf, nil).(*logger)
	log := base.withContext(context.Background()).With("component", "billing")

	_, file, line, _ := runtime.Caller(0)
	log.Warn("slow")

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	assert.Equal(t, sourceOfLine(file, line, 1), path.Base(rec["source"].(string)))
	assert.Equal(t, "billing", rec["component"], "pinned attributes still ride along")
}

func TestLogger_runLoggerBlamesTheJobNotTheFramework(t *testing.T) {
	var buf bytes.Buffer
	log := newRunLogger(NewLoggerTo(&buf, nil), nil)

	_, file, line, _ := runtime.Caller(0)
	log.Info("importing")

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	assert.Equal(t, sourceOfLine(file, line, 1), path.Base(rec["source"].(string)),
		"a job's line belongs to the job, not to the tee in job_log.go")
}

// TestLogger_blameAppFallsBackToItsOwnFrame is the other half of blameApp: when
// no application frame is on the stack — a driver's background goroutine, a
// query run at boot — the line keeps the frame that wrote it rather than
// inventing one.
//
// This test is in package core on purpose. Framework frames are excluded from
// the search, so an in-package test *is* a stack with no application frame on
// it; the same case written in package core_test finds one (see
// logger_appsource_test.go).
func TestLogger_blameAppFallsBackToItsOwnFrame(t *testing.T) {
	var buf bytes.Buffer
	log := blameApp(NewLoggerTo(&buf, nil))

	_, file, line, _ := runtime.Caller(0)
	log.Info("mongo primary elected")

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	assert.Equal(t, sourceOfLine(file, line, 1), path.Base(rec["source"].(string)),
		"with nothing of the application to blame, the writing frame is the honest answer")
}

func TestPkgOf(t *testing.T) {
	assert.Equal(t, "gorm.io/gorm/callbacks", pkgOf("gorm.io/gorm/callbacks.BuildQuerySQL"),
		"the package path has dots of its own, so the name starts after the last slash")
	assert.Equal(t, "gorm.io/gorm", pkgOf("gorm.io/gorm.(*DB).First"))
	assert.Equal(t, "main", pkgOf("main.main"))
	assert.Equal(t, "database/sql", pkgOf("database/sql.(*DB).QueryContext"))
}

func TestIsAppFrame(t *testing.T) {
	// mine-core's own tests build a binary in which mine-core is the module, not
	// a dependency — hence the explicit exclusion the deps list cannot make
	require.Equal(t, "github.com/pskclub/mine-core/v2", frameworkPkg)

	assert.False(t, isAppFrame(frameworkPkg+".(*gormLogger).Trace"), "the framework is never the answer")
	assert.False(t, isAppFrame(frameworkPkg+"/repository.(*Repo[...]).Find"), "nor is a framework subpackage")
	assert.False(t, isAppFrame("gorm.io/gorm.(*DB).First"), "nor is a driver")
	assert.False(t, isAppFrame("runtime.gopanic"), "nor the runtime")
	assert.True(t, isAppFrame(frameworkPkg+"_test.TestSomething"),
		"the external test package stands in for a consuming service — the whole rule is untestable otherwise")

	// what a consuming service looks like: mine-core and its drivers are now
	// dependencies, and the service's own module is not in the list at all
	deps := map[string]struct{}{
		frameworkPkg: {}, "gorm.io/gorm": {}, "example.com/acme/shared-lib": {},
	}
	const service = "example.com/acme/juristic-api"
	appFrame := func(fn string) bool { return appFrameIn(fn, deps, service) }

	assert.True(t, appFrame(service+"/modules/juristic.(*service).Search"))
	assert.True(t, appFrame("main.main"), "a query issued from main is still the service's")
	assert.False(t, appFrame(frameworkPkg+".(*gormLogger).Trace"))
	assert.False(t, appFrame("gorm.io/gorm/callbacks.Query"), "a package of a dependency module, not the module itself")
	assert.False(t, appFrame("database/sql.(*DB).QueryContext"))
	assert.False(t, appFrame("example.com/acme/shared-lib/client.Call"),
		"a shared library is a dependency like any other; the frame that called it is the answer")

	// `go run main.go` leaves the main module unknown, which is the case the
	// deps list exists to cover — the service must still be found without it
	unknown := func(fn string) bool { return appFrameIn(fn, deps, "") }
	assert.True(t, unknown(service+"/modules/juristic.(*service).Search"))
	assert.False(t, unknown("gorm.io/gorm/callbacks.Query"))

	// a module path with no dot in its first element is indistinguishable from
	// the standard library, and only the main module's name can rescue it
	assert.True(t, appFrameIn("backend/services.Search", deps, "backend"))
	assert.False(t, unknown("backend/services.Search"),
		"unknowable: nothing tells this apart from net/http, so it is left alone")
}

// TestReadMachinery_ignoresModulesBuiltFromSource pins the discriminator that
// makes `go run main.go` work. Running a *file* rather than a package builds
// "command-line-arguments" and demotes the service's own module to a dependency
// — one marked "(devel)", because it was not downloaded. Counting it as
// machinery would skip the service's own frames and blame main.go for every
// query in the process.
func TestReadMachinery_ignoresModulesBuiltFromSource(t *testing.T) {
	deps := machineryOf([]*debug.Module{
		{Path: "gorm.io/gorm", Version: "v1.31.2"},
		{Path: "example.com/service", Version: "(devel)"},
		{Path: "example.com/workspace-lib", Version: ""},
	})

	assert.Contains(t, deps, "gorm.io/gorm", "a downloaded dependency is machinery")
	assert.NotContains(t, deps, "example.com/service", "the service being run is not")
	assert.NotContains(t, deps, "example.com/workspace-lib", "nor is anything else built from source")
}

func TestLogger_sourceCanBeTurnedOff(t *testing.T) {
	env := mustEnv(t, map[string]string{"LOG_SOURCE": "false"})
	var buf bytes.Buffer
	NewLoggerTo(&buf, env).Info("hi")

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	assert.NotContains(t, rec, "source")
}

func TestShortSource(t *testing.T) {
	assert.Equal(t, "repository/user.go:42", shortSource("/builds/app/repository/user.go", 42))
	assert.Equal(t, "user.go:7", shortSource("user.go", 7), "a bare file name stays bare")
}

// mustEnv builds an IENV from a map of APP_-less keys for tests.
func mustEnv(t *testing.T, kv map[string]string) IENV {
	t.Helper()
	for k, v := range kv {
		t.Setenv("APP_"+k, v)
	}
	e, err := NewEnvPath(t.TempDir())
	require.NoError(t, err)
	return e
}
