package utils

import "github.com/google/uuid"

// NewUUID returns a new random UUID string (v1's GetUUID).
func NewUUID() string { return uuid.NewString() }

// ParseUUID parses a canonical UUID string.
func ParseUUID(s string) (uuid.UUID, error) { return uuid.Parse(s) }

// IsUUID reports whether s is a valid UUID string.
//
// (v1's IsUUID used uuid.FromBytes, which expects 16 raw bytes and therefore
// rejected ordinary UUID strings — this parses correctly.)
func IsUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}
