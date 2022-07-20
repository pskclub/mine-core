package utils

import uuid "github.com/satori/go.uuid"

func GetUUID() string {
	return uuid.NewV4().String()
}

func ToUUID(str string) (uuid.UUID, error) {
	return uuid.FromString(str)
}

func IsUUID(str string) bool {
	_, err := uuid.FromString(str)
	if err != nil {
		return false
	}

	return true
}
