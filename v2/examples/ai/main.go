// Command ai is a runnable tour of the v2 AI capability: text generation,
// typed values, streaming, tool use, agents as jobs, embeddings and testing.
//
// Each example lives in its own file:
//
//	01_text.go       Generate, conversations, FinishReason, retryable vs permanent
//	02_typed.go      llm.New[T] — a Go value instead of a paragraph to parse
//	03_streaming.go  Stream to a browser as SSE, and what to keep at the end
//	04_tools.go      tools the model calls, MaxSteps, Approve, the audit trail
//	05_agent_job.go  the long loop belongs in a job, not in a bare goroutine
//	06_embeddings.go vectors, similarity, a minimal RAG
//	07_testing.go    the same code under test with no provider and no bill
//
// Run it with: go run ./examples/ai
//
// It runs with no AI configuration at all. Unconfigured, the framework hands
// out the disabled model, whose every call fails with LLM_DISABLED — loudly,
// because a generation that quietly returned nothing is an empty answer the
// caller believes is real. Right for a service, useless for a demo, so this
// program substitutes the in-memory model and says so in the boot log.
//
// With a provider configured every step below is a real, billed call.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/llm/goai"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ai example:", err)
		os.Exit(1)
	}
}

func run() error {
	env, err := core.NewEnv()
	if err != nil {
		return err
	}

	// goai.New returns the *disabled* model rather than an error when nothing is
	// configured, so a service that does not use AI still boots. A provider name
	// that is set but misspelled is an error: booting past a typo turns a
	// five-second fix into a runtime mystery.
	model, err := goai.New(env)
	if err != nil {
		return err
	}
	embedder, err := goai.NewEmbedder(env)
	if err != nil {
		return err
	}

	usingStandIn := !model.Enabled()
	if usingStandIn {
		model = core.NewMemoryLLM()
	}
	if !embedder.Enabled() {
		embedder = core.NewMemoryEmbedder(embedDimensions)
	}

	app, err := core.NewApp(env, core.WithLLM(model), core.WithEmbedder(embedder))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	defer func() { _ = app.Shutdown(context.Background()) }()

	c := app.NewContext(ctx, core.ModeTest)
	c.Log().Info("ai example starting",
		"provider", model.Provider(),
		"model", model.Model(),
		"embedder", embedder.Provider(),
		"stand_in", usingStandIn)

	// Tools are built once, at startup: describing an input type is reflection,
	// and a type that cannot be described is a bug that should stop the process
	// rather than the tenth request of the day.
	desk, err := NewSupportDesk()
	if err != nil {
		return err
	}

	demoGeneration(c, usingStandIn)
	demoTools(c, desk)
	demoEmbeddings(c)
	return demoAgentJob(app, desk)
}

// demoGeneration — 01_text.go and 02_typed.go. The memory model echoes the last
// user turn, so what the first half proves is the plumbing: the request was
// built, an answer came back with a finish reason, the usage was recorded.
//
// The second half deliberately shows the failure path when the stand-in is in
// use: the memory model answers "{}" to any schema, so the Validate rule
// rejects an invoice with no total rather than storing a zero as though it had
// been read.
func demoGeneration(c core.IContext, usingStandIn bool) {
	if out, err := summarize(c, "ยอดขายเดือนกรกฎาคมโต 12% จากเดือนก่อน สินค้าหมวดอาหารโตมากที่สุด"); err != nil {
		c.Log().Warn("summarise failed", "code", err.GetCode(), "err", err)
	} else {
		c.Log().Info("summarised", "text", out)
	}

	inv, err := extractInvoice(c, "ACME CO., LTD.  INV-2026-0731  Total 1,250.50 THB  Due 31/08/2026")
	switch {
	case err == nil:
		c.Log().Info("extracted invoice", "vendor", inv.Vendor, "total", inv.Total)
	case usingStandIn:
		c.Log().Info("validation rejected the stand-in's empty answer, as it should", "code", err.GetCode())
	default:
		c.Log().Warn("extraction failed", "code", err.GetCode(), "err", err)
	}
}

// demoTools — 04_tools.go. Given tools and no script, the memory model calls
// every one of them once and then answers — which is how a service that adds a
// tool gets its handler exercised without anyone writing a script for it. The
// refund is denied by the policy on the way through.
func demoTools(c core.IContext, desk *SupportDesk) {
	answer, err := desk.Answer(c, "ของออเดอร์ TH-1042 อยู่ไหนแล้ว และขอคืนเงินด้วย")
	if err != nil {
		c.Log().Warn("agent failed", "code", err.GetCode(), "err", err)
		return
	}
	c.Log().Info("agent answered", "text", answer)
}

// demoEmbeddings — 06_embeddings.go. The memory embedder hashes words into a
// vector: deterministic, so a ranking assertion never flakes, and meaningless,
// so it exercises the pipeline rather than the search quality.
func demoEmbeddings(c core.IContext) {
	corpus, err := indexChunks(c, "handbook", []string{
		"Parcels are dispatched within two working days.",
		"Refunds are returned to the original card within five working days.",
		"Our office is closed on public holidays.",
	})
	if err != nil {
		c.Log().Warn("indexing failed", "code", err.GetCode(), "err", err)
		return
	}
	answer, err := answerFromCorpus(c, "How long do refunds take?", corpus)
	if err != nil {
		c.Log().Warn("rag failed", "code", err.GetCode(), "err", err)
		return
	}
	c.Log().Info("answered from the corpus", "passages", len(corpus), "text", answer)
}
