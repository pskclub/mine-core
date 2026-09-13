// Package llm is typed generation on top of core.ILLM.
//
// core.LLM(ctx).Generate returns text, which is the right shape for a chat
// reply and the wrong shape for everything else a backend does with a model:
// extraction, classification, scoring. Those want a Go value, and getting one
// from text means describing a schema, asking for JSON, parsing it, and
// checking it — four steps every service would otherwise write again, slightly
// differently.
//
//	inv, err := llm.New[Invoice](ctx).
//	    System("Read the receipt. Amounts are THB.").
//	    Extract(ocrText)
//
// The type parameter is the whole API: the schema is derived from Invoice, the
// model is constrained to it, and the reply is unmarshalled back into Invoice.
//
// It is a package-level constructor taking ctx — the same shape as
// repository.New[M](ctx) — because Go methods cannot have type parameters, and
// because the shape is already the one this codebase reaches for.
package llm

import (
	"context"
	"encoding/json"
	"strings"

	core "github.com/pskclub/mine-core/v2"
)

// Typed builds one structured generation. Every builder method returns a copy,
// so a half-configured value is safe to keep and reuse:
//
//	base := llm.New[Sentiment](ctx).System(rubric).CacheSystem()
//	a, _ := base.Extract(reviewA)
//	b, _ := base.Extract(reviewB)
type Typed[T any] struct {
	ctx      context.Context
	req      core.LLMRequest
	model    core.ILLM
	name     string
	validate func(*T, context.Context) core.IError
	strict   bool
}

// New starts a generation whose result is a T.
//
// ctx is anything carrying the App — an IContext from a handler or a job, or a
// plain context.Context derived from one — exactly like core.LLM.
func New[T any](ctx context.Context) *Typed[T] {
	return &Typed[T]{ctx: ctx, strict: true}
}

func (t *Typed[T]) clone() *Typed[T] {
	cp := *t
	cp.req.Messages = append([]core.LLMMessage(nil), t.req.Messages...)
	return &cp
}

// System sets the system prompt. This is where the wording lives — what to
// extract, how to handle a missing value, which language to answer in. The
// schema says what shape, never what to do.
func (t *Typed[T]) System(prompt string) *Typed[T] {
	cp := t.clone()
	cp.req.System = prompt
	return cp
}

// Ask appends a user message.
func (t *Typed[T]) Ask(text string) *Typed[T] {
	cp := t.clone()
	cp.req.Messages = append(cp.req.Messages, core.LLMUser(text))
	return cp
}

// Messages replaces the conversation — for a follow-up that has to carry what
// was said before.
func (t *Typed[T]) Messages(msgs ...core.LLMMessage) *Typed[T] {
	cp := t.clone()
	cp.req.Messages = append([]core.LLMMessage(nil), msgs...)
	return cp
}

// Model runs this generation against another model of the configured provider —
// a cheap one for a classification that does not need the expensive one:
//
//	llm.New[Category](ctx).Model("gemini-flash-lite-latest").Extract(text)
//
// It stays inside the application's model, so the call is metered, logged and
// attributed like every other. Building a second client to change model is the
// alternative, and its tokens land in nobody's dashboard.
func (t *Typed[T]) Model(id string) *Typed[T] {
	cp := t.clone()
	cp.req.Model = id
	return cp
}

// Using runs the generation against a specific model rather than the
// application's — a second provider entirely, or a memory model in a test that
// does not build an App.
//
// Prefer Model for another model of the same provider: a handle built outside
// core.NewApp carries none of the instrumentation.
func (t *Typed[T]) Using(model core.ILLM) *Typed[T] {
	cp := t.clone()
	cp.model = model
	return cp
}

// MaxTokens caps the reply. A schema with many fields needs room: hitting the
// cap produces truncated JSON, which fails to parse rather than arriving short.
func (t *Typed[T]) MaxTokens(n int) *Typed[T] {
	cp := t.clone()
	cp.req.MaxTokens = n
	return cp
}

// Reasoning sets how hard to think. Worth raising for extraction that requires
// inference (a total that must be computed, a category that is implied).
func (t *Typed[T]) Reasoning(level core.LLMReasoning) *Typed[T] {
	cp := t.clone()
	cp.req.Reasoning = level
	return cp
}

// CacheSystem marks the system prompt as a prompt-cache prefix — worth it when
// the same long instructions run over many inputs, which is the usual shape of
// an extraction job.
func (t *Typed[T]) CacheSystem() *Typed[T] {
	cp := t.clone()
	cp.req.CacheSystem = true
	return cp
}

// ProviderOptions passes provider-specific parameters straight through.
func (t *Typed[T]) ProviderOptions(opts map[string]any) *Typed[T] {
	cp := t.clone()
	cp.req.ProviderOptions = opts
	return cp
}

