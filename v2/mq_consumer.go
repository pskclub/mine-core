package core

import (
	"context"
	"sort"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// MQHandler handles one delivery. The context is a full IMQContext — logger,
// database, cache, Sentry, and the delivery itself — built for that message and
// cancelled when the handler's timeout runs out.
//
// Returning nil acknowledges the message. Returning an error rejects it: it goes
// to the queue's dead-letter exchange, or is dropped when the queue has none.
// Wrap the error in Requeue to ask for redelivery instead.
type MQHandler func(ctx IMQContext, d *Delivery) error

// IMQContext is the per-delivery context: an IContext plus the message that
// caused it. Same name as v1, where it was the consumer's context too.
type IMQContext interface {
	IContext
	// Delivery is the message being handled.
	Delivery() *Delivery
}

// IMQConsumer turns queues into handlers, the way the HTTP server turns routes
// into handlers. It owns the connection, the prefetch, the acknowledgement, the
// per-message context, the panic recovery, the reconnect and the drain.
//
//	c := app.NewMQConsumer(core.WithMQPrefetch(20))
//	c.On("orders", func(ctx core.IMQContext, d *core.Delivery) error {
//	    order, err := core.BindDelivery[Order](d)
//	    if err != nil {
//	        return err // malformed: dead-letter it, redelivery will not help
//	    }
//	    return process(ctx, order)
//	})
//	if err := c.Start(); err != nil { ... }
type IMQConsumer interface {
	// On registers a handler for a queue. Registering a queue twice replaces the
	// first handler.
	On(queue string, h MQHandler) IMQConsumer
	// OnQueue is On with the queue declared and bound first, so a service brings
	// up its own topology rather than depending on somebody having run a script.
	OnQueue(cfg ConsumeQueue, h MQHandler) IMQConsumer
	// Start opens the connection and begins consuming. It returns as soon as the
	// consumers are live; the work happens in the background.
	Start() IError
	// Stop stops consuming and waits for in-flight handlers, up to the deadline
	// on ctx. Safe to call more than once.
	Stop(ctx context.Context) IError
	// Running reports whether Start has run and Stop has not.
	Running() bool
}

// ConsumeQueue is a queue to consume, together with the topology it needs.
type ConsumeQueue struct {
	// Queue is declared before consuming. Name is the only required field.
	Queue QueueConfig
	// Exchange, when named, is declared too.
	Exchange *ExchangeConfig
	// BindingKeys bind Queue to Exchange. An empty list with an exchange set
	// binds on "" — which is what a fanout exchange wants.
	BindingKeys []string
}

// MQConsumerOption tunes a consumer.
type MQConsumerOption func(*mqConsumerOptions)

type mqConsumerOptions struct {
	prefetch    int
	concurrency int
	timeout     time.Duration
	requeueOnUp bool
	reconnect   time.Duration
	tag         string
}

// DefaultMQ consumer settings.
const (
	// DefaultMQPrefetch is how many unacknowledged messages the broker will
	// hand this consumer. It is the backpressure knob: too low and the consumer
	// waits on the network between messages, too high and one instance takes
	// work it cannot get to while another sits idle.
	DefaultMQPrefetch = 10
	// DefaultMQHandlerTimeout bounds one handler.
	DefaultMQHandlerTimeout = 30 * time.Second
	// DefaultMQReconnectDelay is how long a consumer waits before redialling a
	// broker that went away.
	DefaultMQReconnectDelay = 2 * time.Second
)

// WithMQPrefetch sets how many unacknowledged messages the broker may hand this
// consumer at once (default DefaultMQPrefetch).
func WithMQPrefetch(n int) MQConsumerOption {
	return func(o *mqConsumerOptions) {
		if n > 0 {
			o.prefetch = n
		}
	}
}

// WithMQConcurrency allows n handlers to run at once. The default is 1, which
// keeps a queue's messages in order; raising it trades that order for
// throughput, so raise it only when the handlers are independent.
func WithMQConcurrency(n int) MQConsumerOption {
	return func(o *mqConsumerOptions) {
		if n > 0 {
			o.concurrency = n
		}
	}
}

// WithMQHandlerTimeout bounds one handler (default DefaultMQHandlerTimeout). A
// handler that overruns has its context cancelled, and its message is requeued
// rather than lost.
func WithMQHandlerTimeout(d time.Duration) MQConsumerOption {
	return func(o *mqConsumerOptions) {
		if d > 0 {
			o.timeout = d
		}
	}
}

// WithMQReconnectDelay sets how long to wait before redialling (default
// DefaultMQReconnectDelay).
func WithMQReconnectDelay(d time.Duration) MQConsumerOption {
	return func(o *mqConsumerOptions) {
		if d > 0 {
			o.reconnect = d
		}
	}
}

// WithMQConsumerTag names this consumer on the broker, which is what the
// management UI shows. Defaults to the service name.
func WithMQConsumerTag(tag string) MQConsumerOption {
	return func(o *mqConsumerOptions) { o.tag = tag }
}

// NewMQConsumer builds a consumer on the App's broker configuration. Nothing
// connects until Start, so handlers can be registered in any order.
//
// The App remembers it and stops it during Shutdown, before the connections its
// handlers use are closed.
func (a *App) NewMQConsumer(opts ...MQConsumerOption) IMQConsumer {
	o := mqConsumerOptions{
		prefetch:    DefaultMQPrefetch,
		concurrency: 1,
		timeout:     DefaultMQHandlerTimeout,
		reconnect:   DefaultMQReconnectDelay,
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.tag == "" && a.env != nil {
		o.tag = a.env.Config().Service
	}

	c := &mqConsumer{
		app:      a,
		opts:     o,
		handlers: map[string]MQHandler{},
		topology: map[string]ConsumeQueue{},
	}
	a.trackSubscriber(c)
	return c
}

type mqConsumer struct {
	app  *App
	opts mqConsumerOptions

	mu       sync.Mutex
	handlers map[string]MQHandler
	topology map[string]ConsumeQueue
	running  bool

	conn *amqp.Connection
	ch   *amqp.Channel

	wg     sync.WaitGroup
	sem    chan struct{}
	stop   chan struct{}
	once   sync.Once
	closed bool
}

var (
	_ IMQConsumer = (*mqConsumer)(nil)
	// tracked by the App and stopped before the pools close, like a pub/sub
	// subscriber — a consumer still dispatching into a closed database is the
	// thing this ordering exists to prevent
	_ Stoppable = (*mqConsumer)(nil)
)

func (c *mqConsumer) On(queue string, h MQHandler) IMQConsumer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[queue] = h
	return c
}

func (c *mqConsumer) OnQueue(cfg ConsumeQueue, h MQHandler) IMQConsumer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[cfg.Queue.Name] = h
	c.topology[cfg.Queue.Name] = cfg
	return c
}

