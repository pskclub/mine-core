package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/mssola/user_agent"
	"github.com/pskclub/mine-core/consts"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
)

type IHTTPContext interface {
	IContext
	echo.Context
	BindWithValidate(ctx IValidateContext) IError
	BindOnly(i interface{}) IError
	GetPageOptions() *PageOptions
	GetPageOptionsWithOptions(options *PageOptionsOptions) *PageOptions
	GetUserAgent() *user_agent.UserAgent
}

type HTTPContext struct {
	echo.Context
	IContext
	logger ILogger
}

type PageOptionsOptions struct {
	OrderByAllowed []string
}

func (c *HTTPContext) GetPageOptions() *PageOptions {
	limit, _ := strconv.ParseInt(c.QueryParam("limit"), 10, 64)
	page, _ := strconv.ParseInt(c.QueryParam("page"), 10, 64)

	if limit <= 0 {
		limit = consts.PageLimitDefault
	}

	if limit > consts.PageLimitMax {
		limit = consts.PageLimitMax
	}

	if page < 1 {
		page = 1
	}

	return &PageOptions{
		Q:       c.QueryParam("q"),
		Limit:   limit,
		Page:    page,
		OrderBy: c.genOrderBy(c.QueryParam("order_by")),
	}
}

func (c *HTTPContext) genOrderBy(s string) []string {
	orderBy := make([]string, 0)
	fields := strings.Split(s, ",")
	for _, field := range fields {
		spaceParameters := strings.Split(field, " ")
		bracketParameters := strings.Split(field, "(")
		if len(spaceParameters) == 1 && len(bracketParameters) == 1 && spaceParameters[0] != "" {
			orderBy = append(orderBy, fmt.Sprintf("%s desc", spaceParameters[0]))
		} else if len(spaceParameters) == 2 {
			name := spaceParameters[0]
			if name != "" {
				shortingParameter := spaceParameters[1]
				if shortingParameter == "asc" {
					orderBy = append(orderBy, fmt.Sprintf("%s %s", name, shortingParameter))
				} else {
					orderBy = append(orderBy, fmt.Sprintf("%s desc", name))
				}
			}
		} else if len(bracketParameters) == 2 {
			name := strings.TrimSuffix(bracketParameters[1], ")")
			if name != "" {
				shortingParameter := bracketParameters[0]
				if shortingParameter == "asc" {
					orderBy = append(orderBy, fmt.Sprintf("%s %s", name, shortingParameter))
				} else {
					orderBy = append(orderBy, fmt.Sprintf("%s desc", name))
				}
			}
		}
	}
	return orderBy
}

func (c *HTTPContext) GetPageOptionsWithOptions(options *PageOptionsOptions) *PageOptions {
	pageOptions := c.GetPageOptions()
	if options != nil {
		newOrderBy := make([]string, 0)
		for _, field := range pageOptions.OrderBy {
			parameters := strings.Split(field, " ")
			sortBy := parameters[0]
			for _, name := range options.OrderByAllowed {
				if sortBy == name {
					newOrderBy = append(newOrderBy, field)
				}
			}
		}
		pageOptions.OrderBy = newOrderBy
	}
	return pageOptions
}

type HandlerFunc func(IHTTPContext) error

type HTTPContextOptions struct {
	ContextOptions *ContextOptions
}

func NewHTTPContext(ctx echo.Context, options *HTTPContextOptions) IHTTPContext {
	ctxOptions := options.ContextOptions
	ctxOptions.contextType = consts.HTTP
	return &HTTPContext{Context: ctx, logger: nil, IContext: NewContext(ctxOptions)}
}

func WithHTTPContext(h HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		return h(c.(*HTTPContext))
	}
}

func (c *HTTPContext) validateJSON(i interface{}) IError {
	var body []byte
	if c.Request().Body != nil {
		body, _ = ioutil.ReadAll(c.Request().Body)
	}

	// Restore the io.ReadCloser to its original state
	c.Request().Body = ioutil.NopCloser(bytes.NewBuffer(body))
	err := json.Unmarshal(body, &i)
	if err != nil {
		switch err := err.(type) {
		case *json.UnmarshalTypeError:
			return NewValidatorFields(map[string]jsonErr{
				err.Field: {
					Code: "INVALID_TYPE",
					Message: fmt.Sprintf("This %s field must be %s type",
						err.Field, err.Type),
				},
			})

		default:
			return Error{
				Status:  http.StatusBadRequest,
				Code:    "INVALID_JSON",
				Message: "Must be json format"}
		}
	}
	return nil
}

func (c *HTTPContext) BindWithValidate(ctx IValidateContext) IError {
	if err := c.validateJSON(ctx); err != nil {
		newError := err.(IError)
		return newError
	}

	return ctx.Valid(c)
}

func (c *HTTPContext) BindOnly(i interface{}) IError {
	if err := c.validateJSON(i); err != nil {
		return err
	}
	c.Bind(i)
	return nil
}

func (c *HTTPContext) Log() ILogger {
	if c.logger == nil {
		c.logger = NewHTTPLogger(c)
	}
	return c.logger.(ILogger)
}

func (c HTTPContext) GetUserAgent() *user_agent.UserAgent {
	return user_agent.New(c.Request().UserAgent())
}
