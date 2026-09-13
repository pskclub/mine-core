package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func echoTool(name string, ran *[]string) LLMTool {
	return LLMTool{
		Name:        name,
		Description: "test tool",
		Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		Execute: func(_ context.Context, input json.RawMessage) (string, IError) {
			*ran = append(*ran, name+":"+string(input))
			return "ok from " + name, nil
		},
	}
}

func TestLLMTool_memoryRunsScriptedCallsThroughTheRealHandler(t *testing.T) {
	// This is why the memory model runs the loop at all: without it a service's
	// tool handlers are only ever reached by a provider, so nothing about them
	// is covered by the suite CI runs.
	var ran []string
	m := NewMemoryLLM()
	QueueLLMReply(m,
		MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("get_weather", `{"city":"Bangkok"}`)}},
		MemoryLLMReply{Text: "It is hot."},
	)

	resp, err := m.Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("weather?")},
		Tools:    []LLMTool{echoTool("get_weather", &ran)},
		MaxSteps: 5,
	})
	require.NoError(t, err)

	assert.Equal(t, []string{`get_weather:{"city":"Bangkok"}`}, ran,
		"the handler must receive the arguments the model produced, unchanged")
	assert.Equal(t, "It is hot.", resp.Text)
	assert.Equal(t, 2, resp.Steps, "one turn to call, one to answer")
	require.Len(t, resp.ToolCalls, 1, "the audit trail is the point of recording calls")
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
}

func TestLLMTool_approveCanDenyACall(t *testing.T) {
	var ran []string
	m := NewMemoryLLM()
	QueueLLMReply(m,
		MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("refund_order", `{"id":"A1"}`)}},
		MemoryLLMReply{Text: "I cannot refund that."},
	)

	resp, err := m.Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("refund order A1")},
		Tools:    []LLMTool{echoTool("refund_order", &ran)},
		MaxSteps: 5,
		Approve: func(_ context.Context, call LLMToolCall) IError {
			return New(403, "NEEDS_HUMAN", "refunds require an operator")
		},
	})
	require.NoError(t, err, "a denial is not a failed generation — the model gets told and carries on")
	assert.Empty(t, ran, "a denied call must never reach the handler; this is the whole reason the gate exists")
	assert.Equal(t, "I cannot refund that.", resp.Text)
	assert.Len(t, resp.ToolCalls, 1, "a denied call still belongs in the audit trail")
}

func TestLLMTool_approveSeesWhatIsAboutToRun(t *testing.T) {
	var seen LLMToolCall
	var ran []string
	m := NewMemoryLLM()
	QueueLLMReply(m,
		MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("delete_user", `{"id":"u-9"}`)}},
		MemoryLLMReply{Text: "done"},
	)

	_, err := m.Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("delete u-9")},
		Tools:    []LLMTool{echoTool("delete_user", &ran)},
		MaxSteps: 3,
		Approve: func(_ context.Context, call LLMToolCall) IError {
			seen = call
			return nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "delete_user", seen.Name)
	assert.JSONEq(t, `{"id":"u-9"}`, string(seen.Input),
		"a policy that cannot read the arguments can only allow or deny a whole tool, which is too blunt")
}

func TestLLMTool_maxStepsStopsTheLoop(t *testing.T) {
	// A model that keeps calling a tool that keeps failing will do so until
	// something stops it. That something is MaxSteps.
	var ran []string
	m := NewMemoryLLM()
	QueueLLMReply(m,
		MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("loop", `{}`)}},
		MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("loop", `{}`)}},
		MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("loop", `{}`)}},
	)

	resp, err := m.Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("go")},
		Tools:    []LLMTool{echoTool("loop", &ran)},
		MaxSteps: 2,
	})
	require.NoError(t, err)
	assert.Len(t, ran, 1, "two steps means one round of tools and then a stop")
	assert.Equal(t, LLMFinishToolUse, resp.FinishReason,
		"the caller has to be able to tell a finished answer from a loop that ran out of room")
}

