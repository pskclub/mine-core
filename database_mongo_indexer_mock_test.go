package core

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestNewMockMongoIndexer(t *testing.T) {
	mg := NewMockMongoIndexer()
	assert.NotNil(t, mg)
}