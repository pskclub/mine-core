package core

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"sync"
)

// ErrEmbedderDisabled is wrapped by the error every operation returns when no
// embedding model is configured.
//
// Like the language model and unlike the cache, it fails loudly: an empty
// vector is not a degraded answer, it is a search index that will quietly
// return nothing relevant forever.
var ErrEmbedderDisabled = errors.New("embed: not configured")

// ErrEmbedUnsupported is wrapped by the error a driver returns when the request
// asks for something the chosen embedding model cannot do — the counterpart of
// ErrLLMUnsupported, and distinct from a disabled model for the same reason: one
// is fixed by configuration, the other by picking another model or dropping the
// field.
var ErrEmbedUnsupported = errors.New("embed: capability not supported")

// EmbedTask is what the vectors are going to be used for.
//
// It is not a stylistic hint. A model that supports task types embeds the same
// sentence into a different vector depending on the answer, and a corpus indexed
// as documents but searched with query vectors built as documents retrieves
// measurably worse — silently, because every number involved still looks like a
// perfectly good similarity score.
//
// Providers spell these differently (Google RETRIEVAL_DOCUMENT, Cohere/Voyage
// input_type) and some have no concept of them at all, so a driver that cannot
// honour one returns ErrEmbedUnsupported rather than dropping it.
type EmbedTask string

const (
	// EmbedTaskDefault leaves the provider's own default in place. It is the
	// zero value, so an EmbedRequest that says nothing behaves exactly like the
	// plain Embed it replaced.
	EmbedTaskDefault EmbedTask = ""
	// EmbedDocument is for the corpus side of a search — the text being indexed.
	EmbedDocument EmbedTask = "document"
	// EmbedQuery is for the search side. Pair it with EmbedDocument: using one
	// without the other is the mistake this type exists to prevent.
	EmbedQuery EmbedTask = "query"
	// EmbedSimilarity is for comparing two texts symmetrically — deduplication,
	// near-duplicate detection — where neither side is a query.
	EmbedSimilarity     EmbedTask = "similarity"
	EmbedClassification EmbedTask = "classification"
	EmbedClustering     EmbedTask = "clustering"
)

// EmbedRequest is one embedding call with the options Embed cannot express.
//
// Everything beyond Texts is optional, and a driver that cannot honour a field
// it was given returns ErrEmbedUnsupported rather than sending the request
// without it — a vector of the wrong length or built for the wrong task is not a
// degraded result, it is a wrong one that nothing downstream can detect.
type EmbedRequest struct {
	// Texts is what to embed, one vector out per text, in order. Must not be
	// empty.
	Texts []string
	// Model overrides the configured embedding model. It is the provider's own
	// model id.
	Model string
	// Dimensions is the vector length to ask for. 0 uses the model's default,
	// which is the right choice unless a column says otherwise.
	//
	// It matters because the default is frequently not what a schema was built
	// for: gemini-embedding-001 returns 3072 unless asked for less, so a
	// vector(768) column rejects every row — or, with a driver that truncated
	// instead, accepts vectors that no longer match the corpus.
	Dimensions int
	// Task is what the vectors are for. See EmbedTask.
	Task EmbedTask
	// ProviderOptions carries provider-specific parameters straight through,
	// untranslated, merged over whatever Dimensions and Task produced. Same
	// escape hatch as LLMRequest.ProviderOptions, and the same trade-off: no
	// compile-time checking, so prefer a typed field when one exists.
	ProviderOptions map[string]any
}

// Validate checks the request the way every driver must, before anything is
// sent. Exported for the same reason LLMRequest.Validate is: drivers live in
// other packages and a caller's mistake has to read identically whichever
// provider is configured.
func (r EmbedRequest) Validate() IError { return r.validate() }

func (r EmbedRequest) validate() IError {
	if len(r.Texts) == 0 {
		return New(400, "EMBED_INVALID_REQUEST", "embed: nothing to embed")
	}
	for i, t := range r.Texts {
		if strings.TrimSpace(t) == "" {
			// Most providers reject an empty string, and the ones that accept it
			// return a vector that matches everything equally — which is worse,
			// because the index keeps working and stops being useful.
			return Newf(400, "EMBED_INVALID_REQUEST", "embed: texts[%d] is empty", i)
		}
	}
	if r.Dimensions < 0 {
		return Newf(400, "EMBED_INVALID_REQUEST", "embed: Dimensions is %d — use 0 for the model's default", r.Dimensions)
	}
	switch r.Task {
	case EmbedTaskDefault, EmbedDocument, EmbedQuery, EmbedSimilarity, EmbedClassification, EmbedClustering:
	default:
		return Newf(400, "EMBED_INVALID_REQUEST", "embed: unknown Task %q", r.Task)
	}
	return nil
}

