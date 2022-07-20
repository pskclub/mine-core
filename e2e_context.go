package core

import (
	"github.com/pskclub/mine-core/consts"
)

type IE2EContext interface {
	IContext
}

type E2EContext struct {
	IContext
	logger ILogger
}

type E2EContextOptions struct {
	ContextOptions *ContextOptions
}

func NewE2EContext(options *E2EContextOptions) IE2EContext {
	ctxOptions := options.ContextOptions
	ctxOptions.contextType = consts.E2E
	return &E2EContext{IContext: NewContext(ctxOptions)}
}

func (c *E2EContext) Log() ILogger {
	if c.logger == nil {
		c.logger = NewE2ELogger(c)
	}
	return c.logger.(ILogger)
}
