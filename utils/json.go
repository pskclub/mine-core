package utils

import (
	"encoding/json"
	"errors"
	"fmt"
)

func JSONParse(body []byte, model interface{}) error {
	err := json.Unmarshal(body, model)
	if err != nil {
		switch err := err.(type) {
		case *json.UnmarshalTypeError:
			return errors.New(fmt.Sprintf("This %s field must be %s type", err.Field, err.Type))

		default:
			return errors.New("Must be json format")
		}
	}
	return nil
}

func JSONToString(s interface{}) string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}

	return string(b)
}
