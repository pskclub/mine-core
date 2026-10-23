package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Header and query names are spelled with hyphens where the scrub list is
// spelled with underscores; X-Api-Key is the same secret as api_key.
func TestScrubber_matchesHyphenatedKeys(t *testing.T) {
	sc := newScrubber(nil, nil)
	for _, key := range []string{"X-Api-Key", "x-access-key", "Access-Token", "api-key", "Set-Cookie"} {
		assert.True(t, sc.sensitive(key), key)
	}
	assert.False(t, sc.sensitive("Content-Type"))
	assert.False(t, sc.sensitive("X-Request-Id"))
}

// Job parameters are a JSON document attached as one string; masking that
// string by value only would let {"password": "…"} through whole.
func TestScrubbedParams_masksByKey(t *testing.T) {
	out := scrubbedParams(json.RawMessage(`{"user":"alice","password":"hunter2","nested":{"api_key":"k"}}`))
	assert.Contains(t, out, `"user":"alice"`)
	assert.NotContains(t, out, "hunter2")
	assert.NotContains(t, out, `"k"`)
	assert.Equal(t, "", scrubbedParams(nil), "a job with no parameters has nothing to show")
}

// A secret the configuration knows is masked wherever it appears in free text,
// and the AI, chat and Sentry credentials are configuration too.
func TestSecretValuesOf_includesEveryCredentialTheConfigCarries(t *testing.T) {
	values := secretValuesOf(&ENVConfig{
		DBPassword: "db-pass-1", AIAPIKey: "sk-ai-key", AIEmbedAPIKey: "sk-embed",
		ChatSlackToken: "xoxb-slack", SentryDSN: "https://k@o.ingest.sentry.io/1",
	})
	for _, want := range []string{"db-pass-1", "sk-ai-key", "sk-embed", "xoxb-slack", "https://k@o.ingest.sentry.io/1"} {
		assert.Contains(t, values, want)
	}
}
