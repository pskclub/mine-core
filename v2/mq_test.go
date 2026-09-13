package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A service with no MQ_* configuration must still be able to run the code paths
// that publish: they fail with an error naming the missing configuration rather
// than panicking on a nil interface.
func TestMQ_disabledPublisherFailsLoudly(t *testing.T) {
	mq := NewNoopMQ()

	assert.False(t, mq.Enabled())

	err := mq.Publish("orders", "order.created", map[string]string{"id": "o1"})
	require.Error(t, err)
	assert.Equal(t, "MQ_DISABLED", err.GetCode())
	assert.Equal(t, 503, err.GetStatus())
	assert.ErrorIs(t, err, ErrMQDisabled)

	assert.ErrorIs(t, mq.Ping(), ErrMQDisabled)
	assert.NoError(t, mq.Close(), "closing a publisher that owns nothing is not an error")
	assert.ErrorIs(t, mq.WithContext(context.Background()).Publish("x", "y", "z"), ErrMQDisabled,
		"binding a context does not turn a disabled publisher into a real one")
}

func TestMQ_contextMQIsNeverNil(t *testing.T) {
	app := newTestApp(t) // built without WithMQ
	ctx := app.NewContext(context.Background())

	require.NotNil(t, ctx.MQ(), "ctx.MQ() must never be nil")
	assert.False(t, ctx.MQ().Enabled())
	assert.ErrorIs(t, ctx.MQ().Publish("x", "y", "z"), ErrMQDisabled)

	require.NotNil(t, app.MQ())
	assert.False(t, app.MQ().Enabled())
}

// newTestPublisher builds a publisher with a pool but no broker: enough to
// exercise the pool's own accounting, which is what a broker restart depends on.
func newTestPublisher(slots int) *amqpPublisher {
	p := &amqpPublisher{
		url:     "amqp://guest:guest@127.0.0.1:1/",
		timeout: 50 * time.Millisecond,
		slots:   make(chan *mqChannel, slots),
		closed:  make(chan struct{}),
	}
	for i := 0; i < slots; i++ {
		p.slots <- nil
	}
	return p
}

// A broker that is down must not cost the publisher its pool: if a failed
// acquire kept the slot, an outage would end with a publisher that can never
// publish again even after the broker returns.
func TestMQ_failedAcquireReturnsItsSlot(t *testing.T) {
	p := newTestPublisher(2)

	for i := 0; i < 5; i++ {
		_, err := p.acquire(context.Background())
		require.Error(t, err, "no broker is listening")
	}

	assert.Equal(t, 2, len(p.slots), "every failed acquire must give its slot back")
}

