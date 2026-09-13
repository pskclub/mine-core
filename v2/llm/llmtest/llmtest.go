// Package llmtest is the contract every core.ILLM driver must satisfy.
//
// It exists because the framework will have more than one driver, and because
// the library behind the first one is pre-1.0 and ships every few days. A
// conformance suite turns both risks into a test run: a new driver proves it
// behaves like the others before anyone depends on it, and a dependency bump
// that changes behaviour fails here rather than in a service.
//
// A driver's own test file is one function:
//
//	func TestConformance(t *testing.T) {
//	    llmtest.RunSuite(t, func(t *testing.T) core.ILLM { return newTestModel(t) })
//	}
//
// The suite asserts on the contract, never on what a model said — the factory
// may return a driver pointed at a fake server, a local model, or the memory
// model, and all three must pass unchanged.
package llmtest

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Factory builds a model to test. It is called once per case, so a driver whose
// fake server scripts a single reply still works.
type Factory func(t *testing.T) core.ILLM

// RunSuite runs every contract case against the models the factory builds.
func RunSuite(t *testing.T, newModel Factory) {
	t.Helper()

	t.Run("Enabled", func(t *testing.T) { testEnabled(t, newModel) })
	t.Run("Identity", func(t *testing.T) { testIdentity(t, newModel) })
	t.Run("Generate", func(t *testing.T) { testGenerate(t, newModel) })
	t.Run("RejectsEmptyConversation", func(t *testing.T) { testEmptyConversation(t, newModel) })
	t.Run("RejectsUnknownRole", func(t *testing.T) { testUnknownRole(t, newModel) })
	t.Run("RejectsEmptySchema", func(t *testing.T) { testEmptySchema(t, newModel) })
	t.Run("Stream", func(t *testing.T) { testStream(t, newModel) })
	t.Run("StreamCloseIsIdempotent", func(t *testing.T) { testStreamClose(t, newModel) })
	t.Run("StreamCloseBeforeDrain", func(t *testing.T) { testStreamCloseEarly(t, newModel) })
	t.Run("CapabilitiesAreHonest", func(t *testing.T) { testCapabilities(t, newModel) })
	t.Run("PerRequestModelIsHonouredOrRefused", func(t *testing.T) { testPerRequestModel(t, newModel) })
	t.Run("StructuredOutputIsJSON", func(t *testing.T) { testStructuredOutput(t, newModel) })
	t.Run("ToolLoop", func(t *testing.T) { testToolLoop(t, newModel) })
	t.Run("ToolApprovalIsEnforced", func(t *testing.T) { testToolApproval(t, newModel) })
	t.Run("RejectsMalformedAttachment", func(t *testing.T) { testBadAttachment(t, newModel) })
	t.Run("AttachmentSizeIsGuarded", func(t *testing.T) { testAttachmentSize(t, newModel) })
	t.Run("HonoursCancelledContext", func(t *testing.T) { testCancelled(t, newModel) })
}

func hello() core.LLMRequest {
	return core.LLMRequest{Messages: []core.LLMMessage{core.LLMUser("hello")}}
}

func testEnabled(t *testing.T, newModel Factory) {
	m := newModel(t)
	assert.True(t, m.Enabled(),
		"a driver under test is a configured model; the disabled one has its own tests in core")
}

func testIdentity(t *testing.T, newModel Factory) {
	m := newModel(t)
	assert.NotEmpty(t, m.Provider(), "the boot log and every metric are broken down by provider")
	assert.NotEmpty(t, m.Model(), "the boot log names the model this process will be billed for")
}

func testGenerate(t *testing.T, newModel Factory) {
	m := newModel(t)

	resp, err := m.Generate(hello())
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Text, "a successful generation must carry the reply")
	assert.Equal(t, m.Provider(), resp.Provider,
		"the response names who served it, so a metric can be attributed without the caller tracking it")
	assert.NotEmpty(t, resp.FinishReason, "a caller cannot tell a complete answer from a truncated one without this")
	assert.Positive(t, resp.Usage.Total(),
		"usage is what cost accounting is built on; a driver that reports none makes spend invisible")
}

func testEmptyConversation(t *testing.T, newModel Factory) {
	m := newModel(t)

	_, err := m.Generate(core.LLMRequest{})
	require.Error(t, err, "an empty conversation is a caller bug and must not reach the provider")
	assert.Equal(t, "LLM_INVALID_REQUEST", err.GetCode())
	assert.Equal(t, 400, err.GetStatus(),
		"a caller bug must not be reported as a provider failure, or it will be retried forever")
}

func testUnknownRole(t *testing.T, newModel Factory) {
	m := newModel(t)

	_, err := m.Generate(core.LLMRequest{Messages: []core.LLMMessage{{Role: "system", Text: "x"}}})
	require.Error(t, err,
		"the system prompt has its own field; a system role would be silently dropped by some providers")
	assert.Equal(t, "LLM_INVALID_REQUEST", err.GetCode())
}

