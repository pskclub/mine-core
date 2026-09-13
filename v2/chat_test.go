package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChat_disabledFailsLoudly(t *testing.T) {
	c := NewNoopChat()

	assert.False(t, c.Enabled())

	_, err := c.Send(ChatMessage{To: "#ops", Text: "hi"})
	require.Error(t, err)
	assert.Equal(t, "CHAT_DISABLED", err.GetCode(),
		"an unconfigured provider must name itself, not fail as a generic 500")
	assert.ErrorIs(t, err, ErrChatDisabled)
	assert.ErrorIs(t, c.Ping(), ErrChatDisabled)
}

func TestChat_contextChatIsNeverNil(t *testing.T) {
	app := newTestApp(t)
	ctx := app.NewContext(t.Context())

	require.NotNil(t, Chat(ctx))
	assert.False(t, Chat(ctx).Enabled())

	_, err := Chat(ctx).Send(ChatMessage{Text: "x"})
	assert.ErrorIs(t, err, ErrChatDisabled,
		"a service with no chat configuration must get an error, never a nil dereference")
}

// announce is what a domain service looks like: it takes a context.Context, not
// an IContext, so it does not have to import the framework to post. Making that
// possible is the whole reason Chat is a function.
func announce(ctx context.Context, text string) IError {
	_, err := Chat(ctx).Send(ChatMessage{Text: text})
	return err
}

func TestChat_reachableFromAPlainContext(t *testing.T) {
	c := NewMemoryChat()
	app := newTestApp(t, WithChat("default", c))

	// a context that has travelled through code knowing nothing about core still
	// carries the App, so the provider it finds is the configured one
	var plain context.Context = app.NewContext(t.Context())
	plain = context.WithValue(plain, struct{ k string }{"unrelated"}, 1)

	require.NoError(t, announce(plain, "deploy finished"))
	require.Len(t, SentChatMessages(c), 1)
}

// A context from nowhere gets the disabled provider, not nil — a script or an
// early test must not panic on the call.
func TestChat_contextWithNoAppIsDisabled(t *testing.T) {
	got := Chat(context.Background())

	require.NotNil(t, got)
	assert.False(t, got.Enabled())
	assert.ErrorIs(t, announce(context.Background(), "hi"), ErrChatDisabled)
}

// Providers are named because one service posts to more than one place. An
// unregistered name must degrade to the disabled provider rather than reaching
// for the default one — posting a customer's reply into the ops channel because
// of a typo is worse than not posting it.
func TestChat_namedProviders(t *testing.T) {
	ops, line := NewMemoryChat(), NewMemoryChat()
	app := newTestApp(t, WithChat("default", ops), WithChat("line", line))
	ctx := app.NewContext(t.Context())

	_, err := Chat(ctx).Send(ChatMessage{Text: "alert"})
	require.NoError(t, err)
	_, err = Chats(ctx, "line").Send(ChatMessage{Text: "reply"})
	require.NoError(t, err)

	require.Len(t, SentChatMessages(ops), 1)
	assert.Equal(t, "alert", SentChatMessages(ops)[0].Text)
	require.Len(t, SentChatMessages(line), 1)
	assert.Equal(t, "reply", SentChatMessages(line)[0].Text)

	_, err = Chats(ctx, "typo").Send(ChatMessage{Text: "nowhere"})
	assert.ErrorIs(t, err, ErrChatDisabled)
	assert.Len(t, SentChatMessages(ops), 1, "a misspelled name must not fall back to the default provider")
}

