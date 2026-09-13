// Package valid is mine-core v2's validation layer: a typed, fluent builder.
// Build a Validator from an IContext, chain type-specific rules per field, and
// return v.Error(). Machine-readable codes are separated from human messages
// (an English catalog rendered via Render), and DB rules never swallow errors.
package valid

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	core "github.com/pskclub/mine-core/v2"
)

// Violation is a single failed rule on a single field (replaces v1's mis-named
// IValidMessage struct).
type Violation struct {
	Field   string `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	// In is where the value came from — "path", "query", "header" or "body".
	// Filled in by the HTTP layer from the payload's binding tags; empty for
	// validation outside a request (job parameters, say).
	In string `json:"-"`
}

// Errors is the accumulated validation result. It implements core.IError so it
// flows through the framework like any other error. A DB/infra failure during
// validation is surfaced as 500 (not a 400) — v1 swallowed such errors.
type Errors struct {
	items []Violation
	dbErr error
}

var _ core.IError = (*Errors)(nil)

func (e *Errors) Error() string {
	parts := make([]string, len(e.items))
	for i, v := range e.items {
		parts[i] = v.Field + ": " + v.Message
	}
	return strings.Join(parts, ", ")
}

func (e *Errors) GetCode() string {
	if e.dbErr != nil {
		return "VALIDATION_ERROR"
	}
	return "INVALID_PARAMS"
}

func (e *Errors) GetStatus() int {
	if e.dbErr != nil {
		return http.StatusInternalServerError
	}
	return http.StatusBadRequest
}

func (e *Errors) GetMessage() interface{} {
	if e.dbErr != nil {
		return "validation could not be completed"
	}
	return "Invalid parameters"
}

func (e *Errors) OriginalError() error {
	if e.dbErr != nil {
		return e.dbErr
	}
	return e
}

func (e *Errors) Unwrap() error { return e.dbErr }

// JSON renders the v1-compatible body: {code, message, fields:{field:{code,message,in,data}}}.
func (e *Errors) JSON() interface{} {
	fields := make(map[string]fieldError, len(e.items))
	for _, v := range e.items {
		fields[v.Field] = fieldError{Code: v.Code, Message: v.Message, In: v.In, Data: v.Data}
	}
	return body{Code: e.GetCode(), Message: fmt.Sprint(e.GetMessage()), Fields: fields}
}

type body struct {
	Code    string                `json:"code"`
	Message string                `json:"message"`
	Fields  map[string]fieldError `json:"fields,omitempty"`
}

type fieldError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	In      string `json:"in,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// SetFieldSources labels each violation with where its value came from, so a
// client can tell a bad query parameter from a bad body field instead of
// guessing which "id" the API meant.
//
// Keys are field paths with slice indices removed ("items.name" matches
// "items.0.name", "items.1.name", …), because a source is a property of the
// field, not of the element. Unknown fields are left unlabelled rather than
// guessed at. Called by the HTTP layer; safe to call more than once.
func (e *Errors) SetFieldSources(sources map[string]string) {
	if len(sources) == 0 {
		return
	}
	for i := range e.items {
		if src, ok := sources[NormalizeFieldPath(e.items[i].Field)]; ok {
			e.items[i].In = src
		}
	}
}

// NormalizeFieldPath drops slice indices from a field path, so per-element
// violations map back to the field that declared them.
func NormalizeFieldPath(field string) string {
	if !strings.ContainsRune(field, '.') {
		return field
	}
	parts := strings.Split(field, ".")
	kept := parts[:0]
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err == nil {
			continue // an index, not a name
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, ".")
}

// Validator accumulates violations for one request.
type Validator struct {
	ctx    core.IContext
	errs   *Errors
	prefix string
}

// New creates a Validator bound to ctx (used by DB rules).
func New(ctx core.IContext) *Validator {
	return &Validator{ctx: ctx, errs: &Errors{}}
}

// Error returns the accumulated error, or nil when everything passed.
func (v *Validator) Error() core.IError {
	if len(v.errs.items) == 0 && v.errs.dbErr == nil {
		return nil
	}
	return v.errs
}

// add records a violation (stop-on-first is enforced per field builder) and
// returns its index, so a field builder's Message() can override its text.
func (v *Validator) add(field, code string, data any) int {
	v.errs.items = append(v.errs.items, Violation{
		Field:   v.prefix + field,
		Code:    code,
		Message: render(code, v.prefix+field, data),
		Data:    data,
	})
	return len(v.errs.items) - 1
}

// overrideMessage replaces the message of the violation at idx (used by the
// per-field Message() helpers). A negative idx is a no-op.
func (v *Validator) overrideMessage(idx int, msg string) {
	if idx >= 0 && idx < len(v.errs.items) {
		v.errs.items[idx].Message = msg
	}
}

// Each validates every element of a slice with an index-prefixed sub-validator.
func (v *Validator) Each(name string, length int, fn func(iv *Validator, i int)) {
	for i := 0; i < length; i++ {
		iv := &Validator{ctx: v.ctx, errs: v.errs, prefix: fmt.Sprintf("%s%s.%d.", v.prefix, name, i)}
		fn(iv, i)
	}
}

// Field builders ---------------------------------------------------------------

// Str starts a string field rule chain.
func (v *Validator) Str(name string, value *string) *StringField {
	return &StringField{v: v, name: name, value: value}
}

// Int starts an int64 field rule chain.
func (v *Validator) Int(name string, value *int64) *NumberField[int64] {
	return &NumberField[int64]{v: v, name: name, value: value}
}

// Float starts a float64 field rule chain.
func (v *Validator) Float(name string, value *float64) *NumberField[float64] {
	return &NumberField[float64]{v: v, name: name, value: value}
}

// Bool starts a bool field rule chain.
func (v *Validator) Bool(name string, value *bool) *BoolField {
	return &BoolField{v: v, name: name, value: value}
}

// Conditional & cross-field ----------------------------------------------------

// When runs fn only when cond holds — a conditional validation block (e.g. a
// field that is required only when another field has a certain value).
func (v *Validator) When(cond bool, fn func(v *Validator)) *Validator {
	if cond {
		fn(v)
	}
	return v
}

// Must records a violation for field with code when ok is false. Use it for
// cross-field or custom conditions the typed builders don't cover. Optional data
// is attached to the violation (and available to the message template).
func (v *Validator) Must(field, code string, ok bool, data ...any) *Validator {
	if !ok {
		var d any
		if len(data) > 0 {
			d = data[0]
		}
		v.add(field, code, d)
	}
	return v
}

// Reusable nested validators ---------------------------------------------------

// IValidatable is a reusable request piece that contributes its rules to a
// Validator. Implement it once and plug the same type in via Nested / EachNested
// / Run — it then works both standalone and nested inside larger requests.
type IValidatable interface {
	Validate(v *Validator)
}

// Run builds a Validator, applies sub's rules, and returns the result. Handy for
// a request's Valid(ctx) that delegates to a reusable Validate method:
//
//	func (r *AddressRequest) Valid(ctx core.IContext) core.IError { return valid.Run(ctx, r) }
func Run(ctx core.IContext, sub IValidatable) core.IError {
	v := New(ctx)
	sub.Validate(v)
	return v.Error()
}

// Nested applies sub's rules under a "name." field prefix (e.g. address.street),
// reusing the same IValidatable across many parent requests.
func (v *Validator) Nested(name string, sub IValidatable) *Validator {
	sub.Validate(v.group(name))
	return v
}

// group returns a child validator sharing this one's errors/context with an
// extended field prefix.
func (v *Validator) group(name string) *Validator {
	return &Validator{ctx: v.ctx, errs: v.errs, prefix: v.prefix + name + "."}
}

// EachNested applies each item's rules under an indexed prefix (name.0., name.1.
// …). The element type must be a pointer implementing IValidatable.
func EachNested[T IValidatable](v *Validator, name string, items []T) {
	for i, item := range items {
		item.Validate(&Validator{
			ctx:    v.ctx,
			errs:   v.errs,
			prefix: fmt.Sprintf("%s%s.%d.", v.prefix, name, i),
		})
	}
}

// Ctx exposes the bound context so reusable validators can run DB rules.
func (v *Validator) Ctx() core.IContext { return v.ctx }
