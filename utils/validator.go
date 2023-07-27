package utils

import (
	"errors"
	"github.com/asaskevich/govalidator"
	"gorm.io/gorm"
	"strings"
)

func IsStrIn(input *string, rules string, fieldPath string) (bool, error) {
	if input == nil {
		return true, nil
	}

	split := strings.Split(rules, "|")

	if govalidator.IsIn(*input, split...) {
		return true, nil
	}

	return false, errors.New("The " + fieldPath + " field must be one of " + strings.Join(split, ", "))

}

func IsExists(db *gorm.DB, field *string, table string, column string, fieldPath string) (bool, error) {
	if field == nil {
		return true, nil
	}

	if *field == "" {
		return true, nil
	}

	var result []interface{}
	db = db.Table(table).Select(column).Where(column+" = ?", *field)

	db.Scan(&result)

	if len(result) == 0 {
		return false, errors.New("The " + fieldPath + " field's value is not exists")
	}

	return true, nil
}

func IsExistsWithCondition(
	db *gorm.DB,
	table string,
	condition map[string]interface{},
	fieldPath string,
) (bool, error) {
	if condition == nil {
		return true, nil
	}

	var result []interface{}
	db = db.Table(table).Where(condition)

	db.Scan(&result)

	if len(result) == 0 {
		return false, errors.New("The " + fieldPath + " field's value is not exists")
	}

	return true, nil
}
