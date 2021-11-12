package core

type Pagination struct {
	Page  int64       `json:"page"`
	Total int64       `json:"total"`
	Limit int64       `json:"limit"`
	Count int64       `json:"count"`
	Items interface{} `json:"items"`
}

type PageOptions struct {
	Q       string
	Limit   int64
	Page    int64
	OrderBy []string
}

func (p *PageOptions) SetOrderDefault(orders ...string) {
	if len(p.OrderBy) == 0 {
		p.OrderBy = orders
	}
}

type PageResponse struct {
	Total   int64
	Limit   int64
	Count   int64
	Page    int64
	Q       string
	OrderBy []string
}

func NewPagination(items interface{}, options *PageResponse) *Pagination {
	m := &Pagination{}
	if options != nil {
		m.Limit = options.Limit
		m.Page = options.Page
		m.Total = options.Total
		m.Count = options.Count
	}

	if items == nil {
		m.Items = make([]interface{}, 0)
	} else {
		m.Items = items
	}

	return m
}
