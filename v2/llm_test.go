package core

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func llmTestApp(t *testing.T, opts ...Option) *App {
	t.Helper()
	app, err := NewApp(mustEnv(t, map[string]string{"ENV": "test"}), opts...)
	require.NoError(t, err, "an App with no external connections must build")
	return app
}

func TestLLM_disabledWithoutProvider(t *testing.T) {
	app := llmTestApp(t)
	ctx := app.NewContext(context.Background(), ModeTest)

	l := LLM(ctx)
	require.NotNil(t, l, "LLM must never be nil: a call site should not have to nil-check")
	assert.False(t, l.Enabled())

	_, err := l.Generate(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})
	require.Error(t, err, "a generation with no provider must fail rather than return an empty answer")
	assert.Equal(t, "LLM_DISABLED", err.GetCode())
	assert.Equal(t, 503, err.GetStatus())
	assert.ErrorIs(t, err, ErrLLMDisabled, "callers branch on the sentinel, not the code string")
}

func TestLLM_reachableFromAPlainContext(t *testing.T) {
	// A context that never came from an App — a script, an early test — must
	// still return a usable handle rather than nil.
	l := LLM(context.Background())
	require.NotNil(t, l)
	assert.False(t, l.Enabled())
	_, err := l.CountTokens(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})
	assert.ErrorIs(t, err, ErrLLMDisabled)
}

func TestLLM_memoryRecordsRequestsAndReturnsScriptedReplies(t *testing.T) {
	m := NewMemoryLLM("Bangkok", "Chiang Mai")
	app := llmTestApp(t, WithLLM(m))
	ctx := app.NewContext(context.Background(), ModeTest)

	first, err := LLM(ctx).Generate(LLMRequest{
		System:   "answer in one word",
		Messages: []LLMMessage{LLMUser("capital of Thailand?")},
	})
	require.NoError(t, err)
	assert.Equal(t, "Bangkok", first.Text)
	assert.Equal(t, LLMFinishStop, first.FinishReason)
	assert.Positive(t, first.Usage.InputTokens,
		"usage must be populated so cost accounting has something to record")

	second, err := LLM(ctx).Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("and the north?")},
	})
	require.NoError(t, err)
	assert.Equal(t, "Chiang Mai", second.Text, "replies are consumed in order")

	calls := LLMCalls(m)
	require.Len(t, calls, 2,
		"the memory model must stay findable through the instrumentation NewApp wraps it in")
	assert.Equal(t, "answer in one word", calls[0].System)
	assert.Equal(t, "capital of Thailand?", calls[0].Messages[0].Text)
}

func TestLLM_memoryEchoesWhenNothingScripted(t *testing.T) {
	m := NewMemoryLLM()
	resp, err := m.Generate(LLMRequest{Messages: []LLMMessage{LLMUser("echo me")}})
	require.NoError(t, err, "a test that does not care what the model said needs no setup")
	assert.Equal(t, "echo me", resp.Text)
}

func TestLLM_memoryExhaustedRepliesFailLoudly(t *testing.T) {
	m := NewMemoryLLM("only one")
	req := LLMRequest{Messages: []LLMMessage{LLMUser("hi")}}

	_, err := m.Generate(req)
	require.NoError(t, err)

	_, err = m.Generate(req)
	require.Error(t, err,
		"calling more often than the test scripted is a behaviour change, not something to paper over")
	assert.Equal(t, "LLM_MEMORY_EXHAUSTED", err.GetCode())
}

func TestLLM_memoryScriptedError(t *testing.T) {
	m := NewMemoryLLM()
	QueueLLMReply(m, MemoryLLMReply{Err: New(429, "RATE_LIMITED", "slow down")})

	_, err := m.Generate(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})
	require.Error(t, err, "a provider outage must be reproducible without one")
	assert.Equal(t, "RATE_LIMITED", err.GetCode())
}

func TestLLM_memoryReportsScriptedUsage(t *testing.T) {
	m := NewMemoryLLM()
	QueueLLMReply(m, MemoryLLMReply{
		Text:  "ok",
		Usage: LLMUsage{InputTokens: 100, OutputTokens: 7, CachedInputTokens: 900},
	})

	resp, err := m.Generate(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})
	require.NoError(t, err)
	assert.Equal(t, 900, resp.Usage.CachedInputTokens,
		"cached input is billed differently and must stay a separate number")
	assert.Equal(t, 1007, resp.Usage.Total())
}

