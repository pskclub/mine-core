package main

import (
	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
	"github.com/pskclub/mine-core/v2/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// --- Example 4: transactions ------------------------------------------------
//
// Transaction commits when fn returns nil and rolls back on an error or a
// panic. The rule that causes every bug in this area: the repository the
// transaction was started from is *not* in the transaction. A write made
// through it commits immediately and survives the rollback, with no error, no
// warning and nothing in the log to tell the two lines apart.
//
// The only reliable defence is habit — bind every repository at the top of the
// closure and never call repository.New below that point.

func transactionsTour(ctx core.IContext) core.IError {
	users := repository.New[User](ctx)

	payer := User{Email: "tx-payer@example.com", Name: "Payer", Status: UserActive, CreditsSatang: 100_000}
	payee := User{Email: "tx-payee@example.com", Name: "Payee", Status: UserActive}
	if err := users.Create(&payer); err != nil {
		return err
	}
	if err := users.Create(&payee); err != nil {
		return err
	}

	order, err := placeOrder(ctx, payer.ID, 30_000)
	if err != nil {
		return err
	}

	// Publishing belongs *after* the commit: a subscriber that sees this can
	// rely on the row existing. Inside the transaction it could receive an event
	// for work that then rolled back. (When the message must not be lost even if
	// the process dies here, write an outbox row inside the transaction instead
	// and let a job deliver it — v2/docs/database-transactions.md.)
	if err := ctx.PubSub().Publish("order.created", order.ID); err != nil {
		ctx.Log().Warn("publish failed, the order is still committed", "err", err)
	}

	if err := lockForUpdate(ctx, payer.ID, 1_000); err != nil {
		return err
	}
	return transferCredits(ctx, payer.ID, payee.ID, 5_000)
}

// placeOrder debits the buyer and writes the order as one outcome.
func placeOrder(ctx core.IContext, userID string, totalSatang int64) (*Order, core.IError) {
	order := Order{UserID: userID, Status: OrderPending, TotalSatang: totalSatang}

	err := repository.New[User](ctx).Transaction(func(tx *gorm.DB) error {
		// Everything transactional is bound here, before any business logic, so
		// there is nothing non-transactional left in scope below this line.
		txUsers := repository.NewWithDB[User](ctx, tx)
		txOrders := repository.NewWithDB[Order](ctx, tx)

		// The cheapest correct debit: the precondition lives in the WHERE and
		// RowsAffected is the answer. No read, no lock, no lost update — and it
		// is correct even without the surrounding transaction.
		res := txUsers.Where("id = ? AND credits_satang >= ?", userID, totalSatang).
			DB().Update("credits_satang", gorm.Expr("credits_satang - ?", totalSatang))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errmsgs.BadRequest // not enough credit: roll the order back
		}
		return txOrders.Create(&order)
	})
	if err != nil {
		// Worth knowing: an error returned from the closure comes back wrapped
		// as DATABASE_ERROR, so GetCode() no longer says BAD_REQUEST. The cause
		// is preserved, so errors.Is still matches — match on that, or decide
		// the business case before the transaction starts.
		return nil, err
	}
	return &order, nil
}

// lockForUpdate is the shape to reach for when the decision cannot be expressed
// as a WHERE — several reads that must agree, or a value computed in Go.
//
// SELECT … FOR UPDATE makes the second transaction wait, and the lock lives and
// dies with the transaction, so this only works inside one. It is also the more
// expensive answer: try atomic arithmetic (above) or core.WithLock first.
func lockForUpdate(ctx core.IContext, userID string, spend int64) core.IError {
	return repository.New[User](ctx).Transaction(func(tx *gorm.DB) error {
		txUsers := repository.NewWithDB[User](ctx, tx)

		// sqlite has no FOR UPDATE — it serialises writers instead — so the
		// clause is only added on engines that have one. Worth knowing for
		// tests: a locking test passes on sqlite whether or not it locks.
		if supportsRowLocks(tx) {
			txUsers = txUsers.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		u, err := txUsers.FindOne("id = ?", userID)
		if err != nil {
			return err
		}
		if u.CreditsSatang < spend {
			return errmsgs.BadRequest
		}
		return txUsers.Where("id = ?", userID).Update("credits_satang", u.CreditsSatang-spend)
	})
}

func supportsRowLocks(tx *gorm.DB) bool {
	switch tx.Dialector.Name() {
	case "postgres", "mysql", "sqlserver", "oracle":
		return true
	default:
		return false
	}
}

// transferCredits shows the deadlock cure. Two transactions that lock the same
// two rows in opposite orders deadlock and the database kills one of them;
// retrying is not the fix, locking in a consistent order is.
func transferCredits(ctx core.IContext, fromID, toID string, amount int64) core.IError {
	first, second := fromID, toID
	if first > second {
		first, second = second, first
	}

	return repository.New[User](ctx).Transaction(func(tx *gorm.DB) error {
		txUsers := repository.NewWithDB[User](ctx, tx)
		for _, id := range []string{first, second} {
			if _, err := txUsers.FindOne("id = ?", id); err != nil {
				return err
			}
		}

		res := txUsers.Where("id = ? AND credits_satang >= ?", fromID, amount).
			DB().Update("credits_satang", gorm.Expr("credits_satang - ?", amount))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errmsgs.BadRequest
		}
		// Nothing that is not a database statement belongs between here and the
		// commit: an HTTP call, an S3 upload or a slow computation turns its own
		// latency into lock-hold time, and a transaction holds a pooled
		// connection for its whole life.
		return txUsers.Where("id = ?", toID).
			Update("credits_satang", gorm.Expr("credits_satang + ?", amount))
	})
}