func (c *mqConsumer) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

func (c *mqConsumer) Start() IError {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return New(409, "MQ_CONSUMER_RUNNING", "mq: consumer is already running")
	}
	if len(c.handlers) == 0 {
		c.mu.Unlock()
		return New(400, "MQ_CONSUMER_EMPTY", "mq: consumer has no handlers")
	}
	c.stop = make(chan struct{})
	c.once = sync.Once{}
	c.closed = false
	c.sem = make(chan struct{}, c.opts.concurrency)
	c.running = true
	queues := make([]string, 0, len(c.handlers))
	for q := range c.handlers {
		queues = append(queues, q)
	}
	c.mu.Unlock()

	// connect once here so a broker that is down is a Start error the caller can
	// act on, rather than a background goroutine retrying into a log nobody
	// reads at boot
	if err := c.connect(); err != nil {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
		return err
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.supervise()
	}()

	c.logQueues(queues)
	return nil
}

// logQueues names what this consumer reads and how each queue is fed.
//
// "mq consumer started" with a bag of names is enough to know something is
// listening and not enough to know whether it will hear anything: a queue bound
// to the wrong key, or to no exchange at all, looks identical from the outside
// until the messages do not arrive. The bindings are the half worth printing.
//
// The names are sorted because they come out of a map, and a boot log that
// reorders itself between restarts cannot be diffed.
func (c *mqConsumer) logQueues(queues []string) {
	sort.Strings(queues)

	c.app.Log().Info("mq consumer started",
		"queues", len(queues),
		"prefetch", c.opts.prefetch,
		"concurrency", c.opts.concurrency,
		"handler_timeout", c.opts.timeout.String())

	c.mu.Lock()
	topology := make(map[string]ConsumeQueue, len(c.topology))
	for q, t := range c.topology {
		topology[q] = t
	}
	c.mu.Unlock()

	for _, queue := range queues {
		fields := []any{"queue", queue}

		cfg, declared := topology[queue]
		switch {
		case !declared:
			// the queue is expected to exist already, which is a real choice and
			// a real way to end up listening to nothing
			fields = append(fields, "topology", "external")
		case cfg.Exchange != nil && cfg.Exchange.Name != "":
			keys := cfg.BindingKeys
			if len(keys) == 0 {
				keys = []string{""}
			}
			fields = append(fields,
				"exchange", cfg.Exchange.Name,
				"exchange_kind", cfg.Exchange.Kind,
				"binding_keys", keys)
		default:
			fields = append(fields, "topology", "queue-only")
		}
		if declared && cfg.Queue.DeadLetterExchange != "" {
			fields = append(fields, "dead_letter", cfg.Queue.DeadLetterExchange)
		}
		if declared && cfg.Queue.Quorum {
			fields = append(fields, "quorum", true)
		}

		c.app.Log().Info("queue consumed", fields...)
	}
}

