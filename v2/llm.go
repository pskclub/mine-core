package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrLLMDisabled is wrapped by the error every operation returns when no AI_*
// configuration is set.
//
// The model does not degrade the way the cache does. A cache miss is
// recoverable — the value is recomputed — but a generation that quietly
// returned nothing is an empty answer the caller believes is real, so a service
// with no provider fails loudly instead.
var ErrLLMDisabled = errors.New("llm: not configured")

// ErrLLMUnsupported is wrapped by the error a driver returns when the request
// asks for something the chosen model cannot do.
//
// It is a distinct sentinel because the caller's remedy is different: a disabled
// model is fixed by configuration, an unsupported feature by picking another
// model or dropping the field.
var ErrLLMUnsupported = errors.New("llm: capability not supported")

// LLMRole identifies who authored a message. There is no system role — the
// system prompt is its own field on LLMRequest, because providers disagree on
// where it belongs (a leading message, a top-level field, a per-turn block) and
// a driver can only place it correctly if it knows which one it is.
type LLMRole string

const (
	LLMRoleUser      LLMRole = "user"
	LLMRoleAssistant LLMRole = "assistant"
)

// LLMPartType is the kind of attachment carried alongside a message's text.
type LLMPartType string

const (
	LLMPartImage LLMPartType = "image"
	// LLMPartFile is a document — a PDF, a spreadsheet, a text file. Support
	// varies far more than images do: some providers read PDFs natively, others
	// reject them, and a driver that cannot send one says so rather than
	// dropping it.
	LLMPartFile LLMPartType = "file"
)

// LLMPart is something attached to a message that is not text.
//
// Exactly one of Data or URL is set. Data is the bytes themselves — the usual
// case, since a scan or a screenshot is already in memory; URL points at
// something the provider fetches, which only some of them will do.
type LLMPart struct {
	Type LLMPartType
	// Data is the raw bytes. Drivers base64-encode it, which inflates it by a
	// third on the wire — see LLMMaxAttachmentBytes.
	Data []byte
	// URL is a link the provider fetches, or a data: URI. Mutually exclusive
	// with Data.
	URL string
	// MediaType is required with Data ("image/jpeg", "application/pdf"). It
	// cannot be guessed from bytes reliably enough to be worth guessing: a
	// wrong one is rejected by the provider with an error about the image, not
	// about the media type.
	MediaType string
	// Filename is shown to the model for a file. Some providers use it as a
	// hint about the content, so a name like "invoice-2026-08.pdf" is worth
	// more than "upload".
	Filename string
	// Detail asks for a resolution tier on providers that offer one:
	// "low", "high" or "auto". Empty leaves the provider's default. "low" costs
	// far fewer tokens and is enough for a layout question but not for reading
	// small print.
	Detail string
}

// LLMMaxAttachmentBytes is the total attachment size one request may carry.
//
// It is a guard, not a provider limit: the real ceilings differ (Anthropic 32MB
// per request, OpenAI and Google around 20MB) and they apply *after* base64
// inflates the payload by a third. Failing here names the problem; failing at
// the provider returns a size error against a request that also had a prompt in
// it, several seconds and one billed round trip later.
const LLMMaxAttachmentBytes = 15 << 20 // 15 MiB, ~20 MiB once encoded

// LLMMessage is one turn of the conversation.
type LLMMessage struct {
	Role LLMRole
	Text string
	// Parts are attachments — images, documents. Empty for an ordinary text
	// turn, which is what keeps every existing call site working unchanged.
	Parts []LLMPart
}

// LLMUser returns a user turn.
func LLMUser(text string) LLMMessage { return LLMMessage{Role: LLMRoleUser, Text: text} }

// With attaches parts to a message:
//
//	core.LLMUser("ยอดรวมเท่าไหร่").With(core.LLMImage(scan, "image/jpeg"))
//
// The text still matters — a model given an image and no instruction describes
// it, which is rarely what the caller wanted.
func (m LLMMessage) With(parts ...LLMPart) LLMMessage {
	m.Parts = append(append([]LLMPart(nil), m.Parts...), parts...)
	return m
}

// LLMImage attaches an image from bytes.
func LLMImage(data []byte, mediaType string) LLMPart {
	return LLMPart{Type: LLMPartImage, Data: data, MediaType: mediaType}
}

// LLMImageURL attaches an image the provider fetches itself. Not every provider
// will — the ones that do not return LLM_UNSUPPORTED rather than sending a
// message with a hole where the picture should be.
func LLMImageURL(url string) LLMPart {
	return LLMPart{Type: LLMPartImage, URL: url}
}

// LLMFile attaches a document. filename is optional but worth setting.
func LLMFile(data []byte, mediaType, filename string) LLMPart {
	return LLMPart{Type: LLMPartFile, Data: data, MediaType: mediaType, Filename: filename}
}

