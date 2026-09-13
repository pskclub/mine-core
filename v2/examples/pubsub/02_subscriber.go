package main

import (
	"context"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 2: subscribing --------------------------------------------------
//
// A subscriber turns channels into handlers the way the HTTP server turns routes
// into handlers, and owns the parts nobody should rewrite: the subscription, an
// IContext per message, panic recovery, the concurrency limit and the drain on
// shutdown. Under it there are at most two redis subscriptions — one for all the
// exact names, one for all the globs — not one per channel.
//
// Use it by default. Take the raw channel only when the shape does not fit.

func newSubscriber(app *core.App) core.ISubscriber {
	sub := app.NewSubscriber(
		// 1 by default, which keeps messages in the order they arrived. The read
		// loop stays sequential either way; only the handlers fan out, so raising
		// this trades ordering for throughput and nothing else. Keep it at 1 when
		// two messages about the same entity would race — two user.updated for one
		// user, handled out of order, cache the older one.
		core.WithSubscriberConcurrency(4),
		// A handler that overruns has its context cancelled, which is what stops
		// one stuck message from holding the only worker for ever. Cancellation is
		// cooperative: anything using ctx notices, a tight loop never does.
		core.WithSubscriberTimeout(10*time.Second),
		// Worth doing when pub/sub traffic is heavy: redis disconnects a
		// subscriber that falls behind, and keeping that pressure off the instance
		// holding hot keys means it does not land on the request path.
		core.WithSubscriberCache("default"),
	)

	// Registering the same channel twice replaces the first handler — one handler
	// per channel, not a list. If two things must happen, call both from one
	// handler or run two subscribers.
	sub.On("user.updated", handleUserUpdated)
	sub.OnPattern("order.*", handleOrderEvent)

	return sub
}

func handleUserUpdated(ctx core.IContext, msg *core.PubSubMessage) error {
	// The payload is an id, so the handler reads the current state itself and is
	// therefore immune to being run out of order.
	id := msg.String()

	ctx.Log().Info("user changed", "id", id, "channel", msg.Channel)
	return core.SetJSON(ctx.Cache(), "user:"+id, User{ID: id}, time.Minute)
}

func handleOrderEvent(ctx core.IContext, msg *core.PubSubMessage) error {
	// For a pattern subscription, Channel is the real channel and Pattern is the
	// glob that matched it.
	ctx.Log().Info("order event", "channel", msg.Channel, "pattern", msg.Pattern)
	return nil
}

// What returning an error does: it is logged and reported to Sentry, and that is
// all. There is no redelivery in pub/sub, so an error is a record of what went
// wrong rather than a request to try again — and a panic ends that message, not
// the process. Which makes the error return a question about observability: a
// missing row because the message raced the commit is noise, a failing
// downstream call is not.

// runSubscriber is the lifecycle in full: start, serve until the caller's
// context ends, drain. It is what a consumer-only deployment's main looks like —
// a subscriber needs no HTTP server to be a whole process.
func runSubscriber(app *core.App, done <-chan struct{}) error {
	sub := newSubscriber(app)

	// Start returns as soon as the subscriptions are live, so one process can run
	// an HTTP server and a subscriber. It fails loudly when there is no cache
	// (PUBSUB_DISABLED) rather than subscribing to nothing: a subscriber that
	// receives nothing forever is a service that looks healthy while doing none
	// of its work.
	if err := sub.Start(); err != nil {
		return err
	}

	// Stopping is idempotent and waits for in-flight handlers up to the deadline.
	// It is not needed at shutdown — app.Shutdown stops every subscriber it
	// handed out before closing the pools they read from, and that ordering is
	// the point: a subscriber still dispatching into a closed connection is a
	// burst of errors on the way out.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = sub.Stop(ctx)
	}()

	<-done
	return nil
}

// tailChannel takes the raw channel instead, for a listener whose lifetime is
// not the process's — a websocket hub, a one-off tail.
//
// A subscription deliberately does not die with the request context it was
// opened from (a hub opened from an HTTP request has to outlive that request).
// Close is what ends it, and forgetting it leaks a goroutine and a redis
// connection for the life of the process — hence the defer, on the line after.
func tailChannel(ctx core.IContext, pattern string) core.IError {
	sub, err := ctx.PubSub().PSubscribe(pattern)
	if err != nil {
		return err
	}
	defer sub.Close()

	for {
		select {
		case msg, ok := <-sub.C():
			if !ok {
				return nil // Close was called; the channel is closed for us
			}
			// The loop body must be fast. Each subscription buffers a bounded
			// number of messages, and a consumer slower than the publisher fills
			// it, falls behind redis's own output buffer, and is disconnected by
			// the server. Anything slow belongs on a worker pool or a job.
			ctx.Log().Debug("tail", "channel", msg.Channel, "payload", msg.String())
		case <-ctx.Done():
			return nil
		}
	}
}
