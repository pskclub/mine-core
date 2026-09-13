package core

import (
	"reflect"
	"strings"
)

// Where a bound value came from. The names match OpenAPI's `in`, so a client
// generator or an API console can use them directly.
const (
	SourcePath   = "path"
	SourceQuery  = "query"
	SourceHeader = "header"
	SourceBody   = "body"
)

// IFieldSourced is implemented by validation errors that can record where each
// field was bound from. Declared here rather than in valid/ because core cannot
// import valid (valid imports core); the HTTP layer only needs this behaviour,
// not the concrete type.
type IFieldSourced interface {
	// SetFieldSources takes field path -> source ("path"/"query"/"header"/"body").
	SetFieldSources(map[string]string)
}

// maxSourceDepth bounds the walk into nested payloads. Recursive types (a tree
// node holding its own kind) would otherwise never terminate.
const maxSourceDepth = 6

// markFieldSources labels a validation error with the origin of each field, read
// from the payload's own binding tags.
//
// Without it every violation looks alike: a request binding `id` from the path,
// `sort` from the query and `full_name` from the body reports all three in one
// flat map, and the caller cannot tell which part of the request to fix.
//
// Nothing is guessed: a field the validator named but no tag explains is left
// unlabelled.
func markFieldSources(payload any, err IError) {
	sourced, ok := err.(IFieldSourced)
	if !ok || payload == nil {
		return
	}

	sources := make(map[string]string)
	collectFieldSources(reflect.TypeOf(payload), "", 0, sources)
	sourced.SetFieldSources(sources)
}

// collectFieldSources walks a payload type and records, for each bindable
// field, the name the validator would use and where the value came from.
func collectFieldSources(t reflect.Type, prefix string, depth int, out map[string]string) {
	if t == nil || depth > maxSourceDepth {
		return
	}
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}

		name, source := fieldBinding(f)

		// an embedded struct contributes its fields at the parent's level
		if f.Anonymous && name == "" {
			collectFieldSources(f.Type, prefix, depth+1, out)
			continue
		}
		if name == "" {
			continue
		}

		path := prefix + name
		out[path] = source

		// Only body fields nest: a path or query parameter is always scalar, so
		// recursing into one would invent names no binder produces.
		if source == SourceBody {
			collectFieldSources(f.Type, path+".", depth+1, out)
		}
	}
}

// fieldBinding reports the name a field binds under and its source. Tags are
// checked most-specific first: a field tagged both `param` and `json` is filled
// from the path, so that is what a client must correct.
func fieldBinding(f reflect.StructField) (name, source string) {
	for _, t := range []struct {
		tag    string
		source string
	}{
		{"param", SourcePath},
		{"query", SourceQuery},
		{"header", SourceHeader},
		{"json", SourceBody},
		{"form", SourceBody},
		{"xml", SourceBody},
	} {
		if v, ok := f.Tag.Lookup(t.tag); ok {
			if n := tagName(v); n != "" {
				return n, t.source
			}
		}
	}

	// no binding tag: echo falls back to the field name, and so does the
	// convention validators follow
	if f.Anonymous {
		return "", ""
	}
	return f.Name, SourceBody
}

// tagName strips options ("name,omitempty") and reports "" for a skipped field.
func tagName(v string) string {
	name, _, _ := strings.Cut(v, ",")
	if name == "-" {
		return ""
	}
	return name
}
