package main

import (
	"errors"
	"strconv"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/mongorepo"
	"github.com/pskclub/mine-core/v2/utils"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// --- Example 7: transactions ------------------------------------------------
//
// Mongo only offers transactions on a replica set or a sharded cluster. A
// standalone server — including the single-node `docker run mongo` most people
// develop against — refuses, with a message saying so. See main.go for the
// one-liner that starts a single-node replica set instead; without it this
// whole example fails, which is exactly what a service would do in production.
//
// Returning nil from the callback commits; returning an error or panicking
// aborts.

func transactions(ctx core.IContext) core.IError {
	owner, err := mongorepo.New[User](ctx).Eq("email", "repo@example.com").FindOne()
	if err != nil {
		return err
	}

	order := Order{
		UserID:   owner.ID,
		Status:   "paid",
		Total:    99,
		Items:    []OrderItem{{SKU: "SKU-9", Qty: 1, Price: 99}},
		PlacedAt: utils.ToPointer(time.Now()),
	}
	if err := placeOrder(ctx, owner.ID, &order); err != nil {
		return err
	}
	ctx.Log().Info("order committed", "id", order.ID.Hex())

	// Act on a transaction only after it has committed. Publishing inside one
	// lets a subscriber see an event for work that then aborts — and the
	// subscriber has no way to find out.
	//   ctx.PubSub().Publish("order.created", order)

	return rollbackIsRealRollback(ctx)
}

// placeOrder writes two collections that have to agree.
//
// The rule that causes every bug: use the `tx` handle inside. The outer handle
// — ctx.DBMongo(), or any repository built with New inside the closure — is not
// in the transaction, and a write made through it commits immediately and
// survives the abort.
func placeOrder(ctx core.IContext, userID bson.ObjectID, order *Order) core.IError {
	return ctx.DBMongo().Transaction(func(tx core.IMongoDB) error {
		orders := mongorepo.NewIn[Order](tx)
		users := mongorepo.NewIn[User](tx)

		if err := orders.Create(order); err != nil {
			return err
		}
		_, err := users.ByID(userID.Hex()).Inc("order_count", 1)
		return err
	})
}

// rollbackIsRealRollback proves the abort actually rolls back, using a marker
// nothing else writes so a leftover document from a previous run cannot make
// the check pass by accident.
func rollbackIsRealRollback(ctx core.IContext) core.IError {
	marker := "rollback-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	err := ctx.DBMongo().Transaction(func(tx core.IMongoDB) error {
		if _, insertErr := tx.InsertOne("orders", Order{
			Status: marker, PlacedAt: utils.ToPointer(time.Now()),
		}); insertErr != nil {
			return insertErr
		}
		return errors.New("deliberate failure")
	})
	if err == nil {
		return core.New(500, "EXAMPLE_FAILED", "the transaction should have aborted")
	}

	left, countErr := mongorepo.New[Order](ctx).Eq("status", marker).Count()
	if countErr != nil {
		return countErr
	}
	if left != 0 {
		return core.New(500, "EXAMPLE_FAILED", "the aborted insert should be gone")
	}
	ctx.Log().Info("abort rolled the insert back", "marker", marker)
	return nil
}

// singleCollectionTransaction is the shorter spelling when everything happens in
// one collection: the repository binds itself to the session for you.
func singleCollectionTransaction(ctx core.IContext, id string) core.IError {
	return mongorepo.New[User](ctx).Transaction(func(tx *mongorepo.Repo[User]) error {
		if _, err := tx.ByID(id).Inc("logins", 1); err != nil {
			return err
		}
		_, err := tx.ByID(id).Updates(bson.M{"status": "active"})
		return err
	})
}

// rawDriverInsideATransaction is the trap worth knowing about. Every helper on
// the tx handle already runs on the session's context; a *raw driver call* does
// not unless you give it one — and the version that lands outside the
// transaction succeeds, is never rolled back, and logs nothing to distinguish
// itself.
func rawDriverInsideATransaction(ctx core.IContext) core.IError {
	return ctx.DBMongo().Transaction(func(tx core.IMongoDB) error {
		collection := tx.Collection("orders")

		// ✅ in the transaction
		_, err := collection.InsertOne(tx.Context(), Order{Status: "raw", PlacedAt: utils.ToPointer(time.Now())})

		// ❌ silently outside it — no error, no rollback:
		//   collection.InsertOne(context.Background(), order)

		return err
	})
}

// What belongs inside a transaction is only the writes that must succeed or
// fail together. A session holds locks on the documents it touches and has a
// 60-second server limit, so an HTTP call, an upload or a long loop inside one
// turns someone else's latency into your lock-hold time.
//
// And prefer designs that need no transaction at all: $inc and $addToSet are
// already atomic, FindOneAndUpdate claims a document in one operation, and a
// write to a single document never needed a transaction in the first place —
// which is the modelling advantage embedding buys you.
