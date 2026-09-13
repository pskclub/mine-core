package llm

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// SchemaOf builds the JSON Schema a model must answer in, from a Go type.
//
// It deliberately emits a small subset — object/array/string/number/integer/
// boolean, plus enum and description — with every object closed
// (additionalProperties: false) and every field listed in required unless it
// says otherwise. That is the intersection every provider's structured-output
// mode actually enforces: the richer keywords ($ref, oneOf, minimum, pattern)
// are accepted by some and rejected by others, and a schema that silently
// degrades to "some JSON" defeats the point of asking for a schema at all.
//
// Field names come from the json tag. A field is optional when it is a pointer,
// when its json tag has omitempty, or when its jsonschema tag says optional.
//
//	type Invoice struct {
//	    Vendor string    `json:"vendor" jsonschema:"description=Company that issued it"`
//	    Total  float64   `json:"total"`
//	    Status string    `json:"status" jsonschema:"enum=draft|sent|paid"`
//	    Due    time.Time `json:"due"`
//	    Note   string    `json:"note,omitempty"`
//	}
//
// The jsonschema tag takes comma-separated options: description=…, enum=a|b|c,
// optional, required. A description cannot contain a comma — put the long form
// in the system prompt, which is where wording belongs anyway.
func SchemaOf[T any]() (*core.LLMSchema, core.IError) {
	var zero T
	t := reflect.TypeOf(&zero).Elem()

	body, err := schemaFor(t, map[reflect.Type]bool{}, "")
	if err != nil {
		return nil, err
	}
	if body["type"] != "object" {
		// Every provider's JSON mode wants an object at the root. Returning the
		// bare schema would be accepted here and rejected at the provider, one
		// layer further from the type that caused it.
		return nil, core.Newf(400, "LLM_INVALID_SCHEMA",
			"llm: %s cannot be the result type — a model answers with a JSON object, so use a struct", t)
	}
	return &core.LLMSchema{Name: schemaName(t), Schema: body, Strict: true}, nil
}

func schemaName(t reflect.Type) string {
	if n := t.Name(); n != "" {
		return strings.ToLower(n)
	}
	return "response"
}

// schemaFor maps one Go type. seen carries the types already being described so
// a struct that contains itself is reported here rather than recursing until
// the stack runs out — no provider supports recursive schemas anyway.
func schemaFor(t reflect.Type, seen map[reflect.Type]bool, path string) (map[string]any, core.IError) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	// time.Time is a struct, but every provider understands it as a formatted
	// string and none of them can fill in its unexported fields.
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}, nil
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil

	case reflect.Slice, reflect.Array:
		items, err := schemaFor(t.Elem(), seen, path+"[]")
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil

	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, unsupportedType(t, path, "a JSON object's keys are always strings")
		}
		values, err := schemaFor(t.Elem(), seen, path+"{}")
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": values}, nil

	case reflect.Struct:
		return structSchema(t, seen, path)
	}

	return nil, unsupportedType(t, path, "")
}

func structSchema(t reflect.Type, seen map[reflect.Type]bool, path string) (map[string]any, core.IError) {
	if seen[t] {
		return nil, core.Newf(400, "LLM_INVALID_SCHEMA",
			"llm: %s refers to itself%s — no provider supports a recursive schema; break the cycle or ask for the nested part separately",
			t, at(path))
	}
	seen[t] = true
	defer delete(seen, t)

	props := map[string]any{}
	required := []string{}

	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported: the model could not fill it in anyway
		}

		name, jsonOpts := jsonName(f)
		if name == "-" {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
			// An embedded struct with no json tag is flattened by encoding/json,
			// so the schema has to flatten it too or the model answers with a
			// nesting level that will not unmarshal.
			embedded, err := structSchema(f.Type, seen, path+"."+f.Name)
			if err != nil {
				return nil, err
			}
			for k, v := range embedded["properties"].(map[string]any) {
				props[k] = v
			}
			if req, ok := embedded["required"].([]string); ok {
				required = append(required, req...)
			}
			continue
		}

		field, err := schemaFor(f.Type, seen, path+"."+name)
		if err != nil {
			return nil, err
		}
		opts := parseSchemaTag(f.Tag.Get("jsonschema"))
		if opts.description != "" {
			field["description"] = opts.description
		}
		if len(opts.enum) > 0 {
			field["enum"] = opts.enum
		}
		props[name] = field

		optional := opts.optional ||
			(!opts.required && (strings.Contains(jsonOpts, "omitempty") || f.Type.Kind() == reflect.Pointer))
		if !optional {
			required = append(required, name)
		}
	}

	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
		// Closed objects are what stops a model from inventing an extra field
		// and having it silently dropped at unmarshal time.
		"additionalProperties": false,
	}, nil
}

func jsonName(f reflect.StructField) (name, opts string) {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name, ""
	}
	parts := strings.SplitN(tag, ",", 2)
	if len(parts) == 2 {
		opts = parts[1]
	}
	if parts[0] == "" {
		return f.Name, opts
	}
	return parts[0], opts
}

type schemaTag struct {
	description string
	enum        []string
	optional    bool
	required    bool
}

func parseSchemaTag(tag string) schemaTag {
	out := schemaTag{}
	if tag == "" {
		return out
	}
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "optional":
			out.optional = true
		case part == "required":
			out.required = true
		case strings.HasPrefix(part, "description="):
			out.description = strings.TrimPrefix(part, "description=")
		case strings.HasPrefix(part, "enum="):
			for _, v := range strings.Split(strings.TrimPrefix(part, "enum="), "|") {
				if v = strings.TrimSpace(v); v != "" {
					out.enum = append(out.enum, v)
				}
			}
		}
	}
	return out
}

func unsupportedType(t reflect.Type, path, why string) core.IError {
	msg := fmt.Sprintf("llm: %s cannot be described to a model%s", t, at(path))
	if why != "" {
		msg += " — " + why
	}
	return core.New(400, "LLM_INVALID_SCHEMA", msg)
}

func at(path string) string {
	if path == "" {
		return ""
	}
	return " (at " + strings.TrimPrefix(path, ".") + ")"
}
