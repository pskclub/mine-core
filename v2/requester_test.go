package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRequesterCtx(t *testing.T) IContext {
	t.Helper()
	app := newTestApp(t)
	return app.NewContext(context.Background())
}

func TestRequester_send_typedResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"u1","name":"alice"}`))
	}))
	defer srv.Close()

	type user struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	r := Requester(newRequesterCtx(t))

	var out user
	resp, err := r.Send(r.R().SetResult(&out), http.MethodGet, srv.URL)
	require.Nil(t, err)
	assert.True(t, resp.IsSuccess())
	assert.Equal(t, "u1", out.ID)
	assert.Equal(t, "alice", out.Name)
}

func TestRequester_send_non2xxBecomesIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"USER_NOT_FOUND","message":"nope"}`))
	}))
	defer srv.Close()

	r := Requester(newRequesterCtx(t))
	_, err := r.Send(r.R(), http.MethodGet, srv.URL)

	require.NotNil(t, err) // err is core.IError
	assert.Equal(t, http.StatusNotFound, err.GetStatus())
	assert.Equal(t, "USER_NOT_FOUND", err.GetCode(), "remote error code reused")
}

func TestRequester_send_transportErrorBecomesIError(t *testing.T) {
	r := Requester(newRequesterCtx(t))
	// nothing is listening on this port → transport error
	_, err := r.Send(r.R(), http.MethodGet, "http://127.0.0.1:0")
	require.NotNil(t, err)
	assert.Equal(t, "NETWORK_ERROR", err.GetCode())
	assert.Equal(t, http.StatusInternalServerError, err.GetStatus())
}

// TestRequester_fullRestyViaR proves the full go-resty API is reachable when
// building a request: custom headers and query params reach the server.
func TestRequester_fullRestyViaR(t *testing.T) {
	var gotToken, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Token")
		gotQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var out struct {
		OK bool `json:"ok"`
	}
	r := Requester(newRequesterCtx(t))
	req := r.R().
		SetHeader("X-Token", "secret").
		SetQueryParam("q", "search").
		SetResult(&out)

	resp, err := r.Send(req, http.MethodGet, srv.URL)
	require.Nil(t, err)
	assert.True(t, resp.IsSuccess())
	assert.Equal(t, "secret", gotToken, "custom header sent")
	assert.Equal(t, "search", gotQuery, "query param sent")
	assert.True(t, out.OK, "typed result parsed by resty")
}

func TestRequester_customClientReused(t *testing.T) {
	rc := resty.New().SetTimeout(2*time.Second).SetHeader("X-App", "billing")
	r := NewRequesterWithClient(rc)
	require.NotNil(t, r.Resty())
	assert.Same(t, r.Resty(), r.WithContext(context.Background()).Resty(),
		"client is reused, not recreated")
}

// Requester(ctx) must hand back the client the App was built with — a service
// that configured retries or a base URL through WithRequester would otherwise
// silently make its calls on a different one.
func TestRequester_fromContextUsesAppClient(t *testing.T) {
	rc := resty.New().SetHeader("X-App", "billing")
	custom := NewRequesterWithClient(rc)
	app := newTestApp(t, WithRequester(custom))

	ctx := app.NewContext(context.Background())

	assert.Same(t, rc, Requester(ctx).Resty())
	// and through a derived context, which is all a deeper function may hold
	assert.Same(t, rc, Requester(context.WithValue(ctx, struct{}{}, 1)).Resty())
	// and through WithContext, which rebuilds the context from a bare one
	assert.Same(t, rc, Requester(ctx.WithContext(context.Background())).Resty())
}

// A context that never came from an App still answers with a usable client
// rather than nil, so a call site never has to nil-check.
func TestRequester_withoutAppFallsBackToASharedClient(t *testing.T) {
	r := Requester(context.Background())
	require.NotNil(t, r)
	assert.Same(t, r.Resty(), Requester(context.Background()).Resty(),
		"the fallback is built once, not per call")
}

// The request carries the caller's context, so a cancelled request is cancelled
// at the transport — this is what the removed ctx.Requester() did by binding.
func TestRequester_bindsTheCallersContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	app := newTestApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	r := Requester(app.NewContext(ctx))
	_, err := r.Send(r.R(), http.MethodGet, srv.URL)

	require.NotNil(t, err)
	assert.Equal(t, "NETWORK_ERROR", err.GetCode())
}

// SetError is resty's counterpart to SetResult: the upstream's error body,
// decoded into a type of your own. Send still returns an IError, so a handler
// can pass it straight up while the caller branches on whatever the upstream
// says that a code and a message cannot carry.
func TestRequester_setErrorDecodesTheUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":"CARD_DECLINED","message":"insufficient funds",` +
			`"decline_code":"nsf","retryable":false}`))
	}))
	defer srv.Close()

	type chargeError struct {
		Code        string `json:"code"`
		Message     string `json:"message"`
		DeclineCode string `json:"decline_code"`
		Retryable   bool   `json:"retryable"`
	}
	var out struct {
		ID string `json:"id"`
	}
	var fail chargeError

	r := Requester(newRequesterCtx(t))
	resp, err := r.Send(
		r.R().SetBody(map[string]any{"amount": 100}).SetResult(&out).SetError(&fail),
		http.MethodPost, srv.URL,
	)

	require.NotNil(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, err.GetStatus())
	assert.Equal(t, "CARD_DECLINED", err.GetCode(), "remote code is still reused")

	assert.Equal(t, "nsf", fail.DeclineCode, "the fields no IError carries")
	assert.False(t, fail.Retryable)
	assert.Equal(t, &fail, resp.Error(), "resty also hands it back on the response")
	assert.Empty(t, out.ID, "SetResult is left untouched on a failure")
}
