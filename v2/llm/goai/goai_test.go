package goai_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/llm/goai"
	"github.com/pskclub/mine-core/v2/llm/llmtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zendev-sh/goai/provider"
)

// fakeProvider is an OpenAI-compatible server. Pointing the compat provider at
// it exercises the whole driver — request building, SSE parsing, error mapping —
// with no key, no network and no bill, which is what makes these tests part of
// the unit suite CI actually runs.
func fakeProvider(t *testing.T, status int, body string, stream []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, body)
			return
		}
		if len(stream) > 0 {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			for _, line := range stream {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
				if flusher != nil {
					flusher.Flush()
				}
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const okBody = `{"id":"c1","model":"fake-1","choices":[{"message":{"role":"assistant","content":"Hello world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`

// toolCallBody is a model asking for a tool; afterToolBody is the same model
// answering once it has the result.
const toolCallBody = `{"id":"c3","model":"fake-1","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_secret_code","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":6}}`

const afterToolBody = `{"id":"c4","model":"fake-1","choices":[{"message":{"role":"assistant","content":"The secret code is ZEBRA-42."},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":9}}`

// objectBody is what a provider in JSON mode returns: the object itself as the
// message content.
const objectBody = `{"id":"c2","model":"fake-1","choices":[{"message":{"role":"assistant","content":"{\"ok\":true,\"vendor\":\"ACME\",\"total\":1250.5,\"status\":\"paid\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":8}}`

var okStream = []string{
	`{"choices":[{"delta":{"content":"Hello"},"index":0}]}`,
	`{"choices":[{"delta":{"content":" world"},"index":0}]}`,
	`{"choices":[{"delta":{},"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
}

// dualServer answers both a completion and a stream, so one factory can serve
// every case in the conformance suite.
func dualServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// A provider offered tools asks for one on the first turn and answers on
		// the second, once the result comes back. Detecting the returning result
		// is what lets one stateless handler play both turns.
		if bytes.Contains(body, []byte(`"tools"`)) {
			w.Header().Set("Content-Type", "application/json")
			if bytes.Contains(body, []byte(`"tool"`)) && bytes.Contains(body, []byte("tool_call_id")) {
				_, _ = fmt.Fprint(w, afterToolBody)
			} else {
				_, _ = fmt.Fprint(w, toolCallBody)
			}
			return
		}
		if bytes.Contains(body, []byte("json_schema")) {
			// A provider in JSON mode answers with the object, not prose. The
			// driver must hand that through untouched for the typed layer to
			// unmarshal.
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, objectBody)
			return
		}
		if bytes.Contains(body, []byte(`"stream":true`)) || bytes.Contains(body, []byte(`"stream": true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			for _, line := range okStream {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
				if flusher != nil {
					flusher.Flush()
				}
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, okBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newModel(t *testing.T, baseURL string, opts ...goai.Option) core.ILLM {
	t.Helper()
	opts = append([]goai.Option{
		goai.WithProvider("compat"),
		goai.WithModel("fake-1"),
		goai.WithBaseURL(baseURL),
		goai.WithAPIKey("test-key"),
		// Retries would turn a deliberate 500 into three requests and a slow
		// test; the retry path has its own case below.
		goai.WithMaxRetries(0),
	}, opts...)
	m, err := goai.NewConfig(configFrom(opts...))
	require.NoError(t, err)
	return m
}

func configFrom(opts ...goai.Option) goai.Config {
	cfg := goai.Config{}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// TestConformance is the contract shared with every other driver. If a goai
// upgrade changes streaming, error mapping or usage reporting, this fails here
// rather than in a service.
func TestConformance(t *testing.T) {
	llmtest.RunSuite(t, func(t *testing.T) core.ILLM {
		return newModel(t, dualServer(t).URL)
	})
}

func TestNew_noConfigurationGivesTheDisabledModel(t *testing.T) {
	// A service that never uses AI must still boot. The failure belongs at the
	// call, where it can name what is missing.
	m, err := goai.NewConfig(goai.Config{})
	require.NoError(t, err)
	assert.False(t, m.Enabled())

	_, gerr := m.Generate(core.LLMRequest{Messages: []core.LLMMessage{core.LLMUser("hi")}})
	assert.ErrorIs(t, gerr, core.ErrLLMDisabled)
}

func TestNew_unknownProviderIsAnErrorNotADisabledModel(t *testing.T) {
	// A typo in AI_PROVIDER must not boot into a model that silently refuses
	// every call — that turns a one-line fix into a runtime mystery.
	_, err := goai.NewConfig(goai.Config{Provider: "anthropick", Model: "x"})
	require.Error(t, err)
	assert.Equal(t, "LLM_INVALID_CONFIG", err.GetCode())
	assert.Contains(t, err.GetMessage(), "anthropick")
}

func TestNew_compatWithoutBaseURLIsRejected(t *testing.T) {
	_, err := goai.NewConfig(goai.Config{Provider: "compat", Model: "x"})
	require.Error(t, err, "compat with no address has nowhere to send the request")
	assert.Equal(t, "LLM_INVALID_CONFIG", err.GetCode())
}

func TestGenerate_mapsUsageAndFinishReason(t *testing.T) {
	m := newModel(t, fakeProvider(t, http.StatusOK, okBody, nil).URL)

	resp, err := m.Generate(core.LLMRequest{
		System:   "be brief",
		Messages: []core.LLMMessage{core.LLMUser("hi")},
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello world", resp.Text)
	assert.Equal(t, core.LLMFinishStop, resp.FinishReason)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 5, resp.Usage.OutputTokens)
	assert.Equal(t, "compat", resp.Provider)
	assert.NotNil(t, resp.Raw, "Raw is the escape hatch for anything this struct does not carry")
}

func TestGenerate_errorStatusesMapToDistinctCodes(t *testing.T) {
	// Without this mapping every provider failure is a 500, and a rate limit is
	// indistinguishable from a bad key — so callers retry the one they should
	// not and give up on the one they should.
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusUnauthorized, "LLM_UNAUTHORIZED"},
		{http.StatusNotFound, "LLM_MODEL_NOT_FOUND"},
		{http.StatusTooManyRequests, "LLM_RATE_LIMITED"},
		{http.StatusInternalServerError, "LLM_PROVIDER_ERROR"},
		{http.StatusBadRequest, "LLM_REQUEST_REJECTED"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			srv := fakeProvider(t, tc.status, `{"error":{"message":"nope"}}`, nil)
			m := newModel(t, srv.URL)

			_, err := m.Generate(core.LLMRequest{Messages: []core.LLMMessage{core.LLMUser("hi")}})
			require.Error(t, err)
			assert.Equal(t, tc.code, err.GetCode())
			assert.Equal(t, tc.status, err.GetStatus())
		})
	}
}

func TestGenerate_timeoutIsReportedAsSuch(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)

	m := newModel(t, slow.URL, goai.WithTimeout(50*time.Millisecond))
	_, err := m.Generate(core.LLMRequest{Messages: []core.LLMMessage{core.LLMUser("hi")}})
	require.Error(t, err, "a generation must not outlive the deadline it was given")
	assert.Contains(t, err.Error(), "timed out")
}

func TestStream_deltasAndUsage(t *testing.T) {
	m := newModel(t, dualServer(t).URL)

	s, err := m.Stream(core.LLMRequest{Messages: []core.LLMMessage{core.LLMUser("hi")}})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	var got []string
	for s.Next() {
		got = append(got, s.Text())
	}
	require.NoError(t, s.Err())
	assert.Equal(t, []string{"Hello", " world"}, got)
	assert.Equal(t, "Hello world", s.Response().Text)
	assert.Equal(t, core.LLMFinishStop, s.Response().FinishReason)
	assert.Equal(t, 10, s.Response().Usage.InputTokens)
}

func TestRequest_perRequestModelIsRoutedAndReported(t *testing.T) {
	// Routing a classification to a cheap model and the answer to an expensive
	// one is ordinary. What must not happen is the request going to one model
	// while the response — and therefore every metric and log line — names
	// another.
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, okBody)
	}))
	t.Cleanup(srv.Close)

	m := newModel(t, srv.URL)
	resp, err := m.Generate(core.LLMRequest{
		Model:    "fake-lite",
		Messages: []core.LLMMessage{core.LLMUser("hi")},
	})
	require.NoError(t, err)
	assert.Equal(t, "fake-lite", resp.Model,
		"the response names the model that was actually billed, which is what the metric is tagged with")
	require.Len(t, bodies, 1)
	assert.Contains(t, bodies[0], `"model":"fake-lite"`, "the override must reach the wire, not just the response")

	// The second call must reuse the client built for the first: a handler that
	// routes per request would otherwise build one per request.
	_, err = m.Generate(core.LLMRequest{
		Model:    "fake-lite",
		Messages: []core.LLMMessage{core.LLMUser("again")},
	})
	require.NoError(t, err)
	require.Len(t, bodies, 2)
}

