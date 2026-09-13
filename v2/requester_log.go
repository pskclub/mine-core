package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

// defaultSlowRequest is the duration above which an outgoing call is reported as
// slow even when call logging is otherwise off.
//
// A second is a long time to wait on somebody else's service inside a request
// somebody is waiting on. It is deliberately far above the DB's 200ms: a network
// round trip is not a query, and a threshold that fires on every healthy call
// teaches people to ignore the line.
const defaultSlowRequest = time.Second

// maxLoggedBody caps how much of a body reaches the log. Enough to see the shape
// of a payload and the message in an error, short of turning one call into a
// screenful.
const maxLoggedBody = 2048

// httpLogLevel is how much of the outgoing traffic is written, mirroring the
// levels the SQL logger uses.
type httpLogLevel uint8

const (
	httpLogSilent httpLogLevel = iota
	httpLogError
	httpLogWarn
	httpLogAll
)

// httpLogLevelFrom decides how much is logged: HTTP_LOG_LEVEL when set,
// otherwise it follows LOG_LEVEL — debug means every call, anything else keeps
// the log to slow calls and failures.
//
// The separate key exists for the same reason DB_LOG_LEVEL does: turning the
// application up to debug to read one flow should not have to bury it under
// every call a client makes, and a service happy at info may still want the
// calls while an integration is being written.
func httpLogLevelFrom(env IENV) httpLogLevel {
	if env == nil {
		return httpLogWarn
	}
	if level, ok := parseHTTPLogLevel(env.Config().HTTPLogLevel); ok {
		return level
	}
	switch parseLevel(env.Config().LogLevel) {
	case slog.LevelDebug:
		return httpLogAll
	case slog.LevelError:
		return httpLogError
	default:
		return httpLogWarn
	}
}

// parseHTTPLogLevel reads HTTP_LOG_LEVEL. "off"/"false" are accepted alongside
// "silent" because the setting is reached for as a switch.
func parseHTTPLogLevel(s string) (httpLogLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "silent", "off", "none", "false":
		return httpLogSilent, true
	case "error":
		return httpLogError, true
	case "warn", "warning":
		return httpLogWarn, true
	case "info", "debug", "all", "true":
		return httpLogAll, true
	default:
		return 0, false
	}
}

// instrumentRequesterLog records every outgoing call on the application logger,
// as the GORM logger does for every statement.
//
// Without it an outgoing call left no trace anywhere but Sentry — and only when
// a DSN was configured, and only attached to an event, so a service calling a
// dependency that had gone slow showed nothing at all in its own log. This is
// the missing half of "what did we ask, and what came back".
//
// The line lands on the logger of the request or job that made the call, so it
// carries the same request_id as everything else in that unit of work.
func instrumentRequesterLog(r IRequester, log ILogger, level httpLogLevel, slow time.Duration, logBody bool) {
	if r == nil || log == nil || level == httpLogSilent {
		return
	}
	client, ok := r.(interface{ Resty() *resty.Client })
	if !ok || client.Resty() == nil {
		return
	}
	if slow <= 0 {
		slow = defaultSlowRequest
	}

	rc := client.Resty()
	// the same key list the Sentry scrubber uses, so a token is masked wherever
	// it appears rather than in whichever destination somebody remembered
	scrub := newScrubber(nil, nil)

	// exactly one of these runs per Execute, so a call is never logged twice
	rc.OnAfterResponse(func(_ *resty.Client, resp *resty.Response) error {
		logCall(log, level, slow, logBody, scrub, resp.Request, resp, resp.StatusCode(), resp.Time(), nil)
		return nil
	})

	rc.OnError(func(req *resty.Request, err error) {
		status, elapsed := 0, time.Duration(0)
		var resp *resty.Response
		if respErr, ok := err.(*resty.ResponseError); ok && respErr.Response != nil {
			resp = respErr.Response
			status = resp.StatusCode()
			elapsed = resp.Time()
		}
		logCall(log, level, slow, logBody, scrub, req, resp, status, elapsed, err)
	})
}

