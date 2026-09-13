package main

import (
	"context"
	"testing"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Example 7: testing without a provider ----------------------------------
//
// Everything here runs with no API key, no network and no bill. The functions
// are written exactly as they would be in a _test.go file in your own service;
// they sit in an ordinary file so the compiler keeps the docs page honest.
//
// What is under test is our code, never the model: that the prompt was
// assembled the way we meant, that the approval policy held, that a rate limit
// became a queued job. A test asserting on what a model *said* fails the week
// the provider ships a new checkpoint.

// newAIContext builds an App around in-memory capabilities. NewApp wraps the
// model in instrumentation, so the handle the test holds is not the one it
// passed in — LLMCalls, QueueLLMReply and ResetLLM all unwrap for you, which is
// why `m` stays usable afterwards.
func newAIContext(t *testing.T, opts ...core.Option) core.IContext {
	t.Helper()

	env, err := core.NewEnvPath(t.TempDir()) // empty dir: no stray .env leaks in
	require.NoError(t, err)

	app, err := core.NewApp(env, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	return app.NewContext(t.Context(), core.ModeTest)
}

// TestPromptIsAssembled is the assertion worth writing for almost every call
// site: not what came back, but what went out.
func TestPromptIsAssembled(t *testing.T) {
	m := core.NewMemoryLLM("สรุปแล้วครับ")
	ctx := newAIContext(t, core.WithLLM(m))

	out, err := summarize(ctx, "รายงานยอดขายประจำเดือนกรกฎาคม")
	require.Nil(t, err)
	assert.Equal(t, "สรุปแล้วครับ", out)

	calls := core.LLMCalls(m)
	require.Len(t, calls, 1, "one summary is one generation; a second means something retried silently")

	assert.Equal(t, summaryRules, calls[0].System,
		"a per-call system string moves the cache prefix, so every request pays full price")
	assert.True(t, calls[0].CacheSystem)
	assert.Equal(t, 256, calls[0].MaxTokens,
		"unset falls back to whatever the largest caller in the process needs")
	require.Len(t, calls[0].Messages, 1)
	assert.Equal(t, core.LLMRoleUser, calls[0].Messages[0].Role,
		"user input belongs in a message; appending it to the system prompt is the cheapest prompt injection there is")
}

// TestRefundIsDeniedByPolicy is the security property the approval gate exists
// for. Scripting the call is the only way to reach it without a provider: the
// decision to call a tool is the model's, so a test has to play the model.
func TestRefundIsDeniedByPolicy(t *testing.T) {
	desk, derr := NewSupportDesk()
	require.Nil(t, derr)

	ran := false
	refund, terr := llm.Tool("refund_order", "Refund an order in full.",
		func(context.Context, struct{}) (string, core.IError) {
			ran = true // must stay false: the gate runs before the handler
			return `{"refunded":true}`, nil
		})
	require.Nil(t, terr)

	m := core.NewMemoryLLM()
	core.QueueLLMReply(m,
		core.MemoryLLMReply{ToolCalls: []core.LLMToolCall{
			core.MemoryToolCall("refund_order", `{"order_id":"TH-1042","amount":500}`),
		}},
		core.MemoryLLMReply{Text: "ขออภัยครับ การคืนเงินต้องให้เจ้าหน้าที่อนุมัติ"},
	)
	ctx := newAIContext(t, core.WithLLM(m))

	resp, err := core.LLM(ctx).Generate(core.LLMRequest{
		Messages: []core.LLMMessage{core.LLMUser("ขอคืนเงินออเดอร์ TH-1042")},
		Tools:    []core.LLMTool{refund},
		MaxSteps: 5,
		Approve:  desk.approve, // the service's real policy, not a stub
	})

	require.Nil(t, err, "a denial is not a failed generation — the model is told why and answers around it")
	assert.False(t, ran, "a denied call must never reach the handler; this is the whole point of the gate")
	require.Len(t, resp.ToolCalls, 1, "a denied call still belongs in the audit trail")
	assert.Equal(t, core.LLMToolDenied, resp.ToolCalls[0].Outcome)
	assert.Contains(t, resp.ToolCalls[0].Output, "operator",
		"what the model was told is what explains its final answer")
}

// TestRateLimitIsQueued covers the branch hardest to reach in production and
// most important when it happens: the provider says 429 and the work has to
// survive it.
func TestRateLimitIsQueued(t *testing.T) {
	m := core.NewMemoryLLM()
	core.QueueLLMReply(m, core.MemoryLLMReply{
		Err: core.New(429, "LLM_RATE_LIMITED", "slow down"),
	})
	ctx := newAIContext(t, core.WithLLM(m))

	var queued string
	_, err := summarizeOrQueue(ctx, "doc-1", "…", func(id string) core.IError {
		queued = id
		return nil
	})

	require.Nil(t, err, "a rate limit is temporary; the caller sees work accepted, not a failure")
	assert.Equal(t, "doc-1", queued, "429 must become a retryable job, not an in-place retry with no backoff")
}

// TestPermanentFailureIsNotQueued is the other half, and the reason the switch
// in summarizeOrQueue is not just `if err != nil`: a 400 gives the same answer
// however often it is sent, so queueing it pays for the same rejection again on
// a schedule.
func TestPermanentFailureIsNotQueued(t *testing.T) {
	m := core.NewMemoryLLM()
	core.QueueLLMReply(m, core.MemoryLLMReply{
		Err: core.New(400, "LLM_REQUEST_REJECTED", "prompt rejected"),
	})
	ctx := newAIContext(t, core.WithLLM(m))

	queued := false
	_, err := summarizeOrQueue(ctx, "doc-2", "…", func(string) core.IError {
		queued = true
		return nil
	})

	require.Error(t, err)
	assert.Equal(t, "LLM_REQUEST_REJECTED", err.GetCode())
	assert.False(t, queued)
}

// TestSearchEmbedsBothSidesCorrectly catches the pairing that fails most
// quietly of all: a corpus indexed as documents but searched with a query
// vector built as a document retrieves worse, and every score still looks
// perfectly reasonable.
func TestSearchEmbedsBothSidesCorrectly(t *testing.T) {
	e := core.NewMemoryEmbedder(embedDimensions)
	ctx := newAIContext(t, core.WithEmbedder(e))

	corpus, err := indexChunks(ctx, "handbook", []string{"parcels ship within two days", "refunds take five days"})
	require.Nil(t, err)

	_, err = searchTopK(ctx, "when does my parcel ship", corpus, 2)
	require.Nil(t, err)

	reqs := core.EmbeddedRequests(e)
	require.Len(t, reqs, 2)
	assert.Equal(t, core.EmbedDocument, reqs[0].Task, "the corpus side")
	assert.Equal(t, core.EmbedQuery, reqs[1].Task, "the search side — the two must differ or retrieval degrades silently")
	assert.Equal(t, embedDimensions, reqs[0].Dimensions,
		"the requested width must match the column, or the real driver inserts vectors the schema rejects")
}