// LLMAssistant returns an assistant turn — for replaying history, not for
// prefilling a reply. Several current models reject a conversation that ends on
// an assistant turn.
func LLMAssistant(text string) LLMMessage {
	return LLMMessage{Role: LLMRoleAssistant, Text: text}
}

// LLMReasoning is how much thinking to spend, as a level rather than a token
// budget. Providers express this differently — an effort enum, a token budget,
// a boolean — and a level is the only form all of them can honour.
type LLMReasoning string

const (
	// LLMReasoningDefault leaves the provider's own default in place. It is the
	// zero value, so a request that says nothing about reasoning gets whatever
	// the model does normally.
	LLMReasoningDefault LLMReasoning = ""
	LLMReasoningOff     LLMReasoning = "off"
	LLMReasoningLow     LLMReasoning = "low"
	LLMReasoningMedium  LLMReasoning = "medium"
	LLMReasoningHigh    LLMReasoning = "high"
	LLMReasoningMax     LLMReasoning = "max"
)

// LLMSchema constrains the reply to a JSON schema. Schema is the schema itself,
// as the provider expects it; Name is what some providers require alongside it.
type LLMSchema struct {
	Name   string
	Schema map[string]any
	// Strict asks the provider to guarantee the shape rather than merely
	// request it. Drivers that cannot enforce it must fail rather than silently
	// downgrade — a schema that was only a suggestion is the failure mode this
	// whole type exists to prevent.
	Strict bool
}

// LLMToolCall is one invocation the model asked for.
type LLMToolCall struct {
	// ID is the provider's own identifier for the call, used to match the
	// result back. Empty for providers that do not issue one.
	ID   string
	Name string
	// Input is the arguments as the model produced them. It is raw JSON rather
	// than a map so a tool can unmarshal straight into its own type.
	Input json.RawMessage
	// Outcome is what happened once the call was executed — empty on a call the
	// model merely asked for, because the loop ran out of steps before it ran.
	Outcome LLMToolOutcome
	// Output is what went back to the model: the tool's own result, or the text
	// explaining a denial or a failure.
	//
	// It completes the audit trail — "the model asked to refund an order",
	// "the refund ran" and "the refund returned this" are three different
	// facts, and a record holding only the first two cannot answer what the
	// model was actually told. Empty for a call that never ran.
	//
	// Like the arguments, it is application data and can carry anything the
	// tool reads, so it is only written to the log when AI_LOG_COMPLETION says
	// so.
	Output string
}

// LLMTool is a function the model may call during a generation.
//
// Build one with llm.Tool[In] rather than by hand — it derives Schema from the
// input type, which is the part that is tedious to write and easy to get subtly
// wrong.
//
// A tool with ProviderType set is the other kind: something the provider runs
// itself, on its own side of the wire — Google Search grounding, URL fetching,
// a sandboxed interpreter. It has no Execute because there is nothing here to
// run; build one with the driver's constructor (goai.GoogleSearch()) rather
// than by spelling the type out.
type LLMTool struct {
	Name string
	// Description is what the model reads to decide whether to call it. Say
	// *when* to use it, not only what it does: "call this when the user asks
	// about an order's status" beats "looks up orders".
	Description string
	// Schema is the JSON Schema of the input, as a closed object.
	Schema map[string]any
	// Execute runs the tool. Both its result and its error text are sent back
	// to the model, so neither may carry a credential, an internal path, or
	// anything else that should not leave the process.
	//
	// Required for an ordinary tool, and necessarily absent for a
	// provider-defined one.
	Execute func(ctx context.Context, input json.RawMessage) (string, IError)
	// ProviderType marks this as a tool the provider implements, named the way
	// that provider names it ("google.google_search"). The framework does not
	// interpret it: a driver either recognises it or rejects the request, which
	// is what keeps an option meant for one provider from being quietly ignored
	// by another.
	ProviderType string
	// ProviderOptions configures a provider-defined tool — a time-range filter
	// on a search, a display size for computer use. Ignored for an ordinary
	// tool.
	ProviderOptions map[string]any
}

// IsProviderDefined reports whether the provider runs this tool itself.
func (t LLMTool) IsProviderDefined() bool { return t.ProviderType != "" }

// LLMToolApprover decides whether a call the model asked for may run.
//
// Returning an error denies it: the error's message goes back to the model as
// the tool result, so the model can explain itself or try another route, and
// the generation continues rather than failing.
//
// This is the reason tool use belongs in the framework rather than being left
// to each service — a loop that can write to a database needs one place where
// the decision is made, logged and testable:
//
//	Approve: func(ctx context.Context, call core.LLMToolCall) core.IError {
//	    if call.Name == "refund_order" {
//	        return core.New(403, "NEEDS_HUMAN", "refunds require an operator")
//	    }
//	    return nil
//	}
type LLMToolApprover func(ctx context.Context, call LLMToolCall) IError

