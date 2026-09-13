package main

import (
	"context"
	"errors"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/mongorepo"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// --- Example 6: indexes and change streams ---------------------------------
//
// Unlike SQL, where migrations own the schema, Mongo indexes are declared in
// code and applied at boot. That works because creating one is idempotent and
// there is no schema to drift from — but it also means a *removed* MongoIndex
// is not dropped. Retiring an index is a deliberate DropIndex.

func indexesAndStreams(ctx core.IContext) core.IError {
	if err := ensureIndexes(ctx); err != nil {
		return err
	}
	if err := listIndexes(ctx); err != nil {
		return err
	}
	// A change stream needs a replica set. On a standalone server this fails
	// immediately with a message saying so, which is why main treats a failed
	// step as information rather than as a reason to stop.
	return watchUsers(ctx, 2*time.Second)
}

// ensureIndexes is safe to run on every boot.
func ensureIndexes(ctx core.IContext) core.IError {
	if err := mongorepo.New[User](ctx).EnsureIndexes(
		// A unique index is the only thing that actually enforces uniqueness —
		// a check-then-insert in application code loses the race. Partial keeps
		// it honest alongside soft deletes: a deleted user should not keep
		// holding the email address forever.
		core.MongoIndex{
			Keys:    []string{"email"},
			Unique:  true,
			Partial: bson.M{"deleted_at": nil},
		},
		// Equality, then sort, then range. This serves Eq("status", …) and
		// Eq("status", …).Sort("-joined"); it does not serve Sort("-joined")
		// on its own, and no amount of hoping changes that.
		core.MongoIndex{Keys: []string{"status", "-joined"}},
		// A non-numeric direction goes after a colon. One array field per
		// compound index — {tags, items.sku} is rejected, because the number of
		// index entries would be the product of the two arrays.
		core.MongoIndex{Keys: []string{"tags"}},
		core.MongoIndex{Keys: []string{"name:text"}},
	); err != nil {
		return err
	}

	// Through the driver layer, for a collection with no document type of its
	// own. TTL is the right answer for sessions, one-time tokens and rate-limit
	// rows; it is the wrong answer for data with a retention *policy*, because
	// it deletes silently and leaves no record that it did.
	return ctx.DBMongo().EnsureIndexes("sessions",
		core.MongoIndex{Keys: []string{"token"}, Unique: true},
		core.MongoIndex{Keys: []string{"created_at"}, TTL: 24 * time.Hour},
	)
}

// listIndexes is what to run before deleting one: dropping an index a query
// depends on turns that query into a collection scan, quietly, under production
// load.
func listIndexes(ctx core.IContext) core.IError {
	list, err := ctx.DBMongo().ListIndexes("users")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(list))
	for _, index := range list {
		if name, ok := index["name"].(string); ok {
			names = append(names, name)
		}
	}
	ctx.Log().Info("user indexes", "names", names)
	return nil
}

// watchUsers is a live feed of the writes to a collection.
//
// The repository re-points its filter at fullDocument when it builds the
// pipeline, so Eq("status", "active").Watch() means what it reads like — the
// changes to active users, not the events whose top level has a status field.
//
// budget exists because an example has to end. A real subscriber runs until the
// process stops, and stores the resume token so it can reopen where it left off.
func watchUsers(ctx core.IContext, budget time.Duration) core.IError {
	stream, err := mongorepo.New[User](ctx).Eq("status", "active").Watch()
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close(ctx) }()

	watchCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	seen := 0
	for stream.Next(watchCtx) {
		var event struct {
			OperationType string `bson:"operationType"`
			FullDocument  User   `bson:"fullDocument"`
		}
		if decodeErr := stream.Decode(&event); decodeErr != nil {
			return core.Wrap(decodeErr, "mongo: decode change event")
		}
		seen++
		ctx.Log().Info("user changed",
			"op", event.OperationType, "email", event.FullDocument.Email)
	}
	ctx.Log().Info("change stream closed", "events", seen)

	// A change stream is a live feed, not a queue: no acknowledgement, no retry,
	// no dead letter. A consumer that goes away loses whatever happened while it
	// was gone. If losing an event would be a bug, write the fact down durably
	// and let a job act on it.
	//
	// The deadline this example imposed on itself is not one of those failures,
	// so it is not reported as one.
	if streamErr := stream.Err(); streamErr != nil && !errors.Is(streamErr, context.DeadlineExceeded) {
		return core.Wrap(streamErr, "mongo: change stream")
	}
	return nil
}
