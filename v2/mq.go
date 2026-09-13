package core

import (
	"context"
	"errors"
	"time"
)

// ErrMQDisabled is wrapped by the error every operation returns when no MQ_*
// configuration is set.
//
// The queue does not degrade the way the cache does. A cache miss is
// recoverable — the value is recomputed — but a message that is quietly dropped
// is work the caller believes it handed off and nobody will ever pick up, so a
// service with no broker fails loudly instead. This is the same trade-off
// storage makes.
var ErrMQDisabled = errors.New("mq: not configured")

// ErrMQClosed is wrapped by the error a publish returns after the publisher has
// been closed — normally because the App is shutting down.
var ErrMQClosed = errors.New("mq: closed")

// ErrMQNack is wrapped by the error a publish returns when the broker accepted
// the message and then refused it. It means the message was *not* stored, and
// the caller has to decide what to do about that.
var ErrMQNack = errors.New("mq: nacked by broker")

// Exchange kinds.
const (
	ExchangeDirect  = "direct"
	ExchangeFanout  = "fanout"
	ExchangeTopic   = "topic"
	ExchangeHeaders = "headers"
)

// PublishOptions are the per-message properties AMQP carries beside the body.
// The zero value is what Publish sends: persistent, JSON, timestamped.
type PublishOptions struct {
	// ContentType defaults to application/json (text/plain for a []byte body).
	ContentType string
	// Headers travel with the message. A headers exchange routes on them, and a
	// consumer reads them for tracing and versioning.
	Headers map[string]any
	// MessageID, CorrelationID and ReplyTo are the request/reply and
	// deduplication fields. Set MessageID to something stable when the consumer
	// deduplicates.
	MessageID     string
	CorrelationID string
	ReplyTo       string
	// Type names the event ("order.created"), for a consumer that reads several
	// kinds off one queue.
	Type string
	// AppID and UserID identify the sender. UserID is verified by the broker
	// against the connection's login, so set it only when they match.
	AppID  string
	UserID string
	// Priority is 0-9, and only means anything on a queue declared with
	// x-max-priority.
	Priority uint8
	// Expiration discards the message if it has not been delivered in time.
	// This is per-message TTL; the queue may have its own, and the shorter wins.
	Expiration time.Duration
	// Transient stores the message in memory only. It is lost when the broker
	// restarts — for a stream of updates where the next one supersedes this one.
	Transient bool
	// Mandatory returns the message instead of discarding it when no queue is
	// bound to match it, turning a silent misroute into an error the publisher
	// sees. It costs a round trip.
	Mandatory bool
	// Timestamp defaults to now.
	Timestamp time.Time
}

// ExchangeConfig declares an exchange.
type ExchangeConfig struct {
	Name string
	// Kind is direct, fanout, topic or headers (default direct).
	Kind string
	// Durable survives a broker restart. On by default — the zero value here is
	// durable, because a non-durable exchange is almost never what is wanted.
	Transient  bool
	AutoDelete bool
	Internal   bool
	Args       map[string]any
}

// QueueConfig declares a queue.
type QueueConfig struct {
	Name string
	// Transient makes the queue not survive a broker restart.
	Transient bool
	// AutoDelete removes the queue when its last consumer disconnects.
	AutoDelete bool
	// Exclusive limits the queue to this connection and deletes it on close.
	Exclusive bool
	// TTL discards a message that has waited this long.
	TTL time.Duration
	// MaxLength and MaxBytes bound the queue; past them the oldest messages are
	// dropped (or dead-lettered).
	MaxLength int64
	MaxBytes  int64
	// MaxPriority turns this into a priority queue (1-255).
	MaxPriority uint8
	// DeadLetterExchange is where a rejected, expired or overflowing message
	// goes. Declare one for any queue whose failures matter: without it a
	// message a consumer rejects is simply gone.
	DeadLetterExchange string
	// DeadLetterRoutingKey overrides the routing key used when dead-lettering.
	DeadLetterRoutingKey string
	// Quorum makes this a quorum queue — replicated across the cluster, at the
	// cost of throughput. For work that must survive losing a node.
	Quorum bool
	// Args are extra arguments, merged after the fields above.
	Args map[string]any
}

// QueueInfo is what the broker reports about a declared queue.
type QueueInfo struct {
	Name      string
	Messages  int
	Consumers int
}

// IMQ is the message-queue publisher (same name as v1). Publish uses the bound
// context from ctx.MQ(); the payload is JSON-encoded unless it is []byte.
//
// ctx.MQ() is never nil: a service with no MQ_* configuration gets a disabled
// publisher whose every call fails with MQ_DISABLED, naming the missing
// configuration, rather than panicking on a nil interface.
type IMQ interface {
	// Publish sends msg and waits for the broker to confirm it. A nil error
	// means the broker has taken responsibility for the message.
	Publish(exchange, key string, msg any) IError
	// PublishWith is Publish with the message properties spelled out.
	PublishWith(exchange, key string, msg any, opts PublishOptions) IError

	// DeclareExchange creates an exchange if it does not exist. Declaring one
	// that exists with different settings is an error from the broker, which is
	// the point: it catches a topology change nobody applied.
	DeclareExchange(cfg ExchangeConfig) IError
	// DeclareQueue creates a queue if it does not exist and reports its depth.
	DeclareQueue(cfg QueueConfig) (QueueInfo, IError)
	// BindQueue routes messages from an exchange into a queue.
	BindQueue(queue, exchange, key string, args ...map[string]any) IError
	// UnbindQueue removes a binding.
	UnbindQueue(queue, exchange, key string, args ...map[string]any) IError
	// DeleteQueue removes a queue and everything in it.
	DeleteQueue(name string) IError
	// PurgeQueue empties a queue and reports how many messages it dropped.
	PurgeQueue(name string) (int, IError)
	// QueueInfo reports a queue's depth without declaring anything. It fails if
	// the queue does not exist.
	QueueInfo(name string) (QueueInfo, IError)

	// Ping reports whether the broker is reachable — for a readiness probe. It
	// reconnects if the connection has dropped, so a probe built on it recovers
	// on its own.
	Ping() IError
	// Enabled reports whether this is a real publisher. False for the disabled
	// publisher a service with no MQ_* configuration gets.
	Enabled() bool
	// WithContext returns a handle bound to a different context (escape hatch
	// for work that must outlive the request).
	WithContext(ctx context.Context) IMQ
	// Close releases the connection. Owned by App.Shutdown; call it yourself
	// only for a publisher built outside an App.
	Close() IError
}

