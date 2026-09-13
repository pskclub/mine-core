package valid_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/valid"
)

func ptr[T any](v T) *T { return &v }

func newCtx(t *testing.T, seed ...string) core.IContext {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT)`)
	for _, e := range seed {
		db.Exec(`INSERT INTO users (email) VALUES (?)`, e)
	}
	t.Setenv("APP_ENV", "test")
	t.Setenv("APP_SERVICE", "test")
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	app, err := core.NewApp(env, core.WithSQL("default", db))
	require.NoError(t, err)
	return app.NewContext(context.Background())
}

// fieldCodes decodes the {fields:{name:{code}}} map from a validation error.
func fieldCodes(t *testing.T, err core.IError) map[string]string {
	t.Helper()
	require.NotNil(t, err)
	b, merr := json.Marshal(err.JSON())
	require.NoError(t, merr)
	var got struct {
		Fields map[string]struct {
			Code string `json:"code"`
		} `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(b, &got))
	out := map[string]string{}
	for k, v := range got.Fields {
		out[k] = v.Code
	}
	return out
}

func TestValid_stringRules(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Str("email", ptr("not-an-email")).Required().Email()
	v.Str("name", ptr("")).Required()
	v.Str("role", ptr("root")).In("admin", "user")

	err := v.Error()
	require.NotNil(t, err)
	assert.Equal(t, 400, err.GetStatus())
	assert.Equal(t, "INVALID_PARAMS", err.GetCode())
}

func TestValid_passesWhenValid(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Str("email", ptr("a@b.com")).Required().Email()
	v.Int("age", ptr(int64(30))).Required().Between(18, 150)
	v.Str("optional", nil).Email() // nil skipped
	assert.Nil(t, v.Error())
}

func TestValid_stopOnFirstPerField(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	// empty string fails Required; Email/Length must not pile on for the same field
	v.Str("email", ptr("")).Required().Email().Length(5, 10)

	codes := fieldCodes(t, v.Error())
	assert.Len(t, codes, 1)
	assert.Equal(t, "REQUIRED", codes["email"])
}

func TestValid_notBlank(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Str("absent", nil).NotBlank()           // never sent: an optional field stays optional
	v.Str("ok", ptr("2026-07-15")).NotBlank() // sent with a value
	v.Str("empty", ptr("")).NotBlank()
	v.Str("spaces", ptr("   ")).NotBlank()

	codes := fieldCodes(t, v.Error())
	assert.Len(t, codes, 2, "only the fields that arrived blank are reported")
	assert.Equal(t, "BLANK", codes["empty"])
	assert.Equal(t, "BLANK", codes["spaces"])
}

func TestValid_notBlank_stopsBeforeTheFormatRule(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	// The point of the pairing: "" is reported as blank, and a real date still
	// gets format-checked.
	v.Str("change_at", ptr("")).NotBlank().Date()
	v.Str("other_at", ptr("31-12-2025")).NotBlank().Date()

	codes := fieldCodes(t, v.Error())
	assert.Equal(t, "BLANK", codes["change_at"])
	assert.Equal(t, "INVALID_DATE", codes["other_at"])
}

func TestValid_notBlank_requiredWinsWhenBothAreChained(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Str("email", ptr("")).Required().NotBlank()

	codes := fieldCodes(t, v.Error())
	assert.Len(t, codes, 1, "stop-on-first: NotBlank must not pile on after Required")
	assert.Equal(t, "REQUIRED", codes["email"])
}

func TestValid_notBlank_catchesWhitespaceThatRequiredAccepts(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Str("name", ptr("   ")).Required()             // whitespace is a value to Required
	v.Str("title", ptr("   ")).Required().NotBlank() // …NotBlank is what rejects it

	codes := fieldCodes(t, v.Error())
	assert.NotContains(t, codes, "name", "Required only reads \"\" as absent")
	assert.Equal(t, "BLANK", codes["title"])
}

