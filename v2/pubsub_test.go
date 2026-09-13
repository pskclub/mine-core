package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pubsubEvent struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// receive waits for one message, failing the test rather than hanging forever.
func receive(t *testing.T, sub ISubscription) *PubSubMessage {
	t.Helper()
	select {
	case msg, ok := <-sub.C():
		require.True(t, ok, "subscription closed before a message arrived")
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a message")
		return nil
	}
}

func TestPubSub_deliversToASubscriber(t *testing.T) {
	c := newTestCache(t)
	ps := c.PubSub()

	sub, err := ps.Subscribe("user.updated")
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	require.NoError(t, ps.Publish("user.updated", pubsubEvent{ID: "u-1", Kind: "profile"}))

	msg := receive(t, sub)
	assert.Equal(t, "user.updated", msg.Channel)
	assert.Empty(t, msg.Pattern)

	got, bindErr := BindMessage[pubsubEvent](msg)
	require.NoError(t, bindErr)
	assert.Equal(t, pubsubEvent{ID: "u-1", Kind: "profile"}, got)
}

func TestPubSub_deliversToEveryListener(t *testing.T) {
	// fan-out, not a queue: both subscribers get the same message
	c := newTestCache(t)
	ps := c.PubSub()

	first, err := ps.Subscribe("cache.flush")
	require.NoError(t, err)
	defer func() { _ = first.Close() }()
	second, err := ps.Subscribe("cache.flush")
	require.NoError(t, err)
	defer func() { _ = second.Close() }()

	require.NoError(t, ps.Publish("cache.flush", "all"))

	assert.Equal(t, "all", receive(t, first).String())
	assert.Equal(t, "all", receive(t, second).String())
}

func TestPubSub_ignoresOtherChannels(t *testing.T) {
	c := newTestCache(t)
	ps := c.PubSub()

	sub, err := ps.Subscribe("wanted")
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	require.NoError(t, ps.Publish("unwanted", "nope"))
	require.NoError(t, ps.Publish("wanted", "yes"))

	assert.Equal(t, "yes", receive(t, sub).String())
}

func TestPubSub_patternSubscription(t *testing.T) {
	c := newTestCache(t)
	ps := c.PubSub()

	sub, err := ps.PSubscribe("order.*")
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	require.NoError(t, ps.Publish("order.created", "o-1"))

	msg := receive(t, sub)
	assert.Equal(t, "order.created", msg.Channel)
	assert.Equal(t, "order.*", msg.Pattern, "the glob that matched is reported")
}

func TestPubSub_prefixIsolatesNamespaces(t *testing.T) {
	// two environments on one redis must not hear each other
	c := newTestCache(t)
	staging := c.WithPrefix("staging").PubSub()
	prod := c.WithPrefix("prod").PubSub()

	stagingSub, err := staging.Subscribe("user.updated")
	require.NoError(t, err)
	defer func() { _ = stagingSub.Close() }()

	require.NoError(t, prod.Publish("user.updated", "prod-only"))
	require.NoError(t, staging.Publish("user.updated", "staging-only"))

	msg := receive(t, stagingSub)
	assert.Equal(t, "staging-only", msg.String(), "the prod message must not reach staging")
	assert.Equal(t, "user.updated", msg.Channel, "subscribers see their own name, not the wire name")
	assert.Equal(t, "staging:user.updated", staging.Channel("user.updated"))
}

func TestPubSub_closeEndsTheRangeLoop(t *testing.T) {
	c := newTestCache(t)
	sub, err := c.PubSub().Subscribe("k")
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		for range sub.C() { //nolint:revive // draining is the point
		}
		close(done)
	}()

	require.NoError(t, sub.Close())
	require.NoError(t, sub.Close(), "closing twice is safe")

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the message channel was never closed")
	}
}

