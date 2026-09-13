package main

import (
	"context"
	"database/sql"
	"errors"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 3: consuming ----------------------------------------------------
//
// A consumer turns queues into handlers the way the HTTP server turns routes
// into handlers, and owns everything nobody wants to write twice: the
// connection, the prefetch, the acknowledgement, a context per message, panic
// recovery, reconnection after a broker restart, and the drain on shutdown.
//
// Start connects before it returns, so a broker that is down is an error at boot
// that the caller can act on — not a goroutine retrying into a log nobody reads
// during a deploy.

func newShippingConsumer(app *core.App) core.IMQConsumer {
	c := app.NewMQConsumer(
		// Prefetch is the backpressure knob: how many unacknowledged messages the
		// broker will push at this instance. Too low and the consumer waits on the
		// network between messages; too high and one instance hoards work another
		// replica is sitting idle for. prefetch ≈ concurrency × 2 is a starting
		// point that holds up.
		core.WithMQPrefetch(8),
		// 1 by default, which is what keeps a queue's messages in order. Raising
		// it trades that order for throughput and nothing else — do it only when
		// two messages about the same entity can be handled either way round and
		// still leave the right state behind.
		core.WithMQConcurrency(4),
		// The ceiling on one handler. Make it longer than the slowest thing the
		// handler genuinely does, or every slow message dead-letters on a timeout
		// that was never realistic.
		core.WithMQHandlerTimeout(30*time.Second),
		// What the management UI shows next to this connection.
		core.WithMQConsumerTag("examples-shipping"),
	)

	// OnQueue declares the exchange, the queue and the bindings before consuming
	// — and again after every reconnect. On(queue, handler) is the other form,
	// for a queue somebody else owns; it logs as topology=external precisely
	// because nothing here will recreate it.
	c.OnQueue(core.ConsumeQueue{
		Queue: core.QueueConfig{
			Name:               shippingQueue,
			DeadLetterExchange: ordersDLX,
		},
		Exchange:    &core.ExchangeConfig{Name: ordersExchange, Kind: core.ExchangeTopic},
		BindingKeys: []string{"order.created", "order.paid"},
	}, handleShipping)

	return c
}

// handleShipping is the acknowledgement contract in one function:
//
//	return nil                 ack — the broker forgets the message
//	return err                 reject, no requeue → the DLX (or gone, without one)
//	return core.Requeue(err)   reject and redeliver, immediately
//	panic                      recovered, then treated exactly like a returned error
//
// The default not being requeue is deliberate. A permanent failure that is
// requeued comes straight back, fails the same way, and loops as fast as the
// broker can deliver — the classic way a consumer takes its database down with
// it.
func handleShipping(ctx core.IMQContext, d *core.Delivery) error {
	order, err := core.BindDelivery[Order](d)
	if err != nil {
		// A body that will not parse will not parse better the second time. This
		// is the archetypal dead-letter: keep the evidence, do not retry it.
		return err
	}

	// At-least-once is the only guarantee on offer, so a handler has to survive
	// running twice. Redelivered says this *may* be a repeat — it is a hint and
	// never a proof, because a republished copy (04) arrives with it false.
	ctx.Log().Info("shipping order",
		"order", order.ID, "key", d.RoutingKey, "redelivered", d.Redelivered)

	if err := ship(ctx, order); err != nil {
		if errors.Is(err, sql.ErrConnDone) || errors.Is(err, context.DeadlineExceeded) {
			// A failure about *now*: the same message succeeds once the database
			// is back. Requeue has no counter and no delay though, so this is only
			// right for a stumble measured in milliseconds — 04 is the version
			// with both.
			return core.Requeue(err)
		}
		return err
	}
	return nil
}

// ship stands in for the real work. Making it idempotent — a unique index plus
// an upsert on the order id — is what turns a redelivery into a no-op.
func ship(ctx core.IMQContext, order Order) error {
	if order.ID == "" {
		// A business rule saying no is not an error: it must not go and sit in the
		// dead-letter queue where somebody will investigate it.
		ctx.Log().Warn("mq: order without an id, dropping")
		return nil
	}
	return nil
}

// stopShippingConsumer stops one consumer early — draining work on a rolling
// deploy while the process keeps serving HTTP.
//
// It is not needed at shutdown: the App remembers every consumer it handed out
// and stops them *before* closing the pools their handlers are using.
func stopShippingConsumer(app *core.App, c core.IMQConsumer) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// The order inside Stop: cancel at the broker so no new deliveries arrive →
	// nack-with-requeue anything already taken but not started → wait for
	// in-flight handlers → at the deadline, close underneath them so their
	// messages return to the broker unacknowledged rather than hold shutdown
	// open. Keep that deadline below the orchestrator's grace period, or none of
	// it ever happens.
	if err := c.Stop(ctx); err != nil {
		app.Log().Error("mq consumer did not drain in time", "err", err)
	}
}
