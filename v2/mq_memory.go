package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryMQ is an in-process broker: exchanges, queues, bindings, routing by
// kind (direct, fanout, topic, headers), dead-lettering and per-message TTL —
// everything a handler's behaviour depends on, without RabbitMQ. A test that
// publishes an event and asserts the consumer acted on it should not need a
// broker running, and a dev box should not need one either.
//
//	mq := core.NewMemoryMQ()
//	app, _ := core.NewApp(env, core.WithMQ(mq))
//	c := app.NewMQConsumer()          // consumes from the memory broker
//	c.OnQueue(core.ConsumeQueue{...}, handle)
//	require.Nil(t, c.Start())
//
//	require.Nil(t, ctx.MQ().Publish("orders", "order.created", order))
//	assert.Eventually(t, func() bool { return len(mq.Messages("orders")) == 0 }, ...)
//
// What it does not do: persist anything, enforce exclusive or auto-delete
// queues, or honour priority and quorum settings — a message is delivered in
// the order it arrived. Published records every message whether or not a queue
// received it, which is what a unit test usually wants to assert on.
type MemoryMQ struct {
	b   *memoryBrokerState
	ctx context.Context
}

var _ IMQ = (*MemoryMQ)(nil)

// NewMemoryMQ returns an in-process broker. Wire it like any other:
//
//	app, _ := core.NewApp(env, core.WithMQ(core.NewMemoryMQ()))
func NewMemoryMQ() *MemoryMQ {
	return &MemoryMQ{
		b: &memoryBrokerState{
			exchanges: map[string]*memoryExchange{},
			queues:    map[string]*memoryQueue{},
		},
		ctx: context.Background(),
	}
}

// PublishedMessage is one Publish call as the memory broker saw it, kept for
// assertions whether or not anything was bound to receive it.
type PublishedMessage struct {
	Exchange string
	Key      string
	Body     []byte
	Options  PublishOptions
	// Queues are the queues the message was routed into, sorted; empty when
	// nothing was bound for it.
	Queues []string
}

// Bind decodes the body like Delivery.Bind.
func (p PublishedMessage) Bind(dest any) IError { return decodeCacheValue(p.Body, dest) }

type memoryBrokerState struct {
	mu        sync.Mutex
	exchanges map[string]*memoryExchange
	queues    map[string]*memoryQueue
	published []PublishedMessage
	closed    bool
	tags      uint64
}

type memoryExchange struct {
	cfg      ExchangeConfig
	bindings []memoryBinding
}

type memoryBinding struct {
	queue string
	key   string
	args  map[string]any
}

type memoryQueue struct {
	cfg       QueueConfig
	msgs      []*memoryMessage
	consumers int
	// notify wakes one reader when a message arrives. Buffered by one so a
	// publish between a reader's empty check and its wait is not lost.
	notify chan struct{}
}

type memoryMessage struct {
	d         *Delivery
	expiresAt time.Time
}

// ---------------------------------------------------------------------------
// IMQ
// ---------------------------------------------------------------------------

func (m *MemoryMQ) Publish(exchange, key string, msg any) IError {
	return m.PublishWith(exchange, key, msg, PublishOptions{})
}

func (m *MemoryMQ) PublishWith(exchange, key string, msg any, opts PublishOptions) IError {
	body, err := encodeMQBody(msg)
	if err != nil {
		return err
	}
	contentType := opts.ContentType
	if contentType == "" {
		contentType = "application/json"
		if _, raw := msg.([]byte); raw {
			contentType = "application/octet-stream"
		}
	}
	stamp := opts.Timestamp
	if stamp.IsZero() {
		stamp = time.Now()
	}
	d := &Delivery{
		Body:          body,
		Exchange:      exchange,
		RoutingKey:    key,
		Headers:       copyHeaders(opts.Headers),
		ContentType:   contentType,
		MessageID:     opts.MessageID,
		CorrelationID: opts.CorrelationID,
		ReplyTo:       opts.ReplyTo,
		Type:          opts.Type,
		AppID:         opts.AppID,
		UserID:        opts.UserID,
		Priority:      opts.Priority,
		Timestamp:     stamp,
	}
	var expiresAt time.Time
	if opts.Expiration > 0 {
		expiresAt = time.Now().Add(opts.Expiration)
	}

	b := m.b
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return New(503, "MQ_CLOSED", "mq: publisher is closed").WithCause(ErrMQClosed)
	}

	targets, rerr := b.route(exchange, key, d.Headers)
	if rerr != nil {
		return rerr
	}
	record := PublishedMessage{Exchange: exchange, Key: key, Body: body, Options: opts}
	for _, q := range targets {
		record.Queues = append(record.Queues, q.cfg.Name)
		b.enqueue(q, &memoryMessage{d: cloneDelivery(d), expiresAt: expiresAt}, false)
	}
	sort.Strings(record.Queues)
	b.published = append(b.published, record)

	if opts.Mandatory && len(targets) == 0 {
		return Newf(502, "MQ_NO_ROUTE", "mq: nothing is bound to %s for %q", exchange, key).
			WithCause(ErrMQNoRoute)
	}
	return nil
}

