package cmd

import (
	"example.com/fixture/httpx"
	"example.com/fixture/modules/note"
	"example.com/fixture/modules/user"
)

// NewAPI assembles the server the way the standard template does: the framework's
// probes first, then every module's routes.
//
// This file is deliberately not a `.http.go`. The probes are registered where the
// server is built, so a generator that only read route files would miss the two
// endpoints every deployment has — and neither path is written here anyway.
func NewAPI(e *httpx.Server) {
	httpx.RegisterHealthRoutes(e)

	user.NewUserHTTP(e)
	user.NewPublicHTTP(e)
	note.NewNoteHTTP(e)
}