// LLMRequest is one generation. Every field beyond Messages is optional; a
// driver that cannot honour a field it was given returns ErrLLMUnsupported
// rather than dropping it, so the request never runs as something other than
// what the caller asked for.
type LLMRequest struct {
	// Model overrides the configured default. It is the provider's own model
	// id — routing a friendly name to one belongs in configuration, not here.
	Model string
	// System is the system prompt. Keep it byte-stable across requests: it sits
	// at the front of the prompt, so anything varying in it (a timestamp, a
	// request id) defeats prompt caching for everything after it.
	System string
	// Messages is the conversation, oldest first. Must not be empty.
	Messages []LLMMessage
	// MaxTokens caps the reply. 0 uses the configured default.
	MaxTokens int
	// Temperature is a pointer because 0 is a meaningful value and several
	// current models reject the parameter entirely — nil means "do not send it".
	Temperature   *float64
	StopSequences []string
	// Reasoning is how hard to think. See LLMReasoning.
	Reasoning LLMReasoning
	// Schema constrains the reply to JSON. nil means free text.
	Schema *LLMSchema
	// CacheSystem marks the system prompt as a prompt-cache prefix. It is a
	// hint: providers that cache automatically ignore it, and drivers that
	// cannot cache ignore it too, because a missed cache costs money but never
	// changes the answer.
	CacheSystem bool
	// Tools the model may call. With none, the model can only answer.
	Tools []LLMTool
	// MaxSteps bounds the tool loop: one model turn plus its tool results is a
	// step. 0 and 1 both mean a single turn, so a request that lists tools but
	// forgets MaxSteps gets the calls back rather than silently looping.
	//
	// It exists because a loop with no ceiling is a loop that can bill without
	// end — a model that keeps calling a tool that keeps failing will happily
	// do so until something stops it.
	MaxSteps int
	// Approve gates every call before it runs. nil allows them all, which is
	// only appropriate when every tool is read-only.
	Approve LLMToolApprover
	// ProviderOptions carries provider-specific parameters straight through,
	// untranslated. It is the escape hatch that keeps this struct from having
	// to grow a field for every vendor's newest flag — at the cost of no
	// compile-time checking, so prefer a typed field when one exists.
	ProviderOptions map[string]any
}

// Validate checks the request the way every driver must, before anything is
// sent.
//
// It is exported because drivers live in other packages and each one needs the
// same answer: a caller's mistake has to read identically whichever provider is
// configured, and a second copy of these rules is a second thing to forget when
// one of them grows — which is exactly what happened the first time attachments
// were added.
func (r LLMRequest) Validate() IError { return r.validate() }

func (r LLMRequest) validate() IError {
	if len(r.Messages) == 0 {
		return &Error{Status: 400, Code: "LLM_INVALID_REQUEST", Message: "llm: request has no messages"}
	}
	attached := 0
	for i, m := range r.Messages {
		if m.Role != LLMRoleUser && m.Role != LLMRoleAssistant {
			return Newf(400, "LLM_INVALID_REQUEST", "llm: messages[%d] has unknown role %q", i, m.Role)
		}
		for j, p := range m.Parts {
			if err := p.validate(i, j); err != nil {
				return err
			}
			attached += len(p.Data)
		}
	}
	if attached > LLMMaxAttachmentBytes {
		return Newf(413, "LLM_ATTACHMENT_TOO_LARGE",
			"llm: attachments total %d bytes, over the %d-byte limit — resize the image or send fewer per request",
			attached, LLMMaxAttachmentBytes)
	}
	if r.Schema != nil && r.Schema.Schema == nil {
		return &Error{Status: 400, Code: "LLM_INVALID_REQUEST", Message: "llm: schema is set but empty"}
	}
	seen := map[string]bool{}
	for i, tool := range r.Tools {
		if tool.Name == "" {
			return Newf(400, "LLM_INVALID_REQUEST", "llm: tools[%d] has no name", i)
		}
		if seen[tool.Name] {
			// Providers key results back by name; two tools sharing one means
			// the wrong function runs, which is far worse than a rejection.
			return Newf(400, "LLM_INVALID_REQUEST", "llm: tool %q is declared twice", tool.Name)
		}
		seen[tool.Name] = true
		if tool.IsProviderDefined() {
			// The provider runs it, so an Execute here would never be called —
			// and a caller who wrote one believes their code gates the tool.
			if tool.Execute != nil {
				return Newf(400, "LLM_INVALID_REQUEST",
					"llm: tool %q is provider-defined (%s) and cannot have an Execute — the provider runs it, so this function would never be called",
					tool.Name, tool.ProviderType)
			}
			continue
		}
		if tool.Execute == nil {
			return Newf(400, "LLM_INVALID_REQUEST", "llm: tool %q has no Execute", tool.Name)
		}
	}
	return nil
}