func TestRequest_defaultModelIsUsedWhenTheRequestNamesNone(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, okBody)
	}))
	t.Cleanup(srv.Close)

	m := newModel(t, srv.URL)
	resp, err := m.Generate(core.LLMRequest{Messages: []core.LLMMessage{core.LLMUser("hi")}})
	require.NoError(t, err)
	assert.Equal(t, "fake-1", resp.Model)
	assert.Contains(t, body, `"model":"fake-1"`)
}

func TestRequest_perRequestModelIsRejectedOnACallerBuiltHandle(t *testing.T) {
	// NewModel wraps a client whose key and endpoint live in the caller's own
	// closure. There is nothing here to build a second one from, and guessing
	// would send the request somewhere the caller never configured.
	m := goai.NewModel("ollama", nil, goai.WithModel("llama3"))

	_, err := m.Generate(core.LLMRequest{
		Model:    "some-other-model",
		Messages: []core.LLMMessage{core.LLMUser("hi")},
	})
	require.Error(t, err)
	assert.Equal(t, "LLM_INVALID_REQUEST", err.GetCode())
	assert.Contains(t, err.GetMessage(), "some-other-model")
}

func TestRequest_unsupportedReasoningIsRejectedNotDropped(t *testing.T) {
	// Ollama has no reasoning-level parameter. Sending the request without one
	// would return a perfectly plausible answer that cost a fraction of the
	// thinking the caller asked for, and nothing would say so.
	m := goai.NewModel("ollama", nil)
	_, err := m.Generate(core.LLMRequest{
		Messages:  []core.LLMMessage{core.LLMUser("hi")},
		Reasoning: core.LLMReasoningHigh,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrLLMUnsupported)
}

func TestCapabilities_countTokensSaysNoRatherThanGuessing(t *testing.T) {
	m := newModel(t, dualServer(t).URL)
	assert.False(t, m.Capabilities().TokenCounting)

	_, err := m.CountTokens(core.LLMRequest{Messages: []core.LLMMessage{core.LLMUser("hi")}})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrLLMUnsupported)
}

