package core

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

// ErrChatDisabled is wrapped by the error every operation returns when no chat
// provider is configured.
//
// Chat does not degrade quietly, for the same reason mail does not: an alert
// that was silently dropped is an incident nobody was told about, and the log
// says nothing either — the whole point of the message was to be the thing
// somebody reads.
var ErrChatDisabled = errors.New("chat: not configured")

// ChatLevel is how serious a message is. A provider renders it as whatever it
// has — a colour bar on Slack, an embed colour on Discord — so the same alert
// reads the same way wherever it is sent.
type ChatLevel uint8

const (
	// ChatInfo is the default: something happened worth saying.
	ChatInfo ChatLevel = iota
	// ChatSuccess is something that finished the way it was meant to.
	ChatSuccess
	// ChatWarn is something that still worked but should not be ignored.
	ChatWarn
	// ChatError is something that failed.
	ChatError
)

// String is the level's name, for a log line or a test assertion.
func (l ChatLevel) String() string {
	switch l {
	case ChatSuccess:
		return "success"
	case ChatWarn:
		return "warn"
	case ChatError:
		return "error"
	default:
		return "info"
	}
}

// ChatField is one labelled value in a message — "order", "12345". Fields are
// what makes an alert scannable: the same keys in the same order every time,
// rather than a sentence that has to be read.
type ChatField struct {
	Name  string
	Value string
	// Inline asks the provider to put this field beside the next one rather than
	// on its own row. It is a request, not a guarantee: a provider that has no
	// such layout ignores it.
	Inline bool
}

// ChatLink is a labelled URL — the run that failed, the dashboard to look at.
type ChatLink struct {
	Text string
	URL  string
}

// ChatMessage is a message to post.
//
// The fields above Native are the subset every provider can render, so a service
// that sticks to them can be pointed at another platform by changing
// configuration. Anything richer — Slack Block Kit, Discord embeds, LINE Flex —
// goes in Native.
//
// There is deliberately no unified builder for those three. They are genuinely
// different models of a message, and a common denominator over them would be
// worse to write against than any of the three, while still needing an escape
// hatch for everything it failed to cover.
type ChatMessage struct {
	// To is the destination: a channel ("#ops" or "C0123ABCD" on Slack), a user,
	// a group. Empty uses whatever the provider was configured with.
	To string

	// Title is the headline; Text is the body. Either may be empty, but a message
	// with no body at all is rejected before it is sent.
	Title string
	Text  string

	Level  ChatLevel
	Fields []ChatField
	Links  []ChatLink

	// ThreadID replies inside an existing thread. It is the ID a previous Send
	// returned — how a job posts "started" once and then hangs its progress off
	// that message instead of filling the channel.
	ThreadID string

	// Native is the provider's own request body, used as it is. It wins over
	// every field above except To and ThreadID, which are still filled in when
	// the payload does not already carry them.
	//
	// A message carrying Native is written against one provider and is not
	// portable — which is the trade being made, and the reason it is a separate
	// field rather than something that leaks into the rest of the struct.
	Native map[string]any
}

// ChatResult is what came back from a successful Send.
type ChatResult struct {
	// ID identifies the posted message, when the provider returns one. Pass it
	// back as ChatMessage.ThreadID to reply to it.
	ID string
	// To is the destination that was actually used, which is the configured
	// default when the message named none.
	To string
	// ThreadID is the thread the message landed in, empty when it started one.
	ThreadID string
}

// IChat posts messages to a chat platform — Slack, Discord, LINE.
//
// core.Chat(ctx) is never nil: a service with no chat configuration gets a
// disabled provider whose every call fails with CHAT_DISABLED.
type IChat interface {
	// Send posts one message, bounded by the handle's context.
	Send(msg ChatMessage) (ChatResult, IError)
	// Ping checks the credentials without posting anything, for a readiness
	// probe. A provider with no way to ask returns nil.
	Ping() IError
	// Enabled reports whether this is a real provider.
	Enabled() bool
	// Provider names the platform ("slack"), for a capability listing.
	Provider() string
	// WithContext returns a handle bound to a different context.
	WithContext(ctx context.Context) IChat
	// Close releases whatever the provider holds. Owned by App.Shutdown.
	Close() IError
}

// Chat returns the application's default chat provider bound to ctx.
//
// It is a function rather than a method on IContext, for the same reason
// core.Mailer and core.Requester are: posting a message is not a capability of
// the request — it is something code does, with the request's deadline and trace
// attached. Taking only a context.Context is what lets a service that knows
// nothing about this framework post one:
//
//	func (s *DeployService) Announce(ctx context.Context, d Deploy) error {
//	    _, err := core.Chat(ctx).Send(core.ChatMessage{
//	        Title: "Deploy finished", Level: core.ChatSuccess,
//	        Fields: []core.ChatField{{Name: "version", Value: d.Version}},
//	    })
//	    return err
//	}
//
// A context from nowhere — context.Background() in a script or an early test —
// gets the disabled provider rather than nil, so a call site never has to
// nil-check. Every call on it fails with CHAT_DISABLED.
func Chat(ctx context.Context) IChat { return Chats(ctx, defaultConn) }

