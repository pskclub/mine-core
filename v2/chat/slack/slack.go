// Package slack posts messages to Slack through core.IChat.
//
// Wiring, at startup:
//
//	chat, err := slack.New(env)
//	if err != nil { log.Fatal(err) }
//	app, _ := core.NewApp(env, core.WithChat("default", chat))
//
// and then, from a handler or a job:
//
//	_, err := core.Chat(ctx).Send(core.ChatMessage{
//	    Title:  "Import failed",
//	    Level:  core.ChatError,
//	    Fields: []core.ChatField{{Name: "file", Value: name, Inline: true}},
//	})
//
// # Bot tokens only
//
// The driver authenticates with a bot token (xoxb-…) and talks to the Web API.
// It deliberately does not support incoming webhooks, whose credential is a path
// segment of the URL — and this framework logs the URL of every outgoing call,
// puts its host and path in the log's headline, and files it as a Sentry
// breadcrumb. Supporting that mode would mean either printing the credential in
// three places or building URL-path redaction to accommodate one convenience.
//
// A bot token travels in an Authorization header instead, which is already
// masked everywhere, and buys what a webhook cannot do anyway: any channel, a
// message id to thread replies onto, auth.test for the readiness probe, and
// error codes specific enough to act on.
package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// DefaultBaseURL is Slack's Web API root.
const DefaultBaseURL = "https://slack.com/api"

// DefaultTimeout bounds one post. Seconds rather than the requester's thirty:
// posting a message is a fast call, and an alert that is a request's worth of
// latency late is one nobody wanted to wait for.
const DefaultTimeout = 10 * time.Second

// Config is what the driver needs. New fills it from IENV; the options override
// individual fields for a test or a second workspace in the same process.
type Config struct {
	// Token is a bot token (xoxb-…) with chat:write.
	Token string
	// Channel is the destination for a message that names none.
	Channel string
	// BaseURL points at another endpoint — a test server, a proxy. Empty uses
	// DefaultBaseURL.
	BaseURL string
	Timeout time.Duration
}

// Option overrides one piece of configuration.
type Option func(*Config)

// WithToken sets the bot token.
func WithToken(token string) Option { return func(c *Config) { c.Token = token } }

// WithChannel sets the destination for messages that name none.
func WithChannel(channel string) Option { return func(c *Config) { c.Channel = channel } }

// WithBaseURL points the driver at another endpoint — a test server, a proxy.
func WithBaseURL(url string) Option { return func(c *Config) { c.BaseURL = url } }

// WithTimeout bounds one post (default DefaultTimeout).
func WithTimeout(d time.Duration) Option { return func(c *Config) { c.Timeout = d } }

// New builds a Slack provider from CHAT_SLACK_* configuration.
//
// It returns the disabled provider — not an error — when no token is set, so a
// service that does not post to Slack still starts, and one that does fails at
// the call with CHAT_DISABLED naming what is missing.
func New(env core.IENV, opts ...Option) (core.IChat, core.IError) {
	cfg := Config{}
	if env != nil {
		c := env.Config()
		cfg = Config{
			Token:   c.ChatSlackToken,
			Channel: c.ChatSlackChannel,
			Timeout: time.Duration(c.ChatSlackTimeout) * time.Second,
		}
	}
	for _, o := range opts {
		o(&cfg)
	}
	return NewConfig(cfg)
}

