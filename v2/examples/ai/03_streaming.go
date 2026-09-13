package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 3: streaming to a browser --------------------------------------
//
// A stream is an iterator rather than a channel, on purpose: abandoning a
// channel halfway leaves the producer blocked on a send nobody will receive,
// while Close is an ordinary cleanup call. It is the shape of bufio.Scanner and
// sql.Rows, and it gives the terminal error one place to live — s.Err().
//
// The rule the whole file follows: `defer s.Close()` on the line after the
// error check, always. A user closing the tab is the normal case, not the edge
// case, and it is the one that leaks.

// streamAnswer writes an answer to the client as it arrives, as server-sent
// events.
func streamAnswer(c core.IHTTPContext) error {
	s, err := core.LLM(c).Stream(core.LLMRequest{
		System:      summaryRules,
		CacheSystem: true,
		Messages:    []core.LLMMessage{core.LLMUser(c.QueryParam("q"))},
		MaxTokens:   2048,
	})
	if err != nil {
		// Nothing has been written yet, so the framework's error shape still
		// reaches the client as JSON with a real status code. Once the first
		// byte is out, that is no longer true — see below.
		return err
	}
	defer func() { _ = s.Close() }()

	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// Without this nginx buffers the whole response and the user sees nothing
	// until it is finished, which is the one thing streaming was for.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// echo v5 hands out a plain http.ResponseWriter, so the flusher is a type
	// assertion rather than a method. Without flushing, every delta sits in the
	// buffer and arrives at once.
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	for s.Next() {
		// A delta can contain a newline, which ends an SSE frame early and
		// corrupts everything after it. Encoding as JSON is cheaper than
		// discovering that from a bug report about "answers that stop halfway".
		line, _ := json.Marshal(map[string]string{"t": s.Text()})
		if _, werr := fmt.Fprintf(w, "data: %s\n\n", line); werr != nil {
			// The client is gone. Returning nil rather than an error keeps this
			// out of Sentry: a closed tab is not an incident, and the deferred
			// Close releases the provider connection.
			return nil
		}
		flush()
	}
	if err := s.Err(); err != nil {
		// Headers are already sent, so this cannot become a 500. Say so in-band
		// and let the client decide what to render.
		fmt.Fprintf(w, "event: error\ndata: {\"code\":%q}\n\n", err.GetCode())
		flush()
		return nil
	}

	// Everything worth recording is only complete now: usage arrives with the
	// last chunk, and a grounded answer's citations arrive as chunks that carry
	// no text at all, so they never appeared as a delta.
	final := s.Response()
	done, _ := json.Marshal(map[string]any{
		"finish": string(final.FinishReason),
		"tokens": final.Usage.OutputTokens,
	})
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", done)
	flush()

	c.Log().Info("streamed an answer",
		"finish", string(final.FinishReason),
		"input_tokens", final.Usage.InputTokens,
		"output_tokens", final.Usage.OutputTokens,
		"cached_input_tokens", final.Usage.CachedInputTokens)
	return nil
}

// streamAndKeep streams to the user *and* stores the finished answer.
//
// The catch is that s.Response().Text is only complete once Next has returned
// false — mid-stream it holds whatever has arrived so far. Storing it inside
// the loop stores a fragment, and the row looks plausible enough that nobody
// notices.
func streamAndKeep(c core.IHTTPContext, save func(string) core.IError) error {
	s, err := core.LLM(c).Stream(core.LLMRequest{
		Messages:  []core.LLMMessage{core.LLMUser(c.QueryParam("q"))},
		MaxTokens: 2048,
	})
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	w := c.Response()
	for s.Next() {
		if _, werr := w.Write([]byte(s.Text())); werr != nil {
			break // client gone; the partial answer below is still worth keeping
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	return save(s.Response().Text)
}

// streamThenFinishInBackground is for a generation that must complete even
// though the reader left — a transcript that has to be stored whatever happens.
//
// core.LLM(ctx) is bound to the request, so a disconnect normally cancels the
// generation, which is what you want. WithoutCancel keeps everything the
// context carries (the App above all) while cutting the cancellation wire, and
// the explicit timeout is what stops it from becoming an unbounded goroutine.
func streamThenFinishInBackground(ctx context.Context, question string, save func(string) core.IError) core.IError {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()

	s, err := core.LLM(ctx).WithContext(bg).Stream(core.LLMRequest{
		Messages:  []core.LLMMessage{core.LLMUser(question)},
		MaxTokens: 2048,
	})
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	for s.Next() { //nolint:revive // draining is the point; the deltas go nowhere
	}
	if err := s.Err(); err != nil {
		return err
	}
	return save(s.Response().Text)
}
