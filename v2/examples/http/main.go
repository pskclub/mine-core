// Command http is a runnable tour of the v2 HTTP layer: the server and its
// options, binding and validating a request, the errors a client gets back,
// list endpoints, middleware, bearer-token auth, and file upload/download.
//
// Each example lives in its own file:
//
//	01_server.go      the server, its options, groups, and how it is started
//	02_binding.go     one struct per request, one Valid, one BindWithValidate
//	03_errors.go      errors a client can act on, and where they are declared
//	04_pagination.go  paging, ordering, and what a client may sort by
//	05_middleware.go  writing middleware and choosing where to attach it
//	06_auth.go        bearer tokens, roles, and issuing one
//	07_upload.go      multipart in, streamed file out
//
// It runs with no external services: with no DB_* configuration the list
// endpoint pages a fixture, and storage is the in-memory implementation, so
// every route answers.
//
// Run it with: go run ./examples/http
package main

import (
	core "github.com/pskclub/mine-core/v2"
)

func main() {
	env, err := core.NewEnv()
	if err != nil {
		panic(err)
	}

	// The App is the process-lifetime container of pools and capabilities;
	// IContext is the per-request handle onto it. Nothing here is created per
	// request, which is why a handler can take capabilities without cost.
	//
	// NewMemoryStorage is the exported in-memory implementation every capability
	// ships — a dev box should not need MinIO to accept an upload. A real
	// service passes core.WithStorage(s3) and changes nothing else.
	app, err := core.NewApp(env,
		core.WithStorage(core.NewMemoryStorage()),
	)
	if err != nil {
		panic(err)
	}

	e := newServer(app)     // 01: options, deadlines, the standard stack
	api := mountSystem(e)   // 01: /healthz plus the /api/v1 group
	mountArticleWrites(api) // 02: POST/PUT with binding and validation
	mountArticleErrors(api) // 03: what a refused request looks like
	mountArticleList(api)   // 04: GET /api/v1/articles?page=&order_by=
	mountMiddleware(e, app) // 05: a guarded group and a per-route body limit
	mountFiles(e)           // 07: POST /files, GET /files/:id

	// 06 is the one part that cannot be faked. Rather than fall back to a
	// built-in secret — which would sign tokens anyone reading this repository
	// could mint — the authenticated routes are simply not registered, and the
	// reason is on the first screen of the log instead of in a 401 later.
	if auth, ok := newJWTAuth(env); ok {
		mountAuth(e, auth)
	} else {
		app.Log().Warn("authenticated routes are not mounted: no JWT secret",
			"hint", "set APP_JWT_SECRET to try /auth/login, /me, /feed and /admin/articles/:id")
	}

	// Blocks. Outside dev it drains in-flight requests on SIGINT/SIGTERM and
	// only then closes the pools — see startWithRunner in 01_server.go for the
	// process that also runs jobs.
	startServer(e, env)
}
