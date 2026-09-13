package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type schemaParams struct {
	Date     *string    `json:"date"`
	Status   *string    `json:"status"`
	Limit    *int64     `json:"limit"`
	Ratio    float64    `json:"ratio"`
	Force    bool       `json:"force"`
	Emails   []string   `json:"emails"`
	RunAt    *time.Time `json:"run_at"`
	Nested   address    `json:"address"`
	Ignored  string     `json:"-"`
	Untagged string
	private  string //nolint:unused // deliberately unexported
}

type address struct {
	Street *string `json:"street"`
	Zip    *string `json:"zip"`
}

// fieldByName finds a field in a schema.
func fieldByName(t *testing.T, fields []ParamField, name string) ParamField {
	t.Helper()
	for _, f := range fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("field %q is missing from the schema", name)
	return ParamField{}
}

// ---------------------------------------------------------------------------
// Declared schemas
// ---------------------------------------------------------------------------

func TestParams_declaresEverythingAFormNeeds(t *testing.T) {
	schema := Params(
		DateParam("date").Required().Desc("Day to report on").Example("2026-07-01"),
		EnumParam("status", "draft", "sent", "paid").Default("sent"),
		IntParam("limit").Desc("Maximum rows"),
		NumberParam("ratio"),
		BoolParam("force"),
		ArrayParam("emails", StringParam("")),
		ObjectParam("address", StringParam("street").Required(), StringParam("zip")),
		TimeParam("at"),
		DateTimeParam("run_at"),
		AnyParam("extra"),
	)
	require.Len(t, schema, 10)

	date := fieldByName(t, schema, "date")
	assert.Equal(t, ParamDate, date.Kind)
	assert.True(t, date.Required)
	assert.Equal(t, "Day to report on", date.Description)
	assert.Equal(t, "2026-07-01", date.Example)

	status := fieldByName(t, schema, "status")
	assert.Equal(t, ParamEnum, status.Kind)
	assert.Equal(t, []string{"draft", "sent", "paid"}, status.Enum)
	assert.Equal(t, "sent", status.Default)

	assert.Equal(t, ParamInt, fieldByName(t, schema, "limit").Kind)
	assert.Equal(t, ParamNumber, fieldByName(t, schema, "ratio").Kind)
	assert.Equal(t, ParamBool, fieldByName(t, schema, "force").Kind)
	assert.Equal(t, ParamTime, fieldByName(t, schema, "at").Kind)
	assert.Equal(t, ParamDateTime, fieldByName(t, schema, "run_at").Kind)
	assert.Equal(t, ParamAny, fieldByName(t, schema, "extra").Kind)
	assert.False(t, fieldByName(t, schema, "limit").Required,
		"parameters are optional unless declared required")

	arr := fieldByName(t, schema, "emails")
	assert.Equal(t, ParamArray, arr.Kind)
	require.NotNil(t, arr.Items)
	assert.Equal(t, ParamString, arr.Items.Kind)
	assert.Empty(t, arr.Items.Name, "an element has a kind, not a name")

	obj := fieldByName(t, schema, "address")
	assert.Equal(t, ParamObject, obj.Kind)
	require.Len(t, obj.Fields, 2)
	assert.True(t, fieldByName(t, obj.Fields, "street").Required)
}

func TestParams_emptyAndOverrides(t *testing.T) {
	assert.Nil(t, Params(), "no parameters means no schema at all")
	assert.Nil(t, Params(nil))

	// a plain string field promoted to an enum after the fact
	schema := Params(StringParam("status").Enum("on", "off"))
	assert.Equal(t, ParamEnum, schema[0].Kind)
	assert.Equal(t, []string{"on", "off"}, schema[0].Enum)

	// and the escape hatch for a shape the constructors do not cover
	custom := Params(StringParam("blob").Kind("base64"))
	assert.Equal(t, ParamKind("base64"), custom[0].Kind)
}

