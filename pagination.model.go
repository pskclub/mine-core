package core

import "reflect"

type Pagination struct {
	Page  int64       `json:"page"`
	Total int64       `json:"total"`
	Limit int64       `json:"limit,omitempty"`
	Items interface{} `json:"items"`
}

type PageOptions struct {
	Q       string
	Limit   int64
	Page    int64
	OrderBy []string
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
	m := &Pagination{
		Page:  1,
		Total: int64(reflect.ValueOf(items).Len()),
	}
	if options != nil {
		m.Limit = options.Limit
		m.Page = options.Page
		m.Total = options.Total
	}

	if items == nil {
		m.Items = make([]interface{}, 0)
	} else {
		m.Items = items
	}

	return m
}