// The pool is also the concurrency limit. With every slot checked out, a caller
// waits — and gives up when its own context does, rather than blocking a request
// forever behind a slow broker.
func TestMQ_acquireWaitsForASlotAndRespectsTheContext(t *testing.T) {
	p := newTestPublisher(1)
	<-p.slots // hold the only slot

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := p.acquire(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.GreaterOrEqual(t, time.Since(start), 15*time.Millisecond, "it should have waited")
}

func TestMQ_publishAfterCloseIsAnError(t *testing.T) {
	p := newTestPublisher(1)
	require.NoError(t, p.close())
	assert.NoError(t, p.close(), "close is idempotent")

	err := p.publish(context.Background(), "orders", "order.created", amqp.Publishing{}, false)
	require.Error(t, err)
	assert.Equal(t, "MQ_CLOSED", err.GetCode())
	assert.ErrorIs(t, err, ErrMQClosed)
}

// A channel is only reusable while the connection it was opened on is still the
// one in use — a broker restart replaces the connection, and a channel from the
// old one would fail every publish made on it.
func TestMQ_staleChannelIsNotUsable(t *testing.T) {
	var nilChannel *mqChannel
	assert.False(t, nilChannel.usable(nil), "an empty slot is never usable")

	conn := &amqp.Connection{}
	other := &amqp.Connection{}
	c := &mqChannel{ch: &amqp.Channel{}, conn: conn}

	assert.False(t, c.usable(other), "a channel from a replaced connection is stale")
	assert.False(t, c.usable(nil), "no connection means nothing to publish on")
}

func TestMQ_optionsAreApplied(t *testing.T) {
	cfg := &mqConfig{channels: DefaultMQChannels, timeout: DefaultMQPublishTimeout}
	for _, o := range []MQOption{
		WithMQChannels(3),
		WithMQPublishTimeout(2 * time.Second),
		WithMQChannels(0),        // ignored
		WithMQPublishTimeout(-1), // ignored
	} {
		o(cfg)
	}

	assert.Equal(t, 3, cfg.channels)
	assert.Equal(t, 2*time.Second, cfg.timeout)
}

func TestMQ_nackIsDistinguishable(t *testing.T) {
	err := New(502, "MQ_NACK", "mq: the broker refused the message").WithCause(ErrMQNack)
	assert.True(t, errors.Is(err, ErrMQNack), "a caller must be able to tell a refusal from a transport failure")
}

func TestMQ_publishOptionsBecomeProperties(t *testing.T) {
	at := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)

	p := publishing([]byte(`{"id":1}`), map[string]int{"id": 1}, PublishOptions{
		Headers:       map[string]any{"trace": "abc"},
		MessageID:     "m-1",
		CorrelationID: "c-1",
		ReplyTo:       "replies",
		Type:          "order.created",
		AppID:         "orders",
		Priority:      5,
		Expiration:    90 * time.Second,
		Timestamp:     at,
	})

	assert.Equal(t, "application/json", p.ContentType)
	assert.Equal(t, amqp.Persistent, p.DeliveryMode, "durable unless asked otherwise")
	assert.Equal(t, "abc", p.Headers["trace"])
	assert.Equal(t, "m-1", p.MessageId)
	assert.Equal(t, "c-1", p.CorrelationId)
	assert.Equal(t, "replies", p.ReplyTo)
	assert.Equal(t, "order.created", p.Type)
	assert.Equal(t, "orders", p.AppId)
	assert.Equal(t, uint8(5), p.Priority)
	assert.Equal(t, "90000", p.Expiration, "AMQP carries per-message TTL as milliseconds")
	assert.Equal(t, at, p.Timestamp)
}

// Bytes handed over already encoded must not be labelled JSON — that is a claim
// this package cannot make on the caller's behalf.
func TestMQ_rawBytesAreNotCalledJSON(t *testing.T) {
	raw := []byte{0x01, 0x02}
	assert.Equal(t, "application/octet-stream", publishing(raw, raw, PublishOptions{}).ContentType)
	assert.Equal(t, "text/csv",
		publishing(raw, raw, PublishOptions{ContentType: "text/csv"}).ContentType)
}

func TestMQ_transientAndDefaultTimestamp(t *testing.T) {
	p := publishing([]byte("x"), "x", PublishOptions{Transient: true})
	assert.Equal(t, amqp.Transient, p.DeliveryMode)
	assert.False(t, p.Timestamp.IsZero(), "an unset timestamp defaults to now")
}

func TestMQ_queueArgsFromNamedSettings(t *testing.T) {
	args := queueArgs(QueueConfig{
		Name:                 "orders",
		TTL:                  time.Minute,
		MaxLength:            1000,
		MaxBytes:             1 << 20,
		MaxPriority:          10,
		DeadLetterExchange:   "orders.dlx",
		DeadLetterRoutingKey: "failed",
		Quorum:               true,
		Args:                 map[string]any{"x-custom": "v"},
	})

	assert.Equal(t, int64(60000), args["x-message-ttl"])
	assert.Equal(t, int64(1000), args["x-max-length"])
	assert.Equal(t, int64(1<<20), args["x-max-length-bytes"])
	assert.Equal(t, 10, args["x-max-priority"])
	assert.Equal(t, "orders.dlx", args["x-dead-letter-exchange"])
	assert.Equal(t, "failed", args["x-dead-letter-routing-key"])
	assert.Equal(t, "quorum", args["x-queue-type"])
	assert.Equal(t, "v", args["x-custom"])

	assert.Nil(t, queueArgs(QueueConfig{Name: "plain"}), "a plain queue takes no arguments")
}