// connect dials, sets the prefetch, applies the topology and starts one reader
// per queue. It is called by Start and again by the supervisor after a drop.
func (c *mqConsumer) connect() IError {
	conn, err := amqp.Dial(mqURL(c.app.env.Config()))
	if err != nil {
		return Wrap(err, "mq consumer: dial")
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return Wrap(err, "mq consumer: channel")
	}
	// prefetch is per consumer, not global: each reader gets this many in flight
	if err := ch.Qos(c.opts.prefetch, 0, false); err != nil {
		_ = conn.Close()
		return Wrap(err, "mq consumer: qos")
	}

	c.mu.Lock()
	handlers := make(map[string]MQHandler, len(c.handlers))
	for q, h := range c.handlers {
		handlers[q] = h
	}
	topology := make(map[string]ConsumeQueue, len(c.topology))
	for q, t := range c.topology {
		topology[q] = t
	}
	c.mu.Unlock()

	for queue := range handlers {
		if cfg, ok := topology[queue]; ok {
			if terr := declareConsumeTopology(ch, cfg); terr != nil {
				_ = conn.Close()
				return terr
			}
		}
	}

	for queue, handler := range handlers {
		deliveries, cerr := ch.Consume(queue, c.consumerTag(queue), false, false, false, false, nil)
		if cerr != nil {
			_ = conn.Close()
			return Wrapf(cerr, "mq consumer: consume %s", queue)
		}
		c.wg.Add(1)
		go func(queue string, handler MQHandler, in <-chan amqp.Delivery) {
			defer c.wg.Done()
			c.read(queue, handler, in)
		}(queue, handler, deliveries)
	}

	c.mu.Lock()
	c.conn, c.ch = conn, ch
	c.mu.Unlock()
	return nil
}

func (c *mqConsumer) consumerTag(queue string) string {
	if c.opts.tag == "" {
		return ""
	}
	return c.opts.tag + "-" + queue
}

