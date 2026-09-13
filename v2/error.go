// Package core is the root of mine-core v2. It holds the request/job context
// (IContext), the application container (App), and the structured error type
// (IError/Error) that flows through every layer of the framework.
package core

import (
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
)

// IError is the structured error returned by every mine-core layer. It keeps the
// v1 method set (so callers do not have to relearn) and additionally supports the
// standard library's errors.Is / errors.As via Unwrap.
type IError interface {
	error
	// GetCode returns the machine-readable error code (stable, safe to expose).
	GetCode() string
	// GetStatus returns the HTTP status the error maps to.
	GetStatus() int
	// GetMessage returns the human-readable message (safe to expose).
	GetMessage() interface{}
	// JSON returns the value serialised in HTTP responses.
	JSON() interface{}
	// OriginalError returns the wrapped cause, or the error itself when there is none.
	OriginalError() error
	// Unwrap exposes the wrapped cause for errors.Is / errors.As.
	Unwrap() error
}

// Error is the concrete IError. Exported fields mirror v1 so existing response
// shapes are unchanged; cause and stack are captured for diagnostics.
type Error struct {
	Status  int         `json:"-"`
	Code    string      `json:"code"`
	Message interface{} `json:"message"`
	Data    interface{} `json:"-"`
	Fields  interface{} `json:"fields,omitempty"`

	cause error
	stack []uintptr
	// eventID is the Sentry event this error was reported as, set the moment it
	// is captured so no layer above reports the same failure twice.
	eventID string
}

// compile-time guarantee that *Error satisfies IError.
var _ IError = (*Error)(nil)

// New builds an *Error with the given status, code and message, capturing the
// call site for the stack trace.
func New(status int, code string, message string) *Error {
	return &Error{
		Status:  status,
		Code:    code,
		Message: message,
		stack:   captureStack(1),
	}
}

// Newf is New with a formatted message. It builds the error itself instead of
// delegating to New so the captured stack starts at the caller, not at Newf.
func Newf(status int, code string, format string, args ...interface{}) *Error {
	return &Error{
		Status:  status,
		Code:    code,
		Message: fmt.Sprintf(format, args...),
		stack:   captureStack(1),
	}
}

// Wrap turns any error into an *Error (500 by default) without ever panicking.
// If err already is (or wraps) an *Error, that error is returned enriched with
// the extra message so the original status/code/fields are preserved.
func Wrap(err error, message string) *Error {
	if err == nil {
		return nil
	}
	return wrap(err, message, captureStack(1))
}

// Wrapf is Wrap with a formatted message.
func Wrapf(err error, format string, args ...interface{}) *Error {
	if err == nil {
		return nil
	}
	return wrap(err, fmt.Sprintf(format, args...), captureStack(1))
}

// wrap is Wrap with the stack passed in, so every exported entry point can
// capture it at its own call site rather than inside this file.
func wrap(err error, message string, stack []uintptr) *Error {
	var existing *Error
	if errors.As(err, &existing) {
		clone := *existing
		if message != "" {
			clone.Message = message + ": " + fmt.Sprint(existing.Message)
		}
		clone.cause = err
		if clone.stack == nil {
			// only a sentinel gets here (captureStack drops init-time stacks);
			// an error raised at a real call site keeps that call site, which
			// says more than the point it happened to be wrapped at
			clone.stack = stack
		}
		return &clone
	}

	return &Error{
		Status:  http.StatusInternalServerError,
		Code:    "INTERNAL_SERVER_ERROR",
		Message: message,
		cause:   err,
		stack:   stack,
	}
}

// From converts any error to an *Error using errors.As (never panics). Errors
// that are not already *Error become a 500. Returns nil for a nil error.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return wrap(err, "", captureStack(1))
}

// maxCauseDepth bounds the walk down an error chain. A cause that (directly or
// indirectly) points back at its own wrapper would otherwise loop forever.
const maxCauseDepth = 100

// RootCause walks an error chain to its deepest error: the failure as the
// driver, syscall or library reported it, rather than any wrapper placed around
// it on the way up.
func RootCause(err error) error {
	for i := 0; err != nil && i < maxCauseDepth; i++ {
		next := errors.Unwrap(err)
		if next == nil {
			break
		}
		err = next
	}
	return err
}

// devMessage renders what actually went wrong, for dev responses only. It reads
// the root cause, and takes an *Error's own Message rather than the
// "code: … message: …" rendering of Error(), so wrappers do not stack up in the
// text a developer reads.
func devMessage(err error) string {
	root := RootCause(err)
	if root == nil {
		return ""
	}
	if e, ok := root.(*Error); ok {
		return fmt.Sprint(e.Message)
	}
	return root.Error()
}

