// Package errmsgs holds reusable, framework-level *core.Error values. Consumers
// wrap them via core.Wrap(err, "...") or return them directly. Matching v1's
// errmsgs package so the names stay familiar.
package errmsgs

import (
	"fmt"
	"net/http"
	"strings"

	core "github.com/pskclub/mine-core/v2"
)

// Reusable sentinels. Compare with errors.Is (they match by code) or return a
// copy enriched via the core.Error builder helpers.
var (
	DBError             = core.New(http.StatusInternalServerError, "DATABASE_ERROR", "database internal error")
	MQError             = core.New(http.StatusInternalServerError, "MQ_ERROR", "mq internal error")
	CacheError          = core.New(http.StatusInternalServerError, "CACHE_ERROR", "cache internal error")
	CronjobError        = core.New(http.StatusInternalServerError, "CRONJOB_ERROR", "cronjob internal error")
	InternalServerError = core.New(http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "Internal server error")

	NotFound         = core.New(http.StatusNotFound, "NOT_FOUND", "not found")
	BadRequest       = core.New(http.StatusBadRequest, "BAD_REQUEST", "bad request")
	Unauthorized     = core.New(http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
	Forbidden        = core.New(http.StatusForbidden, "FORBIDDEN", "forbidden")
	SignatureInvalid = core.New(http.StatusBadRequest, "INVALID_SIGNATURE", "Signature is not valid")
	JSONInvalid      = core.New(http.StatusBadRequest, "INVALID_JSON", "Must be json format")
	JWTInvalid       = core.New(http.StatusBadRequest, "INVALID_JWT", "jwt is invalid")
)

// NotFoundCustomError builds a "<KEY>_NOT_FOUND" error for a specific resource.
func NotFoundCustomError(key string) *core.Error {
	return core.New(
		http.StatusNotFound,
		fmt.Sprintf("%s_NOT_FOUND", strings.ToUpper(key)),
		fmt.Sprintf("%s is not found", strings.ToLower(key)),
	)
}

// IsNotFoundError reports whether err maps to HTTP 404.
func IsNotFoundError(err core.IError) bool {
	if err == nil {
		return false
	}
	return err.GetStatus() == http.StatusNotFound
}

// IsNotFoundErrorCode reports whether code is the not-found code.
func IsNotFoundErrorCode(code string) bool {
	return code == NotFound.GetCode()
}
