package llm_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Sentiment struct {
	Label      string  `json:"label" jsonschema:"enum=positive|neutral|negative"`
	Confidence float64 `json:"confidence"`
}

// appWith wires a memory model into an App so core.LLM(ctx) finds it, which is
// exactly how a service under test is put together.
func appWith(t *testing.T, model core.ILLM) core.IContext {
	t.Helper()
	t.Setenv("APP_ENV", "test")
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	app, ierr := core.NewApp(env, core.WithLLM(model))
	require.NoError(t, ierr)
	return app.NewContext(context.Background(), core.ModeTest)
}

func TestTyped_extractParsesIntoTheType(t *testing.T) {
	m := core.NewMemoryLLM(`{"label":"positive","confidence":0.91}`)
	ctx := appWith(t, m)

	got, err := llm.New[Sentiment](ctx).
		System("classify the review").
		Extract("อาหารอร่อยมาก บริการดี")
	require.NoError(t, err)
	assert.Equal(t, "positive", got.Label)
	assert.InDelta(t, 0.91, got.Confidence, 0.0001)
}

func TestTyped_sendsTheSchemaDerivedFromTheType(t *testing.T) {
	m := core.NewMemoryLLM(`{"label":"neutral","confidence":0.5}`)
	ctx := appWith(t, m)

	_, err := llm.New[Sentiment](ctx).Extract("ก็โอเค")
	require.NoError(t, err)

	calls := core.LLMCalls(m)
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].Schema, "the whole point is that the caller never writes a schema by hand")
	assert.Equal(t, "sentiment", calls[0].Schema.Name)

	props := calls[0].Schema.Schema["properties"].(map[string]any)
	assert.Equal(t, []string{"positive", "neutral", "negative"},
		props["label"].(map[string]any)["enum"])
}

func TestTyped_builderIsImmutable(t *testing.T) {
	// A half-configured value is meant to be kept and reused — the shared
	// system prompt is usually the expensive part, and the whole reason to
	// cache it. Mutating in place would leak one call's input into the next.
	m := core.NewMemoryLLM(
		`{"label":"positive","confidence":1}`,
		`{"label":"negative","confidence":1}`,
	)
	ctx := appWith(t, m)

	base := llm.New[Sentiment](ctx).System("rubric").CacheSystem()
	_, err := base.Extract("first")
	require.NoError(t, err)
	_, err = base.Extract("second")
	require.NoError(t, err)

	calls := core.LLMCalls(m)
	require.Len(t, calls, 2)
	assert.Len(t, calls[0].Messages, 1, "the first call must not see the second's input")
	assert.Len(t, calls[1].Messages, 1, "and the second must not see the first's")
	assert.Equal(t, "first", calls[0].Messages[0].Text)
	assert.Equal(t, "second", calls[1].Messages[0].Text)
	assert.True(t, calls[1].CacheSystem, "options set on the base must survive the copy")
}

func TestTyped_optionsReachTheRequest(t *testing.T) {
	m := core.NewMemoryLLM(`{"label":"neutral","confidence":0.4}`)
	ctx := appWith(t, m)

	_, err := llm.New[Sentiment](ctx).
		System("sys").
		MaxTokens(512).
		Reasoning(core.LLMReasoningHigh).
		ProviderOptions(map[string]any{"foo": "bar"}).
		SchemaName("custom").
		Extract("x")
	require.NoError(t, err)

	req := core.LLMCalls(m)[0]
	assert.Equal(t, "sys", req.System)
	assert.Equal(t, 512, req.MaxTokens)
	assert.Equal(t, core.LLMReasoningHigh, req.Reasoning)
	assert.Equal(t, "bar", req.ProviderOptions["foo"])
	assert.Equal(t, "custom", req.Schema.Name)
}