// declareConsumeTopology brings up the exchange, queue and bindings a handler
// needs, so deploying the service is what creates them.
func declareConsumeTopology(ch *amqp.Channel, cfg ConsumeQueue) IError {
	if cfg.Exchange != nil && cfg.Exchange.Name != "" {
		kind := cfg.Exchange.Kind
		if kind == "" {
			kind = ExchangeDirect
		}
		if err := ch.ExchangeDeclare(cfg.Exchange.Name, kind, !cfg.Exchange.Transient,
			cfg.Exchange.AutoDelete, cfg.Exchange.Internal, false,
			amqp.Table(cfg.Exchange.Args)); err != nil {
			return Wrapf(err, "mq consumer: declare exchange %s", cfg.Exchange.Name)
		}
	}

	if _, err := ch.QueueDeclare(cfg.Queue.Name, !cfg.Queue.Transient, cfg.Queue.AutoDelete,
		cfg.Queue.Exclusive, false, queueArgs(cfg.Queue)); err != nil {
		return Wrapf(err, "mq consumer: declare queue %s", cfg.Queue.Name)
	}

	if cfg.Exchange == nil || cfg.Exchange.Name == "" {
		return nil
	}
	keys := cfg.BindingKeys
	if len(keys) == 0 {
		// a fanout exchange ignores the key, and this is the only sane default
		// for one — binding on nothing is what "everything" means there
		keys = []string{""}
	}
	for _, key := range keys {
		if err := ch.QueueBind(cfg.Queue.Name, key, cfg.Exchange.Name, false, nil); err != nil {
			return Wrapf(err, "mq consumer: bind %s to %s", cfg.Queue.Name, cfg.Exchange.Name)
		}
	}
	return nil
}

// read consumes one queue until its channel closes. The read loop stays
// sequential; only the handlers fan out, so the semaphore is the single place
// concurrency is decided.
func (c *mqConsumer) read(queue string, handler MQHandler, in <-chan amqp.Delivery) {
	var handlers sync.WaitGroup
	defer handlers.Wait()

	for msg := range in {
		select {
		case c.sem <- struct{}{}:
		case <-c.stop:
			// stopping: hand the message back rather than acknowledging one this
			// consumer will never run
			_ = msg.Nack(false, true)
			return
		}
		handlers.Add(1)
		go func(msg amqp.Delivery) {
			defer handlers.Done()
			defer func() { <-c.sem }()
			c.dispatch(queue, handler, msg)
		}(msg)
	}
}

// supervise watches the connection and rebuilds it after a drop, until Stop.
//
// A consumer that does not do this is a consumer that goes quiet after the first
// broker restart, with nothing in the logs but silence — the queue fills, and
// the first anybody knows is the alert on its depth.
func (c *mqConsumer) supervise() {
	for {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()

		if conn == nil {
			return
		}
		closed := conn.NotifyClose(make(chan *amqp.Error, 1))

		select {
		case <-c.stop:
			return
		case reason := <-closed:
			if c.isStopping() {
				return
			}
			c.app.Log().Warn("mq consumer: connection lost, reconnecting",
				"err", errText(reason), "in", c.opts.reconnect.String())
		}

		for {
			select {
			case <-c.stop:
				return
			case <-time.After(c.opts.reconnect):
			}
			if c.isStopping() {
				return
			}
			if err := c.connect(); err != nil {
				c.app.Log().Error("mq consumer: reconnect failed", "err", err)
				continue
			}
			c.app.Log().Info("mq consumer: reconnected")
			break
		}
	}
}

func (c *mqConsumer) isStopping() bool {
	select {
	case <-c.stop:
		return true
	default:
		return false
	}
}

// dispatch runs one handler with everything a unit of work gets — its own
// context, its own Sentry scope and transaction, a timeout, and a panic that
// ends the message rather than the process — then acknowledges accordingly.
func (c *mqConsumer) dispatch(queue string, h MQHandler, msg amqp.Delivery) {
	d := toDelivery(queue, msg)

	base, cancel := context.WithTimeout(context.Background(), c.opts.timeout)
	defer cancel()

	tracker := c.app.Sentry()
	if hubCtx, hub := withHub(base, tracker); hub != nil {
		hub.Scope().SetTags(map[string]string{
			"mode":        ModeMQ.String(),
			"queue":       queue,
			"routing_key": d.RoutingKey,
		})
		base = hubCtx
	}

	ctx := c.app.NewContext(base, ModeMQ)
	span := ctx.Sentry().StartTransaction("mq "+queue, "queue.process")
	ctx = ctx.WithContext(span.Context())

	started := time.Now()
	var err error
	func() {
		defer Recover(&err)
		err = h(&mqContext{IContext: ctx, delivery: d}, d)
	}()
	span.Finish(err)

	c.acknowledge(ctx, queue, d, msg, err, started)
}

