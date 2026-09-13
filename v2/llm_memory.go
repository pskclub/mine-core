package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// MemoryLLMReply is one scripted reply for a memory model. Only the fields a
// test cares about need setting: an empty Text with a nil Err still produces a
// valid response, which is what a test asserting only on the request wants.
type MemoryLLMReply struct {
	Text         string
	Usage        LLMUsage
	FinishReason LLMFinishReason
	// ToolCalls makes the model ask for tools instead of answering. The memory
	// model runs them — through the same approval gate a real driver uses — and
	// then consumes the next scripted reply as the turn that follows.
	//
	// That is what makes a tool handler testable: the decision to call is the
	// model's job, and scripting it is the only way to reach the handler
	// without a provider.
	ToolCalls []LLMToolCall
	// Sources scripts a grounded answer's citations. Without them a service that
	// renders sources — which the grounding services require it to — could only
	// be tested against a real provider, which is the same as not being tested.
	Sources []LLMSource
	// Err makes the call fail. Use it to exercise the path a provider outage
	// takes without waiting for one.
	Err IError
}

// MemoryToolCall is a shorthand for scripting one call.
//
//	core.QueueLLMReply(m, core.MemoryLLMReply{
//	    ToolCalls: []core.LLMToolCall{core.MemoryToolCall("get_weather", `{"city":"Bangkok"}`)},
//	})
func MemoryToolCall(name, inputJSON string) LLMToolCall {
	return LLMToolCall{ID: "mem-" + name, Name: name, Input: json.RawMessage(inputJSON)}
}

// memoryLLM is an in-process ILLM. A test that generates text should not need
// an API key, a network, or a bill — and neither should a developer running the
// service locally to work on something else.
//
// What it cannot do is produce language. Scripted replies are returned verbatim
// and unscripted ones are echoed, so it tests the wiring around a model — that
// the right prompt was built, that usage was recorded, that an error surfaced —
// never the model's judgement.
type memoryLLM struct {
	ctx context.Context
	rec *llmRecorder
}

type llmRecorder struct {
	mu      sync.Mutex
	calls   []LLMRequest
	replies []MemoryLLMReply
	next    int
}

var _ ILLM = (*memoryLLM)(nil)

// NewMemoryLLM returns a model that records requests and returns canned
// replies, so a test can assert on what a service would have asked a model:
//
//	m := core.NewMemoryLLM("Bangkok")
//	app, _ := core.NewApp(env, core.WithLLM(m))
//	...
//	calls := core.LLMCalls(m)
//	require.Len(t, calls, 1)
//	assert.Contains(t, calls[0].System, "answer in one word")
//
// Replies are consumed in order. Running out is an error rather than a repeat
// of the last one: a service that called the model more often than the test
// scripted has changed behaviour, and silently handing it another answer is how
// that goes unnoticed. With no replies scripted at all it echoes the last user
// message, so a test that does not care what the model said needs no setup.
//
// It runs the same request validation a driver does, so a malformed request
// fails the test rather than passing it.
func NewMemoryLLM(replies ...string) ILLM {
	rec := &llmRecorder{}
	for _, r := range replies {
		rec.replies = append(rec.replies, MemoryLLMReply{Text: r})
	}
	return &memoryLLM{ctx: context.Background(), rec: rec}
}

// QueueLLMReply appends scripted replies to a memory model — for usage numbers
// or a failure, which NewMemoryLLM's plain strings cannot express. It is a no-op
// for any other model.
func QueueLLMReply(l ILLM, replies ...MemoryLLMReply) {
	m, ok := unwrapMemoryLLM(l)
	if !ok {
		return
	}
	m.rec.mu.Lock()
	defer m.rec.mu.Unlock()
	m.rec.replies = append(m.rec.replies, replies...)
}

// LLMCalls returns the requests a memory model has recorded, in order. It
// returns nil for any other model.
func LLMCalls(l ILLM) []LLMRequest {
	m, ok := unwrapMemoryLLM(l)
	if !ok {
		return nil
	}
	m.rec.mu.Lock()
	defer m.rec.mu.Unlock()
	return append([]LLMRequest(nil), m.rec.calls...)
}

