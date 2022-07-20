package core

import (
	"github.com/stretchr/testify/mock"
)

type MockRequester struct {
	mock.Mock
}

func NewMockRequester() *MockRequester {
	return &MockRequester{}
}

func (m *MockRequester) Get(url string, options *RequesterOptions) (*RequestResponse, error) {
	args := m.Called(url, options)

	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	return args.Get(0).(*RequestResponse), args.Error(1)
}

func (m *MockRequester) Delete(url string, options *RequesterOptions) (*RequestResponse, error) {
	args := m.Called(url, options)

	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	return args.Get(0).(*RequestResponse), args.Error(1)
}

func (m *MockRequester) Post(url string, body interface{}, options *RequesterOptions) (*RequestResponse, error) {
	args := m.Called(url, body, options)

	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	return args.Get(0).(*RequestResponse), args.Error(1)
}

func (m *MockRequester) Put(url string, body interface{}, options *RequesterOptions) (*RequestResponse, error) {
	args := m.Called(url, body, options)

	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	return args.Get(0).(*RequestResponse), args.Error(1)
}

func (m *MockRequester) Patch(url string, body interface{}, options *RequesterOptions) (*RequestResponse, error) {
	args := m.Called(url, body, options)

	if args.Get(0) == nil {
		return nil, args.Error(1)
	}

	return args.Get(0).(*RequestResponse), args.Error(1)
}
