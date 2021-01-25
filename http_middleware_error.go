package core

import (
	"fmt"
	"github.com/getsentry/sentry-go"
	"github.com/go-errors/errors"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"net/http"
)

func RecoverWithConfig(env IENV, config middleware.RecoverConfig) echo.MiddlewareFunc {
	// Defaults
	if config.Skipper == nil {
		config.Skipper = middleware.DefaultRecoverConfig.Skipper
	}
	if config.StackSize == 0 {
		config.StackSize = middleware.DefaultRecoverConfig.StackSize
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			cc := c.(IHTTPContext)
			if config.Skipper(c) {
				return next(c)
			}

			defer func() {
				if r := recover(); r != nil {
					err, ok := r.(error)
					if !ok {
						err = fmt.Errorf("%v", r)
					}
					errWrap := errors.Wrap(err, 1)
					if !config.DisablePrintStack {
						stack := errWrap.ErrorStack()
						fmt.Println(stack)
					}
					cc.Log().Error(errWrap)
					cc.Error(err)
				}
			}()
			return next(c)
		}
	}
}

func HandleError(err error, c echo.Context) {
	CaptureErrorEcho(c, sentry.LevelError, err)

	c.JSON(http.StatusInternalServerError, map[string]interface{}{
		"code":    "INTERNAL_SERVER_ERROR",
		"message": err.Error(),
	})
}
func HandleNotFound(c echo.Context) error {
	return c.JSON(http.StatusNotFound, map[string]interface{}{
		"code":    "URL_NOT_FOUND",
		"message": "url not found",
	})
}
