package valid

import (
	"fmt"
	"strings"
)

// catalog maps machine codes to human message templates. {field} plus any keys
// from the rule's data map are interpolated. Swap/extend for i18n.
var catalog = map[string]string{
	"REQUIRED":                   "The {field} field is required",
	"BLANK":                      "The {field} field must not be blank",
	"INVALID_TYPE":               "The {field} field has an invalid type",
	"INVALID_EMAIL":              "The {field} field must be a valid email address",
	"INVALID_URL":                "The {field} field must be a valid URL",
	"INVALID_STRING_LENGTH":      "The {field} field must be between {min} and {max} characters",
	"INVALID_STRING_SIZE_MIN":    "The {field} field must be at least {min} characters",
	"INVALID_STRING_SIZE_MAX":    "The {field} field must be at most {max} characters",
	"INVALID_STRING_LOWERCASE":   "The {field} field must be lowercase",
	"INVALID_STRING_UPPERCASE":   "The {field} field must be uppercase",
	"INVALID_STRING_CONTAIN":     "The {field} field must contain {sub}",
	"INVALID_STRING_START_WITH":  "The {field} field must start with {sub}",
	"INVALID_STRING_END_WITH":    "The {field} field must end with {sub}",
	"INVALID_VALUE_NOT_IN_LIST":  "The {field} field must be one of {options}",
	"INVALID_NUMBER_MIN":         "The {field} field must be greater than or equal to {min}",
	"INVALID_NUMBER_MAX":         "The {field} field must be less than or equal to {max}",
	"INVALID_NUMBER_BETWEEN":     "The {field} field must be between {min} and {max}",
	"INVALID_ARRAY_SIZE":         "The {field} field must contain {size} item(s)",
	"INVALID_ARRAY_SIZE_MIN":     "The {field} field must contain at least {min} item(s)",
	"INVALID_ARRAY_SIZE_MAX":     "The {field} field must contain at most {max} item(s)",
	"INVALID_STRING_NOT_CONTAIN": "The {field} field must not contain {sub}",
	"INVALID_NUMERIC":            "The {field} field must be a number",
	"INVALID_UUID":               "The {field} field must be a valid UUID",
	"INVALID_IP":                 "The {field} field must be a valid IP address",
	"INVALID_BASE64":             "The {field} field must be base64-encoded",
	"INVALID_JSON":               "The {field} field must be valid JSON",
	"INVALID_DATE":               "The {field} field must be a valid date",
	"INVALID_TIME":               "The {field} field must be a valid time",
	"INVALID_DATETIME":           "The {field} field must be a valid datetime (YYYY-MM-DD HH:mm:ss)",
	"INVALID_TEMPORAL":           "The {field} field must be a valid date or time",
	"INVALID_ISO8601":            "The {field} field must be a valid ISO 8601 datetime",
	"INVALID_TIME_BEFORE":        "The {field} field must be before {other}",
	"INVALID_TIME_AFTER":         "The {field} field must be after {other}",
	"INVALID_TIME_MIN":           "The {field} field must be {min} or later",
	"INVALID_TIME_MAX":           "The {field} field must be {max} or earlier",
	"INVALID_TIME_RANGE":         "The {field} field must be between {start} and {end}",
	"INVALID_TIME_PAST":          "The {field} field must be in the past",
	"INVALID_TIME_FUTURE":        "The {field} field must be in the future",
	"INVALID_WEEKDAY":            "The {field} field must fall on {days}",
	"UNIQUE":                     "The {field} field's value already exists",
	"NOT_EXISTS":                 "The {field} field's value does not exist",
	"VALIDATION_ERROR":           "The {field} field could not be validated",
	// common cross-field / conditional codes (override via SetMessage as needed)
	"REQUIRED_WITH":      "The {field} field is required",
	"REQUIRED_ONE_OF":    "Exactly one of these fields is required",
	"INVALID_DATE_RANGE": "The {field} field must be after {other}",
	"MISMATCH":           "The {field} field does not match",
}

// SetMessage registers or overrides the message template for a code. Call it at
// init time (e.g. to localise or add app-specific codes used with Must).
func SetMessage(code, template string) { catalog[code] = template }

// render fills a message template for code with the field name and data values.
func render(code, field string, data any) string {
	tmpl, ok := catalog[code]
	if !ok {
		tmpl = "The {field} field is invalid"
	}
	out := strings.ReplaceAll(tmpl, "{field}", field)

	if m, ok := data.(map[string]any); ok {
		for k, v := range m {
			out = strings.ReplaceAll(out, "{"+k+"}", fmt.Sprint(v))
		}
	}
	return out
}
