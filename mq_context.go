package core

import (
	"fmt"
	"github.com/pskclub/mine-core/consts"
	"github.com/streadway/amqp"
)

type IMQContext interface {
	IContext
	AddConsumer(handlerFunc func(ctx IMQContext))
	Consume(name string, onConsume func(message amqp.Delivery), options *MQConsumeOptions)
	Start()
}

type MQContext struct {
	IContext
}

func (c *MQContext) Start() {
	fmt.Println(fmt.Sprintf("MQ Consumer Service: %s", c.ENV().Config().Service))
	select {}
}

func (c *MQContext) AddConsumer(handlerFunc func(ctx IMQContext)) {
	handlerFunc(c)
}

func (c *MQContext) Consume(name string, onConsume func(message amqp.Delivery), options *MQConsumeOptions) {
	go c.MQ().Consume(c, name, onConsume, options)
}

type MQContextOptions struct {
	ContextOptions *ContextOptions
}

func NewMQContext(options *MQContextOptions) IMQContext {
	ctxOptions := options.ContextOptions
	ctxOptions.contextType = consts.MQ
	return &MQContext{IContext: NewContext(ctxOptions)}
}