// validate checks one attachment. The checks are here rather than in a driver
// because a malformed part is dropped silently by the layer underneath — the
// message goes out with a hole where the picture should be, and the model
// answers about the text alone as though nothing were missing.
func (p LLMPart) validate(msgIdx, partIdx int) IError {
	where := fmt.Sprintf("messages[%d].Parts[%d]", msgIdx, partIdx)

	switch p.Type {
	case LLMPartImage, LLMPartFile:
	case "":
		return Newf(400, "LLM_INVALID_REQUEST", "llm: %s has no Type — use core.LLMImage or core.LLMFile", where)
	default:
		return Newf(400, "LLM_INVALID_REQUEST", "llm: %s has unknown Type %q", where, p.Type)
	}

	switch {
	case len(p.Data) == 0 && p.URL == "":
		return Newf(400, "LLM_INVALID_REQUEST", "llm: %s has neither Data nor URL", where)
	case len(p.Data) > 0 && p.URL != "":
		return Newf(400, "LLM_INVALID_REQUEST", "llm: %s has both Data and URL — pick one", where)
	}

	if len(p.Data) > 0 && p.MediaType == "" {
		return Newf(400, "LLM_INVALID_REQUEST",
			"llm: %s needs a MediaType (\"image/jpeg\", \"application/pdf\") — it cannot be guessed from the bytes", where)
	}
	if p.MediaType != "" && !strings.Contains(p.MediaType, "/") {
		return Newf(400, "LLM_INVALID_REQUEST", "llm: %s has MediaType %q, which is not a media type", where, p.MediaType)
	}
	switch p.Detail {
	case "", "low", "high", "auto":
	default:
		return Newf(400, "LLM_INVALID_REQUEST", "llm: %s has Detail %q — use low, high or auto", where, p.Detail)
	}
	return nil
}

// attachments is how many parts and how many bytes the request carries. Used by
// the log line: an image costs thousands of tokens that the character count of
// the prompt says nothing about, so without this the debug line would imply a
// request was cheap when it was the opposite.
func (r LLMRequest) attachments() (count, bytes int) {
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			count++
			bytes += len(p.Data)
		}
	}
	return count, bytes
}

// Steps is the tool-loop ceiling this request actually runs with.
func (r LLMRequest) steps() int {
	if r.MaxSteps < 1 {
		return 1
	}
	return r.MaxSteps
}

// LLMToolOutcome is what happened to one call. It exists so the audit trail
// records the decision and not only the attempt — "the model asked to refund an
// order" and "the model refunded an order" are different events, and a log that
// cannot tell them apart is worse than no log.
type LLMToolOutcome string

const (
	LLMToolRan     LLMToolOutcome = "ran"
	LLMToolDenied  LLMToolOutcome = "denied"
	LLMToolFailed  LLMToolOutcome = "failed"
	LLMToolUnknown LLMToolOutcome = "unknown_tool"
)

// LLMToolResult is one executed call.
type LLMToolResult struct {
	// Output is what goes back to the model — including when it describes a
	// refusal or a failure, because a model told why its call did not run can
	// adapt, while one told nothing repeats itself until MaxSteps runs out.
	Output  string
	Outcome LLMToolOutcome
	// Err is the failure or the refusal, for the log. It never reaches the
	// model as an error — only its message does, inside Output.
	Err IError
}

// RunTool executes one call against the request's tools, applying Approve
// first. Drivers use it so the gate, the not-found error and the panic guard
// behave identically no matter which provider drove the loop.
func (r LLMRequest) RunTool(ctx context.Context, call LLMToolCall) LLMToolResult {
	var tool *LLMTool
	for i := range r.Tools {
		if r.Tools[i].Name == call.Name {
			tool = &r.Tools[i]
			break
		}
	}
	if tool == nil {
		err := Newf(400, "LLM_UNKNOWN_TOOL", "llm: no tool named %q is available", call.Name)
		return LLMToolResult{
			Output:  fmt.Sprintf("error: no tool named %q is available", call.Name),
			Outcome: LLMToolUnknown,
			Err:     err,
		}
	}
	if tool.IsProviderDefined() {
		// Reaching here means a driver routed a server-side tool back into the
		// local loop. Nothing can run it, and pretending otherwise would send
		// the model a made-up result.
		err := Newf(500, "LLM_TOOL_NOT_LOCAL",
			"llm: tool %q is run by %s and has nothing to execute here", call.Name, tool.ProviderType)
		return LLMToolResult{
			Output:  fmt.Sprintf("error: tool %q is run by the provider", call.Name),
			Outcome: LLMToolFailed,
			Err:     err,
		}
	}
	if r.Approve != nil {
		if err := r.Approve(ctx, call); err != nil {
			return LLMToolResult{
				Output:  "denied: " + fmt.Sprint(err.GetMessage()),
				Outcome: LLMToolDenied,
				Err:     err,
			}
		}
	}

	out, err := runToolGuarded(ctx, tool, call)
	if err != nil {
		return LLMToolResult{
			Output:  "error: " + fmt.Sprint(err.GetMessage()),
			Outcome: LLMToolFailed,
			Err:     err,
		}
	}
	return LLMToolResult{Output: out, Outcome: LLMToolRan}
}