// A message with nothing in it is a caller's bug. It must fail the same way in a
// test as it would against the real platform, or the memory provider is teaching
// people to write messages that get rejected in production.
func TestChat_rejectsEmptyMessages(t *testing.T) {
	c := NewMemoryChat()

	_, err := c.Send(ChatMessage{To: "#ops"})
	require.Error(t, err)
	assert.Equal(t, "CHAT_NO_BODY", err.GetCode())
	assert.Empty(t, SentChatMessages(c))

	for _, msg := range []ChatMessage{
		{Title: "only a title"},
		{Text: "only text"},
		{Fields: []ChatField{{Name: "only", Value: "a field"}}},
		{Native: map[string]any{"blocks": []any{}}},
	} {
		_, err := c.Send(msg)
		assert.NoError(t, err, "any one of the four body carriers is enough: %+v", msg)
	}
}

// The id a Send returns is what a caller passes back as ThreadID, so the memory
// provider has to hand out a distinct one — otherwise a test covering "post,
// then reply under it" passes without exercising the two-step at all.
func TestChat_memoryReturnsDistinctIDs(t *testing.T) {
	c := NewMemoryChat()

	first, err := c.Send(ChatMessage{To: "#ops", Text: "started"})
	require.NoError(t, err)
	require.NotEmpty(t, first.ID)

	second, err := c.Send(ChatMessage{To: "#ops", Text: "done", ThreadID: first.ID})
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, first.ID, second.ThreadID)

	assert.Equal(t, "#ops", first.To)
}

func TestChat_memoryReset(t *testing.T) {
	c := NewMemoryChat()
	_, err := c.Send(ChatMessage{Text: "one"})
	require.NoError(t, err)

	ResetChatMessages(c)
	assert.Empty(t, SentChatMessages(c))

	// the recorder is shared by pointer, so a handle bound to another context
	// records into the list the test is holding
	bound := c.WithContext(context.Background())
	_, err = bound.Send(ChatMessage{Text: "two"})
	require.NoError(t, err)
	assert.Len(t, SentChatMessages(c), 1)
}

// SentChatMessages is only meaningful for the memory provider; asked about any
// other it returns nil rather than pretending nothing was sent.
func TestChat_sentMessagesOnlyForMemory(t *testing.T) {
	assert.Nil(t, SentChatMessages(NewNoopChat()))
}

func TestChat_capabilityIsListed(t *testing.T) {
	app := newTestApp(t, WithChat("default", NewMemoryChat()), WithChat("off", NewNoopChat()))

	var chats []Capability
	for _, c := range app.Capabilities() {
		if c.Kind == "chat" {
			chats = append(chats, c)
		}
	}

	require.Len(t, chats, 2)
	assert.Equal(t, "default", chats[0].Name)
	assert.True(t, chats[0].Enabled)
	assert.Equal(t, "memory", chats[0].Detail, "the panel has to say which platform is on the other end")
	assert.Equal(t, "off", chats[1].Name)
	assert.False(t, chats[1].Enabled,
		"a registered-but-unconfigured provider is the whole reason the list exists")
}

// Only a working provider is probed. A service with no chat must not have a
// readiness check that can never pass.
func TestChat_healthChecksOnlyEnabledProviders(t *testing.T) {
	app := newTestApp(t, WithChat("default", NewMemoryChat()), WithChat("off", NewNoopChat()))

	names := make([]string, 0, 2)
	for _, c := range AppHealthChecks(app) {
		names = append(names, c.Name)
	}

	assert.Contains(t, names, "chat")
	assert.NotContains(t, names, "chat.off")
}

func TestChat_appAccessorIsNeverNil(t *testing.T) {
	app := newTestApp(t, WithChat("default", NewMemoryChat()))

	assert.True(t, app.Chat().Enabled())
	assert.Equal(t, "memory", app.Chat("default").Provider())
	assert.False(t, app.Chat("missing").Enabled())

	empty := newTestApp(t)
	require.NotNil(t, empty.Chat())
	assert.False(t, empty.Chat().Enabled())
}

func TestChatLevel_String(t *testing.T) {
	assert.Equal(t, "info", ChatInfo.String())
	assert.Equal(t, "success", ChatSuccess.String())
	assert.Equal(t, "warn", ChatWarn.String())
	assert.Equal(t, "error", ChatError.String())
}
