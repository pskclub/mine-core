package main

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
	core "github.com/pskclub/mine-core/v2"
)

// --- Example 8: an admin API over the runner --------------------------------
//
// The runner exposes everything an operator UI needs, so the HTTP layer is thin
// enough to keep in your own service — and yours to secure.
//
// SECURITY: these endpoints run arbitrary business jobs. Mount them behind real
// authentication and authorisation, never on a public path.

func mountJobAdmin(e *core.Server, runner *core.JobRunner, adminOnly echo.MiddlewareFunc) {
	if adminOnly == nil {
		panic("job admin API mounted without authentication") // fail fast, loudly
	}
	g := e.Group("/_jobs", adminOnly)

	// What jobs exist, and what each one takes. JobInfo already carries the
	// parameter schema — name, kind (string/int/bool/date/enum/…), whether it is
	// required, its enum values and description — so a UI can render the "run
	// now" form without anyone hand-writing it:
	//
	//	{
	//	  "name": "sales-report",
	//	  "schedule": "cron(30 1 * * *)",
	//	  "params": [
	//	    {"name": "date",   "kind": "date", "required": true,
	//	     "description": "Day to report on", "example": "2026-07-01"},
	//	    {"name": "status", "kind": "enum", "enum": ["draft","sent","paid"], "default": "sent"},
	//	    {"name": "limit",  "kind": "int",  "description": "Max rows"},
	//	    {"name": "force",  "kind": "bool"},
	//	    {"name": "emails", "kind": "array", "items": {"kind": "string"}}
	//	  ]
	//	}
	g.GET("", func(c core.IHTTPContext) error {
		return c.JSON(http.StatusOK, runner.Registry().Info())
	})

	// just the parameter schema of one job, for a form that loads on demand
	g.GET("/:name/params", func(c core.IHTTPContext) error {
		params := runner.Registry().Params(c.Param("name"))
		if _, ok := runner.Registry().Def(c.Param("name")); !ok {
			return core.Newf(http.StatusNotFound, "JOB_NOT_FOUND",
				"job %s is not registered", c.Param("name"))
		}
		return c.JSON(http.StatusOK, params)
	})

	// run it now. ?wait=1 blocks for the outcome; otherwise you get the queued
	// run back straight away and poll (or tail the logs).
	g.POST("/:name/run", func(c core.IHTTPContext) error {
		var params map[string]any
		if err := c.BindOnly(&params); err != nil {
			return err
		}
		opts := core.TriggerOptions{
			By:      userOf(c),
			IdemKey: c.QueryParam("idem_key"),
		}
		if c.QueryParam("capture_logs") == "always" {
			opts.CaptureLogs = core.LogPolicyPtr(core.LogAlways)
		}
		if c.QueryParam("wait") != "" {
			run, err := runner.TriggerAndWait(c, c.Param("name"), params, opts)
			if err != nil {
				return err
			}
			return c.JSON(http.StatusOK, run)
		}
		run, err := runner.Trigger(c, c.Param("name"), params, opts)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusAccepted, run)
	})

	g.POST("/:name/pause", func(c core.IHTTPContext) error {
		if err := runner.Pause(c.Param("name")); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})

	g.POST("/:name/resume", func(c core.IHTTPContext) error {
		if err := runner.Resume(c.Param("name")); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})

	// history, with the framework's usual pagination
	g.GET("/runs", func(c core.IHTTPContext) error {
		f := core.JobRunFilter{
			JobName: c.QueryParam("job"),
			Queue:   c.QueryParam("queue"),
			Trigger: core.Trigger(c.QueryParam("trigger")),
			Page:    c.GetPageOptions(),
		}
		if s := c.QueryParam("status"); s != "" {
			f.Statuses = []core.RunStatus{core.RunStatus(s)}
		}
		page, err := runner.Runs(c, f)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, page)
	})

	g.GET("/runs/:id", func(c core.IHTTPContext) error {
		run, err := runner.Run(c, c.Param("id"))
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, run)
	})

	// persisted logs, paged by seq. Empty when the policy did not keep them —
	// use the stream endpoint below while the run is in flight.
	g.GET("/runs/:id/logs", func(c core.IHTTPContext) error {
		after, _ := strconv.ParseInt(c.QueryParam("after_seq"), 10, 64)
		entries, err := runner.Logs(c, c.Param("id"), after, 500)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, entries)
	})

	// live tail (server-sent events) — reads the in-memory hub, so it works
	// whatever the log policy is
	g.GET("/runs/:id/stream", func(c core.IHTTPContext) error {
		lines, unsubscribe := runner.TailLogs(c.Param("id"))
		defer unsubscribe()

		res := c.Response()
		res.Header().Set(echo.HeaderContentType, "text/event-stream")
		res.Header().Set("Cache-Control", "no-cache")
		res.WriteHeader(http.StatusOK)

		for {
			select {
			case <-c.Request().Context().Done():
				return nil
			case e, ok := <-lines:
				if !ok {
					return nil
				}
				if _, err := res.Write([]byte("data: " + e.Level + " " + e.Message + "\n\n")); err != nil {
					return nil
				}
				// v5 hands out a plain http.ResponseWriter; SSE needs the flusher
				if f, ok := res.(http.Flusher); ok {
					f.Flush()
				}
			}
		}
	})

	g.POST("/runs/:id/cancel", func(c core.IHTTPContext) error {
		reason := c.QueryParam("reason")
		if err := runner.Cancel(c, c.Param("id"), userOf(c), reason); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})

	// replay a finished run as a new one (409 if it is still going, 403 if the
	// job opted out of being replayed)
	g.POST("/runs/:id/replay", func(c core.IHTTPContext) error {
		run, err := runner.Replay(c, c.Param("id"), core.ReplayOptions{By: userOf(c)})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusAccepted, run)
	})
}

func userOf(c core.IHTTPContext) string {
	if u := c.GetUser(); u != nil {
		return u.ID
	}
	return "unknown"
}