// runToolGuarded keeps a panicking tool from taking the process with it. A tool
// is ordinary application code reached through a model's decision, so it is
// exactly the code path least likely to have been run before.
func runToolGuarded(ctx context.Context, tool *LLMTool, call LLMToolCall) (out string, err IError) {
	// Two defers, and the order matters: Recover must be the deferred function
	// itself (recover only works one frame down), so the conversion to IError
	// runs in a second defer that fires after it.
	var panicked error
	defer func() {
		if panicked != nil {
			err = Wrapf(panicked, "llm: tool %q panicked", call.Name)
		}
	}()
	defer Recover(&panicked)
	return tool.Execute(ctx, call.Input)
}

// LLMFinishReason is why generation stopped, normalised across providers.
type LLMFinishReason string

const (
	LLMFinishStop          LLMFinishReason = "stop"
	LLMFinishLength        LLMFinishReason = "length"
	LLMFinishContentFilter LLMFinishReason = "content_filter"
	LLMFinishToolUse       LLMFinishReason = "tool_use"
	LLMFinishError         LLMFinishReason = "error"
	LLMFinishOther         LLMFinishReason = "other"
)

// LLMUsage is what the call consumed. CachedInputTokens and ReasoningTokens are
// reported separately because they are billed differently from ordinary input
// and output — folding them in would make a cost report wrong in the direction
// that looks fine.
type LLMUsage struct {
	InputTokens       int
	OutputTokens      int
	CachedInputTokens int
	ReasoningTokens   int
}

// Total is every token the call touched.
func (u LLMUsage) Total() int {
	return u.InputTokens + u.OutputTokens + u.CachedInputTokens + u.ReasoningTokens
}

// LLMResponse is one completed generation.
type LLMResponse struct {
	Text         string
	Model        string
	Provider     string
	FinishReason LLMFinishReason
	Usage        LLMUsage
	// Steps is how many model turns it took. 1 means the model answered
	// directly; more means it called tools along the way.
	Steps int
	// ToolCalls is every call the model made, in order — the audit trail of
	// what a loop actually did, which is the thing you want when it did
	// something surprising.
	ToolCalls []LLMToolCall
	// Sources is what a grounded answer was based on. It is only populated by
	// providers that ground — a Google Search or URL-context tool, a provider's
	// own citations — and is the part of grounding that has to reach the user:
	// an answer about current events with no link to what it read is exactly as
	// trustworthy as one the model invented.
	Sources []LLMSource
	// Raw is the driver's own response value, for reading something this struct
	// does not carry. Type-assert it to the driver's type; it may be nil.
	Raw any
}

// LLMSource is one thing a grounded answer cited.
type LLMSource struct {
	// ID is the provider's own identifier, empty for providers that issue none.
	ID string
	// Type is the kind of source — "url" for a search result, "document" for a
	// retrieved file.
	Type  string
	URL   string
	Title string
}

// LLMStream is a generation being received. It is an iterator rather than a
// channel so that ending early is an ordinary Close rather than a leaked
// goroutine, and so the terminal error has one place to live:
//
//	s, err := core.LLM(ctx).Stream(req)
//	if err != nil { return err }
//	defer s.Close()
//	for s.Next() {
//	    fmt.Print(s.Text())
//	}
//	if err := s.Err(); err != nil { return err }
//	usage := s.Response().Usage
type LLMStream interface {
	// Next advances to the next delta, returning false at the end of the
	// stream or on error.
	Next() bool
	// Text is the delta Next just advanced to — a fragment, not the whole
	// reply so far.
	Text() string
	// Response is the accumulated result. It is only complete once Next has
	// returned false.
	Response() LLMResponse
	// Err is the error that ended the stream, or nil if it ended normally.
	Err() IError
	// Close releases the underlying connection. Safe to call more than once,
	// and safe to call before the stream is drained.
	Close() IError
}

// LLMCapabilities is what a model can actually do. Drivers report it so a
// caller can branch before sending rather than discovering the answer as an
// error — and so the disabled model can report nothing without lying.
type LLMCapabilities struct {
	Streaming        bool
	StructuredOutput bool
	Reasoning        bool
	PromptCaching    bool
	TokenCounting    bool
	Tools            bool
	// Vision reports whether this driver can *send* attachments to this
	// provider — not whether the configured model can read them. Those are
	// different questions and only the first has an answer the driver knows:
	// vision support varies by model, not by provider, and a model that cannot
	// see is rejected by the provider with its own error.
	//
	// What it guarantees is the thing that matters: with Vision true an image
	// is delivered or reported, never quietly dropped.
	Vision bool
}

