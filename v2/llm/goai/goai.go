// Package goai adapts github.com/zendev-sh/goai to core.ILLM, so a service can
// talk to any of that library's providers through the framework's own
// interface.
//
// The adapter exists rather than exposing goai directly for one reason: goai is
// pre-1.0 and releases every few days. Everything a service imports comes from
// core, so an upstream break is a change to this one file rather than a change
// to every service — and swapping the engine later costs nothing above it.
//
// Wiring, at startup:
//
//	model, err := goai.New(env)
//	if err != nil { log.Fatal(err) }
//	app, _ := core.NewApp(env, core.WithLLM(model))
//
// and then, from a handler or a job:
//
//	resp, err := core.LLM(ctx).Generate(core.LLMRequest{
//	    System:   "Answer in one sentence.",
//	    Messages: []core.LLMMessage{core.LLMUser(q)},
//	})
package goai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	core "github.com/pskclub/mine-core/v2"
	sdk "github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/goai/provider/anthropic"
	"github.com/zendev-sh/goai/provider/compat"
	"github.com/zendev-sh/goai/provider/google"
	"github.com/zendev-sh/goai/provider/ollama"
	"github.com/zendev-sh/goai/provider/openai"
)

// Defaults applied when configuration says nothing. The timeout is minutes
// rather than seconds because a reasoning model on a hard prompt genuinely
// takes that long — an HTTP-shaped timeout would cut off answers that were
// about to arrive, and be billed for anyway.
const (
	defaultMaxTokens  = 4096
	defaultTimeout    = 120 * time.Second
	defaultMaxRetries = 2
)

// Config is what the driver needs. New fills it from IENV; the options override
// individual fields for a test or a second model in the same process.
type Config struct {
	Provider   string
	Model      string
	APIKey     string
	BaseURL    string
	MaxTokens  int
	Timeout    time.Duration
	MaxRetries int
	HTTPClient *http.Client
}

// Option overrides one piece of configuration.
type Option func(*Config)

func WithProvider(name string) Option { return func(c *Config) { c.Provider = name } }
func WithModel(id string) Option      { return func(c *Config) { c.Model = id } }
func WithAPIKey(key string) Option    { return func(c *Config) { c.APIKey = key } }

// WithBaseURL points the provider at another endpoint — a gateway, a proxy, or
// a local server. With provider "compat" it is required: that is how every
// OpenAI-compatible service this driver does not name explicitly is reached.
func WithBaseURL(url string) Option { return func(c *Config) { c.BaseURL = url } }

func WithMaxTokens(n int) Option           { return func(c *Config) { c.MaxTokens = n } }
func WithTimeout(d time.Duration) Option   { return func(c *Config) { c.Timeout = d } }
func WithMaxRetries(n int) Option          { return func(c *Config) { c.MaxRetries = n } }
func WithHTTPClient(h *http.Client) Option { return func(c *Config) { c.HTTPClient = h } }

// New builds a model from AI_* configuration.
//
// It returns the disabled model — not an error — when no provider or model is
// configured, so a service that does not use AI still starts, and one that does
// fails at the call with LLM_DISABLED naming what is missing. A provider name
// that is set but unknown *is* an error: it is a typo, and booting past it would
// turn a five-second fix into a runtime mystery.
func New(env core.IENV, opts ...Option) (core.ILLM, core.IError) {
	cfg := Config{}
	if env != nil {
		c := env.Config()
		cfg = Config{
			Provider:   c.AIProvider,
			Model:      c.AIModel,
			APIKey:     c.AIAPIKey,
			BaseURL:    c.AIBaseURL,
			MaxTokens:  c.AIMaxTokens,
			MaxRetries: c.AIMaxRetries,
			Timeout:    time.Duration(c.AITimeout) * time.Second,
		}
	}
	for _, o := range opts {
		o(&cfg)
	}
	return NewConfig(cfg)
}

// NewConfig builds a model from an explicit configuration, for a service that
// loads its own or needs a second model alongside the configured one.
func NewConfig(cfg Config) (core.ILLM, core.IError) {
	name := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if name == "" || cfg.Model == "" {
		return core.NewNoopLLM(), nil
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = defaultMaxRetries
	}

	model, err := buildModel(name, cfg)
	if err != nil {
		return nil, err
	}
	return &llm{
		cfg:       cfg,
		provider:  name,
		model:     model,
		ctx:       context.Background(),
		siblings:  &sync.Map{},
		buildable: true,
	}, nil
}

