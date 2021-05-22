package core

import (
	"fmt"
	"github.com/getsentry/sentry-go"
	"github.com/go-errors/errors"
	"github.com/pskclub/mine-core/utils"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
)

type MQ struct {
	Host     string
	User     string
	Password string
	Port     string
}

type MQPublishOptions struct {
	MessageID    string
	Exchange     string
	Durable      bool
	AutoDelete   bool
	Exclusive    bool
	Mandatory    bool
	Immediate    bool
	NoWait       bool
	DeliveryMode uint8
	Args         amqp.Table
}

type IMQ interface {
	Close()
	PublishJSON(name string, data interface{}, options *MQPublishOptions) error
	Consume(name string, onConsume func(message amqp.Delivery), options *MQConsumeOptions) error
	Conn() *amqp.Connection
	ReConnect()
}

type mq struct {
	connection *amqp.Connection
	mq         *MQ
}

func (m mq) ReConnect() {
	if m.connection.IsClosed() {
		m.connection, _ = m.mq.ReConnect()
	}
}

func (m mq) PublishJSON(name string, data interface{}, options *MQPublishOptions) error {
	m.ReConnect()
	ch, err := m.Conn().Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	q, err := ch.QueueDeclare(
		name,               // name
		options.Durable,    // durable
		options.AutoDelete, // delete when unused
		options.Exclusive,  // exclusive
		options.NoWait,     // no-wait
		options.Args,       // arguments
	)
	if err != nil {
		return err
	}

	err = ch.Publish(
		options.Exchange,  // exchange
		q.Name,            // routing key
		options.Mandatory, // mandatory
		options.Immediate, // immediate
		amqp.Publishing{
			MessageId:    options.MessageID,
			DeliveryMode: options.DeliveryMode,
			ContentType:  "text/plain",
			Body:         []byte(utils.JSONToString(data)),
		})
	if err != nil {
		return err
	}

	if NewEnv().Config().LogLevel == logrus.DebugLevel {
		fmt.Println(fmt.Sprintf("Publish a message at '%s' channel", name))

	}

	return nil
}

type MQConsumeOptions struct {
	Durable    bool
	AutoDelete bool
	Exclusive  bool
	NoWait     bool
	Args       amqp.Table
	AutoAck    bool
	NoLocal    bool
	Consumer   string
}

func (m mq) Consume(name string, onConsume func(message amqp.Delivery), options *MQConsumeOptions) error {
	m.ReConnect()
	ch, err := m.Conn().Channel()
	if err != nil {
		return err
	}

	defer ch.Close()

	q, err := ch.QueueDeclare(
		name,               // name
		options.Durable,    // durable
		options.AutoDelete, // delete when unused
		options.Exclusive,  // exclusive
		options.NoWait,     // no-wait
		options.Args,       // arguments
	)
	if err != nil {
		return err
	}

	msgs, err := ch.Consume(
		q.Name,            // queue
		options.Consumer,  // consumer
		options.AutoAck,   // auto-ack
		options.Exclusive, // exclusive
		options.NoLocal,   // no-local
		options.NoWait,    // no-wait
		options.Args,      // args
	)
	if err != nil {
		return err
	}

	forever := make(chan bool)
	go func() {
		defer func() {
			if err := recover(); err != nil {
				errmsg := errors.New(fmt.Sprintf("%v", err))
				fmt.Println(errmsg)
				CaptureSimpleError(sentry.LevelFatal, errors.New(fmt.Sprintf("%v", err)))
			}
		}()

		for d := range msgs {
			if NewEnv().Config().LogLevel == logrus.DebugLevel {
				fmt.Println(fmt.Sprintf("Received a message at '%s' channel", name))
			}
			onConsume(d)
		}
	}()
	<-forever
	return nil
}

func NewMQ(env *ENVConfig) *MQ {
	return &MQ{
		Host:     env.MQHost,
		User:     env.MQUser,
		Password: env.MQPassword,
		Port:     env.MQPort,
	}
}

// ConnectDB to connect Database
func (m *MQ) Connect() (IMQ, error) {
	dsn := fmt.Sprintf("amqp://%s:%s@%s:%s/",
		m.User, m.Password, m.Host, m.Port,
	)

	conn, err := amqp.Dial(dsn)
	if err != nil {
		return nil, err
	}

	return &mq{connection: conn, mq: m}, nil
}

// ConnectDB to connect Database
func (m *MQ) ReConnect() (*amqp.Connection, error) {
	dsn := fmt.Sprintf("amqp://%s:%s@%s:%s/",
		m.User, m.Password, m.Host, m.Port,
	)

	conn, err := amqp.Dial(dsn)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

func (m mq) Close() {
	err := m.connection.Close()
	if err != nil {
		panic(err)
	}
}

func (m mq) Conn() *amqp.Connection {
	return m.connection
}
