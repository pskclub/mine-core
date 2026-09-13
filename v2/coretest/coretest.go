// Package coretest builds the fixtures a service's tests need: an App and an
// IContext backed by a real database, an HTTP client that speaks the framework's
// error shape, and a way to run jobs.
//
// It exists so a consuming service does not hand-roll the same forty lines of
// wiring in every repository. Import it from _test.go files only — it depends on
// testing, and every helper takes a *testing.T so failures point at the caller.
//
// # Two database backends, one suite
//
// The same tests run against either, chosen by an environment variable:
//
//	go test ./...                                      # sqlite in memory
//	TEST_DATABASE_URL=postgres://... go test ./...      # the real engine
//
// sqlite needs nothing installed and gives each test its own database, so it is
// what you run on every save. It is also a different database: no partial
// indexes, no UUID column type, no ILIKE, and a schema invented from your Go
// structs rather than the one you deploy. A suite that only ever sees sqlite
// will pass on constraints postgres rejects — so run the postgres path before
// you push, pointed at your real migrations.
//
// # Quick start
//
//	func TestCreateUser(t *testing.T) {
//	    ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&User{}))
//
//	    user, err := services.NewUserService(ctx).Create(payload)
//	    require.Nil(t, err)
//	}
//
// See NewContext, NewServer and RunJob for the three entry points.
package coretest

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/utils"
)

// EnvDatabaseURL names the variable that switches the suite onto postgres.
// Empty means sqlite.
const EnvDatabaseURL = "TEST_DATABASE_URL"

// Option configures the fixture. Defaults: sqlite, ENV=test, no tables.
type Option func(*config)

type config struct {
	env         map[string]string
	models      []any
	migrations  string
	dsn         string
	mode        core.Mode
	appOptions  []core.Option
	skipDefault bool
}

// WithAutoMigrate creates tables from Go models. Convenient, and a shortcut: it
// builds the schema your structs describe, not the one your migrations produce.
// Prefer WithMigrations for anything you deploy.
func WithAutoMigrate(models ...any) Option {
	return func(c *config) { c.models = append(c.models, models...) }
}

// WithMigrations applies every migration.sql found one directory deep in dir,
// in name order — the layout prisma and golang-migrate both produce.
//
// This is what makes a postgres run worth having: the tables, types and indexes
// are the ones production has, so a constraint your code contradicts fails here
// rather than in staging. Ignored on sqlite, whose dialect will not accept them.
func WithMigrations(dir string) Option {
	return func(c *config) { c.migrations = dir }
}

// WithDatabaseURL forces postgres at this DSN, ignoring TEST_DATABASE_URL.
func WithDatabaseURL(dsn string) Option {
	return func(c *config) { c.dsn = dsn }
}

// WithEnv sets configuration keys for the fixture, named as in ENVConfig
// ("log_level", "jwt_secret"). They are scoped to the test.
func WithEnv(kv map[string]string) Option {
	return func(c *config) {
		for k, v := range kv {
			c.env[k] = v
		}
	}
}

// WithMode sets the context mode (default ModeTest).
func WithMode(m core.Mode) Option { return func(c *config) { c.mode = m } }

// WithAppOptions passes extra options to NewApp — a cache, an MQ, a stub
// requester.
func WithAppOptions(opts ...core.Option) Option {
	return func(c *config) { c.appOptions = append(c.appOptions, opts...) }
}

// WithoutDefaultTransaction turns off GORM's implicit transaction around
// writes. Use it to prove your own transaction is doing the work: without this,
// a test cannot tell your Transaction from GORM's.
func WithoutDefaultTransaction() Option {
	return func(c *config) { c.skipDefault = true }
}

// IsPostgres reports whether this run uses postgres, for the occasional test
// that only makes sense on one engine.
func IsPostgres() bool { return os.Getenv(EnvDatabaseURL) != "" }

// NewContext returns an IContext on an isolated database — the usual entry
// point for service and repository tests.
func NewContext(t *testing.T, opts ...Option) core.IContext {
	t.Helper()

	app, cfg := newApp(t, opts...)
	return app.NewContext(t.Context(), cfg.mode)
}

// NewApp returns the App behind NewContext, for tests that need to build several
// contexts (a scheduler, an HTTP server) over one set of pools.
func NewApp(t *testing.T, opts ...Option) *core.App {
	t.Helper()

	app, _ := newApp(t, opts...)
	return app
}

// NewDB returns just the database handle, for tests that want to seed or assert
// without going through a context.
func NewDB(t *testing.T, opts ...Option) *gorm.DB {
	t.Helper()

	cfg := newConfig(opts...)
	return open(t, cfg)
}

