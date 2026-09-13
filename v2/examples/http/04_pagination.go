package main

import (
	"net/http"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/repository"
	"github.com/pskclub/mine-core/v2/utils"
)

// --- Example 4: list endpoints ----------------------------------------------
//
// GetPageOptions reads limit/page/q/order_by from the query string and clamps
// them, so no request can ask for the whole table. Page[T] marshals straight to
// JSON, which makes the happy path one call.
//
// The part worth reading twice is order_by: it becomes a real ORDER BY clause,
// handed to the driver as SQL.

// article is the model. It carries columns a client must never see, which is
// the reason articleResponse exists below.
type article struct {
	ID          string     `gorm:"primaryKey"`
	Title       string     `gorm:"column:title"`
	Slug        string     `gorm:"column:slug"`
	Status      string     `gorm:"column:status"`
	Views       int64      `gorm:"column:views"`
	CreatedAt   *time.Time `gorm:"column:created_at"`
	AuthorEmail string     `gorm:"column:author_email"` // internal
}

func (article) TableName() string { return "articles" }

type articleResponse struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Slug      string    `json:"slug"`
	Status    string    `json:"status"`
	Views     int64     `json:"views"`
	CreatedAt time.Time `json:"created_at"`
}

func toArticleResponse(a article) articleResponse {
	return articleResponse{
		ID:        a.ID,
		Title:     a.Title,
		Slug:      a.Slug,
		Status:    a.Status,
		Views:     a.Views,
		CreatedAt: utils.ToNonPointer(a.CreatedAt),
	}
}

func mountArticleList(g *core.Group) {
	g.GET("/articles", listArticles)
}

func listArticles(c core.IHTTPContext) error {
	// Two defences, and they answer different questions.
	//
	// GetPageOptions already drops anything that is not a column identifier, so
	// no client can smuggle a subquery or a second statement into the ORDER BY.
	// That is the floor. It still leaves "any real column" sortable, including
	// the ones this endpoint never meant to expose — sorting by author_email
	// leaks its ordering, and an indexless column turns a list call into a table
	// scan. The allow-list is what narrows it to the columns you chose.
	opts := c.GetPageOptionsWithAllowed("created_at", "title", "views")

	// An endpoint that wants a tighter ceiling than PageLimitMax says so itself:
	// the framework's cap is high on purpose, because clamping silently returns
	// a short page with no error and nothing in the logs.
	if opts.Limit > 100 {
		opts.Limit = 100
	}

	if c.DB() == nil {
		// No SQL connection configured. NewPage builds the same envelope around
		// items the caller produced itself — a search index, a fixture, a list
		// stitched from several sources — so the response shape does not depend
		// on where the rows came from.
		items, total := pageInMemory(sampleArticles(), opts)

		return c.JSON(http.StatusOK, core.MapPage(core.NewPage(items, total, opts), toArticleResponse))
	}

	// Order on the chain is applied before the request's, so this ordering is
	// the endpoint's and order_by only breaks its ties. Page through something
	// unique, or two rows sharing a created_at can appear on both page 1 and
	// page 2 — or on neither.
	page, err := repository.New[article](c).
		Where("status = ?", statusPublished).
		Order("id asc").
		Pagination(opts)
	if err != nil {
		return err
	}

	// MapPage keeps the metadata and rewrites only the items. Declaring the
	// response type is what makes the compiler — rather than the next reviewer —
	// responsible for keeping author_email out of the response.
	return c.JSON(http.StatusOK, core.MapPage(page, toArticleResponse))
}

// pageInMemory slices a fixture the way the database would. opts arrives
// normalised (limit and page are already at least 1), so the arithmetic needs
// no guards of its own.
func pageInMemory(all []article, opts *core.PageOptions) ([]article, int64) {
	from := int((opts.Page - 1) * opts.Limit)
	if from > len(all) {
		from = len(all)
	}

	to := from + int(opts.Limit)
	if to > len(all) {
		to = len(all)
	}

	return all[from:to], int64(len(all))
}

// sampleArticles is a function, not a package-level slice: a shared slice is
// mutable state that any handler could write through.
func sampleArticles() []article {
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)

	return []article{
		{ID: "1", Title: "Why v2", Slug: "why-v2", Status: statusPublished, Views: 128,
			CreatedAt: utils.ToPointer(base), AuthorEmail: "ann@example.com"},
		{ID: "2", Title: "Binding requests", Slug: "binding-requests", Status: statusPublished, Views: 64,
			CreatedAt: utils.ToPointer(base.AddDate(0, 0, 1)), AuthorEmail: "bob@example.com"},
		{ID: "3", Title: "Draft notes", Slug: "draft-notes", Status: statusDraft, Views: 0,
			CreatedAt: utils.ToPointer(base.AddDate(0, 0, 2)), AuthorEmail: "ann@example.com"},
	}
}