// EmbedUnsupportedError reports that an embedding model cannot do what the
// request asked for. Drivers use it so the message is identical across
// providers, which is what makes it worth asserting on in a test.
func EmbedUnsupportedError(provider, feature string) IError {
	return &Error{
		Status:  400,
		Code:    "EMBED_UNSUPPORTED",
		Message: fmt.Sprintf("embed: %s does not support %s", provider, feature),
		cause:   ErrEmbedUnsupported,
	}
}

// IEmbedder turns text into vectors.
//
// It is a separate capability from ILLM rather than a method on it because the
// two are separate models with separate ids, separate pricing and — often —
// separate vendors: Anthropic has no embedding endpoint at all, so a service
// using Claude for generation embeds with something else entirely.
//
// core.Embedder(ctx) is never nil: a service with no AI_EMBED_MODEL gets a
// disabled embedder whose every call fails with EMBED_DISABLED.
type IEmbedder interface {
	// Embed returns one vector per input, in the same order. Sending a batch is
	// meaningfully cheaper than a call each — providers charge per token, not
	// per request, and the round trips dominate for short texts.
	Embed(texts ...string) ([][]float32, IError)
	// EmbedOne is Embed for a single text, which is what a search query is.
	EmbedOne(text string) ([]float32, IError)
	// EmbedWith is Embed with the options a real index needs — a vector length
	// that matches the column, a task type that matches how the vector will be
	// used, and the provider's own parameters underneath both.
	//
	//	docs, err := core.Embedder(ctx).EmbedWith(core.EmbedRequest{
	//	    Texts: chunks, Dimensions: 768, Task: core.EmbedDocument,
	//	})
	//
	// Embed is EmbedWith with an empty request, so the two share one code path
	// and a driver cannot honour options on one and ignore them on the other.
	EmbedWith(req EmbedRequest) ([][]float32, IError)
	// Dimensions is the vector length this model produces, or 0 when it is not
	// known before the first call. A vector column has to be declared with it,
	// so it is worth knowing at startup rather than at migration time.
	//
	// A configured length (AI_EMBED_DIMENSIONS) is reported straight away; other
	// models learn it from their first response. A per-request Dimensions does
	// not change it — that is a property of the call, not of the handle.
	Dimensions() int
	Model() string
	Provider() string
	Enabled() bool
	WithContext(ctx context.Context) IEmbedder
	Close() IError
}

// Embedder returns the application's embedding model bound to ctx.
//
// Same shape and same reasoning as core.LLM: embedding is something you do with
// the request's deadline attached, not a property of the request.
func Embedder(ctx context.Context) IEmbedder {
	if app := appFrom(ctx); app != nil && app.embedder != nil {
		return app.embedder.WithContext(ctx)
	}
	return noopEmbedder{}
}

func embedDisabled() *Error {
	return &Error{
		Status:  503,
		Code:    "EMBED_DISABLED",
		Message: "embed: no embedding model is configured (set AI_EMBED_MODEL)",
		cause:   ErrEmbedderDisabled,
	}
}

// noopEmbedder is what Embedder(ctx) returns with nothing configured.
type noopEmbedder struct{}

var _ IEmbedder = noopEmbedder{}

// NewNoopEmbedder returns an embedder that refuses every call.
func NewNoopEmbedder() IEmbedder { return noopEmbedder{} }

func (noopEmbedder) Embed(...string) ([][]float32, IError)        { return nil, embedDisabled() }
func (noopEmbedder) EmbedOne(string) ([]float32, IError)          { return nil, embedDisabled() }
func (noopEmbedder) EmbedWith(EmbedRequest) ([][]float32, IError) { return nil, embedDisabled() }
func (noopEmbedder) Dimensions() int                              { return 0 }
func (noopEmbedder) Model() string                                { return "" }
func (noopEmbedder) Provider() string                             { return "" }
func (noopEmbedder) Enabled() bool                                { return false }
func (noopEmbedder) WithContext(context.Context) IEmbedder        { return noopEmbedder{} }
func (noopEmbedder) Close() IError                                { return nil }

// embedderBootField is what the boot log says about the embedding model — the
// same reasoning as the language model's: which model is the question, and a
// vector index built with the wrong one is silently useless.
func embedderBootField(e IEmbedder) any {
	if e == nil || !e.Enabled() {
		return false
	}
	if m := e.Model(); m != "" {
		return e.Provider() + "/" + m
	}
	return e.Provider()
}

