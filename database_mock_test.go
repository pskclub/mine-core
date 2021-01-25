package core

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestNewMockDatabase(t *testing.T) {
	mock := NewMockDatabase()
	assert.NotNil(t, mock.Gorm)
	assert.NotNil(t, mock.Mock)
}