func TestLLM_streamDeltasAssembleIntoTheReply(t *testing.T) {
	m := NewMemoryLLM("the quick brown fox")

	s, err := m.Stream(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	var b strings.Builder
	deltas := 0
	for s.Next() {
		b.WriteString(s.Text())
		deltas++
	}
	require.NoError(t, s.Err())
	assert.Equal(t, "the quick brown fox", b.String(),
		"concatenated deltas must equal the whole reply")
	assert.Greater(t, deltas, 1,
		"a stream arriving in one piece would not exercise a caller's assembly")
	assert.Positive(t, s.Response().Usage.OutputTokens,
		"usage is only complete once the stream has ended")
}

func TestLLM_streamCloseIsIdempotent(t *testing.T) {
	m := NewMemoryLLM("hello there")
	s, err := m.Stream(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})
	require.NoError(t, err)

	require.NoError(t, s.Close())
	require.NoError(t, s.Close(),
		"a deferred Close plus an explicit one is normal and must not error")
	assert.False(t, s.Next(), "a closed stream yields nothing further")
}

func TestLLM_requestValidation(t *testing.T) {
	m := NewMemoryLLM()

	_, err := m.Generate(LLMRequest{})
	require.Error(t, err, "an empty conversation is a caller bug, not a provider error")
	assert.Equal(t, "LLM_INVALID_REQUEST", err.GetCode())
	assert.Equal(t, 400, err.GetStatus())

	_, err = m.Generate(LLMRequest{Messages: []LLMMessage{{Role: "system", Text: "x"}}})
	require.Error(t, err,
		"the system prompt has its own field; a system role would be dropped by some drivers")
	assert.Equal(t, "LLM_INVALID_REQUEST", err.GetCode())

	_, err = m.Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("hi")},
		Schema:   &LLMSchema{Name: "empty"},
	})
	require.Error(t, err, "a schema with no schema would silently degrade to free text")
	assert.Equal(t, "LLM_INVALID_REQUEST", err.GetCode())
}

func TestLLM_cancelledContextStopsGeneration(t *testing.T) {
	app := llmTestApp(t, WithLLM(NewMemoryLLM("never sent")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := LLM(app.NewContext(ctx, ModeTest)).
		Generate(LLMRequest{Messages: []LLMMessage{LLMUser("hi")}})
	require.Error(t, err, "a handle must honour the deadline of the request it was bound to")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestLLM_unsupportedErrorIsIdenticalAcrossDrivers(t *testing.T) {
	err := LLMUnsupportedError("openai", "token counting")
	assert.Equal(t, "LLM_UNSUPPORTED", err.GetCode())
	assert.Equal(t, 400, err.GetStatus())
	assert.ErrorIs(t, err, ErrLLMUnsupported,
		"a driver that cannot do something must be distinguishable from one that is not configured")
	assert.Contains(t, err.GetMessage(), "openai")
}

func TestLLM_appModelIsNeverNil(t *testing.T) {
	app := llmTestApp(t)
	require.NotNil(t, app.LLMModel())
	assert.False(t, app.LLMModel().Enabled())

	withModel := llmTestApp(t, WithLLM(NewMemoryLLM()))
	assert.True(t, withModel.LLMModel().Enabled())
	assert.Equal(t, "memory", withModel.LLMModel().Provider())
}

func TestLLM_disabledModelClaimsNoCapabilities(t *testing.T) {
	// The disabled model must not advertise what it cannot deliver, so code
	// branching on Capabilities takes the same path as code checking Enabled.
	caps := NewNoopLLM().Capabilities()
	assert.False(t, caps.Streaming)
	assert.False(t, caps.StructuredOutput)
	assert.False(t, caps.TokenCounting)
}

func TestLLM_bootLogNamesProviderAndModel(t *testing.T) {
	// "llm=true" would not answer the question anyone actually has at boot,
	// which is which model this process will be billed for.
	assert.Equal(t, false, llmBootField(nil))
	assert.Equal(t, false, llmBootField(NewNoopLLM()))
	assert.Equal(t, "memory/memory", llmBootField(NewMemoryLLM()))
}
