// Package httpx stands in for the framework the fixture is written against. The
// generator reads syntax, never types, so a local stub keeps the fixture
// self-contained — no module has to be fetched to run the test.
package httpx

type Context interface{}

type Server struct{}

type Group struct{}

// Page is generic so the fixture covers rendering an instantiated type.
type Page[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
	Page  int64 `json:"page"`
	Limit int64 `json:"limit"`
}

type PageOptions struct{}