// logCall writes one line for one call, choosing the level the way the SQL
// logger does: failures, then slow, then — only when asked for all of it —
// everything else.
func logCall(
	log ILogger, level httpLogLevel, slow time.Duration, logBody bool,
	scrub *scrubber, req *resty.Request, resp *resty.Response,
	status int, elapsed time.Duration, err error,
) {
	if req == nil {
		return
	}

	// method and url first: they are what the line is about, and a fixed
	// position lets the eye run down a column of calls
	fields := []any{
		"method", req.Method,
		"url", scrubURL(req.URL),
		"duration_ms", elapsed.Milliseconds(),
		// what to filter on to get every outgoing call, and the breadcrumb
		// category Sentry files the line under
		"component", "http.client",
	}
	if status > 0 {
		fields = append(fields, "status", status)
	}
	if logBody {
		fields = append(fields, bodyFields(scrub, req, resp)...)
	}

	msg := callMessage(req.Method, req.URL, status, elapsed)

	// Two markers, for the same reason the access line carries them. No capture:
	// a failed call is reported by the layer that returned an error for it, with
	// the stack and the scope that make it actionable — this line would only add
	// a second, poorer issue. No breadcrumb: the Sentry client hook already
	// leaves a typed http one for every call, and two entries per call make a
	// trail unreadable.
	at := loggerAt(log, withoutBreadcrumb(withoutCapture(req.Context())))

	switch {
	case err != nil && level >= httpLogError:
		at.Error(msg, append(fields, "err", err)...)

	// Their 5xx is a dependency being down, which is ours to notice. A 4xx is an
	// answer — "no such user", "already exists" — and the service that asked
	// decides what it means, so it stays at debug rather than crying wolf on
	// every miss.
	case status >= 500 && level >= httpLogError:
		at.Error(msg, fields...)

	case elapsed >= slow && level >= httpLogWarn:
		at.Warn(msg, append(fields, "threshold_ms", slow.Milliseconds())...)

	case level >= httpLogAll:
		at.Debug(msg, fields...)
	}
}

// callMessage is the line's headline: "GET api.example.com/v1/rates 200 142ms".
//
// The message has to say what happened on its own — a JSON pipeline shows it as
// the headline, and Sentry Logs shows nothing else until the line is opened.
// "http call" answers none of the questions being scanned for: who was called,
// for what, did it work, how slow was it. The same values stay in the fields,
// which is what you filter on. Same shape as the incoming access line, so both
// halves of a request read alike.
func callMessage(method, rawURL string, status int, elapsed time.Duration) string {
	endpoint := callEndpoint(rawURL)
	if status == 0 {
		// never got an answer — the err attribute says why
		return fmt.Sprintf("%s %s failed", method, endpoint)
	}
	return fmt.Sprintf("%s %s %d %dms", method, endpoint, status, elapsed.Milliseconds())
}

// callEndpoint is a URL's host and path — no scheme, no query, no credentials.
//
// Enough to know who was called and for what, short enough to read in a list,
// and stable: keeping the query out means Sentry groups every call to an
// endpoint together instead of fragmenting on parameters, and no secret can
// reach a headline that is displayed everywhere.
func callEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return scrubURL(raw)
	}
	return u.Host + u.Path
}

// bodyFields adds what was sent and what came back, scrubbed and truncated.
//
// Off by default: a body carries credentials and personal data, is frequently
// large, and is the single easiest way to turn a log store into a place secrets
// live. It is worth turning on while an integration is being written, and worth
// turning off again afterwards.
func bodyFields(scrub *scrubber, req *resty.Request, resp *resty.Response) []any {
	out := make([]any, 0, 4)

	if b := requestBody(req); len(b) > 0 {
		out = append(out, "req_body", truncateBody(scrub.scrubJSON(b)))
	}
	// nil after SetDoNotParseResponse — the body was never read, and reading it
	// here would consume the stream the caller is about to use
	if resp != nil && len(resp.Body()) > 0 {
		out = append(out, "res_body", truncateBody(scrub.scrubJSON(resp.Body())))
	}

	return out
}

// requestBody renders whatever was handed to SetBody. A multipart upload keeps
// its file parts out of Body, so this never dumps a file into the log.
func requestBody(req *resty.Request) []byte {
	switch b := req.Body.(type) {
	case nil:
		return nil
	case []byte:
		return b
	case string:
		return []byte(b)
	default:
		encoded, err := json.Marshal(b)
		if err != nil {
			return nil
		}
		return encoded
	}
}

func truncateBody(b []byte) string {
	if len(b) <= maxLoggedBody {
		return string(b)
	}
	return string(b[:maxLoggedBody]) + "…(truncated)"
}

// loggerAt binds the logger to the context the call was made on, so the line
// carries that request's or job's identity. Same shape as the SQL logger's at().
func loggerAt(log ILogger, ctx context.Context) ILogger {
	// blameApp for the same reason as the SQL logger: resty calls this from a
	// middleware, so the line that made the call is still on the stack and is
	// what the reader is looking for — not requester_log.go
	log = blameApp(log)
	if l, ok := log.(*logger); ok && ctx != nil {
		return l.withContext(ctx)
	}
	return log
}
