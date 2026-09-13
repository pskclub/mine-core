package utils

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// JSONParse unmarshals body into a T and returns it, with a friendly message on
// a type mismatch (kept from v1). The result type is given explicitly:
//
//	user, err := utils.JSONParse[User](body)
//	ids, err := utils.JSONParse[[]string](body)
func JSONParse[T any](body []byte) (T, error) {
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return out, fmt.Errorf("the %s field must be %s type", te.Field, te.Type)
		}
		return out, errors.New("must be json format")
	}
	return out, nil
}

// JSONToString marshals v to a compact JSON string ("" on error).
func JSONToString[T any](v T) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// StructToString marshals v to an indented JSON string ("" on error).
func StructToString[T any](v T) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// MapTo converts src into a T via JSON — struct↔map↔struct conversions:
//
//	user, err := utils.MapTo[User](map[string]any{"name": "alice"})
//	dto, err := utils.MapTo[UserDTO](userModel)
func MapTo[T any](src any) (T, error) {
	var out T
	b, err := json.Marshal(src)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

// StructToMap converts src to a map[string]any via JSON. It is MapTo with the
// result type fixed, for the common "give me the fields" case.
func StructToMap[T any](src T) (map[string]any, error) {
	return MapTo[map[string]any](src)
}

// IsZero reports whether v equals its type's zero value (comparable types).
func IsZero[T comparable](v T) bool {
	var zero T
	return v == zero
}

// IsEmpty reports whether v is nil or equal to its type's zero value. Unlike
// IsZero it accepts non-comparable types (slices, maps, structs holding them) at
// the cost of reflection — an empty-but-non-nil slice is *not* empty.
func IsEmpty[T any](v T) bool {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() { // nil interface
		return true
	}
	return rv.IsZero()
}
