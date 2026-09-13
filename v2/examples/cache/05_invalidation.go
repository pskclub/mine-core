package main

import (
	"fmt"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 5: invalidation ------------------------------------------------
//
// Two strategies, and mixing them badly is where stale data comes from.
//
//	expire and forget   a short TTL; stale for at most that long. Simplest, and
//	                    correct wherever "up to a minute old" is fine.
//	write-through       delete the key when the underlying data changes.
//
// Order matters — write first, then invalidate. Invalidating before the write
// lets a concurrent read repopulate the cache with the old value, and the cache
// is then wrong until the TTL rescues it.

func runInvalidation(app *core.App, ctx core.IContext) {
	sub := watchConfigChanges(app)
	if err := sub.Start(); err != nil {
		// Subscribe is the one cache operation that fails loudly with no cache,
		// rather than degrading. A read that misses recomputes and is still
		// correct; a subscriber that silently receives nothing forever is a
		// service that looks healthy while doing none of its work.
		ctx.Log().Warn("no pub/sub: in-process copies will not be invalidated", "err", err)
		return
	}
	defer func() { _ = sub.Stop(ctx) }()

	_ = updateUserName(ctx, "42", "ann-the-second")
	publishConfigChange(ctx, 7)

	// Pub/sub has no acknowledgement, so this is a demo affordance, not a
	// pattern: give the handler a moment before the process moves on.
	time.Sleep(50 * time.Millisecond)
}

// updateUserName is the write-through shape, in the order that is safe.
func updateUserName(ctx core.IContext, id, name string) core.IError {
	if err := saveUser(ctx, id, name); err != nil { // 1. the durable write
		return core.Wrap(err, "save user")
	}

	// 2. then the cache. Forget deletes and ignores the error, because failing
	// a request over a failed cache *invalidation* trades a stale read for an
	// outage. Use Del where a stale value is not survivable.
	core.Forget(ctx.Cache(), "user:v1:"+id, "user:v1:lookup:"+id)

	// Derived views live under one prefix so a single call clears all of them.
	// The thing that actually breaks cache invalidation is a value derived from
	// another value, cached under a key nobody remembers to delete.
	if _, err := ctx.Cache().DelByPrefix("user:v1:" + id + ":"); err != nil {
		ctx.Log().Warn("could not clear derived keys", "id", id, "err", err)
	}
	return nil
}

// Versioning is the other answer, and the better one for an entity with many
// derived views: there is no list of keys to keep in sync, and no window where
// half of them are invalidated and half are not.
func permissionsKey(ctx core.IContext, id string) string {
	version, _ := ctx.Cache().Incr("ver:user:"+id, 0, core.NoExpiry) // delta 0 reads
	return fmt.Sprintf("user:v1:%s:v%d:permissions", id, version)
}

// bumpUserVersion makes every derived key of this user unreachable at once. The
// old keys are not deleted — they simply stop being addressed and expire on
// their own, which is why every derived key still needs a TTL.
func bumpUserVersion(ctx core.IContext, id string) {
	_, _ = ctx.Cache().Incr("ver:user:"+id, 1, core.NoExpiry)
}

// Deleting a key in redis invalidates it for everyone, because there is one
// redis. Invalidating something held *in a process* — a config struct, a
// feature-flag map, a compiled template — needs a message, and pub/sub runs on
// the same cache connection, so there is nothing extra to configure.
func watchConfigChanges(app *core.App) core.ISubscriber {
	sub := app.NewSubscriber()
	sub.On("config.changed", func(ctx core.IContext, msg *core.PubSubMessage) error {
		version, err := core.BindMessage[int](msg)
		if err != nil {
			return err
		}
		// Reload rather than trusting the payload: the message says *that*
		// something changed, and the shared store says what it changed to. A
		// handler that applies the payload directly is one dropped message away
		// from a replica that disagrees with the others forever.
		ctx.Log().Info("configuration changed, reloading", "version", version)
		return nil
	})
	return sub
}

func publishConfigChange(ctx core.IContext, version int) {
	core.Forget(ctx.Cache(), "config")                  // the shared copy
	_ = ctx.PubSub().Publish("config.changed", version) // the in-process copies
}

func saveUser(_ core.IContext, _, _ string) error { return nil }

// Two caveats worth carrying:
//
//   - The memory cache's pub/sub is in-process, so an invalidation message
//     reaches this binary and nobody else. It makes the code testable; it does
//     not make it distributed.
//
//   - permissionsKey and bumpUserVersion read and write a counter that has no
//     TTL. That is deliberate — a version that expires silently reuses old key
//     names — but it is also the one key here that leaks if the entity is
//     deleted, so delete it with the entity.
