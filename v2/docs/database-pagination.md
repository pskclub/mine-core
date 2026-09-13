# Pagination

`Pagination` runs the query twice — a `COUNT` and a `LIMIT`/`OFFSET` page — and
returns both in one typed result.

```go
page, err := repository.New[User](ctx).
    Where("status = ?", "active").
    Order("created_at desc").
    Pagination(c.GetPageOptions())
```

```go
type Page[T any] struct {
    Items   []T      `json:"items"`
    Total   int64    `json:"total"`      // rows matching the query
    Count   int64    `json:"count"`      // rows in this page
    Page    int64    `json:"page"`
    Limit   int64    `json:"limit"`
    Q       string   `json:"q,omitempty"`
    OrderBy []string `json:"order_by,omitempty"`
}
```

`Page[T]` marshals straight to JSON, so a list endpoint is one line:

```go
func List(c core.IHTTPContext) error {
    page, err := repository.New[User](c).
        Where("tenant_id = ?", c.GetUser().ID).
        Pagination(c.GetPageOptions())
    if err != nil {
        return err
    }
    return c.JSON(http.StatusOK, page)
}
```

## From the request

`c.GetPageOptions()` reads the query string:

```
GET /users?page=2&limit=20&q=ann&order_by=created_at desc
```

```go
type PageOptions struct {
    Q       string
    Limit   int64
    Page    int64
    OrderBy []string
}
```

Limits are clamped, so no request can ask for the whole table:

| | Default | Max |
|---|---|---|
| `limit` | 30 (`core.PageLimitDefault`) | 10000 (`core.PageLimitMax`) |
| `page` | 1 | — |

A `limit` of 50000 comes back as 10000; a `page` of 0 or -3 becomes 1.

The ceiling is high on purpose. Some endpoints are data APIs whose callers pull a
whole working set in one request rather than page through it, and clamping those
to a small number returns a short page with no error and nothing in the logs —
the caller just silently sees less than it asked for. `PageLimitMax` bounds the
worst case; it is not a hint about how much a response should carry.

An endpoint that wants a tighter ceiling than the framework's says so itself:

```go
opts := c.GetPageOptions()
if opts.Limit > 200 {
    opts.Limit = 200
}
```

## Ordering

`order_by` is a comma-separated list, each entry a column with an optional
direction. **The default direction is `desc`**, not `asc`:

```
?order_by=created_at            → ORDER BY created_at desc
?order_by=name asc              → ORDER BY name asc
?order_by=status asc,created_at → ORDER BY status asc, created_at desc
```

v1's bracket spelling is accepted too, and may be mixed with the one above — a
client that already sends `asc(name)` keeps its ordering against v2:

```
?order_by=asc(name)             → ORDER BY name asc
?order_by=desc(xxx),yyy asc     → ORDER BY xxx desc, yyy asc
```

Any function name other than `asc` means `desc`, as in v1: `abc(name)` sorts
`name` descending rather than failing.

The request's ordering is **appended after** any `Order` already on the chain, so
a chain `Order` takes precedence and the request only breaks its ties:

```go
// the caller cannot change this ordering — it is fixed by the endpoint
repository.New[User](ctx).Order("created_at desc").Pagination(opts)

// the caller controls it; the fallback is applied when order_by is absent
repo := repository.New[User](ctx)
opts := c.GetPageOptions()
if len(opts.OrderBy) == 0 {
    repo = repo.Order("created_at desc")
}
page, err := repo.Pagination(opts)
```

Order by something unique, or at least tie-broken by something unique. Two rows
with the same `created_at` have no defined order between pages, and a row can
appear on both page 1 and page 2 — or on neither:

```go
Order("created_at desc, id desc")
```

### Allowlisting what can be sorted

`order_by` กลายเป็น SQL `ORDER BY` จริงๆ — GORM ส่ง string นี้ให้ driver ตรงๆ
framework จึงกันไว้สองชั้น

**ชั้นแรก มีให้เสมอ:** ค่าที่ไม่ใช่ชื่อ column (identifier ธรรมดา หรือ
`table.column`) ถูกทิ้งทั้งอัน ทั้งใน `GetPageOptions` และ
`GetPageOptionsWithAllowed` — subquery, function call, statement ที่สองจึงเข้าไม่ได้

```
?order_by=id;DROP TABLE users   → ทิ้ง
?order_by=(SELECT 1)            → ทิ้ง
?order_by=users.created_at asc  → ORDER BY users.created_at asc
```