func TestTyped_modelRoutesToAnotherModelOfTheSameProvider(t *testing.T) {
	// The alternative a service reaches for otherwise is a second client built
	// outside core.NewApp — which is not metered, so its tokens never appear in
	// llm.tokens.* and its calls never appear in a log.
	m := core.NewMemoryLLM(`{"label":"neutral","confidence":0.4}`)
	ctx := appWith(t, m)

	res, err := llm.New[Sentiment](ctx).Model("gemini-flash-lite-latest").Ask("x").Result()
	require.NoError(t, err)
	assert.Equal(t, "gemini-flash-lite-latest", core.LLMCalls(m)[0].Model)
	assert.Equal(t, "gemini-flash-lite-latest", res.Model,
		"the result names the model that was billed, not the one the App was configured with")
}

func TestTyped_usingRunsAgainstAHandleTheCallerBuilt(t *testing.T) {
	// A second provider entirely — the case Model cannot cover, since a model id
	// only means anything to the provider it belongs to.
	app := core.NewMemoryLLM(`{"label":"positive","confidence":0.9}`)
	other := core.NewMemoryLLM(`{"label":"negative","confidence":0.1}`)
	ctx := appWith(t, app)

	got, err := llm.New[Sentiment](ctx).Using(other).Extract("x")
	require.NoError(t, err)
	assert.Equal(t, "negative", got.Label, "the generation must go to the handle that was named")
	assert.Empty(t, core.LLMCalls(app), "and not to the application's model")
}

func TestTyped_resultCarriesUsageAndRawJSON(t *testing.T) {
	m := core.NewMemoryLLM()
	core.QueueLLMReply(m, core.MemoryLLMReply{
		Text:  `{"label":"positive","confidence":0.8}`,
		Usage: core.LLMUsage{InputTokens: 120, OutputTokens: 9},
	})
	ctx := appWith(t, m)

	res, err := llm.New[Sentiment](ctx).Ask("great").Result()
	require.NoError(t, err)
	assert.Equal(t, "positive", res.Value.Label)
	assert.Equal(t, 120, res.Usage.InputTokens, "cost per document is a normal thing to want")
	assert.JSONEq(t, `{"label":"positive","confidence":0.8}`, res.JSON,
		"the raw reply is what you attach to an incident when a value comes back wrong")
}

func TestTyped_unparseableReplyCarriesWhatTheModelSaid(t *testing.T) {
	m := core.NewMemoryLLM("I think it's positive, actually!")
	ctx := appWith(t, m)

	_, err := llm.New[Sentiment](ctx).Extract("x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "I think it's positive",
		`without the reply in the error, "invalid character 'I'" is unactionable and the reply is gone`)
}

func TestTyped_lenientUnwrapsAFencedReply(t *testing.T) {
	// Strict is the default because a model ignoring its schema is a prompt
	// worth fixing. Lenient exists for the small local models whose JSON mode
	// is weak, where the alternative is not using them at all.
	fenced := "Sure!\n```json\n{\"label\":\"negative\",\"confidence\":0.3}\n```\nHope that helps."
	m := core.NewMemoryLLM(fenced, fenced)
	ctx := appWith(t, m)

	_, err := llm.New[Sentiment](ctx).Extract("x")
	require.Error(t, err, "strict mode must not quietly repair a reply that ignored the schema")

	got, err := llm.New[Sentiment](ctx).Lenient().Extract("x")
	require.NoError(t, err)
	assert.Equal(t, "negative", got.Label)
}

func TestTyped_validateRunsOnTheParsedValue(t *testing.T) {
	m := core.NewMemoryLLM(`{"label":"positive","confidence":4.2}`)
	ctx := appWith(t, m)

	_, err := llm.New[Sentiment](ctx).
		Validate(func(s Sentiment) core.IError {
			if s.Confidence > 1 {
				return core.New(422, "BAD_CONFIDENCE", "confidence must be between 0 and 1")
			}
			return nil
		}).
		Extract("x")
	require.Error(t, err, "a JSON schema cannot express a numeric range on every provider — this is the gap it fills")
	assert.Equal(t, "BAD_CONFIDENCE", err.GetCode())
}

