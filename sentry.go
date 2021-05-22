package core

import (
	"fmt"
	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
	"github.com/labstack/echo/v4"
	"io/ioutil"
)

func CaptureError(ctx IContext, level sentry.Level, err error, args ...interface{}) {
	if true {
		sentry.ConfigureScope(func(scope *sentry.Scope) {
			scope.SetRequest(nil)
			scope.SetContext("env", ctx.ENV().All())
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

func CaptureHTTPError(ctx IHTTPContext, level sentry.Level, err error, args ...interface{}) {
	if true {
		if hub := sentryecho.GetHubFromContext(ctx); hub != nil {
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetRequest(ctx.Request())
				scope.SetContext("env", ctx.ENV().All())
				scope.SetLevel(level)
				scope.SetRequestBody(GetBodyString(ctx))
				scope.SetUser(sentry.User{
					IPAddress: ctx.RealIP(),
				})

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
