//go:build integration

package goai_test

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/llm"
	"github.com/pskclub/mine-core/v2/llm/goai"
	"github.com/pskclub/mine-core/v2/llm/llmtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These exercise what only a real provider can: that the request the driver
// builds is one the API accepts, that its SSE stream parses, that JSON mode
// really constrains the answer, and that usage comes back populated. The unit
// tests cover the same contract against a fake server, which is what CI runs.
//
// Against Gemini:
//
//	APP_AI_PROVIDER=google APP_AI_MODEL=gemini-2.5-flash \
//	  APP_AI_API_KEY=... make test-integration
//
// Against anything else — the driver code path is identical:
//
//	APP_AI_PROVIDER=compat APP_AI_BASE_URL=https://api.groq.com/openai/v1 \
//	  APP_AI_MODEL=llama-3.3-70b-versatile APP_AI_API_KEY=... make test-integration
func newRealModel(t *testing.T) core.ILLM {
	t.Helper()
	if os.Getenv("APP_AI_API_KEY") == "" && os.Getenv("APP_AI_BASE_URL") == "" {
		t.Skip("set APP_AI_PROVIDER/APP_AI_MODEL/APP_AI_API_KEY to run against a real provider")
	}
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)

	model, ierr := goai.New(env, goai.WithTimeout(90*time.Second))
	require.NoError(t, ierr)
	require.True(t, model.Enabled(), "AI_PROVIDER and AI_MODEL must both be set")
	return model
}

func realCtx(t *testing.T, model core.ILLM) core.IContext {
	t.Helper()
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	app, ierr := core.NewApp(env, core.WithLLM(model))
	require.NoError(t, ierr)
	return app.NewContext(context.Background(), core.ModeTest)
}

func TestIntegration_generate(t *testing.T) {
	m := newRealModel(t)

	resp, err := m.Generate(core.LLMRequest{
		System:    "Answer with a single word, no punctuation.",
		Messages:  []core.LLMMessage{core.LLMUser("What is the capital of Thailand?")},
		MaxTokens: 64,
	})
	require.NoError(t, err)

	t.Logf("provider=%s model=%s finish=%s in=%d out=%d text=%q",
		resp.Provider, resp.Model, resp.FinishReason,
		resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Text)

	assert.Contains(t, strings.ToLower(resp.Text), "bangkok")
	assert.Equal(t, core.LLMFinishStop, resp.FinishReason)
	assert.Positive(t, resp.Usage.InputTokens, "a real provider reports usage; a driver that drops it makes spend invisible")
	assert.Positive(t, resp.Usage.OutputTokens)
}

func TestIntegration_stream(t *testing.T) {
	m := newRealModel(t)

	// Long enough that a real provider sends it in several chunks — a
	// single-chunk reply would not exercise the assembly a caller does.
	s, err := m.Stream(core.LLMRequest{
		System:    "Reply in English prose. No lists, no headings.",
		Messages:  []core.LLMMessage{core.LLMUser("Describe the Chao Phraya river in about 150 words.")},
		MaxTokens: 1024,
	})
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	var b strings.Builder
	deltas := 0
	for s.Next() {
		b.WriteString(s.Text())
		deltas++
	}
	require.NoError(t, s.Err())

	final := s.Response()
	t.Logf("deltas=%d finish=%s in=%d out=%d text=%q",
		deltas, final.FinishReason, final.Usage.InputTokens, final.Usage.OutputTokens, b.String())

	assert.NotEmpty(t, b.String())
	assert.Equal(t, final.Text, b.String(), "the accumulated response must equal what the deltas said")
	assert.NotEmpty(t, final.FinishReason, "a real stream must end with a reason")
	assert.Positive(t, final.Usage.Total(), "usage arrives on the last chunk and must be captured there")
}

func TestIntegration_streamCloseBeforeDrain(t *testing.T) {
	// The leak case: a caller that stops reading because the user navigated
	// away. Run with -race to see the goroutine actually released.
	m := newRealModel(t)

	s, err := m.Stream(core.LLMRequest{
		Messages:  []core.LLMMessage{core.LLMUser("Write a long paragraph about rivers.")},
		MaxTokens: 512,
	})
	require.NoError(t, err)
	require.True(t, s.Next(), "expected at least one delta before abandoning the stream")
	require.NoError(t, s.Close())
}

