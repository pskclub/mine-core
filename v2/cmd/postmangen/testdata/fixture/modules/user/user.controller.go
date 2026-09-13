package user

import (
	"net/http"

	"example.com/fixture/httpx"
	"example.com/fixture/requests"
	"example.com/fixture/services"
)

type UserController struct{}

// ProfileController holds its service rather than constructing one per call,
// which is how a controller built by a DI container is written. A call on a
// field resolves through the field's declared type — here an interface — and
// nothing in the method body says what that type is.
type ProfileController struct {
	users services.IUserService
}

func NewProfileController(users services.IUserService) *ProfileController {
	return &ProfileController{users: users}
}

func (m ProfileController) Me(c httpx.Context) error {
	user, err := m.users.Find(c.GetUserID())
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, user)
}

// List assigns the service to a variable before calling it — the shape almost
// every handler is actually written in, and the one a chain-only resolver
// cannot follow.
func (m ProfileController) List(c httpx.Context) error {
	svc := services.NewUserService(c)

	res, err := svc.Pagination(c.GetPageOptions())
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, res)
}

// Sessions sits two segments below the resource, which is a sub-resource rather
// than another endpoint of the same one.
func (m ProfileController) Sessions(c httpx.Context) error {
	res, err := m.users.Pagination(c.GetPageOptions())
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, res)
}

// UserFilter re-exports the shared search request under this module's own name.
// It is an alias, not a declaration of its own: binding through it compiles and
// behaves identically, so a lookup that stopped at struct declarations would
// find no fields here and quietly drop every query parameter the endpoint takes.
type UserFilter = requests.UserSearch

func (m UserController) Search(c httpx.Context) error {
	input := &UserFilter{}
	if err := c.BindWithValidate(input); err != nil {
		return err
	}

	res, err := services.NewUserService(c).Pagination(nil)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, res)
}

// Import binds a request whose fields are tagged for a form and takes a file
// alongside them — one multipart body carrying both, which is the request an
// upload with metadata actually is.
func (m UserController) Import(c httpx.Context) error {
	input := &requests.UserImport{}
	if err := c.BindWithValidate(input); err != nil {
		return err
	}
	if _, err := c.FormFile("file"); err != nil {
		return err
	}

	return c.NoContent(http.StatusNoContent)
}

// Feedback reads a posted form with no file in it, which is a url-encoded body
// rather than a multipart one.
func (m UserController) Feedback(c httpx.Context) error {
	_ = c.FormValue("subject")
	_ = c.FormValueOr("rating", "5")

	return c.NoContent(http.StatusNoContent)
}

func (m UserController) Pagination(c httpx.Context) error {
	opts := c.GetPageOptionsWithAllowed("created_at", "email")

	input := &requests.UserSearch{}
	if err := c.BindWithValidate(input); err != nil {
		return err
	}

	res, err := services.NewUserService(c).Pagination(opts)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, res)
}

func (m UserController) Find(c httpx.Context) error {
	user, err := services.NewUserService(c).Find(c.Param("id"))
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, user)
}

func (m UserController) Create(c httpx.Context) error {
	input := &requests.UserCreate{}
	if err := c.BindWithValidate(input); err != nil {
		return err
	}

	user, err := services.NewUserService(c).Create(*input.Email)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusCreated, user)
}

func (m UserController) Delete(c httpx.Context) error {
	return c.NoContent(http.StatusNoContent)
}

// Status is a package-level handler, registered without a controller.
func Status(c httpx.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"status": "ok",
	})
}
