package utils

import (
	"fmt"
	"github.com/go-faker/faker/v4"
	"reflect"
)

func MockExplorer() {
	_ = faker.AddProvider("did", func(v reflect.Value) (interface{}, error) {
		return GenerateDID(NewSha256(GetCurrentDateTime().String()), "idin"), nil
	})

	_ = faker.AddProvider("id", func(v reflect.Value) (interface{}, error) {
		return NewSha256(GetUUID()), nil
	})

	_ = faker.AddProvider("pem", func(v reflect.Value) (interface{}, error) {
		p := fmt.Sprintf("-----BEGIN PUBLIC KEY-----\n%s%s%s\n-----END PUBLIC KEY-----", NewSha256(GetUUID()), NewSha256(GetUUID()), NewSha256(GetUUID()))
		return &p, nil
	})
}
