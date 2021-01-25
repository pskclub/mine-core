package core

import (
	"encoding/json"
	"github.com/thedevsaddam/gojsonq/v2"
	"github.com/pskclub/mine-core/utils"
	"gopkg.in/asaskevich/govalidator.v9"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DateFormat = "2006-01-02 15:04:05"
const TimeFormat = "15:04:05"
const TimeFormatRegex = "^([01]?[0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]$"

type IValidate interface {
	Valid() IError
}

type IValidateContext interface {
	Valid(ctx IContext) IError
}

type BaseValidator struct {
	validator *Valid
	prefix    string
}

func (b *BaseValidator) Error() IError {
	if b == nil || b.validator == nil {
		return nil
	}

	return b.validator.Valid()
}

func (b *BaseValidator) GetValidator() *Valid {
	return b.validator
}

func (b *BaseValidator) SetPrefix(prefix string) {
	b.prefix = prefix
}

func (b *BaseValidator) Must(condition bool, msg *IValidMessage) bool {
	if b.validator == nil {
		b.validator = NewValid()
	}

	if msg != nil {
		msg.Name = b.prefix + msg.Name
	}

	return b.validator.Must(condition, msg)
}

func (b *BaseValidator) Merge(errs ...*Valid) IError {
	err := NewValid()
	for _, value := range errs {
		if value == nil {
			continue
		}

		for _, value := range value.err.errors {
			err.Add(value)
		}

	}

	return err
}

func (b *BaseValidator) LoopJSONArray(j *json.RawMessage) []interface{} {
	js := make([]interface{}, 0)
	if j == nil {
		return js
	}

	err := json.Unmarshal(*j, &js)
	if err != nil {
		return make([]interface{}, 0)
	}
	return js
}

func (b *BaseValidator) IsStringNumber(field *string, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field == "" {
		return true, nil
	}

	_, err := strconv.Atoi(*field)
	if err != nil {
		return false, NumberM(fieldPath)
	}

	return true, nil
}

func (b *BaseValidator) IsStringNumberMin(field *string, min int64, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field == "" {
		return true, nil
	}

	n, err := strconv.ParseInt(*field, 10, 64)
	if err != nil {
		return true, nil
	}

	if n < min {
		return false, NumberMinM(fieldPath, min)
	}

	return true, nil
}

func (b *BaseValidator) IsFloatNumberBetween(field *float64, min float64, max float64, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field < min || *field > max {
		return false, FloatNumberBetweenM(fieldPath, min, max)
	}

	return true, nil
}

func (b *BaseValidator) IsFloatNumberMin(field *float64, min float64, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field < min {
		return false, FloatNumberMinM(fieldPath, min)
	}

	return true, nil
}

func (b *BaseValidator) IsFloatNumberMax(field *float64, max float64, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field > max {
		return false, FloatNumberMaxM(fieldPath, max)
	}

	return true, nil
}

func (b *BaseValidator) IsNumberBetween(field *int64, min int64, max int64, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field < min || *field > max {
		return false, NumberBetweenM(fieldPath, min, max)
	}

	return true, nil
}

func (b *BaseValidator) IsNumberMin(field *int64, min int64, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field < min {
		return false, NumberMinM(fieldPath, min)
	}

	return true, nil
}

func (b *BaseValidator) IsNumberMax(field *int64, max int64, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field > max {
		return false, NumberMaxM(fieldPath, max)
	}

	return true, nil
}

func (b *BaseValidator) IsStrRequired(field *string, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return false, RequiredM(fieldPath)
	}

	if *field == "" {
		return false, RequiredM(fieldPath)
	}

	return true, RequiredM(fieldPath)
}

func (b *BaseValidator) IsTimeRequired(field *time.Time, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return false, RequiredM(fieldPath)
	}

	return true, nil
}

func (b *BaseValidator) IsBase64(field *string, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field == "" {
		return false, Base64M(fieldPath)
	}

	if !govalidator.IsBase64(*field) {
		return false, Base64M(fieldPath)
	}

	return true, nil
}

