package main

import (
	"context"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/utils"
)

// --- Example 5: the agent belongs in a job ----------------------------------
//
// An "agent loop" is the tool loop from 04_tools.go with more steps and tools
// that do real work. There is no separate API for it, and there is no separate
// runtime for it either — the thing it needs is what the job runner already
// has: a timeout that is enforced, retries with backoff, a concurrency ceiling,
// a cancel button, and a run row that says what happened.
//
// The alternative people reach for is `go func()` from an HTTP handler. That
// loop has no deadline, cannot be cancelled, retries nothing, and leaves no
// trace when it ends — and it is holding a model that bills per step.

// AgentParams is the job's input, validated on the way in exactly like an HTTP
// payload, so a bad trigger fails the run up front instead of after two billed
// steps.
type AgentParams struct {
	ConversationID *string `json:"conversation_id"`
	Question       *string `json:"question"`
}

func registerAgentJobs(reg *core.JobRegistry, desk *SupportDesk) {
	_ = core.RegisterJob(reg, core.JobDef{
		Name:        "support-agent",
		Description: "answer one support conversation with the tool loop",

		// A queue of its own, because the constraint is the provider's rate
		// limit rather than this process's CPU. Fifty agents racing into the
		// same 429 is slower than four of them taking turns.
		Queue:         "ai",
		MaxConcurrent: 4,
		Concurrency:   core.ConcurrencyEnqueue,

		// Generous, because a long loop genuinely takes minutes — and finite,
		// because a model that keeps calling a failing tool will keep going
		// until something stops it. This is that something.
		Timeout: 10 * time.Minute,

		// The retryable failures are the provider's: 429 and 5xx. Anything the
		// handler returns as a 4xx is the same answer next time, so the runner
		// should not be asked to find out twice.
		MaxAttempts: 3,

		// Worth persisting here where it is not for a chatty sync job: an agent
		// run is infrequent, expensive, and the thing someone asks about later.
		Logs: core.LogPolicyPtr(core.LogOnFailure),

		Params: core.Params(
			core.StringParam("conversation_id").Required().Desc("Conversation to answer"),
			core.StringParam("question").Required().Multiline().Max(4000),
		),
	}, runSupportAgent(desk))
}

// runSupportAgent closes over the desk so the tools are the ones built at
// startup. Rebuilding them per run would move a schema bug from boot to
// whichever run happened to hit it.
func runSupportAgent(desk *SupportDesk) func(core.ICronjobContext, AgentParams) error {
	return func(c core.ICronjobContext, p AgentParams) error {
		question := utils.ToNonPointerOr(p.Question, "")
		conversation := utils.ToNonPointerOr(p.ConversationID, "")

		c.Progress(10, "thinking")

		// c is an IContext, so core.LLM(c) is bound to this run: cancelling the
		// run cancels the generation, and the job's Timeout is the deadline the
		// provider call actually observes.
		answer, err := desk.Answer(c, question)
		if err != nil {
			// Returned, not logged-and-returned. The runner records it on the
			// JobRun and reports it once — logging it here too makes one
			// incident look like two.
			return err
		}

		c.Progress(90, "replying")
		c.SetResult(map[string]any{"conversation_id": conversation, "chars": len(answer)})
		return nil
	}
}

// triggerAgent is what an HTTP handler does instead of running the loop itself:
// hand the work to the runner and answer immediately with the run to poll.
//
// IdemKey is what makes a double-clicked button one run rather than two
// conversations answered twice — and two bills.
func triggerAgent(ctx context.Context, runner *core.JobRunner, userID, conversationID, question string) (*core.JobRun, core.IError) {
	return runner.Trigger(ctx, "support-agent", &AgentParams{
		ConversationID: utils.ToPointer(conversationID),
		Question:       utils.ToPointer(question),
	}, core.TriggerOptions{
		By:      userID,
		IdemKey: "support-agent:" + conversationID,
	})
}

// demoAgentJob is the wiring main.go runs: a registry, a runner with a queue of
// its own for the model, one triggered run, then a clean stop.
func demoAgentJob(app *core.App, desk *SupportDesk) error {
	reg := core.NewJobRegistry()
	registerAgentJobs(reg, desk)

	runner := core.NewJobRunner(app, reg,
		core.WithWorkers(2),
		// The AI queue is bounded by the provider's rate limit rather than by
		// this process, so it gets a ceiling of its own instead of sharing the
		// default one with everything else.
		core.WithQueues(core.DefaultQueue, "ai"),
		core.WithQueueLimit("ai", 2),
	)
	runner.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	run, err := runner.TriggerAndWait(ctx, "support-agent", &AgentParams{
		ConversationID: utils.ToPointer("conv-1"),
		Question:       utils.ToPointer("ของ TH-1042 ถึงเมื่อไหร่"),
	})
	if err != nil {
		return err
	}
	app.Log().Info("agent job finished",
		"run_id", run.ID, "status", string(run.Status), "duration_ms", run.DurationMS)

	return runner.Stop(ctx)
}

// proposeOnly is the safest shape for anything with side effects, and the one
// to reach for before writing an Approve policy at all: give the agent
// read-only tools, keep what it suggests, and let a person press the button.
//
// A model that is right ninety-nine times and wrong once has still created one
// refund somebody has to chase.
func proposeOnly(ctx core.IContext, readOnly []core.LLMTool, incident string) (core.LLMResponse, core.IError) {
	return core.LLM(ctx).Generate(core.LLMRequest{
		System:      "Diagnose the incident and propose what should be done. Do not act.",
		CacheSystem: true,
		Messages:    []core.LLMMessage{core.LLMUser(incident)},
		Tools:       readOnly, // nothing here can write
		MaxSteps:    8,
		MaxTokens:   4096,
	})
}
