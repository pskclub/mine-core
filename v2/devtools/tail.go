package devtools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// DefaultTailTimeout bounds how long a tail connection is held open. It is not a
// limit on the run: the browser reconnects, and a run that outlives it keeps
// streaming into the next connection. It exists so a forgotten tab does not hold
// a goroutine and a subscription forever.
const DefaultTailTimeout = 10 * time.Minute

// tailRun streams a run's log lines as they are written, as server-sent events.
//
// It reads the runner's live hub rather than the store, and that is the whole
// point: the default log policy is LogOff, so a running job persists nothing at
// all. Reading the store for a run in flight shows an empty panel for exactly
// the run somebody is watching — which is the one that has not finished, and
// therefore the one they are worried about.
//
// It only works in the process that is executing the run. An API replica whose
// worker runs elsewhere has no hub to read: the tail closes immediately and the
// panel falls back to the stored lines. Saying that plainly is better than a
// stream that stays open forever and never emits, which reads as a job that is
// doing nothing.
func (d *devtools) tailRun(c core.IHTTPContext) error {
	if d.opts.Runner == nil {
		return errNoJobs("runner", "Options.Runner")
	}
	id := c.Param("id")

	// Whether the run is even live decides what this endpoint can promise, so it
	// is checked before the stream is opened rather than reported as silence.
	run, err := d.opts.Runner.Run(c, id)
	if err != nil {
		return err
	}

	res := c.Response()
	header := res.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	// Nginx buffers a proxied response by default, which for a stream means the
	// browser sees nothing until it ends — the exact failure this endpoint is
	// meant to avoid.
	header.Set("X-Accel-Buffering", "no")
	res.WriteHeader(http.StatusOK)

	flusher, ok := res.(http.Flusher)
	if !ok {
		return core.New(http.StatusInternalServerError, "STREAM_UNSUPPORTED",
			"devtools: this server cannot stream")
	}

	lines, unsubscribe := d.opts.Runner.TailLogs(id)
	defer unsubscribe()

	// A terminal run has nothing left to say. The stream still opens, so the
	// client's handling is the same either way, and closes at once with a reason.
	if run.Status.IsTerminal() {
		writeEvent(res, flusher, "end", map[string]string{"reason": "run already finished"})
		return nil
	}

	writeEvent(res, flusher, "open", map[string]any{"run_id": id, "status": run.Status})

	deadline := time.After(DefaultTailTimeout)
	// A comment frame every 20s keeps a proxy from closing an idle connection —
	// a quiet job is not a dead one.
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case line, open := <-lines:
			if !open {
				writeEvent(res, flusher, "end", map[string]string{"reason": "run finished"})
				return nil
			}
			writeEvent(res, flusher, "log", line)
		case <-keepalive.C:
			fmt.Fprint(res, ": keepalive\n\n")
			flusher.Flush()
		case <-deadline:
			writeEvent(res, flusher, "end", map[string]string{"reason": "tail timeout — reconnect to keep watching"})
			return nil
		case <-c.Done():
			// the client went away; the deferred unsubscribe is what matters
			return nil
		}
	}
}

// writeEvent emits one SSE frame. A payload that cannot be marshalled is skipped
// rather than allowed to break the framing of everything after it.
func writeEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
	flusher.Flush()
}
