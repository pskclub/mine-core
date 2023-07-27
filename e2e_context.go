package core

import "github.com/pskclub/mine-core/consts"

type IE2EContext interface {
	IContext
}

type E2EContext struct {
	IContext
}

type E2EContextOptions struct {
	ContextOptions *ContextOptions
}

func NewE2EContext(options *E2EContextOptions) IE2EContext {
	ctxOptions := options.ContextOptions
	ctxOptions.contextType = consts.E2E
	return &E2EContext{IContext: NewContext(ctxOptions)}
}
