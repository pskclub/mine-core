package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// errmsgs.DBError and its siblings are package-level values shared by every
// caller. Recording a Sentry event id on one would mark every later use of the
// same sentinel as already reported, and silence those reports for the life of
// the process.
func TestMarkCaptured_leavesSharedSentinelsAlone(t *testing.T) {
	sentinel := &Error{Status: 500, Code: "DATABASE_ERROR", Message: "database internal error"} // no stack, like a var
	markCaptured(sentinel, "evt-1")
	assert.Equal(t, "", capturedID(sentinel), "a sentinel is never marked")

	raised := New(500, "DATABASE_ERROR", "database internal error") // a call site: has a stack
	markCaptured(raised, "evt-2")
	assert.Equal(t, "evt-2", capturedID(raised), "an error raised at a call site is its own instance")
}
