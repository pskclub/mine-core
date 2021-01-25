package core

import "github.com/labstack/echo/v4"

func CreateLoggerMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if err := next(c); err != nil {
			c.Error(err)
		}
		return nil
	}
}
