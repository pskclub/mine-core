package devtools

import (
	"encoding/json"
	"net/http"
	"strings"

	core "github.com/pskclub/mine-core/v2"
)

// --- guards ---

// writable refuses every action unless the mount was built with AllowWrite and a
// Runner. Two separate reasons, reported separately: one is a decision somebody
// made and the other is a wiring mistake, and telling them apart is the
// difference between "ask for the flag" and "fix the Mount call".
func (d *devtools) writable() core.IError {
	if !d.opts.AllowWrite {
		return core.New(http.StatusForbidden, "DEVTOOLS_READ_ONLY",
			"devtools: this mount is read-only — set Options.AllowWrite to enable actions")
	}
	if d.opts.Runner == nil {
		return core.New(http.StatusNotFound, "DEVTOOLS_NO_JOB_RUNNER",
			"devtools: no job runner — pass one as Options.Runner when mounting")
	}
	return nil
}

// actor is who to record an action against.
//
// The authenticated principal when there is one, and otherwise a label that says
// plainly where the action came from. It is never blank: a run whose
// triggered_by is empty is indistinguishable from one the scheduler created, and
// that is precisely the row somebody will be asking about.
func actor(c core.IHTTPContext) string {
	if user := c.GetUser(); user != nil {
		switch {
		case user.Email != "":
			return user.Email
		case user.Username != "":
			return user.Username
		case user.ID != "":
			return user.ID
		}
	}
	return "devtools@" + c.RealIP()
}

// audit records an action that changed something. It is a warn rather than an
// info because these lines are the ones somebody goes looking for after the
// fact, and they should survive a service running at warn.
func (d *devtools) audit(c core.IHTTPContext, action string, fields ...any) {
	d.app.Log().Warn("devtools "+action,
		append([]any{"by", actor(c), "ip", c.RealIP()}, fields...)...)
}

// --- jobs ---

// triggerRequest is the body of a manual trigger: the parameters the job
// declares, plus the few knobs worth having on the button.
type triggerRequest struct {
	// Params is decoded into the job's own parameter type by the runner, and
	// validated there — devtools does not second-guess a schema it only renders.
	//
	// It is raw JSON rather than a map because a job's parameter type is any
	// type: a job taking []string, or a bare string, could not be triggered from
	// here at all while this was map[string]any, and the schema devtools renders
	// a form from does not say which shape the type has.
	Params json.RawMessage `json:"params"`
	// IdemKey makes a double-clicked button one run instead of two.
	IdemKey string `json:"idem_key"`
	// CaptureLogs keeps every line of this run even when the job's policy is
	// LogOff, which is the usual reason for triggering one by hand.
	CaptureLogs *bool `json:"capture_logs"`
}

// givenParams is what the runner should receive: nil when the caller supplied
// none, so the job keeps whatever it would have had otherwise (its own defaults
// on a trigger, the original run's on a replay), and the bytes untouched
// otherwise.
//
// An empty object counts as none — the form sends one for a job whose every
// field was left blank, and that is "run it as the scheduler would", not
// "override every parameter with its zero value".
func givenParams(raw json.RawMessage) any {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "{}":
		return nil
	}
	return raw
}

func (d *devtools) triggerJob(c core.IHTTPContext) error {
	if err := d.writable(); err != nil {
		return err
	}
	var req triggerRequest
	// An empty body is a job that takes no parameters, not a malformed request.
	if c.Request().ContentLength > 0 {
		if err := c.BindOnly(&req); err != nil {
			return err
		}
	}

	name := c.Param("name")
	opts := core.TriggerOptions{By: actor(c), IdemKey: req.IdemKey}
	if req.CaptureLogs != nil && *req.CaptureLogs {
		always := core.LogAlways
		opts.CaptureLogs = &always
	}

	run, err := d.opts.Runner.Trigger(c, name, givenParams(req.Params), opts)
	if err != nil {
		return err
	}
	d.audit(c, "trigger", "job", name, "run_id", run.ID)
	return c.JSON(http.StatusOK, run)
}

