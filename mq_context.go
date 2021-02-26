package core

import (
	"fmt"
	"github.com/go-errors/errors"
	"github.com/pskclub/mine-core/consts"
)

type IMQContext interface {
	IContext
	AddConsumer(handlerFunc func(ctx IMQContext))
}

type MQContext struct {
	IContext
	logger ILogger
}

func (c *MQContext) AddConsumer(handlerFunc func(ctx IMQContext)) {
	defer func() {
		if r := recover(); r != nil {
		}
	}()
	go handlerFunc(c)
}

type MQContextOptions struct {
	ContextOptions *ContextOptions
}

func NewMQContext(options *MQContextOptions) IMQContext {
	ctxOptions := options.ContextOptions
	ctxOptions.contextType = consts.MQ
	return &MQContext{logger: nil, IContext: NewContext(ctxOptions)}
}

func (c *MQContext) NewError(err error, errorType IError, args ...interface{}) IError {
	if err != nil {
		errWrap := errors.Wrap(err, 1)
		if errorType.GetStatus() >= 500 {
			fmt.Println(errWrap.ErrorStack())
			c.Log().Error(errWrap, args...)
		}

	}
	return errorType
}
