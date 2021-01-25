package core

import (
	"github.com/stretchr/testify/mock"
	"time"
)

type MockCache struct {
	mock.Mock
}

func NewMockCache() *MockCache {
	return &MockCache{}
}

func (m *MockCache) Close() {
	m.Called()
}

func (m *MockCache) Set(key string, value interface{}, expiration time.Duration) error {
	args := m.Called(key, value, expiration)
	return args.Error(0)
}

func (m *MockCache) Get(dest interface{}, key string) error {
	args := m.Called(dest, key)
	return args.Error(0)
}

func (m *MockCache) Del(key string) error {
	args := m.Called(key)
	return args.Error(0)
}

func (m *MockCache) SetJSON(key string, value interface{}, expiration time.Duration) error {
	args := m.Called(key, value, expiration)
	return args.Error(0)
}

func (m *MockCache) GetJSON(dest interface{}, key string) error {
	args := m.Called(dest, key)
	return args.Error(0)
}