func TestRequest_attachmentReachesTheWire(t *testing.T) {
	// The failure mode this guards is silent: the SDK skips a part it cannot
	// parse, so a dropped image looks exactly like a model that ignored it.
	// Reading the outgoing body is the only way to tell the two apart.
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, okBody)
	}))
	t.Cleanup(srv.Close)

	m := newModel(t, srv.URL)
	png := []byte{0x89, 0x50, 0x4E, 0x47}

	_, err := m.Generate(core.LLMRequest{
		Messages: []core.LLMMessage{
			core.LLMUser("what is in this picture").With(core.LLMImage(png, "image/png")),
		},
	})
	require.NoError(t, err)

	assert.Contains(t, string(body), "what is in this picture", "the text must survive alongside the image")
	assert.Contains(t, string(body), base64.StdEncoding.EncodeToString(png),
		"the image bytes must arrive base64-encoded, not be dropped on the way")
	assert.Contains(t, string(body), "image/png")
}

func TestRequest_attachmentURLPassesThroughUnencoded(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, okBody)
	}))
	t.Cleanup(srv.Close)

	m := newModel(t, srv.URL)
	_, err := m.Generate(core.LLMRequest{
		Messages: []core.LLMMessage{
			core.LLMUser("describe it").With(core.LLMImageURL("https://example.com/cat.png")),
		},
	})
	require.NoError(t, err)
	assert.Contains(t, string(body), "https://example.com/cat.png")
}

func TestUnwrap_reachesTheDriverClient(t *testing.T) {
	// The escape hatch carries real weight: tool loops, MCP and everything else
	// core deliberately does not wrap are reachable only through it. Asserting
	// non-nil would pass on a value nobody can use — the assertion that matters
	// is that it is the SDK's own model type, because that is what the SDK's
	// entry points take.
	m := newModel(t, dualServer(t).URL)

	raw, ok := m.Unwrap().(provider.LanguageModel)
	require.True(t, ok, "Unwrap must return the driver's model, not an opaque handle")
	require.NotNil(t, raw)
}
