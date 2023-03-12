package core

import "github.com/go-faker/faker/v4"

func Fake(a interface{}) error {
	return faker.FakeData(a)
}
