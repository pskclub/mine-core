package core

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type orderEvent struct {
	ID string `json:"id"`
}

func newMemoryMQApp(t *testing.T) (*App, *MemoryMQ) {
	t.Helper()
	mq := NewMemoryMQ()
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "mq-memory"})
	app, err := NewApp(env, WithMQ(mq))
	require.Nil(t, err)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	return app, mq
}

func TestTopicMatches(t *testing.T) {
	cases := []struct {
		pattern, key string
		want         bool
	}{
		{"order.created", "order.created", true},
		{"order.*", "order.created", true},
		{"order.*", "order.created.v2", false},
		{"order.#", "order", true},
		{"order.#", "order.created.v2", true},
		{"#", "anything.at.all", true},
		{"*.created", "order.created", true},
		{"*.created", "created", false},
		{"#.created", "a.b.created", true},
		{"order.*.v2", "order.created.v2", true},
		{"order.*.v2", "order.v2", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, topicMatches(c.pattern, c.key), "%s vs %s", c.pattern, c.key)
	}
}

func TestMemoryMQ_routesLikeABroker(t *testing.T) {
	mq := NewMemoryMQ()
	require.Nil(t, mq.DeclareExchange(ExchangeConfig{Name: "events", Kind: ExchangeTopic}))
	require.Nil(t, mq.DeclareExchange(ExchangeConfig{Name: "broadcast", Kind: ExchangeFanout}))
	require.Nil(t, mq.DeclareExchange(ExchangeConfig{Name: "tagged", Kind: ExchangeHeaders}))
	for _, q := range []string{"orders", "audit", "all-a", "all-b", "eu"} {
		_, err := mq.DeclareQueue(QueueConfig{Name: q})
		require.Nil(t, err)
	}
	require.Nil(t, mq.BindQueue("orders", "events", "order.*"))
	require.Nil(t, mq.BindQueue("audit", "events", "#"))
	require.Nil(t, mq.BindQueue("all-a", "broadcast", ""))
	require.Nil(t, mq.BindQueue("all-b", "broadcast", ""))
	require.Nil(t, mq.BindQueue("eu", "tagged", "", map[string]any{"region": "eu", "x-match": "all"}))

	require.Nil(t, mq.Publish("events", "order.created", orderEvent{ID: "o1"}))
	require.Nil(t, mq.Publish("events", "user.signed_up", orderEvent{ID: "u1"}))
	require.Nil(t, mq.Publish("broadcast", "ignored", "hello"))
	require.Nil(t, mq.PublishWith("tagged", "", "eu-only", PublishOptions{Headers: map[string]any{"region": "eu"}}))
	require.Nil(t, mq.PublishWith("tagged", "", "us-only", PublishOptions{Headers: map[string]any{"region": "us"}}))

	assert.Len(t, mq.Messages("orders"), 1, "topic: order.* takes order.created only")
	assert.Len(t, mq.Messages("audit"), 2, "topic: # takes everything")
	assert.Len(t, mq.Messages("all-a"), 1)
	assert.Len(t, mq.Messages("all-b"), 1)
	assert.Len(t, mq.Messages("eu"), 1, "headers: only the matching region")

	got, err := BindDelivery[orderEvent](mq.Messages("orders")[0])
	require.Nil(t, err)
	assert.Equal(t, "o1", got.ID)
	assert.Equal(t, "application/json", mq.Messages("orders")[0].ContentType)

	published := mq.Published()
	require.Len(t, published, 5)
	assert.Equal(t, []string{"audit", "orders"}, published[0].Queues)
	assert.Empty(t, published[4].Queues, "the us message reached nothing, and the record says so")

	// the default exchange names the queue directly, as on the broker
	require.Nil(t, mq.Publish("", "orders", orderEvent{ID: "direct"}))
	assert.Len(t, mq.Messages("orders"), 2)
}

func TestMemoryMQ_mandatoryWithNoRouteIsAnError(t *testing.T) {
	mq := NewMemoryMQ()
	require.Nil(t, mq.DeclareExchange(ExchangeConfig{Name: "events"}))

	err := mq.PublishWith("events", "nobody.listens", "x", PublishOptions{Mandatory: true})
	require.NotNil(t, err)
	assert.ErrorIs(t, err, ErrMQNoRoute)
	assert.Len(t, mq.Published(), 1, "recorded all the same: the test can still see the attempt")

	undeclared := mq.Publish("missing", "k", "x")
	require.NotNil(t, undeclared)
	assert.Equal(t, "MQ_NO_EXCHANGE", undeclared.GetCode())
}

func TestMemoryMQ_closeRefusesPublishes(t *testing.T) {
	mq := NewMemoryMQ()
	require.Nil(t, mq.Close())
	err := mq.Publish("", "q", "x")
	require.NotNil(t, err)
	assert.ErrorIs(t, err, ErrMQClosed)
}

