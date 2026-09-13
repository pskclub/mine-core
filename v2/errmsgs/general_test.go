package errmsgs_test

import (
	"net/http"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
)

func TestSentinels_haveExpectedStatus(t *testing.T) {
	cases := []struct {
		err    core.IError
		status int
	}{
		{errmsgs.DBError, http.StatusInternalServerError},
		{errmsgs.NotFound, http.StatusNotFound},
		{errmsgs.BadRequest, http.StatusBadRequest},
		{errmsgs.Unauthorized, http.StatusUnauthorized},
	}
	for _, c := range cases {
		assert.Equal(t, c.status, c.err.GetStatus(), c.err.GetCode())
	}
}

func TestWrapPreservesSentinelIdentity(t *testing.T) {
	err := core.Wrap(errmsgs.NotFound, "loading user 42")

	assert.ErrorIs(t, err, errmsgs.NotFound, "wrapped error should still match the sentinel")
	assert.Equal(t, http.StatusNotFound, err.GetStatus())
}

func TestSentinels_carryNoStackOfTheirOwn(t *testing.T) {
	// these are built during package init, so any stack they held would be
	// "errmsgs.init → runtime.main" — and every error in every service that
	// reuses the sentinel would report that same trace instead of its own
	for _, e := range []*core.Error{
		errmsgs.DBError, errmsgs.MQError, errmsgs.CacheError, errmsgs.CronjobError,
		errmsgs.InternalServerError, errmsgs.NotFound, errmsgs.BadRequest,
		errmsgs.Unauthorized, errmsgs.Forbidden, errmsgs.SignatureInvalid,
		errmsgs.JSONInvalid, errmsgs.JWTInvalid,
	} {
		assert.Empty(t, e.StackTrace(), e.GetCode())
	}
}

func TestWrapSentinel_stackStartsAtTheWrapSite(t *testing.T) {
	err := core.Wrap(errmsgs.DBError, "loading user 42")

	require.NotEmpty(t, err.StackTrace(), "the wrap site is where this failure was actually reached")
	frame, _ := runtime.CallersFrames(err.StackTrace()).Next()
	assert.Contains(t, frame.Function, "TestWrapSentinel_stackStartsAtTheWrapSite")
}

func TestIsNotFoundError(t *testing.T) {
	assert.True(t, errmsgs.IsNotFoundError(errmsgs.NotFound))
	assert.False(t, errmsgs.IsNotFoundError(errmsgs.BadRequest))
	assert.False(t, errmsgs.IsNotFoundError(nil))
}

func TestNotFoundCustomError(t *testing.T) {
	err := errmsgs.NotFoundCustomError("user")
	require.NotNil(t, err)
	assert.Equal(t, "USER_NOT_FOUND", err.GetCode())
	assert.Equal(t, http.StatusNotFound, err.GetStatus())
}
