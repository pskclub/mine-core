package core

import (
	"github.com/bxcodec/faker/v3"
)

func Fake(a interface{}) error {
	return faker.FakeData(a)
}
