package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/repository"
	"gorm.io/gorm"
)

// --- Example 6: reading and writing a lot of rows ---------------------------
//
// FindAll() with no bound is an OOM waiting for the table to grow: it builds a
// slice of every matching row before the first one is used. Everything on this
// page exists to keep the working set to one batch — a fixed amount of memory
// whatever the table does.

func batchTour(ctx core.IContext) core.IError {
	if err := bulkInsert(ctx, 2_000); err != nil {
		return err
	}

	n, err := exportUsers(ctx, io.Discard)
	if err != nil {
		return err
	}
	ctx.Log().Info("exported", "rows", n)

	updated, err := markDormant(ctx, time.Now().Add(time.Hour))
	if err != nil {
		return err
	}
	ctx.Log().Info("marked dormant", "rows", updated)
	return nil
}

// bulkInsert chunks the INSERT so one statement does not exceed the driver's
// bound-parameter limit (postgres stops at 65535, and every column of every row
// is one parameter). The batch size is per statement, not per transaction.
func bulkInsert(ctx core.IContext, n int) core.IError {
	rows := make([]User, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, User{
			Email:         fmt.Sprintf("batch-%05d@example.com", i),
			Name:          fmt.Sprintf("Batch %05d", i),
			Status:        UserTrial,
			CreditsSatang: int64(i),
		})
	}
	return repository.New[User](ctx).CreateInBatches(rows, 200)
}

// exportUsers streams the whole table through a fixed-size buffer.
//
// FindInBatches reuses dest for every batch, so memory is one batch rather than
// one table. The trade-off: the batches are separate queries against a moving
// table, so a row inserted while the export runs may or may not appear. When
// the export has to be a single point in time, run it inside one transaction.
func exportUsers(ctx core.IContext, w io.Writer) (int, core.IError) {
	out := csv.NewWriter(w)
	defer out.Flush()

	written := 0
	var batch []User
	err := repository.New[User](ctx).
		Where("email LIKE ?", "batch-%").
		// Order by the primary key: batching without a stable order can visit a
		// row twice and miss another.
		Order("id").
		FindInBatches(&batch, 500, func(tx *gorm.DB, _ int) error {
			for _, u := range batch {
				if err := out.Write([]string{u.ID, u.Email, string(u.Status)}); err != nil {
					// Returning an error stops the walk — the remaining batches
					// are never fetched.
					return err
				}
				written++
			}
			out.Flush()
			return out.Error()
		})
	if err != nil {
		return written, err
	}
	return written, nil
}

// markDormant updates a large set without holding a lock across all of it.
//
// One UPDATE over a million rows blocks every writer of those rows until it
// finishes. Walking the primary key in pages makes each batch its own short
// transaction, and the loop is resumable: if it dies halfway, running it again
// picks up where it stopped instead of starting over.
func markDormant(ctx core.IContext, cutoff time.Time) (int, core.IError) {
	last := ""
	total := 0

	for {
		var ids []string
		if err := repository.New[User](ctx).
			Where("id > ? AND created_at < ? AND status = ?", last, cutoff, UserTrial).
			Order("id").
			Limit(500).
			Pluck("id", &ids); err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}

		// Pluck first, then update by id: the second statement touches exactly
		// the rows the first one saw, so a row that changes underneath the loop
		// cannot make it run forever.
		if err := repository.New[User](ctx).
			Where("id IN ?", ids).
			Updates(map[string]any{"status": UserDormant}); err != nil {
			return total, err
		}
		total += len(ids)
		last = ids[len(ids)-1]
	}
}
