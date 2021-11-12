package core

import (
	"fmt"
	"github.com/go-errors/errors"
)

type IError interface {
	Error() string
	GetCode() string
	GetStatus() int
	JSON() interface{}
	OriginalError() error
	GetMessage() interface{}
}

type Error struct {
	Status        int         `json:"-"`
	Code          string      `json:"code"`
	Message       interface{} `json:"message"`
	Data          interface{} `json:"-"`
	Fields        interface{} `json:"fields,omitempty"`
	originalError error
}

func (c Error) JSON() interface{} {
	return c
}

func (c Error) Error() string {
	return fmt.Sprintf("code : %v message : %v", c.Code, c.Message)
}

func (c Error) GetCode() string {
	return c.Code
}

func (c Error) GetStatus() int {
	return c.Status
}

func (c Error) OriginalError() error {
	if c.originalError == nil {
		return c
	}

	return c.originalError
}

func (c Error) GetMessage() interface{} {
	return c.Message
}

func Recover(textError string) {
	if r := recover(); r != nil {
		panic(textError)
	}
}

func Crash(err error) error {
	return errors.New(err)
}
