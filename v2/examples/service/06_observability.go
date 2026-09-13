package main

import (
	"errors"
	"net/http"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
)

// --- Example 6: one id through every subsystem, one report per incident ------
//
// The first middleware stamps a request id onto the context itself, and from
// then on everything derived from that context carries it without being asked:
//
//	X-Request-ID ─▶ context ─┬─▶ ctx.Log()          every line: request_id=…
//	                         ├─▶ ctx.DB()           query log, same request
//	                         ├─▶ core.Requester(ctx) outbound calls, same request
//	                         └─▶ ctx.Sentry()       tags and breadcrumbs
//
// So a service needs no logger of its own: none on a struct, none passed as a
// parameter. Under HTTP the logger carries the request id; under the scheduler
// it carries the job name, run id and attempt. Same logger, different unit of
// work.

func registerGreeting(e *core.Server) {
	e.GET("/hello", hello)
	e.GET("/boom", boom)
}

func hello(c core.IHTTPContext) error {
	name := c.QueryParamOr("name", "world")

	// Debug, not Info: this runs on every request. Info is for work that changed
	// something — "user created", "note deleted", "signed in". The access line
	// (method, path, status, latency, request id) is already written by the
	// request middleware, so repeating any of it here only prints it twice.
	//
	// Key-value pairs, never fmt.Sprintf: the output is JSON, and a field can be
	// filtered and aggregated where a sentence cannot. Numbers go out as
	// numbers, so `duration_ms > 500` is a query rather than a string match.
	c.Log().Debug("greeting", "name_len", len(name))

	return c.JSON(http.StatusOK, map[string]string{"hello": name})
}

func boom(c core.IHTTPContext) error {
	err := errors.New("the widget factory is on fire")

	// ✅ one event, one report. NewError attaches this request's user, tags and
	// breadcrumbs, sends it to Sentry, and writes the 5xx log line itself.
	// Returning the error *is* reporting it.
	return c.NewError(err, errmsgs.InternalServerError)

	// ❌ never this. The logger bridges to Sentry too, so logging and then
	// returning files one incident as two issues, triaged separately by two
	// people who each think the other one is theirs:
	//
	//	c.Log().Error("boom", "err", err)
	//	return c.NewError(err, errmsgs.InternalServerError)
	//
	// The same rule upward: a handler that returns an error from a service must
	// not log it either. Whoever returns last is not the one who reports.
}

func sweepExpired(c core.ICronjobContext) error {
	// c.Log() here carries job, run_id and attempt — the cron counterpart of
	// request_id. Adding them by hand just prints them twice.
	deleted := 0 // a real one would delete rows through repository.New[…](c)

	// Reported on every run, including the ones that counted zero: a job that
	// only speaks when it found work is indistinguishable from a job that died
	// three weeks ago.
	c.Log().Info("expired tokens swept", "deleted", deleted)

	c.SetResult(map[string]any{"deleted": deleted})
	return nil
}

// Which level, and what must never appear:
//
//	Info   work that changed data, and every scheduled run
//	Warn   the service worked correctly and refused the caller — a rejected
//	       business rule, a failed sign-in. Alert on the *rate* of these, not on
//	       one appearing.
//	Error  only what nobody else reports. A returned error is already reported,
//	       so this is left for structural mistakes: a route registered without
//	       its middleware, an initialiser never called.
//	Debug  detail for the ten minutes somebody is actually chasing something.
//
// Never logged: tokens, password hashes, any credential — a log store always has
// more readers than the database. Personal data goes in as identifiers, never as
// values: user_id rather than the email, note_id rather than the note.
//
// ⚠ Sentry's scrubber masks sensitive fields on the way *to Sentry only*.
// Whatever is handed to ctx.Log() is printed to stdout in full, so a log
// pipeline that forwards elsewhere has to filter at that layer — or the secret
// has to not be there in the first place.
//
// Field names must match across services, or they cannot be queried together:
// user_id (not userId/uid), err (not error/e), reason (not why/cause),
// duration_ms as a number (not "41ms"). snake_case, like the keys the framework
// pins itself: request_id, trace_id, run_id.
