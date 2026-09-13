package core

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
)

// redacted is what a masked value is replaced with. It is deliberately visible:
// "the field was there, we are not telling you what was in it".
const redacted = "[redacted]"

// defaultScrubKeys are the key fragments whose values never leave the process.
// v1 shipped the whole configuration to Sentry verbatim — DB password, JWT
// secret and S3 keys included. v2 masks by default and lets you add to the list,
// never take safety away silently.
var defaultScrubKeys = []string{
	"password", "passwd", "secret", "token", "authorization", "auth",
	"api_key", "apikey", "access_key", "private", "credential", "dsn",
	"cookie", "session", "signature", "otp", "pin", "cvv", "card_number",
	"jwt", "refresh", "salt", "seed", "mnemonic",
}

// scrubber masks sensitive data on its way to Sentry.
type scrubber struct {
	keys   []string
	values []string
}

func newScrubber(extraKeys, secretValues []string) *scrubber {
	keys := make([]string, 0, len(defaultScrubKeys)+len(extraKeys))
	keys = append(keys, defaultScrubKeys...)
	for _, k := range extraKeys {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			keys = append(keys, k)
		}
	}
	values := make([]string, 0, len(secretValues))
	for _, v := range secretValues {
		if strings.TrimSpace(v) != "" {
			values = append(values, v)
		}
	}
	return &scrubber{keys: keys, values: values}
}

// sensitive reports whether a key's value must be masked.
func (s *scrubber) sensitive(key string) bool {
	k := strings.ToLower(key)
	for _, frag := range s.keys {
		if strings.Contains(k, frag) {
			return true
		}
	}
	return false
}

// maskSecrets replaces any configured secret found inside a free-form string —
// the connection string in an error message, the token in a URL.
func (s *scrubber) maskSecrets(v string) string {
	for _, secret := range s.values {
		if secret != "" && strings.Contains(v, secret) {
			v = strings.ReplaceAll(v, secret, redacted)
		}
	}
	return v
}

// scrubValue masks one key/value pair.
func (s *scrubber) scrubValue(key string, value any) any {
	if s.sensitive(key) {
		return redacted
	}
	switch v := value.(type) {
	case string:
		return s.maskSecrets(v)
	case map[string]any:
		return s.scrubAny(v)
	case map[string]string:
		return s.scrubStrings(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = s.scrubValue("", item)
		}
		return out
	default:
		// a struct logged whole: its own key says nothing about the fields
		// inside it, so it is scrubbed through its JSON, where the field names
		// are visible to the same rules as everything else
		return s.scrubStruct(value)
	}
}

// scrubStruct masks the fields of a value the scrubber cannot walk directly, by
// going through its JSON. A value that will not marshal is returned untouched:
// it is then rendered by Sentry itself, and refusing to log it would be a worse
// answer than logging what we were given.
func (s *scrubber) scrubStruct(value any) any {
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return value
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
	default:
		return value
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	// Only a value that is a document once encoded is walked. Plenty of types
	// are arrays underneath and a single scalar on the wire — a trace id is
	// [16]byte and marshals to a hex string — and rewriting those would change
	// what Sentry receives while masking nothing.
	if trimmed := strings.TrimSpace(string(encoded)); !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return value
	}
	var out any
	if err := json.Unmarshal(s.scrubJSON(encoded), &out); err != nil {
		return value
	}
	return out
}

// scrubAny returns a masked copy of a structured map (the input is never
// mutated — it belongs to the caller's request).
func (s *scrubber) scrubAny(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = s.scrubValue(k, v)
	}
	return out
}

// scrubStrings is scrubAny for a string map (what IENV.All returns).
func (s *scrubber) scrubStrings(in map[string]string) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if s.sensitive(k) {
			out[k] = redacted
			continue
		}
		out[k] = s.maskSecrets(v)
	}
	return out
}