// NewModel wraps a model the caller built itself. It is the way to reach a
// provider this driver does not name — goai has more than twenty — and the way
// a test points at an httptest server.
func NewModel(providerName string, model provider.LanguageModel, opts ...Option) core.ILLM {
	cfg := Config{Provider: providerName, MaxTokens: defaultMaxTokens, Timeout: defaultTimeout, MaxRetries: defaultMaxRetries}
	for _, o := range opts {
		o(&cfg)
	}
	return &llm{cfg: cfg, provider: strings.ToLower(providerName), model: model, ctx: context.Background()}
}

// buildModel maps a provider name to a goai model.
//
// Only providers whose wire shape or auth genuinely differs are named here.
// Everything else — Groq, DeepSeek, OpenRouter, Together, vLLM and the rest —
// speaks the OpenAI protocol, so "compat" plus AI_BASE_URL reaches it without
// this switch having to grow a case per vendor.
func buildModel(name string, cfg Config) (provider.LanguageModel, core.IError) {
	switch name {
	case "anthropic":
		o := []anthropic.Option{}
		if cfg.APIKey != "" {
			o = append(o, anthropic.WithAPIKey(cfg.APIKey))
		}
		if cfg.BaseURL != "" {
			o = append(o, anthropic.WithBaseURL(cfg.BaseURL))
		}
		if cfg.HTTPClient != nil {
			o = append(o, anthropic.WithHTTPClient(cfg.HTTPClient))
		}
		return anthropic.Chat(cfg.Model, o...), nil

	case "openai":
		o := []openai.Option{}
		if cfg.APIKey != "" {
			o = append(o, openai.WithAPIKey(cfg.APIKey))
		}
		if cfg.BaseURL != "" {
			o = append(o, openai.WithBaseURL(cfg.BaseURL))
		}
		if cfg.HTTPClient != nil {
			o = append(o, openai.WithHTTPClient(cfg.HTTPClient))
		}
		return openai.Chat(cfg.Model, o...), nil

	case "google", "gemini":
		o := []google.Option{}
		if cfg.APIKey != "" {
			o = append(o, google.WithAPIKey(cfg.APIKey))
		}
		if cfg.BaseURL != "" {
			o = append(o, google.WithBaseURL(cfg.BaseURL))
		}
		if cfg.HTTPClient != nil {
			o = append(o, google.WithHTTPClient(cfg.HTTPClient))
		}
		return google.Chat(cfg.Model, o...), nil

	case "ollama":
		o := []ollama.Option{}
		if cfg.BaseURL != "" {
			o = append(o, ollama.WithBaseURL(cfg.BaseURL))
		}
		if cfg.HTTPClient != nil {
			o = append(o, ollama.WithHTTPClient(cfg.HTTPClient))
		}
		return ollama.Chat(cfg.Model, o...), nil

	case "compat":
		if cfg.BaseURL == "" {
			return nil, core.New(400, "LLM_INVALID_CONFIG",
				"llm: provider \"compat\" needs AI_BASE_URL — it is the address of the OpenAI-compatible service to call")
		}
		o := []compat.Option{compat.WithBaseURL(cfg.BaseURL)}
		if cfg.APIKey != "" {
			o = append(o, compat.WithAPIKey(cfg.APIKey))
		}
		if cfg.HTTPClient != nil {
			o = append(o, compat.WithHTTPClient(cfg.HTTPClient))
		}
		return compat.Chat(cfg.Model, o...), nil
	}

	return nil, core.Newf(400, "LLM_INVALID_CONFIG",
		"llm: unknown AI_PROVIDER %q (known: anthropic, openai, google, ollama, compat — use compat with AI_BASE_URL for any other OpenAI-compatible service)",
		cfg.Provider)
}

type llm struct {
	cfg      Config
	provider string
	model    provider.LanguageModel
	ctx      context.Context

	// siblings caches the clients built for a per-request LLMRequest.Model. It
	// is a pointer because WithContext copies the struct per request, and two
	// handles that each built their own client for the same model would double
	// the connection pools for no reason.
	siblings *sync.Map // model id -> provider.LanguageModel
	// buildable says whether this handle knows enough to build a sibling — true
	// for one built from a configuration, false for one wrapping a model the
	// caller made, whose credentials and endpoint live in a closure this code
	// cannot read back out.
	buildable bool
}

