package slack

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capture is a stand-in Slack that records what it was asked and answers with
// whatever the test needs it to.
type capture struct {
	path   string
	auth   string
	body   map[string]any
	status int
	reply  string
	header map[string]string
}

func newServer(t *testing.T, c *capture) *httptest.Server {
	t.Helper()
	if c.reply == "" {
		c.reply = `{"ok":true,"channel":"C0123","ts":"1700000000.000100"}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.path = r.URL.Path
		c.auth = r.Header.Get("Authorization")

		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		if len(raw) > 0 {
			require.NoError(t, json.Unmarshal(raw, &c.body))
		}

		for k, v := range c.header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		if c.status != 0 {
			w.WriteHeader(c.status)
		}
		_, _ = w.Write([]byte(c.reply))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newChat(t *testing.T, c *capture, opts ...Option) core.IChat {
	t.Helper()
	srv := newServer(t, c)
	opts = append([]Option{WithToken("xoxb-test"), WithChannel("#ops"), WithBaseURL(srv.URL)}, opts...)

	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}
	built, err := NewConfig(cfg)
	require.Nil(t, err)
	return built
}

// A service with no token still has to boot. The failure belongs at the call,
// naming what is missing — not at startup, where it would stop a service that
// may never post anything.
func TestSlack_noTokenIsDisabledNotAnError(t *testing.T) {
	chat, err := NewConfig(Config{Channel: "#ops"})

	require.Nil(t, err)
	require.NotNil(t, chat)
	assert.False(t, chat.Enabled())

	_, sendErr := chat.Send(core.ChatMessage{Text: "hi"})
	assert.ErrorIs(t, sendErr, core.ErrChatDisabled)
}

func TestSlack_postsToTheWebAPI(t *testing.T) {
	c := &capture{}
	chat := newChat(t, c)

	res, err := chat.Send(core.ChatMessage{Text: "deploy finished"})
	require.Nil(t, err)

	assert.Equal(t, "/chat.postMessage", c.path)
	assert.Equal(t, "Bearer xoxb-test", c.auth,
		"the credential belongs in a header, which is masked everywhere, not in the URL")
	assert.Equal(t, "#ops", c.body["channel"], "an empty To falls back to the configured channel")
	assert.Equal(t, "deploy finished", c.body["text"])
	assert.NotContains(t, c.body, "attachments",
		"a plain info line should stay plain rather than growing a colour bar that says nothing")

	assert.Equal(t, "1700000000.000100", res.ID, "the ts is what a caller threads a reply onto")
	assert.Equal(t, "C0123", res.To)
}

func TestSlack_messageOverridesTheDefaultChannel(t *testing.T) {
	c := &capture{}
	chat := newChat(t, c)

	_, err := chat.Send(core.ChatMessage{To: "#billing", Text: "invoice failed"})
	require.Nil(t, err)
	assert.Equal(t, "#billing", c.body["channel"])
}

func TestSlack_noChannelAnywhereIsRejectedBeforeTheCall(t *testing.T) {
	c := &capture{}
	chat := newChat(t, c, WithChannel(""))

	_, err := chat.Send(core.ChatMessage{Text: "nowhere to go"})
	require.NotNil(t, err)
	assert.Equal(t, "CHAT_NO_CHANNEL", err.GetCode())
	assert.Empty(t, c.path, "a caller's mistake must not cost a round trip to Slack")
}

func TestSlack_rejectsAnEmptyMessage(t *testing.T) {
	c := &capture{}
	chat := newChat(t, c)

	_, err := chat.Send(core.ChatMessage{To: "#ops"})
	require.NotNil(t, err)
	assert.Equal(t, "CHAT_NO_BODY", err.GetCode())
	assert.Empty(t, c.path)
}

// A message with a level, a title or fields becomes an attachment — and keeps a
// top-level text, which is the notification preview and the fallback for
// anything that renders no attachments.
func TestSlack_richMessageBecomesAnAttachment(t *testing.T) {
	c := &capture{}
	chat := newChat(t, c)

	_, err := chat.Send(core.ChatMessage{
		Title: "Import failed",
		Text:  "3 of 40 rows rejected",
		Level: core.ChatError,
		Fields: []core.ChatField{
			{Name: "file", Value: "orders.csv", Inline: true},
			{Name: "job", Value: "import", Inline: true},
			{}, // empty fields are dropped rather than rendered blank
		},
		Links: []core.ChatLink{{Text: "run log", URL: "https://example.com/runs/7"}},
	})
	require.Nil(t, err)

	assert.Equal(t, "Import failed", c.body["text"], "the fallback must carry the message on its own")

	atts, ok := c.body["attachments"].([]any)
	require.True(t, ok)
	require.Len(t, atts, 1)
	att, ok := atts[0].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "#d0021b", att["color"], "an error must be red without every service picking its own")
	assert.Equal(t, "Import failed", att["title"])
	assert.Contains(t, att["text"], "3 of 40 rows rejected")
	assert.Contains(t, att["text"], "<https://example.com/runs/7|run log>")

	fields, ok := att["fields"].([]any)
	require.True(t, ok)
	require.Len(t, fields, 2, "a field with neither name nor value renders as an empty box")
	first, _ := fields[0].(map[string]any)
	assert.Equal(t, "file", first["title"])
	assert.Equal(t, "orders.csv", first["value"])
	assert.Equal(t, true, first["short"])
}

func TestSlack_levelColors(t *testing.T) {
	for _, tc := range []struct {
		level core.ChatLevel
		color string
	}{
		{core.ChatInfo, "#4a90d9"},
		{core.ChatSuccess, "#2eb886"},
		{core.ChatWarn, "#daa038"},
		{core.ChatError, "#d0021b"},
	} {
		assert.Equal(t, tc.color, levelColor(tc.level), "level %s", tc.level)
	}
}

// The body is left alone so that mrkdwn a caller wrote — *bold*, <@U123> — still
// renders. A link label is different: it sits inside <url|label>, where a stray
// separator would cut the link in half.
func TestSlack_escapesLinkLabelsButNotTheBody(t *testing.T) {
	c := &capture{}
	chat := newChat(t, c)

	_, err := chat.Send(core.ChatMessage{
		Text:  "*done* — ping <@U123>",
		Links: []core.ChatLink{{Text: "a|b>c", URL: "https://example.com"}},
	})
	require.Nil(t, err)

	text, _ := c.body["text"].(string)
	assert.Contains(t, text, "*done* — ping <@U123>", "escaping the body would print the markup instead of rendering it")
	assert.Contains(t, text, "https://example.com|a｜b&gt;c>")
}

func TestSlack_linkWithoutALabel(t *testing.T) {
	c := &capture{}
	chat := newChat(t, c)

	_, err := chat.Send(core.ChatMessage{
		Text:  "see",
		Links: []core.ChatLink{{URL: "https://example.com/x"}, {Text: "dropped", URL: "  "}},
	})
	require.Nil(t, err)

	text, _ := c.body["text"].(string)
	assert.Contains(t, text, "<https://example.com/x>")
	assert.NotContains(t, text, "dropped", "a link with no URL is nothing to render")
}

func TestSlack_threadReply(t *testing.T) {
	c := &capture{}
	chat := newChat(t, c)

	_, err := chat.Send(core.ChatMessage{Text: "done", ThreadID: "1700000000.000100"})
	require.Nil(t, err)
	assert.Equal(t, "1700000000.000100", c.body["thread_ts"])
}

// Native is the escape hatch for Block Kit. It is used as it is — the portable
// fields are not merged into it — but the channel and thread are still filled in,
// so threading does not stop working the moment a caller reaches for blocks.
func TestSlack_nativePayloadWins(t *testing.T) {
	c := &capture{}
	chat := newChat(t, c)

	_, err := chat.Send(core.ChatMessage{
		Title:    "ignored",
		Text:     "ignored",
		Level:    core.ChatError,
		ThreadID: "1700000000.000100",
		Native: map[string]any{
			"blocks": []any{map[string]any{"type": "divider"}},
			"text":   "block fallback",
		},
	})
	require.Nil(t, err)

	assert.Equal(t, "block fallback", c.body["text"])
	assert.Contains(t, c.body, "blocks")
	assert.NotContains(t, c.body, "attachments", "Native is used as it is, not merged with the portable fields")
	assert.Equal(t, "#ops", c.body["channel"], "the configured channel still fills in")
	assert.Equal(t, "1700000000.000100", c.body["thread_ts"])
}

// A channel written into the payload is the one the caller meant, even when the
// provider has a default.
func TestSlack_nativeChannelIsNotOverridden(t *testing.T) {
	c := &capture{}
	chat := newChat(t, c)

	_, err := chat.Send(core.ChatMessage{
		To:     "#ignored",
		Native: map[string]any{"channel": "C9999", "text": "hi"},
	})
	require.Nil(t, err)
	assert.Equal(t, "C9999", c.body["channel"])
}

// Slack answers a refusal with 200 and ok:false, so a driver that trusted the
// status code would report every rejected message as sent.
func TestSlack_okFalseIsAFailure(t *testing.T) {
	for _, tc := range []struct {
		slackError string
		code       string
		status     int
	}{
		{"not_in_channel", "CHAT_NOT_IN_CHANNEL", http.StatusForbidden},
		{"channel_not_found", "CHAT_CHANNEL_NOT_FOUND", http.StatusNotFound},
		{"invalid_auth", "CHAT_UNAUTHORIZED", http.StatusUnauthorized},
		{"missing_scope", "CHAT_MISSING_SCOPE", http.StatusForbidden},
		{"is_archived", "CHAT_CHANNEL_ARCHIVED", http.StatusGone},
		{"msg_too_long", "CHAT_MESSAGE_TOO_LONG", http.StatusBadRequest},
		{"invalid_blocks", "CHAT_INVALID_MESSAGE", http.StatusBadRequest},
		{"ratelimited", "CHAT_RATE_LIMITED", http.StatusTooManyRequests},
		{"something_new", "CHAT_SEND_FAILED", http.StatusBadGateway},
	} {
		c := &capture{reply: `{"ok":false,"error":"` + tc.slackError + `"}`}
		chat := newChat(t, c)

		_, err := chat.Send(core.ChatMessage{Text: "hi"})
		require.NotNil(t, err, "%s must not be reported as sent", tc.slackError)
		assert.Equal(t, tc.code, err.GetCode(), "slack error %s", tc.slackError)
		assert.Equal(t, tc.status, err.GetStatus(), "slack error %s", tc.slackError)
	}
}

// The message for the failure every new integration hits has to say what to do
// about it, because "not_in_channel" does not.
func TestSlack_notInChannelSaysHowToFixIt(t *testing.T) {
	c := &capture{reply: `{"ok":false,"error":"not_in_channel"}`}
	chat := newChat(t, c)

	_, err := chat.Send(core.ChatMessage{Text: "hi"})
	require.NotNil(t, err)
	assert.Contains(t, err.GetMessage(), "invite")
}

func TestSlack_rateLimitKeepsRetryAfter(t *testing.T) {
	c := &capture{
		status: http.StatusTooManyRequests,
		reply:  `{"ok":false,"error":"ratelimited"}`,
		header: map[string]string{"Retry-After": "30"},
	}
	chat := newChat(t, c)

	_, err := chat.Send(core.ChatMessage{Text: "hi"})
	require.NotNil(t, err)
	assert.Equal(t, "CHAT_RATE_LIMITED", err.GetCode())
	assert.Contains(t, err.GetMessage(), "30",
		"a caller backing off should not have to guess a wait Slack already told us")
}

func TestSlack_transportFailureIsAGatewayError(t *testing.T) {
	chat, buildErr := NewConfig(Config{Token: "xoxb-test", Channel: "#ops", BaseURL: "http://127.0.0.1:1"})
	require.Nil(t, buildErr)

	_, err := chat.Send(core.ChatMessage{Text: "hi"})
	require.NotNil(t, err)
	assert.Equal(t, "CHAT_SEND_FAILED", err.GetCode())
	assert.Equal(t, http.StatusBadGateway, err.GetStatus())
}

func TestSlack_pingChecksTheTokenWithoutPosting(t *testing.T) {
	c := &capture{reply: `{"ok":true,"user":"bot"}`}
	chat := newChat(t, c)

	require.Nil(t, chat.Ping())
	assert.Equal(t, "/auth.test", c.path, "a readiness probe must not post a message to a channel")
}

func TestSlack_pingFailsOnABadToken(t *testing.T) {
	c := &capture{reply: `{"ok":false,"error":"invalid_auth"}`}
	chat := newChat(t, c)

	err := chat.Ping()
	require.NotNil(t, err)
	assert.Equal(t, "CHAT_UNAUTHORIZED", err.GetCode())
}

func TestSlack_metadata(t *testing.T) {
	c := &capture{}
	chat := newChat(t, c)

	assert.Equal(t, "slack", chat.Provider())
	assert.True(t, chat.Enabled())
	assert.Nil(t, chat.Close())
}

func TestSlack_configDefaults(t *testing.T) {
	built, err := NewConfig(Config{Token: " xoxb-test "})
	require.Nil(t, err)

	impl, ok := built.(*chat)
	require.True(t, ok)
	assert.Equal(t, "xoxb-test", impl.cfg.Token, "a token pasted with whitespace still has to work")
	assert.Equal(t, DefaultBaseURL, impl.cfg.BaseURL)
	assert.Equal(t, DefaultTimeout, impl.cfg.Timeout)
}

func TestSlack_newReadsTheEnvironment(t *testing.T) {
	t.Setenv("APP_CHAT_SLACK_TOKEN", "xoxb-from-env")
	t.Setenv("APP_CHAT_SLACK_CHANNEL", "#from-env")
	t.Setenv("APP_CHAT_SLACK_TIMEOUT", "5")

	env, envErr := core.NewEnvPath(t.TempDir())
	require.Nil(t, envErr)

	built, err := New(env)
	require.Nil(t, err)

	impl, ok := built.(*chat)
	require.True(t, ok)
	assert.Equal(t, "xoxb-from-env", impl.cfg.Token)
	assert.Equal(t, "#from-env", impl.cfg.Channel)
	assert.Equal(t, 5*time.Second, impl.cfg.Timeout)
}
