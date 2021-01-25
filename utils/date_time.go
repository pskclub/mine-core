package utils

import "time"

func GetCurrentDateTime() *time.Time {
	t := time.Now()
	return &t
}
