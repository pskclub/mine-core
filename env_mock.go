package core

import "github.com/stretchr/testify/mock"

type mockENV struct {
	mock.Mock
}

func NewMockENV() *mockENV {
	return &mockENV{}
}

func (m *mockENV) Config() *ENVConfig {
	args := m.Called()
	return args.Get(0).(*ENVConfig)
}

func (m *mockENV) IsDev() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *mockENV) IsTest() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *mockENV) IsMock() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *mockENV) IsProd() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *mockENV) Bool(key string) bool {
	args := m.Called(key)
	return args.Bool(0)
}

func (m *mockENV) Int(key string) int {
	args := m.Called(key)
	return args.Int(0)
}

func (m *mockENV) String(key string) string {
	args := m.Called(key)
	return args.String(0)
}

func (m *mockENV) All() map[string]string {
	args := m.Called()
	return args.Get(0).(map[string]string)
}