// A caller's own argument wins, so a setting this package has not named is still
// reachable.
func TestMQ_queueArgsCallerOverrides(t *testing.T) {
	args := queueArgs(QueueConfig{
		Quorum: true,
		Args:   map[string]any{"x-queue-type": "stream"},
	})
	assert.Equal(t, "stream", args["x-queue-type"])
}

// Requeueing a permanent failure is the classic way an AMQP consumer melts down,
// so a plain error must not do it — the handler has to ask.
func TestMQ_requeueIsOptIn(t *testing.T) {
	plain := errors.New("row not found")
	assert.False(t, shouldRequeue(plain))

	assert.True(t, shouldRequeue(Requeue(plain)))
	assert.True(t, shouldRequeue(fmt.Errorf("wrapped: %w", Requeue(plain))),
		"the request survives being wrapped on the way up")

	assert.ErrorIs(t, Requeue(plain), plain, "the original error is still readable")
	assert.Nil(t, Requeue(nil))
}

func TestMQ_deliveryDecoding(t *testing.T) {
	d := &Delivery{
		Body:    []byte(`{"id":"o-1","total":42}`),
		Headers: map[string]any{"trace": "abc", "attempt": 2},
	}

	order, err := BindDelivery[struct {
		ID    string `json:"id"`
		Total int    `json:"total"`
	}](d)
	require.NoError(t, err)
	assert.Equal(t, "o-1", order.ID)
	assert.Equal(t, 42, order.Total)

	assert.Equal(t, "abc", d.HeaderString("trace"))
	assert.Empty(t, d.HeaderString("attempt"), "a non-string header reads as empty")
	assert.Empty(t, d.HeaderString("missing"))

	v, ok := d.Header("attempt")
	assert.True(t, ok)
	assert.Equal(t, 2, v)
}

func TestMQ_consumerNeedsHandlers(t *testing.T) {
	app := newTestApp(t)
	c := app.NewMQConsumer()

	err := c.Start()
	require.Error(t, err)
	assert.Equal(t, "MQ_CONSUMER_EMPTY", err.GetCode())
	assert.False(t, c.Running())
}

// A consumer that cannot reach the broker must fail Start, not retry quietly in
// the background while the caller believes it is consuming.
func TestMQ_consumerStartFailsWhenTheBrokerIsDown(t *testing.T) {
	env := mustEnv(t, map[string]string{
		"ENV": "test", "MQ_CONNECTION_STRING": "amqp://guest:guest@127.0.0.1:1/",
	})
	app, err := NewApp(env)
	require.NoError(t, err)

	c := app.NewMQConsumer()
	c.On("orders", func(IMQContext, *Delivery) error { return nil })

	require.Error(t, c.Start())
	assert.False(t, c.Running())
	assert.NoError(t, c.Stop(t.Context()), "stopping one that never started is a no-op")
}

func TestMQ_consumerTagNamesTheQueue(t *testing.T) {
	c := &mqConsumer{opts: mqConsumerOptions{tag: "orders-svc"}}
	assert.Equal(t, "orders-svc-orders", c.consumerTag("orders"))

	assert.Empty(t, (&mqConsumer{}).consumerTag("orders"),
		"with no tag the broker picks one")
}

func TestMQ_disabledPublisherRefusesTopologyToo(t *testing.T) {
	mq := NewNoopMQ()

	assert.ErrorIs(t, mq.DeclareExchange(ExchangeConfig{Name: "x"}), ErrMQDisabled)
	_, err := mq.DeclareQueue(QueueConfig{Name: "q"})
	assert.ErrorIs(t, err, ErrMQDisabled)
	assert.ErrorIs(t, mq.BindQueue("q", "x", "k"), ErrMQDisabled)
	assert.ErrorIs(t, mq.UnbindQueue("q", "x", "k"), ErrMQDisabled)
	assert.ErrorIs(t, mq.DeleteQueue("q"), ErrMQDisabled)
	_, err = mq.PurgeQueue("q")
	assert.ErrorIs(t, err, ErrMQDisabled)
	_, err = mq.QueueInfo("q")
	assert.ErrorIs(t, err, ErrMQDisabled)
	assert.ErrorIs(t, mq.PublishWith("x", "k", "v", PublishOptions{}), ErrMQDisabled)
}
