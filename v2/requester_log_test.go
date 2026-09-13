package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// calls is what the requester wrote, in order — found the way an operator finds
// them, by the component attribute rather than by the wording of a message.
func (c captureLogger) calls() []capturedLine {
	out := make([]capturedLine, 0)
	for _, l := range *c.lines {
		if v, ok := field(l, "component"); ok && v == "http.client" {
			out = append(out, l)
		}
	}
	return out
}

func field(l capturedLine, key string) (any, bool) {
	for i := 0; i+1 < len(l.args); i += 2 {
		if k, ok := l.args[i].(string); ok && k == key {
			return l.args[i+1], true
		}
	}
	return nil, false
}

// loggedRequester wires the call log onto a requester, the way NewApp does.
func loggedRequester(t *testing.T, level httpLogLevel, slow time.Duration, body bool) (IRequester, captureLogger) {
	t.Helper()

	log, _ := newCapture()
	r := NewRequesterWithClient(resty.New())
	instrumentRequesterLog(r, log, level, slow, body)

	return r, log
}

// The whole point: a call is visible without Sentry configured.
func TestRequesterLog_writesOneLinePerCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	r, log := loggedRequester(t, httpLogAll, 0, false)
	_, err := r.Send(r.R(), http.MethodGet, srv.URL+"/things")
	require.Nil(t, err)

	lines := log.calls()
	require.Len(t, lines, 1, "exactly one line per call, never two")

	assert.Equal(t, "debug", lines[0].level)
	// the message says what happened on its own: Sentry Logs and a JSON pipeline
	// show nothing else until the line is opened
	assert.Regexp(t, `^GET 127\.0\.0\.1:\d+/things 200 \d+ms$`, lines[0].msg)

	method, _ := field(lines[0], "method")
	assert.Equal(t, http.MethodGet, method)
	status, _ := field(lines[0], "status")
	assert.Equal(t, http.StatusOK, status)
	url, _ := field(lines[0], "url")
	assert.Contains(t, url, "/things")
	_, hasDuration := field(lines[0], "duration_ms")
	assert.True(t, hasDuration)
}

// Their 5xx is a dependency being down — ours to notice. Their 4xx is an answer,
// and the service that asked decides what it means.
func TestRequesterLog_levelByOutcome(t *testing.T) {
	cases := []struct {
		name   string
		status int
		level  string
	}{
		{"success", http.StatusOK, "debug"},
		{"refused by the upstream", http.StatusNotFound, "debug"},
		{"upstream is broken", http.StatusInternalServerError, "error"},
		{"upstream gateway", http.StatusBadGateway, "error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			r, log := loggedRequester(t, httpLogAll, 0, false)
			_, _ = r.Send(r.R(), http.MethodGet, srv.URL)

			lines := log.calls()
			require.Len(t, lines, 1)
			assert.Equal(t, tc.level, lines[0].level)
			assert.Contains(t, lines[0].msg, strconv.Itoa(tc.status),
				"the status is in the headline, not only in the fields")
		})
	}
}

// A dependency that has gone slow is the case this exists for: nothing else
// reports it, and it is invisible in the request line, which only shows the
// total.
func TestRequesterLog_slowCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(60 * time.Millisecond)
	}))
	defer srv.Close()

	// warn only: a healthy call writes nothing at this level, a slow one still does
	r, log := loggedRequester(t, httpLogWarn, 20*time.Millisecond, false)
	_, err := r.Send(r.R(), http.MethodGet, srv.URL)
	require.Nil(t, err)

	lines := log.calls()
	require.Len(t, lines, 1)
	assert.Equal(t, "warn", lines[0].level)
	// the duration is in the headline, so a slow call reads as one at a glance
	assert.Regexp(t, `^GET 127\.0\.0\.1:\d+ 200 \d+ms$`, lines[0].msg)
	threshold, _ := field(lines[0], "threshold_ms")
	assert.Equal(t, int64(20), threshold)
}

func TestRequesterLog_transportFailure(t *testing.T) {
	r, log := loggedRequester(t, httpLogError, 0, false)
	_, err := r.Send(r.R(), http.MethodGet, "http://127.0.0.1:0")
	require.NotNil(t, err)

	lines := log.calls()
	require.Len(t, lines, 1)
	assert.Equal(t, "error", lines[0].level)
	assert.Contains(t, lines[0].msg, "failed", "a call that got no answer says so")
	_, hasErr := field(lines[0], "err")
	assert.True(t, hasErr, "the transport error is on the line")
}

// Silent means silent — including the failures.
func TestRequesterLog_silent(t *testing.T) {
	r, log := loggedRequester(t, httpLogSilent, 0, false)
	_, _ = r.Send(r.R(), http.MethodGet, "http://127.0.0.1:0")

	assert.Empty(t, log.calls())
}

// A credential in the URL is a credential in the log store, which more people
// can read than the database.
func TestRequesterLog_scrubsTheURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	r, log := loggedRequester(t, httpLogAll, 0, false)
	_, err := r.Send(r.R().SetQueryParam("api_key", "super-secret"), http.MethodGet, srv.URL)
	require.Nil(t, err)

	url, _ := field(log.calls()[0], "url")
	assert.NotContains(t, url, "super-secret")
}