// ILLM generates text from a language model.
//
// core.LLM(ctx) is never nil: a service with no AI_* configuration gets a
// disabled model whose every call fails with LLM_DISABLED.
//
// Methods take no context — the handle is already bound to the request or run
// that produced it. Use WithContext for a background deadline that must outlive
// the request.
type ILLM interface {
	// Generate runs one completion to the end.
	Generate(req LLMRequest) (LLMResponse, IError)
	// Stream starts a generation and returns it as deltas arrive. The caller
	// must Close the result.
	Stream(req LLMRequest) (LLMStream, IError)
	// CountTokens reports what req would cost as input, without running it.
	// Providers that have no counting endpoint return ErrLLMUnsupported rather
	// than an estimate — a wrong number is worse than no number, because it is
	// trusted.
	CountTokens(req LLMRequest) (int, IError)
	// Capabilities is what this model supports.
	Capabilities() LLMCapabilities
	// Model is the model id calls default to.
	Model() string
	// Provider names the service behind this handle ("anthropic", "openai",
	// "memory", ""). It is a label for logs and metrics, not a switch to branch
	// on — branch on Capabilities instead.
	Provider() string
	// Enabled reports whether this is a real model.
	Enabled() bool
	// Unwrap returns the driver's underlying client for provider-specific work
	// this interface does not cover. It may be nil.
	Unwrap() any
	// WithContext returns a handle bound to a different context.
	WithContext(ctx context.Context) ILLM
	// Close releases whatever the driver holds. Owned by App.Shutdown.
	Close() IError
}

// LLM returns the application's language model bound to ctx.
//
// Like core.Requester and core.Mailer it is a function rather than a method on
// IContext: a generation is not a capability of the request, it is a thing you
// do with the request's deadline attached — and IContext is already the
// interface everything in a service depends on, so it stays small.
//
//	resp, err := core.LLM(c).Generate(core.LLMRequest{Messages: ...})
//
// ctx is anything carrying the App — an IContext from a handler or a job, or a
// plain context.Context derived from one. A context from nowhere gets the
// disabled model rather than nil, so a call site never has to nil-check.
func LLM(ctx context.Context) ILLM {
	if app := appFrom(ctx); app != nil && app.llm != nil {
		return app.llm.WithContext(ctx)
	}
	return noopLLM{}
}

// LLMDisabledError is the error every disabled-model call fails with. Drivers
// should return it too when their own configuration turns out to be missing, so
// the failure reads the same wherever it came from.
func LLMDisabledError() IError { return llmDisabled() }

func llmDisabled() *Error {
	return &Error{
		Status:  503,
		Code:    "LLM_DISABLED",
		Message: "llm: no model is configured (set AI_PROVIDER and AI_API_KEY)",
		cause:   ErrLLMDisabled,
	}
}

// LLMUnsupportedError reports that a model cannot do what the request asked
// for. Drivers use it so the message is identical across providers, which is
// what makes it worth asserting on in a test.
func LLMUnsupportedError(provider, feature string) IError {
	return &Error{
		Status:  400,
		Code:    "LLM_UNSUPPORTED",
		Message: fmt.Sprintf("llm: %s does not support %s", provider, feature),
		cause:   ErrLLMUnsupported,
	}
}

// noopLLM is what core.LLM(ctx) returns when no provider is configured. Every
// call fails with the same error, which names the missing configuration.
type noopLLM struct{}

var _ ILLM = noopLLM{}

// NewNoopLLM returns a model that refuses every call. It is what a service with
// no AI_* configuration gets, so the failure is a clear error rather than a nil
// dereference.
func NewNoopLLM() ILLM { return noopLLM{} }

func (noopLLM) Generate(LLMRequest) (LLMResponse, IError) { return LLMResponse{}, llmDisabled() }
func (noopLLM) Stream(LLMRequest) (LLMStream, IError)     { return nil, llmDisabled() }
func (noopLLM) CountTokens(LLMRequest) (int, IError)      { return 0, llmDisabled() }
func (noopLLM) Capabilities() LLMCapabilities             { return LLMCapabilities{} }
func (noopLLM) Model() string                             { return "" }
func (noopLLM) Provider() string                          { return "" }
func (noopLLM) Enabled() bool                             { return false }
func (noopLLM) Unwrap() any                               { return nil }
func (noopLLM) WithContext(context.Context) ILLM          { return noopLLM{} }
func (noopLLM) Close() IError                             { return nil }

