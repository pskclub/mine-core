package service

import (
	"example.com/fixture/httpx"
	"example.com/fixture/models"
)

// INoteService is in the same package as the controller that calls it, so the
// generator has to resolve the response type without crossing packages.
type INoteService interface {
	Create(ownerID string, title string) (*models.Note, error)
	Attach(noteID string, public string, caption string) (*models.Note, error)
	Search(keyword string, limit string, tag string) (*httpx.Page[models.Note], error)
	Pagination(ownerID string, opts *httpx.PageOptions) (*httpx.Page[models.Note], error)
}

type noteService struct{}

func NewNoteService(ctx httpx.Context) INoteService { return &noteService{} }

func (s noteService) Create(ownerID string, title string) (*models.Note, error) { return nil, nil }

func (s noteService) Attach(noteID string, public string, caption string) (*models.Note, error) {
	return nil, nil
}

func (s noteService) Search(keyword string, limit string, tag string) (*httpx.Page[models.Note], error) {
	return nil, nil
}

func (s noteService) Pagination(ownerID string, opts *httpx.PageOptions) (*httpx.Page[models.Note], error) {
	return nil, nil
}
