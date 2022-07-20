package core

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestMongoIndexer_Add(t *testing.T) {
	mg := NewMockMongoIndexer()
	assert.NotNil(t, mg)

	batch := NewMockMongoIndexBatch()
	assert.NotNil(t, batch)

	batch.On("Run").Return(nil)
	batch.On("Name").Return("test_create_index")

	mg.On("Add", batch).Return()
	mg.On("Execute").Return(nil)
	mg.Add(batch)

	mg.AssertNumberOfCalls(t, "Add", 1)
}

func TestMongoIndexer_Execute(t *testing.T) {
	mg := NewMockMongoIndexer()
	assert.NotNil(t, mg)

	batch := NewMockMongoIndexBatch()
	assert.NotNil(t, batch)

	batch.On("Run").Return(nil)
	batch.On("Name").Return("test_create_index")

	mg.On("Add", batch).Return()
	mg.On("Execute").Return(nil)
	mg.Add(batch)
	err := mg.Execute()
	assert.NoError(t, err)

	mg.AssertNumberOfCalls(t, "Add", 1)
	mg.AssertNumberOfCalls(t, "Execute", 1)
}