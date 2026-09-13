package main

import (
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 2: topology, declared at boot -----------------------------------
//
// Declaring is idempotent, so this runs on every boot: deploying the service is
// what creates its topology, and nothing depends on somebody having run a script
// or clicked in the management UI first. Declaring something that already exists
// with *different* settings is an error from the broker, which is the feature:
// it catches topology that changed in code while production still has the old
// shape, instead of letting two environments drift apart quietly.
//
// Who declares what:
//
//	publisher   only the exchanges it sends into
//	consumer    its own queue, its own bindings, its own DLX (see 03)
//
// A queue belongs to whoever reads it. A publisher that declares somebody else's
// queue is a service that must be redeployed whenever that consumer changes a
// binding, and the reason an obsolete queue still fills up months later.

const (
	ordersDLX     = "orders.dlx"
	ordersDeadQ   = "orders.dead"
	shippingQueue = "orders.shipping"
	paymentsQueue = "orders.payments"
)

// declarePublisherTopology is everything the publishing side needs to know.
func declarePublisherTopology(app *core.App) core.IError {
	return app.MQ().DeclareExchange(core.ExchangeConfig{
		Name: ordersExchange,
		// topic subsumes direct (an exact key) and fanout ("#"), so it is the
		// default worth choosing: a second consumer can be added later with a new
		// binding and no change here at all.
		Kind: core.ExchangeTopic,
		// Transient is false, so this is durable. The zero value is the safe one
		// on purpose — an exchange that vanishes with the broker is almost never
		// what anybody meant.
	})
}

// declareDeadLetterTopology declares the DLX *and* the queue that gives it a
// point.
//
// A dead-letter exchange with nothing bound to it is a bin: the broker routes
// the rejected message into it and drops it, exactly as if there were no DLX.
// The cost of having one is an empty queue; the cost of not having one is the
// unanswerable question "where did that order go".
func declareDeadLetterTopology(app *core.App) core.IError {
	mq := app.MQ()

	if err := mq.DeclareExchange(core.ExchangeConfig{
		Name: ordersDLX,
		Kind: core.ExchangeTopic,
	}); err != nil {
		return err
	}

	if _, err := mq.DeclareQueue(core.QueueConfig{
		Name: ordersDeadQ,
		// Two weeks to come and look, rather than for ever. A dead-letter queue
		// nobody ever empties is a disk that fills at the worst possible moment.
		TTL: 14 * 24 * time.Hour,
	}); err != nil {
		return err
	}

	// "#" catches every routing key that dead-letters into this exchange.
	return mq.BindQueue(ordersDeadQ, ordersDLX, "#")
}

// declarePaymentsQueue is the queue where losing one message means somebody
// reconciling by hand.
func declarePaymentsQueue(app *core.App) core.IError {
	mq := app.MQ()

	info, err := mq.DeclareQueue(core.QueueConfig{
		Name:               paymentsQueue,
		DeadLetterExchange: ordersDLX,
		// Replicated across the cluster, so losing a node loses no message. It
		// costs throughput and memory, which is why it belongs on the queues that
		// carry money and not on the one that sends notifications.
		//
		// Known limits: a quorum queue supports neither MaxPriority nor Exclusive.
		Quorum: true,
	})
	if err != nil {
		return err
	}
	// DeclareQueue reports the queue as it is at that moment — a free reading of
	// what survived the last deploy.
	app.Log().Info("payments queue declared",
		"queue", info.Name, "messages", info.Messages, "consumers", info.Consumers)

	return mq.BindQueue(paymentsQueue, ordersExchange, "order.paid")
}

// queueDepth reads depth without declaring anything: QueueInfo uses a passive
// declare, so it creates nothing and fails when the queue is absent. A typo
// therefore reports an error instead of conjuring a ghost queue that quietly
// collects messages nobody consumes.
//
// Depth is the signal that arrives first when a consumer falls behind. It is
// still succeeding, only too slowly, so there is no error rate to alert on —
// export this, not just failures.
func queueDepth(app *core.App, queues ...string) {
	for _, q := range queues {
		info, err := app.MQ().QueueInfo(q)
		if err != nil {
			app.Log().Warn("mq: cannot read queue depth", "queue", q, "err", err)
			continue
		}
		app.Log().Info("mq queue depth",
			"queue", info.Name, "messages", info.Messages, "consumers", info.Consumers)
	}
}
