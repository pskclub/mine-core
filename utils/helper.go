package utils

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
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

func StructToMap(src interface{}) (map[string]interface{}, error) {
	inrec, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}

	var to map[string]interface{}
	err = json.Unmarshal(inrec, &to)
	if err != nil {
		return nil, err
	}

	return to, nil
}

func RootDir() string {
	_, b, _, _ := runtime.Caller(0)
	d := path.Join(path.Dir(b))
	return filepath.Dir(d)
}
