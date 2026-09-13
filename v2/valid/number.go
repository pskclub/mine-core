package valid

// Number is the constraint for numeric field builders.
type Number interface{ ~int64 | ~float64 }

// NumberField chains numeric rules. A nil pointer skips every rule except Required.
type NumberField[T Number] struct {
	v        *Validator
	name     string
	value    *T
	failed   bool
	idx      int
	msgArmed bool
}

func (f *NumberField[T]) fail(code string, data any) *NumberField[T] {
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
func (f *NumberField[T]) Message(msg string) *NumberField[T] {
	if f.msgArmed {
		f.v.overrideMessage(f.idx, msg)
		f.msgArmed = false
	}
	return f
}

func (f *NumberField[T]) skip() bool {
	f.msgArmed = false
	return f.failed || f.value == nil
}

// Required fails when the value is nil.
func (f *NumberField[T]) Required() *NumberField[T] {
	f.msgArmed = false
	if f.value == nil {
		return f.fail("REQUIRED", nil)
	}
	return f
}

// Min checks a minimum (inclusive).
func (f *NumberField[T]) Min(min T) *NumberField[T] {
	if f.skip() {
		return f
	}
	if *f.value < min {
		return f.fail("INVALID_NUMBER_MIN", map[string]any{"min": min})
	}
	return f
}

// Max checks a maximum (inclusive).
func (f *NumberField[T]) Max(max T) *NumberField[T] {
	if f.skip() {
		return f
	}
	if *f.value > max {
		return f.fail("INVALID_NUMBER_MAX", map[string]any{"max": max})
	}
	return f
}

// Between checks the value lies within [min, max]. Uses the correct
// INVALID_NUMBER_BETWEEN code (v1 wrongly reused INVALID_NUMBER_MIN).
func (f *NumberField[T]) Between(min, max T) *NumberField[T] {
	if f.skip() {
		return f
	}
	if *f.value < min || *f.value > max {
		return f.fail("INVALID_NUMBER_BETWEEN", map[string]any{"min": min, "max": max})
	}
	return f
}

// In checks membership in a fixed set of numbers.
func (f *NumberField[T]) In(options ...T) *NumberField[T] {
	if f.skip() {
		return f
	}
	for _, o := range options {
		if *f.value == o {
			return f
		}
	}
	return f.fail("INVALID_VALUE_NOT_IN_LIST", map[string]any{"options": options})
}

// Positive requires a value greater than zero.
func (f *NumberField[T]) Positive() *NumberField[T] {
	if f.skip() {
		return f
	}
	if *f.value <= 0 {
		return f.fail("INVALID_NUMBER_MIN", map[string]any{"min": 0})
	}
	return f
}