func TestParams_customisation(t *testing.T) {
	schema := Params(
		// a kind this package does not define, plus free-form attributes
		CustomParam("payout", "currency").
			Label("Amount").
			Meta("currency", "THB").
			Meta("widget", "money-input").
			Between(1, 1_000_000),

		// constraints a form can enforce before anything is submitted
		StringParam("note").Multiline().Max(500).Desc("Why you are running this"),
		StringParam("tenant").Pattern("^t_[a-z0-9]+$").Required(),
		IntParam("workers").Between(1, 8),

		// an enum whose display text differs from the value sent
		StringParam("status").Options(
			Opt("draft", "ฉบับร่าง"),
			Opt("sent", "ส่งแล้ว"),
			Opt("paid"),
		),
	)

	payout := fieldByName(t, schema, "payout")
	assert.Equal(t, ParamKind("currency"), payout.Kind, "any kind is allowed")
	assert.Equal(t, "Amount", payout.Label)
	assert.Equal(t, "THB", payout.Meta["currency"])
	assert.Equal(t, "money-input", payout.Meta["widget"], "Meta accumulates, it does not replace")
	require.NotNil(t, payout.Min)
	assert.EqualValues(t, 1, *payout.Min)
	assert.EqualValues(t, 1_000_000, *payout.Max)

	note := fieldByName(t, schema, "note")
	assert.True(t, note.Multiline)
	assert.EqualValues(t, 500, *note.Max)
	assert.Nil(t, note.Min, "an unset bound stays absent, not zero")

	assert.Equal(t, "^t_[a-z0-9]+$", fieldByName(t, schema, "tenant").Pattern)

	status := fieldByName(t, schema, "status")
	assert.Equal(t, ParamEnum, status.Kind)
	require.Len(t, status.Options, 3)
	assert.Equal(t, "ฉบับร่าง", status.Options[0].Label)
	assert.Equal(t, "paid", status.Options[2].Label, "no label falls back to the value")
	assert.Equal(t, []string{"draft", "sent", "paid"}, status.Enum,
		"Enum stays populated so a plain consumer still works")
}

// With lets a team wrap its own conventions in a helper.
func TestParams_withEscapeHatch(t *testing.T) {
	tenantID := func() *ParamSpec {
		return StringParam("tenant").Required().
			With(func(f *ParamField) {
				f.Pattern = "^t_[a-z0-9]+$"
				f.Description = "Tenant to run for"
			})
	}
	schema := Params(tenantID())
	assert.Equal(t, "^t_[a-z0-9]+$", schema[0].Pattern)
	assert.Equal(t, "Tenant to run for", schema[0].Description)
	assert.True(t, schema[0].Required)

	assert.NotPanics(t, func() { _ = StringParam("x").With(nil).Build() })
}

// ---------------------------------------------------------------------------
// Schemas derived from the type (the fallback)
// ---------------------------------------------------------------------------

func TestParamSchemaOf_derivesNamesAndKinds(t *testing.T) {
	schema := ParamSchemaOf[schemaParams]()
	require.NotEmpty(t, schema)

	assert.Equal(t, ParamString, fieldByName(t, schema, "date").Kind,
		"a date is just a string to Go — only a declared schema knows better")
	assert.Equal(t, ParamInt, fieldByName(t, schema, "limit").Kind)
	assert.Equal(t, ParamNumber, fieldByName(t, schema, "ratio").Kind)
	assert.Equal(t, ParamBool, fieldByName(t, schema, "force").Kind)
	assert.Equal(t, ParamDateTime, fieldByName(t, schema, "run_at").Kind,
		"time.Time is a datetime, not an object")
	assert.Equal(t, "Untagged", fieldByName(t, schema, "Untagged").Name,
		"a field with no json tag keeps its Go name, as encoding/json does")

	arr := fieldByName(t, schema, "emails")
	assert.Equal(t, ParamArray, arr.Kind)
	require.NotNil(t, arr.Items)
	assert.Equal(t, ParamString, arr.Items.Kind)

	obj := fieldByName(t, schema, "address")
	assert.Equal(t, ParamObject, obj.Kind)
	assert.Len(t, obj.Fields, 2)

	for _, f := range schema {
		assert.False(t, f.Required, "reflection cannot know what is required")
		assert.NotEqual(t, "Ignored", f.Name, `json:"-" fields are not parameters`)
		assert.NotEqual(t, "private", f.Name, "unexported fields are not parameters")
	}
}