// ResetLLM drops everything a memory model has recorded and rewinds its
// scripted replies.
func ResetLLM(l ILLM) {
	m, ok := unwrapMemoryLLM(l)
	if !ok {
		return
	}
	m.rec.mu.Lock()
	defer m.rec.mu.Unlock()
	m.rec.calls = nil
	m.rec.next = 0
}

// unwrapMemoryLLM finds the memory model behind whatever wrapping the App
// applied. NewApp instruments every registered model, so the handle a test holds
// is not the one it passed in — without this, every assertion helper would
// silently return nothing the moment the model was registered.
func unwrapMemoryLLM(l ILLM) (*memoryLLM, bool) {
	for i := 0; i < 8 && l != nil; i++ {
		if m, ok := l.(*memoryLLM); ok {
			return m, true
		}
		w, ok := l.(interface{ inner() ILLM })
		if !ok {
			return nil, false
		}
		l = w.inner()
	}
	return nil, false
}

func (m *memoryLLM) Generate(req LLMRequest) (LLMResponse, IError) {
	if err := req.validate(); err != nil {
		return LLMResponse{}, err
	}
	if err := m.ctxErr(); err != nil {
		return LLMResponse{}, err
	}

	reply, err := m.take(req, 1)
	if err != nil {
		return LLMResponse{}, err
	}
	if reply.Err != nil {
		return LLMResponse{}, reply.Err
	}

	// Scripted tool calls drive the same loop a real driver runs, so a service's
	// tool handlers and its approval policy are exercised here rather than only
	// in an integration test nobody runs on every commit.
	steps := 1
	var made []LLMToolCall
	for len(reply.ToolCalls) > 0 && steps < req.steps() {
		for _, call := range reply.ToolCalls {
			res := req.RunTool(m.ctxOrBackground(), call)
			call.Outcome = res.Outcome
			call.Output = res.Output
			made = append(made, call)
		}
		steps++
		if reply, err = m.take(req, steps); err != nil {
			return LLMResponse{}, err
		}
		if reply.Err != nil {
			return LLMResponse{}, reply.Err
		}
	}
	if len(reply.ToolCalls) > 0 {
		// Out of steps with the model still asking: the same shape a real
		// provider leaves behind, so a caller's handling of it is testable.
		for _, call := range reply.ToolCalls {
			made = append(made, call)
		}
		return LLMResponse{
			Model:        m.modelOf(req),
			Provider:     "memory",
			FinishReason: LLMFinishToolUse,
			Steps:        steps,
			ToolCalls:    made,
			Usage:        LLMUsage{InputTokens: estimateTokens(req.promptChars())},
		}, nil
	}

	usage := reply.Usage
	if usage.Total() == 0 {
		usage = LLMUsage{
			InputTokens:  estimateTokens(req.promptChars()),
			OutputTokens: estimateTokens(len(reply.Text)),
		}
	}
	finish := reply.FinishReason
	if finish == "" {
		finish = LLMFinishStop
	}
	return LLMResponse{
		Text:         reply.Text,
		Model:        m.modelOf(req),
		Provider:     "memory",
		FinishReason: finish,
		Usage:        usage,
		Steps:        steps,
		ToolCalls:    made,
		Sources:      reply.Sources,
	}, nil
}

func (m *memoryLLM) Stream(req LLMRequest) (LLMStream, IError) {
	resp, err := m.Generate(req)
	if err != nil {
		return nil, err
	}
	// Splitting on whitespace keeps the deltas recognisable as words, which is
	// what a caller assembling them into a UI is really testing.
	return &memoryStream{parts: splitDeltas(resp.Text), resp: resp}, nil
}

func (m *memoryLLM) CountTokens(req LLMRequest) (int, IError) {
	if err := req.validate(); err != nil {
		return 0, err
	}
	return estimateTokens(req.promptChars()), nil
}

func (m *memoryLLM) Capabilities() LLMCapabilities {
	return LLMCapabilities{
		Streaming:        true,
		StructuredOutput: true,
		Reasoning:        true,
		PromptCaching:    true,
		TokenCounting:    true,
		Tools:            true,
		Vision:           true,
	}
}

func (m *memoryLLM) Model() string { return "memory" }

