package errmsgs

import (
	"fmt"
	"gitlab.finema.co/finema/idin-core"
	"net/http"
	"strings"
)

func IsNotFoundError(err core.IError) bool {
	if err == nil {
		return false
	}
	return err.GetStatus() == http.StatusNotFound
}

func IsNotFoundErrorCode(code string) bool {
	return code == NotFound.Code
}

var (
	DBError = core.Error{
		Status:  http.StatusInternalServerError,
		Code:    "DATABASE_ERROR",
		Message: "database internal error"}

	MQError = core.Error{
		Status:  http.StatusInternalServerError,
		Code:    "MQ_ERROR",
		Message: "mq internal error"}

	CacheError = core.Error{
		Status:  http.StatusInternalServerError,
		Code:    "CACHE_ERROR",
		Message: "cache internal error"}

	InternalServerError = core.Error{
		Status:  http.StatusInternalServerError,
		Code:    "INTERNAL_SERVER_ERROR",
		Message: "Internal server error"}

	NotFound = core.Error{
		Status:  http.StatusNotFound,
		Code:    "NOT_FOUND",
		Message: "not found"}

	BadRequest = core.Error{
		Status:  http.StatusBadRequest,
		Code:    "BAD_REQUEST",
		Message: "bad request"}

	SignatureInValid = core.Error{
		Status:  http.StatusBadRequest,
		Code:    "INVALID_SIGNATURE",
		Message: "Signature is not valid"}

	JSONInValid = core.Error{
		Status:  http.StatusBadRequest,
		Code:    "INVALID_JSON",
		Message: "Must be json format"}
)

func NotFoundCustomError(key string) core.Error {
	return core.Error{
		Status:  http.StatusNotFound,
		Code:    fmt.Sprintf("%s_NOT_FOUND", strings.ToUpper(key)),
		Message: fmt.Sprintf("%s is not found", strings.ToLower(key))}
}