var _ core.ILLM = (*llm)(nil)

func (l *llm) Model() string    { return l.cfg.Model }
func (l *llm) Provider() string { return l.provider }
func (l *llm) Enabled() bool    { return true }
func (l *llm) Unwrap() any      { return l.model }
func (l *llm) Close() core.IError {
	// goai holds no pool of its own: it uses the http.Client it was given, whose
	// lifetime belongs to whoever created it.
	return nil
}

func (l *llm) WithContext(ctx context.Context) core.ILLM {
	cp := *l
	cp.ctx = ctx
	return &cp
}

func (l *llm) Capabilities() core.LLMCapabilities {
	return core.LLMCapabilities{
		Streaming:        true,
		PromptCaching:    true,
		StructuredOutput: true,
		Tools:            true,
		// Every provider this driver speaks to carries attachments in its wire
		// format. Whether the *configured model* can read one is a different
		// question the driver cannot answer — see the field's own comment.
		Vision:    true,
		Reasoning: l.reasoningSupported(),
		// No provider here exposes a token-counting endpoint through goai, and
		// an estimate is worse than nothing: it is the number a caller uses to
		// decide whether a prompt fits, so a plausible wrong one is trusted.
		TokenCounting: false,
	}
}

func (l *llm) reasoningSupported() bool {
	switch l.provider {
	case "anthropic", "openai", "compat":
		return true
	case "google", "gemini":
		// Unlike the others, only some of this provider's models think at all —
		// and a capability that claimed otherwise would send a caller down a
		// path that fails per request instead of at the branch. This is also the
		// signal a service can warn on at boot when its configured model turns
		// out not to reason.
		return geminiStyleOf(l.cfg.Model) != geminiNoThinking
	}
	return false
}

func (l *llm) Generate(req core.LLMRequest) (core.LLMResponse, core.IError) {
	rec := &callRecorder{}
	call, err := l.prepare(req, rec)
	if err != nil {
		return core.LLMResponse{}, err
	}

	ctx, cancel := l.callContext()
	defer cancel()

	if req.Schema != nil {
		return l.generateStructured(ctx, req, call, rec)
	}

	res, gerr := sdk.GenerateText(ctx, call.model, call.opts...)
	if gerr != nil {
		return core.LLMResponse{}, l.mapError(gerr)
	}
	return core.LLMResponse{
		Text:         res.Text,
		Model:        call.modelID,
		Provider:     l.provider,
		FinishReason: mapFinish(res.FinishReason),
		Usage:        mapUsage(res.TotalUsage),
		Steps:        max(len(res.Steps), 1),
		ToolCalls:    rec.all(),
		Sources:      mapSources(res.Sources),
		Raw:          res,
	}, nil
}

// generateStructured asks the provider to answer in its native JSON mode.
//
// The result type is json.RawMessage rather than the caller's own: this
// interface deals in text, and the typed layer above it (llm.New[T]) owns
// unmarshalling and validation. Passing the schema explicitly also skips the
// reflection the SDK would otherwise do on the type parameter, which is the
// point — the schema was already built from the caller's type.
func (l *llm) generateStructured(ctx context.Context, req core.LLMRequest, call preparedCall, rec *callRecorder) (core.LLMResponse, core.IError) {
	raw, jerr := json.Marshal(req.Schema.Schema)
	if jerr != nil {
		return core.LLMResponse{}, core.Wrap(jerr, "llm: schema cannot be encoded")
	}
	name := req.Schema.Name
	if name == "" {
		name = "response"
	}
	opts := append(call.opts, sdk.WithExplicitSchema(raw), sdk.WithSchemaName(name))

	res, gerr := sdk.GenerateObject[json.RawMessage](ctx, call.model, opts...)
	if gerr != nil {
		return core.LLMResponse{}, l.mapError(gerr)
	}
	return core.LLMResponse{
		Text:         string(res.Object),
		Model:        call.modelID,
		Provider:     l.provider,
		FinishReason: mapFinish(res.FinishReason),
		Usage:        mapUsage(res.Usage),
		Steps:        max(len(res.Steps), 1),
		ToolCalls:    rec.all(),
		Raw:          res,
	}, nil
}