// CosineSimilarity is how close two vectors point in the same direction: 1 is
// identical, 0 unrelated, -1 opposite.
//
// It lives here because every caller of an embedder needs it and it is the one
// piece of vector arithmetic that is easy to get wrong — forgetting to
// normalise turns a similarity score into a magnitude comparison, which ranks
// long documents above relevant ones.
//
// Vectors of different lengths return 0: they came from different models, and
// any number would be meaningless.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// memoryEmbedder is an in-process IEmbedder producing deterministic vectors.
//
// Deterministic matters more than realistic: a test asserting that two
// paraphrases rank above an unrelated sentence needs the same answer on every
// run, and a real model does not guarantee that across versions. What it gives
// up is meaning — its vectors encode which words are present, not what they
// say, so it tests the plumbing around a search, never the search quality.
type memoryEmbedder struct {
	ctx  context.Context
	dims int
	rec  *embedRecorder
}

type embedRecorder struct {
	mu       sync.Mutex
	texts    []string
	requests []EmbedRequest
}

var _ IEmbedder = (*memoryEmbedder)(nil)

// NewMemoryEmbedder returns an embedder that hashes text into vectors, so a
// test can index and search without a key, a network or a bill.
//
//	e := core.NewMemoryEmbedder(8)
//	app, _ := core.NewApp(env, core.WithEmbedder(e))
//
// dims defaults to 8 — small enough to print in a failing assertion, large
// enough that unrelated texts do not collide.
func NewMemoryEmbedder(dims ...int) IEmbedder {
	d := 8
	if len(dims) > 0 && dims[0] > 0 {
		d = dims[0]
	}
	return &memoryEmbedder{ctx: context.Background(), dims: d, rec: &embedRecorder{}}
}

// EmbeddedTexts returns what a memory embedder was asked to embed, in order.
// It returns nil for any other embedder.
func EmbeddedTexts(e IEmbedder) []string {
	m, ok := e.(*memoryEmbedder)
	if !ok {
		return nil
	}
	m.rec.mu.Lock()
	defer m.rec.mu.Unlock()
	return append([]string(nil), m.rec.texts...)
}

// EmbeddedRequests returns the calls a memory embedder recorded, in order — the
// counterpart of core.LLMCalls, and the way a test asserts that the corpus was
// indexed as documents and the search embedded as a query. It returns nil for
// any other embedder.
func EmbeddedRequests(e IEmbedder) []EmbedRequest {
	m, ok := e.(*memoryEmbedder)
	if !ok {
		return nil
	}
	m.rec.mu.Lock()
	defer m.rec.mu.Unlock()
	return append([]EmbedRequest(nil), m.rec.requests...)
}

func (m *memoryEmbedder) Embed(texts ...string) ([][]float32, IError) {
	return m.EmbedWith(EmbedRequest{Texts: texts})
}

func (m *memoryEmbedder) EmbedWith(req EmbedRequest) ([][]float32, IError) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	if m.ctx != nil {
		if err := m.ctx.Err(); err != nil {
			return nil, Wrap(err, "embed: context ended before the call completed")
		}
	}

	m.rec.mu.Lock()
	m.rec.texts = append(m.rec.texts, req.Texts...)
	m.rec.requests = append(m.rec.requests, req)
	m.rec.mu.Unlock()

	// A requested length is honoured rather than ignored: a test whose schema
	// says vector(768) is testing that the call asks for 768, and a stand-in that
	// always returned its own width would pass while the real one failed.
	dims := m.dims
	if req.Dimensions > 0 {
		dims = req.Dimensions
	}

	out := make([][]float32, 0, len(req.Texts))
	for _, t := range req.Texts {
		out = append(out, hashVector(t, dims))
	}
	return out, nil
}

func (m *memoryEmbedder) EmbedOne(text string) ([]float32, IError) {
	vecs, err := m.Embed(text)
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (m *memoryEmbedder) Dimensions() int  { return m.dims }
func (m *memoryEmbedder) Model() string    { return "memory" }
func (m *memoryEmbedder) Provider() string { return "memory" }
func (m *memoryEmbedder) Enabled() bool    { return true }
func (m *memoryEmbedder) Close() IError    { return nil }

func (m *memoryEmbedder) WithContext(ctx context.Context) IEmbedder {
	cp := *m
	cp.ctx = ctx
	return &cp
}

// hashVector spreads a text's words across the dimensions, so texts sharing
// words point in similar directions and unrelated ones do not. It is a
// bag-of-words sketch, not an embedding — enough for a test to assert an
// ordering, never enough to judge relevance.
func hashVector(text string, dims int) []float32 {
	vec := make([]float32, dims)
	for _, word := range strings.Fields(strings.ToLower(text)) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(word))
		sum := h.Sum32()
		idx := int(sum % uint32(dims))
		// The sign spreads collisions apart: two different words landing on the
		// same dimension should not always reinforce each other.
		if sum%2 == 0 {
			vec[idx] += 1
		} else {
			vec[idx] += 0.5
		}
	}
	return vec
}