// PublishAs is a typed convenience over IMQ.Publish.
func PublishAs[T any](mq IMQ, exchange, key string, msg T) IError {
	return mq.Publish(exchange, key, msg)
}

// ---------------------------------------------------------------------------
// Deliveries
// ---------------------------------------------------------------------------

// Delivery is one message a consumer received.
type Delivery struct {
	Body        []byte
	Exchange    string
	RoutingKey  string
	Queue       string
	ConsumerTag string

	Headers       map[string]any
	ContentType   string
	MessageID     string
	CorrelationID string
	ReplyTo       string
	Type          string
	AppID         string
	UserID        string
	Priority      uint8
	Timestamp     time.Time

	// Redelivered is set when the broker has handed this message to a consumer
	// before. It is the only hint a handler gets that it may be running twice,
	// and the reason a handler should be idempotent.
	Redelivered bool
	DeliveryTag uint64
}

// Bind decodes the body into dest, following the same rules as ICache.Get:
// *string, *[]byte and *json.RawMessage take the raw bytes, anything else is
// JSON-decoded.
func (d *Delivery) Bind(dest any) IError {
	return decodeCacheValue(d.Body, dest)
}

// Header reads one header.
func (d *Delivery) Header(name string) (any, bool) {
	v, ok := d.Headers[name]
	return v, ok
}

// HeaderString reads one header as a string, or "" when it is absent or is
// something else.
func (d *Delivery) HeaderString(name string) string {
	if s, ok := d.Headers[name].(string); ok {
		return s
	}
	return ""
}

// BindDelivery decodes a delivery's body into a T.
//
//	order, err := core.BindDelivery[Order](d)
func BindDelivery[T any](d *Delivery) (T, IError) {
	var out T
	if err := d.Bind(&out); err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Handler outcomes
// ---------------------------------------------------------------------------

// requeueError asks for a delivery to be redelivered rather than dead-lettered.
type requeueError struct{ err error }

func (e requeueError) Error() string { return e.err.Error() }
func (e requeueError) Unwrap() error { return e.err }

// Requeue wraps a handler's error to ask the broker to deliver the message
// again, instead of dead-lettering it.
//
// Use it for a failure that is about *now* — a database that is down, a rate
// limit — and not for one that will fail again in the same way. A message
// requeued for a permanent failure comes straight back, and the loop it forms
// is the most common way an AMQP consumer melts down. Reach for a dead-letter
// queue instead.
func Requeue(err error) error {
	if err == nil {
		return nil
	}
	return requeueError{err: err}
}

// shouldRequeue reports whether a handler asked for redelivery.
func shouldRequeue(err error) bool {
	var r requeueError
	return errors.As(err, &r)
}

// ErrMQNoRoute is wrapped by the error a mandatory publish returns when the
// broker could route the message nowhere.
var ErrMQNoRoute = errors.New("mq: no queue is bound for the routing key")

// ---------------------------------------------------------------------------
// Disabled publisher
// ---------------------------------------------------------------------------

// noopMQ is what ctx.MQ() returns when no broker is configured. Every call fails
// with the same error, which names the missing configuration.
type noopMQ struct{}

var _ IMQ = noopMQ{}

// NewNoopMQ returns a publisher that refuses every operation. It is what a
// service with no MQ_* configuration gets, so the failure is a clear error
// instead of a nil dereference.
func NewNoopMQ() IMQ { return noopMQ{} }

func (noopMQ) Publish(string, string, any) IError { return mqDisabled() }
func (noopMQ) Ping() IError                       { return mqDisabled() }
func (noopMQ) Enabled() bool                      { return false }
func (n noopMQ) WithContext(context.Context) IMQ  { return n }
func (noopMQ) Close() IError                      { return nil }

func (noopMQ) PublishWith(string, string, any, PublishOptions) IError { return mqDisabled() }
func (noopMQ) DeclareExchange(ExchangeConfig) IError                  { return mqDisabled() }
func (noopMQ) DeleteQueue(string) IError                              { return mqDisabled() }

func (noopMQ) DeclareQueue(QueueConfig) (QueueInfo, IError) {
	return QueueInfo{}, mqDisabled()
}
func (noopMQ) BindQueue(string, string, string, ...map[string]any) IError {
	return mqDisabled()
}
func (noopMQ) UnbindQueue(string, string, string, ...map[string]any) IError {
	return mqDisabled()
}
func (noopMQ) PurgeQueue(string) (int, IError)      { return 0, mqDisabled() }
func (noopMQ) QueueInfo(string) (QueueInfo, IError) { return QueueInfo{}, mqDisabled() }

func mqDisabled() *Error {
	return &Error{
		Status:  503,
		Code:    "MQ_DISABLED",
		Message: "mq: no broker is configured (set MQ_CONNECTION_STRING or MQ_HOST)",
		cause:   ErrMQDisabled,
	}
}
