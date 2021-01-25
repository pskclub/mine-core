package core

import (
	"github.com/stretchr/testify/mock"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type MockMongoDB struct {
	mock.Mock
}

func NewMockMongoDB() *MockMongoDB {
	return &MockMongoDB{}
}

func (m *MockMongoDB) Close() {
	m.Called()
}

func (m *MockMongoDB) FindPagination(dest interface{}, coll string, filter interface{}, pageOptions *PageOptions, opts ...*options.FindOptions) (*PageResponse, error) {
	args := m.Called(dest, coll, filter, pageOptions, opts)
	return args.Get(0).(*PageResponse), args.Error(1)
}

func (m *MockMongoDB) Count(coll string, filter interface{}, opts ...*options.CountOptions) (int64, error) {
	args := m.Called(coll, filter, opts)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockMongoDB) FindAggregate(dest interface{}, coll string, pipeline interface{}, opts ...*options.AggregateOptions) error {
	args := m.Called(dest, coll, pipeline, opts)
	return args.Error(0)
}

func (m *MockMongoDB) FindAggregatePagination(dest interface{}, coll string, pipeline interface{}, pageOptions *PageOptions, opts ...*options.AggregateOptions) (*PageResponse, error) {
	args := m.Called(dest, coll, pipeline, pageOptions, opts)
	return args.Get(0).(*PageResponse), args.Error(1)
}

func (m *MockMongoDB) FindAggregateOne(dest interface{}, coll string, pipeline interface{}, opts ...*options.AggregateOptions) error {
	args := m.Called(dest, coll, pipeline, opts)
	return args.Error(0)
}

func (m *MockMongoDB) Find(dest interface{}, coll string, filter interface{}, opts ...*options.FindOptions) error {
	args := m.Called(dest, coll, filter, opts)
	return args.Error(0)
}

func (m *MockMongoDB) UpdateOne(coll string, filter interface{}, update interface{},
	opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	args := m.Called(coll, filter, update, opts)
	return args.Get(0).(*mongo.UpdateResult), args.Error(1)
}

func (m *MockMongoDB) FindOneAndUpdate(dest interface{}, coll string, filter interface{}, update interface{},
	opts ...*options.FindOneAndUpdateOptions) error {
	args := m.Called(dest, coll, filter, update, opts)
	return args.Error(0)
}

func (m *MockMongoDB) Drop(coll string) error {
	args := m.Called(coll)
	return args.Error(0)
}

func (m *MockMongoDB) DeleteOne(coll string, filter interface{}, opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {
	args := m.Called(coll, filter, opts)
	return args.Get(0).(*mongo.DeleteResult), args.Error(1)
}

func (m *MockMongoDB) FindOneAndDelete(coll string, filter interface{}, opts ...*options.FindOneAndDeleteOptions) error {
	args := m.Called(coll, filter, opts)
	return args.Error(0)
}

func (m *MockMongoDB) DeleteMany(coll string, filter interface{}, opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {
	args := m.Called(coll, filter, opts)
	return args.Get(0).(*mongo.DeleteResult), args.Error(1)
}

func (m *MockMongoDB) FindOne(dest interface{}, coll string, filter interface{}, opts ...*options.FindOneOptions) error {
	args := m.Called(dest, coll, filter, opts)
	return args.Error(0)
}

func (m *MockMongoDB) Create(coll string, document interface{}, opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	args := m.Called(coll, document, opts)
	return args.Get(0).(*mongo.InsertOneResult), args.Error(1)
}

func (m *MockMongoDB) DB() *mongo.Database {
	args := m.Called()
	return args.Get(0).(*mongo.Database)
}

func (m *MockMongoDB) Helper() IMongoDBHelper {
	return NewMongoHelper()
}
