package coretest

import (
	"encoding/json"
	"errors"
	"testing"

	core "github.com/pskclub/mine-core/v2"
)

// Assertions for core.IError.
//
// They exist because a plain require.Error says nothing useful about a framework
// error: what matters is the status a caller receives and the code they branch
// on, and reading those from the wrong layer is easy to get subtly wrong.

// RequireNoError fails the test when err is non-nil, printing status and code.
//
// It takes core.IError rather than error deliberately: a nil *Error stored in an
// error interface is non-nil, and this signature makes that mistake impossible.
func RequireNoError(t *testing.T, err core.IError) {
	t.Helper()

	if err != nil {
		t.Fatalf("unexpected error: status=%d code=%s message=%v",
			err.GetStatus(), err.GetCode(), err.GetMessage())
	}
}

// RequireStatus fails unless err carries the given HTTP status.
func RequireStatus(t *testing.T, err core.IError, want int) core.IError {
	t.Helper()

	if err == nil {
		t.Fatalf("expected an error with status %d, got nil", want)
	}
	if err.GetStatus() != want {
		t.Fatalf("status = %d (code %s), want %d", err.GetStatus(), err.GetCode(), want)
	}
	return err
}

// RequireCode fails unless err carries the given machine-readable code. Prefer
// this to matching on messages, which are prose and change.
func RequireCode(t *testing.T, err core.IError, want string) core.IError {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error %q, got nil", want)
	}
	if err.GetCode() != want {
		t.Fatalf("code = %q, want %q", err.GetCode(), want)
	}
	return err
}

// RequireIs fails unless err matches target under errors.Is — the way to compare
// against an errmsgs sentinel, since those match by code through any wrapping.
func RequireIs(t *testing.T, err error, target error) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want it to match %v", err, target)
	}
}

// Fields returns the per-field violations of a validation error, keyed by field
// path ("email", "users.0.email").
func Fields(t *testing.T, err core.IError) map[string]FieldError {
	t.Helper()

	if err == nil {
		t.Fatal("expected a validation error, got nil")
	}

	body := encodeDecode(t, err.JSON())
	if body.Fields == nil {
		t.Fatalf("error carries no fields: code=%s message=%v", err.GetCode(), err.GetMessage())
	}
	return body.Fields
}

// FieldCodes reduces a validation error to field -> code, for asserting on the
// whole set in one comparison.
func FieldCodes(t *testing.T, err core.IError) map[string]string {
	t.Helper()

	out := map[string]string{}
	for field, fe := range Fields(t, err) {
		out[field] = fe.Code
	}
	return out
}

// encodeDecode renders an error the way a client receives it, so assertions are
// made against the wire shape rather than internals.
func encodeDecode(t *testing.T, v any) ErrorBody {
	t.Helper()

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("coretest: encode error body: %v", err)
	}

	var body ErrorBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("coretest: decode error body: %v\nbody: %s", err, raw)
	}
	return body
}
