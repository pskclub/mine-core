package goai

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	core "github.com/pskclub/mine-core/v2"
	sdk "github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/goai/provider/compat"
	"github.com/zendev-sh/goai/provider/google"
	"github.com/zendev-sh/goai/provider/ollama"
	"github.com/zendev-sh/goai/provider/openai"
)

// EmbedConfig is what the embedder needs. It falls back to the generation
// settings for everything it is not given, because most services embed with the
// same account they generate with — and the ones that do not are exactly the
// ones that need the separate keys.
type EmbedConfig struct {
	Provider   string
	Model      string
	APIKey     string
	BaseURL    string
	Timeout    time.Duration
	MaxRetries int
	// Dimensions is the vector length every call asks for, unless the request
	// says otherwise. 0 leaves the model's own default.
	//
	// It is configuration rather than a per-call argument because the number
	// belongs to the *index*: it is fixed by the column the vectors are stored
	// in, and one call that forgot it writes rows the corpus can never be
	// compared against.
	Dimensions int
}

func WithEmbedModel(id string) func(*EmbedConfig) {
	return func(c *EmbedConfig) { c.Model = id }
}

func WithEmbedAPIKey(key string) func(*EmbedConfig) {
	return func(c *EmbedConfig) { c.APIKey = key }
}

func WithEmbedBaseURL(url string) func(*EmbedConfig) {
	return func(c *EmbedConfig) { c.BaseURL = url }
}

// WithEmbedDimensions asks for a vector length other than the model's default —
// the option a service needs when its vector column was declared with one.
func WithEmbedDimensions(n int) func(*EmbedConfig) {
	return func(c *EmbedConfig) { c.Dimensions = n }
}

// NewEmbedder builds an embedding model from AI_EMBED_* configuration, falling
// back to AI_PROVIDER / AI_API_KEY / AI_BASE_URL.
//
// With no AI_EMBED_MODEL it returns the disabled embedder rather than an error:
// most services never embed anything, and the ones that do should hear about it
// at the call, where the message can name the missing key.
//
//	embedder, err := goai.NewEmbedder(env)
//	if err != nil { log.Fatal(err) }
//	app, _ := core.NewApp(env, core.WithLLM(model), core.WithEmbedder(embedder))
func NewEmbedder(env core.IENV, opts ...func(*EmbedConfig)) (core.IEmbedder, core.IError) {
	cfg := EmbedConfig{}
	if env != nil {
		c := env.Config()
		cfg = EmbedConfig{
			Provider:   firstNonEmpty(c.AIEmbedProvider, c.AIProvider),
			Model:      c.AIEmbedModel,
			APIKey:     firstNonEmpty(c.AIEmbedAPIKey, c.AIAPIKey),
			BaseURL:    firstNonEmpty(c.AIEmbedBaseURL, c.AIBaseURL),
			MaxRetries: c.AIMaxRetries,
			Timeout:    time.Duration(c.AITimeout) * time.Second,
			Dimensions: c.AIEmbedDimensions,
		}
	}
	for _, o := range opts {
		o(&cfg)
	}
	return NewEmbedConfig(cfg)
}

// NewEmbedConfig builds an embedder from an explicit configuration.
func NewEmbedConfig(cfg EmbedConfig) (core.IEmbedder, core.IError) {
	name := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if name == "" || cfg.Model == "" {
		return core.NewNoopEmbedder(), nil
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = defaultMaxRetries
	}

	model, err := buildEmbeddingModel(name, cfg)
	if err != nil {
		return nil, err
	}
	return &embedder{
		cfg:       cfg,
		provider:  name,
		model:     model,
		ctx:       context.Background(),
		dims:      &atomic.Int32{},
		siblings:  &sync.Map{},
		buildable: true,
	}, nil
}

// NewEmbeddingModel wraps a model the caller built, for a provider this driver
// does not name or for a test pointed at a fake server.
func NewEmbeddingModel(providerName, modelID string, model provider.EmbeddingModel) core.IEmbedder {
	return &embedder{
		cfg:      EmbedConfig{Provider: providerName, Model: modelID, Timeout: defaultTimeout},
		provider: strings.ToLower(providerName),
		model:    model,
		ctx:      context.Background(),
		dims:     &atomic.Int32{},
	}
}