// SchemaName overrides the name the schema is sent under. Only a few providers
// surface it; it is here because those that do put it in their error messages.
func (t *Typed[T]) SchemaName(name string) *Typed[T] {
	cp := t.clone()
	cp.name = name
	return cp
}

// Lenient accepts a reply the model wrapped in prose or a ```json fence.
//
// Off by default: a model that ignored its schema is usually a prompt worth
// fixing, and silently repairing the output hides that. Turn it on for a model
// whose structured-output mode is weak — several small local ones — where the
// alternative is not using it at all.
func (t *Typed[T]) Lenient() *Typed[T] {
	cp := t.clone()
	cp.strict = false
	return cp
}

// Validate runs a check on the parsed value before it is returned. Use it for
// rules a JSON schema cannot express — a total that must equal the sum of the
// lines, a date that cannot be in the future.
//
// A T implementing core.IValidateContext is checked automatically when ctx is
// an IContext, so a type already used as a request payload needs nothing here.
func (t *Typed[T]) Validate(fn func(T) core.IError) *Typed[T] {
	cp := t.clone()
	cp.validate = func(v *T, _ context.Context) core.IError { return fn(*v) }
	return cp
}

// Result is one structured generation, with what it cost.
type Result[T any] struct {
	Value    T
	Usage    core.LLMUsage
	Model    string
	Provider string
	// JSON is exactly what the model answered, before parsing — the thing to
	// log or attach to a Sentry event when a value comes back wrong.
	JSON string
}

// Extract asks for text and returns the value. The common case:
//
//	inv, err := llm.New[Invoice](ctx).System(rules).Extract(ocrText)
func (t *Typed[T]) Extract(text string) (T, core.IError) {
	return t.Ask(text).Generate()
}

// Generate runs the conversation built so far.
func (t *Typed[T]) Generate() (T, core.IError) {
	res, err := t.Result()
	return res.Value, err
}

// Result runs the generation and returns the value together with its usage —
// for a caller that records cost per document, or logs what the model answered
// when a value looks wrong.
func (t *Typed[T]) Result() (Result[T], core.IError) {
	var out Result[T]

	schema, err := SchemaOf[T]()
	if err != nil {
		return out, err
	}
	if t.name != "" {
		schema.Name = t.name
	}

	model := t.model
	if model == nil {
		model = core.LLM(t.ctx)
	} else if t.ctx != nil {
		// A handle given to Using was built at startup and carries no deadline.
		// Binding it here is what makes the generation stop when the request it
		// belongs to does — the same thing core.LLM(ctx) does for the App's own.
		model = model.WithContext(t.ctx)
	}
	if !model.Capabilities().StructuredOutput && model.Enabled() {
		// Better here than as a parse failure three layers down: the model is
		// answering in prose because it was never told to do otherwise.
		return out, core.LLMUnsupportedError(model.Provider(), "structured output")
	}

	req := t.req
	req.Schema = schema

	resp, err := model.Generate(req)
	if err != nil {
		return out, err
	}

	body := resp.Text
	if !t.strict {
		body = unwrapJSON(body)
	}

	var value T
	if jerr := json.Unmarshal([]byte(body), &value); jerr != nil {
		// The raw reply goes in the error: without it "invalid character 'H'"
		// is unactionable, and the reply is gone.
		return out, core.Wrapf(jerr, "llm: %s answered with something that is not the requested JSON: %s",
			resp.Provider, truncate(body, 400))
	}

	out = Result[T]{
		Value:    value,
		Usage:    resp.Usage,
		Model:    resp.Model,
		Provider: resp.Provider,
		JSON:     resp.Text,
	}

	if verr := t.runValidation(&value); verr != nil {
		return out, verr
	}
	out.Value = value
	return out, nil
}

// runValidation applies the explicit check, then the one the type carries.
func (t *Typed[T]) runValidation(v *T) core.IError {
	if t.validate != nil {
		if err := t.validate(v, t.ctx); err != nil {
			return err
		}
	}
	// A type already used as an HTTP payload validates the same way here, so
	// the rules live in one place rather than being restated per call site.
	if ictx, ok := t.ctx.(core.IContext); ok {
		if vc, ok := any(v).(core.IValidateContext); ok {
			return vc.Valid(ictx)
		}
	}
	return nil
}

// unwrapJSON pulls the object out of a reply that arrived wrapped in prose or a
// fenced block. Only used when Lenient is on.
func unwrapJSON(s string) string {
	s = strings.TrimSpace(s)
	if fence := strings.Index(s, "```"); fence >= 0 {
		rest := s[fence+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			rest = rest[:end]
		}
		s = strings.TrimSpace(rest)
	}
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return s
	}
	open := s[start]
	close := byte('}')
	if open == '[' {
		close = ']'
	}
	if end := strings.LastIndexByte(s, close); end > start {
		return s[start : end+1]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
