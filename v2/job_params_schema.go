package core

import (
	"reflect"
	"strings"
	"time"
)

// ParamKind is the shape of a job parameter, in terms an admin UI can render a
// form from.
type ParamKind string

const (
	ParamString   ParamKind = "string"
	ParamInt      ParamKind = "int"
	ParamNumber   ParamKind = "number"
	ParamBool     ParamKind = "bool"
	ParamDate     ParamKind = "date"     // YYYY-MM-DD
	ParamTime     ParamKind = "time"     // HH:mm:ss
	ParamDateTime ParamKind = "datetime" // RFC3339
	ParamEnum     ParamKind = "enum"
	ParamArray    ParamKind = "array"
	ParamObject   ParamKind = "object"
	ParamAny      ParamKind = "any"
)

// ParamField describes one parameter of a job: what it is called, what shape it
// takes, whether it is required, and anything else a form needs. It is what the
// admin API publishes.
//
// Build these with the Params helper rather than by hand:
//
//	core.JobDef{
//	    Name: "sales-report",
//	    Params: core.Params(
//	        core.DateParam("date").Required().Desc("Day to report on").Example("2026-07-01"),
//	        core.EnumParam("status", "draft", "sent", "paid").Default("sent"),
//	        core.IntParam("limit").Desc("Maximum rows"),
//	        core.BoolParam("force"),
//	        core.ArrayParam("emails", core.StringParam("")),
//	    ),
//	}
type ParamField struct {
	Name string    `json:"name"`
	Kind ParamKind `json:"kind"`
	// Label is the display name, when the field name is not what a human should
	// read (translations, wording).
	Label       string   `json:"label,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Example     string   `json:"example,omitempty"`

	// Options is Enum with a label per value, for a select whose text differs
	// from the value sent. Enum stays populated alongside it.
	Options []ParamOption `json:"options,omitempty"`

	// Min/Max bound a number, a string's length, or an array's size.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// Pattern is a regular expression the value should match.
	Pattern string `json:"pattern,omitempty"`
	// Multiline asks for a text area rather than a single-line input.
	Multiline bool `json:"multiline,omitempty"`

	Items  *ParamField  `json:"items,omitempty"`  // element type of an array
	Fields []ParamField `json:"fields,omitempty"` // members of an object

	// Meta carries anything this framework does not model — a widget name, a
	// form group, an ordering hint, whatever your UI needs. It is passed through
	// untouched.
	Meta map[string]any `json:"meta,omitempty"`
}

// ParamOption is one choice of an enum, with the text to show for it.
type ParamOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// Opt builds an enum option. Pass no label to reuse the value.
func Opt(value string, label ...string) ParamOption {
	o := ParamOption{Value: value, Label: value}
	if len(label) > 0 && label[0] != "" {
		o.Label = label[0]
	}
	return o
}

// ---------------------------------------------------------------------------
// Declaring parameters
// ---------------------------------------------------------------------------

// ParamSpec builds a ParamField. Every method returns the spec, so rules read as
// one chain.
type ParamSpec struct{ f ParamField }

func newParamSpec(name string, kind ParamKind) *ParamSpec {
	return &ParamSpec{f: ParamField{Name: name, Kind: kind}}
}

// StringParam declares a free-text parameter.
func StringParam(name string) *ParamSpec { return newParamSpec(name, ParamString) }

// IntParam declares a whole-number parameter.
func IntParam(name string) *ParamSpec { return newParamSpec(name, ParamInt) }

// NumberParam declares a decimal parameter.
func NumberParam(name string) *ParamSpec { return newParamSpec(name, ParamNumber) }

// BoolParam declares a true/false parameter.
func BoolParam(name string) *ParamSpec { return newParamSpec(name, ParamBool) }

// DateParam declares a calendar date (YYYY-MM-DD).
func DateParam(name string) *ParamSpec { return newParamSpec(name, ParamDate) }

// TimeParam declares a wall-clock time (HH:mm:ss).
func TimeParam(name string) *ParamSpec { return newParamSpec(name, ParamTime) }

// DateTimeParam declares an instant (RFC3339).
func DateTimeParam(name string) *ParamSpec { return newParamSpec(name, ParamDateTime) }

// EnumParam declares a parameter limited to a fixed set of values.
func EnumParam(name string, values ...string) *ParamSpec {
	s := newParamSpec(name, ParamEnum)
	s.f.Enum = values
	return s
}

// ArrayParam declares a list. item describes one element; its name is ignored.
func ArrayParam(name string, item *ParamSpec) *ParamSpec {
	s := newParamSpec(name, ParamArray)
	if item != nil {
		field := item.Build()
		field.Name = ""
		s.f.Items = &field
	}
	return s
}

// ObjectParam declares a nested object with its own fields.
func ObjectParam(name string, fields ...*ParamSpec) *ParamSpec {
	s := newParamSpec(name, ParamObject)
	s.f.Fields = Params(fields...)
	return s
}

// AnyParam declares a parameter with no fixed shape (a free-form value).
func AnyParam(name string) *ParamSpec { return newParamSpec(name, ParamAny) }

// CustomParam declares a parameter of a kind this package does not define — a
// widget of your own ("duration", "currency", "user-picker"). Everything else
// works the same; consumers that do not know the kind fall back to a text input.
//
//	core.CustomParam("payout", "currency").
//	    Meta("currency", "THB").
//	    Desc("Amount to pay out")
func CustomParam(name string, kind ParamKind) *ParamSpec { return newParamSpec(name, kind) }

// Required marks the parameter as one the caller must supply.
//
// This is what a form shows a human; it does not validate anything. Enforcement
// stays in the payload's Valid method — deliberately, because a job that is also
// scheduled has to tolerate empty parameters even while the form marks them
// required.
func (s *ParamSpec) Required() *ParamSpec { s.f.Required = true; return s }

// Desc sets the human explanation shown next to the field.
func (s *ParamSpec) Desc(description string) *ParamSpec {
	s.f.Description = description
	return s
}

// Default records the value used when the caller supplies none.
func (s *ParamSpec) Default(value string) *ParamSpec { s.f.Default = value; return s }

// Example records a sample value, for placeholder text and documentation.
func (s *ParamSpec) Example(value string) *ParamSpec { s.f.Example = value; return s }

// Enum restricts the parameter to a fixed set of values.
func (s *ParamSpec) Enum(values ...string) *ParamSpec {
	s.f.Enum = values
	s.f.Kind = ParamEnum
	return s
}

// Options restricts the parameter to a fixed set of values, each with the text
// to show for it — the labelled form of Enum.
//
//	core.StringParam("status").Options(
//	    core.Opt("draft", "ฉบับร่าง"),
//	    core.Opt("sent", "ส่งแล้ว"),
//	)
func (s *ParamSpec) Options(options ...ParamOption) *ParamSpec {
	s.f.Options = options
	s.f.Enum = make([]string, 0, len(options))
	for _, o := range options {
		s.f.Enum = append(s.f.Enum, o.Value)
	}
	s.f.Kind = ParamEnum
	return s
}

// Label sets the display name shown instead of the field name.
func (s *ParamSpec) Label(label string) *ParamSpec { s.f.Label = label; return s }

// Min bounds a number, a string's length, or an array's size.
func (s *ParamSpec) Min(v float64) *ParamSpec { s.f.Min = &v; return s }

// Max bounds a number, a string's length, or an array's size.
func (s *ParamSpec) Max(v float64) *ParamSpec { s.f.Max = &v; return s }

// Between is Min and Max together.
func (s *ParamSpec) Between(min, max float64) *ParamSpec { return s.Min(min).Max(max) }

// Pattern sets a regular expression the value should match.
func (s *ParamSpec) Pattern(expr string) *ParamSpec { s.f.Pattern = expr; return s }

// Multiline asks for a text area rather than a single-line input.
func (s *ParamSpec) Multiline() *ParamSpec { s.f.Multiline = true; return s }

// Meta attaches an arbitrary attribute, for anything this package does not
// model — a widget name, a form group, an ordering hint. Values are passed
// through to the API untouched.
//
//	core.StringParam("region").Meta("widget", "map").Meta("group", "targeting")
func (s *ParamSpec) Meta(key string, value any) *ParamSpec {
	if s.f.Meta == nil {
		s.f.Meta = map[string]any{}
	}
	s.f.Meta[key] = value
	return s
}

// Kind overrides the shape, for the rare case the constructors do not cover.
func (s *ParamSpec) Kind(kind ParamKind) *ParamSpec { s.f.Kind = kind; return s }

// With applies an arbitrary change to the field being built — the escape hatch
// for a house style, so a team can wrap its own conventions in one helper:
//
//	func tenantID() *core.ParamSpec {
//	    return core.StringParam("tenant_id").Required().
//	        With(func(f *core.ParamField) { f.Pattern = "^t_[a-z0-9]+$" })
//	}
func (s *ParamSpec) With(fn func(*ParamField)) *ParamSpec {
	if fn != nil {
		fn(&s.f)
	}
	return s
}

// Build returns the described field.
func (s *ParamSpec) Build() ParamField { return s.f }

// Params turns declared specs into the schema stored on JobDef.
func Params(specs ...*ParamSpec) []ParamField {
	if len(specs) == 0 {
		return nil
	}
	out := make([]ParamField, 0, len(specs))
	for _, s := range specs {
		if s != nil {
			out = append(out, s.Build())
		}
	}
	if len(out) == 0 {
		return nil // "no parameters", not "an empty list of them"
	}
	return out
}

// ---------------------------------------------------------------------------
// Deriving a schema from the parameter type
// ---------------------------------------------------------------------------

// IParamsDescriber is implemented by a parameter type that describes itself.
// It is the third way to publish a schema, for when the description belongs next
// to the struct rather than on the JobDef — and the only way to build one that
// depends on runtime state (values loaded from the database, say).
//
//	func (ReportParams) ParamSchema() []core.ParamField {
//	    return core.Params(
//	        core.DateParam("date").Required(),
//	        core.EnumParam("status", loadStatuses()...),
//	    )
//	}
//
// JobDef.Params still wins when both are given.
type IParamsDescriber interface {
	ParamSchema() []ParamField
}

// paramSchemaMaxDepth bounds recursion so a self-referencing struct cannot spin.
const paramSchemaMaxDepth = 5

// ParamSchemaOf derives a basic schema from T — the field names (from their json
// tags) and their kinds. It is the fallback for jobs that do not declare their
// parameters: it cannot know which fields are required, which are enums, or what
// any of them mean, so declare JobDef.Params when that matters.
//
// It returns nil when T has no fields to describe (no parameters, or a free-form
// map).
func ParamSchemaOf[T any]() []ParamField {
	return paramSchemaOfType(reflect.TypeFor[T](), 0)
}

func paramSchemaOfType(t reflect.Type, depth int) []ParamField {
	if t == nil || depth > paramSchemaMaxDepth {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t == reflect.TypeFor[time.Time]() {
		return nil
	}

	fields := make([]ParamField, 0, t.NumField())
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, ok := jsonFieldName(sf)
		if !ok {
			continue
		}
		// an embedded struct contributes its own fields, as JSON does
		if sf.Anonymous && sf.Tag.Get("json") == "" {
			fields = append(fields, paramSchemaOfType(sf.Type, depth+1)...)
			continue
		}
		fields = append(fields, describeField(name, sf.Type, depth))
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// describeField maps one struct field to a ParamField.
func describeField(name string, t reflect.Type, depth int) ParamField {
	f := ParamField{Name: name}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch {
	case t == reflect.TypeFor[time.Time]():
		f.Kind = ParamDateTime
	default:
		switch t.Kind() {
		case reflect.String:
			f.Kind = ParamString
		case reflect.Bool:
			f.Kind = ParamBool
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			f.Kind = ParamInt
		case reflect.Float32, reflect.Float64:
			f.Kind = ParamNumber
		case reflect.Slice, reflect.Array:
			f.Kind = ParamArray
			item := describeField("", t.Elem(), depth+1)
			f.Items = &item
		case reflect.Struct:
			f.Kind = ParamObject
			f.Fields = paramSchemaOfType(t, depth+1)
		default: // map, interface, chan…
			f.Kind = ParamAny
		}
	}
	return f
}

// jsonFieldName resolves the wire name of a struct field, reporting false when
// the field is not serialised at all.
func jsonFieldName(sf reflect.StructField) (string, bool) {
	tag := sf.Tag.Get("json")
	if tag == "-" {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = sf.Name
	}
	return name, true
}

// unknownParamNames reports declared parameters that do not exist on the actual
// parameter type. Declaring the schema separately from the struct is clearer to
// read, but it can drift — this is what catches a renamed or misspelt field at
// startup instead of leaving a form field that silently does nothing.
//
// It compares top-level names only, and says nothing when the type has no
// derivable fields (a map, for instance).
func unknownParamNames(declared, derived []ParamField) []string {
	if len(declared) == 0 || len(derived) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(derived))
	for _, f := range derived {
		known[f.Name] = struct{}{}
	}
	var unknown []string
	for _, f := range declared {
		if _, ok := known[f.Name]; !ok {
			unknown = append(unknown, f.Name)
		}
	}
	return unknown
}