// NewConfig builds a provider from an explicit configuration, for a service that
// loads its own or posts to a second workspace alongside the configured one.
func NewConfig(cfg Config) (core.IChat, core.IError) {
	cfg.Token = strings.TrimSpace(cfg.Token)
	if cfg.Token == "" {
		return core.NewNoopChat(), nil
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	return &chat{ctx: context.Background(), cfg: cfg}, nil
}

type chat struct {
	ctx context.Context
	cfg Config
}

var _ core.IChat = (*chat)(nil)

func (c *chat) Provider() string { return "slack" }

func (c *chat) Enabled() bool { return true }

func (c *chat) Close() core.IError { return nil }

func (c *chat) WithContext(ctx context.Context) core.IChat {
	cp := *c
	cp.ctx = ctx
	return &cp
}

// Send posts one message. The HTTP call goes through core.Requester so it
// inherits the application's client, its call log and its Sentry breadcrumbs —
// a post that started failing shows up in the service's own log rather than only
// in the error a caller decided what to do with.
func (c *chat) Send(msg core.ChatMessage) (core.ChatResult, core.IError) {
	if err := core.ValidateChatMessage(msg); err != nil {
		return core.ChatResult{}, err
	}

	channel := strings.TrimSpace(msg.To)
	if channel == "" {
		channel = c.cfg.Channel
	}
	if channel == "" && !hasNativeChannel(msg) {
		return core.ChatResult{}, core.New(400, "CHAT_NO_CHANNEL",
			"chat: no channel (set ChatMessage.To or CHAT_SLACK_CHANNEL)")
	}

	var out postResponse
	if err := c.call("chat.postMessage", c.payload(msg, channel), &out); err != nil {
		return core.ChatResult{}, err
	}

	res := core.ChatResult{ID: out.TS, To: out.Channel, ThreadID: msg.ThreadID}
	if res.To == "" {
		res.To = channel
	}
	return res, nil
}

// Ping asks Slack whether the token is still good, without posting anything.
func (c *chat) Ping() core.IError {
	var out struct{}
	return c.call("auth.test", map[string]any{}, &out)
}

type postResponse struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// slackResponse is the envelope every Web API method answers with. Slack reports
// a refusal as 200 with ok:false, so the status code alone never says whether a
// message was posted.
type slackResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// call runs one Web API method and unmarshals the result into out.
func (c *chat) call(method string, body map[string]any, out any) core.IError {
	ctx, cancel := context.WithTimeout(c.ctx, c.cfg.Timeout)
	defer cancel()

	r := core.Requester(ctx)
	req := r.R().
		SetHeader("Authorization", "Bearer "+c.cfg.Token).
		SetHeader("Content-Type", "application/json; charset=utf-8").
		SetBody(body).
		SetResult(out)

	// SetError is not used: an envelope carrying ok:false arrives with a 200, so
	// the refusal has to be read out of the success path anyway
	resp, sendErr := r.Send(req, http.MethodPost, c.cfg.BaseURL+"/"+method)
	if resp != nil && resp.StatusCode() == http.StatusTooManyRequests {
		return rateLimited(resp.Header().Get("Retry-After"))
	}
	if sendErr != nil {
		return core.Wrapf(sendErr, "chat: slack %s", method).
			WithStatus(http.StatusBadGateway).
			WithCode("CHAT_SEND_FAILED")
	}

	var env slackResponse
	// the envelope is read off the raw body rather than through SetResult, so
	// that out stays the method's own shape instead of every result type having
	// to embed ok/error
	if err := json.Unmarshal(resp.Body(), &env); err != nil {
		return core.Wrapf(err, "chat: slack %s: decode", method).
			WithStatus(http.StatusBadGateway).
			WithCode("CHAT_SEND_FAILED")
	}
	if !env.OK {
		return apiError(method, env.Error)
	}
	return nil
}

// hasNativeChannel reports whether a Native payload names its own destination,
// which is the one case where an empty To and an empty default are fine.
func hasNativeChannel(msg core.ChatMessage) bool {
	if len(msg.Native) == 0 {
		return false
	}
	ch, ok := msg.Native["channel"].(string)
	return ok && strings.TrimSpace(ch) != ""
}

// payload builds the chat.postMessage body.
func (c *chat) payload(msg core.ChatMessage, channel string) map[string]any {
	if len(msg.Native) > 0 {
		p := make(map[string]any, len(msg.Native)+2)
		for k, v := range msg.Native {
			p[k] = v
		}
		// filled in, never overridden: a caller who wrote a channel into the
		// payload meant that channel
		if _, ok := p["channel"]; !ok && channel != "" {
			p["channel"] = channel
		}
		if _, ok := p["thread_ts"]; !ok && msg.ThreadID != "" {
			p["thread_ts"] = msg.ThreadID
		}
		return p
	}

	p := map[string]any{"channel": channel}
	if msg.ThreadID != "" {
		p["thread_ts"] = msg.ThreadID
	}

	body := strings.TrimSpace(msg.Text)
	if links := renderLinks(msg.Links); links != "" {
		if body != "" {
			body += "\n"
		}
		body += links
	}

	// A plain line stays plain. An info-level message with no title and no
	// fields is a sentence somebody wanted in a channel, and wrapping it in an
	// attachment only adds a colour bar and an indent that say nothing.
	if msg.Title == "" && len(msg.Fields) == 0 && msg.Level == core.ChatInfo {
		p["text"] = body
		return p
	}

	att := map[string]any{
		"color": levelColor(msg.Level),
		// mrkdwn is off inside an attachment unless the fields are named
		"mrkdwn_in": []string{"text", "fields"},
	}
	if msg.Title != "" {
		att["title"] = msg.Title
	}
	if body != "" {
		att["text"] = body
	}
	if fields := renderFields(msg.Fields); len(fields) > 0 {
		att["fields"] = fields
	}

	// text alongside attachments is the notification preview and the fallback for
	// anything that does not render attachments — a mobile banner, a screen
	// reader. Without it Slack shows "This content can't be displayed".
	p["text"] = fallbackText(msg, body)
	p["attachments"] = []any{att}
	return p
}

// fallbackText is the one line that has to carry the message on its own. It is
// never empty: ValidateChatMessage has already rejected a message with nothing
// in it.
func fallbackText(msg core.ChatMessage, body string) string {
	if msg.Title != "" {
		return msg.Title
	}
	if body != "" {
		return body
	}
	for _, f := range msg.Fields {
		if f.Name != "" || f.Value != "" {
			return strings.TrimSpace(f.Name + " " + f.Value)
		}
	}
	return ""
}

// renderFields maps the portable fields onto an attachment's.
func renderFields(in []core.ChatField) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, f := range in {
		if f.Name == "" && f.Value == "" {
			continue
		}
		out = append(out, map[string]any{
			"title": f.Name,
			"value": f.Value,
			"short": f.Inline,
		})
	}
	return out
}