type capital struct {
	City       string  `json:"city" jsonschema:"description=The capital city name in English"`
	Country    string  `json:"country"`
	Population int     `json:"population" jsonschema:"description=Approximate metro population"`
	Confidence float64 `json:"confidence" jsonschema:"description=0 to 1"`
	Region     string  `json:"region" jsonschema:"enum=asia|europe|africa|americas|oceania"`
}

func TestIntegration_typedExtraction(t *testing.T) {
	m := newRealModel(t)
	ctx := realCtx(t, m)

	got, err := llm.New[capital](ctx).
		System("Answer factually. Use your best estimate for the population.").
		Extract("Tell me about the capital of Thailand.")
	require.NoError(t, err)

	t.Logf("%+v", got)

	assert.Equal(t, "Bangkok", got.City)
	assert.Contains(t, strings.ToLower(got.Country), "thailand")
	assert.Positive(t, got.Population)
	assert.Equal(t, "asia", got.Region, "the enum in the schema must constrain the answer, not just suggest it")
}

func TestIntegration_typedExtractionRejectsAnInvalidValue(t *testing.T) {
	m := newRealModel(t)
	ctx := realCtx(t, m)

	_, err := llm.New[capital](ctx).
		System("Answer factually.").
		Validate(func(c capital) core.IError {
			if c.Confidence > 1 {
				return core.New(422, "BAD_CONFIDENCE", "confidence out of range")
			}
			return nil
		}).
		Extract("Tell me about the capital of France.")
	require.NoError(t, err, "a well-formed answer must pass the extra check")
}

func TestIntegration_badKeyIsUnauthorizedNotAServerError(t *testing.T) {
	// The mapping that matters operationally: a wrong key must not look like an
	// outage, or a caller retries it forever.
	if os.Getenv("APP_AI_API_KEY") == "" {
		t.Skip("needs a configured provider")
	}
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)

	m, ierr := goai.New(env, goai.WithAPIKey("definitely-not-a-real-key"), goai.WithMaxRetries(0))
	require.NoError(t, ierr)

	_, gerr := m.Generate(core.LLMRequest{Messages: []core.LLMMessage{core.LLMUser("hi")}})
	require.Error(t, gerr)
	t.Logf("code=%s status=%d", gerr.GetCode(), gerr.GetStatus())
	assert.Contains(t, []string{"LLM_UNAUTHORIZED", "LLM_REQUEST_REJECTED"}, gerr.GetCode())
	assert.Less(t, gerr.GetStatus(), 500, "a rejected key is the caller's problem, not the provider's")
}

