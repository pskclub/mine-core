package core

import "log"

type ELSMigration struct {
	ctx IContext
}

type IELSMigration interface {
	Up() error
}

func NewELSMigration(options *ContextOptions) *ELSMigration {
	return &ELSMigration{ctx: NewContext(options)}
}

func (e ELSMigration) Add(f func(ctx IContext) IELSMigration) {
	err := f(e.ctx).Up()
	if err != nil {
		log.Fatal(err)
	}
}
