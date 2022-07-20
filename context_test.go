// +build e2e

package core

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"net/http"
	"testing"
)

func getInternalServerError() IError {
	return Error{
		Status:  http.StatusInternalServerError,
		Code:    "INTERNAL_SERVER_ERROR",
		Message: "Internal server error",
	}
}

func TestCoreContextNewError(t *testing.T) {
	ctx := NewContext(&ContextOptions{
		ENV: NewEnv(),
	})

	err := errors.New("RootError")
	ierr := ctx.NewError(err, getInternalServerError())
	assert.Equal(t, "RootError", ierr.OriginalError().Error())

	newIError := ctx.NewError(ierr, ierr)
	assert.Equal(t, "RootError", newIError.OriginalError().Error())
}