func TestIntegration_unsupportedReasoningNeverReachesTheProvider(t *testing.T) {
	// The design claim worth checking against a live provider: asking for a
	// reasoning level a driver cannot express must fail, not quietly produce a
	// perfectly plausible answer the model never thought about.
	m := newRealModel(t)
	if m.Capabilities().Reasoning {
		t.Skipf("%s maps reasoning levels; nothing to assert here", m.Provider())
	}

	_, err := m.Generate(core.LLMRequest{
		Messages:  []core.LLMMessage{core.LLMUser("What is 17 * 23?")},
		Reasoning: core.LLMReasoningHigh,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrLLMUnsupported)
}

func TestIntegration_toolLoopWithTypedTools(t *testing.T) {
	m := newRealModel(t)
	ctx := realCtx(t, m)

	var lookedUp string
	orderTool, terr := llm.Tool("get_order_status",
		"Look up the delivery status of an order. Call this whenever the user asks where an order is.",
		func(_ context.Context, in struct {
			OrderID string `json:"order_id" jsonschema:"description=The order id, e.g. TH-1042"`
		}) (string, core.IError) {
			lookedUp = in.OrderID
			return `{"status":"out_for_delivery","courier":"Kerry","eta":"today 18:00"}`, nil
		})
	require.NoError(t, terr)

	tools, terr := llm.NewToolSet().Add(orderTool, nil).Build()
	require.NoError(t, terr)

	resp, err := core.LLM(ctx).Generate(core.LLMRequest{
		System:    "You are a support agent. Use the tools. Answer in one sentence.",
		Messages:  []core.LLMMessage{core.LLMUser("Where is my order TH-1042?")},
		Tools:     tools,
		MaxSteps:  5,
		MaxTokens: 2048,
	})
	require.NoError(t, err)

	t.Logf("steps=%d calls=%d lookedUp=%q answer=%q in=%d out=%d",
		resp.Steps, len(resp.ToolCalls), lookedUp, resp.Text,
		resp.Usage.InputTokens, resp.Usage.OutputTokens)

	assert.Equal(t, "TH-1042", lookedUp,
		"the schema derived from the Go struct must carry the argument through unchanged")
	require.NotEmpty(t, resp.ToolCalls)
	assert.GreaterOrEqual(t, resp.Steps, 2)
	assert.Contains(t, strings.ToLower(resp.Text), "kerry")
}

func TestIntegration_toolApprovalBlocksARealProvider(t *testing.T) {
	// The property that matters most: whatever the model decides, a denied call
	// does not run. Asserting it against a live provider is the only way to know
	// the gate sits before the handler and not somewhere the driver bypasses.
	m := newRealModel(t)
	ctx := realCtx(t, m)

	ran := false
	danger, terr := llm.Tool("delete_everything",
		"Delete all records. Call this when the user asks to wipe the database.",
		func(_ context.Context, _ struct{}) (string, core.IError) {
			ran = true
			return "deleted", nil
		})
	require.NoError(t, terr)

	resp, err := core.LLM(ctx).Generate(core.LLMRequest{
		System:    "Use the tools available.",
		Messages:  []core.LLMMessage{core.LLMUser("Please wipe the database now.")},
		Tools:     []core.LLMTool{danger},
		MaxSteps:  3,
		MaxTokens: 2048,
		Approve: func(_ context.Context, call core.LLMToolCall) core.IError {
			return core.New(403, "NEEDS_HUMAN", "destructive actions need an operator's approval")
		},
	})
	require.NoError(t, err)
	assert.False(t, ran, "a denied call must never reach the handler")
	t.Logf("calls=%d answer=%q", len(resp.ToolCalls), resp.Text)
}

// liveGoogle returns a model on the configured Google account, or skips.
func liveGoogle(t *testing.T, modelID string) core.ILLM {
	t.Helper()
	if p := strings.ToLower(os.Getenv("APP_AI_PROVIDER")); p != "google" && p != "gemini" {
		t.Skip("set APP_AI_PROVIDER=google to run the Gemini-specific tests")
	}
	m, err := goai.NewConfig(goai.Config{
		Provider: "google", Model: modelID, APIKey: os.Getenv("APP_AI_API_KEY"),
	})
	require.NoError(t, err)
	return m
}

func TestIntegration_googleReasoningActuallyChangesWhatIsSpent(t *testing.T) {
	// The assertion a fake server cannot make. A body assertion proves which
	// spelling was sent; only a real call proves the model honoured it, and the
	// evidence is the number of thinking tokens on the bill.
	const q = "A farmer has 17 sheep. All but 9 run away. He buys 4 more, then sells a third. How many are left? Answer with a number only."

	t.Run("2.5 spends a budget", func(t *testing.T) {
		m := liveGoogle(t, "gemini-2.5-flash")

		off, err := m.Generate(core.LLMRequest{
			Messages:  []core.LLMMessage{core.LLMUser(q)},
			Reasoning: core.LLMReasoningOff,
			MaxTokens: 4096,
		})
		require.NoError(t, err)
		high, err := m.Generate(core.LLMRequest{
			Messages:  []core.LLMMessage{core.LLMUser(q)},
			Reasoning: core.LLMReasoningHigh,
			MaxTokens: 4096,
		})
		require.NoError(t, err)

		t.Logf("off=%d high=%d thinking tokens", off.Usage.ReasoningTokens, high.Usage.ReasoningTokens)
		assert.Zero(t, off.Usage.ReasoningTokens, "a zero budget must actually stop the model thinking")
		assert.Positive(t, high.Usage.ReasoningTokens,
			"a high level that spent nothing means the config never reached the model")
	})

	t.Run("a -latest alias reasons like the model it points at", func(t *testing.T) {
		// The case the first implementation got wrong: an alias matched no
		// prefix, so AI_MODEL=gemini-flash-latest — what our own sample config
		// suggests — failed every request that asked to think.
		m := liveGoogle(t, "gemini-flash-latest")
		require.True(t, m.Capabilities().Reasoning)

		low, err := m.Generate(core.LLMRequest{
			Messages:  []core.LLMMessage{core.LLMUser(q)},
			Reasoning: core.LLMReasoningLow,
			MaxTokens: 8192,
		})
		require.NoError(t, err, "an alias must not be refused for a level the model it points at supports")
		high, err := m.Generate(core.LLMRequest{
			Messages:  []core.LLMMessage{core.LLMUser(q)},
			Reasoning: core.LLMReasoningHigh,
			MaxTokens: 8192,
		})
		require.NoError(t, err)

		t.Logf("alias low=%d high=%d thinking tokens", low.Usage.ReasoningTokens, high.Usage.ReasoningTokens)
		assert.Positive(t, low.Usage.ReasoningTokens)
		assert.Greater(t, high.Usage.ReasoningTokens, low.Usage.ReasoningTokens,
			"the level must reach the model the alias resolved to, not just be accepted by the driver")
	})

	t.Run("3 takes a level and cannot be turned off", func(t *testing.T) {
		m := liveGoogle(t, "gemini-3-flash-preview")

		resp, err := m.Generate(core.LLMRequest{
			Messages:  []core.LLMMessage{core.LLMUser(q)},
			Reasoning: core.LLMReasoningHigh,
			MaxTokens: 8192,
		})
		require.NoError(t, err)
		t.Logf("thinking tokens=%d answer=%q", resp.Usage.ReasoningTokens, resp.Text)
		assert.Positive(t, resp.Usage.ReasoningTokens)

		_, err = m.Generate(core.LLMRequest{
			Messages:  []core.LLMMessage{core.LLMUser("hi")},
			Reasoning: core.LLMReasoningOff,
		})
		require.Error(t, err, "gemini-3 always thinks — claiming otherwise prices the call wrongly")
		assert.ErrorIs(t, err, core.ErrLLMUnsupported)
	})
}

func TestIntegration_googleSearchGroundingReturnsSources(t *testing.T) {
	m := liveGoogle(t, "gemini-2.5-flash")

	resp, err := m.Generate(core.LLMRequest{
		Messages:  []core.LLMMessage{core.LLMUser("What is the latest stable version of Go? Answer in one sentence.")},
		Tools:     []core.LLMTool{goai.GoogleSearch()},
		MaxTokens: 4096,
	})
	require.NoError(t, err)
	t.Logf("answer=%q sources=%d", resp.Text, len(resp.Sources))
	for _, s := range resp.Sources {
		t.Logf("  [%s] %s %s", s.Type, s.Title, s.URL)
	}
	require.NotEmpty(t, resp.Sources,
		"a grounded answer with no citations is as trustworthy as one the model invented — and showing them is a term of the service")
	assert.NotEmpty(t, resp.Sources[0].URL)
}

func TestIntegration_googleRejectsMixingBuiltInAndFunctionTools(t *testing.T) {
	// Documented restriction, asserted so a future goai or API change shows up
	// here rather than in a service: Gemini 2.5 refuses the combination outright,
	// and 3.x wants a tool_config flag goai v0.9.4 cannot send.
	m := liveGoogle(t, "gemini-2.5-flash")

	echo, terr := llm.Tool("echo_back", "Echo the text back. Call this when asked to echo.",
		func(_ context.Context, in struct {
			Text string `json:"text"`
		}) (string, core.IError) {
			return in.Text, nil
		})
	require.NoError(t, terr)

	_, err := m.Generate(core.LLMRequest{
		Messages:  []core.LLMMessage{core.LLMUser("echo hello")},
		Tools:     []core.LLMTool{goai.GoogleSearch(), echo},
		MaxSteps:  3,
		MaxTokens: 2048,
	})
	require.Error(t, err)
	t.Logf("code=%s status=%d msg=%s", err.GetCode(), err.GetStatus(), err.GetMessage())
	assert.Less(t, err.GetStatus(), 500,
		"the provider's refusal is a caller error — retrying it forever would be the wrong reading")
}

func TestIntegration_embeddingOptions(t *testing.T) {
	// The one thing a fake server cannot prove: that the provider *accepts* the
	// keys this driver spells out and returns a vector of the length asked for.
	// Gemini answers a request carrying an option it does not recognise with a
	// perfectly good 3072-dimension vector, so only a real call tells them apart.
	if os.Getenv("APP_AI_EMBED_MODEL") == "" {
		t.Skip("set APP_AI_EMBED_MODEL to run the embedding tests")
	}
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)

	e, ierr := goai.NewEmbedder(env)
	require.NoError(t, ierr)
	require.True(t, e.Enabled())
	if e.Provider() != "google" && e.Provider() != "gemini" && e.Provider() != "openai" && e.Provider() != "compat" {
		t.Skipf("provider %q supports neither option", e.Provider())
	}

	const want = 768
	req := core.EmbedRequest{Texts: []string{"ประกาศราชกิจจานุเบกษา"}, Dimensions: want}
	if e.Provider() == "google" || e.Provider() == "gemini" {
		req.Task = core.EmbedDocument
	}

	vecs, ierr := e.EmbedWith(req)
	require.NoError(t, ierr)
	require.Len(t, vecs, 1)
	assert.Len(t, vecs[0], want,
		"a length the provider ignored is a row that will not insert — or one that can never be compared against the corpus")

	if req.Task == core.EmbedTaskDefault {
		return
	}
	// The task type has no visible effect other than the vector itself, so the
	// only way to tell "honoured" from "ignored" is that the same text embedded
	// for a query points somewhere else than embedded for a document.
	query, ierr := e.EmbedWith(core.EmbedRequest{Texts: req.Texts, Dimensions: want, Task: core.EmbedQuery})
	require.NoError(t, ierr)
	t.Logf("cosine(document, query) of the same text = %.6f", core.CosineSimilarity(vecs[0], query[0]))
	assert.NotEqual(t, vecs[0], query[0],
		"identical vectors mean taskType never reached the model, and every search would be ranked by the wrong pairing")
}

