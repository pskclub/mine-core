package core

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectTaps is a tap that remembers what it was given.
type collectTaps struct {
	mu      sync.Mutex
	entries []LogEntry
	ctxs    []context.Context
}

func (c *collectTaps) tap(ctx context.Context, e LogEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
	c.ctxs = append(c.ctxs, ctx)
}

func (c *collectTaps) find(msg string) (LogEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if e.Message == msg {
			return e, true
		}
	}
	return LogEntry{}, false
}

func TestWithLogTap_seesEveryLineAndItsFields(t *testing.T) {
	tap := &collectTaps{}
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "tap"})
	app, err := NewApp(env, WithLogTap(tap.tap))
	require.Nil(t, err)

	app.Log().Info("order placed", "order_id", "o-1", "total", 250)

	entry, ok := tap.find("order placed")
	require.True(t, ok, "the tap must see what the logger writes")
	assert.Equal(t, "INFO", entry.Level)
	assert.Equal(t, "o-1", entry.Attrs["order_id"])
	assert.EqualValues(t, 250, entry.Attrs["total"])
	assert.Contains(t, entry.Source, "logger_tap_test.go:",
		"a tapped line has to name the same place the written line does")
}

// The attributes a bound logger pins — the request id above all — are on the
// handler, not on the record. A tap that only read the record would report every
// line as belonging to no request, which is exactly the field the trace groups
// by.
func TestWithLogTap_seesPinnedAttributes(t *testing.T) {
	tap := &collectTaps{}
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "tap"})
	app, err := NewApp(env, WithLogTap(tap.tap))
	require.Nil(t, err)

	app.Log().With("module", "billing").Info("charged")

	entry, ok := tap.find("charged")
	require.True(t, ok)
	assert.Equal(t, "billing", entry.Attrs["module"])
}

// The tap is installed by wrapping the slog handler rather than the ILogger,
// because a decorated ILogger is no longer the framework's own type and
// ctx.Log() silently stops binding the request id to the line. This is that
// regression, asserted.
func TestWithLogTap_doesNotBreakContextBinding(t *testing.T) {
	var out bytes.Buffer
	tap := &collectTaps{}
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "tap"})
	app, err := NewApp(env, WithLogger(NewLoggerTo(&out, env)), WithLogTap(tap.tap))
	require.Nil(t, err)

	ctx := app.NewContext(WithRequestID(context.Background(), "req-99"))
	ctx.Log().Info("bound line")

	var line map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(out.Bytes()), &line))
	assert.Equal(t, "req-99", line["request_id"],
		"the written line must still carry the request id")

	entry, ok := tap.find("bound line")
	require.True(t, ok)
	assert.Equal(t, "req-99", entry.Attrs["request_id"])

	tap.mu.Lock()
	defer tap.mu.Unlock()
	require.NotEmpty(t, tap.ctxs)
	assert.Equal(t, "req-99", RequestID(tap.ctxs[len(tap.ctxs)-1]),
		"the tap is handed the context the line was written under, which is what it groups by")
}

// A tap must not stop the line reaching stdout: it observes, it does not
// intercept.
func TestWithLogTap_stillWritesTheLine(t *testing.T) {
	var out bytes.Buffer
	env := mustEnv(t, map[string]string{"ENV": "test", "SERVICE": "tap"})
	app, err := NewApp(env,
		WithLogger(NewLoggerTo(&out, env)),
		WithLogTap(func(context.Context, LogEntry) {}))
	require.Nil(t, err)

	app.Log().Info("still written")
	assert.Contains(t, out.String(), "still written")
}

func TestRequestID(t *testing.T) {
	assert.Empty(t, RequestID(context.Background()))
	// a nil context is what a caller outside any request has, and asking for the
	// id there must answer "none" rather than panic
	assert.Empty(t, RequestID(context.Context(nil)))
	assert.Equal(t, "abc", RequestID(WithRequestID(context.Background(), "abc")))
	assert.Empty(t, RequestID(WithRequestID(context.Background(), "")),
		"an empty id is not carried, so nothing downstream correlates on it")
}
