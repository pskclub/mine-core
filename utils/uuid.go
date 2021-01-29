package utils

import (
	uuid "github.com/satori/go.uuid"
	"github.com/teris-io/shortid"
)

func GetUUID() string {
	return uuid.NewV4().String()
}

func GetShortID() string {
	return shortid.MustGenerate()
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