// modelOf reports what a request ran against. A memory model serves whatever
// model id it is handed, so a service that routes a cheap classification to one
// model and the answer to another can be tested for exactly that.
func (m *memoryLLM) modelOf(req LLMRequest) string {
	if req.Model != "" {
		return req.Model
	}
	return m.Model()
}

func (m *memoryLLM) Provider() string { return "memory" }
func (m *memoryLLM) Enabled() bool    { return true }
func (m *memoryLLM) Unwrap() any      { return nil }
func (m *memoryLLM) Close() IError    { return nil }

func (m *memoryLLM) WithContext(ctx context.Context) ILLM {
	cp := *m
	cp.ctx = ctx
	return &cp
}

func (m *memoryLLM) ctxOrBackground() context.Context {
	if m.ctx == nil {
		return context.Background()
	}
	return m.ctx
}

func (m *memoryLLM) ctxErr() IError {
	if m.ctx == nil {
		return nil
	}
	if err := m.ctx.Err(); err != nil {
		return Wrap(err, "llm: context ended before the call completed")
	}
	return nil
}

// take records the call and returns the reply scripted for it.
func (m *memoryLLM) take(req LLMRequest, step int) (MemoryLLMReply, IError) {
	m.rec.mu.Lock()
	defer m.rec.mu.Unlock()
	m.rec.calls = append(m.rec.calls, req)

	if len(m.rec.replies) == 0 {
		// With tools offered and nothing scripted, call each one once and then
		// answer. A real model decides for itself and cannot be asked to;
		// exercising every handler is the most useful thing a stand-in can do
		// instead, and it means a service that adds a tool gets it covered
		// without anyone writing a script for it.
		if step == 1 && len(req.Tools) > 0 && req.steps() > 1 {
			calls := make([]LLMToolCall, 0, len(req.Tools))
			for _, tool := range req.Tools {
				if tool.IsProviderDefined() {
					// The provider runs this one, so there is no handler here to
					// exercise — and calling it would fail a test that was right.
					continue
				}
				calls = append(calls, MemoryToolCall(tool.Name, "{}"))
			}
			if len(calls) > 0 {
				return MemoryLLMReply{ToolCalls: calls}, nil
			}
		}
		if req.Schema != nil {
			// Echoing the prompt would not parse as the requested shape, and
			// the failure would read as a driver bug rather than as "this test
			// asked for a value it never scripted".
			return MemoryLLMReply{Text: "{}"}, nil
		}
		return MemoryLLMReply{Text: lastUserText(req)}, nil
	}
	if m.rec.next >= len(m.rec.replies) {
		return MemoryLLMReply{}, Newf(500, "LLM_MEMORY_EXHAUSTED",
			"llm: memory model was called %d times but only %d replies were scripted",
			len(m.rec.calls), len(m.rec.replies))
	}
	reply := m.rec.replies[m.rec.next]
	m.rec.next++
	return reply, nil
}

func lastUserText(req LLMRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == LLMRoleUser {
			return req.Messages[i].Text
		}
	}
	return ""
}

// estimateTokens is the rule of thumb of roughly four characters per token. It
// is only ever used by the memory model: a driver that cannot count for real
// returns ErrLLMUnsupported instead, because a plausible wrong number gets
// trusted in a way that a missing one does not.
func estimateTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

func splitDeltas(text string) []string {
	if text == "" {
		return nil
	}
	words := strings.Fields(text)
	if len(words) <= 1 {
		return []string{text}
	}
	out := make([]string, 0, len(words))
	for i, w := range words {
		if i == 0 {
			out = append(out, w)
			continue
		}
		out = append(out, " "+w)
	}
	return out
}

type memoryStream struct {
	parts  []string
	i      int
	cur    string
	resp   LLMResponse
	closed bool
}

var _ LLMStream = (*memoryStream)(nil)

func (s *memoryStream) Next() bool {
	if s.closed || s.i >= len(s.parts) {
		return false
	}
	s.cur = s.parts[s.i]
	s.i++
	return true
}

func (s *memoryStream) Text() string          { return s.cur }
func (s *memoryStream) Response() LLMResponse { return s.resp }
func (s *memoryStream) Err() IError           { return nil }

func (s *memoryStream) Close() IError {
	s.closed = true
	return nil
}