type Reviewed struct {
	Score int `json:"score"`
}

func (r *Reviewed) Valid(core.IContext) core.IError {
	if r.Score < 0 || r.Score > 10 {
		return core.New(422, "BAD_SCORE", "score must be 0-10")
	}
	return nil
}

func TestTyped_typeThatValidatesItselfIsCheckedAutomatically(t *testing.T) {
	// A type already used as an HTTP payload keeps its rules in one place
	// rather than restating them at every model call site.
	m := core.NewMemoryLLM(`{"score":42}`, `{"score":7}`)
	ctx := appWith(t, m)

	_, err := llm.New[Reviewed](ctx).Extract("x")
	require.Error(t, err)
	assert.Equal(t, "BAD_SCORE", err.GetCode())

	ok, err := llm.New[Reviewed](ctx).Extract("x")
	require.NoError(t, err)
	assert.Equal(t, 7, ok.Score)
}

func TestTyped_disabledModelFailsWithTheSameError(t *testing.T) {
	// No provider configured must read the same whether the caller went through
	// the typed layer or core.LLM directly.
	t.Setenv("APP_ENV", "test")
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	app, ierr := core.NewApp(env)
	require.NoError(t, ierr)

	_, gerr := llm.New[Sentiment](app.NewContext(context.Background(), core.ModeTest)).Extract("x")
	require.Error(t, gerr)
	assert.ErrorIs(t, gerr, core.ErrLLMDisabled)
}

func TestTyped_invalidSchemaFailsBeforeCallingTheModel(t *testing.T) {
	m := core.NewMemoryLLM()
	ctx := appWith(t, m)

	_, err := llm.New[Node](ctx).Extract("x")
	require.Error(t, err)
	assert.Equal(t, "LLM_INVALID_SCHEMA", err.GetCode())
	assert.Empty(t, core.LLMCalls(m), "a schema that cannot be built must not cost a request")
}

func TestTyped_messagesCarryAConversation(t *testing.T) {
	m := core.NewMemoryLLM(`{"label":"neutral","confidence":0.5}`)
	ctx := appWith(t, m)

	_, err := llm.New[Sentiment](ctx).
		Messages(
			core.LLMUser("first question"),
			core.LLMAssistant("first answer"),
			core.LLMUser("now classify it"),
		).
		Generate()
	require.NoError(t, err)

	msgs := core.LLMCalls(m)[0].Messages
	require.Len(t, msgs, 3)
	assert.Equal(t, core.LLMRoleAssistant, msgs[1].Role)
}

func TestTyped_worksFromAPlainContext(t *testing.T) {
	// A service function that takes context.Context and knows nothing about the
	// framework must still be able to run a typed generation.
	m := core.NewMemoryLLM(`{"label":"positive","confidence":1}`)
	ctx := appWith(t, m)

	var plain context.Context = ctx
	got, err := llm.New[Sentiment](plain).Extract("x")
	require.NoError(t, err)
	assert.Equal(t, "positive", got.Label)
}

func TestTyped_emptySchemaObjectStillUnmarshals(t *testing.T) {
	// The memory model answers "{}" when nothing is scripted, so a test that
	// only cares that the code path ran needs no fixture.
	m := core.NewMemoryLLM()
	ctx := appWith(t, m)

	got, err := llm.New[Sentiment](ctx).Extract("x")
	require.NoError(t, err)
	assert.Empty(t, got.Label)
}

func TestTyped_schemaIsValidJSON(t *testing.T) {
	// The schema is marshalled by the driver before it goes out; a map that
	// cannot be encoded would fail at the provider instead of here.
	s, err := llm.SchemaOf[Sentiment]()
	require.NoError(t, err)
	raw, jerr := json.Marshal(s.Schema)
	require.NoError(t, jerr)
	assert.True(t, strings.HasPrefix(string(raw), "{"))
}
