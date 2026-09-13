package main

import (
	"errors"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/mongorepo"
	"github.com/pskclub/mine-core/v2/utils"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// --- Example 4: writing through the repository -----------------------------
//
// Every write reports what it actually did (Matched, Modified, UpsertedID)
// rather than only whether it failed: "no such document" and "found it, nothing
// to change" are different answers, and only the caller knows which matters.

func repoWrites(ctx core.IContext) core.IError {
	users := mongorepo.New[User](ctx)

	u := User{
		Email:  "repo@example.com",
		Name:   "Repo Example",
		Status: "active",
		Age:    41,
		Tags:   []string{"seed"},
		Joined: utils.ToPointer(time.Now()),
	}
	// Create fills the generated id back into the document, the way GORM fills a
	// primary key — so the caller can use it without a second read.
	err := users.Create(&u)
	if errors.Is(err, core.ErrDuplicateKey) {
		// The unique index is the only thing that actually enforces uniqueness;
		// a check-then-insert loses the race that matters.
		found, findErr := users.Eq("email", u.Email).FindOne()
		if findErr != nil {
			return findErr
		}
		u = *found
	} else if err != nil {
		return err
	}
	ctx.Log().Info("created", "id", u.ID.Hex())

	if err := repoAtomicWrites(ctx, u.ID.Hex()); err != nil {
		return err
	}
	return repoUpsertAndDelete(ctx)
}

// repoAtomicWrites is the set of updates that need no transaction at all: each
// is a single-document operation, and a write to one document is already
// atomic. Reaching for a transaction here is a habit carried over from SQL.
func repoAtomicWrites(ctx core.IContext, id string) core.IError {
	user := mongorepo.New[User](ctx).ByID(id)

	// $inc, not read-modify-write: two requests can increment at once without
	// one of them losing its update.
	if _, err := user.Inc("logins", 1); err != nil {
		return err
	}
	// AddToSet is Push that skips values already there.
	if _, err := user.AddToSet("tags", "returning", "seed"); err != nil {
		return err
	}
	if _, err := user.Pull("tags", "stale"); err != nil {
		return err
	}
	// Unset removes the field rather than zeroing it — which is what "not
	// deleted" has to look like for the partial unique index in Example 6.
	if _, err := user.Unset("deleted_at"); err != nil {
		return err
	}

	// A plain map is wrapped in $set; a map that already speaks in operators is
	// sent as written. Both spellings work, so nothing has to be un-learned.
	res, err := user.Updates(bson.M{"status": "active", "name": "Repo Example"})
	if err != nil {
		return err
	}
	ctx.Log().Info("updated", "matched", res.Matched, "modified", res.Modified)

	// Claiming a document: one operation, so exactly one worker wins.
	claimed, err := mongorepo.New[User](ctx).
		Eq("status", "active").
		Sort("joined").
		FindOneAndUpdate(bson.M{"$inc": bson.M{"logins": 1}})
	if errors.Is(err, core.ErrDocumentNotFound) {
		return nil // nothing to claim is not a failure
	}
	if err != nil {
		return err
	}
	ctx.Log().Info("claimed", "user", claimed.Name, "logins", claimed.Logins)
	return nil
}

func repoUpsertAndDelete(ctx core.IContext) core.IError {
	users := mongorepo.New[User](ctx)

	// Upsert is the idempotent write: insert when the filter matches nothing,
	// update when it does. The filter's own fields land on the inserted
	// document, which is why the email is not repeated in the values.
	res, err := users.Eq("email", "upsert@example.com").Upsert(bson.M{
		"name":   "Upsert Example",
		"status": "invited",
		"joined": time.Now(),
	})
	if err != nil {
		return err
	}
	ctx.Log().Info("upserted", "matched", res.Matched, "upserted_id", res.UpsertedID)

	// Save replaces the whole document: every field not on the struct is gone.
	invited, err := users.Eq("email", "upsert@example.com").FindOne()
	if err != nil {
		return err
	}
	invited.Status = "active"
	if err := users.ByID(invited.ID.Hex()).Save(invited); err != nil {
		return err
	}

	// A scope-less Delete would empty the collection, so it is refused: that is
	// almost always a filter that was forgotten. DeleteAll is the deliberate
	// version.
	if _, err := users.Delete(); err == nil {
		return core.New(500, "EXAMPLE_FAILED", "an unscoped Delete must be refused")
	}

	n, err := users.Eq("status", "expired").Delete()
	if err != nil {
		return err
	}
	ctx.Log().Info("deleted", "count", n)
	return nil
}

// seedOrders gives the aggregation example something to read. CreateMany is one
// round trip for many documents, and it returns the generated ids in order.
func seedOrders(ctx core.IContext, userID bson.ObjectID) ([]string, core.IError) {
	return mongorepo.New[Order](ctx).CreateMany([]Order{
		{UserID: userID, Status: "paid", Total: 250, PlacedAt: utils.ToPointer(time.Now()),
			Items: []OrderItem{{SKU: "SKU-1", Qty: 2, Price: 125}}},
		{UserID: userID, Status: "paid", Total: 80, PlacedAt: utils.ToPointer(time.Now()),
			Items: []OrderItem{{SKU: "SKU-2", Qty: 1, Price: 80}}},
		{UserID: userID, Status: "cancelled", Total: 40, PlacedAt: utils.ToPointer(time.Now()),
			Items: []OrderItem{{SKU: "SKU-3", Qty: 1, Price: 40}}},
	})
}
