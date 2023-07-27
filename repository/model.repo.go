package repository

type Pagination[M any] struct {
	Page  int64 `json:"page" example:"1"`
	Total int64 `json:"total" example:"45"`
	Limit int64 `json:"limit" example:"30"`
	Count int64 `json:"count" example:"30"`
	Items []M   `json:"items"`
}

type IModel interface {
	TableName() string
}
