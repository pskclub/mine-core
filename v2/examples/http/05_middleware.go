package main

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	core "github.com/pskclub/mine-core/v2"
)

// --- Example 5: middleware, and where to attach it --------------------------
//
// Middleware runs on *echo.Context — echo v5 made Context a struct, so the
// signature is a pointer — and it runs *before* WithHTTPContext has built the
// IHTTPContext. There is therefore no c.DB(), no c.Log(), no repository here:
// a middleware that needs a capability takes the App when it is constructed.
//
// That is a feature. Middleware is for cross-cutting concerns (auth, tracing,
// limits); business logic hidden in it is logic nobody finds when reading the
// handler that it changes.

// tenantContextKey is echo's own per-request store, which is what middleware
// and handlers share. It is not ctx.SetData — that lives on the IContext, which
// does not exist yet when this runs.
const tenantContextKey = "example.tenant_id"

// requireTenant rejects a request with no tenant header.
//
// Returning a core.IError straight from middleware is enough: the framework's
// error handler renders it exactly as it renders a handler's, so the rejection
// is logged, traced and shaped like every other response instead of being an
// echo.HTTPError in a different format.
func requireTenant() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec *echo.Context) error {
			tenant := ec.Request().Header.Get("X-Tenant-Id")
			if tenant == "" {
				return core.New(http.StatusBadRequest, "TENANT_REQUIRED",
					"the X-Tenant-Id header is required")
			}

			ec.Set(tenantContextKey, tenant)

			return next(ec)
		}
	}
}

// auditWrites is the middleware that does need a capability. Taking *core.App
// at construction — rather than reaching for a package-level variable — is what
// keeps two Apps in one process (every parallel test builds its own) from
// sharing one logger and one set of connections.
func auditWrites(app *core.App) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec *echo.Context) error {
			if ec.Request().Method == http.MethodGet {
				return next(ec)
			}

			started := time.Now()
			err := next(ec)

			// Build the context from the request's, so this line shares the
			// trace and the request id of everything the handler logged.
			ctx := app.NewContext(ec.Request().Context())
			ctx.Log().Info("write attempted",
				"method", ec.Request().Method,
				"path", ec.Request().URL.Path,
				"tenant", tenantOfEcho(ec),
				"took_ms", time.Since(started).Milliseconds(),
				"failed", err != nil,
			)

			return err
		}
	}
}

func tenantOfEcho(ec *echo.Context) string {
	tenant, _ := ec.Get(tenantContextKey).(string)

	return tenant
}

// tenantOf reads the same value from the handler side.
func tenantOf(c core.IHTTPContext) string {
	tenant, _ := c.Get(tenantContextKey).(string)

	return tenant
}

func mountMiddleware(e *core.Server, app *core.App) {
	// Order is the order they are added, and it is not cosmetic: auditWrites
	// names the tenant, so requireTenant has to have resolved it first. The
	// general rule is that anything a later middleware reads must be produced
	// by an earlier one — auth before any guard that inspects the user, body
	// limits before anything that reads a body.
	admin := e.Group("/admin", requireTenant(), auditWrites(app))
	admin.GET("/stats", tenantStats)

	// A third place to attach: one route. A group protects every route added to
	// it later, which is safer; a per-route line is readable on its own, which
	// makes an audit a grep rather than a walk up the file. Use the group when
	// forgetting is the bigger risk, the line when clarity is.
	//
	// core.BodyLimit is ordinary middleware, so a route can set a limit larger
	// *or smaller* than the server's — the value is read when the body is read,
	// and by then the innermost one has set it.
	e.POST("/webhooks/billing", handleWebhook, core.BodyLimit(64<<10))
}

func tenantStats(c core.IHTTPContext) error {
	return c.JSON(http.StatusOK, map[string]any{
		"tenant":   tenantOf(c),
		"articles": len(sampleArticles()),
	})
}

func handleWebhook(c core.IHTTPContext) error {
	payload := map[string]any{}
	if err := c.BindOnly(&payload); err != nil {
		return err
	}

	// Answer fast and do the work elsewhere: a sender that times out retries,
	// and a webhook processed inline is processed twice. Any goroutine started
	// here must carry a context of its own (ctx.WithContext(bg)), or it is
	// cancelled the moment this response is written.
	c.Log().Info("webhook received", "keys", len(payload))

	return c.NoContent(http.StatusAccepted)
}