func (l *llm) Stream(req core.LLMRequest) (core.LLMStream, core.IError) {
	rec := &callRecorder{}
	call, err := l.prepare(req, rec)
	if err != nil {
		return nil, err
	}

	ctx, cancel := l.callContext()

	s, gerr := sdk.StreamText(ctx, call.model, call.opts...)
	if gerr != nil {
		cancel()
		return nil, l.mapError(gerr)
	}
	return &stream{
		src:    s.Stream(),
		text:   s,
		cancel: cancel,
		owner:  l,
		rec:    rec,
		resp: core.LLMResponse{
			Model:    call.modelID,
			Provider: l.provider,
		},
	}, nil
}

func (l *llm) CountTokens(req core.LLMRequest) (int, core.IError) {
	if err := req.Validate(); err != nil {
		return 0, err
	}
	// An estimate would be worse than nothing here: it is the number a caller
	// uses to decide whether a prompt fits, and a plausible wrong one is
	// trusted right up until the request is rejected.
	return 0, core.LLMUnsupportedError(l.provider, "token counting")
}

// callContext bounds one call. The driver's own timeout is applied on top of
// whatever deadline the request already carries, so a generation cannot outlive
// the HTTP request that started it — and cannot run forever when it was started
// by something with no deadline at all, such as a queue consumer.
func (l *llm) callContext() (context.Context, context.CancelFunc) {
	base := l.ctx
	if base == nil {
		base = context.Background()
	}
	return context.WithTimeout(base, l.cfg.Timeout)
}

// preparedCall is one request resolved against this handle: which client will
// serve it, what it is called, and the options it runs with. The three travel
// together because the model id is no longer a property of the handle — a
// request may name its own — and a response that reported the configured model
// while a different one was billed is the exact confusion this replaces.
type preparedCall struct {
	model   provider.LanguageModel
	modelID string
	opts    []sdk.Option
}

func (l *llm) prepare(req core.LLMRequest, rec *callRecorder) (preparedCall, core.IError) {
	if err := req.Validate(); err != nil {
		return preparedCall{}, err
	}

	out := preparedCall{model: l.model, modelID: l.cfg.Model}
	if req.Model != "" && req.Model != l.cfg.Model {
		model, err := l.modelFor(req.Model)
		if err != nil {
			return preparedCall{}, err
		}
		out.model, out.modelID = model, req.Model
	}

	opts, err := l.options(req, out.modelID, rec)
	if err != nil {
		return preparedCall{}, err
	}
	out.opts = opts
	return out, nil
}

// modelFor returns the client for a per-request model id, building it once and
// reusing it after.
//
// Routing a request to another model is ordinary — a cheap model for
// classification and an expensive one for the answer, in the same handler — and
// making each service build a second client for it costs more than the client:
// a model constructed outside core.NewApp is not instrumented, so its tokens
// never reach llm.tokens.* and its calls never appear in a log.
func (l *llm) modelFor(id string) (provider.LanguageModel, core.IError) {
	if !l.buildable {
		return nil, core.Newf(400, "LLM_INVALID_REQUEST",
			"llm: this handle wraps a model built by the caller and is bound to %q; build a second model for %q",
			l.cfg.Model, id)
	}
	if l.siblings != nil {
		if cached, ok := l.siblings.Load(id); ok {
			return cached.(provider.LanguageModel), nil
		}
	}

	cfg := l.cfg
	cfg.Model = id
	model, err := buildModel(l.provider, cfg)
	if err != nil {
		return nil, err
	}
	if l.siblings != nil {
		// LoadOrStore rather than Store: two requests racing on the same new
		// model must end up sharing one client, not building one each.
		if actual, loaded := l.siblings.LoadOrStore(id, model); loaded {
			return actual.(provider.LanguageModel), nil
		}
	}
	return model, nil
}

