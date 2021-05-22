package core

import (
	"fmt"
	"github.com/go-errors/errors"
)

type IError interface {
	Error() string
	GetStatus() int
	GetCode() string
	JSON() interface{}
	OriginalError() error
}

type Error struct {
	Status        int         `json:"-"`
	Code          string      `json:"code"`
	Message       interface{} `json:"message"`
	originalError error
}

func (c Error) JSON() interface{} {
	return c
}

func (c Error) Error() string {
	return fmt.Sprintf("code : %v message : %v", c.Code, c.Message)
}

func (c Error) GetStatus() int {
	return c.Status
}

func (c Error) GetCode() string {
	return c.Code
}

func (c Error) OriginalError() error {
	if c.originalError == nil {
		return c
	}

	return c.originalError
}

func Recover(textError string) {
	if r := recover(); r != nil {
		panic(textError)
	}
}

func Crash(err error) error {
	return errors.New(err)
}
