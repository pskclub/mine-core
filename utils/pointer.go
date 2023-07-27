package utils

import (
	"encoding/json"
)

func GetString(v *string) string {
	if v == nil {
		return ""
	}

	if *v == "" {
		return ""
	}

	return *v
}

func GetInt(v *int) int {
	if v == nil {
		return 0
	}

	return *v
}

func GetInt64(v *int64) int64 {
	if v == nil {
		return 0
	}

	return *v
}

func GetFloat64(v *float64) float64 {
	if v == nil {
		return 0
	}

	return *v
}

func GetJSON(v *json.RawMessage) json.RawMessage {
	if v == nil {
		return nil
	}
	e := *v
	return e
}

func GetArrayString(v []*string) []string {
	res := make([]string, 0)
	if v == nil {
		return nil
	}

	for _, value := range v {
		res = append(res, GetString(value))
	}
	return res
}

func GetBool(v *bool) bool {
	if v == nil {
		return false
	}
	e := *v

	return e
}

func GetBoolTrue(v *bool) bool {
	if v == nil {
		return true
	}
	e := *v

	return e
}

func GetBoolFalse(v *bool) bool {
	if v == nil {
		return false
	}
	e := *v
	return e
}

func ToPointer[T comparable](v T) *T {
	return &v
}
func ToNonPointer[T comparable](v *T) T {
	return *v
}
