package main

import (
	"errors"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/utils"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// --- Example 2: the driver layer -------------------------------------------
//
// core.IMongoDB is a thin set of helpers over the official driver: filters are
// bson.M, results decode into your structs, failures come back as core.IError.
// It is what mongorepo is built on, and what to reach for when a query does not
// fit a repository. The handle from ctx.DBMongo() is already bound to the
// request context: no method takes a ctx, and a cancelled request cancels the
// query it started.

func driverLayer(ctx core.IContext) core.IError {
	m := ctx.DBMongo()

	id, err := driverInsert(m)
	if err != nil {
		return err
	}
	if err := driverRead(ctx, m, id); err != nil {
		return err
	}
	if err := driverUpdate(ctx, m, id); err != nil {
		return err
	}
	return driverClaim(ctx, m)
}

// driverInsert returns the new id as a hex string, ready to hand to a filter —
// which is the whole point of returning it rather than the driver's own value.
func driverInsert(m core.IMongoDB) (string, core.IError) {
	id, err := m.InsertOne("users", User{
		Email:  "driver@example.com",
		Name:   "Driver Example",
		Status: "active",
		Joined: utils.ToPointer(time.Now()),
	})
	// A unique-index collision is a 409 with an answer to give, not a 500.
	if errors.Is(err, core.ErrDuplicateKey) {
		var existing User
		findErr := m.FindOne(&existing, "users", bson.M{"email": "driver@example.com"})
		if findErr != nil {
			return "", findErr
		}
		return existing.ID.Hex(), nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func driverRead(ctx core.IContext, m core.IMongoDB, id string) core.IError {
	// MongoByID parses the hex into an ObjectID. A filter built from the raw
	// string matches nothing, silently — the classic way to lose an afternoon.
	var user User
	if err := m.FindOne(&user, "users", core.MongoByID(id)); err != nil {
		return err
	}

	var active []User
	if err := m.Find(&active, "users", bson.M{"status": "active"}, core.MongoFindOptions{
		Sort:       []string{"-joined", "name"}, // "-" is descending
		Limit:      20,                          // without one, Find reads the whole result set
		Projection: bson.M{"tags": 0},
		MaxTime:    5 * time.Second, // bounded on the *server*, not merely abandoned here
	}); err != nil {
		return err
	}

	// Count is exact and walks the index; EstimatedCount reads metadata —
	// instant, and approximate after an unclean shutdown.
	exact, err := m.Count("users", bson.M{"status": "active"})
	if err != nil {
		return err
	}
	rough, err := m.EstimatedCount("users")
	if err != nil {
		return err
	}

	var statuses []string
	if err := m.Distinct(&statuses, "users", "status", nil); err != nil {
		return err
	}

	ctx.Log().Info("driver reads", "found", user.Name, "active", len(active),
		"count", exact, "estimated", rough, "statuses", statuses)
	return nil
}

func driverUpdate(ctx core.IContext, m core.IMongoDB, id string) core.IError {
	// An update takes operators, not a document: bson.M{"logins": 1} without a
	// $set is a replace in disguise and the driver rejects it.
	res, err := m.UpdateOne("users", core.MongoByID(id), bson.M{
		"$set": bson.M{"status": "active"},
		"$inc": bson.M{"logins": 1},
	})
	if err != nil {
		return err
	}
	// Matched == 0 is "no such document"; Matched > 0 && Modified == 0 is "found
	// it, already correct". Telling those apart is why writes return a result.
	ctx.Log().Info("driver update", "matched", res.Matched, "modified", res.Modified)

	// One round trip for many independent writes. Unordered keeps going after a
	// failure — one bad document should not abandon the other nine hundred.
	bulk, err := m.BulkWrite("users", []mongo.WriteModel{
		mongo.NewUpdateManyModel().
			SetFilter(bson.M{"status": "new"}).
			SetUpdate(bson.M{"$set": bson.M{"status": "active"}}),
		mongo.NewDeleteManyModel().SetFilter(bson.M{"status": "expired"}),
	}, false)
	if err != nil {
		return err
	}
	ctx.Log().Info("driver bulk", "modified", bulk.Modified, "deleted", bulk.Deleted)
	return nil
}

// driverClaim is the read-then-write that must not be two operations: a Find
// followed by an Update lets two workers read the same document and both decide
// they own it. FindOneAndUpdate does both in one, so exactly one wins.
func driverClaim(ctx core.IContext, m core.IMongoDB) core.IError {
	var claimed User
	err := m.FindOneAndUpdate(&claimed, "users",
		bson.M{"status": "active"},
		bson.M{"$inc": bson.M{"logins": 1}},
		core.MongoFindModifyOptions{
			Sort:      []string{"joined"}, // which one, when several match
			ReturnNew: true,               // decode the document *after* the change
		})
	if errors.Is(err, core.ErrDocumentNotFound) {
		ctx.Log().Info("nothing to claim") // an empty queue is not a failure
		return nil
	}
	if err != nil {
		return err
	}
	ctx.Log().Info("claimed", "user", claimed.Name, "logins", claimed.Logins)
	return nil
}
