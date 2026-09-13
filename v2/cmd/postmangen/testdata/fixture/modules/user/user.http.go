package user

import (
	"example.com/fixture/httpx"
	"example.com/fixture/middlewares"
)

// NewUserHTTP registers routes on a prefixed group, with the middleware applied
// once to the group rather than repeated per route.
func NewUserHTTP(e *httpx.Server) {
	c := &UserController{}

	g := e.Group("/users", middlewares.Auth(e))
	g.GET("", c.Pagination)
	g.GET("/search", c.Search)
	g.GET("/:id", c.Find)
	g.POST("", c.Create)
	g.POST("/import", c.Import)
	g.POST("/feedback", c.Feedback)

	// A nested group: the prefixes have to compose.
	admin := g.Group("/admin")
	admin.DELETE("/:id", c.Delete)

	// A controller built with its dependency handed in, rather than one that
	// constructs it per call.
	p := NewProfileController(nil)
	g.GET("/me", p.Me)
	g.GET("/list", p.List)

	// Deep enough to sit in a folder of its own rather than beside /users: two
	// static segments below the resource, which is what a sub-resource looks
	// like and what the sidebar has to nest.
	g.GET("/:id/sessions/active", p.Sessions)
}

// NewPublicHTTP registers a route straight on the server, with no group and no
// middleware, and a handler that is a plain function.
func NewPublicHTTP(e *httpx.Server) {
	e.GET("/status", Status)

	// The liveness probe mounted by hand, at a path this service chose. It is
	// the framework's own handler, so the reply is known without a handler in
	// this repository to read it from.
	e.GET("/live", httpx.LiveHandler())
}
