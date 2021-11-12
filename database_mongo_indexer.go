package core

import (
	"fmt"
)

type IMongoIndexBatch interface {
	Name() string
	Run() error
}

type IMongoIndexer interface {
	Add(batch IMongoIndexBatch)
	Execute() error
}

type MongoIndexer struct {
	ctx     IContext
	Batches []IMongoIndexBatch
}

func NewMongoIndexer(ctx IContext) IMongoIndexer {
	return &MongoIndexer{
		ctx: ctx,
	}
}

func (i *MongoIndexer) Add(batch IMongoIndexBatch) {
	if i.Batches == nil {
		i.Batches = []IMongoIndexBatch{batch}
	} else {
		i.Batches = append(i.Batches, batch)
	}
}

func (i *MongoIndexer) Execute() error {
	if i.Batches == nil {
		return nil
	}

	for _, b := range i.Batches {
		i.ctx.Log().Debug(fmt.Sprintf(`Mongo Indexing: %s`, b.Name()))
		err := b.Run()
		if err != nil {
			return err
		}
	}

	return nil
}
