package services

import (
	"example.com/fixture/httpx"
	"example.com/fixture/models"
)

// IUserService is the interface the constructor hands back, so resolving a
// response type has to go through it rather than through the struct.
type IUserService interface {
	Create(email string) (*models.User, error)
	Find(id string) (*models.User, error)
	Pagination(opts *httpx.PageOptions) (*httpx.Page[models.User], error)
}

type userService struct{}

func NewUserService(ctx httpx.Context) IUserService {
	return &userService{}
}

func (s userService) Create(email string) (*models.User, error) { return nil, nil }

func (s userService) Find(id string) (*models.User, error) { return nil, nil }

func (s userService) Pagination(opts *httpx.PageOptions) (*httpx.Page[models.User], error) {
	return nil, nil
}
