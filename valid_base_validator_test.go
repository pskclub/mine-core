package core

import (
	"encoding/json"
	"github.com/asaskevich/govalidator"
	"github.com/pskclub/mine-core/utils"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestBaseValidator_IsStrIn(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is empty
	field := "bar"
	rules := "foo|bar|baz"
	valid, _ := validator.IsStrIn(&field, rules, "field")
	assert.True(t, valid)

	// Test when the field is one of the allowed values
	field = "foo"
	valid, _ = validator.IsStrIn(&field, rules, "field")
	assert.True(t, valid)

	// Test when the field is not in the allowed values
	field = "qux"
	valid, _ = validator.IsStrIn(&field, rules, "field")
	assert.False(t, valid)
}

func TestBaseValidator_IsStrMax(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is empty
	field := ""
	size := 10
	valid, _ := validator.IsStrMax(&field, size, "field")
	assert.True(t, valid)

	// Test when the field length is equal to the max size
	field = "example"
	valid, _ = validator.IsStrMax(&field, size, "field")
	assert.True(t, valid)

	// Test when the field length exceeds the max size
	field = "this is a long string"
	valid, _ = validator.IsStrMax(&field, size, "field")
	assert.False(t, valid)
}

func TestBaseValidator_IsStrMin(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is empty
	field := "gresd"
	size := 5
	valid, _ := validator.IsStrMin(&field, size, "field")
	assert.True(t, valid)

	// Test when the field length is equal to the min size
	field = "hello"
	valid, _ = validator.IsStrMin(&field, size, "field")
	assert.True(t, valid)

	// Test when the field length is less than the min size
	field = "hi"
	valid, _ = validator.IsStrMin(&field, size, "field")
	assert.False(t, valid)
}

func TestBaseValidator_IsStrRequired(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is an empty string
	field := ""
	valid, _ := validator.IsStrRequired(&field, "field")
	assert.False(t, valid)

	// Test when the field is a non-empty string
	field = "example"
	valid, _ = validator.IsStrRequired(&field, "field")
	assert.True(t, valid)
}

func TestBaseValidator_IsStringContain(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is an empty string
	field := ""
	substr := "world"
	valid, _ := validator.IsStringContain(&field, substr, "field")
	assert.True(t, valid)

	// Test when the field contains the substring
	field = "hello world"
	substr = "world"
	valid, _ = validator.IsStringContain(&field, substr, "field")
	assert.True(t, valid)

	// Test when the field does not contain the substring
	field = "hello world"
	substr = "foo"
	valid, _ = validator.IsStringContain(&field, substr, "field")
	assert.False(t, valid)
}

func TestBaseValidator_IsStringEndWith(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is an empty string
	field := ""
	substr := "world"
	valid, _ := validator.IsStringEndWith(&field, substr, "field")
	assert.True(t, valid)

	// Test when the field ends with the specified substring
	field = "hello world"
	substr = "world"
	valid, _ = validator.IsStringEndWith(&field, substr, "field")
	assert.True(t, valid)

	// Test when the field does not end with the specified substring
	field = "hello world"
	substr = "foo"
	valid, _ = validator.IsStringEndWith(&field, substr, "field")
	assert.False(t, valid)
}

func TestBaseValidator_IsStringLowercase(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is an empty string
	field := ""
	valid, _ := validator.IsStringLowercase(&field, "field")
	assert.True(t, valid)

	// Test when the field is lowercase
	field = "hello"
	valid, _ = validator.IsStringLowercase(&field, "field")
	assert.True(t, valid)

	// Test when the field is not lowercase
	field = "Hello"
	valid, _ = validator.IsStringLowercase(&field, "field")
	assert.False(t, valid)
}

func TestBaseValidator_IsStringNotContain(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is an empty string
	field := ""
	substr := "world"
	valid, _ := validator.IsStringNotContain(&field, substr, "field")
	assert.True(t, valid)

	// Test when the field does not contain the substring
	field = "hello world"
	substr = "foo"
	valid, _ = validator.IsStringNotContain(&field, substr, "field")
	assert.True(t, valid)

	// Test when the field contains the substring
	field = "hello world"
	substr = "world"
	valid, _ = validator.IsStringNotContain(&field, substr, "field")
	assert.False(t, valid)
}

func TestBaseValidator_IsStringNumber(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is an empty string
	field := ""
	valid, _ := validator.IsStringNumber(&field, "field")
	assert.True(t, valid)

	// Test when the field is a valid string number
	field = "123"
	valid, _ = validator.IsStringNumber(&field, "field")
	assert.True(t, valid)

	// Test when the field is not a valid string number
	field = "abc"
	valid, _ = validator.IsStringNumber(&field, "field")
	assert.False(t, valid)
}

func TestBaseValidator_IsStringNumberMin(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is an empty string
	field := ""
	valid, _ := validator.IsStringNumberMin(&field, 10, "field")
	assert.True(t, valid)

	// Test when the field is a valid string number greater than the min value
	field = "15"
	valid, _ = validator.IsStringNumberMin(&field, 10, "field")
	assert.True(t, valid)

	// Test when the field is a valid string number less than the min value
	field = "5"
	valid, _ = validator.IsStringNumberMin(&field, 10, "field")
	assert.False(t, valid)

	// Test when the field is not a valid string number
	field = "abc"
	valid, _ = validator.IsStringNumberMin(&field, 10, "field")
	assert.True(t, valid)
}

func TestBaseValidator_IsStringStartWith(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is an empty string
	field := ""
	substr := "hello"
	valid, _ := validator.IsStringStartWith(&field, substr, "field")
	assert.True(t, valid)

	// Test when the field starts with the specified substring
	field = "hello world"
	substr = "hello"
	valid, _ = validator.IsStringStartWith(&field, substr, "field")
	assert.True(t, valid)

	// Test when the field does not start with the specified substring
	field = "hello world"
	substr = "foo"
	valid, _ = validator.IsStringStartWith(&field, substr, "field")
	assert.False(t, valid)
}

func TestBaseValidator_IsStringUppercase(t *testing.T) {
	validator := &BaseValidator{}

	// Test when the field is an empty string
	field := ""
	valid, _ := validator.IsStringUppercase(&field, "field")
	assert.True(t, valid)

	// Test when the field is uppercase
	field = "HELLO"
	valid, _ = validator.IsStringUppercase(&field, "field")
	assert.True(t, valid)

	// Test when the field is not uppercase
	field = "Hello"
	valid, _ = validator.IsStringUppercase(&field, "field")
	assert.False(t, valid)
}

type testStruct struct {
	BaseValidator
	Name *string `json:"name"`
}

func (r testStruct) Valid(ctx IContext) IError {
	r.Must(r.IsCustom(func() (bool, *IValidMessage) {
		if r.Name == nil {
			return true, nil
		}
		return govalidator.IsIP(utils.GetString(r.Name)), RequiredM("field.required")
	}))

	return r.Error()
}

func TestBaseValidator_IsJSONBoolPathRequired(t *testing.T) {

	emptyObject := []byte(`{}`)
	objectWithWrongType := []byte(`{"required": "true"}`)
	objectWithCorrectType := []byte(`{"required": true}`)

	type fields struct {
		validator *Valid
		prefix    string
	}
	type args struct {
		json      *json.RawMessage
		path      string
		fieldPath string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		valid   bool
		message *IValidMessage
	}{
		{
			name: "Empty object",
			args: args{
				json:      (*json.RawMessage)(&emptyObject),
				path:      "required",
				fieldPath: "field.required",
			},
			valid:   false,
			message: RequiredM("field.required"),
		},
		{
			name: "Object with wrong type",
			args: args{
				json:      (*json.RawMessage)(&objectWithWrongType),
				path:      "required",
				fieldPath: "field.required",
			},
			valid:   false,
			message: BooleanM("field.required"),
		}, {
			name: "Object with correct type",
			args: args{
				json:      (*json.RawMessage)(&objectWithCorrectType),
				path:      "required",
				fieldPath: "field.required",
			},
			valid:   true,
			message: RequiredM("field.required"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &BaseValidator{
				validator: tt.fields.validator,
				prefix:    tt.fields.prefix,
			}
			valid, message := b.IsJSONBoolPathRequired(tt.args.json, tt.args.path, tt.args.fieldPath)
			assert.Equal(t, valid, tt.valid)
			assert.Equal(t, message, tt.message)
		})
	}
}

func TestBaseValidator_IsJSONPathRequireNotEmpty(t *testing.T) {
	emptyPathObject := []byte(`{"nested": {}}`)
	validPathObject := []byte(`{"nested": {"message": "hello world"}}`)
	emptyPathNextNestedObject := []byte(`{"nested": {"nextNested": {}}}`)
	validPathNextNestedObject := []byte(`{"nested": {"nextNested": {"message": "hello world"}}}`)
	type fields struct {
		validator *Valid
		prefix    string
	}
	type args struct {
		j         *json.RawMessage
		path      string
		fieldPath string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		valid   bool
		message *IValidMessage
	}{
		{
			name: "Empty path object",
			args: args{
				j:         (*json.RawMessage)(&emptyPathObject),
				path:      "nested",
				fieldPath: "field.nested",
			},
			valid:   false,
			message: JSONObjectEmptyM("field.nested"),
		}, {
			name: "Valid path object",
			args: args{
				j:         (*json.RawMessage)(&validPathObject),
				path:      "nested",
				fieldPath: "field.nested",
			},
			valid:   true,
			message: JSONObjectEmptyM("field.nested"),
		},
		{
			name: "Empty path nested object",
			args: args{
				j:         (*json.RawMessage)(&emptyPathNextNestedObject),
				path:      "nested.nextNested",
				fieldPath: "field.nested.nextNested",
			},
			valid:   false,
			message: JSONObjectEmptyM("field.nested.nextNested"),
		},
		{
			name: "Valid path nested object",
			args: args{
				j:         (*json.RawMessage)(&validPathNextNestedObject),
				path:      "nested.nextNested",
				fieldPath: "field.nested.nextNested",
			},
			valid:   true,
			message: JSONObjectEmptyM("field.nested.nextNested"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &BaseValidator{
				validator: tt.fields.validator,
				prefix:    tt.fields.prefix,
			}
			valid, message := b.IsJSONPathRequireNotEmpty(tt.args.j, tt.args.path, tt.args.fieldPath)
			assert.Equal(t, valid, tt.valid)
			assert.Equal(t, message, tt.message)
		})
	}
}

func TestBaseValidator_IsJSONObjectNotEmpty(t *testing.T) {
	emptyObject := []byte(`{}`)
	validObject := []byte(`{"message": "hello world"}`)
	type fields struct {
		validator *Valid
		prefix    string
	}
	type args struct {
		field     *json.RawMessage
		fieldPath string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		valid   bool
		message *IValidMessage
	}{
		{
			name: "Empty object",
			args: args{
				field:     (*json.RawMessage)(&emptyObject),
				fieldPath: "field",
			},
			valid:   false,
			message: JSONObjectEmptyM("field"),
		}, {
			name: "Valid object",
			args: args{
				field:     (*json.RawMessage)(&validObject),
				fieldPath: "field",
			},
			valid:   true,
			message: JSONObjectEmptyM("field"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &BaseValidator{
				validator: tt.fields.validator,
				prefix:    tt.fields.prefix,
			}
			valid, message := b.IsJSONObjectNotEmpty(tt.args.field, tt.args.fieldPath)
			assert.Equal(t, valid, tt.valid)
			assert.Equal(t, message, tt.message)
		})
	}
}