// instrumentLLM wraps l so every generation records its usage and latency.
//
// It lives here rather than in each driver because usage accounting is the one
// thing every service needs and no service should have to write: without it,
// "what did we spend on AI last month" has no answer, and by the time anyone
// asks, the calls are gone.
func instrumentLLM(l ILLM, log ILogger, env IENV) ILLM {
	if l == nil {
		return nil
	}
	slow := defaultSlowGeneration
	logPrompt, logCompletion := false, false
	if env != nil {
		if s := env.Config().AILogSlow; s > 0 {
			slow = time.Duration(s) * time.Second
		}
		logPrompt = env.Config().AILogPrompt
		logCompletion = env.Config().AILogCompletion
	}
	return &meteredLLM{
		ILLM: l,
		// blameApp: the generation is logged from inside the wrapper, on the
		// goroutine of the handler or job that asked for it — that call is the
		// useful frame, llm.go is not
		log:           blameApp(log),
		level:         llmLogLevelFrom(env),
		slow:          slow,
		logPrompt:     logPrompt,
		logCompletion: logCompletion,
	}
}

type meteredLLM struct {
	ILLM
	log           ILogger
	level         llmLogLevel
	slow          time.Duration
	logPrompt     bool
	logCompletion bool
	ctx           context.Context
}

// inner exposes the wrapped model so the test helpers can find a memory model
// through the instrumentation NewApp adds. It is deliberately not part of ILLM:
// production code has no business reaching past the wrapper, and Unwrap already
// covers reaching past the *driver*.
func (m *meteredLLM) inner() ILLM { return m.ILLM }

func (m *meteredLLM) WithContext(ctx context.Context) ILLM {
	cp := *m
	cp.ILLM = m.ILLM.WithContext(ctx)
	cp.ctx = ctx
	return &cp
}

func (m *meteredLLM) Generate(req LLMRequest) (LLMResponse, IError) {
	start := time.Now()
	resp, err := m.ILLM.Generate(req)
	m.record("generate", req, resp, time.Since(start), err)
	return resp, err
}

func (m *meteredLLM) Stream(req LLMRequest) (LLMStream, IError) {
	start := time.Now()
	s, err := m.ILLM.Stream(req)
	if err != nil {
		m.record("stream", req, LLMResponse{}, time.Since(start), err)
		return nil, err
	}
	return &meteredStream{LLMStream: s, owner: m, req: req, start: start}, nil
}

// meteredStream defers the measurement until the stream ends, so a stream's
// latency is time-to-last-token rather than time-to-first — the number that
// matches what Generate reports, and so the two are comparable.
type meteredStream struct {
	LLMStream
	owner    *meteredLLM
	req      LLMRequest
	start    time.Time
	recorded bool
}

func (s *meteredStream) Next() bool {
	if s.LLMStream.Next() {
		return true
	}
	s.finish()
	return false
}

func (s *meteredStream) Close() IError {
	// A stream abandoned early still consumed tokens. Recording on Close as
	// well as on drain means the spend shows up either way, and `recorded`
	// keeps it from being counted twice.
	s.finish()
	return s.LLMStream.Close()
}

func (s *meteredStream) finish() {
	if s.recorded {
		return
	}
	s.recorded = true
	s.owner.record("stream", s.req, s.LLMStream.Response(), time.Since(s.start), s.LLMStream.Err())
}

