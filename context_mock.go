package core

import (
	"github.com/stretchr/testify/mock"
	"github.com/pskclub/mine-core/consts"
	"gorm.io/gorm"
)

func NewMockContext() *ContextMock {
	return &ContextMock{
		MockRequester: NewMockRequester(),
		MockCache:     NewMockCache(),
		MockMQ:        NewMockMQ(),
		MockLog:       NewMockLogger(),
		MockDBMongo:   NewMockMongoDB(),
		MockDB:        NewMockDatabase(),
	}
}

type ContextMock struct {
	mock.Mock
	MockRequester *MockRequester
	MockCache     *MockCache
	MockMQ        *MockMQ
	MockLog       *MockLogger
	MockDBMongo   *MockMongoDB
	MockDB        *MockDatabase
}

func (m *ContextMock) Cache() ICache {
	args := m.Called()
	return args.Get(0).(ICache)
}

func (m *ContextMock) MQ() IMQ {
	args := m.Called()
	return args.Get(0).(IMQ)
}

func (m *ContextMock) Caches(name string) ICache {
	args := m.Called(name)
	return args.Get(0).(ICache)
}

func (m *ContextMock) Requester() IRequester {
	args := m.Called()
	return args.Get(0).(IRequester)
}

func (m *ContextMock) SetType(t consts.ContextType) {
	m.Called(t)
}

// Log return the logger
func (m *ContextMock) Log() ILogger {
	args := m.Called()
	return args.Get(0).(ILogger)
}

func (m *ContextMock) Type() string {
	args := m.Called()
	return args.Get(0).(string)
}

func (m *ContextMock) DB() *gorm.DB {
	args := m.Called()
	return args.Get(0).(*gorm.DB)
}

func (m *ContextMock) DBS(name string) *gorm.DB {
	args := m.Called(name)
	return args.Get(0).(*gorm.DB)
}

func (m *ContextMock) DBMongo() IMongoDB {
	args := m.Called()
	return args.Get(0).(IMongoDB)
}

func (m *ContextMock) DBSMongo(name string) IMongoDB {
	args := m.Called(name)
	return args.Get(0).(IMongoDB)
}

func (m *ContextMock) NewError(err error, errorType IError, args ...interface{}) IError {
	args2 := m.Called(err, errorType, args)
	return MockIError(args2, 0)
}

func (m *ContextMock) ENV() IENV {
	args := m.Called()
	return args.Get(0).(IENV)
}
