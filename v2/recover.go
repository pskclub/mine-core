package core

import "fmt"

// Recover converts a panic into an *Error stored in *dst, preserving a stack
// trace. Unlike v1's Recover (which re-panicked with a new string and discarded
// the stack), this lets a deferred call turn a panic into a normal error return.
//
//	func doWork() (err error) {
//	    defer core.Recover(&err)
//	    // ... code that may panic ...
//	    return nil
//	}
func Recover(dst *error) {
	r := recover()
	if r == nil {
		return
	}

	switch v := r.(type) {
	case error:
		*dst = Wrapf(v, "panic recovered: %v", v)
	default:
		*dst = Wrapf(fmt.Errorf("%v", v), "panic recovered: %v", v)
	}
}