func TestPubSub_subscribeFailsWithoutACache(t *testing.T) {
	ctx := newTestApp(t).NewContext(t.Context())

	// publishing is dropped, like a write to a disabled cache
	require.NoError(t, ctx.PubSub().Publish("user.updated", "x"))

	// subscribing is not: a subscriber that silently hears nothing is a service
	// that looks healthy while doing none of its work
	_, err := ctx.PubSub().Subscribe("user.updated")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCacheDisabled))
	assert.Equal(t, "PUBSUB_DISABLED", err.GetCode())
}

func TestPubSub_subscribeNeedsAChannel(t *testing.T) {
	c := newTestCache(t)
	_, err := c.PubSub().Subscribe()
	assert.Error(t, err)
}

func TestMatchChannel(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"order.*", "order.created", true},
		{"order.*", "order.", true},
		{"order.*", "orders.created", false},
		{"*", "anything.at.all", true},
		{"user.?", "user.1", true},
		{"user.?", "user.12", false},
		{"cache:[ab]*", "cache:alpha", true},
		{"cache:[ab]*", "cache:gamma", false},
		{"cache:[a-c]1", "cache:b1", true},
		{"cache:[^a]1", "cache:b1", true},
		{"cache:[^a]1", "cache:a1", false},
		{"exact", "exact", true},
		{"exact", "exactly", false},
		{`a\*b`, "a*b", true},
		{`a\*b`, "axb", false},
		{"a*b*c", "a-bb-c", true},
		{"a*b*c", "a-bb-d", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, matchChannel(tc.pattern, tc.name),
			"matchChannel(%q, %q)", tc.pattern, tc.name)
	}
}

// --- subscriber ----------------------------------------------------------

func newSubscriberApp(t *testing.T) (*App, ICache) {
	t.Helper()
	c := NewMemoryCache()
	app := newTestApp(t, WithCache("default", c))
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	return app, c
}

func TestSubscriber_dispatchesToItsHandler(t *testing.T) {
	app, c := newSubscriberApp(t)

	got := make(chan pubsubEvent, 1)
	sub := app.NewSubscriber()
	sub.On("user.updated", func(ctx IContext, msg *PubSubMessage) error {
		assert.Equal(t, ModeMQ, ctx.Mode(), "a delivery is a unit of work like a job is")
		event, err := BindMessage[pubsubEvent](msg)
		if err != nil {
			return err
		}
		got <- event
		return nil
	})
	require.NoError(t, sub.Start())
	assert.True(t, sub.Running())

	require.NoError(t, c.PubSub().Publish("user.updated", pubsubEvent{ID: "u-1"}))

	select {
	case event := <-got:
		assert.Equal(t, "u-1", event.ID)
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never ran")
	}
}

func TestSubscriber_routesPatternsAndExactNames(t *testing.T) {
	app, c := newSubscriberApp(t)

	var mu sync.Mutex
	seen := map[string]string{}
	sub := app.NewSubscriber()
	sub.On("user.updated", func(_ IContext, msg *PubSubMessage) error {
		mu.Lock()
		defer mu.Unlock()
		seen["exact"] = msg.Channel
		return nil
	})
	sub.OnPattern("order.*", func(_ IContext, msg *PubSubMessage) error {
		mu.Lock()
		defer mu.Unlock()
		seen["pattern"] = msg.Channel
		return nil
	})
	require.NoError(t, sub.Start())

	require.NoError(t, c.PubSub().Publish("user.updated", "x"))
	require.NoError(t, c.PubSub().Publish("order.created", "y"))

	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 2
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "user.updated", seen["exact"])
	assert.Equal(t, "order.created", seen["pattern"])
}

