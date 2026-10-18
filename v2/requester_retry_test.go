package core

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// HTTP_RETRY_COUNT retries the failures a second attempt can change, and only
// for a request that is safe to repeat: a GET is, a POST without an
// Idempotency-Key is not — repeating it could create a second order.
func TestRequester_retriesIdempotentRequestsOnly(t *testing.T) {
	var gets, posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if gets.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		case http.MethodPost:
			posts.Add(1)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	env := mustEnv(t, map[string]string{
		"ENV": "test", "SERVICE": "retry",
		"HTTP_RETRY_COUNT": "2", "HTTP_RETRY_WAIT_MS": "1", "HTTP_RETRY_MAX_WAIT_MS": "2",
	})
	r := NewRequester(env, NewLogger(env))

	resp, err := r.Send(r.R(), http.MethodGet, srv.URL)
	require.Nil(t, err, "the second attempt succeeded")
	assert.Equal(t, http.StatusOK, resp.StatusCode())
	assert.Equal(t, int32(2), gets.Load())

	_, err = r.Send(r.R(), http.MethodPost, srv.URL)
	require.NotNil(t, err)
	assert.Equal(t, int32(1), posts.Load(), "a POST is sent once")

	_, err = r.Send(r.R().SetHeader("Idempotency-Key", "abc"), http.MethodPost, srv.URL)
	require.NotNil(t, err)
	assert.Equal(t, int32(4), posts.Load(), "with a key the POST is safe to repeat: 1 + 2 retries")
}

func TestParseRetryAfter(t *testing.T) {
	d, ok := parseRetryAfter("3")
	assert.True(t, ok)
	assert.Equal(t, 3, int(d.Seconds()))
	_, ok = parseRetryAfter("")
	assert.False(t, ok)
	_, ok = parseRetryAfter("soon")
	assert.False(t, ok)
	d, ok = parseRetryAfter("Wed, 21 Oct 2015 07:28:00 GMT")
	assert.True(t, ok, "an HTTP date is the other legal form")
	assert.Equal(t, 0, int(d), "a date in the past means now")
}