func TestLLMTool_noMaxStepsMeansNoLoop(t *testing.T) {
	// Forgetting MaxSteps must not silently start a loop: the calls come back
	// and the caller decides.
	var ran []string
	m := NewMemoryLLM()
	QueueLLMReply(m, MemoryLLMReply{ToolCalls: []LLMToolCall{MemoryToolCall("t", `{}`)}})

	resp, err := m.Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("go")},
		Tools:    []LLMTool{echoTool("t", &ran)},
	})
	require.NoError(t, err)
	assert.Empty(t, ran)
	assert.Equal(t, LLMFinishToolUse, resp.FinishReason)
	assert.Len(t, resp.ToolCalls, 1)
}

func TestLLMTool_unknownToolIsReportedToTheModelNotTheCaller(t *testing.T) {
	req := LLMRequest{Messages: []LLMMessage{LLMUser("x")}}
	res := req.RunTool(context.Background(), LLMToolCall{Name: "nope"})
	assert.Equal(t, LLMToolUnknown, res.Outcome)
	assert.Contains(t, res.Output, "no tool named")
	assert.Contains(t, res.Output, "nope",
		"the model can pick another route if told which name failed; a silent empty result cannot be recovered from")
}

func TestLLMTool_panicInAToolDoesNotTakeTheProcess(t *testing.T) {
	// A tool is ordinary code reached through a model's decision — the path
	// least likely to have been exercised before it runs in production.
	req := LLMRequest{
		Messages: []LLMMessage{LLMUser("x")},
		Tools: []LLMTool{{
			Name:    "boom",
			Execute: func(context.Context, json.RawMessage) (string, IError) { panic("kaboom") },
		}},
	}

	res := req.RunTool(context.Background(), LLMToolCall{Name: "boom"})
	assert.Equal(t, LLMToolFailed, res.Outcome)
	assert.Contains(t, res.Output, "error:")
	assert.Contains(t, res.Output, "kaboom")
}

func TestLLMTool_failingToolTellsTheModelWhy(t *testing.T) {
	req := LLMRequest{
		Messages: []LLMMessage{LLMUser("x")},
		Tools: []LLMTool{{
			Name: "lookup",
			Execute: func(context.Context, json.RawMessage) (string, IError) {
				return "", New(404, "NOT_FOUND", "no such order")
			},
		}},
	}

	res := req.RunTool(context.Background(), LLMToolCall{Name: "lookup"})
	assert.Equal(t, LLMToolFailed, res.Outcome)
	assert.Contains(t, res.Output, "no such order",
		"a model told why its call failed can adapt; one told nothing repeats itself until MaxSteps runs out")
}

func TestLLMTool_validationRejectsBadDeclarations(t *testing.T) {
	base := LLMRequest{Messages: []LLMMessage{LLMUser("x")}}
	m := NewMemoryLLM()

	req := base
	req.Tools = []LLMTool{{Name: "a", Execute: func(context.Context, json.RawMessage) (string, IError) { return "", nil }},
		{Name: "a", Execute: func(context.Context, json.RawMessage) (string, IError) { return "", nil }}}
	_, err := m.Generate(req)
	require.Error(t, err, "providers key results back by name — a duplicate runs the wrong function")
	assert.Equal(t, "LLM_INVALID_REQUEST", err.GetCode())

	req = base
	req.Tools = []LLMTool{{Name: "b"}}
	_, err = m.Generate(req)
	require.Error(t, err, "a tool the model can call but nothing implements would hang the loop")
	assert.Contains(t, err.GetMessage(), "Execute")

	req = base
	req.Tools = []LLMTool{{Execute: func(context.Context, json.RawMessage) (string, IError) { return "", nil }}}
	_, err = m.Generate(req)
	require.Error(t, err)
	assert.Contains(t, err.GetMessage(), "no name")
}

func TestLLMTool_providerDefinedToolsNeedNoExecute(t *testing.T) {
	// A tool the provider runs on its own side has nothing here to execute. The
	// validation that demands an Execute is what kept Google Search grounding
	// out of the framework, so this is the case that has to pass.
	m := NewMemoryLLM("grounded")

	resp, err := m.Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("what is the latest go release")},
		Tools: []LLMTool{{
			Name:            "google_search",
			Description:     "Search Google.",
			ProviderType:    "google.google_search",
			ProviderOptions: map[string]any{"searchTypes": map[string]any{"webSearch": map[string]any{}}},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "grounded", resp.Text)
}