func TestParamSchemaOf_pointerAndEmptyTypes(t *testing.T) {
	assert.NotEmpty(t, ParamSchemaOf[*schemaParams](), "a pointer parameter type is unwrapped")
	assert.Nil(t, ParamSchemaOf[struct{}]())
	assert.Nil(t, ParamSchemaOf[map[string]any](), "a free-form map has no schema to show")
	assert.Nil(t, ParamSchemaOf[string]())
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// The registry is where this becomes useful: listing the jobs tells an operator
// what each one takes, without reading the code.
func TestRegistry_infoCarriesTheDeclaredSchema(t *testing.T) {
	reg := NewJobRegistry()
	require.NoError(t, RegisterJob(reg, JobDef{
		Name:          "report",
		Description:   "build the report",
		Schedule:      Cron("0 2 * * *"),
		Queue:         "heavy",
		MaxAttempts:   3,
		MaxConcurrent: 1,
		Concurrency:   ConcurrencySkip,
		Replayable:    BoolPtr(false),
		Params: Params(
			DateParam("date").Required().Desc("Day to report on"),
			EnumParam("status", "draft", "sent").Default("sent"),
		),
	}, func(c ICronjobContext, p schemaParams) error { return nil }))
	require.NoError(t, reg.Register(JobDef{Name: "heartbeat"},
		func(c ICronjobContext) error { return nil }))

	info := reg.Info()
	require.Len(t, info, 2)
	assert.Equal(t, "heartbeat", info[0].Name, "ordered by name")

	report := info[1]
	assert.Equal(t, "cron(0 2 * * *)", report.Schedule)
	assert.Equal(t, "heavy", report.Queue)
	assert.Equal(t, 3, report.MaxAttempts)
	assert.Equal(t, "skip", report.Concurrency)
	assert.False(t, report.Replayable)
	assert.False(t, report.Paused)
	require.Len(t, report.Params, 2, "the declared schema is published as written")
	assert.True(t, fieldByName(t, report.Params, "date").Required)
	assert.Equal(t, ParamDate, fieldByName(t, report.Params, "date").Kind)

	assert.Empty(t, info[0].Params, "a job without parameters advertises none")
	assert.Empty(t, info[0].Schedule, "a manual-only job has no schedule to report")

	require.NoError(t, reg.Pause("report"))
	assert.True(t, reg.Info()[1].Paused)
	assert.Equal(t, report.Params, reg.Params("report"))
	assert.Nil(t, reg.Params("nope"))
}

func TestRegistry_undeclaredParamsFallBackToTheType(t *testing.T) {
	reg := NewJobRegistry()
	require.NoError(t, RegisterJob(reg, JobDef{Name: "inferred"},
		func(c ICronjobContext, p schemaParams) error { return nil }))

	schema := reg.Params("inferred")
	require.NotEmpty(t, schema)
	assert.Equal(t, ParamString, fieldByName(t, schema, "date").Kind)
}

// Declaring the schema apart from the struct reads better, but it can drift.
// Registration is where that gets caught — at startup, not in a form that
// silently sends a field the job cannot receive.
func TestRegistry_rejectsASchemaThatDoesNotMatchTheType(t *testing.T) {
	reg := NewJobRegistry()
	err := RegisterJob(reg, JobDef{
		Name: "typo",
		Params: Params(
			DateParam("date"),
			StringParam("statuss"), // misspelt
			StringParam("nope"),    // does not exist at all
		),
	}, func(c ICronjobContext, p schemaParams) error { return nil })

	require.Error(t, err)
	assert.Equal(t, "INVALID_JOB", err.GetCode())
	assert.Contains(t, err.Error(), "statuss")
	assert.Contains(t, err.Error(), "nope")
	assert.NotContains(t, err.Error(), "date")
	assert.Empty(t, reg.List(), "a job with a broken schema is not registered")
}

// A parameter type may describe itself — the place to put a schema that depends
// on runtime state.
type describedParams struct {
	Date   *string `json:"date"`
	Status *string `json:"status"`
}

func (describedParams) ParamSchema() []ParamField {
	return Params(
		DateParam("date").Required().Desc("from the type itself"),
		EnumParam("status", "a", "b"),
	)
}

func TestRegistry_parameterTypeCanDescribeItself(t *testing.T) {
	reg := NewJobRegistry()
	require.NoError(t, RegisterJob(reg, JobDef{Name: "self"},
		func(c ICronjobContext, p describedParams) error { return nil }))

	schema := reg.Params("self")
	require.Len(t, schema, 2)
	date := fieldByName(t, schema, "date")
	assert.True(t, date.Required)
	assert.Equal(t, ParamDate, date.Kind, "not the plain string reflection would report")
	assert.Equal(t, "from the type itself", date.Description)
}

func TestRegistry_jobDefWinsOverTheTypesOwnSchema(t *testing.T) {
	reg := NewJobRegistry()
	require.NoError(t, RegisterJob(reg, JobDef{
		Name:   "override",
		Params: Params(StringParam("date").Desc("declared on the job")),
	}, func(c ICronjobContext, p describedParams) error { return nil }))

	schema := reg.Params("override")
	require.Len(t, schema, 1)
	assert.Equal(t, "declared on the job", schema[0].Description)
}

// Whichever way the schema arrives, a name the struct does not have is caught.
func TestRegistry_rejectsADescriberThatDoesNotMatchTheType(t *testing.T) {
	reg := NewJobRegistry()
	err := RegisterJob(reg, JobDef{Name: "self-typo"},
		func(c ICronjobContext, p describedTypoParams) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gone")
}

type describedTypoParams struct {
	Date *string `json:"date"`
}

func (describedTypoParams) ParamSchema() []ParamField {
	return Params(DateParam("date"), StringParam("gone"))
}

// A free-form parameter type has nothing to check against, so a declared schema
// is taken at its word.
func TestRegistry_freeFormParamsAcceptAnyDeclaredSchema(t *testing.T) {
	reg := NewJobRegistry()
	require.NoError(t, RegisterJob(reg, JobDef{
		Name:   "adhoc",
		Params: Params(StringParam("query").Required().Desc("SQL to run")),
	}, func(c ICronjobContext, p map[string]any) error { return nil }))

	assert.Len(t, reg.Params("adhoc"), 1)
}
