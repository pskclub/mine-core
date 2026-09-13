package core

import (
	"regexp"
	"strings"

	"gorm.io/gorm"
)

// Pagination defaults. Both match v1 (consts.PageLimitDefault, consts.PageLimitMax),
// so a service moving to v2 keeps the page sizes its clients already see.
//
// PageLimitMax is deliberately high: some endpoints are data APIs whose callers
// pull a working set in one request rather than page through it, and clamping
// those returns a short page with no error and nothing in the logs — the caller
// silently gets less than it asked for. The cap still bounds the worst case, but
// it is not a hint about response size. An endpoint that wants a tighter ceiling
// clamps PageOptions.Limit itself.
const (
	PageLimitDefault int64 = 30
	PageLimitMax     int64 = 10000
)

// IModel is implemented by repository models. TableName keeps parity with v1 and
// GORM's convention.
type IModel interface {
	TableName() string
}

// PageOptions carries list query parameters parsed from the request.
type PageOptions struct {
	Q       string
	Limit   int64
	Page    int64
	OrderBy []string
}

// Page is a generic paginated result.
type Page[T any] struct {
	Items   []T      `json:"items"`
	Total   int64    `json:"total"`
	Count   int64    `json:"count"`
	Page    int64    `json:"page"`
	Limit   int64    `json:"limit"`
	Q       string   `json:"q,omitempty"`
	OrderBy []string `json:"order_by,omitempty"`
}

// normalize clamps limit/page to sane bounds.
func (o *PageOptions) normalize() {
	if o.Limit <= 0 {
		o.Limit = PageLimitDefault
	}
	if o.Limit > PageLimitMax {
		o.Limit = PageLimitMax
	}
	if o.Page < 1 {
		o.Page = 1
	}
}

// NewPage assembles a Page around items the caller produced itself — a raw SQL
// query, a search index, a list stitched together from several sources. Paginate
// and MongoPaginate already return one; this is for the cases they do not cover.
//
// The metadata comes from opts, so it reports the same clamped limit and page the
// query should have used. total is the size of the whole result, not of items.
func NewPage[T any](items []T, total int64, opts *PageOptions) *Page[T] {
	if opts == nil {
		opts = &PageOptions{}
	}
	opts.normalize()
	if items == nil {
		items = []T{}
	}
	return &Page[T]{
		Items:   items,
		Total:   total,
		Count:   int64(len(items)),
		Page:    opts.Page,
		Limit:   opts.Limit,
		Q:       opts.Q,
		OrderBy: opts.OrderBy,
	}
}

// MapPage turns a Page[T] into a Page[R], keeping the pagination metadata and
// running f over each item — for handing a response type back to the client
// instead of the model that was queried. (v1 called this MapPaginatedItems.)
//
//	page, err := repository.New[User](ctx).Pagination(opts)
//	return c.JSON(http.StatusOK, core.MapPage(page, toUserResponse))
func MapPage[T, R any](p *Page[T], f func(T) R) *Page[R] {
	if p == nil {
		return nil
	}
	items := make([]R, 0, len(p.Items))
	for _, item := range p.Items {
		items = append(items, f(item))
	}
	return &Page[R]{
		Items:   items,
		Total:   p.Total,
		Count:   int64(len(items)),
		Page:    p.Page,
		Limit:   p.Limit,
		Q:       p.Q,
		OrderBy: p.OrderBy,
	}
}

// orderByColumn is what a sortable column may look like: an identifier, or one
// qualified by its table. Anything else — a function call, a subquery, a second
// statement — is not a column name and is dropped.
//
// GORM hands an Order string to the driver as SQL, so without this the query
// parameter *is* the ORDER BY clause. The check runs whether or not the caller
// passed an allowlist, because the endpoints that forget the allowlist are
// exactly the ones that need it.
var orderByColumn = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// ParseOrderBy turns an order_by query parameter into GORM order clauses,
// dropping anything that is not a column identifier and — when allowed is
// non-nil — any column not in it.
//
// Two spellings, both from v1, may be mixed in one parameter:
//
//	name                     → "name desc"      (desc is the default direction)
//	age asc                  → "age asc"
//	asc(age)                 → "age asc"        (bracket form)
//	desc(name),created_at    → "name desc", "created_at desc"
//
// Any function name other than asc means desc, matching v1: "abc(name)" sorts
// name descending rather than failing.
//
// GetPageOptions calls this for you. Reach for it directly when the order comes
// from somewhere other than the query string, or when you have rewritten the
// parameter first — passing hand-built strings straight into PageOptions.OrderBy
// skips the column check, and GORM will hand whatever is there to the driver.
func ParseOrderBy(s string, allowed []string) []string {
	return parseOrderBy(s, allowed)
}

func parseOrderBy(s string, allowed []string) []string {
	out := make([]string, 0)
	for _, field := range strings.Split(s, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}

		col, dir := field, "desc"
		if fn, inner, isBracket := strings.Cut(field, "("); isBracket && strings.HasSuffix(inner, ")") {
			// asc(name) / desc(name). A second "(" stays in col and the column
			// check drops it, so "desc((name))" is rejected rather than unwrapped.
			col = strings.TrimSuffix(inner, ")")
			if strings.EqualFold(strings.TrimSpace(fn), "asc") {
				dir = "asc"
			}
		} else if parts := strings.Fields(field); len(parts) > 0 {
			col = parts[0]
			if len(parts) == 2 && strings.EqualFold(parts[1], "asc") {
				dir = "asc"
			}
		}

		if !orderByColumn.MatchString(col) {
			continue
		}
		if allowed != nil && !contains(allowed, col) {
			continue
		}
		out = append(out, col+" "+dir)
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Paginate runs a counted, ordered, limited query into dest and returns a Page.
func Paginate[T any](db *gorm.DB, dest *[]T, opts *PageOptions) (*Page[T], error) {
	if opts == nil {
		opts = &PageOptions{}
	}
	opts.normalize()

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, Wrap(err, "paginate: count")
	}

	q := db.Limit(int(opts.Limit)).Offset(int((opts.Page - 1) * opts.Limit))
	for _, ord := range opts.OrderBy {
		if strings.TrimSpace(ord) != "" {
			q = q.Order(ord)
		}
	}
	if err := q.Find(dest).Error; err != nil {
		return nil, Wrap(err, "paginate: find")
	}

	return &Page[T]{
		Items:   *dest,
		Total:   total,
		Count:   int64(len(*dest)),
		Page:    opts.Page,
		Limit:   opts.Limit,
		Q:       opts.Q,
		OrderBy: opts.OrderBy,
	}, nil
}