// route resolves the queues a message reaches. Caller holds the lock.
func (b *memoryBrokerState) route(exchange, key string, headers map[string]any) ([]*memoryQueue, IError) {
	if exchange == "" {
		// the default exchange: the routing key names the queue
		if q, ok := b.queues[key]; ok {
			return []*memoryQueue{q}, nil
		}
		return nil, nil
	}
	ex, ok := b.exchanges[exchange]
	if !ok {
		return nil, Newf(404, "MQ_NO_EXCHANGE", "mq: exchange %q is not declared", exchange)
	}
	seen := map[string]bool{}
	var out []*memoryQueue
	for _, bind := range ex.bindings {
		if seen[bind.queue] || !bindingMatches(ex.cfg.Kind, bind, key, headers) {
			continue
		}
		if q, ok := b.queues[bind.queue]; ok {
			seen[bind.queue] = true
			out = append(out, q)
		}
	}
	return out, nil
}

// bindingMatches applies the exchange kind's routing rule.
func bindingMatches(kind string, bind memoryBinding, key string, headers map[string]any) bool {
	switch kind {
	case ExchangeFanout:
		return true
	case ExchangeTopic:
		return topicMatches(bind.key, key)
	case ExchangeHeaders:
		return headersMatch(bind.args, headers)
	default: // direct
		return bind.key == key
	}
}

// topicMatches implements AMQP topic routing: words separated by ".", "*"
// matches exactly one word, "#" matches zero or more.
func topicMatches(pattern, key string) bool {
	return topicWords(strings.Split(pattern, "."), strings.Split(key, "."))
}

func topicWords(pattern, words []string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case "#":
			if len(pattern) == 1 {
				return true
			}
			for i := 0; i <= len(words); i++ {
				if topicWords(pattern[1:], words[i:]) {
					return true
				}
			}
			return false
		case "*":
			if len(words) == 0 {
				return false
			}
		default:
			if len(words) == 0 || words[0] != pattern[0] {
				return false
			}
		}
		pattern, words = pattern[1:], words[1:]
	}
	return len(words) == 0
}

// headersMatch implements the headers exchange: every binding argument but
// x-match must equal the message header ("all", the default) or any one must
// ("any").
func headersMatch(args map[string]any, headers map[string]any) bool {
	all := true
	if mode, ok := args["x-match"].(string); ok && mode == "any" {
		all = false
	}
	matched, checked := 0, 0
	for k, want := range args {
		if k == "x-match" {
			continue
		}
		checked++
		if got, ok := headers[k]; ok && fmt.Sprint(got) == fmt.Sprint(want) {
			matched++
		}
	}
	if checked == 0 {
		return true
	}
	if all {
		return matched == checked
	}
	return matched > 0
}

