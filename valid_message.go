package core

import (
	"fmt"
	"strings"
)

var RequiredM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "REQUIRED",
		Message: "The " + field + " field is required",
	}
}

var Base64M = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "BASE64",
		Message: "The " + field + " must be base64 format",
	}
}

var StringM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_TYPE",
		Message: "The " + field + " field must be string",
	}
}

var BooleanM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_TYPE",
		Message: "The " + field + " field must be boolean",
	}
}

var NumberM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_TYPE",
		Message: "The " + field + " field cannot parse to number",
	}
}

var FloatNumberBetweenM = func(field string, min float64, max float64) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_NUMBER_MIN",
		Message: fmt.Sprintf("The %v field must be from %v to %v", field, min, max),
	}
}

var FloatNumberMinM = func(field string, size float64) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_NUMBER_MIN",
		Message: fmt.Sprintf("The %v field must be greater than or equal %v", field, size),
		Data:    size,
	}
}

var FloatNumberMaxM = func(field string, size float64) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_NUMBER_MAX",
		Message: fmt.Sprintf("The %v field must be less than or equal %v", field, size),
		Data:    size,
	}
}

var NumberBetweenM = func(field string, min int64, max int64) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_NUMBER_MIN",
		Message: fmt.Sprintf("The %v field must be from %v to %v", field, min, max),
	}
}

var NumberMinM = func(field string, size int64) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_NUMBER_MIN",
		Message: fmt.Sprintf("The %v field must be greater than or equal %v", field, size),
		Data:    size,
	}
}

var NumberMaxM = func(field string, size int64) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_NUMBER_MAX",
		Message: fmt.Sprintf("The %v field must be less than or equal %v", field, size),
		Data:    size,
	}
}

var StringContainM = func(field string, substr string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_STRING_CONTAIN",
		Message: fmt.Sprintf("The %v field must contain %v", field, substr),
		Data:    substr,
	}
}

var StringNotContainM = func(field string, substr string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_STRING_NOT_CONTAIN",
		Message: fmt.Sprintf("The %v field must not contain %v", field, substr),
		Data:    substr,
	}
}

var StringStartWithM = func(field string, substr string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_STRING_START_WITH",
		Message: fmt.Sprintf("The %v field must start with %v", field, substr),
		Data:    substr,
	}
}

var StringEndWithM = func(field string, substr string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_STRING_END_WITH",
		Message: fmt.Sprintf("The %v field must end with %v", field, substr),
		Data:    substr,
	}
}

var StringLowercaseM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_STRING_LOWERCASE",
		Message: fmt.Sprintf("The %v field must be lowercase", field),
	}
}

var StringUppercaseM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_STRING_UPPERCASE",
		Message: fmt.Sprintf("The %v field must be uppercase", field),
	}
}
var UniqueM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "UNIQUE",
		Message: "The " + field + " field's value already exists",
	}
}

var ExistsM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "NOT_EXISTS",
		Message: "The " + field + " field's value is not exists",
	}
}

var URLM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_URL",
		Message: "The " + field + " field is not url",
	}
}

var JSONM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_JSON",
		Message: "The " + field + " field must be json",
	}
}

var JSONObjectM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_JSON",
		Message: "The " + field + " field must be json object",
	}
}

var JSONObjectEmptyM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_JSON",
		Message: "The " + field + " field cannot be empty object",
	}
}

var JSONArrayM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_JSON_ARRAY",
		Message: "The " + field + " field must be array format",
	}
}

var ArrayM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_ARRAY",
		Message: "The " + field + " field must be array format",
	}
}

var DateTimeM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_DATE_TIME",
		Message: "The " + field + ` field must be in "yyyy-MM-dd HH:mm:ss" format`,
	}
}

var DateTimeBeforeM = func(field string, before string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_DATE_TIME",
		Message: fmt.Sprintf("The "+field+` field must be before "%s"`, before),
	}
}

var DateTimeAfterM = func(field string, after string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_DATE_TIME",
		Message: fmt.Sprintf("The "+field+` field must be after "%s"`, after),
	}
}

var TimeBeforeM = func(field string, before string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_TIME",
		Message: fmt.Sprintf("The "+field+` field must be before "%s"`, before),
	}
}

var TimeAfterM = func(field string, after string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_TIME",
		Message: fmt.Sprintf("The "+field+` field must be after "%s"`, after),
	}
}

var TimeM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_TIME",
		Message: "The " + field + ` field must be in "HH:mm:ss"" format`,
	}
}

var InM = func(field string, rules string) *IValidMessage {
	split := strings.Split(rules, "|")
	msg := strings.Join(split, ", ")
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_VALUE_NOT_IN_LIST",
		Message: "The " + field + " field must be one of " + msg,
		Data:    split,
	}
}

var ArraySizeM = func(field string, size int) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_ARRAY_SIZE",
		Message: fmt.Sprintf("The %v field must contain %v item(s)", field, size),
		Data:    size,
	}
}

var ArrayMinM = func(field string, min int) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_ARRAY_SIZE_MIN",
		Message: fmt.Sprintf("The %v field required at least %v items", field, min),
		Data:    min,
	}
}

var ArrayMaxM = func(field string, max int) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_ARRAY_SIZE_MAX",
		Message: fmt.Sprintf("The %v field must not be greater than %v item(s)", field, max),
		Data:    max,
	}
}

var ArrayBetweenM = func(field string, min int, max int) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_ARRAY_SIZE_BETWEEN",
		Message: fmt.Sprintf("The %v field field must contain between %v and %v item(s)", field, min, max),
		Data: map[string]interface{}{
			"min": min,
			"max": max,
		},
	}
}

var StrMaxM = func(field string, max int) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_STRING_SIZE_MAX",
		Message: fmt.Sprintf("The %v field must not be longer than %v character(s)", field, max),
		Data:    max,
	}
}

var StrMinM = func(field string, min int) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_STRING_SIZE_MIN",
		Message: fmt.Sprintf("The %v field must not be shorter than %v character(s)", field, min),
		Data:    min,
	}
}

var IPM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_IP",
		Message: fmt.Sprintf("The %v field must be IP Address", field),
	}
}

var EmailM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_EMAIL",
		Message: fmt.Sprintf("The %v field must be Email Address", field),
	}
}

var ObjectEmptyM = func(field string) *IValidMessage {
	return &IValidMessage{
		Name:    field,
		Code:    "INVALID_TYPE",
		Message: "The " + field + " field cannot be empty object",
	}
}