func (b *BaseValidator) IsRequiredArray(array interface{}, fieldPath string) (bool, *IValidMessage) {
	if array == nil {
		return false, RequiredM(fieldPath)
	}

	kind := reflect.TypeOf(array).Kind()

	if !(kind == reflect.Array || kind == reflect.Slice) {
		return false, RequiredM(fieldPath)
	}

	if reflect.ValueOf(array).Len() == 0 {
		return false, RequiredM(fieldPath)
	}

	return true, RequiredM(fieldPath)
}

func (b *BaseValidator) IsRequired(field interface{}, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return false, RequiredM(fieldPath)
	}

	if reflect.ValueOf(field).IsNil() {
		return false, RequiredM(fieldPath)
	}

	return true, RequiredM(fieldPath)
}

func (b *BaseValidator) IsStrUnique(ctx IContext, field *string, table string, column string, except string, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	var result []interface{}
	db := ctx.DB().Table(table).Select(column).Where(column+" = ?", *field)
	if except != "" {
		db = db.Where(column+" != ?", except)
	}

	db.Scan(&result)

	if len(result) > 0 {
		return false, UniqueM(fieldPath)
	}

	return true, nil
}

func (b *BaseValidator) IsMongoStrUnique(ctx IContext, table string, filter interface{}, fieldPath string) (bool, *IValidMessage) {
	var result interface{}
	ctx.DBMongo().FindOne(&result, table, filter)
	if result != nil {
		return false, UniqueM(fieldPath)
	}

	return true, nil
}

func (b *BaseValidator) IsExistsWithCondition(
	ctx IContext,
	table string,
	condition map[string]interface{},
	fieldPath string,
) (bool, *IValidMessage) {
	if condition == nil {
		return true, nil
	}

	var result []interface{}
	db := ctx.DB().Table(table).Where(condition)

	db.Scan(&result)

	if len(result) == 0 {
		return false, ExistsM(fieldPath)
	}

	return true, nil
}

func (b *BaseValidator) IsMongoExistsWithCondition(
	ctx IContext,
	table string,
	filter interface{},
	fieldPath string,
) (bool, *IValidMessage) {
	if filter == nil {
		return true, nil
	}

	var result interface{}
	ctx.DBMongo().FindOne(&result, table, filter)

	if result == nil {
		return false, ExistsM(fieldPath)
	}

	return true, nil
}

func (b *BaseValidator) IsExists(ctx IContext, field *string, table string, column string, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field == "" {
		return true, nil
	}

	var result []interface{}
	db := ctx.DB().Table(table).Select(column).Where(column+" = ?", *field)

	db.Scan(&result)

	if len(result) == 0 {
		return false, ExistsM(fieldPath)
	}

	return true, nil
}

func (b *BaseValidator) IsJSONRequired(field *json.RawMessage, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return false, RequiredM(fieldPath)
	}

	return true, RequiredM(fieldPath)
}

func (b *BaseValidator) IsJSONObject(field *json.RawMessage, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, JSONArrayM(fieldPath)
	}

	var js map[string]interface{}
	return json.Unmarshal(*field, &js) == nil, JSONM(fieldPath)
}

func (b *BaseValidator) IsJSONArray(field *json.RawMessage, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, JSONArrayM(fieldPath)
	}

	var js []interface{}
	return json.Unmarshal(*field, &js) == nil, JSONArrayM(fieldPath)
}

func (b *BaseValidator) IsJSONArrayMin(field *json.RawMessage, min int, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	var js []interface{}
	if json.Unmarshal(*field, &js) != nil {
		return true, nil
	}

	if len(js) < min {
		return false, ArrayMinM(fieldPath, min)
	}

	return true, nil
}

func (b *BaseValidator) IsJSONArrayMax(field *json.RawMessage, max int, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	var js []interface{}
	if json.Unmarshal(*field, &js) != nil {
		return true, nil
	}

	if len(js) > max {
		return false, ArrayMaxM(fieldPath, max)
	}

	return true, nil
}