// enqueue appends to a queue, dead-lettering the oldest message when the queue
// is full. Caller holds the lock; redelivered puts the message at the head.
func (b *memoryBrokerState) enqueue(q *memoryQueue, msg *memoryMessage, redelivered bool) {
	msg.d.Queue = q.cfg.Name
	if q.cfg.TTL > 0 {
		limit := time.Now().Add(q.cfg.TTL)
		if msg.expiresAt.IsZero() || limit.Before(msg.expiresAt) {
			msg.expiresAt = limit
		}
	}
	if redelivered {
		msg.d.Redelivered = true
		q.msgs = append([]*memoryMessage{msg}, q.msgs...)
	} else {
		q.msgs = append(q.msgs, msg)
	}
	if q.cfg.MaxLength > 0 {
		for int64(len(q.msgs)) > q.cfg.MaxLength {
			oldest := q.msgs[0]
			q.msgs = q.msgs[1:]
			b.deadLetter(q, oldest, "maxlen")
		}
	}
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// deadLetter forwards a message to the queue's dead-letter exchange, or drops
// it when there is none — exactly what the broker does. Caller holds the lock.
func (b *memoryBrokerState) deadLetter(q *memoryQueue, msg *memoryMessage, reason string) {
	if q.cfg.DeadLetterExchange == "" {
		return
	}
	key := msg.d.RoutingKey
	if q.cfg.DeadLetterRoutingKey != "" {
		key = q.cfg.DeadLetterRoutingKey
	}
	d := cloneDelivery(msg.d)
	if d.Headers == nil {
		d.Headers = map[string]any{}
	}
	// the same headers RabbitMQ writes, so a replay handler reads one shape
	d.Headers["x-first-death-reason"] = reason
	d.Headers["x-first-death-queue"] = q.cfg.Name
	d.Headers["x-first-death-exchange"] = d.Exchange
	d.Exchange, d.RoutingKey, d.Redelivered = q.cfg.DeadLetterExchange, key, false

	targets, err := b.route(q.cfg.DeadLetterExchange, key, d.Headers)
	if err != nil {
		return // an undeclared dead-letter exchange drops the message, as the broker does
	}
	for _, target := range targets {
		b.enqueue(target, &memoryMessage{d: cloneDelivery(d)}, false)
	}
}

func (m *MemoryMQ) DeclareExchange(cfg ExchangeConfig) IError {
	if cfg.Name == "" {
		return New(400, "MQ_INVALID_TOPOLOGY", "mq: an exchange needs a name")
	}
	if cfg.Kind == "" {
		cfg.Kind = ExchangeDirect
	}
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	if existing, ok := m.b.exchanges[cfg.Name]; ok {
		if existing.cfg.Kind != cfg.Kind {
			return Newf(409, "MQ_TOPOLOGY_MISMATCH",
				"mq: exchange %q is %s, not %s", cfg.Name, existing.cfg.Kind, cfg.Kind)
		}
		return nil
	}
	m.b.exchanges[cfg.Name] = &memoryExchange{cfg: cfg}
	return nil
}

func (m *MemoryMQ) DeclareQueue(cfg QueueConfig) (QueueInfo, IError) {
	if cfg.Name == "" {
		return QueueInfo{}, New(400, "MQ_INVALID_TOPOLOGY", "mq: a queue needs a name")
	}
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	q := m.b.declareQueue(cfg)
	return q.info(), nil
}

// declareQueue creates a queue if it does not exist. Caller holds the lock.
func (b *memoryBrokerState) declareQueue(cfg QueueConfig) *memoryQueue {
	if q, ok := b.queues[cfg.Name]; ok {
		return q
	}
	q := &memoryQueue{cfg: cfg, notify: make(chan struct{}, 1)}
	b.queues[cfg.Name] = q
	return q
}

func (q *memoryQueue) info() QueueInfo {
	return QueueInfo{Name: q.cfg.Name, Messages: len(q.msgs), Consumers: q.consumers}
}

func (m *MemoryMQ) BindQueue(queue, exchange, key string, args ...map[string]any) IError {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	ex, ok := m.b.exchanges[exchange]
	if !ok {
		return Newf(404, "MQ_NO_EXCHANGE", "mq: exchange %q is not declared", exchange)
	}
	if _, ok := m.b.queues[queue]; !ok {
		return Newf(404, "MQ_NO_QUEUE", "mq: queue %q is not declared", queue)
	}
	var bindArgs map[string]any
	if len(args) > 0 {
		bindArgs = args[0]
	}
	for _, b := range ex.bindings {
		if b.queue == queue && b.key == key && fmt.Sprint(b.args) == fmt.Sprint(bindArgs) {
			return nil // binding twice is idempotent, as on the broker
		}
	}
	ex.bindings = append(ex.bindings, memoryBinding{queue: queue, key: key, args: bindArgs})
	return nil
}

func (m *MemoryMQ) UnbindQueue(queue, exchange, key string, args ...map[string]any) IError {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	ex, ok := m.b.exchanges[exchange]
	if !ok {
		return Newf(404, "MQ_NO_EXCHANGE", "mq: exchange %q is not declared", exchange)
	}
	kept := ex.bindings[:0]
	for _, b := range ex.bindings {
		if b.queue == queue && b.key == key {
			continue
		}
		kept = append(kept, b)
	}
	ex.bindings = kept
	return nil
}

func (m *MemoryMQ) DeleteQueue(name string) IError {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	delete(m.b.queues, name)
	for _, ex := range m.b.exchanges {
		kept := ex.bindings[:0]
		for _, b := range ex.bindings {
			if b.queue != name {
				kept = append(kept, b)
			}
		}
		ex.bindings = kept
	}
	return nil
}

func (m *MemoryMQ) PurgeQueue(name string) (int, IError) {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	q, ok := m.b.queues[name]
	if !ok {
		return 0, Newf(404, "MQ_NO_QUEUE", "mq: queue %q is not declared", name)
	}
	n := len(q.msgs)
	q.msgs = nil
	return n, nil
}

func (m *MemoryMQ) QueueInfo(name string) (QueueInfo, IError) {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	q, ok := m.b.queues[name]
	if !ok {
		return QueueInfo{}, Newf(404, "MQ_NO_QUEUE", "mq: queue %q is not declared", name)
	}
	return q.info(), nil
}

func (m *MemoryMQ) Ping() IError  { return nil }
func (m *MemoryMQ) Enabled() bool { return true }

func (m *MemoryMQ) WithContext(ctx context.Context) IMQ {
	if ctx == nil {
		ctx = context.Background()
	}
	return &MemoryMQ{b: m.b, ctx: ctx}
}

// Close stops the broker: every later Publish fails with MQ_CLOSED. Messages
// already queued stay readable for assertions.
func (m *MemoryMQ) Close() IError {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	m.b.closed = true
	return nil
}

// ---------------------------------------------------------------------------
// Test accessors
// ---------------------------------------------------------------------------

// Published returns every message published so far, in order, routed or not.
func (m *MemoryMQ) Published() []PublishedMessage {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	out := make([]PublishedMessage, len(m.b.published))
	copy(out, m.b.published)
	return out
}

// Messages returns what is waiting in a queue — published and not yet taken
// by a consumer, or requeued. A queue that does not exist is empty.
func (m *MemoryMQ) Messages(queue string) []*Delivery {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	q, ok := m.b.queues[queue]
	if !ok {
		return nil
	}
	out := make([]*Delivery, 0, len(q.msgs))
	for _, msg := range q.msgs {
		out = append(out, cloneDelivery(msg.d))
	}
	return out
}

// Reset forgets every message, published record and queue content, keeping
// the topology — for a test that reuses one broker across cases.
func (m *MemoryMQ) Reset() {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	m.b.published = nil
	for _, q := range m.b.queues {
		q.msgs = nil
	}
}

// ---------------------------------------------------------------------------
// Consuming
// ---------------------------------------------------------------------------

// take pops the next live message off a queue, dead-lettering expired ones on
// the way. When the queue is empty it returns the channel to wait on.
func (b *memoryBrokerState) take(queue string) (*memoryMessage, <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	q, ok := b.queues[queue]
	if !ok {
		return nil, nil
	}
	now := time.Now()
	for len(q.msgs) > 0 {
		msg := q.msgs[0]
		q.msgs = q.msgs[1:]
		if !msg.expiresAt.IsZero() && msg.expiresAt.Before(now) {
			b.deadLetter(q, msg, "expired")
			continue
		}
		b.tags++
		msg.d.DeliveryTag = b.tags
		return msg, nil
	}
	return nil, q.notify
}

// settle applies a handler's outcome: ack drops the message, requeue puts it
// back at the head marked redelivered, reject dead-letters it.
func (b *memoryBrokerState) settle(queue string, msg *memoryMessage, err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	q, ok := b.queues[queue]
	if !ok {
		return
	}
	if shouldRequeue(err) {
		b.enqueue(q, msg, true)
		return
	}
	b.deadLetter(q, msg, "rejected")
}

func (b *memoryBrokerState) addConsumer(queue string, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if q, ok := b.queues[queue]; ok {
		q.consumers += n
	}
}

// memoryMQConsumer is IMQConsumer over a MemoryMQ. App.NewMQConsumer returns
// one whenever the App's publisher is a MemoryMQ, so the service's own wiring
// (OnQueue, Start, Stop) is what the test exercises.
type memoryMQConsumer struct {
	app  *App
	mq   *MemoryMQ
	opts mqConsumerOptions

	mu       sync.Mutex
	handlers map[string]MQHandler
	topology map[string]ConsumeQueue
	running  bool

	wg   sync.WaitGroup
	sem  chan struct{}
	stop chan struct{}
	once sync.Once
}

var (
	_ IMQConsumer = (*memoryMQConsumer)(nil)
	_ Stoppable   = (*memoryMQConsumer)(nil)
)

func (m *MemoryMQ) newConsumer(app *App, o mqConsumerOptions) *memoryMQConsumer {
	return &memoryMQConsumer{
		app:      app,
		mq:       m,
		opts:     o,
		handlers: map[string]MQHandler{},
		topology: map[string]ConsumeQueue{},
	}
}

func (c *memoryMQConsumer) On(queue string, h MQHandler) IMQConsumer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[queue] = h
	return c
}