func TestLLMTool_providerDefinedToolWithAnExecuteIsRejected(t *testing.T) {
	// The dangerous shape: a caller who wrote an Execute believes their code
	// gates the tool, when the provider is running it out of reach.
	m := NewMemoryLLM()

	_, err := m.Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("x")},
		Tools: []LLMTool{{
			Name:         "google_search",
			ProviderType: "google.google_search",
			Execute:      func(context.Context, json.RawMessage) (string, IError) { return "", nil },
		}},
	})
	require.Error(t, err)
	assert.Equal(t, "LLM_INVALID_REQUEST", err.GetCode())
	assert.Contains(t, err.GetMessage(), "provider-defined")
}

func TestLLMTool_aProviderDefinedToolIsNeverRunLocally(t *testing.T) {
	// Reaching RunTool with one means a driver routed a server-side tool into
	// the local loop. Answering the model with a made-up result would be worse
	// than failing.
	req := LLMRequest{
		Messages: []LLMMessage{LLMUser("x")},
		Tools:    []LLMTool{{Name: "google_search", ProviderType: "google.google_search"}},
	}

	res := req.RunTool(context.Background(), LLMToolCall{Name: "google_search"})
	assert.Equal(t, LLMToolFailed, res.Outcome)
	require.NotNil(t, res.Err)
	assert.Equal(t, "LLM_TOOL_NOT_LOCAL", res.Err.GetCode())
}

func TestLLMTool_memoryDoesNotInventCallsForProviderDefinedTools(t *testing.T) {
	// With nothing scripted the memory model calls every tool once, which is the
	// most useful thing a stand-in can do — but a server-side tool has no
	// handler to exercise, and calling it would fail a test that was correct.
	m := NewMemoryLLM()

	resp, err := m.Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("what is the latest go release")},
		Tools:    []LLMTool{{Name: "google_search", ProviderType: "google.google_search"}},
		MaxSteps: 3,
	})
	require.NoError(t, err)
	assert.Empty(t, resp.ToolCalls)
}

func TestEmbedder_disabledWithoutConfiguration(t *testing.T) {
	app := llmTestApp(t)
	ctx := app.NewContext(context.Background(), ModeTest)

	e := Embedder(ctx)
	require.NotNil(t, e, "Embedder must never be nil")
	assert.False(t, e.Enabled())

	_, err := e.EmbedOne("hello")
	require.Error(t, err, "an empty vector is a search index that returns nothing relevant, forever")
	assert.Equal(t, "EMBED_DISABLED", err.GetCode())
	assert.True(t, errors.Is(err, ErrEmbedderDisabled))
}

func TestEmbedder_memoryIsDeterministicAndOrderPreserving(t *testing.T) {
	e := NewMemoryEmbedder(16)

	first, err := e.Embed("alpha beta", "gamma")
	require.NoError(t, err)
	require.Len(t, first, 2, "one vector per input, in the same order — callers pair them by index")
	assert.Len(t, first[0], 16)

	second, err := e.Embed("alpha beta", "gamma")
	require.NoError(t, err)
	assert.Equal(t, first, second,
		"a test asserting a ranking needs the same answer every run; a real model does not promise that")
}

func TestEmbedder_memoryRanksSharedWordsHigher(t *testing.T) {
	// Not semantic — a bag-of-words sketch. Enough to test the plumbing of a
	// search, never enough to judge its quality.
	e := NewMemoryEmbedder(64)

	vecs, err := e.Embed(
		"the cat sat on the mat",
		"a cat sat on a mat today",
		"quarterly revenue exceeded forecast",
	)
	require.NoError(t, err)

	related := CosineSimilarity(vecs[0], vecs[1])
	unrelated := CosineSimilarity(vecs[0], vecs[2])
	assert.Greater(t, related, unrelated,
		"a fixture that ranked an unrelated document first would make every search test meaningless")
}

func TestEmbedder_memoryRecordsWhatWasAsked(t *testing.T) {
	e := NewMemoryEmbedder()
	app := llmTestApp(t, WithEmbedder(e))
	ctx := app.NewContext(context.Background(), ModeTest)

	_, err := Embedder(ctx).Embed("first", "second")
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "second"}, EmbeddedTexts(e))
}

