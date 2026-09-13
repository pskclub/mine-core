package utils

import "time"

// NowPtr returns a pointer to the current time (v1's GetCurrentDateTime).
func NowPtr() *time.Time {
	t := time.Now()
	return &t
}
