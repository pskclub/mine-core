package valid

import (
	"slices"
	"strings"
	"time"
)

// Temporal layouts -------------------------------------------------------------

// DateLayouts, TimeLayouts and DateTimeLayouts are the formats the format-free
// rules (AnyDate, AnyTime, AnyDateTime, AnyTemporal, AsTime) accept. They are
// tried in order and the first layout that consumes the whole value wins.
//
// Append to them at init time to teach the validator a house format:
//
//	func init() { valid.DateLayouts = append(valid.DateLayouts, "02 Jan 06") }
//
// A fractional-seconds field is always accepted after a seconds field, even
// when the layout does not mention one — that is time.Parse's own rule — so
// "15:04:05" also matches "15:04:05.123".
//
// Ambiguity is resolved in favour of day-first: "02/01/2006" reads 03/04/2026 as
// 3 April, never 4 March. A month-first API must say so explicitly with
// Date("01/02/2006"); no auto-detecting rule can tell the two apart.
var (
	// DateLayouts are calendar dates with no clock component.
	DateLayouts = []string{
		"2006-01-02",
		"2006/01/02",
		"2006.01.02",
		"20060102",
		"02-01-2006",
		"02/01/2006",
		"02.01.2006",
		"2 January 2006",
		"2 Jan 2006",
		"January 2, 2006",
		"Jan 2, 2006",
	}

	// TimeLayouts are clock times with no date component.
	TimeLayouts = []string{
		"15:04:05Z07:00",
		"15:04:05",
		"15:04",
		"150405",
		"3:04:05 PM",
		"3:04:05PM",
		"3:04 PM",
		"3:04PM",
	}

	// DateTimeLayouts carry both a date and a clock time.
	DateTimeLayouts = []string{
		time.RFC3339, // 2006-01-02T15:04:05Z07:00
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006/01/02 15:04:05",
		"2006/01/02 15:04",
		"02/01/2006 15:04:05",
		"02/01/2006 15:04",
		"20060102150405",
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		time.RFC850,
		time.ANSIC,
		time.UnixDate,
		time.RubyDate,
	}
)

// AllLayouts returns every known layout, richest first, so a value that carries
// both a date and a time is never truncated to one of them.
func AllLayouts() []string {
	return slices.Concat(DateTimeLayouts, DateLayouts, TimeLayouts)
}

// ParseTime parses s against each layout in turn and returns the first success.
// With no layouts it tries AllLayouts, so any supported date, time or datetime
// format parses.
//
// The value must match a layout exactly — surrounding whitespace is a failure,
// not something to strip. A validator that accepted " 2026-07-15 " would be
// vouching for a string that the service's own time.Parse then chokes on, and
// the field ends up silently unset. Trim at the edge (before binding) if a
// client is known to pad its values.
func ParseTime(s string, layouts ...string) (time.Time, bool) {
	return ParseTimeIn(time.UTC, s, layouts...)
}

