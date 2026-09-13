// Package middlewares stands in for the project's middlewares package: the
// authentication a route reaches for by name.
package middlewares

import "example.com/fixture/httpx"

// AuthRequire is what the generator has to recognise as "this route needs a
// token", written per route rather than inherited from a group.
func AuthRequire(e *httpx.Server) func() { return nil }

// AuthOptional must NOT count: it lets an anonymous request through.
func AuthOptional(e *httpx.Server) func() { return nil }

// Auth is the older spelling, applied to a group, and still recognised.
func Auth(e *httpx.Server) func() { return nil }
