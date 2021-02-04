package core

import (
	"gopkg.in/asaskevich/govalidator.v9"
	"net/http"
	"strings"
)

type Validator struct{}

type FieldError struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Fields  interface{} `json:"fields,omitempty"`
}

func (FieldError) GetStatus() int {
	return http.StatusBadRequest
}

func (f FieldError) Error() string {
	return "ErrorCode : " + f.Code
}

func (f FieldError) JSON() interface{} {
	return f
}

func (f FieldError) GetCode() string {
	return f.Code
}

type jsonErr struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func ErrorToJson(err error) (m map[string]jsonErr) {
	m = make(map[string]jsonErr)
	for _, value := range err.(govalidator.Errors) {
		m[value.(govalidator.Error).Name] = jsonErr{
			Code:    strings.ToUpper(value.(govalidator.Error).Validator),
			Message: value.(govalidator.Error).Err.Error(),
		}
	}
	return
}

func (cv *Validator) Validate(i interface{}) error {
	govalidator.SetFieldsRequiredByDefault(true)

	defer Recover("Validator has errors")

	if _, err := govalidator.ValidateStruct(i); err != nil {
		return NewValidatorFields(ErrorToJson(err))
	}

	return nil
}

func NewValidatorFields(fields interface{}) IError {
	return &FieldError{
		Code:    "INVALID_PARAMS",
		Message: "Invalid parameters",
		Fields:  fields,
	}
}