// The point of a normalizer: the payload the handler goes on to use is cleaned
// too, not just the copy the rules looked at.
func TestValid_trimAndLower_writeBackToThePayload(t *testing.T) {
	ctx := newCtx(t)
	payload := struct {
		Email     *string
		MeetingAt *string
	}{
		Email:     ptr("  Admin@Example.COM  "),
		MeetingAt: ptr(" 2026-07-15 10:00:00 "),
	}

	v := valid.New(ctx)
	v.Str("email", payload.Email).Trim().Lower().Required().Email()
	v.Str("meeting_at", payload.MeetingAt).Trim().Required().DateTime()

	assert.Nil(t, v.Error(), "padding is accepted once the field says to normalize it")
	assert.Equal(t, "admin@example.com", *payload.Email)
	assert.Equal(t, "2026-07-15 10:00:00", *payload.MeetingAt)
}

func TestValid_apply_runsAHouseNormalizer(t *testing.T) {
	ctx := newCtx(t)
	// A service's own rule: a local phone number is stored in +66 form.
	toE164 := func(s string) string { return "+66" + strings.TrimPrefix(s, "0") }
	phone := ptr(" 0812345678 ")

	v := valid.New(ctx)
	v.Str("phone", phone).Trim().Apply(toE164).Required().Prefix("+66")

	assert.Nil(t, v.Error(), "later rules see the normalized value")
	assert.Equal(t, "+66812345678", *phone)
}

func TestValid_apply_nilFuncAndAbsentFieldAreNoops(t *testing.T) {
	ctx := newCtx(t)
	kept := ptr("as-is")
	v := valid.New(ctx)
	v.Str("absent", nil).Apply(strings.ToUpper)
	v.Str("kept", kept).Apply(nil)

	assert.Nil(t, v.Error())
	assert.Equal(t, "as-is", *kept)
}

func TestValid_trim_collapsesWhitespaceToMissing(t *testing.T) {
	ctx := newCtx(t)
	spaces := ptr("   ")
	v := valid.New(ctx)
	v.Str("name", spaces).Trim().Required()

	assert.Equal(t, "REQUIRED", fieldCodes(t, v.Error())["name"])
	assert.Equal(t, "", *spaces)
}

func TestValid_normalizersOnAbsentFieldAreNoops(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Str("absent", nil).Trim().Lower().Date() // must not panic on a nil value
	assert.Nil(t, v.Error())
}

func TestValid_normalizersDoNotBindMessage(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	// A normalizer never fails, so a Message() after one has nothing to override
	// and must not steal the next rule's message.
	v.Str("name", ptr("  ")).Trim().Message("ignored").Required()

	assert.Equal(t, "The name field is required", fieldMessages(t, v.Error())["name"])
}

func TestValid_notBlank_messageOverride(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Str("change_at", ptr("  ")).NotBlank().Message("change_at must not be empty").Date()

	err := v.Error()
	require.NotNil(t, err)
	b, merr := json.Marshal(err.JSON())
	require.NoError(t, merr)
	assert.Contains(t, string(b), "change_at must not be empty")
}

func TestValid_numberBetweenCode(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Int("age", ptr(int64(5))).Between(18, 150)
	assert.Equal(t, "INVALID_NUMBER_BETWEEN", fieldCodes(t, v.Error())["age"])
}

func TestValid_arrayRules(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Arr("tags", []string{}).Required()
	v.Arr("items", []int{1, 2, 3}).Max(2)
	assert.NotNil(t, v.Error())
}

func TestValid_uniqueAndExists(t *testing.T) {
	ctx := newCtx(t, "taken@b.com")

	v := valid.New(ctx)
	v.Str("email", ptr("taken@b.com")).Unique("users", "email")
	assert.NotNil(t, v.Error(), "Unique should fail for an existing value")

	v2 := valid.New(ctx)
	v2.Str("email", ptr("free@b.com")).Unique("users", "email")
	assert.Nil(t, v2.Error(), "Unique should pass for a free value")

	v3 := valid.New(ctx)
	v3.Str("email", ptr("missing@b.com")).Exists("users", "email")
	assert.NotNil(t, v3.Error(), "Exists should fail for an absent value")
}

