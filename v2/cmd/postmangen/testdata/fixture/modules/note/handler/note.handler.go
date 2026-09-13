package handler

import (
	"net/http"

	"example.com/fixture/httpx"
	"example.com/fixture/modules/note/service"
)

type NoteHandler struct{}

// NewNoteHandler is a constructor rather than a bare literal, which is how a
// controller with dependencies is built — the generator has to read the type out
// of the function name.
func NewNoteHandler() *NoteHandler { return &NoteHandler{} }

func (m NoteHandler) Pagination(c httpx.Context) error {
	opts := c.GetPageOptionsWithAllowed("created_at", "title")

	res, err := service.NewNoteService(c).Pagination(c.GetUserID(), opts)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, res)
}

// Upload takes its request apart by hand. Nothing is bound here, so a path
// parameter, two form fields, a file, a header and a cookie are stated only by
// these calls — an endpoint the generator would otherwise document as accepting
// nothing at all.
func (m NoteHandler) Upload(c httpx.Context) error {
	if _, err := c.FormFile("file"); err != nil {
		return err
	}
	if _, err := c.Cookie("session_id"); err != nil {
		return err
	}
	_ = c.Request().Header.Get("X-Api-Key")

	note, err := service.NewNoteService(c).Attach(
		c.Param("id"),
		c.FormValue("is_public"),
		c.FormValueOr("caption", "untitled"),
	)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusCreated, note)
}

// Search reads its query both ways a handler can: off the context, and off the
// request's own URL. QueryParamOr states a default, which is a value the server
// is known to accept and therefore beats any sample guessed from the name.
func (m NoteHandler) Search(c httpx.Context) error {
	res, err := service.NewNoteService(c).Search(
		c.QueryParam("q"),
		c.QueryParamOr("limit", "20"),
		c.Request().URL.Query().Get("tag"),
	)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, res)
}

func (m NoteHandler) Create(c httpx.Context) error {
	input := &CreateRequest{}
	if err := c.BindWithValidate(input); err != nil {
		return err
	}

	note, err := service.NewNoteService(c).Create(c.GetUserID(), *input.Title)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusCreated, note)
}
