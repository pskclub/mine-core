package main

import (
	"net/http"
	"strconv"
	"strings"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 5: configuration, and failing at boot instead of at 2am ---------
//
// core.NewEnv() reads ./.env (or ./test.env when APP_ENV=test), then lets
// APP_-prefixed environment variables override it. Two naming rules, and the
// second one is where the hours go:
//
//	OS environment   APP_DB_HOST=localhost   ->  key db_host
//	.env file        DB_HOST=localhost       ->  key db_host
//
// An OS variable without APP_ is ignored entirely (so the system's own HOST and
// PATH cannot collide with configuration). A key written *with* APP_ inside the
// file binds "app_db_host", matches no field, and reports nothing — the value
// is simply never read.
//
// Adding a key is one field in ENVConfig with a koanf tag; the loader binds it
// automatically, and unlike v1 there is no second list to forget:
//
//	NewFeatureURL string `koanf:"new_feature_url"`   // APP_NEW_FEATURE_URL=...
//
// A bool that should default to true cannot be a bool field: "unset" and
// "explicitly false" are the same zero value. Read it as a string first, which
// is how the framework's own LOG_SOURCE and SENTRY_CAPTURE_BODY work.

func loadConfig() (core.IENV, core.IError) {
	// NewEnv already fails on an APP_ENV that is not dev|test|mock|prod and on a
	// malformed Sentry DSN. Everything the framework cannot know goes below.
	env, err := core.NewEnv()
	if err != nil {
		return nil, err
	}
	if err := validateConfig(env); err != nil {
		return nil, err
	}
	return env, nil
}

// serviceConfig is this service's own configuration, resolved once at boot so
// that a bad value is a failed deploy rather than a failed request. Parsing a
// setting on every use spreads the same error across every code path that reads
// it, and delays it until the one request that happened to take that path.
type serviceConfig struct {
	Role role
	// GreetingsPerMinute has no field in ENVConfig because it belongs to this
	// service, not to the framework. env.String/Int is the escape hatch for
	// exactly that; a key the framework owns should be added to ENVConfig.
	GreetingsPerMinute int
}

const defaultGreetingsPerMinute = 60

func newServiceConfig(env core.IENV) (serviceConfig, core.IError) {
	cfg := serviceConfig{
		Role: roleFrom(env),
		// a default belongs here, next to the field it fills, and not in a .env
		// checked into the repository — the file then holds only what a
		// deployment actually overrides
		GreetingsPerMinute: defaultGreetingsPerMinute,
	}
	if raw := env.String("greetings_per_minute"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return cfg, core.Newf(http.StatusInternalServerError, "INVALID_CONFIG",
				"GREETINGS_PER_MINUTE must be a positive integer, got %q", raw)
		}
		cfg.GreetingsPerMinute = n
	}
	return cfg, nil
}

func validateConfig(env core.IENV) core.IError {
	cfg := env.Config()

	// A misspelled role is the failure this catches. roleFrom() falls back to
	// "api" for an unset key, which is the right default — but it would also
	// swallow APP_ROLE=wroker and quietly deploy a second API instead of the
	// worker, leaving every scheduled job unrun with nothing in any log to say so.
	if r := strings.ToLower(strings.TrimSpace(env.String("role"))); r != "" {
		switch role(r) {
		case roleAPI, roleWorker, roleAll:
		default:
			return core.Newf(http.StatusInternalServerError, "INVALID_CONFIG",
				"ROLE %q must be one of api|worker|all", r)
		}
	}

	// SERVICE tags every Sentry event. Unset, a production incident arrives with
	// no way to tell which service raised it.
	if env.IsProd() && cfg.Service == "" {
		return core.New(http.StatusInternalServerError, "INVALID_CONFIG",
			"SERVICE must be set in production")
	}

	if _, err := newServiceConfig(env); err != nil {
		return err
	}
	return nil
}

// logConfigWarnings covers what should be a warning rather than a refusal to
// start. ENV unset makes IsDev, IsTest, IsMock and IsProd all report false, so
// `if !env.IsProd() { … }` silently behaves as though it were dev — in
// production. It cannot be checked in loadConfig, which runs before any logger
// exists, so main calls this once the App is built.
func logConfigWarnings(app *core.App) {
	if app.Config().ENV == "" {
		app.Log().Warn("ENV is not set — every environment gate reads as false",
			"expected", "dev|test|mock|prod")
	}
}
