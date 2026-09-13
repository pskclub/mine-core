package main

import (
	"errors"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
	"github.com/pskclub/mine-core/v2/repository"
	"gorm.io/gorm"
)

// --- Example 2: create, read, update, delete --------------------------------
//
// repository.New[M](ctx) takes both the connection and the deadline from the
// context, so no query method needs a ctx argument — and a client that hangs up
// cancels the query it started. Nothing here returns a raw error: a missing row
// is errmsgs.NotFound, everything else is DATABASE_ERROR, and both already
// carry the status the HTTP layer will use.

func crudTour(ctx core.IContext) core.IError {
	users := repository.New[User](ctx)

	// Create takes a pointer because the driver writes back into it: the id from
	// BeforeCreate and the timestamps GORM fills are only visible that way.
	u := User{Email: "crud-ann@example.com", Name: "Ann", Status: UserTrial, CreditsSatang: 25_000}
	if err := users.Create(&u); err != nil {
		return err
	}
	ctx.Log().Info("created", "id", u.ID, "created_at", u.CreatedAt)

	// A missing row is a named outcome, not a driver sentinel. errors.Is works
	// through the wrapping, so this holds however the error travelled.
	if _, err := users.Where("email = ?", "nobody@example.com").FindOne(); !errors.Is(err, errmsgs.NotFound) {
		return core.Newf(500, "EXAMPLE_FAILED", "expected NOT_FOUND, got %v", err)
	}

	// Count, Exists and FindAll never report emptiness as an error — zero rows
	// is an answer. Only FindOne, Take and Last can miss.
	n, err := users.Where("status = ?", UserTrial).Count()
	if err != nil {
		return err
	}
	ctx.Log().Info("trial users", "count", n)

	if err := crudCopyOnWrite(ctx); err != nil {
		return err
	}
	return crudUpdateAndDelete(ctx, u.ID)
}

// crudCopyOnWrite is the property that removes a whole class of v1 bugs: every
// chainable method clones, so a base query can be branched without one branch's
// conditions leaking into the next.
func crudCopyOnWrite(ctx core.IContext) core.IError {
	base := repository.New[User](ctx).Where("credits_satang > ?", 0)

	trial, err := base.Where("status = ?", UserTrial).Count()
	if err != nil {
		return err
	}
	active, err := base.Where("status = ?", UserActive).Count()
	if err != nil {
		return err
	}
	// Neither count saw the other's condition, and base is still just
	// "credits > 0". A Repo value is a query, not a connection — it is safe to
	// keep on a service struct and share between goroutines.
	ctx.Log().Info("branched off one base query", "trial", trial, "active", active)
	return nil
}

func crudUpdateAndDelete(ctx core.IContext, id string) core.IError {
	users := repository.New[User](ctx)

	// One column.
	if err := users.Where("id = ?", id).Update("name", "Ann B."); err != nil {
		return err
	}

	// Several columns. Use a map whenever a zero value is a value you mean:
	// Updates with a *struct* skips zero fields, so Status: "" and
	// CreditsSatang: 0 are indistinguishable from "not set" and are left alone.
	if err := users.Where("id = ?", id).Updates(map[string]any{
		"status":         UserActive,
		"credits_satang": 0,
	}); err != nil {
		return err
	}

	// Arithmetic belongs in the database. Read-modify-write in Go loses updates
	// the moment two requests do it at once.
	if err := users.Where("id = ?", id).
		Update("credits_satang", gorm.Expr("credits_satang + ?", 500)); err != nil {
		return err
	}

	// When the row count *is* the answer — a compare-and-swap, "did that
	// actually change anything" — take it from DB(). Putting the precondition
	// in the WHERE makes check-then-act atomic with no transaction and no lock.
	res := users.Where("id = ? AND status = ?", id, UserActive).
		DB().Update("status", UserDormant)
	if res.Error != nil {
		return ctx.NewError(res.Error, errmsgs.DBError)
	}
	if res.RowsAffected == 0 {
		return ctx.NewError(nil, errmsgs.NotFound)
	}

	// Soft delete: the row stays, and every later query filters it out for you.
	if err := users.Where("id = ?", id).Delete(); err != nil {
		return err
	}
	still, err := users.Unscoped().Where("id = ?", id).Count()
	if err != nil {
		return err
	}
	ctx.Log().Info("soft deleted", "rows_still_on_disk", still)

	// HardDelete is the only way to free the unique email again.
	return users.Where("id = ?", id).HardDelete()
}