func TestMemoryMQ_consumerAcksRequeuesAndDeadLetters(t *testing.T) {
	app, mq := newMemoryMQApp(t)

	var (
		mu       sync.Mutex
		handled  []string
		attempts atomic.Int32
	)
	c := app.NewMQConsumer(WithMQHandlerTimeout(time.Second))
	c.OnQueue(ConsumeQueue{
		Queue:       QueueConfig{Name: "orders", DeadLetterExchange: "dlx"},
		Exchange:    &ExchangeConfig{Name: "events", Kind: ExchangeTopic},
		BindingKeys: []string{"order.*"},
	}, func(ctx IMQContext, d *Delivery) error {
		ev, err := BindDelivery[orderEvent](d)
		if err != nil {
			return err // malformed: dead-letter
		}
		switch ev.ID {
		case "flaky":
			if attempts.Add(1) == 1 {
				return Requeue(errors.New("database is down"))
			}
		case "broken":
			return errors.New("permanent")
		}
		mu.Lock()
		handled = append(handled, ev.ID+redeliveredMark(d))
		mu.Unlock()
		return nil
	})
	// the dead-letter side, declared the way a service would
	c.OnQueue(ConsumeQueue{
		Queue:    QueueConfig{Name: "orders.dead"},
		Exchange: &ExchangeConfig{Name: "dlx", Kind: ExchangeFanout},
	}, func(ctx IMQContext, d *Delivery) error {
		mu.Lock()
		handled = append(handled, "dead:"+string(d.Body)+":"+d.HeaderString("x-first-death-reason"))
		mu.Unlock()
		return nil
	})
	require.Nil(t, c.Start())
	assert.True(t, c.Running())

	ctx := app.NewContext(context.Background())
	require.Nil(t, ctx.MQ().Publish("events", "order.created", orderEvent{ID: "ok"}))
	require.Nil(t, ctx.MQ().Publish("events", "order.created", orderEvent{ID: "flaky"}))
	require.Nil(t, ctx.MQ().Publish("events", "order.paid", orderEvent{ID: "broken"}))

	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 3
	}, 5*time.Second, 10*time.Millisecond, "every message ends somewhere: acked, redelivered or dead-lettered")

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, handled, "ok")
	assert.Contains(t, handled, "flaky!", "the requeued message came back marked Redelivered")
	assert.Contains(t, handled, `dead:{"id":"broken"}:rejected`, "a plain error dead-letters, with the broker's own header")
	assert.Equal(t, int32(2), attempts.Load())
	assert.Empty(t, mq.Messages("orders"), "nothing is left waiting")

	require.Nil(t, c.Stop(context.Background()))
	assert.False(t, c.Running())
}

func redeliveredMark(d *Delivery) string {
	if d.Redelivered {
		return "!"
	}
	return ""
}

func TestMemoryMQ_expiredMessagesAreDeadLettered(t *testing.T) {
	mq := NewMemoryMQ()
	require.Nil(t, mq.DeclareExchange(ExchangeConfig{Name: "dlx", Kind: ExchangeFanout}))
	_, err := mq.DeclareQueue(QueueConfig{Name: "dead"})
	require.Nil(t, err)
	require.Nil(t, mq.BindQueue("dead", "dlx", ""))
	_, err = mq.DeclareQueue(QueueConfig{Name: "short", TTL: time.Millisecond, DeadLetterExchange: "dlx"})
	require.Nil(t, err)

	require.Nil(t, mq.Publish("", "short", "late"))
	time.Sleep(5 * time.Millisecond)

	// a consumer taking the next message skips the expired one and forwards it
	msg, _ := mq.b.take("short")
	assert.Nil(t, msg, "nothing live to deliver")
	dead := mq.Messages("dead")
	require.Len(t, dead, 1)
	assert.Equal(t, "expired", dead[0].HeaderString("x-first-death-reason"))
	assert.Equal(t, "short", dead[0].HeaderString("x-first-death-queue"))
}

func TestMemoryMQ_maxLengthDropsTheOldest(t *testing.T) {
	mq := NewMemoryMQ()
	_, err := mq.DeclareQueue(QueueConfig{Name: "bounded", MaxLength: 2})
	require.Nil(t, err)
	for _, id := range []string{"1", "2", "3"} {
		require.Nil(t, mq.Publish("", "bounded", orderEvent{ID: id}))
	}
	msgs := mq.Messages("bounded")
	require.Len(t, msgs, 2)
	first, _ := BindDelivery[orderEvent](msgs[0])
	assert.Equal(t, "2", first.ID, "the oldest went, as on a broker with x-max-length")

	info, err := mq.QueueInfo("bounded")
	require.Nil(t, err)
	assert.Equal(t, 2, info.Messages)
	n, err := mq.PurgeQueue("bounded")
	require.Nil(t, err)
	assert.Equal(t, 2, n)
}