**ชั้นที่สอง ต้องเขียนเอง:** ชั้นแรกยังยอมให้ sort ด้วย column จริงทุกตัว รวมถึงตัวที่
endpoint ไม่ได้ตั้งใจเปิด บอกชื่อที่อนุญาตด้วย allowlist บนทุก endpoint ที่ client
เข้าถึงได้ — column ที่ไม่อยู่ในลิสต์ถูกทิ้งเงียบๆ ไม่ reject เพื่อให้ bookmark เก่ายัง
เปิดได้:

```go
opts := c.GetPageOptionsWithAllowed("created_at", "name", "status")
page, err := repository.New[User](c).Pagination(opts)
```

`GetPageOptions` เปล่าๆ เหมาะกับ internal caller และ endpoint ที่ fix ลำดับไว้แล้ว

**ถ้าเซ็ต `PageOptions.OrderBy` เอง ทั้งสองชั้นถูกข้าม** — GORM ได้ string อะไรก็ส่งต่อ
ให้ driver อันนั้น ลำดับที่มาจากที่อื่นนอกจาก query string หรือที่ผ่านการแปลงมาก่อน
ให้ส่งผ่าน `core.ParseOrderBy` เอง:

```go
opts := c.GetPageOptions()
opts.OrderBy = core.ParseOrderBy(rewritten, []string{"created_at", "name"})
```

## Searching

`Q` is carried on `PageOptions` and echoed back in the result, but nothing
applies it for you — what "search" means is the endpoint's decision:

```go
opts := c.GetPageOptions()

repo := repository.New[User](c).Where("tenant_id = ?", tenantID)
if opts.Q != "" {
    like := "%" + opts.Q + "%"
    repo = repo.Where("name ILIKE ? OR email ILIKE ?", like, like)
}
page, err := repo.Pagination(opts)
```

`ILIKE '%…%'` cannot use a plain B-tree index. It is fine over a few thousand
rows and the wrong tool over a few million — a trigram index (`pg_trgm`) or a
full-text column is the next step, not a bigger `LIMIT`.

## Paginating something that is not a model

`core.Paginate` works on any `*gorm.DB`, so a projection or a joined query pages
the same way:

```go
type row struct {
    ID    string `json:"id"`
    Email string `json:"email"`
    Total int64  `json:"total"`
}

var rows []row
q := ctx.DB().Table("users").
    Select("users.id, users.email, count(orders.id) as total").
    Joins("LEFT JOIN orders ON orders.user_id = users.id").
    Group("users.id, users.email")

page, err := core.Paginate(q, &rows, c.GetPageOptions())
```

When the list does not come from a `*gorm.DB` at all — a search index, a
third-party API, several sources stitched together — build the page with
`core.NewPage`. It takes the options it was paged with, so the response reports
the same clamped `limit` and `page` the query should have used:

```go
hits, total, err := search.Query(opts.Q, opts.Page, opts.Limit)
if err != nil {
    return ctx.NewError(err, errmsgs.InternalServerError)
}
return c.JSON(http.StatusOK, core.NewPage(hits, total, opts))
```

`total` is the size of the whole result; `Count` is filled in from the items.

## Returning a response type instead of the model

`core.MapPage` converts `Page[T]` to `Page[R]` and keeps the pagination metadata,
so the client never sees the model that was queried:

```go
page, err := repository.New[User](c).Pagination(c.GetPageOptions())
if err != nil {
    return err
}
return c.JSON(http.StatusOK, core.MapPage(page, func(u User) UserResponse {
    return UserResponse{ID: u.ID, Name: u.Name}
}))
```

v1 called this `repository.MapPaginatedItems`; in v2 it lives in `core` next to
the `Page` type it maps, and works on a Mongo page just as well as a SQL one.

## The cost of `OFFSET`

`OFFSET 100000` makes the database walk a hundred thousand rows and throw them
away, and the `COUNT` scans the whole matching set. For an admin table that is
fine. For an endpoint under load, or a deep-paged export, use a keyset instead:

```go
// "give me the 20 after this one"
list, err := repository.New[User](ctx).
    Where("(created_at, id) < (?, ?)", cursor.CreatedAt, cursor.ID).
    Order("created_at desc, id desc").
    Limit(20).
    FindAll()
```

Keyset paging is O(limit) at any depth and stable while rows are inserted. It
gives up random access to page *N* — which most callers were not using.

## MongoDB

The Mongo side returns the same `core.Page[T]`, so a handler does not change
shape when the collection moves:

```go
page, err := mongorepo.New[User](ctx).Eq("status", "active").
    Pagination(c.GetPageOptions())
```

See [Mongo repository → paging](./mongo-repository-queries.md#counting-plucking-paging-streaming).
