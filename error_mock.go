package core

import (
	"fmt"
	"github.com/stretchr/testify/mock"
)

func MockIError(args mock.Arguments, index int) IError {
	obj := args.Get(index)
	var s IError
	var ok bool
	if obj == nil {
		return nil
	}
	if s, ok = obj.(IError); !ok {
		panic(fmt.Sprintf("assert: arguments: Error(%d) failed because object wasn't correct type: %v", index, args.Get(index)))
	}
	return s
}