// scrubJSON masks sensitive fields inside a JSON document, recursively. A body
// that is not JSON is returned with configured secrets masked and nothing else.
func (s *scrubber) scrubJSON(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return []byte(s.maskSecrets(string(body)))
	}
	cleaned, err := json.Marshal(s.scrubDoc("", doc))
	if err != nil {
		return []byte(redacted)
	}
	return cleaned
}

// scrubDoc walks a decoded JSON document masking sensitive leaves.
func (s *scrubber) scrubDoc(key string, node any) any {
	if key != "" && s.sensitive(key) {
		return redacted
	}
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = s.scrubDoc(k, item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = s.scrubDoc(key, item)
		}
		return out
	case string:
		return s.maskSecrets(v)
	default:
		return node
	}
}

// scrubEvent is the last line of defence: whatever any integration put on the
// event — headers, cookies, query strings, contexts, breadcrumbs — is masked
// here, so a new capture path cannot leak by forgetting to scrub.
func (s *scrubber) scrubEvent(event *sentry.Event) *sentry.Event {
	if event == nil {
		return nil
	}
	if event.Request != nil {
		event.Request.Cookies = ""
		event.Request.Headers = s.scrubHeaders(event.Request.Headers)
		event.Request.Env = s.scrubStringMap(event.Request.Env)
		event.Request.QueryString = s.scrubQuery(event.Request.QueryString)
		event.Request.URL = s.maskSecrets(event.Request.URL)
		if event.Request.Data != "" {
			event.Request.Data = string(s.scrubJSON([]byte(event.Request.Data)))
		}
	}
	for name, ctx := range event.Contexts {
		event.Contexts[name] = sentry.Context(s.scrubAny(ctx))
	}
	for _, crumb := range event.Breadcrumbs {
		if crumb == nil {
			continue
		}
		crumb.Data = s.scrubAny(crumb.Data)
		crumb.Message = s.maskSecrets(crumb.Message)
	}
	if event.User.Data != nil {
		event.User.Data = s.scrubStringMap(event.User.Data)
	}
	event.Message = s.maskSecrets(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = s.maskSecrets(event.Exception[i].Value)
	}
	return event
}

// scrubLog masks a log line on its way to Sentry Logs: its attributes by key —
// the same list every other channel uses — and its body by value, so a
// connection string that ended up inside a message is masked there too.
func (s *scrubber) scrubLog(log *sentry.Log) *sentry.Log {
	if log == nil {
		return nil
	}
	log.Body = s.maskSecrets(log.Body)
	for k, v := range log.Attributes {
		if s.sensitive(k) {
			log.Attributes[k] = attribute.StringValue(redacted)
			continue
		}
		if v.Type() == attribute.STRING {
			log.Attributes[k] = attribute.StringValue(s.maskSecrets(v.AsString()))
		}
	}
	return log
}

// scrubMetric masks a measurement's dimensions on the way out. A metric name is
// never masked — it is chosen by the code, not taken from data.
func (s *scrubber) scrubMetric(metric *sentry.Metric) *sentry.Metric {
	if metric == nil {
		return nil
	}
	for k, v := range metric.Attributes {
		if s.sensitive(k) {
			metric.Attributes[k] = attribute.StringValue(redacted)
			continue
		}
		if v.Type() == attribute.STRING {
			metric.Attributes[k] = attribute.StringValue(s.maskSecrets(v.AsString()))
		}
	}
	return metric
}

func (s *scrubber) scrubHeaders(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if s.sensitive(k) {
			out[k] = redacted
			continue
		}
		out[k] = s.maskSecrets(v)
	}
	return out
}

func (s *scrubber) scrubStringMap(in map[string]string) map[string]string {
	return s.scrubHeaders(in)
}

// scrubQuery masks sensitive query parameters without decoding the URL (the
// raw string is what Sentry displays).
func (s *scrubber) scrubQuery(q string) string {
	if q == "" {
		return q
	}
	parts := strings.Split(q, "&")
	for i, part := range parts {
		key, _, found := strings.Cut(part, "=")
		if found && s.sensitive(key) {
			parts[i] = key + "=" + redacted
		}
	}
	return s.maskSecrets(strings.Join(parts, "&"))
}
