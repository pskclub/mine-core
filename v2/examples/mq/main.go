// Command mq is a runnable tour of the v2 RabbitMQ integration: publishing with
// confirms, topology declared at boot, consuming with the right
// acknowledgement, retry that actually waits, and a transactional outbox.
//
// Each example lives in its own file:
//
//	01_publish.go    Publish / PublishAs / PublishWith, and what err == nil buys
//	02_topology.go   exchanges, queues, bindings, DLX, quorum — declared at boot
//	03_consumer.go   NewMQConsumer, ack/nack, prefetch, concurrency, shutdown
//	04_retry_dlq.go  delay queues for backoff, and a DLQ that can be replayed
//	05_outbox.go     the message written in the same transaction as the work
//
// Run it with: go run ./examples/mq
//
// It runs without a broker: everything needing RabbitMQ is skipped with a log
// line rather than a panic. For a real one:
//
//	docker run -d --rm -p 5672:5672 -p 15672:15672 rabbitmq:3-management
//
// then point the example at it:
//
//	APP_MQ_CONNECTION_STRING=amqp://guest:guest@127.0.0.1:5672/ go run ./examples/mq
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

func main() {
	env, err := core.NewEnv()
	if err != nil {
		panic(err)
	}

	// NewMQ dials at boot and fails if it cannot reach the broker — the same
	// fail-fast the cache and the database do, because a wrong connection string
	// should be found at boot and not at three in the morning on the first
	// publish. Here that failure is expected, so it is logged and the App is
	// built without a publisher.
	opts := []core.Option{}
	mq, mqErr := core.NewMQ(env)
	if mqErr == nil {
		opts = append(opts, core.WithMQ(mq))
	}

	app, err := core.NewApp(env, opts...)
	if err != nil {
		panic(err)
	}

	// The outbox drain is a job: registering it needs a registry and nothing
	// else, broker or no broker.
	reg := core.NewJobRegistry()
	registerOutboxDrain(reg)

	// app.MQ() is never nil. Without configuration it is the disabled publisher,
	// whose every call returns MQ_DISABLED naming what is missing — so this check
	// is a choice about what to do, not a guard against a nil dereference.
	if !app.MQ().Enabled() {
		app.Log().Warn("no broker: publishing and consuming are skipped",
			"err", mqErr,
			"hint", "docker run -d --rm -p 5672:5672 rabbitmq:3-management")
		return
	}

	if err := bootTopology(app); err != nil {
		panic(err)
	}

	consumer := newShippingConsumer(app)
	// Start connects for real before returning, so a broker that is down fails
	// the boot instead of leaving the service looking healthy and hearing
	// nothing.
	if err := consumer.Start(); err != nil {
		panic(err)
	}

	// Replaying a dead-letter queue before the cause is fixed just refills it, so
	// this stays behind a flag and is switched on deliberately.
	if app.ENV().Bool("DLQ_REPLAY") {
		if err := newDLQReplayConsumer(app).Start(); err != nil {
			panic(err)
		}
	}

	demoPublish(app)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.Log().Info("mq example running — press ctrl-c to stop")
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// One call is the whole shutdown: the App stops every consumer it handed out
	// before closing the pools those handlers are using, waits for what is in
	// flight, and returns unacknowledged messages to the broker.
	_ = app.Shutdown(shutdownCtx)
}

// bootTopology declares what this service owns, in the order the pieces depend
// on each other. Re-declaring identical topology is a no-op, so this is safe on
// every boot.
func bootTopology(app *core.App) core.IError {
	if err := declarePublisherTopology(app); err != nil {
		return err
	}
	if err := declareDeadLetterTopology(app); err != nil {
		return err
	}
	if err := declarePaymentsQueue(app); err != nil {
		return err
	}
	return declareRetryTopology(app)
}

func demoPublish(app *core.App) {
	// A context that belongs to no request: this publish outlives nothing in
	// particular, so it must not be cancelled by anything in particular.
	ctx := app.NewContext(context.Background())

	order := Order{ID: "o-1", Total: 4200, Currency: "THB", PlacedAt: time.Now()}
	if err := publishOrderCreated(ctx, order); err != nil {
		_ = handlePublishError(ctx, err)
	}
	if err := publishOrderPaid(ctx, order, "req-1"); err != nil {
		_ = handlePublishError(ctx, err)
	}

	queueDepth(app, shippingQueue, paymentsQueue, ordersDeadQ)
}
