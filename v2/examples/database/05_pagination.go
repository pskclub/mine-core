package main

import (
	"fmt"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/repository"
	"github.com/pskclub/mine-core/v2/utils"
)

// --- Example 5: pagination, ordering and search -----------------------------
//
// Pagination runs the query twice — a COUNT and a LIMIT/OFFSET page — and
// returns both in one typed core.Page[T] that marshals straight to JSON. In an
// HTTP handler the options come from c.GetPageOptionsWithAllowed(...); here
// they are built by hand, which is exactly the case where the allow-list has to
// be applied yourself.

// userSortable is the allow-list. order_by becomes a real SQL ORDER BY — GORM
// hands the string to the driver — so the framework drops anything that is not
// a column identifier, and this list drops the real columns the endpoint did
// not mean to expose. Unknown names are ignored rather than rejected, so an old
// bookmark keeps working.
var userSortable = []string{"created_at", "name", "status"}

type UserResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func paginationTour(ctx core.IContext) core.IError {
	if err := seedPagedUsers(ctx, 45); err != nil {
		return err
	}

	page, err := listUsers(ctx, "page-", "name asc", 2, 20)
	if err != nil {
		return err
	}
	ctx.Log().Info("page 2", "total", page.Total, "count", page.Count, "limit", page.Limit)

	// Anything not on the allow-list is dropped silently, including an attempt
	// to smuggle SQL through the parameter.
	if _, err := listUsers(ctx, "page-", "credits_satang, id;DROP TABLE users", 1, 20); err != nil {
		return err
	}
	return keysetTour(ctx)
}

func listUsers(ctx core.IContext, prefix, rawOrderBy string, page, limit int64) (*core.Page[UserResponse], core.IError) {
	opts := &core.PageOptions{
		Q:     prefix,
		Page:  page,  // 0 or negative becomes 1
		Limit: limit, // 0 becomes 30, anything over 10000 is clamped
		// Setting OrderBy by hand skips both of GetPageOptions' guards, so put
		// the string through ParseOrderBy yourself whenever the order comes
		// from somewhere other than the query string.
		OrderBy: core.ParseOrderBy(rawOrderBy, userSortable),
	}

	repo := repository.New[User](ctx)
	if opts.Q != "" {
		// Nothing applies Q for you — what "search" means is the endpoint's
		// decision. LIKE '%…%' cannot use a plain B-tree index: fine over a few
		// thousand rows, the wrong tool over a few million, where a trigram
		// index or a full-text column is the next step. (On postgres this would
		// be ILIKE; sqlite's LIKE is already case-insensitive for ASCII.)
		repo = repo.Where("email LIKE ?", opts.Q+"%")
	}
	if len(opts.OrderBy) == 0 {
		// A fallback the caller cannot see, applied only when it asked for
		// nothing. Ordering by something unique — or tie-broken by something
		// unique — is what stops a row appearing on two pages or on none.
		repo = repo.Order("created_at desc, id desc")
	}

	pageOfUsers, err := repo.Pagination(opts)
	if err != nil {
		return nil, err
	}
	// MapPage keeps the pagination metadata and swaps the item type, so the
	// client never sees the model that was queried.
	return core.MapPage(pageOfUsers, func(u User) UserResponse {
		return UserResponse{ID: u.ID, Name: u.Name}
	}), nil
}

type userCursor struct {
	CreatedAt time.Time
	ID        string
}

// keysetTour walks the whole set without OFFSET.
//
// OFFSET 100000 makes the database walk a hundred thousand rows and throw them
// away, and the COUNT scans the whole matching set every page. For an admin
// table that is fine; for an endpoint under load or a deep-paged export it is
// not. A keyset is O(limit) at any depth and stable while rows are inserted —
// it gives up random access to page N, which most callers were not using.
func keysetTour(ctx core.IContext) core.IError {
	var cursor *userCursor
	seen := 0

	for {
		repo := repository.New[User](ctx).
			Where("email LIKE ?", "page-%").
			// The ORDER BY must match the cursor comparison exactly, or rows
			// are skipped and repeated.
			Order("created_at desc, id desc").
			Limit(20)
		if cursor != nil {
			repo = repo.Where("(created_at, id) < (?, ?)", cursor.CreatedAt, cursor.ID)
		}

		batch, err := repo.FindAll()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			ctx.Log().Info("keyset walk finished", "rows", seen)
			return nil
		}
		seen += len(batch)
		last := batch[len(batch)-1]
		cursor = &userCursor{CreatedAt: utils.ToNonPointer(last.CreatedAt), ID: last.ID}
	}
}

func seedPagedUsers(ctx core.IContext, n int) core.IError {
	rows := make([]User, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, User{
			Email:  fmt.Sprintf("page-%03d@example.com", i),
			Name:   fmt.Sprintf("Paged %03d", i),
			Status: UserActive,
		})
	}
	return repository.New[User](ctx).CreateInBatches(rows, 100)
}
