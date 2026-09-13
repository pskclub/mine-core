package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPusher_disabledFailsLoudly(t *testing.T) {
	p := NewNoopPusher()

	assert.False(t, p.Enabled())
	err := p.Send("tok", PushMessage{Title: "hi"})
	require.Error(t, err)
	assert.Equal(t, "PUSH_DISABLED", err.GetCode())
	assert.ErrorIs(t, err, ErrPushDisabled)

	_, merr := p.SendMulticast([]string{"a"}, PushMessage{})
	assert.ErrorIs(t, merr, ErrPushDisabled)
	_, serr := p.Subscribe("news", "a")
	assert.ErrorIs(t, serr, ErrPushDisabled)
}

func TestPusher_contextPusherIsNeverNil(t *testing.T) {
	app := newTestApp(t)
	ctx := app.NewContext(t.Context())

	require.NotNil(t, Pusher(ctx))
	assert.False(t, Pusher(ctx).Enabled())
	assert.ErrorIs(t, Pusher(ctx).Send("tok", PushMessage{}), ErrPushDisabled)

	assert.False(t, Pusher(context.Background()).Enabled(),
		"a context from nowhere gets the disabled pusher, not nil")
}

// notifyShipped is what a domain service looks like: a context.Context, not an
// IContext. Making that possible is why Pusher is a function.
func notifyShipped(ctx context.Context, token string) IError {
	return Pusher(ctx).Send(token, PushMessage{Title: "Order shipped"})
}

func TestPusher_reachableFromAPlainContext(t *testing.T) {
	p := NewMemoryPusher()
	app, err := NewApp(mustEnv(t, map[string]string{"ENV": "test"}), WithPusher(p))
	require.NoError(t, err)

	var plain context.Context = app.NewContext(t.Context())
	require.NoError(t, notifyShipped(plain, "tok-1"))

	sent := SentPushes(p)
	require.Len(t, sent, 1)
	assert.Equal(t, []string{"tok-1"}, sent[0].Tokens)
}

func TestPusher_memoryRecordsEveryShape(t *testing.T) {
	p := NewMemoryPusher()
	app, err := NewApp(mustEnv(t, map[string]string{"ENV": "test"}), WithPusher(p))
	require.NoError(t, err)
	ctx := app.NewContext(t.Context())

	require.NoError(t, Pusher(ctx).Send("tok-1", PushMessage{Title: "Order shipped"}))
	require.NoError(t, Pusher(ctx).SendToTopic("news", PushMessage{Title: "Headline"}))
	require.NoError(t, Pusher(ctx).SendToCondition("'news' in topics", PushMessage{Title: "Both"}))

	res, merr := Pusher(ctx).SendMulticast([]string{"a", "", "b"}, PushMessage{Title: "Many"})
	require.NoError(t, merr)
	assert.Equal(t, 2, res.SuccessCount, "an empty token is dropped, not sent")

	sent := SentPushes(p)
	require.Len(t, sent, 4)
	assert.Equal(t, []string{"tok-1"}, sent[0].Tokens)
	assert.Equal(t, "news", sent[1].Topic)
	assert.Equal(t, "'news' in topics", sent[2].Condition)
	assert.Equal(t, "Many", sent[3].Message.Title)

	ResetPushes(p)
	assert.Empty(t, SentPushes(p))
}

func TestPusher_memoryTracksTopicSubscriptions(t *testing.T) {
	p := NewMemoryPusher()

	_, err := p.Subscribe("news", "a", "b")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, PushTopicTokens(p, "news"))

	_, err = p.Unsubscribe("news", "a")
	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, PushTopicTokens(p, "news"))
}

func TestPusher_rejectsEmptyTargets(t *testing.T) {
	p := NewMemoryPusher()

	assert.Equal(t, "PUSH_NO_TOKEN", p.Send("", PushMessage{}).GetCode())
	assert.Equal(t, "PUSH_NO_TOPIC", p.SendToTopic("", PushMessage{}).GetCode())
	assert.Equal(t, "PUSH_NO_CONDITION", p.SendToCondition("", PushMessage{}).GetCode())
}

// A dead token is the one failure a caller must act on, so it has to be
// findable without knowing anything about the provider's error types.
func TestPusher_unregisteredTokensAreCollectable(t *testing.T) {
	b := &BatchResult{Results: []PushResult{
		{Token: "live", Success: true},
		{Token: "dead", Error: errors.Join(ErrPushUnregistered, errors.New("404"))},
		{Token: "flaky", Error: errors.New("temporary")},
	}}

	assert.Equal(t, []string{"dead"}, b.UnregisteredTokens(),
		"only a token the provider called dead should be deleted")
	assert.Empty(t, (*BatchResult)(nil).UnregisteredTokens())
}

// The shorthand fields must land on the right platform, and the per-platform
// config a caller built must be left exactly as it is.
func TestPusher_shorthandBuildsPlatformConfigs(t *testing.T) {
	p := &pusher{}
	ttl := 5 * time.Minute
	badge := 3

	m := p.message(PushMessage{
		Title: "Hi", Body: "there",
		Priority: PushPriorityHigh, TTL: ttl, Sound: "ping.caf",
		ChannelID: "orders", CollapseKey: "order-1", Badge: &badge,
	}, nil)

	require.NotNil(t, m.Android)
	assert.Equal(t, "high", m.Android.Priority)
	assert.Equal(t, "order-1", m.Android.CollapseKey)
	require.NotNil(t, m.Android.TTL)
	assert.Equal(t, ttl, *m.Android.TTL)
	assert.Equal(t, "orders", m.Android.Notification.ChannelID)

	require.NotNil(t, m.APNS)
	assert.Equal(t, "10", m.APNS.Headers["apns-priority"])
	assert.Equal(t, "ping.caf", m.APNS.Payload.Aps.Sound)
	require.NotNil(t, m.APNS.Payload.Aps.Badge)
	assert.Equal(t, 3, *m.APNS.Payload.Aps.Badge)
	assert.False(t, m.APNS.Payload.Aps.ContentAvailable, "this one has an alert to show")
}

// A message with only data is a silent one, and iOS drops it unless it is
// marked content-available.
func TestPusher_dataOnlyMessageIsMarkedSilent(t *testing.T) {
	p := &pusher{}

	m := p.message(PushMessage{Data: map[string]string{"sync": "1"}}, nil)

	assert.Nil(t, m.Notification)
	require.NotNil(t, m.APNS)
	assert.True(t, m.APNS.Payload.Aps.ContentAvailable)
}

func TestPusher_chunkRespectsTheProviderLimit(t *testing.T) {
	in := make([]string, 1200)
	for i := range in {
		in[i] = "t"
	}

	batches := chunkStrings(in, fcmMulticastLimit)
	require.Len(t, batches, 3)
	assert.Len(t, batches[0], 500)
	assert.Len(t, batches[1], 500)
	assert.Len(t, batches[2], 200)

	assert.Empty(t, chunkStrings(nil, 10))
}
