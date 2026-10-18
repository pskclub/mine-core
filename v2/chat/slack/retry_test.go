package slack

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "github.com/pskclub/mine-core/v2"
)

// A short Retry-After is waited for and the post tried again; one longer than
// the cap is returned as the 429, since holding a request open for a minute to
// post a Slack message is the wrong trade.
func TestSlack_retriesAShortRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error":"ratelimited"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C0123","ts":"1700000000.000100"}`))
	}))
	t.Cleanup(srv.Close)

	chat, err := NewConfig(Config{Token: "xoxb-test", Channel: "#ops", BaseURL: srv.URL})
	require.Nil(t, err)

	started := time.Now()
	_, sendErr := chat.Send(core.ChatMessage{Text: "hi"})
	require.Nil(t, sendErr, "the retry after Slack's own wait went through")
	assert.Equal(t, int32(2), calls.Load())
	assert.GreaterOrEqual(t, time.Since(started), 900*time.Millisecond, "it waited what Retry-After said")
}

func TestSlack_doesNotWaitForALongRateLimit(t *testing.T) {
	c := &capture{
		status: http.StatusTooManyRequests,
		reply:  `{"ok":false,"error":"ratelimited"}`,
		header: map[string]string{"Retry-After": "120"},
	}
	chat := newChat(t, c, WithRetryMaxWait(5*time.Second))

	started := time.Now()
	_, err := chat.Send(core.ChatMessage{Text: "hi"})
	require.NotNil(t, err)
	assert.Equal(t, "CHAT_RATE_LIMITED", err.GetCode())
	wait, ok := core.RetryAfterOf(err)
	assert.True(t, ok, "the wait is on the error for a caller that can afford it")
	assert.Equal(t, 120*time.Second, wait)
	assert.Less(t, time.Since(started), time.Second, "returned at once")
}

func TestSlack_retriesCanBeTurnedOff(t *testing.T) {
	var calls atomic.Int32
	c := &capture{status: http.StatusTooManyRequests, reply: `{"ok":false,"error":"ratelimited"}`,
		header: map[string]string{"Retry-After": "1"}}
	srv := newServer(t, c)
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		srv.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(counting.Close)

	chat, err := NewConfig(Config{Token: "xoxb-test", Channel: "#ops", BaseURL: counting.URL})
	require.Nil(t, err)
	_ = chat
	off, err := NewConfig(Config{Token: "xoxb-test", Channel: "#ops", BaseURL: counting.URL, Retries: intPtr(0)})
	require.Nil(t, err)
	_, sendErr := off.Send(core.ChatMessage{Text: "hi"})
	require.NotNil(t, sendErr)
	assert.Equal(t, int32(1), calls.Load(), "zero retries: one call")
}

func intPtr(n int) *int { return &n }
