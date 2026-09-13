package llm_test

import (
	"testing"
	"time"

	"github.com/pskclub/mine-core/v2/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Invoice struct {
	Vendor string    `json:"vendor" jsonschema:"description=Company that issued it"`
	Total  float64   `json:"total"`
	Lines  int       `json:"lines"`
	Paid   bool      `json:"paid"`
	Status string    `json:"status" jsonschema:"enum=draft|sent|paid"`
	Due    time.Time `json:"due"`
	Note   string    `json:"note,omitempty"`
	Ref    *string   `json:"ref"`
	Skip   string    `json:"-"`
	hidden string    //nolint:unused // asserts unexported fields stay out of the schema
}

func TestSchemaOf_shape(t *testing.T) {
	s, err := llm.SchemaOf[Invoice]()
	require.NoError(t, err)

	assert.Equal(t, "invoice", s.Name, "the name is derived from the type, so a provider error can be traced back to it")
	assert.Equal(t, "object", s.Schema["type"])
	assert.Equal(t, false, s.Schema["additionalProperties"],
		"a closed object is what stops a model inventing a field that then gets dropped at unmarshal")

	props := s.Schema["properties"].(map[string]any)
	assert.Equal(t, "string", props["vendor"].(map[string]any)["type"])
	assert.Equal(t, "number", props["total"].(map[string]any)["type"])
	assert.Equal(t, "integer", props["lines"].(map[string]any)["type"])
	assert.Equal(t, "boolean", props["paid"].(map[string]any)["type"])

	assert.NotContains(t, props, "Skip", `json:"-" means the caller does not want it filled in`)
	assert.NotContains(t, props, "hidden", "a model cannot fill an unexported field")
}

func TestSchemaOf_descriptionAndEnumComeFromTags(t *testing.T) {
	s, err := llm.SchemaOf[Invoice]()
	require.NoError(t, err)
	props := s.Schema["properties"].(map[string]any)

	assert.Equal(t, "Company that issued it", props["vendor"].(map[string]any)["description"],
		"a description is the cheapest way to improve extraction accuracy")
	assert.Equal(t, []string{"draft", "sent", "paid"}, props["status"].(map[string]any)["enum"],
		"an enum constrains the model rather than hoping the prompt did")
}

func TestSchemaOf_timeIsAFormattedString(t *testing.T) {
	// time.Time is a struct with unexported fields — describing it structurally
	// would ask the model to fill in wall clock and monotonic readings.
	s, err := llm.SchemaOf[Invoice]()
	require.NoError(t, err)
	due := s.Schema["properties"].(map[string]any)["due"].(map[string]any)
	assert.Equal(t, "string", due["type"])
	assert.Equal(t, "date-time", due["format"])
}

func TestSchemaOf_requiredRules(t *testing.T) {
	s, err := llm.SchemaOf[Invoice]()
	require.NoError(t, err)
	required := s.Schema["required"].([]string)

	assert.Contains(t, required, "vendor")
	assert.Contains(t, required, "status")
	assert.NotContains(t, required, "note", "omitempty means the caller can live without it")
	assert.NotContains(t, required, "ref", "a pointer field is how Go says the value may be absent")
}

type Address struct {
	City string `json:"city"`
	Zip  string `json:"zip"`
}

type Customer struct {
	Name    string            `json:"name"`
	Address Address           `json:"address"`
	Tags    []string          `json:"tags"`
	Extra   map[string]string `json:"extra"`
	Orders  []Address         `json:"orders"`
}

func TestSchemaOf_nestedTypes(t *testing.T) {
	s, err := llm.SchemaOf[Customer]()
	require.NoError(t, err)
	props := s.Schema["properties"].(map[string]any)

	addr := props["address"].(map[string]any)
	assert.Equal(t, "object", addr["type"])
	assert.Contains(t, addr["properties"].(map[string]any), "city")

	tags := props["tags"].(map[string]any)
	assert.Equal(t, "array", tags["type"])
	assert.Equal(t, "string", tags["items"].(map[string]any)["type"])

	extra := props["extra"].(map[string]any)
	assert.Equal(t, "object", extra["type"])
	assert.Equal(t, "string", extra["additionalProperties"].(map[string]any)["type"],
		"a map becomes an open object whose values share one shape")

	orders := props["orders"].(map[string]any)
	assert.Equal(t, "object", orders["items"].(map[string]any)["type"], "array of structs")
}

type Base struct {
	ID string `json:"id"`
}

type WithEmbedded struct {
	Base
	Name string `json:"name"`
}

func TestSchemaOf_embeddedStructIsFlattened(t *testing.T) {
	// encoding/json flattens an untagged embedded struct, so the schema has to
	// as well — otherwise the model answers with a nesting level that will not
	// unmarshal into the type that asked for it.
	s, err := llm.SchemaOf[WithEmbedded]()
	require.NoError(t, err)

	props := s.Schema["properties"].(map[string]any)
	assert.Contains(t, props, "id")
	assert.Contains(t, props, "name")
	assert.NotContains(t, props, "Base")
	assert.ElementsMatch(t, []string{"id", "name"}, s.Schema["required"].([]string))
}

type Node struct {
	Name     string  `json:"name"`
	Children []Node  `json:"children"`
	Parent   *Node   `json:"parent"`
	Weight   float64 `json:"weight"`
}

func TestSchemaOf_recursiveTypeIsRejected(t *testing.T) {
	// No provider supports a recursive schema. Failing here names the type;
	// recursing would blow the stack, and emitting $ref would be rejected by
	// the provider one layer further away.
	_, err := llm.SchemaOf[Node]()
	require.Error(t, err)
	assert.Equal(t, "LLM_INVALID_SCHEMA", err.GetCode())
	assert.Contains(t, err.GetMessage(), "refers to itself")
}

func TestSchemaOf_nonObjectRootIsRejected(t *testing.T) {
	_, err := llm.SchemaOf[string]()
	require.Error(t, err, "a model answers with an object; a bare string root would be rejected by the provider")
	assert.Equal(t, "LLM_INVALID_SCHEMA", err.GetCode())

	_, err = llm.SchemaOf[[]Invoice]()
	require.Error(t, err, "wrap a list in a struct with a named field instead")
}

type Unsupported struct {
	Fn func() `json:"fn"`
}

func TestSchemaOf_unsupportedFieldNamesThePath(t *testing.T) {
	_, err := llm.SchemaOf[Unsupported]()
	require.Error(t, err)
	assert.Contains(t, err.GetMessage(), "fn", "the error must say which field, not just which type")
}