func (d *devtools) pauseJob(c core.IHTTPContext) error {
	if err := d.writable(); err != nil {
		return err
	}
	name := c.Param("name")
	if err := d.opts.Runner.Pause(name); err != nil {
		return err
	}
	d.audit(c, "pause", "job", name)
	return c.JSON(http.StatusOK, map[string]any{"name": name, "paused": true})
}

func (d *devtools) resumeJob(c core.IHTTPContext) error {
	if err := d.writable(); err != nil {
		return err
	}
	name := c.Param("name")
	if err := d.opts.Runner.Resume(name); err != nil {
		return err
	}
	d.audit(c, "resume", "job", name)
	return c.JSON(http.StatusOK, map[string]any{"name": name, "paused": false})
}

// --- runs ---

type cancelRequest struct {
	Reason string `json:"reason"`
}

func (d *devtools) cancelRun(c core.IHTTPContext) error {
	if err := d.writable(); err != nil {
		return err
	}
	var req cancelRequest
	if c.Request().ContentLength > 0 {
		if err := c.BindOnly(&req); err != nil {
			return err
		}
	}
	if req.Reason == "" {
		req.Reason = "canceled from devtools"
	}

	id := c.Param("id")
	if err := d.opts.Runner.Cancel(c, id, actor(c), req.Reason); err != nil {
		return err
	}
	d.audit(c, "cancel", "run_id", id, "reason", req.Reason)

	// The run itself, so the caller sees the state the cancellation left it in —
	// a queued run is already canceled, a running one has only been asked.
	run, err := d.opts.Runner.Run(c, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// replayRequest re-runs a finished run, optionally with different parameters.
//
// Sending none is the common case and keeps the original's — "run last night's
// billing again" — while sending some covers the other half of why anyone
// replays: the run failed on one bad input, and the point is to change it.
type replayRequest struct {
	Params      json.RawMessage `json:"params"`
	CaptureLogs *bool           `json:"capture_logs"`
}

func (d *devtools) replayRun(c core.IHTTPContext) error {
	if err := d.writable(); err != nil {
		return err
	}
	var req replayRequest
	if c.Request().ContentLength > 0 {
		if err := c.BindOnly(&req); err != nil {
			return err
		}
	}

	id := c.Param("id")
	// same rule as a trigger: absent parameters mean "unchanged", which for a
	// replay is the original run's, so an empty form never silently blanks them
	opts := core.ReplayOptions{By: actor(c), Params: givenParams(req.Params)}
	if req.CaptureLogs != nil && *req.CaptureLogs {
		always := core.LogAlways
		opts.CaptureLogs = &always
	}

	run, err := d.opts.Runner.Replay(c, id, opts)
	if err != nil {
		return err
	}
	d.audit(c, "replay", "run_id", id, "new_run_id", run.ID, "params_overridden", opts.Params != nil)
	return c.JSON(http.StatusOK, run)
}

// --- trace ---

func (d *devtools) traceList(c core.IHTTPContext) error {
	if d.opts.Trace == nil {
		return errNoTrace()
	}
	items := d.opts.Trace.List()
	return c.JSON(http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (d *devtools) traceGet(c core.IHTTPContext) error {
	if d.opts.Trace == nil {
		return errNoTrace()
	}
	entry, ok := d.opts.Trace.Get(c.Param("id"))
	if !ok {
		return core.Newf(http.StatusNotFound, "TRACE_NOT_FOUND",
			"request %s is not in the trace buffer — it is bounded, so an old request is gone", c.Param("id"))
	}
	return c.JSON(http.StatusOK, entry)
}

// traceClear does not need AllowWrite: emptying a debug buffer changes nothing
// about the service, and the alternative is an operator restarting a process to
// get a readable trace.
func (d *devtools) traceClear(c core.IHTTPContext) error {
	if d.opts.Trace == nil {
		return errNoTrace()
	}
	d.opts.Trace.Clear()
	return c.NoContent(http.StatusNoContent)
}

func errNoTrace() core.IError {
	return core.New(http.StatusNotFound, "DEVTOOLS_NO_TRACE",
		"devtools: request tracing is off — pass Options.Trace and core.WithLogTap to turn it on")
}
