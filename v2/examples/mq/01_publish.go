package main

import (
	"errors"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
)

// --- Example 1: publishing ---------------------------------------------------
//
// The publisher runs in confirm mode, so one publish is one round trip that ends
// in the broker's acknowledgement. That is what makes `err == nil` worth
// something here: the broker has taken responsibility for the message, not
// merely accepted the bytes. It is the difference between *sent* and *stored*,
// and the reason persistence alone never guaranteed anything.
//
// What nil still does not say is that anything will ever *read* it. A message
// published to an exchange with no matching binding is confirmed and thrown
// away. Routing is topology's job (02), not the publisher's — Mandatory is the
// opt-in that turns that particular silence into an error.

// Order is the payload. Anything that is not []byte is JSON-encoded on the way
// out, with content type application/json.
type Order struct {
	ID       string    `json:"id"`
	Total    int64     `json:"total"`
	Currency string    `json:"currency"`
	PlacedAt time.Time `json:"placed_at"`
}

const ordersExchange = "orders"

// publishOrderCreated is the whole API for the common case.
//
// Note what has to have happened before it: the row exists. A message published
// inside a transaction escapes immediately — the broker knows nothing about the
// database's transaction — so a consumer can win the race and go looking for an
// order that is not there yet. Commit first, publish second.
func publishOrderCreated(ctx core.IContext, order Order) core.IError {
	// PublishAs is Publish with the payload type pinned by the compiler. Same
	// call, same wire format; worth having on a publish nobody reads again for a
	// year and then changes the struct of.
	return core.PublishAs(ctx.MQ(), ordersExchange, "order.created", order)
}

// publishOrderPaid spells out the properties that decide whether this message is
// debuggable and de-duplicatable once it is somebody else's problem.
func publishOrderPaid(ctx core.IContext, order Order, requestID string) core.IError {
	return ctx.MQ().PublishWith(ordersExchange, "order.paid", order, core.PublishOptions{
		// MessageID has to come from the identity of the *work* — an order id, a
		// payment id — never a fresh UUID per attempt. Two publishes of the same
		// event must collide, or the consumer's dedupe has nothing to match on.
		MessageID: order.ID,
		// CorrelationID threads request → message → consumer work together in the
		// logs. It costs nothing and is the first thing wanted during an incident.
		CorrelationID: requestID,
		// Type names the event for a queue that receives several kinds.
		Type: "order.paid",
		// Headers travel with the message. A schema version from day one is where
		// a breaking change gets to stand later.
		Headers: map[string]any{"schema": 1},
		// Mandatory costs an extra round trip and buys MQ_NO_ROUTE instead of a
		// silent discard. Right for an event whose loss becomes a support ticket;
		// wrong for a fan-out that is *meant* to have no listeners yet, where the
		// error is one nobody can act on.
		Mandatory: true,
	})
}

// handlePublishError tells apart the failures that mean different things. The
// one that always deserves a decision is MQ_NACK: the broker accepted the
// message and then refused it, so it was *not* stored and the caller is the only
// one who can say what happens next.
func handlePublishError(ctx core.IContext, err core.IError) error {
	switch {
	case err == nil:
		return nil

	case errors.Is(err, core.ErrMQNoRoute):
		// Confirmed and discarded: nothing is bound for this key. Almost always a
		// topology that was never applied, not a transient fault — retrying sends
		// it to the same nowhere.
		return ctx.NewError(err, errmsgs.MQError)

	case errors.Is(err, core.ErrMQNack):
		// The broker took it and then said no (a full disk, a queue refusing the
		// write). The message is gone; hand it to whatever can send it again —
		// an outbox row (05), a job — rather than swallowing the error.
		return ctx.NewError(err, errmsgs.MQError)

	case errors.Is(err, core.ErrMQDisabled):
		// No MQ_* configuration at all. The queue fails loudly where the cache
		// would degrade quietly, because a dropped message is work somebody
		// believes was handed off and nobody will ever pick up.
		return ctx.NewError(err, errmsgs.MQError)

	case errors.Is(err, core.ErrMQClosed):
		// Shutting down. A publish that arrives now is better answered honestly
		// than allowed to hold the shutdown open.
		ctx.Log().Warn("mq: publish during shutdown", "err", err)
		return err

	default:
		// Dial failures, timeouts, waiting too long for a free channel. Transient
		// by nature — this is the group worth retrying.
		return err
	}
}