func TestValid_dbErrorSurfacesAs500(t *testing.T) {
	ctx := newCtx(t) // table has no such column → query errors
	v := valid.New(ctx)
	v.Str("email", ptr("x@b.com")).Unique("users", "nonexistent_column")

	err := v.Error()
	require.NotNil(t, err)
	assert.Equal(t, 500, err.GetStatus(), "DB failure during validation must be 500")
}

func TestValid_eachNested(t *testing.T) {
	ctx := newCtx(t)
	names := []*string{ptr("ok"), ptr("")}
	v := valid.New(ctx)
	v.Each("items", len(names), func(iv *valid.Validator, i int) {
		iv.Str("name", names[i]).Required()
	})
	assert.Contains(t, fieldCodes(t, v.Error()), "items.1.name", "nested field should be prefixed")
}

// --- conditional / cross-field ---

func TestValid_when_conditionalRequired(t *testing.T) {
	ctx := newCtx(t)

	// notify=true → notify_email required
	v := valid.New(ctx)
	notify := true
	v.When(notify, func(v *valid.Validator) {
		v.Str("notify_email", nil).Required().Email()
	})
	assert.Equal(t, "REQUIRED", fieldCodes(t, v.Error())["notify_email"])

	// notify=false → block skipped, no error
	v2 := valid.New(ctx)
	v2.When(false, func(v *valid.Validator) {
		v2.Str("notify_email", nil).Required()
	})
	assert.Nil(t, v2.Error())
}

func TestValid_must_crossField(t *testing.T) {
	ctx := newCtx(t)
	start, end := 10, 5 // end before start → invalid

	v := valid.New(ctx)
	v.Must("end", "INVALID_DATE_RANGE", end > start, map[string]any{"other": "start"})
	assert.Equal(t, "INVALID_DATE_RANGE", fieldCodes(t, v.Error())["end"])

	// valid range → no error
	v2 := valid.New(ctx)
	v2.Must("end", "INVALID_DATE_RANGE", 20 > start)
	assert.Nil(t, v2.Error())
}

// --- reusable nested validators ---

type addressReq struct {
	Street *string
	Zip    *string
}

func (r *addressReq) Validate(v *valid.Validator) {
	v.Str("street", r.Street).Required()
	v.Str("zip", r.Zip).Required().Length(5, 5)
}

// addressReq is also usable standalone.
func (r *addressReq) Valid(ctx core.IContext) core.IError { return valid.Run(ctx, r) }

func TestValid_nested_reuseAcrossFields(t *testing.T) {
	ctx := newCtx(t)
	billing := &addressReq{Street: ptr("Main St"), Zip: nil} // zip missing
	shipping := &addressReq{Street: nil, Zip: ptr("bad")}    // street missing, zip too short

	v := valid.New(ctx)
	v.Nested("billing_address", billing)
	v.Nested("shipping_address", shipping)

	codes := fieldCodes(t, v.Error())
	assert.Equal(t, "REQUIRED", codes["billing_address.zip"])
	assert.Equal(t, "REQUIRED", codes["shipping_address.street"])
	assert.Equal(t, "INVALID_STRING_LENGTH", codes["shipping_address.zip"])
}

func TestValid_run_standalone(t *testing.T) {
	ctx := newCtx(t)
	err := valid.Run(ctx, &addressReq{Street: ptr("Main St"), Zip: ptr("12345")})
	assert.Nil(t, err, "a fully valid address should pass standalone")

	err2 := (&addressReq{}).Valid(ctx) // via the Valid(ctx) wrapper
	require.NotNil(t, err2)
	assert.Contains(t, fieldCodes(t, err2), "street")
}

