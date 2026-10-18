package valid_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/valid"
)

type wrapPayload struct {
	Name *string
}

func (p *wrapPayload) Valid(ctx core.IContext) core.IError {
	v := valid.New(ctx)
	v.Str("name", p.Name).Required()
	return v.Error()
}

// A validation error that passes through core.Wrap, or through ctx.NewError as
// the error type, is still the client's mistake: it must keep its 400 and its
// fields rather than become the 500 every non-*Error used to turn into.
func TestWrap_keepsAForeignIErrorsStatusAndFields(t *testing.T) {
	ctx := newCtx(t)
	verr := (&wrapPayload{}).Valid(ctx)
	require.NotNil(t, verr)
	require.Equal(t, http.StatusBadRequest, verr.GetStatus(), "precondition: a bare validation error is a 400")

	wrapped := core.Wrap(verr, "create user")
	assert.Equal(t, http.StatusBadRequest, wrapped.GetStatus(),
		"wrapping names where it happened; it does not change whose fault it was")
	assert.Equal(t, "INVALID_PARAMS", wrapped.GetCode())
	assert.NotNil(t, wrapped.Fields, "the per-field violations are what the client acts on")
	assert.True(t, errors.Is(wrapped, verr), "the original stays reachable for errors.Is")

	viaFrom := core.From(verr)
	assert.Equal(t, http.StatusBadRequest, viaFrom.GetStatus())

	reported := ctx.NewError(nil, verr)
	assert.Equal(t, http.StatusBadRequest, reported.GetStatus(),
		"NewError builds on From, so the same rule applies to the reported error")
}