// acknowledge turns the handler's outcome into an AMQP acknowledgement.
//
// The choice that matters is what a plain error does. Requeueing it puts the
// message straight back at the head of the queue, and a failure that is not
// transient then loops as fast as the broker can deliver it — the classic way an
// AMQP consumer takes itself and its database down. So a plain error rejects
// without requeue, which dead-letters the message if the queue has an exchange
// for it and drops it otherwise, and a handler that knows the failure is
// transient says so with Requeue.
func (c *mqConsumer) acknowledge(ctx IContext, queue string, d *Delivery, msg amqp.Delivery, err error, started time.Time) {
	took := time.Since(started)

	if err == nil {
		if ackErr := msg.Ack(false); ackErr != nil {
			ctx.Log().Error("mq: ack failed", "queue", queue, "err", ackErr)
		}
		ctx.Log().Debug("mq message handled",
			"queue", queue, "routing_key", d.RoutingKey, "took", took.String())
		return
	}

	requeue := shouldRequeue(err)
	if nackErr := msg.Nack(false, requeue); nackErr != nil {
		ctx.Log().Error("mq: nack failed", "queue", queue, "err", nackErr)
	}

	// logging it is what reports it: the logger's Sentry bridge turns a line
	// carrying a 5xx error into an event, so this is one issue, not two
	ctx.Log().Error("mq handler failed",
		"queue", queue,
		"routing_key", d.RoutingKey,
		"message_id", d.MessageID,
		"redelivered", d.Redelivered,
		"requeued", requeue,
		"took", took.String(),
		"err", err)
}

// toDelivery converts the driver's delivery into ours, so a handler never has to
// import the AMQP package.
func toDelivery(queue string, msg amqp.Delivery) *Delivery {
	return &Delivery{
		Body:          msg.Body,
		Exchange:      msg.Exchange,
		RoutingKey:    msg.RoutingKey,
		Queue:         queue,
		ConsumerTag:   msg.ConsumerTag,
		Headers:       map[string]any(msg.Headers),
		ContentType:   msg.ContentType,
		MessageID:     msg.MessageId,
		CorrelationID: msg.CorrelationId,
		ReplyTo:       msg.ReplyTo,
		Type:          msg.Type,
		AppID:         msg.AppId,
		UserID:        msg.UserId,
		Priority:      msg.Priority,
		Timestamp:     msg.Timestamp,
		Redelivered:   msg.Redelivered,
		DeliveryTag:   msg.DeliveryTag,
	}
}

func (c *mqConsumer) Stop(ctx context.Context) IError {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	c.running = false
	stop := c.stop
	conn, ch := c.conn, c.ch
	c.conn, c.ch = nil, nil
	c.mu.Unlock()

	c.once.Do(func() { close(stop) })

	// cancelling the consumers closes the delivery channels, which ends the read
	// loops; the connection stays up until the handlers they started are done
	if ch != nil {
		_ = ch.Cancel("", false)
	}

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), stopGrace)
		defer cancel()
	}

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		closeAMQP(conn)
		c.app.Log().Info("mq consumer stopped")
		return nil
	case <-ctx.Done():
		// the deadline is the deadline: close underneath the handlers still
		// running, so their messages go back to the broker unacknowledged rather
		// than holding the shutdown open
		closeAMQP(conn)
		return Wrap(ctx.Err(), "mq: stop timed out with handlers still running")
	}
}

func closeAMQP(conn *amqp.Connection) {
	if conn != nil && !conn.IsClosed() {
		_ = conn.Close()
	}
}

func errText(err *amqp.Error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// mqContext is the per-delivery context handed to a handler.
type mqContext struct {
	IContext
	delivery *Delivery
}

var _ IMQContext = (*mqContext)(nil)

func (c *mqContext) Delivery() *Delivery { return c.delivery }
