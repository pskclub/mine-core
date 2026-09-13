package core

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecover_fromErrorPanic(t *testing.T) {
	sentinel := errors.New("kaboom")

	err := func() (err error) {
		defer Recover(&err)
		panic(sentinel)
	}()

	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "recovered error should wrap the panicked error")
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.NotEmpty(t, e.StackString())
}

func TestRecover_fromStringPanic(t *testing.T) {
	err := func() (err error) {
		defer Recover(&err)
		panic("plain string boom")
	}()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "plain string boom")
}

func TestRecover_noPanicLeavesNil(t *testing.T) {
	err := func() (err error) {
		defer Recover(&err)
		return nil
	}()

	assert.NoError(t, err)
}