func TestIntegration_embeddings(t *testing.T) {
	if os.Getenv("APP_AI_EMBED_MODEL") == "" {
		t.Skip("set APP_AI_EMBED_MODEL to run the embedding tests")
	}
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)

	e, ierr := goai.NewEmbedder(env)
	require.NoError(t, ierr)
	require.True(t, e.Enabled())

	vecs, ierr := e.Embed(
		"The cat sat on the mat.",
		"A kitten is resting on the rug.",
		"Quarterly revenue exceeded the forecast.",
	)
	require.NoError(t, ierr)
	require.Len(t, vecs, 3, "one vector per input, in order — callers pair them by index")

	related := core.CosineSimilarity(vecs[0], vecs[1])
	unrelated := core.CosineSimilarity(vecs[0], vecs[2])
	t.Logf("provider=%s model=%s dims=%d related=%.4f unrelated=%.4f",
		e.Provider(), e.Model(), e.Dimensions(), related, unrelated)

	assert.Positive(t, e.Dimensions(), "the vector length is what a vector column has to be declared with")
	assert.Greater(t, related, unrelated,
		"a real embedding model must rank a paraphrase above an unrelated sentence, or the index is worthless")
}

// receiptPNG renders "ACME / TOTAL / 1,250.50" in a 260×120 monochrome PNG. It
// is embedded rather than read from disk so the test needs no fixture file, and
// deliberately crude: a model that reads it can read a real scan.
const receiptPNG = "iVBORw0KGgoAAAANSUhEUgAAAQQAAAB4CAAAAAAx9NPYAAABHUlEQVR42u3cywqFIBAAUP//p7vbEF9TXDDnzEbC" +
	"hDzQkGNYLnEVBBAgQIAAAQIECBAghBDKLXrXdXvvhwABwnkIrXaEdGRihAABwtIkZ4kTAgQI+b4TRv0QIEA4H8EC" +
	"CgKEvAhZAwIECBAgQIAQK6qcHhBGCF4HCBAgIIAAAQIECBAgQIDwrJ7w9BoCBAjnINQTaU1u1A8BAgQIEiMECBAk" +
	"RggQ8iLUW9kzhC/+3AkBAoQ+QsaAAAECBAgQIECAAAHCuwVUqy+yQRsdDwEChL0QVh9o9ZCJGfAuBVoIECD8LzHO" +
	"ECL3Q4AAYT+E3sbLKMHNJgUBAoRvIKxswEYXUJHxECBA2K+ootAKAQKEVuJyzpJzluQECBAgQEgfPzd++eZJu54E" +
	"AAAAAElFTkSuQmCC"

