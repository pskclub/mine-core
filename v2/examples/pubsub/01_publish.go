package main

import (
	"context"

	"gorm.io/gorm"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/repository"
)

// --- Example 1: publishing ---------------------------------------------------
//
// A publish is a fan-out to whoever happens to be listening at that instant.
// Nothing is stored, nothing is acknowledged, nothing is redelivered. So a nil
// error carries almost no information: redis accepted the message. It does not
// mean anybody was listening (nobody may have been, and the message is then
// gone), that anybody handled it, or that anybody ever will — and on a service
// with no cache configured the publish is dropped silently and still returns
// nil.
//
// Which is why the shape below is the one to reach for nearly every time: the
// row is the work, the message is only a hint that the row is there. A
// subscriber that misses it still finds the row; one that receives it just finds
// it sooner.

type User struct {
	ID   string `gorm:"primaryKey" json:"id"`
	Name string `json:"name"`
}

func (User) TableName() string { return "users" }

// UpdateUser writes first and publishes second.
func UpdateUser(ctx core.IContext, user *User) core.IError {
	if err := repository.New[User](ctx).Save(user); err != nil {
		return err
	}

	// The id, not the document.
	//
	// The subscriber then reads whatever is true when it runs, so two updates
	// handled out of order still converge on the right answer, and nothing
	// sensitive travels through a channel anyone with redis access can read. The
	// whole document saves the subscriber a read but is a snapshot: of two rapid
	// updates, the older can be the one that lands last and wins.
	ctx.PubSub().Publish("user.updated", user.ID)
	return nil
}

// CreateUserInTransaction is the ordering that matters, spelled out.
//
// A message published inside a transaction escapes immediately — redis knows
// nothing about the database's transaction — so a subscriber can receive
// "user.created", go looking for the row, and not find it, because the commit
// has not happened yet (or never will).
func CreateUserInTransaction(ctx core.IContext, user *User) core.IError {
	if err := repository.New[User](ctx).Transaction(func(tx *gorm.DB) error {
		return repository.NewWithDB[User](ctx, tx).Create(user)
	}); err != nil {
		return err
	}
	// Outside the transaction, once the row is definitely there.
	ctx.PubSub().Publish("user.created", user.ID)
	return nil
}

// publishBulk is the difference between one round trip and fifty thousand.
//
// It is also better for the subscriber: one handler invocation that can do one
// bulk invalidation, instead of fifty thousand that each do one.
func publishBulk(ctx core.IContext, ids []string) {
	ctx.PubSub().Publish("users.bulk-updated", ids)
}

// publishFromBackground publishes from a goroutine that outlives the request.
//
// ctx.PubSub() is bound to the request's context, so without a longer-lived one
// the publish is cancelled the moment the request finishes.
func publishFromBackground(ctx core.IContext, importID string) {
	pub := ctx.PubSub().WithContext(context.Background())
	go func() {
		if err := pub.Publish("import.finished", importID); err != nil {
			// There is no request left to return an error to.
			ctx.Log().Warn("pubsub: publish failed", "channel", "import.finished", "err", err)
		}
	}()
}

// wireName reveals the name a channel actually travels under.
//
// CACHE_PREFIX namespaces channels as well as keys, which is what stops a
// staging deploy from publishing into production's channels on a shared redis.
// Both ends of this framework see the plain name, so the prefix is invisible —
// until the other end is a Node service, redis-cli or a dashboard subscribing
// directly, which is what this is for.
//
//	redis-cli SUBSCRIBE myservice:user.updated
func wireName(ctx core.IContext, channel string) string {
	return ctx.PubSub().Channel(channel)
}

// Naming, worth settling once:
//
//	user.updated          entity.past-tense-verb — a statement anybody may act on
//	order.status.changed  dots, hierarchically, most general first
//	cache.flush           imperative for a command, not an event
//
// Past tense for events and imperative for commands, kept apart: a channel that
// mixes them ends with subscribers disagreeing about whose job it is to act.
// Dots first-most-general is what makes patterns useful — "order.*" only catches
// everything about orders because the entity comes first.
//
// Keep ids out of channel names ("user.42.updated"). Redis copes, but every
// subscriber then needs a pattern subscription, and the id belongs in the
// payload where it can have a type.
