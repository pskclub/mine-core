package core

import (
	"github.com/streadway/amqp"
	"github.com/stretchr/testify/mock"
)

type MockMQ struct {
	mock.Mock
}

func NewMockMQ() *MockMQ {
	return &MockMQ{}
}

func (m *MockMQ) PublishJSON(name string, data interface{}, options *MQPublishOptions) error {
	args := m.Called(name, data, options)
	return args.Error(0)
}

func (m *MockMQ) Consume(name string, onConsume func(message amqp.Delivery), options *MQConsumeOptions) error {
	args := m.Called(name, onConsume, options)
	return args.Error(0)
}

func (m *MockMQ) Close() {
	m.Called()
}

func (m *MockMQ) Conn() *amqp.Connection {
	args := m.Called()
	return args.Get(0).(*amqp.Connection)
}
