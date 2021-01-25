package utils

import (
	"encoding/json"
	"fmt"
	"reflect"
)

func IsEmpty(x interface{}) bool {
	if x == nil {
		return true
	}
	return reflect.DeepEqual(x, reflect.Zero(reflect.TypeOf(x)).Interface())
}

func StructToString(v interface{}) string {
	value, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return ""
	}
	return string(value)
}

func LogStruct(v interface{}) {
	fmt.Println(StructToString(v))
}

func MapToStruct(src interface{}, to interface{}) error {
	bytes, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes, to)
}