func (l *llm) options(req core.LLMRequest, modelID string, rec *callRecorder) ([]sdk.Option, core.IError) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = l.cfg.MaxTokens
	}

	opts := []sdk.Option{
		sdk.WithMessages(toMessages(req)...),
		sdk.WithMaxOutputTokens(maxTokens),
		sdk.WithMaxRetries(l.cfg.MaxRetries),
	}
	if req.System != "" {
		opts = append(opts, sdk.WithSystem(req.System))
	}
	if len(req.StopSequences) > 0 {
		opts = append(opts, sdk.WithStopSequences(req.StopSequences...))
	}
	if req.Temperature != nil {
		opts = append(opts, sdk.WithTemperature(*req.Temperature))
	}
	if req.CacheSystem {
		opts = append(opts, sdk.WithPromptCaching(true))
	}
	if len(req.Tools) > 0 {
		opts = append(opts, sdk.WithTools(l.toTools(req, rec)...), sdk.WithMaxSteps(req.MaxSteps))
	}

	providerOpts, err := l.providerOptions(req, modelID)
	if err != nil {
		return nil, err
	}
	if len(providerOpts) > 0 {
		opts = append(opts, sdk.WithProviderOptions(providerOpts))
	}
	return opts, nil
}

// providerOptions translates the normalised fields that have no single wire
// form, and merges the caller's own escape-hatch values on top — the caller
// wins, because they asked for the provider's own spelling on purpose.
func (l *llm) providerOptions(req core.LLMRequest, modelID string) (map[string]any, core.IError) {
	out := map[string]any{}

	if req.Reasoning != core.LLMReasoningDefault {
		switch l.provider {
		case "anthropic":
			if req.Reasoning == core.LLMReasoningOff {
				out["thinking"] = map[string]any{"type": "disabled"}
			} else {
				out["thinking"] = map[string]any{"type": "adaptive"}
				out["effort"] = string(req.Reasoning)
			}
		case "openai", "compat":
			if req.Reasoning == core.LLMReasoningOff {
				// There is no "off" on the OpenAI shape — a reasoning model
				// always reasons — so saying so beats sending nothing and
				// letting the caller believe it was honoured.
				return nil, core.LLMUnsupportedError(l.provider, "turning reasoning off")
			}
			out["reasoning_effort"] = string(req.Reasoning)
		case "google", "gemini":
			cfg, err := googleThinking(modelID, req.Reasoning)
			if err != nil {
				return nil, err
			}
			out["google"] = map[string]any{"thinkingConfig": cfg}
		default:
			return nil, core.LLMUnsupportedError(l.provider, "reasoning levels")
		}
	}

	// The caller's own values go on top: they asked for the provider's spelling
	// on purpose. A namespaced map is merged rather than replaced, so passing
	// one Google key does not silently drop the thinking config above it.
	for k, v := range req.ProviderOptions {
		if inner, ok := v.(map[string]any); ok {
			if existing, ok := out[k].(map[string]any); ok {
				for ik, iv := range inner {
					existing[ik] = iv
				}
				continue
			}
		}
		out[k] = v
	}
	return out, nil
}

// Gemini 2.5 spends a token budget on thinking; Gemini 3 takes a level instead
// and has no budget at all. The numbers are the middle of each tier rather than
// a documented mapping — there is none — chosen so that raising a level always
// raises the budget, and so "max" leaves the model to decide (-1, dynamic).
const (
	geminiBudgetLow    = 2048
	geminiBudgetMedium = 8192
	geminiBudgetHigh   = 24576
	geminiBudgetAuto   = -1
)

// geminiThinkingStyle is which spelling of thinkingConfig a model understands.
type geminiThinkingStyle int

const (
	geminiNoThinking geminiThinkingStyle = iota
	// geminiBudgetStyle is gemini-2.5: a token budget, 0 to turn it off.
	geminiBudgetStyle
	// geminiLevelStyle is gemini-3 and newer: a level, and no way off.
	geminiLevelStyle
)

// geminiStyleOf decides how to spell thinking for a model id.
//
// Matching a list of prefixes was the first version of this and it was wrong in
// the way that matters: the id a service actually configures is frequently an
// alias — AI_MODEL=gemini-flash-latest is what our own sample suggests — and an
// alias matched nothing, so every request asking to think failed with
// LLM_UNSUPPORTED. Being strictly honest about an id we cannot resolve turned
// out to mean being useless for the ids people use.
//
// So: an alias resolves to whatever is current, and current models take a
// level. That can only stay true, because an alias moves forward. And the
// version is parsed rather than prefix-matched, so gemini-4 does not
// reintroduce this the week it ships.
func geminiStyleOf(modelID string) geminiThinkingStyle {
	id := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case id == "":
		return geminiNoThinking
	case strings.HasSuffix(id, "-latest"):
		return geminiLevelStyle
	case strings.HasPrefix(id, "gemini-2.5"):
		return geminiBudgetStyle
	}
	if major, ok := geminiMajorVersion(id); ok && major >= 3 {
		return geminiLevelStyle
	}
	// Gemma and Gemini 1.5/2.0 have no thinking at all.
	return geminiNoThinking
}

