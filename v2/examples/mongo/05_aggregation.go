package main

import (
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/mongorepo"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// --- Example 5: typed aggregation pipelines --------------------------------
//
// mongorepo.Aggregate[Row](repo) is typed twice over: D is the document it
// reads, Row is what each result decodes into. The repository's own chain
// becomes the leading $match, so a scope and a pipeline compose instead of
// being two different ways to say "active users".
//
// Nothing is hidden — Stage(bson.M{…}) appends anything the builder does not
// name ($setWindowFields, Atlas $search), and Stages() hands the pipeline back.

type revenueRow struct {
	Status string  `bson:"_id"`
	Total  float64 `bson:"total"`
	Orders int64   `bson:"orders"`
}

type spenderRow struct {
	ID    bson.ObjectID `bson:"_id"`
	Email string        `bson:"email"`
	Spent float64       `bson:"spent"`
}

type orderRow struct {
	ID        bson.ObjectID `bson:"_id"`
	Status    string        `bson:"status"`
	Total     float64       `bson:"total"`
	UserEmail string        `bson:"user_email"`
}

func aggregations(ctx core.IContext) core.IError {
	if err := ensureOrders(ctx); err != nil {
		return err
	}

	revenue, err := revenueByStatus(ctx, time.Now().AddDate(0, 0, -30))
	if err != nil {
		return err
	}
	spenders, err := topSpenders(ctx, 5)
	if err != nil {
		return err
	}
	page, err := orderPage(ctx, &core.PageOptions{Page: 1, Limit: 20, OrderBy: []string{"-total"}})
	if err != nil {
		return err
	}

	ctx.Log().Info("aggregations",
		"revenue_groups", len(revenue), "spenders", len(spenders),
		"orders_total", page.Total, "orders_on_page", page.Count)
	return nil
}

// revenueByStatus is the plain grouping case.
//
// The $match comes from the repository chain and therefore runs *first*, which
// is the whole performance story of an aggregation: a $match before a $group is
// an index read, the same $match after it is a collection scan.
func revenueByStatus(ctx core.IContext, since time.Time) ([]revenueRow, core.IError) {
	return mongorepo.Aggregate[revenueRow](
		mongorepo.New[Order](ctx).Gte("placed_at", since),
	).
		Group("$status", bson.M{
			"total":  bson.M{"$sum": "$total"},
			"orders": bson.M{"$sum": 1},
		}).
		Sort("-total").
		// $group and $sort get 100MB of memory and then fail. AllowDiskUse is
		// the difference between a report that keeps working as the data grows
		// and one that starts failing with "Sort exceeded memory limit".
		AllowDiskUse().
		// The comment shows up in currentOp and the profiler — how a slow
		// pipeline is identified in production without guessing.
		Comment("revenue-by-status").
		All()
}

// topSpenders joins. Unwind's preserveEmpty is the flag that decides whether
// users with no orders disappear: without it the join silently drops them and
// the report under-counts in a way nobody notices until someone complains.
func topSpenders(ctx core.IContext, n int64) ([]spenderRow, core.IError) {
	return mongorepo.Aggregate[spenderRow](
		mongorepo.New[User](ctx).Eq("status", "active"),
	).
		Lookup(mongorepo.Lookup{
			From: "orders", LocalField: "_id", ForeignField: "user_id", As: "orders",
		}).
		Unwind("$orders", true).
		Group("$_id", bson.M{
			"email": bson.M{"$first": "$email"},
			"spent": bson.M{"$sum": "$orders.total"},
		}).
		Sort("-spent").
		Limit(n).
		All()
}

// orderPage pages the *output of a pipeline* rather than of a filter — a list
// with a joined column, which no repository chain can express.
//
// Build it without $sort/$skip/$limit: Page appends them from the options. A
// page that fits comes back in one pass via $facet, so the count cannot drift
// from the items; a page too large for the single document $facet builds costs
// a second round trip instead.
func orderPage(ctx core.IContext, opts *core.PageOptions) (*core.Page[orderRow], core.IError) {
	return mongorepo.Aggregate[orderRow](mongorepo.New[Order](ctx)).
		// LookupOne is Lookup plus the unwind that turns a one-element array
		// into an embedded document — the shape a belongs-to join is wanted in.
		LookupOne(mongorepo.Lookup{
			From: "users", LocalField: "user_id", ForeignField: "_id", As: "user",
		}).
		Project(bson.M{
			"status":     1,
			"total":      1,
			"user_email": "$user.email",
		}).
		Page(opts)
}

// ensureOrders seeds the collection the first time this example runs, so the
// pipelines above have something to aggregate.
func ensureOrders(ctx core.IContext) core.IError {
	n, err := mongorepo.New[Order](ctx).Count()
	if err != nil || n > 0 {
		return err
	}
	owner, err := mongorepo.New[User](ctx).Eq("email", "repo@example.com").FindOne()
	if err != nil {
		return err
	}
	ids, err := seedOrders(ctx, owner.ID)
	if err != nil {
		return err
	}
	ctx.Log().Info("seeded orders", "count", len(ids))
	return nil
}
