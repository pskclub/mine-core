package main

import (
	"net/http"
	"testing"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/coretest"
)

// --- Example 7: testing the real service, with nothing mocked ---------------
//
// These are ordinary tests; they live in this file rather than a _test.go one
// only so the documentation can show them next to the code they exercise. Copy
// them into service_test.go in your own repository and they run as they stand.
//
// v2 ships no generated mocks and no mock generator. Every capability exports an
// in-memory implementation instead — NewMemoryCache, NewMemoryStorage,
// NewMemoryMailer, NewMemoryPusher, NewRecordingSentry — and coretest composes
// them into fixtures. The reason is that a generated mock proves a method was
// called, while a memory implementation proves the result was right; and being
// real code compiled against the real interface, it breaks the day the interface
// changes rather than staying silent until somebody regenerates.

// TestHTTP drives the same routes production serves, through the same middleware
// stack: request id, recovery, the IError renderer. What the test asserts is
// therefore what a caller receives, not an internal struct.
func TestHTTP(t *testing.T) {
	app := coretest.NewApp(t)
	srv := coretest.NewServerWithApp(t, app, nil)
	registerModules(srv.Server) // ★ the production composition root, not a copy

	// liveness must answer without touching anything
	srv.Get("/healthz").RequireStatus(http.StatusOK)

	body := srv.Get("/hello?name=ada").RequireStatus(http.StatusOK).Map()
	if body["hello"] != "ada" {
		t.Fatalf("the handler must echo the name it was given, got %v", body["hello"])
	}

	// a failing route returns core.IError, so the body has the framework's
	// shape — asserting on it is asserting on the contract clients depend on
	failure := srv.Get("/boom").RequireStatus(http.StatusInternalServerError).Error()
	if failure.Code != "INTERNAL_SERVER_ERROR" {
		t.Fatalf("a 5xx must carry its error code so clients can branch on it, got %q", failure.Code)
	}
}

// TestReadiness proves the probe reports what this deployment actually depends
// on. The fixture registers a SQL connection and nothing else, so "database" is
// checked and there is no cache check to fail.
func TestReadiness(t *testing.T) {
	app := coretest.NewApp(t)

	report := core.CheckHealth(t.Context(), app)
	if report.Status != core.HealthUp {
		t.Fatalf("a fixture with a live sqlite must be up, got %s: %+v", report.Status, report.Checks)
	}
	if _, ok := report.Checks["cache"]; ok {
		t.Fatal("an unconfigured cache must be absent from the probe, not a check that always fails")
	}
}

// TestSweepExpired runs the job through a real runner, so the test exercises the
// path a schedule or a manual trigger would: parameters are validated, a panic
// becomes a failed run, and the run is recorded with a status.
func TestSweepExpired(t *testing.T) {
	j := coretest.NewJob(t)
	j.Register("sweep-expired-tokens", sweepExpired)

	run := j.Run("sweep-expired-tokens", nil)

	// a failing run does not fail the test — assert on the status, because a
	// failure is often exactly what is being tested
	if run.Status != core.RunSucceeded {
		t.Fatalf("the sweep must succeed with nothing to delete, got %s: %v", run.Status, run.Error)
	}
}

// TestWithCache shows the substitution: the service code is unchanged, the App
// is given a memory cache instead of redis, and the assertions are about stored
// values rather than about calls.
func TestWithCache(t *testing.T) {
	ctx := coretest.NewContext(t,
		coretest.WithAppOptions(core.WithCache("default", core.NewMemoryCache())),
		coretest.WithEnv(map[string]string{"service": "example-service"}),
	)

	if err := ctx.Cache().Set("greeting", "hello", core.NoExpiry); err != nil {
		t.Fatalf("the memory cache must accept writes: %v", err)
	}
	var got string
	if err := ctx.Cache().Get("greeting", &got); err != nil || got != "hello" {
		t.Fatalf("what was written must read back, got %q (%v)", got, err)
	}
}

// Two backends, one suite. sqlite needs nothing installed and gives each test
// its own database, so it is what runs on every save; postgres is the schema you
// deploy:
//
//	go test ./...                                    # sqlite, in memory
//	TEST_DATABASE_URL=postgres://… go test ./...      # the real engine
//
// Run the postgres path before pushing, pointed at your real migrations
// (coretest.WithMigrations). sqlite has no partial indexes, no UUID type and no
// ILIKE, and builds its schema from your Go structs — a suite that only ever
// sees sqlite passes on constraints postgres rejects.