// Bodies are opt-in, and even then a password never reaches the log.
func TestRequesterLog_bodiesAreOptInAndScrubbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"u1","token":"leaked-token"}`))
	}))
	defer srv.Close()

	t.Run("off by default", func(t *testing.T) {
		r, log := loggedRequester(t, httpLogAll, 0, false)
		_, err := r.Send(r.R().SetBody(map[string]any{"email": "a@b.co"}), http.MethodPost, srv.URL)
		require.Nil(t, err)

		_, hasReq := field(log.calls()[0], "req_body")
		_, hasRes := field(log.calls()[0], "res_body")
		assert.False(t, hasReq)
		assert.False(t, hasRes)
	})

	t.Run("on, and scrubbed", func(t *testing.T) {
		r, log := loggedRequester(t, httpLogAll, 0, true)
		_, err := r.Send(
			r.R().SetBody(map[string]any{"email": "a@b.co", "password": "hunter2"}),
			http.MethodPost, srv.URL,
		)
		require.Nil(t, err)

		line := log.calls()[0]

		reqBody, ok := field(line, "req_body")
		require.True(t, ok)
		assert.Contains(t, reqBody, "a@b.co", "what was sent is readable")
		assert.NotContains(t, reqBody, "hunter2", "the password is not")

		resBody, ok := field(line, "res_body")
		require.True(t, ok)
		assert.Contains(t, resBody, "u1")
		assert.NotContains(t, resBody, "leaked-token")
	})
}

// A file upload must not put the file in the log, whatever the body setting is.
func TestRequesterLog_multipartBodyIsNotDumped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	r, log := loggedRequester(t, httpLogAll, 0, true)
	_, err := r.Send(
		r.R().SetFileReader("doc", "id.png", strings.NewReader("PNG-BYTES-PRETEND-BINARY")),
		http.MethodPost, srv.URL,
	)
	require.Nil(t, err)

	reqBody, ok := field(log.calls()[0], "req_body")
	if ok {
		assert.NotContains(t, reqBody, "PNG-BYTES")
	}
}

func TestHTTPLogLevel_followsLogLevelUnlessOverridden(t *testing.T) {
	cases := []struct {
		name     string
		logLevel string
		override string
		want     httpLogLevel
	}{
		{"debug logs every call", "debug", "", httpLogAll},
		{"info keeps slow and failed", "info", "", httpLogWarn},
		{"error keeps failures only", "error", "", httpLogError},
		{"override wins", "debug", "silent", httpLogSilent},
		{"override off", "debug", "off", httpLogSilent},
		{"override up", "info", "debug", httpLogAll},
		{"nonsense falls back", "info", "loud", httpLogWarn},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := mustEnv(t, map[string]string{
				"ENV":            "test",
				"LOG_LEVEL":      tc.logLevel,
				"HTTP_LOG_LEVEL": tc.override,
			})

			assert.Equal(t, tc.want, httpLogLevelFrom(env))
		})
	}
}

// The line has to belong to the request that made the call, or nothing joins the
// two when somebody is reading a burst of failures.
//
// This also covers the wiring: NewApp attaches the call log, with no Sentry DSN
// in sight.
func TestRequesterLog_carriesTheCallersRequestID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	var buf strings.Builder
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "test-svc", "LOG_LEVEL": "debug"})
	app, err := NewApp(env, WithLogger(NewLoggerTo(&buf, env)))
	require.Nil(t, err)

	// what the request-id middleware puts there on a real request
	ctx := app.NewContext(context.WithValue(t.Context(), requestIDKey, "req-abc"), ModeHTTP)

	r := Requester(ctx)
	_, sErr := r.Send(r.R(), http.MethodGet, srv.URL)
	require.Nil(t, sErr)

	out := buf.String()
	assert.Contains(t, out, `"component":"http.client"`)
	assert.Contains(t, out, "req-abc", "the call is joined to the request that made it")
}

// A failed call must not file an issue of its own. The service that made it
// returns an error, and that error is captured where the stack and the scope
// make it actionable — this line would only add a second, worse issue titled
// after a URL.
//
// The breadcrumb is the same argument: the Sentry client hook already leaves a
// typed http one per call, so the log line must not leave a second. Run through
// a real request, because that is what installs the hub both of them reach.
func TestRequesterLog_doesNotDoubleReportToSentry(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	app, rec := newSentryApp(t, map[string]string{"LOG_LEVEL": "debug"})
	e := NewHTTPServer(app, nil)
	e.GET("/charge", func(c IHTTPContext) error {
		r := Requester(c)
		if _, err := r.Send(r.R(), http.MethodGet, upstream.URL+"/v1/charges"); err != nil {
			// what a service does: translate, and let this be the reported failure
			return New(http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", "charge failed")
		}
		return c.JSON(http.StatusOK, "ok")
	})

	rr := httptest.NewRecorder()
	e.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/charge", nil))
	require.Equal(t, http.StatusBadGateway, rr.Code)

	require.Equal(t, 1, rec.Len(),
		"one issue — the handler's. The call log must not file its own")

	calls := 0
	for _, c := range rec.Last().Breadcrumbs {
		if strings.Contains(c.Message, "/v1/charges") {
			calls++
		}
	}
	assert.Equal(t, 1, calls, "exactly one breadcrumb per outgoing call, not two")
}
