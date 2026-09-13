// Command pubsub is a runnable tour of the v2 redis publish/subscribe support:
// publishing, the handler-based subscriber, raw subscriptions, and the three
// jobs pub/sub is genuinely the right tool for.
//
// Each example lives in its own file:
//
//	01_publish.go     publishing, payloads, channel names, prefixes
//	02_subscriber.go  NewSubscriber, handlers, options, lifecycle, raw channels
//	03_patterns.go    invalidation, config reloads, websockets — and the limits
//
// Run it with: go run ./examples/pubsub
//
// It runs without redis: with no CACHE_* configuration it falls back to the
// memory cache, which carries an in-process broker, so every example below
// actually delivers. For a real redis:
//
//	docker run -d --rm -p 6379:6379 redis:7-alpine
//
// then point the example at it:
//
//	APP_CACHE_HOST=127.0.0.1:6379 go run ./examples/pubsub
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

	// NewCache pings before returning, so a wrong address fails here rather than
	// on the first request. The memory cache is the fallback because a subscriber
	// with no cache at all cannot start: subscribing fails with PUBSUB_DISABLED
	// on purpose, since a subscriber that silently receives nothing forever is a
	// service that looks healthy while doing none of its work.
	cache, cacheErr := core.NewCache(env)
	if cacheErr != nil {
		cache = core.NewMemoryCache()
	}

	app, err := core.NewApp(env, core.WithCache("default", cache))
	if err != nil {
		panic(err)
	}
	if cacheErr != nil {
		app.Log().Warn("no redis: running on the in-process memory broker",
			"err", cacheErr,
			"hint", "docker run -d --rm -p 6379:6379 redis:7-alpine")
		// What the memory broker does not prove: fan-out to *other replicas*, the
		// disconnection of a slow subscriber, and reconnection after a redis
		// restart are all real-redis behaviour.
	}

	sub := newSubscriber(app)
	if err := sub.Start(); err != nil {
		panic(err)
	}

	// The websocket hub is a raw subscription with its own lifetime — one per
	// process, fanned out in memory.
	h := newHub()
	go func() {
		if err := h.Run(app); err != nil {
			app.Log().Error("hub stopped", "err", err)
		}
	}()

	demoPublish(app)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.Log().Info("pubsub example running — press ctrl-c to stop")
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Shutdown stops every subscriber the App handed out before closing the
	// connections they are reading from, waiting for in-flight handlers within
	// this deadline.
	_ = app.Shutdown(shutdownCtx)
}

// demoPublish shows the one ordering rule that always applies: subscribe first,
// then publish. There is no buffering and no replay, so a message published
// before Start is simply gone.
func demoPublish(app *core.App) {
	ctx := app.NewContext(context.Background())

	ctx.PubSub().Publish("user.updated", "u-1")
	ctx.PubSub().Publish("order.created", "o-1")
	ctx.PubSub().Publish("notify.u-1", map[string]string{"text": "Your export is ready"})

	// The wire name is what a subscriber outside this framework has to use.
	app.Log().Info("channel names on the wire",
		"logical", "user.updated",
		"wire", ctx.PubSub().Channel("user.updated"))
}