func newApp(t *testing.T, opts ...Option) (*core.App, *config) {
	t.Helper()

	cfg := newConfig(opts...)
	db := open(t, cfg)

	for k, v := range cfg.env {
		t.Setenv("APP_"+strings.ToUpper(k), v)
	}

	// an empty directory: configuration comes from the environment above, so a
	// stray .env in the checkout cannot change what the tests see
	env, err := core.NewEnvPath(t.TempDir())
	if err != nil {
		t.Fatalf("coretest: env: %v", err)
	}

	appOpts := append([]core.Option{core.WithSQL("default", db)}, cfg.appOptions...)
	app, err := core.NewApp(env, appOpts...)
	if err != nil {
		t.Fatalf("coretest: app: %v", err)
	}

	return app, cfg
}

func newConfig(opts ...Option) *config {
	cfg := &config{
		env:  map[string]string{"env": "test", "service": "coretest"},
		mode: core.ModeTest,
	}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.dsn == "" {
		cfg.dsn = os.Getenv(EnvDatabaseURL)
	}
	return cfg
}

func open(t *testing.T, cfg *config) *gorm.DB {
	t.Helper()

	if cfg.dsn != "" {
		return openPostgres(t, cfg)
	}
	return openSQLite(t, cfg)
}

// openSQLite gives each test its own database: ":memory:" dies with the
// connection, so nothing needs cleaning up and tests cannot interfere.
func openSQLite(t *testing.T, cfg *config) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), gormConfig(cfg))
	if err != nil {
		t.Fatalf("coretest: sqlite: %v", err)
	}
	if len(cfg.models) > 0 {
		if err := db.AutoMigrate(cfg.models...); err != nil {
			t.Fatalf("coretest: sqlite migrate: %v", err)
		}
	}
	return db
}

// openPostgres gives each test its own schema inside the shared database.
//
// A schema is the cheap unit of isolation: creating one costs a statement, tests
// stay independent enough to run in parallel, and dropping it cascades away
// every table — so this can point at a development database without disturbing
// the data in it.
func openPostgres(t *testing.T, cfg *config) *gorm.DB {
	t.Helper()

	schema := "test_" + strings.ReplaceAll(utils.NewUUID(), "-", "")

	admin, err := gorm.Open(postgres.Open(cfg.dsn), gormConfig(cfg))
	if err != nil {
		t.Fatalf("coretest: postgres unreachable at %s: %v", redact(cfg.dsn), err)
	}
	if err := admin.Exec(fmt.Sprintf(`CREATE SCHEMA %q`, schema)).Error; err != nil {
		t.Fatalf("coretest: create schema: %v", err)
	}

	scoped, err := gorm.Open(postgres.Open(withSearchPath(t, cfg.dsn, schema)), gormConfig(cfg))
	if err != nil {
		t.Fatalf("coretest: postgres (scoped): %v", err)
	}

	t.Cleanup(func() {
		admin.Exec(fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema))
		closeDB(scoped)
		closeDB(admin)
	})

	if cfg.migrations != "" {
		applyMigrations(t, scoped, cfg.migrations)
	}
	if len(cfg.models) > 0 && cfg.migrations == "" {
		if err := scoped.AutoMigrate(cfg.models...); err != nil {
			t.Fatalf("coretest: postgres migrate: %v", err)
		}
	}

	return scoped
}

func gormConfig(cfg *config) *gorm.Config {
	return &gorm.Config{
		SkipDefaultTransaction: cfg.skipDefault,
		Logger:                 gormSilent(),
	}
}

func closeDB(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// withSearchPath points a connection at one schema, so the unqualified names in
// migrations land there instead of in public.
func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("coretest: cannot parse %s: %v", EnvDatabaseURL, err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	return u.String()
}

// applyMigrations runs every <dir>/*/migration.sql in name order. Both prisma
// and golang-migrate prefix directories with a timestamp, so lexical order is
// apply order.
func applyMigrations(t *testing.T, db *gorm.DB, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("coretest: read migrations %s: %v", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatalf("coretest: no migrations in %s", dir)
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(dir, name, "migration.sql")
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("coretest: read %s: %v", path, readErr)
		}
		if err := db.Exec(string(body)).Error; err != nil {
			t.Fatalf("coretest: apply %s: %v", name, err)
		}
	}
}

// redact keeps the password out of a failure message.
func redact(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "(unparseable dsn)"
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	return u.String()
}
