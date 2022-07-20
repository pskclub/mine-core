package core

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"testing"
)

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
