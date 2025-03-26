package core

import (
	"fmt"
	"github.com/pskclub/mine-core/utils"
	"github.com/sirupsen/logrus"
	"net/http"

	"github.com/go-errors/errors"
	amqp "github.com/rabbitmq/amqp091-go"
)

var MQError = Error{
	Status:  http.StatusInternalServerError,
	Code:    "MQ_ERROR",
	Message: "mq internal error"}

type MQ struct {
	URI      string
	Host     string
	User     string
	Password string
	Port     string
	LogLevel logrus.Level
}

type MQPublishOptions struct {
	Exchange     string
	MessageID    string
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
	Consume(ctx IMQContext, name string, onConsume func(message amqp.Delivery), options *MQConsumeOptions)
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

	if m.mq.LogLevel == logrus.DebugLevel {
		fmt.Printf("Publish a message at '%s' channel\n", name)
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

func (m mq) Consume(ctx IMQContext, name string, onConsume func(message amqp.Delivery), options *MQConsumeOptions) {
	m.ReConnect()
	ch, err := m.Conn().Channel()
	if err != nil {
		ctx.NewError(err, MQError)
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
		ctx.NewError(err, MQError)
	}

	err = ch.Qos(
		1,     // prefetch count
		0,     // prefetch size
		false, // global
	)
	if err != nil {
		ctx.NewError(err, MQError)
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
		ctx.NewError(err, MQError)
	}

	var forever chan struct{}
	go func() {
		defer func() {
			if err := recover(); err != nil {
				errmsg := errors.New(fmt.Sprintf("%v", err))
				fmt.Println(errmsg)
				ctx.NewError(errmsg, MQError)
			}
		}()

		for d := range msgs {
			if m.mq.LogLevel == logrus.DebugLevel {
				fmt.Println(fmt.Sprintf("Received a message at '%s' channel", name))
			}

			onConsume(d)
		}
	}()
	<-forever
}

func NewMQ(env *ENVConfig) *MQ {
	return &MQ{
		URI:      env.MQURI,
		Host:     env.MQHost,
		User:     env.MQUser,
		Password: env.MQPassword,
		Port:     env.MQPort,
		LogLevel: env.LogLevel,
	}
}

// ConnectDB to connect Database
func (m *MQ) Connect() (IMQ, error) {
	var dsn string
	if m.URI != "" {
		dsn = m.URI
	} else {
		dsn = fmt.Sprintf("amqp://%s:%s@%s:%s/",
			m.User, m.Password, m.Host, m.Port,
		)
	}
	conn, err := amqp.Dial(dsn)
	if err != nil {
		return nil, err
	}

	return &mq{connection: conn, mq: m}, nil
}

// ConnectDB to connect Database
func (m *MQ) ReConnect() (*amqp.Connection, error) {
	var dsn string
	if m.URI != "" {
		dsn = m.URI
	} else {
		dsn = fmt.Sprintf("amqp://%s:%s@%s:%s/",
			m.User, m.Password, m.Host, m.Port,
		)
	}

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