// geminiMajorVersion reads the leading version number out of a Gemini model id:
// "gemini-3.1-flash-lite" is 3, "gemini-2.0-flash" is 2, "gemma-4-31b-it" is
// not a Gemini id at all.
func geminiMajorVersion(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "gemini-")
	if !ok {
		return 0, false
	}
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	switch {
	case end == 0:
		return 0, false
	case end < 0:
		end = len(rest)
	}
	major, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return major, true
}

// googleThinking maps a reasoning level onto Gemini's thinkingConfig.
//
// It has to know the model, not just the provider, because the families
// disagree and the API enforces the difference rather than ignoring it —
// checked against the live API:
//
//	gemini-2.5 + thinkingLevel   400 "Thinking level is not supported for this model"
//	gemini-3.x + thinkingBudget 0 400 "Budget 0 is invalid. This model only works in thinking mode"
//	gemini-3.x + thinkingBudget n  accepted and honoured
//
// So the mapping is not protection against a silent overcharge — it is what
// makes the field work at all on 2.5, and what turns "off" on a model that
// cannot stop thinking into an error naming the alternative instead of a 400
// from the provider halfway through a request.
func googleThinking(modelID string, level core.LLMReasoning) (map[string]any, core.IError) {
	switch geminiStyleOf(modelID) {
	case geminiLevelStyle:
		if level == core.LLMReasoningOff {
			return nil, core.LLMUnsupportedError("google/"+modelID,
				"turning thinking off (this model always thinks; use low instead)")
		}
		tier := "low"
		switch level {
		case core.LLMReasoningMedium:
			tier = "medium"
		case core.LLMReasoningHigh, core.LLMReasoningMax:
			tier = "high"
		}
		return map[string]any{"thinkingLevel": tier, "includeThoughts": true}, nil

	case geminiBudgetStyle:
		budget := geminiBudgetLow
		switch level {
		case core.LLMReasoningOff:
			return map[string]any{"thinkingBudget": 0, "includeThoughts": false}, nil
		case core.LLMReasoningMedium:
			budget = geminiBudgetMedium
		case core.LLMReasoningHigh:
			budget = geminiBudgetHigh
		case core.LLMReasoningMax:
			budget = geminiBudgetAuto
		}
		return map[string]any{"thinkingBudget": budget, "includeThoughts": true}, nil
	}

	return nil, core.LLMUnsupportedError("google/"+modelID, "reasoning levels")
}

// mapSources carries the citations of a grounded answer across. Without them a
// Google Search answer arrives with no way to show what it was based on, which
// is the one thing grounding was for.
func mapSources(in []provider.Source) []core.LLMSource {
	if len(in) == 0 {
		return nil
	}
	out := make([]core.LLMSource, 0, len(in))
	for _, s := range in {
		out = append(out, core.LLMSource{ID: s.ID, Type: s.Type, URL: s.URL, Title: s.Title})
	}
	return out
}

