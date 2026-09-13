package handler

import "example.com/fixture/requests"

// The request lives in the module, alongside the handler that binds it — the
// layout the project uses. It covers the generator resolving a request type from
// the same package as the controller rather than from a shared one.
type CreateRequest struct {
	Title  *string `json:"title"`
	Body   *string `json:"body"`
	Pinned *bool   `json:"pinned"`
}

func (r *CreateRequest) Valid(v *requests.Validator) {
	v.Str("title", r.Title).Required().Length(1, 120)
	v.Str("body", r.Body).Required().Length(1, 10000)
}