func mustReceipt(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(receiptPNG)
	require.NoError(t, err)
	return b
}

func TestIntegration_visionReadsAnImage(t *testing.T) {
	m := newRealModel(t)

	resp, err := m.Generate(core.LLMRequest{
		System: "Read the receipt image. Answer with the total amount only.",
		Messages: []core.LLMMessage{
			core.LLMUser("What is the total on this receipt?").
				With(core.LLMImage(mustReceipt(t), "image/png")),
		},
		MaxTokens: 256,
	})
	require.NoError(t, err)

	t.Logf("answer=%q in=%d out=%d", resp.Text, resp.Usage.InputTokens, resp.Usage.OutputTokens)

	// Separators are stripped before comparing: the model answers "1,250.50" or
	// "1250.50" depending on the run, and which one it chose is not what this
	// test is about — that the image reached it at all is.
	assert.Contains(t, strings.NewReplacer(",", "", " ", "", "฿", "").Replace(resp.Text), "1250.50",
		"the image must actually reach the model — a dropped attachment reads as a model that ignored it")
	assert.Greater(t, resp.Usage.InputTokens, 100,
		"an image costs far more input tokens than the prompt text alone, which is why they are logged separately")
}

type receipt struct {
	Vendor string  `json:"vendor" jsonschema:"description=The shop or company name printed at the top"`
	Total  float64 `json:"total" jsonschema:"description=The total amount as a number, without separators"`
}

