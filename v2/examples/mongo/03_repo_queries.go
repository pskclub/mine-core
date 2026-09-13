package main

import (
	"errors"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/mongorepo"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// --- Example 3: the typed repository ---------------------------------------
//
// mongorepo.Repo[D] is the same shape as the SQL repository: the context is
// bound once at New, the finishers take no ctx, and every chaining method
// clones — so a base scope can be branched twice without the first branch
// leaking into the second.
//
// It is a layer over core.IMongoDB, not a replacement: DB() gives the handle
// back and Collection() gives the driver's, so nothing is out of reach.

func repoQueries(ctx core.IContext) core.IError {
	users := mongorepo.New[User](ctx)

	// One base scope, reused below. Because the chain is copy-on-write this is a
	// value, not a builder someone else can spoil.
	active := users.Eq("status", "active").HasField("deleted_at", false)

	recent, err := active.
		Between("joined", time.Now().AddDate(0, 0, -30), time.Now()).
		Sort("-joined").
		Limit(20).
		Omit("tags"). // projection: keep the bulky fields out of a list read
		FindAll()
	if err != nil {
		return err
	}

	// In with no values matches nothing, deliberately: that is what the caller
	// asked for, so it is not silently dropped into "match everything".
	invited, err := users.In("status", "invited", "pending").Count()
	if err != nil {
		return err
	}

	// A miss is a 404 wrapping core.ErrDocumentNotFound, not a nil document —
	// so the "not there" branch is explicit rather than a nil check that
	// someone forgets.
	one, err := active.Sort("-joined").FindOne()
	switch {
	case errors.Is(err, core.ErrDocumentNotFound):
		ctx.Log().Info("no active user yet")
	case err != nil:
		return err
	default:
		ctx.Log().Info("newest active user", "name", one.Name, "id", one.ID.Hex())
	}

	ctx.Log().Info("repo reads", "recent", len(recent), "invited", invited)
	if err := repoNested(ctx); err != nil {
		return err
	}

	page, err := listUsers(ctx, &core.PageOptions{Page: 1, Limit: 20, Q: "example"})
	if err != nil {
		return err
	}
	exported, err := exportUsers(ctx)
	if err != nil {
		return err
	}
	ctx.Log().Info("repo paging", "total", page.Total, "on_page", page.Count, "exported", exported)
	return nil
}

// repoNested is where the typed operators earn their keep: the conditions that
// are easy to get subtly wrong when written as bson.M by hand.
func repoNested(ctx core.IContext) core.IError {
	orders := mongorepo.New[Order](ctx)

	// ElemMatch is the difference a nested query usually turns on. A plain
	// dotted filter on two fields of the same array can be satisfied by two
	// *different* elements:
	//
	//	Eq("items.sku", "A").Gte("items.qty", 2)   → element 1 is A, element 2 has qty 2
	//	ElemMatch("items", …)                      → one element is both
	bulk, err := orders.
		ElemMatch("items", bson.M{"sku": "SKU-1", "qty": bson.M{"$gte": 2}}).
		FindAll()
	if err != nil {
		return err
	}

	// Search is the "q" of a list endpoint: a case-insensitive substring ORed
	// across the fields, with the term escaped so a user cannot turn it into a
	// pattern of their own choosing. Only a prefix-anchored pattern can use an
	// index, so this is a convenience for small collections, not a search engine.
	found, err := mongorepo.New[User](ctx).
		Eq("status", "active").
		Search("exam", "name", "email").
		Count()
	if err != nil {
		return err
	}

	// Pluck reads one field of every match without decoding whole documents —
	// how you gather ids to hand to the next query.
	var emails []string
	if err := mongorepo.New[User](ctx).Eq("status", "active").
		Limit(100).Pluck("email", &emails); err != nil {
		return err
	}

	ctx.Log().Info("repo nested", "bulk_orders", len(bulk), "matched_q", found, "emails", len(emails))
	return nil
}

// listUsers is the shape an HTTP handler actually has: options straight from
// the request, one call, a *core.Page ready to return.
//
// GetPageOptionsWithAllowed is what keeps order_by from being whatever the
// caller typed. A Sort on the chain is only the *default* — an explicit OrderBy
// in the options wins, which is what makes the endpoint sortable at all.
func listUsers(ctx core.IContext, opts *core.PageOptions) (*core.Page[User], core.IError) {
	return mongorepo.New[User](ctx).
		Eq("status", "active").
		Search(opts.Q, "name", "email").
		Sort("-joined").
		Pagination(opts)
}

// exportUsers streams instead of collecting. FindAll without a Limit reads the
// whole result set into memory; Each never holds more than a batch, which is
// the difference between an export that runs and one that gets OOM-killed at
// three in the morning.
func exportUsers(ctx core.IContext) (int, core.IError) {
	count := 0
	err := mongorepo.New[User](ctx).
		Eq("status", "active").
		Select("_id", "email"). // read only what the export writes
		Each(func(u User) error {
			count++
			return nil // returning an error here stops the iteration
		})
	return count, err
}