func TestEmbedder_rejectsAnEmptyBatch(t *testing.T) {
	e := NewMemoryEmbedder()
	_, err := e.Embed()
	require.Error(t, err)
	assert.Equal(t, "EMBED_INVALID_REQUEST", err.GetCode())
}

func TestEmbedder_memoryRecordsTheOptionsTooAndHonoursTheLength(t *testing.T) {
	// The pairing this asserts is the one that goes wrong silently: a corpus
	// indexed as documents has to be searched with query vectors, and a column
	// declared vector(768) has to be filled by calls that asked for 768.
	e := NewMemoryEmbedder(8)

	_, err := e.EmbedWith(EmbedRequest{
		Texts: []string{"ประกาศราชกิจจานุเบกษา"}, Dimensions: 768, Task: EmbedDocument,
	})
	require.NoError(t, err)
	query, err := e.EmbedWith(EmbedRequest{Texts: []string{"ประกาศ"}, Dimensions: 768, Task: EmbedQuery})
	require.NoError(t, err)
	assert.Len(t, query[0], 768,
		"a stand-in that returned its own width would pass while the real model failed the insert")

	recorded := EmbeddedRequests(e)
	require.Len(t, recorded, 2)
	assert.Equal(t, EmbedDocument, recorded[0].Task)
	assert.Equal(t, EmbedQuery, recorded[1].Task)
}

func TestEmbedder_requestValidation(t *testing.T) {
	cases := map[string]EmbedRequest{
		"no texts":            {},
		"blank text":          {Texts: []string{"x", " "}},
		"unknown task":        {Texts: []string{"x"}, Task: "retrieval_document"},
		"negative dimensions": {Texts: []string{"x"}, Dimensions: -1},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			err := req.Validate()
			require.Error(t, err, "every driver runs this, so a caller's mistake reads the same whichever is configured")
			assert.Equal(t, "EMBED_INVALID_REQUEST", err.GetCode())
			assert.Equal(t, 400, err.GetStatus())
		})
	}
}

func TestEmbedder_unsupportedErrorIsIdenticalAcrossDrivers(t *testing.T) {
	err := EmbedUnsupportedError("openai", "task types")
	assert.Equal(t, "EMBED_UNSUPPORTED", err.GetCode())
	assert.Equal(t, 400, err.GetStatus())
	assert.True(t, errors.Is(err, ErrEmbedUnsupported),
		"an unsupported option must be distinguishable from an outage, or callers will retry it forever")
}

func TestEmbedder_disabledRefusesTheOptionsFormToo(t *testing.T) {
	_, err := NewNoopEmbedder().EmbedWith(EmbedRequest{Texts: []string{"x"}, Dimensions: 768})
	require.Error(t, err)
	assert.Equal(t, "EMBED_DISABLED", err.GetCode())
}

func TestEmbedder_cosineSimilarityGuards(t *testing.T) {
	assert.InDelta(t, 1.0, CosineSimilarity([]float32{1, 2, 3}, []float32{1, 2, 3}), 1e-9)
	assert.InDelta(t, 1.0, CosineSimilarity([]float32{1, 2, 3}, []float32{2, 4, 6}), 1e-9,
		"direction is what matters — a doubled vector is the same meaning, not a better match")
	assert.InDelta(t, -1.0, CosineSimilarity([]float32{1, 0}, []float32{-1, 0}), 1e-9)
	assert.Zero(t, CosineSimilarity([]float32{1, 2}, []float32{1, 2, 3}),
		"vectors of different lengths came from different models; any score would be meaningless")
	assert.Zero(t, CosineSimilarity(nil, nil))
	assert.Zero(t, CosineSimilarity([]float32{0, 0}, []float32{1, 1}), "a zero vector has no direction")
}

func TestEmbedder_bootLogNamesTheModel(t *testing.T) {
	assert.Equal(t, false, embedderBootField(nil))
	assert.Equal(t, false, embedderBootField(NewNoopEmbedder()))
	assert.Equal(t, "memory/memory", embedderBootField(NewMemoryEmbedder()))
}

func TestEmbedder_cancelledContextStops(t *testing.T) {
	app := llmTestApp(t, WithEmbedder(NewMemoryEmbedder()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Embedder(app.NewContext(ctx, ModeTest)).EmbedOne("x")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