func testEmptySchema(t *testing.T, newModel Factory) {
	m := newModel(t)

	_, err := m.Generate(core.LLMRequest{
		Messages: []core.LLMMessage{core.LLMUser("hi")},
		Schema:   &core.LLMSchema{Name: "empty"},
	})
	require.Error(t, err, "a schema with no schema would quietly degrade to free text")
	assert.Contains(t, []string{"LLM_INVALID_REQUEST", "LLM_UNSUPPORTED"}, err.GetCode(),
		"either reject the empty schema or say structured output is unsupported — never accept it silently")
}

func testStream(t *testing.T, newModel Factory) {
	m := newModel(t)
	if !m.Capabilities().Streaming {
		t.Skip("driver reports no streaming support")
	}

	s, err := m.Stream(hello())
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	var b strings.Builder
	for s.Next() {
		assert.NotEmpty(t, s.Text(), "an empty delta wastes a caller's loop iteration and should be filtered")
		b.WriteString(s.Text())
	}
	require.NoError(t, s.Err())

	final := s.Response()
	assert.Equal(t, final.Text, b.String(),
		"the accumulated response must equal what the deltas said, or a caller rendering deltas shows something different from one reading the result")
	assert.NotEmpty(t, final.FinishReason, "a stream that ended must say why")
	assert.Positive(t, final.Usage.Total(), "usage arrives at the end of a stream and must be recorded there")
}

func testStreamClose(t *testing.T, newModel Factory) {
	m := newModel(t)
	if !m.Capabilities().Streaming {
		t.Skip("driver reports no streaming support")
	}

	s, err := m.Stream(hello())
	require.NoError(t, err)

	require.NoError(t, s.Close())
	require.NoError(t, s.Close(),
		"a deferred Close alongside an explicit one is the normal shape and must not error")
	assert.False(t, s.Next(), "a closed stream yields nothing further")
}

func testStreamCloseEarly(t *testing.T, newModel Factory) {
	m := newModel(t)
	if !m.Capabilities().Streaming {
		t.Skip("driver reports no streaming support")
	}

	s, err := m.Stream(hello())
	require.NoError(t, err)
	s.Next() // take one delta, then abandon the rest

	// This is the case that leaks: a caller that stops reading because the user
	// navigated away. Close must release the reader rather than leaving a
	// goroutine blocked on a send nobody will receive. Run the suite with -race
	// and a leak check to see it.
	require.NoError(t, s.Close())
}

func testCapabilities(t *testing.T, newModel Factory) {
	m := newModel(t)
	caps := m.Capabilities()

	_, err := m.CountTokens(hello())
	if caps.TokenCounting {
		require.NoError(t, err, "a driver claiming token counting must actually count")
		return
	}
	require.Error(t, err, "a driver that cannot count must say so rather than return a guess")
	assert.ErrorIs(t, err, core.ErrLLMUnsupported,
		"an unsupported capability must be distinguishable from an outage, or callers will retry it")
}

func testPerRequestModel(t *testing.T, newModel Factory) {
	m := newModel(t)

	req := hello()
	req.Model = m.Model() + "-lite"
	resp, err := m.Generate(req)

	// Two answers are correct — route it, or refuse. The third is the one this
	// rules out: quietly generating with the configured model while the caller,
	// the metrics and the bill all say something else.
	//
	// A refusal may come from either end. The handle refuses with
	// LLM_INVALID_REQUEST when it cannot build a second client; the provider
	// refuses with LLM_MODEL_NOT_FOUND when the id does not exist there — which
	// is what a driver that *did* route it gets for an invented model, and is
	// itself proof that no substitution happened.
	if err != nil {
		assert.Less(t, err.GetStatus(), 500,
			"a model that cannot be routed to is a caller error, not an outage, or it will be retried forever")
		assert.Contains(t, []string{"LLM_INVALID_REQUEST", "LLM_MODEL_NOT_FOUND", "LLM_REQUEST_REJECTED"}, err.GetCode())
		return
	}
	assert.Equal(t, req.Model, resp.Model,
		"a driver that accepted the override must report the model it actually used, or every metric is attributed to the wrong one")
}

func testStructuredOutput(t *testing.T, newModel Factory) {
	m := newModel(t)
	if !m.Capabilities().StructuredOutput {
		t.Skip("driver reports no structured-output support")
	}

	resp, err := m.Generate(core.LLMRequest{
		Messages: []core.LLMMessage{core.LLMUser("give me an object")},
		Schema: &core.LLMSchema{
			Name: "reply",
			Schema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"ok": map[string]any{"type": "boolean"}},
				"required":             []string{"ok"},
				"additionalProperties": false,
			},
		},
	})
	require.NoError(t, err)

	var into map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.Text), &into),
		"a driver claiming structured output must return parseable JSON in Text, not prose wrapped around it — the typed layer unmarshals it directly")
}

