package utils

// DerefSlice returns a []T from a []*T, using the zero value for nil elements.
// Replaces v1's GetArrayString (which was string-only).
func DerefSlice[T any](in []*T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	for i, p := range in {
		if p != nil {
			out[i] = *p
		}
	}
	return out
}

// PtrSlice returns a []*T from a []T.
func PtrSlice[T any](in []T) []*T {
	if in == nil {
		return nil
	}
	out := make([]*T, len(in))
	for i := range in {
		v := in[i]
		out[i] = &v
	}
	return out
}

// Map applies fn to every element.
func Map[T, R any](in []T, fn func(T) R) []R {
	out := make([]R, len(in))
	for i, v := range in {
		out[i] = fn(v)
	}
	return out
}

// Filter keeps the elements for which keep returns true.
func Filter[T any](in []T, keep func(T) bool) []T {
	out := make([]T, 0, len(in))
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

// Contains reports whether v is present in list.
func Contains[T comparable](list []T, v T) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Unique returns list with duplicates removed, preserving first-seen order.
func Unique[T comparable](list []T) []T {
	seen := make(map[T]struct{}, len(list))
	out := make([]T, 0, len(list))
	for _, v := range list {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
