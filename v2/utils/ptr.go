// Package utils holds small, dependency-free generic helpers.
package utils

// ToPointer returns a pointer to v.
func ToPointer[T any](v T) *T { return &v }

// ToNonPointer returns the pointed-to value, or the zero value when p is nil.
func ToNonPointer[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// ToNonPointerOr returns the pointed-to value, or def when p is nil.
func ToNonPointerOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// PtrConvert converts *T to *R for string-underlying types (typically enums),
// nil-safe. It replaces the verbose deref→convert→re-pointer dance:
//
//	// before
//	Type: utils.ToPointer(models.PMOProjectType(utils.ToNonPointer(input.Type))),
//	// after
//	Type: utils.PtrConvert[models.PMOProjectType](input.Type),
//
// R is given explicitly; T is inferred. Returns nil when p is nil.
func PtrConvert[R ~string, T ~string](p *T) *R {
	if p == nil {
		return nil
	}
	r := R(*p)
	return &r
}

// PtrConvertNum is PtrConvert for numeric-underlying types (int/uint/float enums).
//
//	Priority: utils.PtrConvertNum[models.Priority](input.Priority),
func PtrConvertNum[R Numeric, T Numeric](p *T) *R {
	if p == nil {
		return nil
	}
	r := R(*p)
	return &r
}

// Numeric constrains numeric underlying types.
type Numeric interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64
}

// Convert converts a non-pointer value between string-underlying types.
//
//	status := utils.Convert[models.PMOProjectStatus](input.Status) // string → PMOProjectStatus
func Convert[R ~string, T ~string](v T) R { return R(v) }

// ConvertNum is Convert for numeric-underlying types.
func ConvertNum[R Numeric, T Numeric](v T) R { return R(v) }

// MapPtr maps *T to *R via fn, nil-safe. Use it for conversions the typed helpers
// above don't cover (structs, non-trivial transforms):
//
//	dto := utils.MapPtr(model.CreatedAt, func(t time.Time) string { return t.Format(time.RFC3339) })
func MapPtr[T, R any](p *T, fn func(T) R) *R {
	if p == nil {
		return nil
	}
	r := fn(*p)
	return &r
}