// buildEmbeddingModel maps a provider name to a goai embedding model.
//
// Anthropic is absent on purpose: it has no embedding API at all, so a service
// generating with Claude must set AI_EMBED_PROVIDER to something else. Saying
// so here is better than a 404 from a URL nobody meant to call.
func buildEmbeddingModel(name string, cfg EmbedConfig) (provider.EmbeddingModel, core.IError) {
	switch name {
	case "openai":
		o := []openai.Option{}
		if cfg.APIKey != "" {
			o = append(o, openai.WithAPIKey(cfg.APIKey))
		}
		if cfg.BaseURL != "" {
			o = append(o, openai.WithBaseURL(cfg.BaseURL))
		}
		return openai.Embedding(cfg.Model, o...), nil

	case "google", "gemini":
		o := []google.Option{}
		if cfg.APIKey != "" {
			o = append(o, google.WithAPIKey(cfg.APIKey))
		}
		if cfg.BaseURL != "" {
			o = append(o, google.WithBaseURL(cfg.BaseURL))
		}
		return google.Embedding(cfg.Model, o...), nil

	case "ollama":
		o := []ollama.Option{}
		if cfg.BaseURL != "" {
			o = append(o, ollama.WithBaseURL(cfg.BaseURL))
		}
		return ollama.Embedding(cfg.Model, o...), nil

	case "compat":
		if cfg.BaseURL == "" {
			return nil, core.New(400, "EMBED_INVALID_CONFIG",
				"embed: provider \"compat\" needs AI_EMBED_BASE_URL (or AI_BASE_URL)")
		}
		o := []compat.Option{compat.WithBaseURL(cfg.BaseURL)}
		if cfg.APIKey != "" {
			o = append(o, compat.WithAPIKey(cfg.APIKey))
		}
		return compat.Embedding(cfg.Model, o...), nil

	case "anthropic":
		return nil, core.New(400, "EMBED_INVALID_CONFIG",
			"embed: anthropic has no embedding API — set AI_EMBED_PROVIDER to openai, google, ollama or compat")
	}

	return nil, core.Newf(400, "EMBED_INVALID_CONFIG",
		"embed: unknown embedding provider %q (known: openai, google, ollama, compat)", cfg.Provider)
}

type embedder struct {
	cfg      EmbedConfig
	provider string
	model    provider.EmbeddingModel
	ctx      context.Context

	// dims is learned from the first response, because a provider states the
	// vector length in its documentation and not in its API — and a caller
	// declaring a vector column needs the number, not the documentation.
	//
	// It is a pointer so that WithContext can copy the struct the way every
	// other handle in this codebase does: a sync.Once or a mutex by value would
	// either reset the guard or race, and a per-request handle that had to
	// forward every call to a shared parent is how that race gets written.
	dims *atomic.Int32

	// siblings caches the models built for a per-request EmbedRequest.Model, so
	// a mixed-model batch job does not rebuild a client per call. Same pointer
	// rationale as dims.
	siblings *sync.Map // model id -> provider.EmbeddingModel
	// buildable says whether this handle knows enough to build a sibling — true
	// for one built from a configuration, false for one wrapping a model the
	// caller made, where the credentials and endpoint live in the caller's own
	// closure and cannot be read back out.
	buildable bool
}

var _ core.IEmbedder = (*embedder)(nil)

func (e *embedder) Embed(texts ...string) ([][]float32, core.IError) {
	return e.EmbedWith(core.EmbedRequest{Texts: texts})
}

func (e *embedder) EmbedWith(req core.EmbedRequest) ([][]float32, core.IError) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	model := e.model
	if req.Model != "" && req.Model != e.cfg.Model {
		var err core.IError
		if model, err = e.modelFor(req.Model); err != nil {
			return nil, err
		}
	}

	opts, err := e.embedOptions(req)
	if err != nil {
		return nil, err
	}

	base := e.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, e.cfg.Timeout)
	defer cancel()

	texts := req.Texts
	res, gerr := sdk.EmbedMany(ctx, model, texts, opts...)
	if gerr != nil {
		return nil, mapEmbedError(e.provider, e.cfg.Timeout, gerr)
	}
	if len(res.Embeddings) != len(texts) {
		// Order is the contract: caller i matches vector i. A short response
		// would silently pair the wrong text with the wrong vector, and nothing
		// downstream could tell.
		return nil, core.Newf(502, "EMBED_PROVIDER_ERROR",
			"embed: asked for %d vectors and got %d", len(texts), len(res.Embeddings))
	}

	out := make([][]float32, 0, len(res.Embeddings))
	for _, v := range res.Embeddings {
		vec := make([]float32, len(v))
		for i, f := range v {
			vec[i] = float32(f)
		}
		out = append(out, vec)
	}
	// Learned only from a call that used this handle's own settings: a one-off
	// request for a different length or a different model says nothing about
	// what the configured model produces, and Dimensions() is what a caller
	// declares a column with.
	if e.dims != nil && req.Dimensions == 0 && req.Model == "" {
		e.dims.CompareAndSwap(0, int32(len(out[0])))
	}
	return out, nil
}

