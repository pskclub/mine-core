package core

import (
	"github.com/labstack/echo/v4"
)

func Core(options *HTTPContextOptions) func(next echo.HandlerFunc) echo.HandlerFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			cc := NewHTTPContext(c, options)
			return next(cc)
		}
	}
}
