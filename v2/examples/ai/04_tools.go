package main

import (
	"context"
	"encoding/json"
	"fmt"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/llm"
)

// --- Example 4: tools the model calls ---------------------------------------
//
// A tool is ordinary Go code that happens to be reachable by a model's
// decision. llm.Tool[In] derives the JSON schema from In and unmarshals the
// model's arguments into it, so the function signature is the contract.
//
// Two things carry the whole design: MaxSteps, because a loop with no ceiling
// is a loop that can bill without end, and Approve, because the model decides
// what to call and the decision about what may *run* has to stay ours.

// SupportDesk holds the tools. They are built once at startup, not per request:
// building them is reflection over the input types, and a type that cannot be
// described as a schema is a bug that should stop the process, not the tenth
// request of the day.
type SupportDesk struct {
	tools []core.LLMTool
}

func NewSupportDesk() (*SupportDesk, core.IError) {
	tools, err := llm.NewToolSet().
		// The description is the only thing the model reads when deciding. Say
		// *when* to call it, not just what it is: "looks up orders" gets called
		// at random or never.
		Add(llm.Tool("get_order_status",
			"Delivery status of one order. Call this when the user asks where their parcel is, when it will arrive, or for a tracking number.",
			getOrderStatus)).
		Add(llm.Tool("list_couriers",
			"Couriers available for a destination. Call this before quoting a delivery time.",
			listCouriers)).
		Add(llm.Tool("refund_order",
			"Refund an order in full. Only for an order that is already cancelled.",
			refundOrder)).
		Build()
	if err != nil {
		return nil, err
	}
	return &SupportDesk{tools: tools}, nil
}

func getOrderStatus(_ context.Context, in struct {
	OrderID string `json:"order_id" jsonschema:"description=Order code such as TH-1042"`
}) (string, core.IError) {
	// A real one queries: repository.New[Order](ctx).FindOne("code = ?", in.OrderID)
	return fmt.Sprintf(`{"order":%q,"status":"shipped","courier":"Kerry"}`, in.OrderID), nil
}

func listCouriers(_ context.Context, in struct {
	Province string `json:"province" jsonschema:"description=Destination province in English"`
}) (string, core.IError) {
	return fmt.Sprintf(`{"province":%q,"couriers":["Kerry","Flash","ThaiPost"]}`, in.Province), nil
}

func refundOrder(_ context.Context, in struct {
	OrderID string  `json:"order_id"`
	Amount  float64 `json:"amount" jsonschema:"description=Amount in THB"`
}) (string, core.IError) {
	// Whatever this returns — result or error — is sent back to the model, so
	// it leaves the process. An error message naming a host, a DSN or an
	// internal id is an error message handed to the provider.
	return fmt.Sprintf(`{"order":%q,"refunded":%.2f}`, in.OrderID, in.Amount), nil
}

// approve is the gate. It runs before every call, and returning an error denies
// it: the handler is never reached, the message goes back to the model as the
// tool result, and the generation continues rather than failing.
//
// That last part is deliberate. A model told why it was refused explains itself
// to the user; a model told nothing calls the same tool until MaxSteps runs out.
func (d *SupportDesk) approve(_ context.Context, call core.LLMToolCall) core.IError {
	switch call.Name {
	case "get_order_status", "list_couriers":
		return nil // read-only: the model may call these freely

	case "refund_order":
		// The arguments are readable here, so the policy can be about the
		// amount rather than about the tool. Approving a whole tool is a much
		// blunter instrument than it looks.
		var p struct {
			Amount float64 `json:"amount"`
		}
		_ = json.Unmarshal(call.Input, &p)
		if p.Amount > 10_000 {
			return core.New(403, "NEEDS_APPROVAL", "refunds over THB 10,000 need an operator")
		}
		return core.New(403, "NEEDS_HUMAN", "refunds are approved by an operator, not automatically")

	default:
		// A tool nobody wrote a rule for is denied. The opposite default means
		// adding a tool silently grants the model permission to run it.
		return core.Newf(403, "NOT_ALLOWED", "tool %q has no approval policy", call.Name)
	}
}

// Answer runs the loop. Steps are what gets billed: one model turn plus its
// tool results is one step, and each step resends the whole conversation, so
// cost grows faster than the step count does.
func (d *SupportDesk) Answer(ctx core.IContext, question string) (string, core.IError) {
	resp, err := core.LLM(ctx).Generate(core.LLMRequest{
		System: `You are a delivery support agent.
Use the tools to check facts — never state a status you did not look up.
Answer in the language the customer wrote in, in at most three sentences.`,
		CacheSystem: true,
		Messages:    []core.LLMMessage{core.LLMUser(question)},
		Tools:       d.tools,
		// Without MaxSteps the model's calls come back unexecuted, with
		// FinishReason == tool_use. That is the deliberate default: a caller who
		// forgot the ceiling gets an obvious non-answer instead of a silent loop.
		MaxSteps:  5,
		MaxTokens: 1024,
		Approve:   d.approve,
	})
	if err != nil {
		return "", err
	}

	d.audit(ctx, resp)

	if resp.FinishReason == core.LLMFinishToolUse {
		// The ceiling was reached with the model still asking. resp.Text is not
		// an answer, and raising MaxSteps is usually the wrong fix — a loop that
		// cannot finish in five steps normally has a tool that describes itself
		// badly.
		return "", core.Newf(500, "AGENT_INCOMPLETE", "gave up after %d steps", resp.Steps)
	}
	return resp.Text, nil
}

// audit records what the loop actually did. resp.ToolCalls holds every call in
// order — including the denied ones, with the reason the model was given — and
// it is the only thing that answers "why did it say that" after the fact.
func (d *SupportDesk) audit(ctx core.IContext, resp core.LLMResponse) {
	for _, call := range resp.ToolCalls {
		ctx.Log().Info("agent tool call",
			"tool", call.Name,
			"outcome", string(call.Outcome), // ran · denied · failed · unknown_tool
			"steps", resp.Steps)
		// A tool that touches real data deserves a row rather than a log line:
		// repository.New[AgentAudit](ctx).Create(&AgentAudit{...})
	}
}
