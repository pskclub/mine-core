package main

import (
	"context"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/llm"
)

// --- Example 2: a value, not a paragraph ------------------------------------
//
// Generate returns text, which is right for a chat reply and wrong for
// everything else a backend does with a model. Extraction, classification and
// scoring all want a Go value, and getting one out of text means describing a
// schema, asking for JSON, parsing it and checking it — four steps that every
// service would otherwise write again, slightly differently.
//
// llm.New[T] is those four steps: the schema is derived from T by reflection,
// the model is constrained to it, and the reply is unmarshalled back into T.
// The failure mode it removes is the quiet one — a model that helpfully wrote
// "Here is the JSON you asked for:" in front of the object, which json.Unmarshal
// rejects on a line nowhere near the prompt that caused it.

// Invoice is the result type. The jsonschema tag carries what the schema can
// express; anything longer belongs in the system prompt, where wording lives.
//
// A pointer field or omitempty makes a field optional — everything else is
// listed in `required`, and the object is closed, so a model cannot invent an
// extra key and have it silently dropped at unmarshal time.
type Invoice struct {
	Vendor string    `json:"vendor" jsonschema:"description=Company that issued the invoice"`
	Number string    `json:"number" jsonschema:"description=Invoice number as printed"`
	Total  float64   `json:"total" jsonschema:"description=Grand total including tax"`
	Status string    `json:"status" jsonschema:"enum=draft|sent|paid"`
	Due    time.Time `json:"due"`
	Note   string    `json:"note,omitempty"`
}

const invoiceRules = `Read the OCR text of a Thai invoice and return its fields.
Amounts are THB. Strip thousands separators.
If a field is genuinely absent, leave it empty rather than guessing.`

// extractInvoice is the common case: text in, value out.
//
// Validate covers the rules a JSON schema cannot express. It runs on the parsed
// value before it is returned, so a caller never has to remember to check —
// which is the difference between a rule and a convention.
func extractInvoice(ctx context.Context, ocr string) (Invoice, core.IError) {
	return llm.New[Invoice](ctx).
		System(invoiceRules).
		// The instructions are long and identical for every document, which is
		// exactly the shape prompt caching pays for.
		CacheSystem().
		// A schema with several fields needs room: hitting the cap truncates the
		// JSON, which fails to parse rather than arriving short.
		MaxTokens(1024).
		Reasoning(core.LLMReasoningLow).
		Validate(func(inv Invoice) core.IError {
			if inv.Total <= 0 {
				return core.New(422, "INVALID_TOTAL", "an invoice with no total was not read correctly")
			}
			if inv.Due.After(time.Now().AddDate(5, 0, 0)) {
				// A due date five years out is the model misreading a Buddhist-era
				// year, and it is worth failing on rather than storing.
				return core.Newf(422, "INVALID_DUE_DATE", "due date %s is implausible", inv.Due.Format(time.DateOnly))
			}
			return nil
		}).
		Extract(ocr)
}

// Category is a one-field result, which is the cheapest useful shape there is:
// a classification that the compiler checks and a switch can branch on.
type Category struct {
	Label      string  `json:"label" jsonschema:"enum=billing|technical|sales|abuse|other"`
	Confidence float64 `json:"confidence" jsonschema:"description=0 to 1"`
}

// classifyTicket routes a cheap job to a cheap model.
//
// Model changes the model *within the configured provider*, so the call is
// still metered, logged and attributed like every other. Building a second
// client to change model is the alternative, and its tokens land in nobody's
// dashboard.
func classifyTicket(ctx context.Context, body string) (Category, core.IError) {
	return llm.New[Category](ctx).
		System("Classify the support ticket. Answer with the label only.").
		Model("gemini-flash-lite-latest").
		MaxTokens(64).
		Extract(body)
}

// extractInvoiceWithCost is the same extraction when the cost has to be
// recorded per document — a per-tenant bill, a spend dashboard, a limit.
//
// Result carries the raw JSON as well, which is the thing to attach to a Sentry
// event when a value comes back wrong: by the time anyone looks, the reply is
// otherwise gone.
func extractInvoiceWithCost(ctx context.Context, ocr string) (llm.Result[Invoice], core.IError) {
	// Result, not Generate: the value alone throws away the usage numbers, and
	// they are gone for good — the provider does not answer "what did that
	// document cost" after the fact.
	return llm.New[Invoice](ctx).
		System(invoiceRules).
		CacheSystem().
		MaxTokens(1024).
		Ask(ocr).
		Result()
}

// reusableExtractor shows why every builder method returns a copy: a
// half-configured value is safe to build once at startup and share, because
// nothing a later call does can mutate it.
func reusableExtractor(ctx context.Context, docs []string) ([]Invoice, core.IError) {
	base := llm.New[Invoice](ctx).System(invoiceRules).CacheSystem().MaxTokens(1024)

	out := make([]Invoice, 0, len(docs))
	for _, doc := range docs {
		inv, err := base.Extract(doc) // base is unchanged by this
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, nil
}
