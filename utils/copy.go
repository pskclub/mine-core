package utils

import "github.com/jinzhu/copier"

func Copy(toValue interface{}, fromValue interface{}) (err error) {
	return copier.Copy(toValue, fromValue)
}
