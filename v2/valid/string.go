package valid

import (
	"encoding/base64"
	"encoding/json"
	"net/mail"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// StringField chains string rules. Empty/nil values are skipped by every rule
// except Required, so optional fields validate only when present.
type StringField struct {
	v        *Validator
	name     string
	value    *string
	failed   bool
	idx      int
	msgArmed bool // true right after the rule that failed, so Message() binds to it
}

func (f *StringField) fail(code string, data any) *StringField {
	if f.failed {
		return f
	}
	f.failed = true
	f.idx = f.v.add(f.name, code, data)
	f.msgArmed = true
	return f
}

// Message overrides the human message of the rule immediately before it — and
// only when that rule is the one that failed. This gives a custom message per
// rule (stop-on-first still shows a single violation):
//
//	v.Str("password", r.Password).
//	    Required().Message("Password is required").
//	    Min(8).Message("Password must be at least 8 characters")
func (f *StringField) Message(msg string) *StringField {
	if f.msgArmed {
		f.v.overrideMessage(f.idx, msg)
		f.msgArmed = false
	}
	return f
}

func (f *StringField) empty() bool { return f.value == nil || *f.value == "" }

// skip reports whether the current rule should be skipped, and disarms any
// pending Message() (a skipped/passing rule is not the one Message binds to).
func (f *StringField) skip() bool {
	f.msgArmed = false
	return f.failed || f.empty()
}

// Normalizers ------------------------------------------------------------------
//
// Trim and Lower do not judge a value, they rewrite it — through the pointer the
// field was built from, which is the one binding filled in, so the handler that
// reads the payload afterwards sees the cleaned value too. Put them first in the
// chain: rules run in the order they are called, and a normalizer only helps the
// rules that come after it.
//
// They are per-field on purpose. A password may legitimately end in a space and
// a free-text note may need its indentation, so there is no "trim everything"
// switch at the binding layer.
//
// One trap: the value must be a pointer into the payload. Passing a temporary
// (`v.Str("x", utils.Ptr(s))`) normalizes that temporary and the payload keeps
// the original — silently.

// Trim strips surrounding whitespace from the bound value.
//
//	v.Str("meeting_at", &r.MeetingAt).Trim().Required().DateTime()
//
// The temporal rules match a layout exactly, so this is how a client that pads
// its values is accommodated without handing the service a string its own
// time.Parse cannot read. Trim().Required() also collapses a whitespace-only
// value to "", which Required then reports as missing.
func (f *StringField) Trim() *StringField {
	f.msgArmed = false
	if f.value != nil {
		*f.value = strings.TrimSpace(*f.value)
	}
	return f
}

// Lower lowercases the bound value — for a field whose case is noise rather than
// data, so it is stored and looked up one way:
//
//	v.Str("email", &r.Email).Trim().Lower().Required().Email().Unique("users", "email")
//
// Lower normalizes; Lowercase validates. Use Lowercase when a client sending
// "ADMIN" is an error worth reporting, Lower when it is merely something to fix.
func (f *StringField) Lower() *StringField {
	f.msgArmed = false
	if f.value != nil {
		*f.value = strings.ToLower(*f.value)
	}
	return f
}

// Apply rewrites the bound value with fn — the general form of Trim and Lower,
// for a normalization a service keeps to itself:
//
//	v.Str("phone", &r.Phone).Apply(normalizeThaiPhone).Required().Match(phoneRe)
//
// fn must be total: it is handed whatever the client sent and its result is the
// value every later rule (and the handler) sees. A nil fn or an absent field is
// a no-op.
func (f *StringField) Apply(fn func(string) string) *StringField {
	f.msgArmed = false
	if f.value != nil && fn != nil {
		*f.value = fn(*f.value)
	}
	return f
}

// Rules --------------------------------------------------------------------------

// Required fails when the value is nil or empty.
func (f *StringField) Required() *StringField {
	f.msgArmed = false
	if f.empty() {
		return f.fail("REQUIRED", nil)
	}
	return f
}

// NotBlank rejects a value that was sent but holds nothing — "" or whitespace
// only — while an absent field still passes. It is the rule for an optional
// field where a blank value is a client bug rather than a way to say "no value":
//
//	v.Str("change_at", r.ChangeAt).NotBlank().Date()   // absent ok, "" is a 400
//
// Required is about whether the field arrives at all and reads "" as absent, but
// it accepts "   " — whitespace is a value. Chain both when a required field must
// also carry something: Required().NotBlank().
func (f *StringField) NotBlank() *StringField {
	f.msgArmed = false
	if f.failed || f.value == nil {
		return f
	}
	if strings.TrimSpace(*f.value) == "" {
		return f.fail("BLANK", nil)
	}
	return f
}

// Email validates an RFC 5322 address.
func (f *StringField) Email() *StringField {
	if f.skip() {
		return f
	}
	if _, err := mail.ParseAddress(*f.value); err != nil {
		return f.fail("INVALID_EMAIL", nil)
	}
	return f
}

// URL validates an absolute URL.
func (f *StringField) URL() *StringField {
	if f.skip() {
		return f
	}
	if u, err := url.ParseRequestURI(*f.value); err != nil || u.Scheme == "" {
		return f.fail("INVALID_URL", nil)
	}
	return f
}

// Length checks the rune count is within [min, max].
func (f *StringField) Length(min, max int) *StringField {
	if f.skip() {
		return f
	}
	n := len([]rune(*f.value))
	if n < min || n > max {
		return f.fail("INVALID_STRING_LENGTH", map[string]any{"min": min, "max": max})
	}
	return f
}

// Min checks a minimum rune count.
func (f *StringField) Min(min int) *StringField {
	if f.skip() {
		return f
	}
	if len([]rune(*f.value)) < min {
		return f.fail("INVALID_STRING_SIZE_MIN", map[string]any{"min": min})
	}
	return f
}

// Max checks a maximum rune count.
func (f *StringField) Max(max int) *StringField {
	if f.skip() {
		return f
	}
	if len([]rune(*f.value)) > max {
		return f.fail("INVALID_STRING_SIZE_MAX", map[string]any{"max": max})
	}
	return f
}

// In checks membership in a fixed set.
func (f *StringField) In(options ...string) *StringField {
	if f.skip() {
		return f
	}
	if !slices.Contains(options, *f.value) {
		return f.fail("INVALID_VALUE_NOT_IN_LIST", map[string]any{"options": strings.Join(options, ", ")})
	}
	return f
}

// Lowercase requires an all-lowercase value.
func (f *StringField) Lowercase() *StringField {
	if f.skip() {
		return f
	}
	if *f.value != strings.ToLower(*f.value) {
		return f.fail("INVALID_STRING_LOWERCASE", nil)
	}
	return f
}

// Uppercase requires an all-uppercase value.
func (f *StringField) Uppercase() *StringField {
	if f.skip() {
		return f
	}
	if *f.value != strings.ToUpper(*f.value) {
		return f.fail("INVALID_STRING_UPPERCASE", nil)
	}
	return f
}

// Contains requires a substring.
func (f *StringField) Contains(sub string) *StringField {
	if f.skip() {
		return f
	}
	if !strings.Contains(*f.value, sub) {
		return f.fail("INVALID_STRING_CONTAIN", map[string]any{"sub": sub})
	}
	return f
}

// Prefix requires a prefix.
func (f *StringField) Prefix(sub string) *StringField {
	if f.skip() {
		return f
	}
	if !strings.HasPrefix(*f.value, sub) {
		return f.fail("INVALID_STRING_START_WITH", map[string]any{"sub": sub})
	}
	return f
}

// Suffix requires a suffix.
func (f *StringField) Suffix(sub string) *StringField {
	if f.skip() {
		return f
	}
	if !strings.HasSuffix(*f.value, sub) {
		return f.fail("INVALID_STRING_END_WITH", map[string]any{"sub": sub})
	}
	return f
}

// Match requires the value to match a precompiled regular expression.
func (f *StringField) Match(re *regexp.Regexp) *StringField {
	if f.skip() {
		return f
	}
	if !re.MatchString(*f.value) {
		return f.fail("INVALID_TYPE", nil)
	}
	return f
}

// NotContains requires the value to NOT contain a substring.
func (f *StringField) NotContains(sub string) *StringField {
	if f.skip() {
		return f
	}
	if strings.Contains(*f.value, sub) {
		return f.fail("INVALID_STRING_NOT_CONTAIN", map[string]any{"sub": sub})
	}
	return f
}

// Numeric requires the value to be an integer or decimal number (as a string).
func (f *StringField) Numeric() *StringField {
	if f.skip() {
		return f
	}
	if _, err := strconv.ParseFloat(*f.value, 64); err != nil {
		return f.fail("INVALID_NUMERIC", nil)
	}
	return f
}

// UUID requires a canonical UUID string.
func (f *StringField) UUID() *StringField {
	if f.skip() {
		return f
	}
	if _, err := uuid.Parse(*f.value); err != nil {
		return f.fail("INVALID_UUID", nil)
	}
	return f
}

// IP requires a valid IPv4/IPv6 address.
func (f *StringField) IP() *StringField {
	if f.skip() {
		return f
	}
	if _, err := netip.ParseAddr(*f.value); err != nil {
		return f.fail("INVALID_IP", nil)
	}
	return f
}

// Base64 requires standard base64 encoding.
func (f *StringField) Base64() *StringField {
	if f.skip() {
		return f
	}
	if _, err := base64.StdEncoding.DecodeString(*f.value); err != nil {
		return f.fail("INVALID_BASE64", nil)
	}
	return f
}

// JSON requires the value to be syntactically valid JSON.
func (f *StringField) JSON() *StringField {
	if f.skip() {
		return f
	}
	if !json.Valid([]byte(*f.value)) {
		return f.fail("INVALID_JSON", nil)
	}
	return f
}

// Date requires a date, in any of the given layouts (default "2006-01-02").
// Pass several to accept more than one shape, or use AnyDate for every format
// the package knows:
//
//	v.Str("dob", &r.DOB).Date()                            // ISO only
//	v.Str("dob", &r.DOB).Date("2006-01-02", "02/01/2006")  // either
//	v.Str("dob", &r.DOB).AnyDate()                         // any known format
func (f *StringField) Date(layouts ...string) *StringField {
	if len(layouts) == 0 {
		layouts = []string{"2006-01-02"}
	}
	if f.skip() {
		return f
	}
	if _, ok := ParseTime(*f.value, layouts...); !ok {
		return f.fail("INVALID_DATE", nil)
	}
	return f
}

// Time requires a clock time, in any of the given layouts (default "15:04:05").
// AnyTime accepts every clock format the package knows, 12-hour ones included.
func (f *StringField) Time(layouts ...string) *StringField {
	if len(layouts) == 0 {
		layouts = []string{"15:04:05"}
	}
	if f.skip() {
		return f
	}
	if _, ok := ParseTime(*f.value, layouts...); !ok {
		return f.fail("INVALID_TIME", nil)
	}
	return f
}

// DateTime requires a date and time, in any of the given layouts (default
// "2006-01-02 15:04:05"). AnyDateTime accepts every datetime format the package
// knows, RFC 3339 included.
func (f *StringField) DateTime(layouts ...string) *StringField {
	if len(layouts) == 0 {
		layouts = []string{"2006-01-02 15:04:05"}
	}
	if f.skip() {
		return f
	}
	if _, ok := ParseTime(*f.value, layouts...); !ok {
		return f.fail("INVALID_DATETIME", nil)
	}
	return f
}

// ISO8601 requires an RFC 3339 / ISO 8601 timestamp (e.g. 2006-01-02T15:04:05Z).
func (f *StringField) ISO8601() *StringField {
	if f.skip() {
		return f
	}
	if _, err := time.Parse(time.RFC3339, *f.value); err != nil {
		return f.fail("INVALID_ISO8601", nil)
	}
	return f
}

// AnyDate accepts a date in any layout listed in DateLayouts — ISO, slashed,
// dotted, compact or spelled-out month. Clock components are rejected: a date
// field stays a date field.
func (f *StringField) AnyDate() *StringField { return f.Date(DateLayouts...) }

// AnyTime accepts a clock time in any layout listed in TimeLayouts, 24-hour or
// 12-hour, with or without seconds.
func (f *StringField) AnyTime() *StringField { return f.Time(TimeLayouts...) }

// AnyDateTime accepts a date and time in any layout listed in DateTimeLayouts,
// RFC 3339 and the RFC 822/850/1123 email forms included.
func (f *StringField) AnyDateTime() *StringField { return f.DateTime(DateTimeLayouts...) }

// AnyTemporal accepts a date, a clock time or a datetime, in any known layout —
// the loosest temporal rule, for a field whose precision the client chooses.
func (f *StringField) AnyTemporal() *StringField {
	if f.skip() {
		return f
	}
	if _, ok := ParseTime(*f.value); !ok {
		return f.fail("INVALID_TEMPORAL", nil)
	}
	return f
}

// Custom runs an arbitrary predicate; ok==false records code.
func (f *StringField) Custom(code string, ok bool) *StringField {
	if f.skip() {
		return f
	}
	if !ok {
		return f.fail(code, nil)
	}
	return f
}