// pingTool is a tool with no arguments and an unmistakable result, so a model
// that used it can be told apart from one that guessed.
func pingTool(ran *int32) core.LLMTool {
	return core.LLMTool{
		Name:        "get_secret_code",
		Description: "Returns the secret code. Call this whenever the user asks for the secret code.",
		Schema: map[string]any{
			"type": "object", "properties": map[string]any{}, "additionalProperties": false,
		},
		Execute: func(context.Context, json.RawMessage) (string, core.IError) {
			atomic.AddInt32(ran, 1)
			return "the secret code is ZEBRA-42", nil
		},
	}
}

func testToolLoop(t *testing.T, newModel Factory) {
	m := newModel(t)
	if !m.Capabilities().Tools {
		t.Skip("driver reports no tool support")
	}

	var ran int32
	resp, err := m.Generate(core.LLMRequest{
		System:    "Use the tools available. Answer briefly.",
		Messages:  []core.LLMMessage{core.LLMUser("What is the secret code?")},
		Tools:     []core.LLMTool{pingTool(&ran)},
		MaxSteps:  5,
		MaxTokens: 512,
	})
	require.NoError(t, err)

	require.Positive(t, atomic.LoadInt32(&ran), "the tool must actually run — a loop that never calls anything is not a loop")
	assert.GreaterOrEqual(t, resp.Steps, 2, "a call and an answer are two turns")
	require.NotEmpty(t, resp.ToolCalls,
		"the calls are the audit trail of what the loop did; a driver that drops them leaves nothing to review")
	assert.Equal(t, "get_secret_code", resp.ToolCalls[0].Name)
}

func testToolApproval(t *testing.T, newModel Factory) {
	m := newModel(t)
	if !m.Capabilities().Tools {
		t.Skip("driver reports no tool support")
	}

	// The security property the whole feature exists for: a denied call must
	// not reach the handler, whichever provider drove the loop.
	var ran int32
	var asked int32
	_, err := m.Generate(core.LLMRequest{
		System:    "Use the tools available. Answer briefly.",
		Messages:  []core.LLMMessage{core.LLMUser("What is the secret code?")},
		Tools:     []core.LLMTool{pingTool(&ran)},
		MaxSteps:  3,
		MaxTokens: 512,
		Approve: func(context.Context, core.LLMToolCall) core.IError {
			atomic.AddInt32(&asked, 1)
			return core.New(403, "DENIED", "not allowed in this test")
		},
	})
	require.NoError(t, err, "a denial is not a failed generation")
	require.Positive(t, atomic.LoadInt32(&asked), "the gate must be consulted before the tool runs")
	assert.Zero(t, atomic.LoadInt32(&ran), "a denied call must never reach the handler")
}

func testBadAttachment(t *testing.T, newModel Factory) {
	m := newModel(t)
	if !m.Capabilities().Vision {
		t.Skip("driver reports it cannot send attachments")
	}

	// The failure this guards against is specific: the layer underneath drops a
	// part it cannot parse, the message goes out with a hole where the picture
	// should be, and the model answers about the text alone as though nothing
	// were missing. A rejection is the only outcome a caller can act on.
	cases := []struct {
		name string
		part core.LLMPart
	}{
		{"no data and no url", core.LLMPart{Type: core.LLMPartImage}},
		{"data without a media type", core.LLMPart{Type: core.LLMPartImage, Data: []byte{1, 2, 3}}},
		{"unknown type", core.LLMPart{Type: "video", Data: []byte{1}, MediaType: "video/mp4"}},
		{"both data and url", core.LLMPart{
			Type: core.LLMPartImage, Data: []byte{1}, MediaType: "image/png", URL: "https://x/y.png",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.Generate(core.LLMRequest{
				Messages: []core.LLMMessage{core.LLMUser("what is this").With(tc.part)},
			})
			require.Error(t, err)
			assert.Equal(t, "LLM_INVALID_REQUEST", err.GetCode())
			assert.Equal(t, 400, err.GetStatus(),
				"a malformed attachment is a caller bug and must not be reported as a provider failure")
		})
	}
}

func testAttachmentSize(t *testing.T, newModel Factory) {
	m := newModel(t)
	if !m.Capabilities().Vision {
		t.Skip("driver reports it cannot send attachments")
	}

	// Failing here names the problem. Failing at the provider returns a size
	// error against a request that also carried a prompt, several seconds and
	// one billed round trip later.
	huge := make([]byte, core.LLMMaxAttachmentBytes+1)
	_, err := m.Generate(core.LLMRequest{
		Messages: []core.LLMMessage{
			core.LLMUser("read this").With(core.LLMImage(huge, "image/png")),
		},
	})
	require.Error(t, err)
	assert.Equal(t, "LLM_ATTACHMENT_TOO_LARGE", err.GetCode())
	assert.Equal(t, 413, err.GetStatus())
}

func testCancelled(t *testing.T, newModel Factory) {
	m := newModel(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.WithContext(ctx).Generate(hello())
	require.Error(t, err,
		"a handle bound to a dead request must not keep spending tokens on an answer nobody will read")
}
