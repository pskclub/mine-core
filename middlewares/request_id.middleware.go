package middlewares

import (
	"encoding/base64"
	"github.com/labstack/echo/v4"
	"math/rand"
	"time"
)

var (
	random = rand.New(rand.NewSource(time.Now().UTC().UnixNano()))
)

func uuid(len int) string {
	bytes := make([]byte, len)
	random.Read(bytes)
	return base64.StdEncoding.EncodeToString(bytes)[:len]
}

func HTTPRequestID() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			rid := uuid(16)
			c.Set(echo.HeaderXRequestID, rid)
			c.Request().Header.Set(echo.HeaderXRequestID, rid)
			c.Response().Header().Set(echo.HeaderXRequestID, rid)

			return next(c)
		}
	}
}