// --- builder helpers (copy-on-write, safe to chain) ---

// WithStatus returns a copy with a different HTTP status.
func (e *Error) WithStatus(status int) *Error { c := e.clone(); c.Status = status; return c }

// WithCode returns a copy with a different code.
func (e *Error) WithCode(code string) *Error { c := e.clone(); c.Code = code; return c }

// WithMessage returns a copy with a different message.
func (e *Error) WithMessage(message interface{}) *Error {
	c := e.clone()
	c.Message = message
	return c
}

// WithFields returns a copy carrying validation/detail fields.
func (e *Error) WithFields(fields interface{}) *Error { c := e.clone(); c.Fields = fields; return c }

// WithData returns a copy carrying non-serialised data.
func (e *Error) WithData(data interface{}) *Error { c := e.clone(); c.Data = data; return c }

// WithCause returns a copy wrapping cause (for errors.Is / errors.As chains).
func (e *Error) WithCause(cause error) *Error { c := e.clone(); c.cause = cause; return c }

func (e *Error) clone() *Error {
	c := *e
	return &c
}

// --- IError implementation ---

func (e *Error) Error() string {
	return fmt.Sprintf("code: %s message: %v", e.Code, e.Message)
}

func (e *Error) GetCode() string { return e.Code }

func (e *Error) GetStatus() int { return e.Status }

func (e *Error) GetMessage() interface{} { return e.Message }

func (e *Error) JSON() interface{} { return e }

// OriginalError returns the wrapped cause, or the error itself when there is none
// (matching v1 semantics).
func (e *Error) OriginalError() error {
	if e.cause == nil {
		return e
	}
	return e.cause
}

func (e *Error) Unwrap() error { return e.cause }

// Is reports whether target is an *Error with the same code, so sentinels can be
// matched with errors.Is regardless of wrapping.
func (e *Error) Is(target error) bool {
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	return e.Code == t.Code
}

// EventID is the Sentry event this error was reported as, or "" when it was
// never reported (no DSN, or a status below the reporting threshold). Handing
// it to a user turns "it broke" into a one-click lookup.
func (e *Error) EventID() string { return e.eventID }

// StackTrace returns the program counters captured when the error was created.
// sentry-go looks for this method by reflection, so a reported error carries
// the stack of where it actually happened — not of where it was captured.
func (e *Error) StackTrace() []uintptr { return e.stack }

// StackString renders the captured call stack as "func\n\tfile:line" frames.
func (e *Error) StackString() string {
	if len(e.stack) == 0 {
		return ""
	}
	var b strings.Builder
	frames := runtime.CallersFrames(e.stack)
	for {
		frame, more := frames.Next()
		fmt.Fprintf(&b, "%s\n\t%s:%d\n", frame.Function, frame.File, frame.Line)
		if !more {
			break
		}
	}
	return b.String()
}

// captureStack records the call stack starting skip frames above the function
// that calls it — 0 is that function itself, 1 its caller, and so on.
//
// A stack captured while a package was still initialising is discarded. A
// package-level sentinel (var DBError = New(...)) would otherwise record
// "errmsgs.init → runtime.main" and hand that same trace to every error cloned
// from it, pointing at the declaration instead of at the failure. No stack is
// strictly better than that one: newError, Wrap and Sentry itself all capture a
// real stack when the error carries none.
func captureStack(skip int) []uintptr {
	pc := make([]uintptr, 32)
	// +2: runtime.Callers and captureStack itself are never worth showing
	n := runtime.Callers(skip+2, pc)
	if n == 0 {
		return nil
	}
	if frame, _ := runtime.CallersFrames(pc[:n]).Next(); isInitFrame(frame.Function) {
		return nil
	}
	return pc[:n]
}

// isInitFrame reports whether a frame belongs to package initialisation: the
// generated "pkg.init" that runs var initialisers, a hand-written "pkg.init.0",
// or a closure inside either. The package path is stripped first so that a
// method actually named init ("pkg.(*T).init") is not mistaken for one.
func isInitFrame(function string) bool {
	if i := strings.LastIndexByte(function, '/'); i >= 0 {
		function = function[i+1:]
	}
	i := strings.IndexByte(function, '.')
	if i < 0 {
		return false
	}
	name := function[i+1:]
	return name == "init" || strings.HasPrefix(name, "init.")
}
