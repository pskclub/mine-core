// Package consts holds the values the service branches on, which is where a set
// of allowed values usually lives — the handler compares against these, and the
// validator states them, so a literal repeated in the validator is a literal
// that drifts from the one the handler checks.
package consts

// Status is a typed string, declared the way a service normally declares one.
type Status string

// The two spellings a constant of this kind is written in: a plain string, and
// a conversion. Both stand for a value a caller must send as text.
const (
	StatusActive   = "ACTIVE"
	StatusInactive = Status("INACTIVE")
)

// Priority is a numeric enum, to prove a rule argument that is a named number
// reads as the number rather than as nothing.
const (
	PriorityLow  = 1
	PriorityHigh = 9
)
