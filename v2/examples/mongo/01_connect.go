package main

import (
	"errors"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// --- Example 1: connecting, and what "not configured" looks like ------------
//
// core.NewMongoDB pings before it returns, so a wrong password or an
// unreachable replica set is a boot failure rather than a 500 on the first
// request that happens to need a document. The App then owns the handle:
// app.Shutdown closes it, and nothing else should.

// User is the document the rest of this topic reads and writes.
//
// CollectionName on the *value* — not on *User — is what satisfies
// core.IDocument, and it is what makes mongorepo.New[User](ctx) compile.
//
// ID is a bson.ObjectID rather than a string on purpose. Driver v2 refuses to
// decode a Mongo-generated ObjectID into a Go string unless the client opts in
// (Decoder.ObjectIDAsHexString), which core does not — so `ID string` only
// works for a collection whose ids you generate yourself. mongorepo fills in
// either spelling after Create, and core.MongoByID takes the hex form of both.
// The omitempty is load-bearing: without it a zero id is sent to the server and
// the generated one never happens.
type User struct {
	ID        bson.ObjectID `bson:"_id,omitempty"        json:"id"`
	Email     string        `bson:"email"                json:"email"`
	Name      string        `bson:"name"                 json:"name"`
	Status    string        `bson:"status"               json:"status"`
	Age       int64         `bson:"age"                  json:"age"`
	Tags      []string      `bson:"tags"                 json:"tags"`
	Logins    int64         `bson:"logins"               json:"logins"`
	Orders    int64         `bson:"order_count"          json:"order_count"`
	Joined    *time.Time    `bson:"joined"               json:"joined"`
	DeletedAt *time.Time    `bson:"deleted_at,omitempty" json:"deleted_at,omitempty"`
}

func (User) CollectionName() string { return "users" }

// OrderItem is an embedded document — the shape ElemMatch exists for.
type OrderItem struct {
	SKU   string  `bson:"sku"   json:"sku"`
	Qty   int64   `bson:"qty"   json:"qty"`
	Price float64 `bson:"price" json:"price"`
}

// Order is the second collection, so the examples have something to $lookup and
// two collections to write in one transaction.
type Order struct {
	ID       bson.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID   bson.ObjectID `bson:"user_id"       json:"user_id"`
	Status   string        `bson:"status"        json:"status"`
	Total    float64       `bson:"total"         json:"total"`
	Items    []OrderItem   `bson:"items"         json:"items"`
	PlacedAt *time.Time    `bson:"placed_at"     json:"placed_at"`
}

func (Order) CollectionName() string { return "orders" }

// newApp connects and registers the connection as "default", which is what
// ctx.DBMongo() resolves. A second cluster is a second core.WithMongo under its
// own name, read back with ctx.DBSMongo(name).
func newApp(env core.IENV) (*core.App, core.IError) {
	m, err := core.NewMongoDB(env,
		// 200ms is already the default; naming it here is a reminder that every
		// command slower than this is logged, which is the cheapest query budget
		// a service can have.
		core.WithMongoSlowQuery(200*time.Millisecond),
	)
	if err != nil {
		// Nothing to fall back to: Mongo is not optional for a service that
		// stores its data there, and a handle that pretends otherwise only moves
		// the failure somewhere less obvious.
		return nil, err
	}
	return core.NewApp(env, core.WithMongo("default", m))
}

// mongoIsNeverNil is this example's entry point: what an *unconfigured* Mongo
// does when someone uses it anyway.
//
// Capabilities are never nil in v2, but they do not all behave the same way
// when they are missing. The cache degrades silently — a miss just recomputes.
// Mongo does not: a read that quietly returns nothing and a write that quietly
// goes nowhere are both worse than a clear failure, so every call fails with
// MONGO_DISABLED naming the configuration that is missing.
func mongoIsNeverNil(ctx core.IContext) core.IError {
	// "audit" was never registered, so this is the disabled handle rather than
	// nil: the failure is an error you can read instead of a nil-interface panic
	// in whichever line happened to touch it first.
	audit := ctx.DBSMongo("audit")

	_, err := audit.Count("events", nil)
	if !errors.Is(err, core.ErrMongoDisabled) {
		return core.New(500, "EXAMPLE_FAILED",
			"an unregistered connection should refuse every call")
	}
	ctx.Log().Info("unregistered connection refuses loudly",
		"enabled", audit.Enabled(), "code", err.GetCode())

	// The honest branch, for a path that can genuinely run without Mongo.
	if !ctx.DBMongo().Enabled() {
		return core.New(503, "EXAMPLE_FAILED", "the default connection should be enabled here")
	}

	// Ping is the readiness check. Housekeeping commands like this one are kept
	// out of the query log, so a probe running every few seconds does not bury
	// the queries the service actually ran.
	return ctx.DBMongo().Ping()
}

// reportingHandle sends heavy, staleness-tolerant reads to a secondary without
// changing the connection every other query uses.
//
// WithReadPreference returns a new handle; it does not mutate the one it was
// called on. A read from a secondary is a read from *behind* the primary —
// right for a dashboard, wrong for "insert it, then read it back".
func reportingHandle(ctx core.IContext) core.IMongoDB {
	return ctx.DBMongo().WithReadPreference(readpref.SecondaryPreferred())
}