func TestValid_eachNested_indexedPrefix(t *testing.T) {
	ctx := newCtx(t)
	items := []*addressReq{
		{Street: ptr("A"), Zip: ptr("12345")}, // ok
		{Street: nil, Zip: ptr("99999")},      // street missing
	}
	v := valid.New(ctx)
	valid.EachNested(v, "addresses", items)

	codes := fieldCodes(t, v.Error())
	assert.Equal(t, "REQUIRED", codes["addresses.1.street"])
	assert.NotContains(t, codes, "addresses.0.street", "valid element should have no violation")
}

// --- custom messages ---

// fieldMessages decodes {fields:{name:{message}}} from a validation error.
func fieldMessages(t *testing.T, err core.IError) map[string]string {
	t.Helper()
	require.NotNil(t, err)
	b, merr := json.Marshal(err.JSON())
	require.NoError(t, merr)
	var got struct {
		Fields map[string]struct {
			Message string `json:"message"`
		} `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(b, &got))
	out := map[string]string{}
	for k, v := range got.Fields {
		out[k] = v.Message
	}
	return out
}

func TestValid_defaultCatalogMessage(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Str("email", ptr("")).Required()
	// message comes from the catalog template with {field} filled in
	assert.Equal(t, "The email field is required", fieldMessages(t, v.Error())["email"])
}

func TestValid_inlineMessageOverride(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Str("email", ptr("")).Required().Message("กรุณากรอกอีเมล")
	v.Int("age", ptr(int64(5))).Between(18, 150).Message("อายุต้องอยู่ระหว่าง 18-150")

	msgs := fieldMessages(t, v.Error())
	assert.Equal(t, "กรุณากรอกอีเมล", msgs["email"])
	assert.Equal(t, "อายุต้องอยู่ระหว่าง 18-150", msgs["age"])
}

func TestValid_messageNoopWhenValid(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	// field passes → Message() must not create a violation
	v.Str("email", ptr("a@b.com")).Required().Email().Message("should not appear")
	assert.Nil(t, v.Error())
}

func TestValid_globalSetMessage(t *testing.T) {
	ctx := newCtx(t)
	valid.SetMessage("REQUIRED", "{field} is mandatory")
	defer valid.SetMessage("REQUIRED", "The {field} field is required") // restore

	v := valid.New(ctx)
	v.Str("name", ptr("")).Required()
	assert.Equal(t, "name is mandatory", fieldMessages(t, v.Error())["name"])
}

// --- same field, rules split across lines ---

func TestValid_sameFieldStoredBuilder(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)

	// keep the builder in a variable → rules on separate lines share one builder,
	// so stop-on-first still yields a single violation for the field
	email := v.Str("email", ptr(""))
	email.Required()
	email.Email()
	email.Min(3)

	codes := fieldCodes(t, v.Error())
	assert.Len(t, codes, 1)
	assert.Equal(t, "REQUIRED", codes["email"])
}

func TestValid_sameFieldConditionalRule(t *testing.T) {
	ctx := newCtx(t, "taken@b.com")
	v := valid.New(ctx)

	email := v.Str("email", ptr("taken@b.com"))
	email.Required().Email()
	if true { // e.g. only enforce uniqueness on create
		email.Unique("users", "email")
	}
	assert.Equal(t, "UNIQUE", fieldCodes(t, v.Error())["email"])
}

// --- custom message PER RULE ---

func TestValid_perRuleMessage(t *testing.T) {
	ctx := newCtx(t)

	build := func(pw string) core.IError {
		v := valid.New(ctx)
		v.Str("password", ptr(pw)).
			Required().Message("Password is required").
			Min(8).Message("Password must be at least 8 characters")
		return v.Error()
	}

	// empty → the Required message (not the Min one)
	assert.Equal(t, "Password is required", fieldMessages(t, build(""))["password"])
	// too short → the Min message (not the Required one)
	assert.Equal(t, "Password must be at least 8 characters", fieldMessages(t, build("abc"))["password"])
	// valid → no error
	assert.Nil(t, build("longenough"))
}

func TestValid_perRuleMessage_number(t *testing.T) {
	ctx := newCtx(t)
	build := func(age *int64) core.IError {
		v := valid.New(ctx)
		v.Int("age", age).
			Required().Message("age is required").
			Between(18, 150).Message("age must be 18-150")
		return v.Error()
	}
	assert.Equal(t, "age is required", fieldMessages(t, build(nil))["age"])
	assert.Equal(t, "age must be 18-150", fieldMessages(t, build(ptr(int64(5))))["age"])
}

func TestValid_perRuleMessage_onlyBoundRuleApplies(t *testing.T) {
	ctx := newCtx(t)
	// Message after a rule that did NOT fail must not override the actual failure
	v := valid.New(ctx)
	v.Str("email", ptr("")). // empty → Required fails
					Required().                         // no custom message here
					Email().Message("bad email format") // Email is skipped (empty) → its Message must NOT apply
	assert.Equal(t, "The email field is required", fieldMessages(t, v.Error())["email"])
}

// --- new string/time validators ---

func TestValid_stringFormats(t *testing.T) {
	ctx := newCtx(t)
	cases := []struct {
		name  string
		rule  func(*valid.Validator)
		field string
		fail  bool
	}{
		{"numeric ok", func(v *valid.Validator) { v.Str("f", ptr("12.5")).Numeric() }, "f", false},
		{"numeric bad", func(v *valid.Validator) { v.Str("f", ptr("abc")).Numeric() }, "f", true},
		{"uuid ok", func(v *valid.Validator) { v.Str("f", ptr("123e4567-e89b-12d3-a456-426614174000")).UUID() }, "f", false},
		{"uuid bad", func(v *valid.Validator) { v.Str("f", ptr("nope")).UUID() }, "f", true},
		{"ip ok", func(v *valid.Validator) { v.Str("f", ptr("192.168.1.1")).IP() }, "f", false},
		{"ip bad", func(v *valid.Validator) { v.Str("f", ptr("999.1.1.1")).IP() }, "f", true},
		{"json ok", func(v *valid.Validator) { v.Str("f", ptr(`{"a":1}`)).JSON() }, "f", false},
		{"json bad", func(v *valid.Validator) { v.Str("f", ptr(`{bad`)).JSON() }, "f", true},
		{"date ok", func(v *valid.Validator) { v.Str("f", ptr("2026-07-15")).Date() }, "f", false},
		{"date bad", func(v *valid.Validator) { v.Str("f", ptr("15/07/2026")).Date() }, "f", true},
		{"date multi-layout", func(v *valid.Validator) { v.Str("f", ptr("15/07/2026")).Date("2006-01-02", "02/01/2006") }, "f", false},
		{"iso ok", func(v *valid.Validator) { v.Str("f", ptr("2026-07-15T10:00:00Z")).ISO8601() }, "f", false},
		{"time ok", func(v *valid.Validator) { v.Str("f", ptr("23:59:59")).Time() }, "f", false},
		{"time bad", func(v *valid.Validator) { v.Str("f", ptr("11:59 PM")).Time() }, "f", true},
		{"any time 12h", func(v *valid.Validator) { v.Str("f", ptr("11:59 PM")).AnyTime() }, "f", false},
		{"any time hh:mm", func(v *valid.Validator) { v.Str("f", ptr("23:59")).AnyTime() }, "f", false},
		{"any time bad", func(v *valid.Validator) { v.Str("f", ptr("25:00")).AnyTime() }, "f", true},
		{"any date slash", func(v *valid.Validator) { v.Str("f", ptr("15/07/2026")).AnyDate() }, "f", false},
		{"any date spelled", func(v *valid.Validator) { v.Str("f", ptr("15 July 2026")).AnyDate() }, "f", false},
		{"any date compact", func(v *valid.Validator) { v.Str("f", ptr("20260715")).AnyDate() }, "f", false},
		{"any date rejects datetime", func(v *valid.Validator) { v.Str("f", ptr("2026-07-15 10:00:00")).AnyDate() }, "f", true},
		{"any datetime rfc3339", func(v *valid.Validator) { v.Str("f", ptr("2026-07-15T10:00:00+07:00")).AnyDateTime() }, "f", false},
		{"any datetime spaced", func(v *valid.Validator) { v.Str("f", ptr("2026-07-15 10:00")).AnyDateTime() }, "f", false},
		{"any datetime fractional", func(v *valid.Validator) { v.Str("f", ptr("2026-07-15 10:00:00.123")).AnyDateTime() }, "f", false},
		{"any datetime rejects date", func(v *valid.Validator) { v.Str("f", ptr("2026-07-15")).AnyDateTime() }, "f", true},
		{"any temporal date", func(v *valid.Validator) { v.Str("f", ptr("2026-07-15")).AnyTemporal() }, "f", false},
		{"any temporal time", func(v *valid.Validator) { v.Str("f", ptr("10:00")).AnyTemporal() }, "f", false},
		{"any temporal datetime", func(v *valid.Validator) { v.Str("f", ptr("2026-07-15T10:00:00Z")).AnyTemporal() }, "f", false},
		{"any temporal rejects padded", func(v *valid.Validator) { v.Str("f", ptr("  2026-07-15  ")).AnyTemporal() }, "f", true},
		{"any temporal bad", func(v *valid.Validator) { v.Str("f", ptr("yesterday")).AnyTemporal() }, "f", true},
		{"optional temporal skipped", func(v *valid.Validator) { v.Str("f", ptr("")).AnyTemporal() }, "f", false},
		{"notcontains ok", func(v *valid.Validator) { v.Str("f", ptr("hello")).NotContains("x") }, "f", false},
		{"notcontains bad", func(v *valid.Validator) { v.Str("f", ptr("hexlo")).NotContains("x") }, "f", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := valid.New(ctx)
			c.rule(v)
			if c.fail {
				assert.NotNil(t, v.Error())
			} else {
				assert.Nil(t, v.Error())
			}
		})
	}
}

func TestValid_timeField(t *testing.T) {
	ctx := newCtx(t)
	now := time.Now()

	v := valid.New(ctx)
	v.Time("start", nil).Required()
	assert.Equal(t, "REQUIRED", fieldCodes(t, v.Error())["start"])

	v2 := valid.New(ctx)
	past := now.Add(-time.Hour)
	v2.Time("end", &past).After(now)
	assert.Equal(t, "INVALID_TIME_AFTER", fieldCodes(t, v2.Error())["end"])

	v3 := valid.New(ctx)
	future := now.Add(time.Hour)
	v3.Time("end", &future).After(now).Message("end must be in the future")
	assert.Nil(t, v3.Error(), "future time passes After(now)")

	v4 := valid.New(ctx)
	v4.Time("min", &past).Min(now)
	v4.Time("max", &future).Max(now)
	codes := fieldCodes(t, v4.Error())
	assert.Equal(t, "INVALID_TIME_MIN", codes["min"])
	assert.Equal(t, "INVALID_TIME_MAX", codes["max"])

	v5 := valid.New(ctx)
	v5.Time("edge_min", &now).Min(now)
	v5.Time("edge_max", &now).Max(now)
	assert.Nil(t, v5.Error(), "Min/Max are inclusive")

	v6 := valid.New(ctx)
	v6.Time("past", &future).Past()
	v6.Time("future", &past).Future()
	codes = fieldCodes(t, v6.Error())
	assert.Equal(t, "INVALID_TIME_PAST", codes["past"])
	assert.Equal(t, "INVALID_TIME_FUTURE", codes["future"])

	// 2026-07-15 is a Wednesday.
	wed := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	v7 := valid.New(ctx)
	v7.Time("ok", &wed).Weekday(time.Monday, time.Wednesday)
	assert.Nil(t, v7.Error())

	v8 := valid.New(ctx)
	v8.Time("slot", &wed).Weekday(time.Saturday, time.Sunday)
	assert.Equal(t, "INVALID_WEEKDAY", fieldCodes(t, v8.Error())["slot"])
}

func TestValid_asTime(t *testing.T) {
	ctx := newCtx(t)

	// A string in any supported format continues as a temporal chain.
	v := valid.New(ctx)
	f := v.Str("starts_at", ptr("15/07/2026 08:30")).Required().AsTime()
	f.After(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	assert.Nil(t, v.Error())
	if assert.NotNil(t, f.Value()) {
		assert.Equal(t, "2026-07-15T08:30:00Z", f.Value().Format(time.RFC3339))
	}

	// An unparsable value reports once, and the range rule after it stays quiet.
	v2 := valid.New(ctx)
	v2.Str("starts_at", ptr("not a date")).AsTime().Future()
	errs := fieldCodes(t, v2.Error())
	assert.Equal(t, "INVALID_TEMPORAL", errs["starts_at"])
	assert.Len(t, errs, 1)

	// A string rule that already failed is not reported twice.
	v3 := valid.New(ctx)
	v3.Str("starts_at", ptr("nope")).Date().AsTime().Future()
	errs = fieldCodes(t, v3.Error())
	assert.Equal(t, "INVALID_DATE", errs["starts_at"])
	assert.Len(t, errs, 1)

	// Absent optional value: every rule but Required skips.
	v4 := valid.New(ctx)
	v4.Str("starts_at", nil).AsTime().Future()
	assert.Nil(t, v4.Error())

	v5 := valid.New(ctx)
	v5.Str("starts_at", nil).AsTime().Required()
	assert.Equal(t, "REQUIRED", fieldCodes(t, v5.Error())["starts_at"])

	// A zone-less value is read in the location given.
	bkk := time.FixedZone("ICT", 7*60*60)
	v6 := valid.New(ctx)
	f6 := v6.Str("d", ptr("2026-07-15")).AsTimeIn(bkk, valid.DateLayouts...)
	assert.Nil(t, v6.Error())
	if assert.NotNil(t, f6.Value()) {
		assert.Equal(t, "2026-07-15T00:00:00+07:00", f6.Value().Format(time.RFC3339))
	}

	// Custom message binds to the failing temporal rule.
	v7 := valid.New(ctx)
	v7.Str("d", ptr("2020-01-01")).AsTime().Future().Message("must be a future date")
	assert.Equal(t, "must be a future date", fieldMessages(t, v7.Error())["d"])
}

// A padded value must not validate: the service is handed the raw string, and
// its own time.Parse cannot read it — passing validation here is how a field
// ends up silently unset.
func TestValid_temporalRulesRejectPaddedValues(t *testing.T) {
	ctx := newCtx(t)
	v := valid.New(ctx)
	v.Str("date", ptr(" 2026-07-15 ")).Date()
	v.Str("datetime", ptr(" 2026-07-15 10:00:00 ")).DateTime()
	v.Str("trailing", ptr("2026-07-15\n")).Date()
	v.Str("temporal", ptr(" 2026-07-15")).AsTime()
	v.Str("clean", ptr("2026-07-15")).Date()

	codes := fieldCodes(t, v.Error())
	assert.Equal(t, "INVALID_DATE", codes["date"])
	assert.Equal(t, "INVALID_DATETIME", codes["datetime"])
	assert.Equal(t, "INVALID_DATE", codes["trailing"])
	assert.Equal(t, "INVALID_TEMPORAL", codes["temporal"])
	assert.NotContains(t, codes, "clean")

	// The rule and the service now agree on what is parsable.
	_, err := time.Parse("2006-01-02", " 2026-07-15 ")
	assert.Error(t, err, "downstream parse fails on the same value the rule rejects")
}

func TestValid_parseTime(t *testing.T) {
	if _, ok := valid.ParseDate("2026-07-15"); !ok {
		t.Fatal("ISO date should parse")
	}
	if _, ok := valid.ParseDate("2026-07-15 10:00:00"); ok {
		t.Fatal("a datetime is not a date")
	}
	if _, ok := valid.ParseClock("11:59 PM"); !ok {
		t.Fatal("12-hour clock should parse")
	}
	if _, ok := valid.ParseDateTime("Wed, 15 Jul 2026 10:00:00 +0700"); !ok {
		t.Fatal("RFC 1123Z should parse")
	}
	if _, ok := valid.ParseTime(""); ok {
		t.Fatal("empty value should not parse")
	}
	if _, ok := valid.ParseTime(" 2026-07-15 "); ok {
		t.Fatal("padding is not stripped: the value must match a layout exactly")
	}
	// Day-first wins for the ambiguous slashed form.
	got, ok := valid.ParseTime("03/04/2026", valid.DateLayouts...)
	if !ok {
		t.Fatal("slashed date should parse")
	}
	assert.Equal(t, time.April, got.Month())
	assert.Equal(t, 3, got.Day())
}

// --- complex DB conditions ---

func newCtxTenant(t *testing.T) core.IContext {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT, tenant_id INTEGER, deleted_at TEXT)`)
	db.Exec(`INSERT INTO users (id, email, tenant_id, deleted_at) VALUES (1,'a@b.com',10,NULL)`)
	db.Exec(`INSERT INTO users (id, email, tenant_id, deleted_at) VALUES (2,'a@b.com',20,NULL)`) // same email, other tenant
	db.Exec(`INSERT INTO users (id, email, tenant_id, deleted_at) VALUES (3,'gone@b.com',10,'2026-01-01')`)
	t.Setenv("APP_ENV", "test")
	t.Setenv("APP_SERVICE", "test")
	env, _ := core.NewEnvPath(t.TempDir())
	app, _ := core.NewApp(env, core.WithSQL("default", db))
	return app.NewContext(context.Background())
}

func TestValid_uniqueWithScopes(t *testing.T) {
	ctx := newCtxTenant(t)

	// unique WITHIN a tenant: a@b.com exists in tenant 10 → fail
	v := valid.New(ctx)
	v.Str("email", ptr("a@b.com")).Unique("users", "email", valid.Cond("tenant_id = ?", 10))
	assert.NotNil(t, v.Error(), "duplicate within tenant 10")

	// same email is free in tenant 30 → pass
	v2 := valid.New(ctx)
	v2.Str("email", ptr("a@b.com")).Unique("users", "email", valid.Cond("tenant_id = ?", 30))
	assert.Nil(t, v2.Error(), "free within tenant 30")

	// updating user id=1 in tenant 10: exclude self → pass (only self matches)
	v3 := valid.New(ctx)
	v3.Str("email", ptr("a@b.com")).
		Unique("users", "email", valid.Cond("tenant_id = ?", 10), valid.Except("id", 1))
	assert.Nil(t, v3.Error(), "self excluded on update")

	// soft-delete filter: gone@b.com only exists as deleted → Exists with active filter fails
	v4 := valid.New(ctx)
	v4.Str("email", ptr("gone@b.com")).Exists("users", "email", valid.Cond("deleted_at IS NULL"))
	assert.NotNil(t, v4.Error(), "soft-deleted row should not count as existing")
}

func TestValid_checkCustomDB(t *testing.T) {
	ctx := newCtxTenant(t)
	v := valid.New(ctx)
	// arbitrary predicate with context/DB access
	v.Str("email", ptr("a@b.com")).Check("BLOCKED", func(c core.IContext, val string) (bool, error) {
		var n int64
		err := c.DB().Table("users").Where("email = ? AND tenant_id > ?", val, 15).Count(&n).Error
		return n == 0, err // ok if not present in high-tenant range
	})
	assert.Equal(t, "BLOCKED", fieldCodes(t, v.Error())["email"]) // tenant 20 matches → blocked

	// infra error → 500
	v2 := valid.New(ctx)
	v2.Str("email", ptr("x")).Check("X", func(c core.IContext, val string) (bool, error) {
		return true, assertAnError()
	})
	require.NotNil(t, v2.Error())
	assert.Equal(t, 500, v2.Error().GetStatus())
}

func assertAnError() error { return context.DeadlineExceeded }
