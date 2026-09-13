package main

import (
	"context"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 1: text in, text out -------------------------------------------
//
// The lowest layer. core.LLM(ctx) is a function rather than a method on
// IContext for the same reason core.Requester is: calling a model is not a
// capability of the request, it is something you do with the request's deadline
// attached — so a plain context.Context works here too, and a context from
// nowhere gets the disabled model instead of nil.

// summaryRules is a package-level const, not a string built per call, and that
// is the whole point: the system prompt sits at the front of the prompt, so any
// byte that varies in it (a timestamp, the user's name, a request id) moves the
// cache prefix and every request pays full price. Anything that changes belongs
// in a message.
const summaryRules = `Summarise the text in at most three lines.
Answer in the language the text is written in.
State only what the text says — never add facts of your own.`

// summarize is the shape most calls have: one system prompt, one user turn, a
// hard ceiling on the reply.
func summarize(ctx context.Context, body string) (string, core.IError) {
	resp, err := core.LLM(ctx).Generate(core.LLMRequest{
		System:      summaryRules,
		CacheSystem: true, // paid in full once, then read at the cached rate
		Messages:    []core.LLMMessage{core.LLMUser(body)},
		// MaxTokens is the only per-call cost ceiling that is actually enforced.
		// 0 would fall back to AI_MAX_TOKENS, which is sized for the largest
		// caller in the process, not for this one.
		MaxTokens: 256,
	})
	if err != nil {
		return "", err
	}
	return readAnswer(resp)
}

// readAnswer is why FinishReason is not decoration.
//
// "length" means the answer was cut mid-sentence, not that the model finished
// early — and the text still looks like an answer. Returning it is how half a
// summary ends up stored as the real one.
func readAnswer(resp core.LLMResponse) (string, core.IError) {
	switch resp.FinishReason {
	case core.LLMFinishStop:
		return resp.Text, nil

	case core.LLMFinishLength:
		return "", core.New(502, "AI_RESPONSE_TRUNCATED",
			"the model ran out of output tokens — raise MaxTokens or ask for a shorter answer")

	case core.LLMFinishContentFilter:
		return "", core.New(422, "AI_CONTENT_BLOCKED", "the provider refused to answer this prompt")

	case core.LLMFinishToolUse:
		// Out of MaxSteps with the model still asking for tools. resp.Text is
		// not the final answer — see 04_tools.go.
		return "", core.New(500, "AI_DID_NOT_FINISH", "the model stopped mid tool loop")

	default:
		return "", core.New(502, "AI_UNEXPECTED_FINISH", "the model stopped for an unexpected reason")
	}
}

// followUp replays a stored conversation. The framework keeps no history of its
// own: a chat is whatever the service loaded out of its own table, in order,
// oldest first.
//
// LLMAssistant is for replaying what was said, not for prefilling a reply —
// several current models reject a conversation that ends on an assistant turn,
// which is why the new question is appended last.
func followUp(ctx context.Context, history []core.LLMMessage, question string) (string, core.IError) {
	msgs := append(append([]core.LLMMessage(nil), history...), core.LLMUser(question))

	resp, err := core.LLM(ctx).Generate(core.LLMRequest{
		System:      summaryRules,
		CacheSystem: true,
		Messages:    msgs,
		MaxTokens:   512,
	})
	if err != nil {
		return "", err
	}
	return readAnswer(resp)
}

// summarizeOrQueue is what "design for failure" looks like at one call site.
//
// The distinction that matters is temporary versus permanent: a 429 or a 5xx is
// the provider asking for time, and the work should end up somewhere that will
// try again with backoff. Everything else returns the same answer however many
// times it is sent, and retrying only pays for it twice.
func summarizeOrQueue(ctx context.Context, docID, body string, queue func(string) core.IError) (string, core.IError) {
	text, err := summarize(ctx, body)
	switch {
	case err == nil:
		return text, nil

	case err.GetStatus() == 429, err.GetStatus() >= 500:
		// Temporary. A job gets timeout, retry, backoff and a run log for free —
		// see 05_agent_job.go — where an in-place retry loop gets none of them.
		return "", queue(docID)

	default:
		// Permanent: a malformed request, a rejected prompt, a model that cannot
		// see. Retrying is spending money to be told the same thing again.
		return "", err
	}
}

// costOf reports what one call consumed. CachedInputTokens is reported apart
// from InputTokens because it is billed at a different rate — folding the two
// together makes a cost report wrong in the direction that looks fine.
func costOf(resp core.LLMResponse) (total, cached int) {
	return resp.Usage.Total(), resp.Usage.CachedInputTokens
}
