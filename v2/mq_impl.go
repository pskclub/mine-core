package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// MQ publisher defaults.
const (
	// DefaultMQPublishTimeout bounds one publish, waiting for the broker's
	// confirmation included. A request context with a shorter deadline still
	// wins — this is the ceiling, not the deadline.
	DefaultMQPublishTimeout = 5 * time.Second
	// DefaultMQChannels is how many publishes may be in flight at once. An AMQP
	// channel may only be used by one goroutine at a time, so this is both the
	// size of the channel pool and the concurrency limit.
	DefaultMQChannels = 8
)

// MQOption tunes a publisher at construction.
type MQOption func(*mqConfig)

type mqConfig struct {
	channels int
	timeout  time.Duration
}

// WithMQChannels sets how many publishes may be in flight at once. Raise it for
// a service that publishes from many requests at once; every channel is a small
// amount of state on the broker, and brokers cap them (channel_max, 2047 by
// default).
func WithMQChannels(n int) MQOption {
	return func(c *mqConfig) {
		if n > 0 {
			c.channels = n
		}
	}
}

// WithMQPublishTimeout bounds one publish including the wait for the broker's
// confirmation.
func WithMQPublishTimeout(d time.Duration) MQOption {
	return func(c *mqConfig) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// amqpPublisher owns the connection and the pool of channels every handle
// WithContext hands out shares.
//
// It reconnects on demand rather than in the background: a broker that comes
// back is noticed by the first publish after it does, and a service that
// publishes nothing does not hold a connection open trying.
type amqpPublisher struct {
	url     string
	timeout time.Duration

	// slots bounds how many publishes run at once and carries the idle channels
	// between them. It is prefilled with nil entries: taking a nil slot is
	// permission to open a channel, taking a live one is permission to reuse it.
	// Every publish puts its slot back, so the pool neither grows nor shrinks.
	slots chan *mqChannel

	dialMu sync.Mutex
	conn   *amqp.Connection

	closeOnce sync.Once
	closed    chan struct{}
}

// mqChannel is a channel together with the connection it was opened on, so one
// left over from a connection that has since been replaced is recognised and
// dropped instead of used.
type mqChannel struct {
	ch   *amqp.Channel
	conn *amqp.Connection
}

type rabbitMQ struct {
	ctx context.Context
	p   *amqpPublisher
}

var _ IMQ = (*rabbitMQ)(nil)

// NewMQ connects to RabbitMQ using configuration and returns an IMQ publisher.
// It accepts either a full URI (MQ_CONNECTION_STRING, e.g.
// "amqp://user:pass@host:5672/vhost" or "amqps://...") or discrete
// MQ_HOST/MQ_PORT/MQ_USER/MQ_PASSWORD fields.
//
// The returned publisher survives a broker restart: it reopens the connection
// and its channels on the first publish after the old ones died, so a service
// does not have to be restarted along with its broker.
func NewMQ(env IENV, opts ...MQOption) (IMQ, IError) {
	cfg := &mqConfig{channels: DefaultMQChannels, timeout: DefaultMQPublishTimeout}
	for _, o := range opts {
		o(cfg)
	}

	p := &amqpPublisher{
		url:     mqURL(env.Config()),
		timeout: cfg.timeout,
		slots:   make(chan *mqChannel, cfg.channels),
		closed:  make(chan struct{}),
	}
	for i := 0; i < cfg.channels; i++ {
		p.slots <- nil
	}

	// fail fast, as the cache and the database do: a broker that cannot be
	// reached at boot is a configuration error worth finding at boot
	if err := p.dial(); err != nil {
		return nil, err
	}
	return &rabbitMQ{ctx: context.Background(), p: p}, nil
}

// mqURL returns the AMQP dial URL, preferring an explicit connection string.
func mqURL(cfg *ENVConfig) string {
	if cfg.MQConnectionString != "" {
		return cfg.MQConnectionString
	}
	return fmt.Sprintf("amqp://%s:%s@%s:%s/", cfg.MQUser, cfg.MQPassword, cfg.MQHost, cfg.MQPort)
}

func (m *rabbitMQ) WithContext(ctx context.Context) IMQ {
	cp := *m
	cp.ctx = ctx
	return &cp
}

func (m *rabbitMQ) Enabled() bool { return true }

// Publish sends msg to exchange with routing key and waits for the broker to
// confirm it. Non-[]byte payloads are JSON-encoded. The bound context bounds the
// publish.
//
// A nil error means the broker has taken responsibility for the message, not
// merely that it went out on the wire — that is what publisher confirms buy, and
// it is the difference between "sent" and "stored". It does not mean a queue
// received it: a message published to an exchange with no matching binding is
// confirmed and discarded.
func (m *rabbitMQ) Publish(exchange, key string, msg any) IError {
	return m.PublishWith(exchange, key, msg, PublishOptions{})
}

// PublishWith is Publish with the message properties spelled out.
func (m *rabbitMQ) PublishWith(exchange, key string, msg any, opts PublishOptions) IError {
	body, err := encodeMQBody(msg)
	if err != nil {
		return err
	}

	perr := m.p.publish(m.ctx, exchange, key, publishing(body, msg, opts), opts.Mandatory)
	if perr != nil {
		breadcrumbTo(m.ctx, Breadcrumb{
			Type: "error", Category: "mq.publish", Level: LevelError,
			Message: exchange + " " + key,
			Data:    map[string]any{"bytes": len(body), "error": perr.Error()},
		})
		return perr
	}
	breadcrumbTo(m.ctx, Breadcrumb{
		Category: "mq.publish",
		Message:  exchange + " " + key,
		Data:     map[string]any{"bytes": len(body)},
	})
	return nil
}

// Ping proves the broker is reachable by opening a channel on it, reconnecting
// first if the connection has dropped — so a readiness probe built on it reports
// the service healthy again once the broker is back.
func (m *rabbitMQ) Ping() IError {
	ctx, cancel := context.WithTimeout(m.ctx, m.p.timeout)
	defer cancel()

	c, err := m.p.acquire(ctx)
	if err != nil {
		return err
	}
	m.p.release(c, true)
	return nil
}

func (m *rabbitMQ) Close() IError { return m.p.close() }

// encodeMQBody renders a payload for the wire: bytes go as they are, everything
// else is JSON.
func encodeMQBody(msg any) ([]byte, IError) {
	if b, ok := msg.([]byte); ok {
		return b, nil
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, Wrap(err, "mq: marshal")
	}
	return b, nil
}

// publishing turns the options into AMQP properties. The defaults are the ones
// a service wants without asking: persistent, JSON, stamped with the time.
func publishing(body []byte, msg any, opts PublishOptions) amqp.Publishing {
	contentType := opts.ContentType
	if contentType == "" {
		contentType = "application/json"
		if _, raw := msg.([]byte); raw {
			// a caller that handed over bytes did its own encoding, and calling
			// that JSON is a claim this package cannot make
			contentType = "application/octet-stream"
		}
	}

	mode := amqp.Persistent
	if opts.Transient {
		mode = amqp.Transient
	}

	stamp := opts.Timestamp
	if stamp.IsZero() {
		stamp = time.Now()
	}

	out := amqp.Publishing{
		ContentType:   contentType,
		Body:          body,
		DeliveryMode:  mode,
		Timestamp:     stamp,
		Headers:       amqp.Table(opts.Headers),
		MessageId:     opts.MessageID,
		CorrelationId: opts.CorrelationID,
		ReplyTo:       opts.ReplyTo,
		Type:          opts.Type,
		AppId:         opts.AppID,
		UserId:        opts.UserID,
		Priority:      opts.Priority,
	}
	if opts.Expiration > 0 {
		// AMQP carries per-message TTL as a string of milliseconds
		out.Expiration = strconv.FormatInt(opts.Expiration.Milliseconds(), 10)
	}
	return out
}

// ---------------------------------------------------------------------------
// Connection and channel pool
// ---------------------------------------------------------------------------

// publish runs one publish on a pooled channel and waits for its confirmation.
//
// With mandatory set it also watches for a return: the broker sends one back
// *before* the ack when nothing was bound to receive it, so a message that was
// confirmed and discarded is reported as the failure it is.
func (p *amqpPublisher) publish(ctx context.Context, exchange, key string, pub amqp.Publishing, mandatory bool) IError {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	c, err := p.acquire(ctx)
	if err != nil {
		return err
	}

	var returns chan amqp.Return
	if mandatory {
		returns = make(chan amqp.Return, 1)
		c.ch.NotifyReturn(returns)
	}

	conf, perr := c.ch.PublishWithDeferredConfirmWithContext(ctx, exchange, key, mandatory, false, pub)
	if perr != nil {
		// the broker closes a channel on a protocol error, so this one is spent
		p.release(c, false)
		return Wrap(perr, "mq: publish")
	}

	acked, werr := conf.WaitContext(ctx)
	// a confirmation left outstanding belongs to a channel nobody should wait on
	// again: put a fresh one in its place rather than inherit the backlog.
	// A channel with a return listener is also spent — the listener is bound to
	// this call, and reusing the channel would deliver the next call's returns
	// to a closed one.
	p.release(c, werr == nil && !mandatory)

	if werr != nil {
		return Wrap(werr, "mq: await confirm")
	}
	if !acked {
		return New(502, "MQ_NACK", "mq: the broker refused the message").WithCause(ErrMQNack)
	}
	if mandatory {
		select {
		case ret := <-returns:
			return Newf(502, "MQ_NO_ROUTE",
				"mq: nothing is bound to %s for %q (%s)", exchange, key, ret.ReplyText).
				WithCause(ErrMQNoRoute)
		default:
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Topology
// ---------------------------------------------------------------------------

// DeclareExchange creates an exchange if it does not exist.
func (m *rabbitMQ) DeclareExchange(cfg ExchangeConfig) IError {
	if cfg.Name == "" {
		return New(400, "MQ_INVALID_TOPOLOGY", "mq: an exchange needs a name")
	}
	kind := cfg.Kind
	if kind == "" {
		kind = ExchangeDirect
	}
	return m.p.onChannel(m.ctx, func(ch *amqp.Channel) error {
		return ch.ExchangeDeclare(cfg.Name, kind, !cfg.Transient, cfg.AutoDelete,
			cfg.Internal, false, amqp.Table(cfg.Args))
	}, "mq: declare exchange")
}

// DeclareQueue creates a queue if it does not exist and reports its depth.
func (m *rabbitMQ) DeclareQueue(cfg QueueConfig) (QueueInfo, IError) {
	if cfg.Name == "" {
		return QueueInfo{}, New(400, "MQ_INVALID_TOPOLOGY", "mq: a queue needs a name")
	}

	var info QueueInfo
	err := m.p.onChannel(m.ctx, func(ch *amqp.Channel) error {
		q, derr := ch.QueueDeclare(cfg.Name, !cfg.Transient, cfg.AutoDelete,
			cfg.Exclusive, false, queueArgs(cfg))
		if derr != nil {
			return derr
		}
		info = QueueInfo{Name: q.Name, Messages: q.Messages, Consumers: q.Consumers}
		return nil
	}, "mq: declare queue")
	return info, err
}

// queueArgs turns the named settings into the x- arguments the broker expects,
// with the caller's own Args merged last so they can override.
func queueArgs(cfg QueueConfig) amqp.Table {
	args := amqp.Table{}
	if cfg.TTL > 0 {
		args["x-message-ttl"] = cfg.TTL.Milliseconds()
	}
	if cfg.MaxLength > 0 {
		args["x-max-length"] = cfg.MaxLength
	}
	if cfg.MaxBytes > 0 {
		args["x-max-length-bytes"] = cfg.MaxBytes
	}
	if cfg.MaxPriority > 0 {
		args["x-max-priority"] = int(cfg.MaxPriority)
	}
	if cfg.DeadLetterExchange != "" {
		args["x-dead-letter-exchange"] = cfg.DeadLetterExchange
	}
	if cfg.DeadLetterRoutingKey != "" {
		args["x-dead-letter-routing-key"] = cfg.DeadLetterRoutingKey
	}
	if cfg.Quorum {
		args["x-queue-type"] = "quorum"
	}
	for k, v := range cfg.Args {
		args[k] = v
	}
	if len(args) == 0 {
		return nil
	}
	return args
}

// BindQueue routes messages from an exchange into a queue.
func (m *rabbitMQ) BindQueue(queue, exchange, key string, args ...map[string]any) IError {
	return m.p.onChannel(m.ctx, func(ch *amqp.Channel) error {
		return ch.QueueBind(queue, key, exchange, false, tableOf(args))
	}, "mq: bind queue")
}

// UnbindQueue removes a binding.
func (m *rabbitMQ) UnbindQueue(queue, exchange, key string, args ...map[string]any) IError {
	return m.p.onChannel(m.ctx, func(ch *amqp.Channel) error {
		return ch.QueueUnbind(queue, key, exchange, tableOf(args))
	}, "mq: unbind queue")
}

// DeleteQueue removes a queue and everything in it.
func (m *rabbitMQ) DeleteQueue(name string) IError {
	return m.p.onChannel(m.ctx, func(ch *amqp.Channel) error {
		_, err := ch.QueueDelete(name, false, false, false)
		return err
	}, "mq: delete queue")
}

// PurgeQueue empties a queue and reports how many messages it dropped.
func (m *rabbitMQ) PurgeQueue(name string) (int, IError) {
	var n int
	err := m.p.onChannel(m.ctx, func(ch *amqp.Channel) error {
		purged, perr := ch.QueuePurge(name, false)
		n = purged
		return perr
	}, "mq: purge queue")
	return n, err
}

// QueueInfo reports a queue's depth without changing anything: it uses a passive
// declare, which fails rather than creating a queue that is not there.
func (m *rabbitMQ) QueueInfo(name string) (QueueInfo, IError) {
	var info QueueInfo
	err := m.p.onChannel(m.ctx, func(ch *amqp.Channel) error {
		q, derr := ch.QueueDeclarePassive(name, true, false, false, false, nil)
		if derr != nil {
			return derr
		}
		info = QueueInfo{Name: q.Name, Messages: q.Messages, Consumers: q.Consumers}
		return nil
	}, "mq: queue info")
	return info, err
}

func tableOf(args []map[string]any) amqp.Table {
	if len(args) == 0 || len(args[0]) == 0 {
		return nil
	}
	return amqp.Table(args[0])
}

// onChannel runs a topology operation on a pooled channel.
//
// A failed declare closes the channel — the broker's way of reporting a
// mismatch — so the channel is never returned to the pool on error. That is why
// topology goes through here rather than through publish.
func (p *amqpPublisher) onChannel(ctx context.Context, fn func(*amqp.Channel) error, what string) IError {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	c, err := p.acquire(ctx)
	if err != nil {
		return err
	}

	opErr := fn(c.ch)
	p.release(c, opErr == nil)
	if opErr != nil {
		return Wrap(opErr, what)
	}
	return nil
}

// acquire takes a slot and returns a channel ready to publish on, opening or
// reopening one as needed. It blocks while every slot is in use, which is what
// bounds concurrency: without it a burst of requests would each open a channel
// and walk into the broker's channel_max.
func (p *amqpPublisher) acquire(ctx context.Context) (*mqChannel, IError) {
	var slot *mqChannel
	select {
	case slot = <-p.slots:
	case <-p.closed:
		return nil, mqClosed()
	case <-ctx.Done():
		return nil, Wrap(ctx.Err(), "mq: waiting for a free channel")
	}

	if slot.usable(p.currentConn()) {
		return slot, nil
	}
	if slot != nil {
		_ = slot.ch.Close()
	}

	c, err := p.open()
	if err != nil {
		// hand the slot back empty, or the pool loses a slot on every failure and
		// a broker outage ends with a publisher that can never publish again
		p.putSlot(nil)
		return nil, err
	}
	return c, nil
}

// release returns the slot to the pool. A channel that failed is not reused:
// after a protocol error the broker has closed it, and the next publish would
// fail for a reason that has nothing to do with its own message.
func (p *amqpPublisher) release(c *mqChannel, reusable bool) {
	if c != nil && (!reusable || !c.usable(p.currentConn())) {
		_ = c.ch.Close()
		c = nil
	}
	p.putSlot(c)
}

// putSlot returns a slot without blocking. The send always fits — every caller
// took a slot first — but Close drains the pool, and a publish that finishes
// after it must not block forever on a pool nobody is reading.
func (p *amqpPublisher) putSlot(c *mqChannel) {
	select {
	case p.slots <- c:
	default:
		if c != nil {
			_ = c.ch.Close()
		}
	}
}

// open dials if necessary and puts a fresh channel into confirm mode. It tries
// twice: the connection can die between the check and the Channel call, and a
// caller should not be told its message failed because of that.
func (p *amqpPublisher) open() (*mqChannel, IError) {
	var last IError
	for attempt := 0; attempt < 2; attempt++ {
		if err := p.dial(); err != nil {
			last = err
			continue
		}
		conn := p.currentConn()
		if conn == nil {
			last = mqClosed()
			continue
		}
		ch, err := conn.Channel()
		if err != nil {
			last = Wrap(err, "mq: channel")
			p.discard(conn)
			continue
		}
		// confirm mode is what makes a nil error from Publish mean the broker has
		// the message, rather than only that it left this process
		if cerr := ch.Confirm(false); cerr != nil {
			_ = ch.Close()
			last = Wrap(cerr, "mq: confirm mode")
			p.discard(conn)
			continue
		}
		return &mqChannel{ch: ch, conn: conn}, nil
	}
	if last == nil {
		last = New(503, "MQ_UNAVAILABLE", "mq: cannot open a channel")
	}
	return nil, last
}

// dial opens a connection, replacing a dead one. Concurrent callers collapse
// into one attempt: whoever gets the lock second finds the connection already
// live and returns it.
func (p *amqpPublisher) dial() IError {
	p.dialMu.Lock()
	defer p.dialMu.Unlock()

	if p.isClosed() {
		return mqClosed()
	}
	if p.conn != nil && !p.conn.IsClosed() {
		return nil
	}
	conn, err := amqp.Dial(p.url)
	if err != nil {
		return Wrap(err, "mq: dial")
	}
	p.conn = conn
	return nil
}

// discard drops a connection that has proved unusable, but only if it is still
// the current one — another goroutine may already have replaced it.
func (p *amqpPublisher) discard(conn *amqp.Connection) {
	p.dialMu.Lock()
	defer p.dialMu.Unlock()

	if p.conn == conn {
		_ = p.conn.Close()
		p.conn = nil
	}
}

func (p *amqpPublisher) currentConn() *amqp.Connection {
	p.dialMu.Lock()
	defer p.dialMu.Unlock()
	return p.conn
}

func (p *amqpPublisher) isClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

// close stops the publisher and releases the connection. Safe to call twice, so
// App.Shutdown and a service that closes its own publisher can coexist.
func (p *amqpPublisher) close() IError {
	var ierr IError
	p.closeOnce.Do(func() {
		close(p.closed)
		// take the idle channels out of the pool so nothing starts a publish on
		// one we are about to kill. Slots checked out right now are not waited
		// for: their publish fails against a closed connection, which is the
		// honest answer during a shutdown.
	drain:
		for i := 0; i < cap(p.slots); i++ {
			select {
			case c := <-p.slots:
				if c != nil {
					_ = c.ch.Close()
				}
			default:
				break drain
			}
		}

		p.dialMu.Lock()
		defer p.dialMu.Unlock()
		if p.conn != nil && !p.conn.IsClosed() {
			if err := p.conn.Close(); err != nil {
				ierr = Wrap(err, "mq: close")
			}
		}
		p.conn = nil
	})
	return ierr
}

// usable reports whether a pooled channel can still be published on: it must
// belong to the connection in use, and neither may have been closed underneath
// it (a broker restart closes both, a protocol error only the channel).
func (c *mqChannel) usable(current *amqp.Connection) bool {
	return c != nil && current != nil && c.conn == current &&
		!current.IsClosed() && !c.ch.IsClosed()
}

func mqClosed() *Error {
	return &Error{
		Status:  503,
		Code:    "MQ_CLOSED",
		Message: "mq: the publisher is closed",
		cause:   ErrMQClosed,
	}
}
