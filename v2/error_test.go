package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	e := New(http.StatusBadRequest, "BAD_REQUEST", "bad request")
	assert.Equal(t, http.StatusBadRequest, e.GetStatus())
	assert.Equal(t, "BAD_REQUEST", e.GetCode())
	assert.Equal(t, "bad request", e.GetMessage())
	assert.NotEmpty(t, e.StackString(), "expected a captured stack trace")
}

func TestNewf(t *testing.T) {
	e := Newf(http.StatusConflict, "CONFLICT", "user %d exists", 7)
	assert.Equal(t, "user 7 exists", e.GetMessage())
}

func TestWrap_nilReturnsNil(t *testing.T) {
	assert.Nil(t, Wrap(nil, "x"))
	assert.Nil(t, From(nil))
}

func TestWrap_plainError(t *testing.T) {
	cause := errors.New("boom")
	e := Wrap(cause, "loading user")

	assert.Equal(t, http.StatusInternalServerError, e.GetStatus())
	assert.ErrorIs(t, e, cause, "wrapped error should match its cause")
	assert.Equal(t, cause, e.OriginalError())
}

func TestWrap_preservesExistingError(t *testing.T) {
	base := New(http.StatusNotFound, "NOT_FOUND", "not found")
	wrapped := Wrap(base, "loading user")

	assert.Equal(t, http.StatusNotFound, wrapped.GetStatus(), "status preserved")
	assert.Equal(t, "NOT_FOUND", wrapped.GetCode(), "code preserved")
	assert.Contains(t, fmt.Sprint(wrapped.GetMessage()), "loading user")
}

func TestWrap_deepChainMatchesSentinel(t *testing.T) {
	sentinel := New(http.StatusNotFound, "NOT_FOUND", "not found")
	err := Wrapf(Wrap(sentinel, "layer 1"), "layer 2")

	assert.ErrorIs(t, err, sentinel, "should match sentinel through multiple wraps")
	var target *Error
	assert.ErrorAs(t, err, &target)
}

func TestIs_matchesByCode(t *testing.T) {
	a := New(http.StatusNotFound, "NOT_FOUND", "a")
	b := New(http.StatusNotFound, "NOT_FOUND", "b different message")
	assert.ErrorIs(t, a, b, "same code should match")

	c := New(http.StatusBadRequest, "BAD_REQUEST", "c")
	assert.NotErrorIs(t, a, c, "different codes must not match")
}

func TestBuilders_doNotMutateOriginal(t *testing.T) {
	base := New(http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "oops")
	derived := base.WithStatus(400).WithCode("BAD_REQUEST").WithFields(map[string]string{"x": "y"})

	assert.Equal(t, http.StatusInternalServerError, base.GetStatus(), "original untouched")
	assert.Equal(t, "INTERNAL_SERVER_ERROR", base.GetCode(), "original untouched")
	assert.Equal(t, 400, derived.GetStatus())
	assert.Equal(t, "BAD_REQUEST", derived.GetCode())
}

func TestJSON_shape(t *testing.T) {
	e := New(http.StatusBadRequest, "INVALID_PARAMS", "Invalid parameters").
		WithFields(map[string]any{"email": map[string]string{"code": "REQUIRED"}})

	b, err := json.Marshal(e.JSON())
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, "INVALID_PARAMS", got["code"])
	assert.Equal(t, "Invalid parameters", got["message"])
	assert.Contains(t, got, "fields")
	assert.NotContains(t, got, "Status", "Status must not be serialised")
}

func TestJSON_omitsEmptyFields(t *testing.T) {
	e := New(http.StatusNotFound, "NOT_FOUND", "not found")
	b, _ := json.Marshal(e.JSON())
	assert.NotContains(t, string(b), "fields", "empty fields should be omitted")
}

// testSentinel stands in for errmsgs.DBError and friends: a *Error built while
// the package is initialising, reused from many unrelated call sites.
var testSentinel = New(http.StatusInternalServerError, "TEST_SENTINEL", "sentinel")

// topFrame is the innermost frame of an error's captured stack — the one Sentry
// shows as the culprit.
func topFrame(t *testing.T, e *Error) runtime.Frame {
	t.Helper()
	require.NotEmpty(t, e.StackTrace(), "expected a captured stack")
	frame, _ := runtime.CallersFrames(e.StackTrace()).Next()
	return frame
}

func TestNew_packageLevelSentinelKeepsNoStack(t *testing.T) {
	assert.Empty(t, testSentinel.StackTrace(),
		"an init-time stack points at the declaration, so it must not be kept and handed to every error cloned from the sentinel")
}

func TestNewf_stackStartsAtCaller(t *testing.T) {
	e := Newf(http.StatusConflict, "CONFLICT", "user %d exists", 7)
	assert.Contains(t, topFrame(t, e).Function, "TestNewf_stackStartsAtCaller",
		"Newf must not leave its own frame on top of the trace")
}

func TestWrap_sentinelTakesTheWrapSite(t *testing.T) {
	e := Wrap(testSentinel, "loading user")
	assert.Contains(t, topFrame(t, e).Function, "TestWrap_sentinelTakesTheWrapSite",
		"a sentinel carries no stack, so the wrap site is the first real location this failure has")
}

// newUpstreamError raises an error somewhere other than the test body, so the
// assertion below can tell the origin apart from the wrap site.
func newUpstreamError() *Error {
	return New(http.StatusBadGateway, "UPSTREAM", "upstream said no")
}

func TestWrap_keepsTheOriginStack(t *testing.T) {
	origin := newUpstreamError()
	e := Wrap(origin, "loading user")
	assert.Contains(t, topFrame(t, e).Function, "newUpstreamError",
		"where the error was raised beats where it was wrapped, so the origin stack is kept")
	assert.Equal(t, origin.StackTrace(), e.StackTrace())
}

func TestIsInitFrame(t *testing.T) {
	cases := map[string]bool{
		"github.com/pskclub/mine-core/v2/errmsgs.init":       true, // var initialisers
		"github.com/pskclub/mine-core/v2/errmsgs.init.0":     true, // hand-written func init()
		"github.com/pskclub/mine-core/v2/errmsgs.init.func1": true, // closure inside one
		"main.init": true,
		"github.com/pskclub/mine-core/v2.(*coreContext).init": false, // a method named init is a real call site
		"github.com/pskclub/mine-core/v2/errmsgs.initialise":  false,
		"github.com/pskclub/mine-core/v2.New":                 false,
		"runtime.main":                                        false,
	}
	for function, want := range cases {
		assert.Equal(t, want, isInitFrame(function), function)
	}
}