// Chats returns a named chat provider bound to ctx.
//
// Providers are named the way SQL connections and caches are, rather than there
// being one of them, because one service routinely posts to more than one place:
// alerts to an internal Slack, replies to customers on LINE. An unregistered
// name gives the disabled provider rather than nil.
func Chats(ctx context.Context, name string) IChat {
	if app := appFrom(ctx); app != nil {
		if c, ok := app.chats[name]; ok && c != nil {
			return c.WithContext(ctx)
		}
	}
	return noopChat{}
}

// ValidateChatMessage rejects a message no provider could send.
//
// It is exported because providers live outside this package — a driver calls it
// first so that a bug in a caller is reported the same way whichever platform is
// configured, and before a request is made rather than as somebody's API error.
func ValidateChatMessage(msg ChatMessage) IError {
	if msg.Title == "" && msg.Text == "" && len(msg.Fields) == 0 && len(msg.Native) == 0 {
		return New(400, "CHAT_NO_BODY", "chat: the message has no body")
	}
	return nil
}

// ChatDisabledError is the error a disabled provider returns. Drivers use it so
// that a provider left unconfigured fails identically to none being registered.
func ChatDisabledError() IError { return chatDisabled() }

// ---------------------------------------------------------------------------
// Memory provider (tests)
// ---------------------------------------------------------------------------

// memoryChat records messages instead of posting them.
type memoryChat struct {
	ctx context.Context

	// the recorder is shared by pointer, so a handle from WithContext records
	// into the same list the test holds
	rec *chatRecorder
}

type chatRecorder struct {
	mu   sync.Mutex
	sent []ChatMessage
	n    int
}

var _ IChat = (*memoryChat)(nil)

// NewMemoryChat returns a provider that records messages instead of posting
// them, so a test can assert on what a service would have said:
//
//	c := core.NewMemoryChat()
//	app, _ := core.NewApp(env, core.WithChat("default", c))
//	...
//	sent := core.SentChatMessages(c)
//	require.Len(t, sent, 1)
//	assert.Equal(t, core.ChatError, sent[0].Level)
//
// It runs the same validation a real provider does, so a message the platform
// would have rejected fails the test rather than passing it.
func NewMemoryChat() IChat {
	return &memoryChat{ctx: context.Background(), rec: &chatRecorder{}}
}

// SentChatMessages returns what a memory provider has recorded, in order. It
// returns nil for any other provider.
func SentChatMessages(c IChat) []ChatMessage {
	mc, ok := c.(*memoryChat)
	if !ok {
		return nil
	}
	mc.rec.mu.Lock()
	defer mc.rec.mu.Unlock()
	return append([]ChatMessage(nil), mc.rec.sent...)
}

// ResetChatMessages drops everything a memory provider has recorded.
func ResetChatMessages(c IChat) {
	if mc, ok := c.(*memoryChat); ok {
		mc.rec.mu.Lock()
		defer mc.rec.mu.Unlock()
		mc.rec.sent = nil
		mc.rec.n = 0
	}
}

func (c *memoryChat) Send(msg ChatMessage) (ChatResult, IError) {
	if err := ValidateChatMessage(msg); err != nil {
		return ChatResult{}, err
	}

	c.rec.mu.Lock()
	defer c.rec.mu.Unlock()
	c.rec.sent = append(c.rec.sent, msg)
	c.rec.n++

	// a distinct id per message, so a test covering "post, then reply in the
	// thread" exercises the same two-step the real provider makes it do
	return ChatResult{
		ID:       "memory-" + strconv.Itoa(c.rec.n),
		To:       msg.To,
		ThreadID: msg.ThreadID,
	}, nil
}

func (c *memoryChat) Ping() IError     { return nil }
func (c *memoryChat) Enabled() bool    { return true }
func (c *memoryChat) Provider() string { return "memory" }
func (c *memoryChat) Close() IError    { return nil }

func (c *memoryChat) WithContext(ctx context.Context) IChat {
	cp := *c
	cp.ctx = ctx
	return &cp
}

// ---------------------------------------------------------------------------
// Disabled provider
// ---------------------------------------------------------------------------

type noopChat struct{}

var _ IChat = noopChat{}

// NewNoopChat returns a provider that refuses every operation. It is what a
// service with no chat configuration gets, so the failure is a clear error
// instead of a nil dereference.
func NewNoopChat() IChat { return noopChat{} }

func (noopChat) Send(ChatMessage) (ChatResult, IError) { return ChatResult{}, chatDisabled() }
func (noopChat) Ping() IError                          { return chatDisabled() }
func (noopChat) Enabled() bool                         { return false }
func (noopChat) Provider() string                      { return "" }
func (n noopChat) WithContext(context.Context) IChat   { return n }
func (noopChat) Close() IError                         { return nil }

func chatDisabled() *Error {
	return &Error{
		Status:  503,
		Code:    "CHAT_DISABLED",
		Message: "chat: no provider is configured",
		cause:   ErrChatDisabled,
	}
}