// toTools hands the SDK a closure per tool that routes back through
// req.RunTool, so approval, the unknown-tool error and the panic guard are the
// framework's — identical no matter which provider drove the loop, and
// identical to what the memory model does in a test.
//
// Recording each call as it happens is the only way to get one: the SDK reports
// the calls of its last step, and a loop that ran five steps has already
// forgotten the first four by the time it returns.
func (l *llm) toTools(req core.LLMRequest, rec *callRecorder) []sdk.Tool {
	out := make([]sdk.Tool, 0, len(req.Tools))
	for _, t := range req.Tools {
		if t.IsProviderDefined() {
			// No schema and no Execute: the provider knows the shape of its own
			// tool and runs it server-side, so all that crosses the wire is the
			// type and its configuration.
			out = append(out, sdk.Tool{
				Name:                   t.Name,
				ProviderDefinedType:    t.ProviderType,
				ProviderDefinedOptions: t.ProviderOptions,
			})
			continue
		}

		schema, err := json.Marshal(t.Schema)
		if err != nil || t.Schema == nil {
			// A tool the model cannot see is better than one it calls with
			// arguments nobody described; an empty object says "no arguments".
			schema = []byte(`{"type":"object","properties":{},"additionalProperties":false}`)
		}
		out = append(out, sdk.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
			Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
				call := core.LLMToolCall{Name: t.Name, Input: input}
				res := req.RunTool(ctx, call)
				// Recorded after the fact so the audit trail carries the
				// decision and the answer, not only the attempt: "asked to
				// refund", "refunded" and "this is what it said" are three
				// different facts.
				call.Outcome = res.Outcome
				call.Output = res.Output
				rec.add(call)
				return res.Output, nil
			},
		})
	}
	return out
}

// callRecorder collects the calls of one generation. It is per-call rather than
// per-handle because a handle is shared: two requests generating at once would
// otherwise each be told about the other's tool calls.
type callRecorder struct {
	mu    sync.Mutex
	calls []core.LLMToolCall
}

func (r *callRecorder) add(call core.LLMToolCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *callRecorder) all() []core.LLMToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.LLMToolCall(nil), r.calls...)
}

func toMessages(req core.LLMRequest) []provider.Message {
	out := make([]provider.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := provider.RoleUser
		if m.Role == core.LLMRoleAssistant {
			role = provider.RoleAssistant
		}

		content := make([]provider.Part, 0, 1+len(m.Parts))
		// Text first: several providers weight the instruction differently
		// depending on whether it precedes or follows the image, and "look at
		// this, then do X" is the order a person would write.
		if m.Text != "" || len(m.Parts) == 0 {
			content = append(content, provider.Part{Type: provider.PartText, Text: m.Text})
		}
		for _, p := range m.Parts {
			content = append(content, toPart(p))
		}

		out = append(out, provider.Message{Role: role, Content: content})
	}
	return out
}

// toPart maps one attachment. The SDK carries bytes as a data: URI, so inline
// data is encoded here; a URL passes through untouched.
func toPart(p core.LLMPart) provider.Part {
	out := provider.Part{
		Type:      provider.PartImage,
		MediaType: p.MediaType,
		Filename:  p.Filename,
		Detail:    p.Detail,
	}
	if p.Type == core.LLMPartFile {
		out.Type = provider.PartFile
	}

	if len(p.Data) > 0 {
		out.URL = "data:" + p.MediaType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
	} else {
		out.URL = p.URL
	}
	return out
}

func mapUsage(u provider.Usage) core.LLMUsage {
	return core.LLMUsage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		// Cache writes are billed as input at a premium and reads at a
		// discount; folding writes in with reads would make a cost report wrong
		// in the direction that looks cheap.
		CachedInputTokens: u.CacheReadTokens,
		ReasoningTokens:   u.ReasoningTokens,
	}
}

func mapFinish(f provider.FinishReason) core.LLMFinishReason {
	switch f {
	case provider.FinishStop:
		return core.LLMFinishStop
	case provider.FinishLength:
		return core.LLMFinishLength
	case provider.FinishContentFilter:
		return core.LLMFinishContentFilter
	case provider.FinishToolCalls:
		return core.LLMFinishToolUse
	case provider.FinishError:
		return core.LLMFinishError
	case "":
		return ""
	default:
		return core.LLMFinishOther
	}
}

// mapError turns a goai error into an IError whose status says whether retrying
// could help. Without this every provider failure would surface as a 500, and a
// rate limit would look identical to a malformed request.
func (l *llm) mapError(err error) core.IError {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return core.Wrapf(err, "llm: %s timed out after %s", l.provider, l.cfg.Timeout)
	}
	if errors.Is(err, context.Canceled) {
		return core.Wrap(err, "llm: the request ended before the model replied")
	}

	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		status := apiErr.StatusCode
		if status <= 0 {
			status = 502
		}
		return &core.Error{
			Status:  status,
			Code:    llmErrorCode(status),
			Message: apiErr.Error(),
		}
	}

	var overflow *sdk.ContextOverflowError
	if errors.As(err, &overflow) {
		return core.Wrap(err, "llm: the prompt is longer than the model's context window")
	}

	// Anything left is a transport failure or a bug in the driver: 502, because
	// the caller's request was fine and retrying might work.
	return &core.Error{Status: 502, Code: "LLM_ERROR", Message: err.Error()}
}

