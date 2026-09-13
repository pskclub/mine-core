package note

import (
	"example.com/fixture/httpx"
	"example.com/fixture/middlewares"
	"example.com/fixture/modules/note/handler"
)

// The controller lives in a sub-package, as every module in this project does.
// That is what the generator has to follow to find the request struct: reading
// the package off this file would look in modules/note and find nothing.
//
// NewNoteHTTP writes each path in full and applies the middleware per route — the
// style the project uses, and the one the generator has to read without a group
// to inherit a prefix or middleware from.
func NewNoteHTTP(e *httpx.Server) {
	c := handler.NewNoteHandler()

	e.GET("/notes", c.Pagination, middlewares.AuthRequire(e))
	e.GET("/notes/search", c.Search)
	e.POST("/notes", c.Create, middlewares.AuthRequire(e))
	e.POST("/notes/:id/attachments", c.Upload, middlewares.AuthRequire(e))
}