func (c *memoryMQConsumer) OnQueue(cfg ConsumeQueue, h MQHandler) IMQConsumer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[cfg.Queue.Name] = h
	c.topology[cfg.Queue.Name] = cfg
	return c
}

func (c *memoryMQConsumer) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

func (c *memoryMQConsumer) Start() IError {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return New(409, "MQ_CONSUMER_RUNNING", "mq: consumer is already running")
	}
	if len(c.handlers) == 0 {
		c.mu.Unlock()
		return New(400, "MQ_CONSUMER_EMPTY", "mq: consumer has no handlers")
	}
	handlers := make(map[string]MQHandler, len(c.handlers))
	for q, h := range c.handlers {
		handlers[q] = h
	}
	topology := make(map[string]ConsumeQueue, len(c.topology))
	for q, t := range c.topology {
		topology[q] = t
	}
	c.stop = make(chan struct{})
	c.once = sync.Once{}
	c.sem = make(chan struct{}, c.opts.concurrency)
	c.running = true
	c.mu.Unlock()

	// the same topology the RabbitMQ consumer declares, through the same
	// publisher API, so a binding mistake fails here as it would there
	for queue := range handlers {
		cfg, ok := topology[queue]
		if !ok {
			// an "external" queue is expected to exist; make it so the reader
			// has somewhere to wait, as a pre-provisioned broker would
			cfg = ConsumeQueue{Queue: QueueConfig{Name: queue}}
		}
		if err := c.declare(cfg); err != nil {
			c.mu.Lock()
			c.running = false
			c.mu.Unlock()
			return err
		}
	}

	for queue, handler := range handlers {
		c.mq.b.addConsumer(queue, 1)
		c.wg.Add(1)
		go func(queue string, handler MQHandler) {
			defer c.wg.Done()
			defer c.mq.b.addConsumer(queue, -1)
			c.read(queue, handler)
		}(queue, handler)
	}
	c.app.Log().Info("mq consumer started (memory)",
		"queues", len(handlers), "concurrency", c.opts.concurrency)
	return nil
}