func (b *BaseValidator) IsJSONObjectPath(j *json.RawMessage, path string, fieldPath string) (bool, *IValidMessage) {
	if j == nil {
		return false, JSONObjectM(fieldPath)
	}

	val := gojsonq.New().FromString(string(*j)).Find(path)
	if val == nil {
		return true, JSONObjectM(fieldPath)
	}

	value, err := json.Marshal(val)
	if err != nil {
		return false, JSONObjectM(fieldPath)
	}

	var js map[string]interface{}
	return json.Unmarshal(value, &js) == nil, JSONObjectM(fieldPath)
}

func (b *BaseValidator) IsJSONStrPathRequired(json *json.RawMessage, path string, fieldPath string) (bool, *IValidMessage) {
	if json == nil {
		return false, RequiredM(fieldPath)
	}

	val := gojsonq.New().FromString(string(*json)).Find(path)
	if val == nil {
		return false, RequiredM(fieldPath)
	}
	_, ok := val.(string)
	if !ok {
		return false, StringM(fieldPath)
	}
	if val == "" || !ok {
		return false, RequiredM(fieldPath)
	}

	return true, RequiredM(fieldPath)
}

func (b *BaseValidator) IsJSONPathRequired(j *json.RawMessage, path string, fieldPath string) (bool, *IValidMessage) {
	if j == nil {
		return false, RequiredM(fieldPath)
	}

	val := gojsonq.New().FromString(string(*j)).Find(path)
	if val == nil {
		return false, RequiredM(fieldPath)
	}

	_, err := json.Marshal(val)
	if err != nil {
		return false, RequiredM(fieldPath)
	}

	return true, RequiredM(fieldPath)
}

func (b *BaseValidator) IsDateTime(input *string, fieldPath string) (bool, *IValidMessage) {
	if input == nil {
		return true, nil
	}
	_, ok := time.Parse(DateFormat, *input)
	if ok != nil {
		return false, DateTimeM(fieldPath)
	}

	return true, nil
}

func (b *BaseValidator) IsDateTimeAfter(input *string, after *string, fieldPath string) (bool, *IValidMessage) {
	checkAfter := func(after, check time.Time) bool {
		return check.After(after)
	}

	if input == nil {
		return true, nil
	}

	timeInput, ok := time.Parse(DateFormat, utils.GetString(input))
	if ok != nil {
		return false, DateTimeM(fieldPath)
	}

	afterInput, ok := time.Parse(DateFormat, utils.GetString(after))
	if ok != nil {
		return false, DateTimeM(fieldPath)
	}

	if !checkAfter(afterInput, timeInput) {
		return false, DateTimeAfterM(fieldPath, *after)
	}

	return true, nil
}

func (b *BaseValidator) IsDateTimeBefore(input *string, before *string, fieldPath string) (bool, *IValidMessage) {
	checkBefore := func(before, check time.Time) bool {
		return check.Before(before)
	}

	if input == nil {
		return true, nil
	}

	timeInput, ok := time.Parse(DateFormat, utils.GetString(input))
	if ok != nil {
		return false, DateTimeM(fieldPath)
	}

	beforeInput, ok := time.Parse(DateFormat, utils.GetString(before))
	if ok != nil {
		return false, DateTimeM(fieldPath)
	}

	if !checkBefore(beforeInput, timeInput) {
		return false, DateTimeBeforeM(fieldPath, *before)
	}

	return true, nil
}

func (b *BaseValidator) IsTimeAfter(input *string, after *string, fieldPath string) (bool, *IValidMessage) {
	checkAfter := func(after, check time.Time) bool {
		return check.After(after)
	}

	if input == nil {
		return true, nil
	}

	timeInput, ok := time.Parse(TimeFormat, utils.GetString(input))

	if ok != nil {
		return false, TimeM(fieldPath)
	}

	afterInput, ok := time.Parse(TimeFormat, utils.GetString(after))
	if ok != nil {
		return false, TimeM(fieldPath)
	}

	if !checkAfter(afterInput, timeInput) {
		return false, TimeAfterM(fieldPath, utils.GetString(after))
	}

	return true, nil
}

