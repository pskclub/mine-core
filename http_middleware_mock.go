package core

import (
	"github.com/labstack/echo/v4"
	"gitlab.finema.co/finema/idin-core/consts"
	"net/http"
)

type MockMiddlewareWrapper func(model interface{}) interface{}
type MockMiddlewareManual func(c IHTTPContext) error
type MockMiddlewareOptions struct {
	Wrapper      MockMiddlewareWrapper
	Manual       MockMiddlewareManual
	IsPagination bool
	IsDisabled   bool
}

func MockMiddleware(model interface{}, options *MockMiddlewareOptions) func(next echo.HandlerFunc) echo.HandlerFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			cc := c.(*HTTPContext)

			if options != nil {
				if options.IsDisabled == true && !cc.ENV().IsMock() {
					return next(cc)
				}
				if options.Manual != nil {
					return options.Manual(cc)
				}
				err := Fake(model)
				if err != nil {
					return c.JSON(http.StatusInternalServerError, Map{
						"message": err.Error(),
					})
				}

				if options.IsPagination == true {
					return c.JSON(http.StatusOK, NewPagination(model, &PageResponse{
						Total: 230,
						Limit: consts.PageLimitDefault,
						Page:  1,
					}))
				}

				if options.Wrapper != nil {
					return c.JSON(http.StatusOK, options.Wrapper(model))
				}
			}

			return c.JSON(http.StatusOK, model)
		}
	}
}
