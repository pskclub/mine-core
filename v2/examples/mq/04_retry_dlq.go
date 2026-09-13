package main

import (
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 4: retry with a real gap, and a DLQ you can replay --------------
//
// core.Requeue means "again, now". No counter, no delay. That is right for a
// stumble measured in milliseconds and wrong for a dependency that is down for
// ten minutes: the message comes straight back, fails the same way, and the loop
// runs as fast as the broker can deliver it.
//
// The pattern that works is a delay queue — a queue nobody consumes, with a TTL
// and a dead-letter exchange pointing back at the original one. A message sent
// there lies still for the TTL and then bounces home by itself, with its
// original routing key, which is what makes the round trip invisible to the
// handler.
//
//	orders ──▶ orders.shipping ──(transient)──▶ orders.retry.10s
//	     ▲                                              │ TTL 10s
//	     └──────────── DLX back into orders ◀───────────┘

const (
	retryExchange = "orders.retry"
	maxAttempts   = 5
)

type retryLevel struct {
	queue string
	delay time.Duration
}

// retryLevels is one queue per delay, deliberately.
//
// The alternative — one queue and a different PublishOptions.Expiration per
// message — looks simpler and is a trap: RabbitMQ only expires a message when it
// reaches the head of the queue, so a 10s message queued behind a 10m one waits
// ten minutes (head-of-line blocking).
func retryLevels() []retryLevel {
	return []retryLevel{
		{queue: "orders.retry.10s", delay: 10 * time.Second},
		{queue: "orders.retry.1m", delay: time.Minute},
		{queue: "orders.retry.10m", delay: 10 * time.Minute},
	}
}

func declareRetryTopology(app *core.App) core.IError {
	mq := app.MQ()

	if err := mq.DeclareExchange(core.ExchangeConfig{
		Name: retryExchange,
		Kind: core.ExchangeTopic,
	}); err != nil {
		return err
	}

	for _, lvl := range retryLevels() {
		if _, err := mq.DeclareQueue(core.QueueConfig{
			Name: lvl.queue,
			TTL:  lvl.delay,
			// Where an expired message goes: back into the exchange the work came
			// from, so it is redelivered to the normal handler.
			DeadLetterExchange: ordersExchange,
		}); err != nil {
			return err
		}
		if err := mq.BindQueue(lvl.queue, retryExchange, lvl.queue); err != nil {
			return err
		}
	}
	return nil
}

// scheduleRetry acks the delivery it was given (by returning nil) and puts a
// copy in the delay queue.
//
// Publish first, ack second: this order can duplicate the message if the process
// dies in between, and the other order loses it. Duplication is the failure a
// consumer already has to tolerate; loss is not.
func scheduleRetry(ctx core.IMQContext, d *core.Delivery, cause error) error {
	attempt := attemptOf(d) + 1
	if attempt > maxAttempts {
		// Out of attempts: return the cause so the message rejects into the DLX.
		// What waits there is then only what genuinely exhausted its retries,
		// which is what makes the dead-letter queue worth reading.
		return cause
	}

	levels := retryLevels()
	lvl := levels[min(attempt-1, len(levels)-1)]
	ctx.Log().Warn("mq: retrying later",
		"attempt", attempt, "in", lvl.queue, "delay", lvl.delay.String(), "err", cause)

	return ctx.MQ().PublishWith(retryExchange, lvl.queue, d.Body, core.PublishOptions{
		// d.Body is []byte, and this package will not claim on the caller's behalf
		// that a byte slice is JSON. Without this the copy arrives as
		// application/octet-stream and the next handler is none the wiser.
		ContentType: d.ContentType,
		// Keep the identity, or the consumer's dedupe stops recognising its own
		// message.
		MessageID:     d.MessageID,
		CorrelationID: d.CorrelationID,
		Type:          d.Type,
		Headers:       withAttempt(d.Headers, attempt),
	})
}

// attemptOf counts with a header of our own. The broker's x-death array carries
// something similar, but reading it means reaching into the amqp091 types this
// layer exists to keep out of handlers — and it counts "times dead-lettered from
// this queue", which is not the same question.
//
// AMQP header integers arrive as whichever width the wire used, so all three
// cases are real.
func attemptOf(d *core.Delivery) int {
	switch n := d.Headers["x-attempt"].(type) {
	case int32:
		return int(n)
	case int64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

// withAttempt copies the headers rather than mutating the delivery's map, so the
// original message stays exactly as it arrived for logging and Sentry.
func withAttempt(h map[string]any, n int) map[string]any {
	out := make(map[string]any, len(h)+1)
	for k, v := range h {
		out[k] = v
	}
	out["x-attempt"] = n
	return out
}

// newDLQReplayConsumer reads the dead-letter queue and republishes into the
// original exchange. Start it only once the cause is fixed — replaying a full
// DLQ into a still-broken consumer refills it, with a second helping of load.
func newDLQReplayConsumer(app *core.App) core.IMQConsumer {
	replay := app.NewMQConsumer(
		core.WithMQConcurrency(1),
		core.WithMQConsumerTag("dlq-replay"),
	)

	replay.On(ordersDeadQ, func(ctx core.IMQContext, d *core.Delivery) error {
		// Not d.Exchange: a dead-lettered delivery reports the exchange it was
		// last published to, which is the DLX itself. Republishing there would
		// route straight back into this queue and spin. The destination has to be
		// named.
		return ctx.MQ().PublishWith(ordersExchange, d.RoutingKey, d.Body, core.PublishOptions{
			ContentType:   d.ContentType,
			MessageID:     d.MessageID,
			CorrelationID: d.CorrelationID,
			Type:          d.Type,
			// Start the count again — this message is getting a fresh budget.
			Headers: withAttempt(d.Headers, 0),
		})
	})
	return replay
}
