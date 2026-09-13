package utils

import "github.com/jinzhu/copier"

// Copy deep-copies the matching fields of src into a fresh T and returns it.
// Thin wrapper over jinzhu/copier, kept from v1 but value-returning:
//
//	payload, err := utils.Copy[service.CreatePayload](input)
//
// T is a value type — a struct, slice or map, not a pointer. Copying a list
// works as-is: utils.Copy[[]NoteDTO](notes).
func Copy[T any](src any) (T, error) {
	var dst T
	err := copier.Copy(&dst, src)
	return dst, err
}