func (b *BaseValidator) IsTimeBefore(input *string, before *string, fieldPath string) (bool, *IValidMessage) {
	checkBefore := func(before, check time.Time) bool {
		return check.Before(before)
	}

	if input == nil {
		return true, nil
	}

	timeInput, ok := time.Parse(TimeFormat, utils.GetString(input))
	if ok != nil {
		return false, TimeM(fieldPath)
	}

	beforeInput, ok := time.Parse(TimeFormat, utils.GetString(before))
	if ok != nil {
		return false, TimeM(fieldPath)
	}

	if !checkBefore(beforeInput, timeInput) {
		return false, TimeBeforeM(fieldPath, utils.GetString(before))
	}

	return true, nil
}

func (b *BaseValidator) IsTime(input *string, fieldPath string) (bool, *IValidMessage) {
	if input == nil {
		return true, nil
	}

	r, _ := regexp.Compile(TimeFormatRegex)
	ok := r.Match([]byte(*input))
	if !ok {
		return false, TimeM(fieldPath)
	}

	return true, nil
}

func (b *BaseValidator) IsStrIn(input *string, rules string, fieldPath string) (bool, *IValidMessage) {
	if input == nil {
		return true, nil
	}

	split := strings.Split(rules, "|")

	return govalidator.IsIn(*input, split...), InM(fieldPath, rules)

}

func (b *BaseValidator) IsStrMax(input *string, size int, fieldPath string) (bool, *IValidMessage) {
	if input == nil {
		return true, nil
	}

	if len(*input) > size {
		return false, StrMaxM(fieldPath, size)
	}

	return true, nil
}

func (b *BaseValidator) IsArraySize(array interface{}, size int, fieldPath string) (bool, *IValidMessage) {
	if array == nil {
		return true, nil
	}

	kind := reflect.TypeOf(array).Kind()

	if !(kind == reflect.Array || kind == reflect.Slice) {
		return false, ArrayM(fieldPath)
	}

	if reflect.ValueOf(array).Len() != size {
		return false, ArraySizeM(fieldPath, size)
	}

	return true, nil

}

func (b *BaseValidator) IsArrayMin(array interface{}, size int, fieldPath string) (bool, *IValidMessage) {
	if array == nil {
		return true, nil
	}

	kind := reflect.TypeOf(array).Kind()

	if !(kind == reflect.Array || kind == reflect.Slice) {
		return false, ArrayMinM(fieldPath, size)
	}

	if reflect.ValueOf(array).Len() < size {
		return false, ArrayMinM(fieldPath, size)
	}

	return true, nil

}

func (b *BaseValidator) IsArrayMax(array interface{}, size int, fieldPath string) (bool, *IValidMessage) {
	if array == nil {
		return true, nil
	}

	kind := reflect.TypeOf(array).Kind()

	if !(kind == reflect.Array || kind == reflect.Slice) {
		return true, nil
	}

	if reflect.ValueOf(array).Len() > size {
		return false, ArrayMaxM(fieldPath, size)
	}

	return true, nil
}

func (b *BaseValidator) IsArrayBetween(array interface{}, min int, max int, fieldPath string) (bool, *IValidMessage) {
	if array == nil {
		return true, nil
	}

	if reflect.ValueOf(array).Len() < min || reflect.ValueOf(array).Len() > max {
		return false, ArrayBetweenM(fieldPath, min, max)
	}

	return true, nil
}

func (b *BaseValidator) IsURL(field *string, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field == "" {
		return true, nil
	}

	if _, err := url.ParseRequestURI(*field); err != nil {
		return false, URLM(fieldPath)
	}

	return true, nil
}

func (b *BaseValidator) IsIP(field *string, fieldPath string) (bool, *IValidMessage) {
	if field == nil {
		return true, nil
	}

	if *field == "" {
		return true, nil
	}

	if !govalidator.IsIP(*field) {
		return false, IPM(fieldPath)
	}

	return true, nil
}
