package valid

import "reflect"

// BoolField chains bool rules.
type BoolField struct {
	v        *Validator
	name     string
	value    *bool
	failed   bool
	idx      int
	msgArmed bool
}

// Required fails when the value is nil.
func (f *BoolField) Required() *BoolField {
	f.msgArmed = false
	if f.value == nil && !f.failed {
		f.failed = true
		f.idx = f.v.add(f.name, "REQUIRED", nil)
		f.msgArmed = true
	}
	return f
}

// Message overrides the message of the preceding rule when it failed.
func (f *BoolField) Message(msg string) *BoolField {
	if f.msgArmed {
		f.v.overrideMessage(f.idx, msg)
		f.msgArmed = false
	}
	return f
}

// ArrayField chains slice/array rules. A nil value skips every rule except Required.
type ArrayField struct {
	v        *Validator
	name     string
	value    any
	failed   bool
	idx      int
	msgArmed bool
}

// Arr starts a slice/array field rule chain.
func (v *Validator) Arr(name string, value any) *ArrayField {
	return &ArrayField{v: v, name: name, value: value}
}

func (f *ArrayField) fail(code string, data any) *ArrayField {
	if f.failed {
		return f
	}
	f.failed = true
	f.idx = f.v.add(f.name, code, data)
	f.msgArmed = true
	return f
}

// Message overrides the message of the rule immediately before it, when that
// rule is the one that failed (custom message per rule).
func (f *ArrayField) Message(msg string) *ArrayField {
	if f.msgArmed {
		f.v.overrideMessage(f.idx, msg)
		f.msgArmed = false
	}
	return f
}

// length returns the element count and whether value is a slice/array.
func (f *ArrayField) length() (int, bool) {
	if f.value == nil {
		return 0, false
	}
	rv := reflect.ValueOf(f.value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return 0, false
	}
	return rv.Len(), true
}

// Required fails when the value is nil or an empty slice.
func (f *ArrayField) Required() *ArrayField {
	f.msgArmed = false
	n, ok := f.length()
	if !ok || n == 0 {
		return f.fail("REQUIRED", nil)
	}
	return f
}

// Size requires exactly n elements.
func (f *ArrayField) Size(n int) *ArrayField {
	f.msgArmed = false
	if f.failed || f.value == nil {
		return f
	}
	l, ok := f.length()
	if !ok || l != n {
		return f.fail("INVALID_ARRAY_SIZE", map[string]any{"size": n})
	}
	return f
}

// Min requires at least n elements.
func (f *ArrayField) Min(n int) *ArrayField {
	f.msgArmed = false
	if f.failed || f.value == nil {
		return f
	}
	l, ok := f.length()
	if !ok || l < n {
		return f.fail("INVALID_ARRAY_SIZE_MIN", map[string]any{"min": n})
	}
	return f
}

// Max requires at most n elements.
func (f *ArrayField) Max(n int) *ArrayField {
	f.msgArmed = false
	if f.failed || f.value == nil {
		return f
	}
	l, ok := f.length()
	if !ok || l > n {
		return f.fail("INVALID_ARRAY_SIZE_MAX", map[string]any{"max": n})
	}
	return f
}
