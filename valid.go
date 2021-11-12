package core

import (
	"errors"
	"net/http"
	"strings"
)

type IValidMessage struct {
	Name    string      `json:"-"`
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (f IValidMessage) Error() string {
	return f.Message
}

// Error is the validate error
type ValidError struct {
	errors []error
}

// Error implements error interface
func (err *ValidError) Error() string {
	return strings.Join(err.Strings(), ", ")
}

// Errors returns errors
func (err *ValidError) Errors() []error {
	return err.errors
}

// Strings returns errors in strings
func (err *ValidError) Strings() []string {
	s := make([]string, len(err.errors))
	for i := range err.errors {
		s[i] = err.errors[i].Error()
	}
	return s
}

func (err *ValidError) clone() *ValidError {
	return &ValidError{errors: err.errors}
}

// IsError returns true if given error is validate error
func IsError(err error) bool {
	_, ok := err.(*Error)
	return ok
}

// New creates new validator
func NewValid() *Valid {
	return &Valid{}
}

// Validator type
type Valid struct {
	err ValidError
}

// Error returns error if has error
func (v *Valid) Error() string {
	return strings.Join(v.err.Strings(), ", ")
}

func (v *Valid) OriginalError() error {
	return errors.New(v.Error())
}

func (v *Valid) GetCode() string {
	return "BAD_REQUEST"
}

func (v *Valid) GetStatus() int {
	return http.StatusBadRequest
}

func (v *Valid) JSON() interface{} {
	fields := make(map[string]jsonErr)
	for _, err := range v.err.errors {
		fields[err.(*IValidMessage).Name] = jsonErr{
			Code:    err.(*IValidMessage).Code,
			Message: err.(*IValidMessage).Message,
			Data:    err.(*IValidMessage).Data,
		}
	}
	return NewValidatorFields(fields)
}

func (v *Valid) GetMessage() interface{} {
	return v.err.Error()
}

// Valid returns true if no error
func (v *Valid) Valid() IError {
	if len(v.err.errors) == 0 {
		return nil
	}

	return v
}

// Must checks x must not an error or true if bool
// and return true if valid
//
// msg must be error or string
func (v *Valid) Must(x bool, msg *IValidMessage) bool {
	if x == true || msg == nil {
		return true
	}

	var m error
	for _, err := range v.err.errors {
		if err.(*IValidMessage).Name == msg.Name {
			return true
		}
	}

	m = msg

	v.err.errors = append(v.err.errors, m)
	return false
}

// Add adds errors
func (v *Valid) Add(err ...error) {
	v.err.errors = append(v.err.errors, err...)
}

func (v *Valid) OriginalErrorMessage() string {
	return v.err.Error()
}