func llmErrorCode(status int) string {
	switch {
	case status == 401 || status == 403:
		return "LLM_UNAUTHORIZED"
	case status == 404:
		return "LLM_MODEL_NOT_FOUND"
	case status == 429:
		return "LLM_RATE_LIMITED"
	case status >= 500:
		return "LLM_PROVIDER_ERROR"
	default:
		return "LLM_REQUEST_REJECTED"
	}
}

// stream adapts goai's channel of chunks to the framework's iterator.
//
// The adaptation is the point: a channel makes ending early the caller's
// problem (abandon it and the producer blocks or leaks), while an iterator with
// Close makes it the driver's. Close cancels the call context, which is what
// goai watches to shut its reader down.
type stream struct {
	src <-chan provider.StreamChunk
	// text is the SDK's own handle, kept for the one thing the chunks do not
	// carry: a grounded answer's citations. They are accumulated as the stream
	// runs and only readable from the result it builds at the end.
	text   *sdk.TextStream
	cancel context.CancelFunc
	owner  *llm
	rec    *callRecorder
	cur    string
	resp   core.LLMResponse
	err    core.IError
	closed bool
	// drained says the source channel has ended, which is what makes the SDK's
	// Result() safe to call — it blocks until the producing goroutine finishes,
	// so asking mid-stream would deadlock against our own reader.
	drained bool
	sourced bool
}

var _ core.LLMStream = (*stream)(nil)

func (s *stream) Next() bool {
	if s.closed {
		return false
	}
	for chunk := range s.src {
		switch chunk.Type {
		case provider.ChunkText:
			if chunk.Text == "" {
				continue
			}
			s.cur = chunk.Text
			s.resp.Text += chunk.Text
			return true
		case provider.ChunkStepFinish:
			s.resp.Steps++
			if r := mapFinish(chunk.FinishReason); r != "" {
				s.resp.FinishReason = r
			}
			if u := mapUsage(chunk.Usage); u.Total() > 0 {
				s.resp.Usage = u
			}
		case provider.ChunkFinish:
			// Both carry part of the ending: a step's finish reason arrives
			// with the step, while the authoritative usage totals arrive with
			// the final chunk — and either may leave the other's field empty.
			// Taking whichever is populated is what makes the two consistent
			// with what Generate reports for the same call.
			if r := mapFinish(chunk.FinishReason); r != "" {
				s.resp.FinishReason = r
			}
			if u := mapUsage(chunk.Usage); u.Total() > 0 {
				s.resp.Usage = u
			}
		case provider.ChunkError:
			if chunk.Error != nil && s.err == nil {
				s.err = s.owner.mapError(chunk.Error)
				s.resp.FinishReason = core.LLMFinishError
			}
		}
	}
	// The source closed: the generation is over, one way or the other.
	s.cur = ""
	s.drained = true
	return false
}

func (s *stream) Text() string { return s.cur }
func (s *stream) Response() core.LLMResponse {
	// Citations only once the stream has ended. A grounded answer that streams
	// without them is not merely missing a field: showing sources is a term of
	// Google's grounding service, so the alternative was telling callers to give
	// up streaming on exactly the route that needs grounding most.
	//
	// Read once and kept: Result builds a fresh value each call, and Response is
	// called at least twice on the normal path — once by the caller and once by
	// the instrumentation that records what the stream cost.
	if s.drained && s.text != nil && !s.sourced {
		s.sourced = true
		s.resp.Sources = mapSources(s.text.Result().Sources)
	}

	out := s.resp
	if out.Steps < 1 {
		out.Steps = 1
	}
	if s.rec != nil {
		out.ToolCalls = s.rec.all()
	}
	return out
}

func (s *stream) Err() core.IError { return s.err }

func (s *stream) Close() core.IError {
	if s.closed {
		return nil
	}
	s.closed = true
	// Cancelling first unblocks goai's reader; draining afterwards lets its
	// goroutine finish rather than being left mid-send.
	s.cancel()
	for range s.src { //nolint:revive // drain
	}
	s.drained = true
	return nil
}