func (m *meteredLLM) record(op string, req LLMRequest, resp LLMResponse, took time.Duration, err IError) {
	provider := resp.Provider
	if provider == "" {
		provider = m.ILLM.Provider()
	}
	model := resp.Model
	if model == "" {
		model = req.Model
	}
	if model == "" {
		model = m.ILLM.Model()
	}

	attrs := []MetricOption{
		MetricAttr("provider", provider),
		MetricAttr("model", model),
		MetricAttr("operation", op),
	}

	meter := meterFrom(m.ctx)
	meter.Duration("llm.latency", took, attrs...)
	if u := resp.Usage; u.Total() > 0 {
		meter.Count("llm.tokens.input", int64(u.InputTokens), attrs...)
		meter.Count("llm.tokens.output", int64(u.OutputTokens), attrs...)
		if u.CachedInputTokens > 0 {
			meter.Count("llm.tokens.cached_input", int64(u.CachedInputTokens), attrs...)
		}
		if u.ReasoningTokens > 0 {
			meter.Count("llm.tokens.reasoning", int64(u.ReasoningTokens), attrs...)
		}
	}
	if err != nil {
		meter.Count("llm.errors", 1, append(attrs, MetricAttr("code", err.GetCode()))...)
	}

	if m.log == nil || m.level == llmLogSilent {
		return
	}

	// The prompt is not written unless AI_LOG_PROMPT says so: it routinely
	// carries whatever the user typed. Length is enough to explain a cost or a
	// truncation after the fact — and attachments are counted separately,
	// because an image costs thousands of tokens that a character count says
	// nothing about.
	attachCount, attachBytes := req.attachments()
	fields := []any{
		"provider", provider,
		"model", model,
		"operation", op,
		"took_ms", took.Milliseconds(),
		"prompt_chars", req.promptChars(),
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens,
		"cached_input_tokens", resp.Usage.CachedInputTokens,
		"finish_reason", string(resp.FinishReason),
	}
	if attachCount > 0 {
		fields = append(fields, "attachments", attachCount, "attachment_bytes", attachBytes)
	}
	if resp.Steps > 1 {
		fields = append(fields, "steps", resp.Steps, "tool_calls", len(resp.ToolCalls))
	}
	// Sources are counted whatever the settings, because a tool the *provider*
	// ran leaves no other trace: google_search never reaches RunTool, so without
	// this a generation that searched the web looks identical to one that
	// answered from memory. The count is not content — the URLs themselves wait
	// for AI_LOG_COMPLETION.
	if len(resp.Sources) > 0 {
		fields = append(fields, "sources", len(resp.Sources))
	}
	if m.logPrompt {
		fields = append(fields, req.promptFields()...)
	}
	if m.logCompletion {
		fields = append(fields, resp.completionFields()...)
	}

	msg := llmCallMessage(op, provider, model, resp.FinishReason, took)

	switch {
	// A provider being down is ours to notice. A 4xx is an answer — a rejected
	// prompt, a model that cannot see — and it stays at warn rather than paging
	// anyone, but it must not sit at debug where production never sees it.
	case err != nil && err.GetStatus() >= 500 && m.level >= llmLogError:
		m.log.Error(msg, append(fields, "error.code", err.GetCode(), "err", err)...)

	case err != nil && m.level >= llmLogWarn:
		m.log.Warn(msg, append(fields, "error.code", err.GetCode(), "err", err)...)

	case took >= m.slow && m.level >= llmLogWarn:
		m.log.Warn(msg, append(fields, "threshold_ms", m.slow.Milliseconds())...)

	case m.level >= llmLogAll:
		m.log.Debug(msg, fields...)
	}

	m.recordToolCalls(resp)
}

// recordToolCalls writes one line per call the loop made.
//
// It is here rather than left to each service because a tool loop can change
// data, and the record of what it decided to do has to exist whether or not
// anyone remembered to write an Approve that logs. A service with no approval
// policy is exactly the one whose audit trail matters most.
func (m *meteredLLM) recordToolCalls(resp LLMResponse) {
	if m.level < llmLogWarn || len(resp.ToolCalls) == 0 {
		return
	}
	for _, call := range resp.ToolCalls {
		fields := []any{
			"tool", call.Name,
			"outcome", string(call.Outcome),
			"provider", resp.Provider,
			"model", resp.Model,
		}
		// The arguments are the model's own words about what it wants done and
		// can carry user data, so they follow the same rule as the prompt.
		if m.logPrompt && len(call.Input) > 0 {
			fields = append(fields, "input", truncateForLog(string(call.Input)))
		}
		// The result is whatever the tool read — a row out of the database as
		// often as not — so it follows the reply's rule rather than the
		// prompt's. Without it the trail records that a lookup happened and not
		// what the model was told, which is the half that explains the answer.
		if m.logCompletion && call.Output != "" {
			fields = append(fields, "output", truncateForLog(call.Output))
		}

		switch call.Outcome {
		case LLMToolDenied, LLMToolFailed, LLMToolUnknown:
			// A denial is the security control working, and a failure is a tool
			// the model will probably try again — both are worth seeing without
			// turning the level up.
			m.log.Warn("llm tool "+string(call.Outcome)+" "+call.Name, fields...)
		default:
			if m.level >= llmLogAll {
				m.log.Debug("llm tool ran "+call.Name, fields...)
			}
		}
	}
}

func (r LLMRequest) promptChars() int {
	n := len(r.System)
	for _, m := range r.Messages {
		n += len(m.Text)
	}
	return n
}

// llmBootField is what the boot log says about the model. The other
// capabilities log a bool, but "true" is not the question anyone has here —
// which provider and which model is, because the cost of getting it wrong is a
// month of calls to a model nobody meant to use.
func llmBootField(l ILLM) any {
	if l == nil || !l.Enabled() {
		return false
	}
	if m := l.Model(); m != "" {
		return l.Provider() + "/" + m
	}
	return l.Provider()
}

// meterFrom finds the meter for ctx. A handle built at startup has no context
// yet, and a context from outside an App has no meter, so both fall back to a
// no-op rather than making every call site nil-check.
func meterFrom(ctx context.Context) IMeter {
	if ctx != nil {
		if app := appFrom(ctx); app != nil {
			return app.Sentry().Meter().WithContext(ctx)
		}
	}
	return NewNoopSentry().Meter()
}
