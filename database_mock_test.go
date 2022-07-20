package core

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestNewMockGorm(t *testing.T) {
	m := NewMockDatabase()
	assert.NotNil(t, m.Gorm)
	assert.NotNil(t, m.Mock)
}

func TestNewMockDatabase(t *testing.T) {
	m := NewMockDatabase()
	assert.NotNil(t, m)
}