func (c *memoryMQConsumer) declare(cfg ConsumeQueue) IError {
	if cfg.Exchange != nil && cfg.Exchange.Name != "" {
		if err := c.mq.DeclareExchange(*cfg.Exchange); err != nil {
			return err
		}
	}
	if _, err := c.mq.DeclareQueue(cfg.Queue); err != nil {
		return err
	}
	if cfg.Exchange == nil || cfg.Exchange.Name == "" {
		return nil
	}
	keys := cfg.BindingKeys
	if len(keys) == 0 {
		keys = []string{""}
	}
	for _, key := range keys {
		if err := c.mq.BindQueue(cfg.Queue.Name, cfg.Exchange.Name, key); err != nil {
			return err
		}
	}
	return nil
}

// read is the per-queue loop: wait for a message, take a slot, run the handler
// in the background, settle the message with its outcome.
func (c *memoryMQConsumer) read(queue string, handler MQHandler) {
	var handlers sync.WaitGroup
	defer handlers.Wait()

	for {
		msg, wait := c.mq.b.take(queue)
		if msg == nil {
			if wait == nil {
				return // the queue was deleted underneath us
			}
			select {
			case <-wait:
				continue
			case <-c.stop:
				return
			}
		}
		select {
		case c.sem <- struct{}{}:
		case <-c.stop:
			// stopping: hand the message back rather than lose one this
			// consumer will never run
			c.mq.b.settle(queue, msg, Requeue(context.Canceled))
			return
		}
		handlers.Add(1)
		go func(msg *memoryMessage) {
			defer handlers.Done()
			defer func() { <-c.sem }()
			started := time.Now()
			ctx, err := runMQHandler(c.app, c.opts.timeout, queue, handler, msg.d)
			c.mq.b.settle(queue, msg, err)
			logMQOutcome(ctx, queue, msg.d, err, time.Since(started))
		}(msg)
	}
}

func (c *memoryMQConsumer) Stop(ctx context.Context) IError {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	c.running = false
	stop := c.stop
	c.mu.Unlock()
	c.once.Do(func() { close(stop) })

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), DefaultGracefulTimeout)
		defer cancel()
	}
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		c.app.Log().Info("mq consumer stopped (memory)")
		return nil
	case <-ctx.Done():
		return Wrap(ctx.Err(), "mq: stop timed out with handlers still running")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func cloneDelivery(d *Delivery) *Delivery {
	cp := *d
	cp.Body = append([]byte(nil), d.Body...)
	cp.Headers = copyHeaders(d.Headers)
	return &cp
}

func copyHeaders(h map[string]any) map[string]any {
	if h == nil {
		return nil
	}
	out := make(map[string]any, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out
}