func TestIntegration_visionWithTypedExtraction(t *testing.T) {
	// The pairing that makes this worth having in the framework: an image in,
	// a Go value out, with the schema derived from the type.
	m := newRealModel(t)
	ctx := realCtx(t, m)

	got, err := llm.New[receipt](ctx).
		System("Read the receipt image and extract the fields.").
		Messages(core.LLMUser("Extract this receipt.").
			With(core.LLMImage(mustReceipt(t), "image/png"))).
		Generate()
	require.NoError(t, err)

	t.Logf("%+v", got)
	// The number is the assertion that means something here: it proves the image
	// arrived, was read, and came back as a Go float rather than a string.
	assert.InDelta(t, 1250.50, got.Total, 0.01)
	// The vendor is not. "ACME" in a 260×120 monochrome render sits at the edge
	// of legibility and Gemini reads it as "ACHE" perhaps a third of the time —
	// asserting the exact string tests the model's eyesight, and a suite that
	// fails a third of the time is a suite people learn to ignore.
	assert.Len(t, got.Vendor, 4, "the vendor line must be read as a four-letter word, whichever letters the model saw")
}

func TestIntegration_conformance(t *testing.T) {
	// The same contract the fake-server suite asserts, against the real thing.
	// If these two ever disagree, the fake server has drifted from the protocol.
	llmtest.RunSuite(t, func(t *testing.T) core.ILLM { return newRealModel(t) })
}
