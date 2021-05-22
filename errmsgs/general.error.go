package errmsgs

import (
	"github.com/pskclub/mine-core"
	"net/http"
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

	ELSError = core.Error{
		Status:  http.StatusInternalServerError,
		Code:    "ELS_ERROR",
		Message: "elasticsearch internal error"}

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

	JSONInValid = core.Error{
		Status:  http.StatusBadRequest,
		Code:    "INVALID_JSON",
		Message: "Must be json format"}
)
