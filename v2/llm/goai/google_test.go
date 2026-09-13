package goai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/llm/goai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Everything Gemini-specific is asserted against the outgoing request body
// rather than the reply, because the body is the whole of this driver's job:
// what it emits is either the spelling the model's family takes or a 400. The
// live API rejects the wrong one ("Thinking level is not supported for this
// model"), so a body assertion here is the cheap version of a real call — and
// the integration suite makes the same call for real.

const geminiOK = `{"candidates":[{"content":{"parts":[{"text":"Hello world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`

// geminiGrounded is an answer built from a search, with the citations Gemini
// returns alongside it.
const geminiGrounded = `{"candidates":[{"content":{"parts":[{"text":"Go 1.25 is the latest release."}]},"finishReason":"STOP","groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://go.dev/doc/devel/release","title":"Go Release History"}}]}}],"usageMetadata":{"promptTokenCount":31,"candidatesTokenCount":9}}`

// fakeGemini records the request bodies it is sent and answers with body.
func fakeGemini(t *testing.T, body string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		seen = append(seen, decoded)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func newGemini(t *testing.T, url, model string) core.ILLM {
	t.Helper()
	m, err := goai.NewConfig(goai.Config{
		Provider: "google", Model: model, APIKey: "test-key", BaseURL: url, MaxRetries: 0,
	})
	require.NoError(t, err)
	return m
}

func generationConfig(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	cfg, ok := req["generationConfig"].(map[string]any)
	require.True(t, ok, "the request carries no generationConfig: %v", req)
	return cfg
}

func TestGoogle_reasoningBecomesAThinkingBudgetOn25(t *testing.T) {
	srv, seen := fakeGemini(t, geminiOK)
	m := newGemini(t, srv.URL, "gemini-2.5-flash")

	cases := []struct {
		level  core.LLMReasoning
		budget float64
	}{
		{core.LLMReasoningOff, 0},
		{core.LLMReasoningLow, 2048},
		{core.LLMReasoningMedium, 8192},
		{core.LLMReasoningHigh, 24576},
		{core.LLMReasoningMax, -1}, // dynamic: the model decides
	}
	for _, tc := range cases {
		t.Run(string(tc.level), func(t *testing.T) {
			*seen = nil
			_, err := m.Generate(core.LLMRequest{
				Messages:  []core.LLMMessage{core.LLMUser("hi")},
				Reasoning: tc.level,
			})
			require.NoError(t, err)
			require.Len(t, *seen, 1)

			thinking, ok := generationConfig(t, (*seen)[0])["thinkingConfig"].(map[string]any)
			require.True(t, ok, "a reasoning level must reach the request as a thinkingConfig")
			assert.Equal(t, tc.budget, thinking["thinkingBudget"],
				"raising the level must raise the budget; the caller pays for thinking they asked for")
		})
	}
}

func TestGoogle_reasoningBecomesAThinkingLevelOn3(t *testing.T) {
	// Gemini 3 has no budget at all — it takes a level — so sending 2.5's
	// spelling would be accepted, ignored, and billed as though nothing had been
	// asked for.
	srv, seen := fakeGemini(t, geminiOK)
	m := newGemini(t, srv.URL, "gemini-3-pro-preview")

	_, err := m.Generate(core.LLMRequest{
		Messages:  []core.LLMMessage{core.LLMUser("hi")},
		Reasoning: core.LLMReasoningMax,
	})
	require.NoError(t, err)
	require.Len(t, *seen, 1)

	thinking, ok := generationConfig(t, (*seen)[0])["thinkingConfig"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "high", thinking["thinkingLevel"])
	assert.NotContains(t, thinking, "thinkingBudget", "gemini-3 has no budget to set")
}

func TestGoogle_latestAliasesReasonRatherThanBeingRejected(t *testing.T) {
	// The first version of this matched a list of prefixes, so an alias matched
	// nothing and every request asking to think failed with LLM_UNSUPPORTED —
	// including AI_MODEL=gemini-flash-latest, which our own sample config
	// suggests. Confirmed against the live API: that alias resolves to
	// gemini-3.6-flash, and it honours thinkingLevel (low 2,320 thinking tokens
	// vs high 5,712).
	for _, id := range []string{"gemini-flash-latest", "gemini-flash-lite-latest", "gemini-pro-latest"} {
		t.Run(id, func(t *testing.T) {
			srv, seen := fakeGemini(t, geminiOK)
			m := newGemini(t, srv.URL, id)

			assert.True(t, m.Capabilities().Reasoning,
				"a service branching on this must not be told the model cannot think when it can")

			_, err := m.Generate(core.LLMRequest{
				Messages:  []core.LLMMessage{core.LLMUser("hi")},
				Reasoning: core.LLMReasoningHigh,
			})
			require.NoError(t, err)
			require.Len(t, *seen, 1)

			thinking, ok := generationConfig(t, (*seen)[0])["thinkingConfig"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "high", thinking["thinkingLevel"],
				"an alias always points at a current model, and current models take a level")
		})
	}
}

func TestGoogle_thinkingStyleFollowsTheVersionNotAPrefixList(t *testing.T) {
	// gemini-4 must not reintroduce the alias bug the week it ships, and a
	// non-Gemini model on this provider must still say it cannot think.
	cases := map[string]struct {
		reasons bool
		key     string
	}{
		"gemini-2.5-flash":         {true, "thinkingBudget"},
		"gemini-3-pro-preview":     {true, "thinkingLevel"},
		"gemini-3.1-flash-lite":    {true, "thinkingLevel"},
		"gemini-3.6-flash":         {true, "thinkingLevel"},
		"gemini-4-flash":           {true, "thinkingLevel"},
		"gemini-10-pro":            {true, "thinkingLevel"},
		"gemini-2.0-flash":         {false, ""},
		"gemini-1.5-pro":           {false, ""},
		"gemma-4-31b-it":           {false, ""},
		"some-fine-tuned-endpoint": {false, ""},
	}
	for id, want := range cases {
		t.Run(id, func(t *testing.T) {
			srv, seen := fakeGemini(t, geminiOK)
			m := newGemini(t, srv.URL, id)
			assert.Equal(t, want.reasons, m.Capabilities().Reasoning)

			_, err := m.Generate(core.LLMRequest{
				Messages:  []core.LLMMessage{core.LLMUser("hi")},
				Reasoning: core.LLMReasoningMedium,
			})
			if !want.reasons {
				require.Error(t, err)
				assert.ErrorIs(t, err, core.ErrLLMUnsupported)
				return
			}
			require.NoError(t, err)
			thinking, ok := generationConfig(t, (*seen)[0])["thinkingConfig"].(map[string]any)
			require.True(t, ok)
			assert.Contains(t, thinking, want.key)
		})
	}
}

func TestGoogle_turningThinkingOffOnGemini3IsRejectedNotDropped(t *testing.T) {
	srv, _ := fakeGemini(t, geminiOK)
	m := newGemini(t, srv.URL, "gemini-3-pro-preview")

	_, err := m.Generate(core.LLMRequest{
		Messages:  []core.LLMMessage{core.LLMUser("hi")},
		Reasoning: core.LLMReasoningOff,
	})
	require.Error(t, err, "the model always thinks; a request that believed otherwise is priced wrongly")
	assert.ErrorIs(t, err, core.ErrLLMUnsupported)
}

func TestGoogle_reasoningOnAModelWithoutThinkingIsRejected(t *testing.T) {
	srv, _ := fakeGemini(t, geminiOK)
	m := newGemini(t, srv.URL, "gemini-1.5-flash")

	_, err := m.Generate(core.LLMRequest{
		Messages:  []core.LLMMessage{core.LLMUser("hi")},
		Reasoning: core.LLMReasoningHigh,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrLLMUnsupported)
	assert.False(t, m.Capabilities().Reasoning,
		"a caller must be able to branch on this rather than discovering it as an error")
}

func TestGoogle_capabilitiesReportReasoningForThinkingModels(t *testing.T) {
	srv, _ := fakeGemini(t, geminiOK)
	assert.True(t, newGemini(t, srv.URL, "gemini-2.5-pro").Capabilities().Reasoning)
	assert.True(t, newGemini(t, srv.URL, "gemini-3-pro-preview").Capabilities().Reasoning)
	assert.False(t, newGemini(t, srv.URL, "gemma-3-27b-it").Capabilities().Reasoning)
}

func TestGoogle_providerOptionsWinOverTheMappedReasoning(t *testing.T) {
	// The escape hatch has to be able to override what the mapping produced, or
	// a caller with a newer key than this driver knows about has no way through.
	srv, seen := fakeGemini(t, geminiOK)
	m := newGemini(t, srv.URL, "gemini-2.5-flash")

	_, err := m.Generate(core.LLMRequest{
		Messages:  []core.LLMMessage{core.LLMUser("hi")},
		Reasoning: core.LLMReasoningLow,
		ProviderOptions: map[string]any{
			"google": map[string]any{"safetySettings": []map[string]any{{"category": "HARM_CATEGORY_HARASSMENT"}}},
		},
	})
	require.NoError(t, err)
	require.Len(t, *seen, 1)

	cfg := generationConfig(t, (*seen)[0])
	thinking, ok := cfg["thinkingConfig"].(map[string]any)
	require.True(t, ok, "a namespaced provider option must be merged into the mapped one, not replace it")
	assert.Equal(t, float64(2048), thinking["thinkingBudget"])
	assert.Contains(t, (*seen)[0], "safetySettings", "the caller's own key must survive alongside it")
}

func TestGoogle_searchGroundingReachesTheWireAndCitationsComeBack(t *testing.T) {
	srv, seen := fakeGemini(t, geminiGrounded)
	m := newGemini(t, srv.URL, "gemini-2.5-flash")

	resp, err := m.Generate(core.LLMRequest{
		Messages: []core.LLMMessage{core.LLMUser("what is the latest go release")},
		Tools:    []core.LLMTool{goai.GoogleSearch()},
	})
	require.NoError(t, err)
	require.Len(t, *seen, 1)

	tools, ok := (*seen)[0]["tools"].([]any)
	require.True(t, ok, "a provider-defined tool must be sent as a tool: %v", (*seen)[0])
	require.Len(t, tools, 1)
	assert.Contains(t, tools[0], "googleSearch",
		"Gemini names it googleSearch; anything else is accepted and ignored, so the answer is ungrounded and nothing says so")

	require.Len(t, resp.Sources, 1,
		"an answer built from a search must carry what it read — showing it is a term of the grounding service, not a nicety")
	assert.Equal(t, "https://go.dev/doc/devel/release", resp.Sources[0].URL)
	assert.Equal(t, "Go Release History", resp.Sources[0].Title)
	assert.Equal(t, "url", resp.Sources[0].Type)
}

// geminiGroundedSSE is a grounded answer as Gemini streams it: the text first,
// then the citations, which arrive as their own chunks carrying no text.
var geminiGroundedSSE = []string{
	`{"candidates":[{"content":{"parts":[{"text":"Go 1.26.5 "}]}}]}`,
	`{"candidates":[{"content":{"parts":[{"text":"is the latest release."}]}}]}`,
	`{"candidates":[{"content":{"parts":[]},"finishReason":"STOP","groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://go.dev/doc/devel/release","title":"Go Release History"}},{"web":{"uri":"https://go.dev/blog","title":"The Go Blog"}}]}}],"usageMetadata":{"promptTokenCount":31,"candidatesTokenCount":9}}`,
}

func fakeGeminiStream(t *testing.T, events []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, e := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", e)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGoogle_groundedStreamStillCarriesItsCitations(t *testing.T) {
	// The case that would otherwise force a grounded route to give up streaming:
	// citations arrive as chunks with no text, so a reader that only forwards
	// deltas sees nothing — and an answer shown without its sources breaks the
	// terms of the grounding service, not just the UI.
	m := newGemini(t, fakeGeminiStream(t, geminiGroundedSSE).URL, "gemini-2.5-flash")

	s, err := m.Stream(core.LLMRequest{
		Messages: []core.LLMMessage{core.LLMUser("what is the latest go release")},
		Tools:    []core.LLMTool{goai.GoogleSearch()},
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	var text string
	for s.Next() {
		assert.NotEmpty(t, s.Text(), "a citation chunk carries no text and must not surface as an empty delta")
		text += s.Text()
	}
	require.NoError(t, s.Err())

	final := s.Response()
	assert.Equal(t, "Go 1.26.5 is the latest release.", text)
	require.Len(t, final.Sources, 2, "every source the answer was built on must survive the stream")
	assert.Equal(t, "https://go.dev/doc/devel/release", final.Sources[0].URL)
	assert.Equal(t, "The Go Blog", final.Sources[1].Title)
}

func TestGoogle_groundedStreamAbandonedEarlyDoesNotHang(t *testing.T) {
	// Reading the citations means asking the SDK for its accumulated result,
	// which blocks until its producing goroutine is done. A caller that stops
	// reading — the user navigated away — must still get a Response, not a
	// deadlock against our own unread channel.
	m := newGemini(t, fakeGeminiStream(t, geminiGroundedSSE).URL, "gemini-2.5-flash")

	s, err := m.Stream(core.LLMRequest{
		Messages: []core.LLMMessage{core.LLMUser("what is the latest go release")},
		Tools:    []core.LLMTool{goai.GoogleSearch()},
	})
	require.NoError(t, err)

	require.True(t, s.Next())
	require.NoError(t, s.Close())

	done := make(chan core.LLMResponse, 1)
	go func() { done <- s.Response() }()
	select {
	case resp := <-done:
		t.Logf("sources after an early close: %d", len(resp.Sources))
	case <-time.After(5 * time.Second):
		t.Fatal("Response() blocked after Close — the result was read before the stream finished")
	}
}

func TestGoogle_responseBeforeTheStreamEndsDoesNotBlock(t *testing.T) {
	// Same hazard, reached the other way: a caller that inspects Response()
	// mid-stream (to render usage as it goes) must not deadlock.
	m := newGemini(t, fakeGeminiStream(t, geminiGroundedSSE).URL, "gemini-2.5-flash")

	s, err := m.Stream(core.LLMRequest{Messages: []core.LLMMessage{core.LLMUser("hi")}})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	require.True(t, s.Next())

	done := make(chan core.LLMResponse, 1)
	go func() { done <- s.Response() }()
	select {
	case resp := <-done:
		assert.Empty(t, resp.Sources, "citations are not known until the stream ends; reporting some early would be a guess")
	case <-time.After(5 * time.Second):
		t.Fatal("Response() blocked mid-stream")
	}
}

func TestGoogle_urlContextAndCodeExecutionReachTheWire(t *testing.T) {
	cases := []struct {
		name string
		tool core.LLMTool
		key  string
	}{
		{"url context", goai.URLContext(), "urlContext"},
		{"code execution", goai.CodeExecution(), "codeExecution"},
		{"search restricted to the web", goai.GoogleSearchWebOnly(), "googleSearch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, seen := fakeGemini(t, geminiOK)
			m := newGemini(t, srv.URL, "gemini-2.5-flash")

			_, err := m.Generate(core.LLMRequest{
				Messages: []core.LLMMessage{core.LLMUser("read https://go.dev")},
				Tools:    []core.LLMTool{tc.tool},
			})
			require.NoError(t, err)
			require.Len(t, *seen, 1)

			tools, ok := (*seen)[0]["tools"].([]any)
			require.True(t, ok)
			require.Len(t, tools, 1)
			assert.Contains(t, tools[0], tc.key)
		})
	}
}

// Both kinds of tool must survive into the request — the driver's job ends
// there. Gemini itself currently refuses the combination (2.5 outright; 3.x
// wants tool_config.include_server_side_tool_invocations, which goai v0.9.4
// has no way to send), so this asserts the mapping and not an end-to-end
// capability. When that restriction lifts, this test already covers the wire.
func TestGoogle_providerDefinedToolsMixWithOrdinaryOnes(t *testing.T) {
	srv, seen := fakeGemini(t, geminiOK)
	m := newGemini(t, srv.URL, "gemini-2.5-flash")

	_, err := m.Generate(core.LLMRequest{
		Messages: []core.LLMMessage{core.LLMUser("what is the weather in bangkok")},
		Tools: []core.LLMTool{
			goai.GoogleSearch(),
			{
				Name:        "get_weather",
				Description: "Current weather for a city.",
				Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
				Execute: func(context.Context, json.RawMessage) (string, core.IError) {
					return "hot", nil
				},
			},
		},
		MaxSteps: 3,
	})
	require.NoError(t, err)
	require.Len(t, *seen, 1)

	tools, ok := (*seen)[0]["tools"].([]any)
	require.True(t, ok)
	assert.Len(t, tools, 2, "a search tool and a function tool must both survive the same request")
}