func TestSubscriber_survivesAPanickingHandler(t *testing.T) {
	app, c := newSubscriberApp(t)

	handled := make(chan string, 2)
	sub := app.NewSubscriber()
	sub.On("boom", func(_ IContext, _ *PubSubMessage) error {
		handled <- "boom"
		panic("handler exploded")
	})
	sub.On("fine", func(_ IContext, _ *PubSubMessage) error {
		handled <- "fine"
		return nil
	})
	require.NoError(t, sub.Start())

	require.NoError(t, c.PubSub().Publish("boom", "1"))
	require.NoError(t, c.PubSub().Publish("fine", "2"))

	var got []string
	for range 2 {
		select {
		case v := <-handled:
			got = append(got, v)
		case <-time.After(2 * time.Second):
			t.Fatal("the subscriber stopped after the panic")
		}
	}
	assert.ElementsMatch(t, []string{"boom", "fine"}, got)
}

func TestSubscriber_stopDrainsAndIsIdempotent(t *testing.T) {
	app, c := newSubscriberApp(t)

	started := make(chan struct{})
	finished := make(chan struct{})
	sub := app.NewSubscriber()
	sub.On("slow", func(_ IContext, _ *PubSubMessage) error {
		close(started)
		time.Sleep(100 * time.Millisecond)
		close(finished)
		return nil
	})
	require.NoError(t, sub.Start())
	require.NoError(t, c.PubSub().Publish("slow", "1"))

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, sub.Stop(ctx), "Stop waits for the handler that is still running")

	select {
	case <-finished:
	default:
		t.Fatal("Stop returned while a handler was still running")
	}

	assert.False(t, sub.Running())
	require.NoError(t, sub.Stop(ctx), "stopping twice is safe")
}

func TestSubscriber_startRequiresHandlers(t *testing.T) {
	app, _ := newSubscriberApp(t)
	err := app.NewSubscriber().Start()
	require.Error(t, err)
	assert.Equal(t, "SUBSCRIBER_EMPTY", err.GetCode())
}

func TestSubscriber_startTwiceIsRefused(t *testing.T) {
	app, _ := newSubscriberApp(t)
	sub := app.NewSubscriber()
	sub.On("k", func(IContext, *PubSubMessage) error { return nil })
	require.NoError(t, sub.Start())

	err := sub.Start()
	require.Error(t, err)
	assert.Equal(t, "SUBSCRIBER_RUNNING", err.GetCode())
}

func TestSubscriber_needsACache(t *testing.T) {
	app := newTestApp(t)
	sub := app.NewSubscriber()
	sub.On("k", func(IContext, *PubSubMessage) error { return nil })

	err := sub.Start()
	require.Error(t, err)
	assert.Equal(t, "PUBSUB_DISABLED", err.GetCode(), "the failure names the missing configuration")
}

func TestAppShutdown_stopsSubscribers(t *testing.T) {
	c := NewMemoryCache()
	app := newTestApp(t, WithCache("default", c))

	sub := app.NewSubscriber()
	sub.On("k", func(IContext, *PubSubMessage) error { return nil })
	require.NoError(t, sub.Start())

	require.NoError(t, app.Shutdown(context.Background()))
	assert.False(t, sub.Running(), "a subscriber must stop before the pools it reads from close")
}

func TestSubscriber_concurrencyLimitsHandlersInFlight(t *testing.T) {
	app, c := newSubscriberApp(t)

	const messages = 6
	var mu sync.Mutex
	var inFlight, peak, handled int
	sub := app.NewSubscriber(WithSubscriberConcurrency(2))
	sub.On("work", func(IContext, *PubSubMessage) error {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		inFlight--
		handled++
		mu.Unlock()
		return nil
	})
	require.NoError(t, sub.Start())

	for range messages {
		require.NoError(t, c.PubSub().Publish("work", "x"))
	}

	// wait for the work rather than for Stop to drain it: stopping first would
	// race the reader that has not been scheduled yet, and the test would then
	// measure nothing
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return handled == messages
	}, 3*time.Second, 5*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, sub.Stop(ctx))

	mu.Lock()
	defer mu.Unlock()
	assert.LessOrEqual(t, peak, 2, "no more handlers ran at once than were allowed")
	assert.Greater(t, peak, 0, "handlers did run")
}
