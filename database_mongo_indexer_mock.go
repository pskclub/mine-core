package core

import "github.com/stretchr/testify/mock"

type MockMongoIndexBatch struct {
	mock.Mock
}

type MockMongoIndexer struct {
	mock.Mock
	Batches []*MockMongoIndexBatch
}

func NewMockMongoIndexBatch() *MockMongoIndexBatch {
	return &MockMongoIndexBatch{}
}

func (m *MockMongoIndexBatch) Name() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockMongoIndexBatch) Run() error {
	args := m.Called()
	return args.Error(0)
}

func NewMockMongoIndexer() *MockMongoIndexer {
	return &MockMongoIndexer{}
}

func (m *MockMongoIndexer) Add(batch *MockMongoIndexBatch) {
	if m.Batches == nil {
		m.Batches = []*MockMongoIndexBatch{batch}
	} else {
		m.Batches = append(m.Batches, batch)
	}

	_ = m.Called(batch)
}

func (m *MockMongoIndexer) Execute() error {
	if m.Batches != nil {
		for _, b := range m.Batches {
			_ = b.Name()
			_ = b.Run()
		}
	}

	args := m.Called()
	return args.Error(0)
}