// embedOptions turns the normalised fields into the provider's own spelling.
//
// The two that matter have no shared wire form: Google nests them under a
// "google" key and calls them outputDimensionality and taskType, while OpenAI
// takes a flat "dimensions" and has no task type at all. A driver that guessed
// wrong here fails silently — the request succeeds and returns vectors of a
// length nobody asked for.
func (e *embedder) embedOptions(req core.EmbedRequest) ([]sdk.Option, core.IError) {
	opts := []sdk.Option{sdk.WithMaxRetries(e.cfg.MaxRetries)}

	dims := req.Dimensions
	if dims == 0 {
		dims = e.cfg.Dimensions
	}

	out := map[string]any{}
	switch e.provider {
	case "google", "gemini":
		g := map[string]any{}
		if dims > 0 {
			g["outputDimensionality"] = dims
		}
		if task := googleTaskType(req.Task); task != "" {
			g["taskType"] = task
		}
		if len(g) > 0 {
			out["google"] = g
		}

	case "openai", "compat":
		if dims > 0 {
			out["dimensions"] = dims
		}
		if req.Task != core.EmbedTaskDefault {
			// OpenAI's models embed one way regardless of what the vector is
			// for. Sending the request without the task would return vectors
			// that look right and rank worse, with nothing to say why.
			return nil, core.EmbedUnsupportedError(e.provider, "task types")
		}

	default:
		if dims > 0 {
			return nil, core.EmbedUnsupportedError(e.provider, "choosing the vector length")
		}
		if req.Task != core.EmbedTaskDefault {
			return nil, core.EmbedUnsupportedError(e.provider, "task types")
		}
	}

	// The caller's own values go on top: they asked for the provider's spelling
	// on purpose, and a nested namespace is merged rather than replaced so that
	// setting one Google key does not silently drop the dimensions above it.
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

	if len(out) > 0 {
		opts = append(opts, sdk.WithEmbeddingProviderOptions(out))
	}
	return opts, nil
}

// googleTaskType maps the normalised task onto Gemini's own enum.
func googleTaskType(task core.EmbedTask) string {
	switch task {
	case core.EmbedDocument:
		return "RETRIEVAL_DOCUMENT"
	case core.EmbedQuery:
		return "RETRIEVAL_QUERY"
	case core.EmbedSimilarity:
		return "SEMANTIC_SIMILARITY"
	case core.EmbedClassification:
		return "CLASSIFICATION"
	case core.EmbedClustering:
		return "CLUSTERING"
	}
	return ""
}

// modelFor returns the embedding model for a per-request model id, building it
// once and reusing it after.
//
// Mixing models inside one index is a mistake — their vectors are not
// comparable — but embedding with a second model is not: re-indexing a corpus
// under a new model, or scoring the two against each other before switching, is
// exactly how that migration is done.
func (e *embedder) modelFor(id string) (provider.EmbeddingModel, core.IError) {
	if !e.buildable {
		return nil, core.Newf(400, "EMBED_INVALID_REQUEST",
			"embed: this handle wraps a model built by the caller and is bound to %q; build a second embedder for %q",
			e.cfg.Model, id)
	}
	if e.siblings != nil {
		if cached, ok := e.siblings.Load(id); ok {
			return cached.(provider.EmbeddingModel), nil
		}
	}

	cfg := e.cfg
	cfg.Model = id
	model, err := buildEmbeddingModel(e.provider, cfg)
	if err != nil {
		return nil, err
	}
	if e.siblings != nil {
		// LoadOrStore rather than Store: two requests racing on the same new
		// model must end up using one client, not silently one each.
		if actual, loaded := e.siblings.LoadOrStore(id, model); loaded {
			return actual.(provider.EmbeddingModel), nil
		}
	}
	return model, nil
}

func (e *embedder) EmbedOne(text string) ([]float32, core.IError) {
	vecs, err := e.Embed(text)
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (e *embedder) Dimensions() int {
	// A configured length is known before the first call, which is the point of
	// configuring it: a migration declaring vector(n) runs at startup, not after
	// the first document has been embedded.
	if e.cfg.Dimensions > 0 {
		return e.cfg.Dimensions
	}
	if e.dims == nil {
		return 0
	}
	return int(e.dims.Load())
}

func (e *embedder) Model() string      { return e.cfg.Model }
func (e *embedder) Provider() string   { return e.provider }
func (e *embedder) Enabled() bool      { return true }
func (e *embedder) Close() core.IError { return nil }

func (e *embedder) WithContext(ctx context.Context) core.IEmbedder {
	cp := *e
	cp.ctx = ctx
	return &cp
}

func mapEmbedError(providerName string, timeout time.Duration, err error) core.IError {
	ierr := (&llm{provider: providerName, cfg: Config{Timeout: timeout}}).mapError(err)
	if ierr == nil {
		return nil
	}
	// Same statuses, embedding-shaped codes, so a dashboard can tell which
	// model ran out of quota.
	e, ok := ierr.(*core.Error)
	if !ok {
		return ierr
	}
	e.Code = strings.Replace(e.Code, "LLM_", "EMBED_", 1)
	return e
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