// ParseTimeIn is ParseTime with the location a zone-less value is read in — pass
// the app's timezone when "2026-07-15" means midnight in Bangkok, not in UTC. A
// value that carries its own offset keeps it.
func ParseTimeIn(loc *time.Location, s string, layouts ...string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if loc == nil {
		loc = time.UTC
	}
	if len(layouts) == 0 {
		layouts = AllLayouts()
	}
	for _, l := range layouts {
		if l == "" {
			continue
		}
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ParseDate parses any supported date-only format.
func ParseDate(s string) (time.Time, bool) { return ParseTime(s, DateLayouts...) }

// ParseClock parses any supported clock-time-only format.
func ParseClock(s string) (time.Time, bool) { return ParseTime(s, TimeLayouts...) }

// ParseDateTime parses any supported date-and-time format.
func ParseDateTime(s string) (time.Time, bool) { return ParseTime(s, DateTimeLayouts...) }

// TimeField --------------------------------------------------------------------

// TimeField chains rules for a *time.Time. A nil value skips every rule except
// Required. Build one from a time.Time with Validator.Time, or from a string in
// any supported format with StringField.AsTime.
type TimeField struct {
	v        *Validator
	name     string
	value    *time.Time
	failed   bool
	idx      int
	msgArmed bool
}

// Time starts a *time.Time field rule chain.
func (v *Validator) Time(name string, value *time.Time) *TimeField {
	return &TimeField{v: v, name: name, value: value}
}

// AsTime parses a string field in any supported format (or only the layouts
// given) and continues as a temporal rule chain, so range rules apply to what
// the client actually sent:
//
//	v.Str("starts_at", &r.StartsAt).Required().AsTime().Future()
//
// A field that already failed a string rule, or is absent, carries that state
// over: no second violation is recorded for the same field.
func (f *StringField) AsTime(layouts ...string) *TimeField {
	return f.AsTimeIn(time.UTC, layouts...)
}

// AsTimeIn is AsTime with the location a zone-less value is read in.
func (f *StringField) AsTimeIn(loc *time.Location, layouts ...string) *TimeField {
	tf := &TimeField{v: f.v, name: f.name}
	if f.failed {
		tf.failed = true // already reported; stay quiet but skip the rest
		return tf
	}
	if f.empty() {
		return tf // optional and absent: later rules skip, Required still fires
	}
	t, ok := ParseTimeIn(loc, *f.value, layouts...)
	if !ok {
		return tf.fail("INVALID_TEMPORAL", nil)
	}
	tf.value = &t
	return tf
}

// Value returns the field's value — the parsed one after AsTime — or nil when
// absent or unparsable. Use it for cross-field comparisons:
//
//	start := v.Str("start", &r.Start).Required().AsTime()
//	end := v.Str("end", &r.End).Required().AsTime()
//	if s, e := start.Value(), end.Value(); s != nil && e != nil {
//	    v.Must("end", "INVALID_DATE_RANGE", e.After(*s), map[string]any{"other": "start"})
//	}
func (f *TimeField) Value() *time.Time { return f.value }

func (f *TimeField) fail(code string, data any) *TimeField {
	if f.failed {
		return f
	}
	f.failed = true
	f.idx = f.v.add(f.name, code, data)
	f.msgArmed = true
	return f
}

func (f *TimeField) skip() bool {
	f.msgArmed = false
	return f.failed || f.value == nil
}

// Message overrides the message of the rule immediately before it, when it failed.
func (f *TimeField) Message(msg string) *TimeField {
	if f.msgArmed {
		f.v.overrideMessage(f.idx, msg)
		f.msgArmed = false
	}
	return f
}

// Required fails when the value is nil.
func (f *TimeField) Required() *TimeField {
	f.msgArmed = false
	if f.value == nil {
		return f.fail("REQUIRED", nil)
	}
	return f
}

// Before requires the value to be strictly before t.
func (f *TimeField) Before(t time.Time) *TimeField {
	if f.skip() {
		return f
	}
	if !f.value.Before(t) {
		return f.fail("INVALID_TIME_BEFORE", map[string]any{"other": t.Format(time.RFC3339)})
	}
	return f
}

// After requires the value to be strictly after t.
func (f *TimeField) After(t time.Time) *TimeField {
	if f.skip() {
		return f
	}
	if !f.value.After(t) {
		return f.fail("INVALID_TIME_AFTER", map[string]any{"other": t.Format(time.RFC3339)})
	}
	return f
}

// Min requires the value to be at or after min (inclusive, like NumberField.Min).
func (f *TimeField) Min(min time.Time) *TimeField {
	if f.skip() {
		return f
	}
	if f.value.Before(min) {
		return f.fail("INVALID_TIME_MIN", map[string]any{"min": min.Format(time.RFC3339)})
	}
	return f
}

// Max requires the value to be at or before max (inclusive, like NumberField.Max).
func (f *TimeField) Max(max time.Time) *TimeField {
	if f.skip() {
		return f
	}
	if f.value.After(max) {
		return f.fail("INVALID_TIME_MAX", map[string]any{"max": max.Format(time.RFC3339)})
	}
	return f
}

// Between requires the value to be within [start, end].
func (f *TimeField) Between(start, end time.Time) *TimeField {
	if f.skip() {
		return f
	}
	if f.value.Before(start) || f.value.After(end) {
		return f.fail("INVALID_TIME_RANGE", map[string]any{
			"start": start.Format(time.RFC3339),
			"end":   end.Format(time.RFC3339),
		})
	}
	return f
}

// Past requires a value earlier than now.
func (f *TimeField) Past() *TimeField {
	if f.skip() {
		return f
	}
	if !f.value.Before(time.Now()) {
		return f.fail("INVALID_TIME_PAST", nil)
	}
	return f
}

// Future requires a value later than now.
func (f *TimeField) Future() *TimeField {
	if f.skip() {
		return f
	}
	if !f.value.After(time.Now()) {
		return f.fail("INVALID_TIME_FUTURE", nil)
	}
	return f
}

// Weekday requires the value to fall on one of the given days — a booking slot
// restricted to weekdays, say.
func (f *TimeField) Weekday(days ...time.Weekday) *TimeField {
	if f.skip() {
		return f
	}
	if slices.Contains(days, f.value.Weekday()) {
		return f
	}
	names := make([]string, len(days))
	for i, d := range days {
		names[i] = d.String()
	}
	return f.fail("INVALID_WEEKDAY", map[string]any{"days": strings.Join(names, ", ")})
}

// Custom runs an arbitrary predicate; ok==false records code.
func (f *TimeField) Custom(code string, ok bool) *TimeField {
	if f.skip() {
		return f
	}
	if !ok {
		return f.fail(code, nil)
	}
	return f
}