// renderLinks writes the links as one mrkdwn line.
//
// Only the label is escaped, not the message body: a caller writing "*done*" or
// "<@U123>" means the mrkdwn, and escaping the body would print the markup
// instead of rendering it. A label is different — it sits inside <url|label>,
// where a stray "|" or ">" would cut the link in half.
func renderLinks(in []core.ChatLink) string {
	parts := make([]string, 0, len(in))
	for _, l := range in {
		url := strings.TrimSpace(l.URL)
		if url == "" {
			continue
		}
		if l.Text == "" {
			parts = append(parts, "<"+url+">")
			continue
		}
		parts = append(parts, "<"+url+"|"+escapeLabel(l.Text)+">")
	}
	return strings.Join(parts, "  ·  ")
}

var labelEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "|", "｜")

func escapeLabel(s string) string { return labelEscaper.Replace(s) }

// levelColor is the bar down the side of the attachment. Slack's own green and
// red, so an alert looks like the rest of the workspace rather than like a
// service that picked its own palette.
func levelColor(l core.ChatLevel) string {
	switch l {
	case core.ChatSuccess:
		return "#2eb886"
	case core.ChatWarn:
		return "#daa038"
	case core.ChatError:
		return "#d0021b"
	default:
		return "#4a90d9"
	}
}

// rateLimited reports a 429, keeping Slack's own wait so a caller backing off
// does not have to guess it.
func rateLimited(retryAfter string) core.IError {
	err := core.New(http.StatusTooManyRequests, "CHAT_RATE_LIMITED", "chat: slack is rate limiting this app")
	// ok:false carries no wait, only the 429 header does — so the field is absent
	// rather than empty when there is nothing to report
	if retryAfter = strings.TrimSpace(retryAfter); retryAfter == "" {
		return err
	}
	return err.
		WithMessage("chat: slack is rate limiting this app (retry after " + retryAfter + "s)").
		WithFields(map[string]any{"retry_after": retryAfter})
}

// apiError maps Slack's error string onto a framework error, keeping the ones a
// caller or an operator can act on apart from the ones they cannot.
func apiError(method, code string) core.IError {
	switch code {
	case "invalid_auth", "not_authed", "token_revoked", "account_inactive":
		return core.Newf(http.StatusUnauthorized, "CHAT_UNAUTHORIZED",
			"chat: slack rejected the bot token (%s)", code)

	case "missing_scope", "not_allowed_token_type":
		return core.Newf(http.StatusForbidden, "CHAT_MISSING_SCOPE",
			"chat: the bot token is missing a scope for %s (%s) — chat:write is the one posting needs", method, code)

	case "channel_not_found":
		return core.New(http.StatusNotFound, "CHAT_CHANNEL_NOT_FOUND",
			"chat: no such slack channel — a private channel also reads as missing until the bot is invited")

	// the failure every new integration hits: the app is installed, the token is
	// good, and the bot was never invited to the channel it is posting to
	case "not_in_channel":
		return core.New(http.StatusForbidden, "CHAT_NOT_IN_CHANNEL",
			"chat: the bot is not in that slack channel — invite it with /invite @your-app")

	case "is_archived":
		return core.New(http.StatusGone, "CHAT_CHANNEL_ARCHIVED", "chat: the slack channel is archived")

	case "msg_too_long":
		return core.New(http.StatusBadRequest, "CHAT_MESSAGE_TOO_LONG", "chat: the message is over slack's length limit")

	case "rate_limited", "ratelimited":
		return rateLimited("")

	case "invalid_blocks", "invalid_blocks_format", "invalid_attachments", "no_text", "invalid_arguments":
		return core.Newf(http.StatusBadRequest, "CHAT_INVALID_MESSAGE",
			"chat: slack rejected the message payload (%s)", code)

	default:
		return core.Newf(http.StatusBadGateway, "CHAT_SEND_FAILED",
			"chat: slack refused %s (%s)", method, code)
	}
}
