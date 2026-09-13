package main

import (
	"fmt"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/repository"
)

// --- Example 3: relations, and the N+1 that hides in them -------------------
//
// Preload runs one extra query per relation with an IN (…) — that is what makes
// it the cure for N+1 rather than a cause of it. Joins puts the relation in the
// same query, which is what you need when the *parent* rows are chosen by
// something on the child. Picking the wrong one is not a syntax error; it is a
// hundred queries per request that nobody sees until the table grows.

func relationsTour(ctx core.IContext) core.IError {
	if err := seedRelations(ctx); err != nil {
		return err
	}

	// ❌ 1 + N queries: one for the users, one more for every user found. It is
	// invisible in review and obvious in the log — turn SQL logging on
	// (APP_DB_LOG_LEVEL=info) and watch a burst of near-identical statements.
	users, err := repository.New[User](ctx).Where("email LIKE ?", "rel-%").FindAll()
	if err != nil {
		return err
	}
	for i := range users {
		p, err := repository.New[Profile](ctx).FindOne("user_id = ?", users[i].ID)
		if err == nil {
			users[i].Profile = p
		}
	}

	// ✅ two queries, whatever N is.
	users, err = repository.New[User](ctx).
		Where("email LIKE ?", "rel-%").
		Preload("Profile").
		// Preload what the response renders, not what the struct happens to
		// contain: each nesting level is another query and potentially a lot of
		// rows. A condition on the preload keeps it to the ones that matter.
		Preload("Orders", "status = ?", OrderPaid).
		Preload("Orders.Items").
		FindAll()
	if err != nil {
		return err
	}
	ctx.Log().Info("preloaded", "users", len(users))

	if err := relationsFilterByChild(ctx); err != nil {
		return err
	}
	return relationsAggregate(ctx)
}

// relationsFilterByChild is the case Preload cannot serve: the parents are
// selected by a column on the child, so the child has to be in the same query.
func relationsFilterByChild(ctx core.IContext) core.IError {
	// ❌ pulls every order of every user across the wire in order to throw most
	// of them away, and gets slower every month.
	all, err := repository.New[User](ctx).Preload("Orders").FindAll()
	if err != nil {
		return err
	}
	slow := 0
	for _, u := range all {
		for _, o := range u.Orders {
			if o.TotalSatang > 100_000 {
				slow++
				break
			}
		}
	}

	// ✅ the database decides and sends only the answer. Distinct matters: a
	// join to a has-many multiplies the parent, so a user with three large
	// orders would otherwise appear three times.
	fast, err := repository.New[User](ctx).
		Joins("JOIN orders ON orders.user_id = users.id").
		Where("orders.total_satang > ?", 100_000).
		Distinct("users.*").
		FindAll()
	if err != nil {
		return err
	}
	ctx.Log().Info("big spenders", "in_go", slow, "in_sql", len(fast))

	// For a belongs-to or has-one you also want to display, InnerJoins by
	// relation name populates the struct in the same query — no second round
	// trip. A has-many still wants Preload; a join would multiply the rows.
	orders, err := repository.New[Order](ctx).
		InnerJoins("User").
		Where(`"User".status = ?`, UserActive).
		FindAll()
	if err != nil {
		return err
	}
	ctx.Log().Info("orders of active users", "count", len(orders))
	return nil
}

// relationsAggregate counts children without loading any of them.
func relationsAggregate(ctx core.IContext) core.IError {
	type userRow struct {
		ID     string
		Name   string
		Orders int64
		Spent  int64
	}
	var rows []userRow

	// LEFT JOIN keeps users with no paid order; coalesce turns their NULL sum
	// into 0. Both are easy to leave out and both change the answer.
	if err := repository.New[User](ctx).
		Select(`users.id, users.name,
		        count(orders.id) as orders,
		        coalesce(sum(orders.total_satang), 0) as spent`).
		Joins("LEFT JOIN orders ON orders.user_id = users.id AND orders.status = ?", OrderPaid).
		Group("users.id, users.name").
		Scan(&rows); err != nil {
		return err
	}
	ctx.Log().Info("spend report", "rows", len(rows))
	return nil
}

func seedRelations(ctx core.IContext) core.IError {
	users := repository.New[User](ctx)
	for i := 0; i < 3; i++ {
		u := User{
			Email:   fmt.Sprintf("rel-%d@example.com", i),
			Name:    fmt.Sprintf("Rel %d", i),
			Status:  UserActive,
			Profile: &Profile{City: "BKK"},
			Orders: []Order{
				{Status: OrderPaid, TotalSatang: int64(50_000 * (i + 1)),
					Items: []OrderItem{{SKU: "sku-a", Qty: 1, PriceSatang: 50_000}}},
				{Status: OrderPending, TotalSatang: 10_000},
			},
		}
		// Create on a parent with populated children inserts the children too,
		// in one transaction. Convenient — and easy to trigger by accident:
		// loading with Preload and then calling Save rewrites the children as
		// well, unless you Omit(clause.Associations).
		if err := users.Create(&u); err != nil {
			return err
		}
	}
	return nil
}
