package core

import (
	"fmt"
	"github.com/pskclub/mine-core/utils"
	"io/ioutil"

	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
	"github.com/labstack/echo/v4"
)

func CaptureError(ctx IContext, level sentry.Level, err error, args ...interface{}) {
	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetRequest(nil)
		valueMap, _ := utils.StructToMap(ctx.ENV().All())
		scope.SetContext("env", valueMap)
		scope.SetContext("context-data", ctx.GetAllData())
		requestID, ok := ctx.GetData(echo.HeaderXRequestID).(string)
		if ok {
			scope.SetTag("request_id", requestID)
		}

		scope.SetLevel(level)

		user := sentry.User{}

		if ctx.GetUser() != nil {
			user.ID = ctx.GetUser().ID
			user.Username = ctx.GetUser().Username
			user.Email = ctx.GetUser().Email
			user.Name = ctx.GetUser().Name
			user.Data = ctx.GetUser().Data
		}

		scope.SetUser(user)

		breadcrumbs, ok := ctx.GetData("breadcrumb").([]sentry.Breadcrumb)
		if !ok {
			breadcrumbs = make([]sentry.Breadcrumb, 0)
		}

		for _, breadcrumb := range breadcrumbs {
			scope.AddBreadcrumb(&breadcrumb, 30)
		}

		for i, arg := range args {
			scope.SetExtra(fmt.Sprintf("ARG-%v", i), arg)
		}

		if ierr, ok := err.(IError); ok {
			sentry.CaptureException(ierr.OriginalError())
		} else {
			sentry.CaptureException(err)
		}
	})
}

func CaptureHTTPError(ctx IHTTPContext, level sentry.Level, err error, args ...interface{}) {
	if hub := sentryecho.GetHubFromContext(ctx); hub != nil {
		hub.WithScope(func(scope *sentry.Scope) {
			scope.SetRequest(ctx.Request())
			scope.SetRequestBody(GetBodyString(ctx))
			breadcrumbs, ok := ctx.GetData("breadcrumb").([]sentry.Breadcrumb)
			if !ok {
				breadcrumbs = make([]sentry.Breadcrumb, 0)
			}

			for _, breadcrumb := range breadcrumbs {
				scope.AddBreadcrumb(&breadcrumb, 30)
			}

			valueMap, _ := utils.StructToMap(ctx.ENV().All())

			requestID, ok := ctx.GetData(echo.HeaderXRequestID).(string)
			if ok {
				scope.SetTag("request_id", requestID)
			}

			scope.SetContext("env", valueMap)
			scope.SetContext("context-data", ctx.GetAllData())
			scope.SetLevel(level)
			user := sentry.User{
				IPAddress: ctx.RealIP(),
			}

			if ctx.GetUser() != nil {
				user.ID = ctx.GetUser().ID
				user.Username = ctx.GetUser().Username
				user.Email = ctx.GetUser().Email
				user.Name = ctx.GetUser().Name
				user.Data = ctx.GetUser().Data
			}

			scope.SetUser(user)

			for i, arg := range args {
				scope.SetExtra(fmt.Sprintf("ARG-%v", i), arg)
			}

			if ierr, ok := err.(IError); ok {
				hub.CaptureException(ierr.OriginalError())
			} else {
				hub.CaptureException(err)
			}
		})
	}
}

func GetBodyString(c echo.Context) []byte {
	var body []byte
	if c.Request().Body != nil {
		body, _ = ioutil.ReadAll(c.Request().Body)
	}

	return body
}

func CaptureSimpleError(level sentry.Level, err error, args ...interface{}) {
	if !NewEnv().IsDev() {
		sentry.ConfigureScope(func(scope *sentry.Scope) {
			scope.SetLevel(level)
			for i, arg := range args {
				scope.SetExtra(fmt.Sprintf("ARG-%v", i), arg)
			}

			if ierr, ok := err.(IError); ok {
				sentry.CaptureException(ierr.OriginalError())
			} else {
				sentry.CaptureException(err)
			}
		})
	}
}

func CaptureErrorEcho(ctx echo.Context, level sentry.Level, err error) {
	if !NewEnv().IsDev() {
		sentry.ConfigureScope(func(scope *sentry.Scope) {

			scope.SetRequest(ctx.Request())
			scope.SetLevel(level)
			scope.SetRequestBody(GetBodyString(ctx))
			scope.SetExtra("Env", NewEnv().All())
			requestID, ok := ctx.Get(echo.HeaderXRequestID).(string)
			if ok {
				scope.SetTag("request_id", requestID)
			}

			scope.SetUser(sentry.User{
				IPAddress: ctx.RealIP(),
			})

			if ierr, ok := err.(IError); ok {
				sentry.CaptureException(ierr.OriginalError())
			} else {
				sentry.CaptureException(err)
			}
		})
	}
}
