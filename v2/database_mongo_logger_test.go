package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	// aliased: this package's tests already have a type called address
	mongoaddr "go.mongodb.org/mongo-driver/v2/mongo/address"
)

// mongoCommandDoc builds the command document the driver would hand the monitor.
func mongoCommandDoc(t *testing.T, d bson.D) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(d)
	require.NoError(t, err)
	return raw
}

// runCommand drives one command through a monitor, the way the driver would.
func runCommand(t *testing.T, monitor *event.CommandMonitor, name string, cmd bson.D, took time.Duration, failure string) {
	t.Helper()
	require.NotNil(t, monitor, "the monitor is silent, so nothing can be observed")

	const requestID = int64(42)
	monitor.Started(context.Background(), &event.CommandStartedEvent{
		Command:      mongoCommandDoc(t, cmd),
		DatabaseName: "testdb",
		CommandName:  name,
		RequestID:    requestID,
	})
	if failure != "" {
		monitor.Failed(context.Background(), &event.CommandFailedEvent{
			CommandFinishedEvent: event.CommandFinishedEvent{
				Duration:    took,
				CommandName: name,
				RequestID:   requestID,
			},
			// driver v2 carries the failure as an error rather than a string
			Failure: errors.New(failure),
		})
		return
	}
	monitor.Succeeded(context.Background(), &event.CommandSucceededEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{
			Duration:    took,
			CommandName: name,
			RequestID:   requestID,
		},
	})
}

func mongoLines(lines []capturedLine) []capturedLine {
	out := make([]capturedLine, 0)
	for _, l := range lines {
		if strings.Contains(l.msg, "mongo") {
			out = append(out, l)
		}
	}
	return out
}

func fieldValue(line capturedLine, key string) any {
	for i := 0; i+1 < len(line.args); i += 2 {
		if line.args[i] == key {
			return line.args[i+1]
		}
	}
	return nil
}

// LOG_LEVEL is the knob everyone reaches for; it must reach Mongo too.
func TestMongoLogLevel_followsLogLevel(t *testing.T) {
	assert.Equal(t, MongoLogInfo, mongoLogLevelFrom(mustEnv(t, map[string]string{"LOG_LEVEL": "debug"})),
		"debug means show me everything, and that includes the queries")
	assert.Equal(t, MongoLogWarn, mongoLogLevelFrom(mustEnv(t, map[string]string{"LOG_LEVEL": "info"})))
	assert.Equal(t, MongoLogError, mongoLogLevelFrom(mustEnv(t, map[string]string{"LOG_LEVEL": "error"})))
	assert.Equal(t, MongoLogWarn, mongoLogLevelFrom(nil))
}

func TestMongoLogLevel_theExplicitKeyWins(t *testing.T) {
	env := mustEnv(t, map[string]string{"LOG_LEVEL": "debug", "DB_MONGO_LOG_LEVEL": "silent"})
	assert.Equal(t, MongoLogSilent, mongoLogLevelFrom(env),
		"reading one flow at debug must not force every query into the log")
}

func TestParseMongoLogLevel(t *testing.T) {
	for _, spelling := range []string{"silent", "off", "none", "false", "SILENT"} {
		level, ok := parseMongoLogLevel(spelling)
		assert.True(t, ok, spelling)
		assert.Equal(t, MongoLogSilent, level, spelling)
	}
	for _, spelling := range []string{"info", "debug", "all", "true"} {
		level, ok := parseMongoLogLevel(spelling)
		assert.True(t, ok, spelling)
		assert.Equal(t, MongoLogInfo, level, spelling)
	}
	_, ok := parseMongoLogLevel("nonsense")
	assert.False(t, ok, "an unreadable value falls back rather than being obeyed")
}

func TestMongoMonitor_logsEveryCommandAtInfo(t *testing.T) {
	log, lines := newCapture()
	monitor := NewMongoMonitor(log, MongoLogInfo, time.Second)

	runCommand(t, monitor, "find", bson.D{
		{Key: "find", Value: "users"},
		{Key: "filter", Value: bson.D{{Key: "status", Value: "active"}}},
	}, 5*time.Millisecond, "")

	got := mongoLines(*lines)
	require.Len(t, got, 1)
	assert.Equal(t, "debug", got[0].level, "an ordinary query is debug, not info noise")
	assert.Equal(t, "mongo command", got[0].msg)
	assert.Equal(t, "find", fieldValue(got[0], "command"))
	assert.Equal(t, "users", fieldValue(got[0], "collection"))
	assert.Equal(t, int64(5), fieldValue(got[0], "duration_ms"))
	assert.Contains(t, fieldValue(got[0], "query"), "status", "the filter is in the line")
}

func TestMongoMonitor_quietAtWarn(t *testing.T) {
	log, lines := newCapture()
	monitor := NewMongoMonitor(log, MongoLogWarn, time.Second)

	runCommand(t, monitor, "find", bson.D{{Key: "find", Value: "users"}}, 5*time.Millisecond, "")

	assert.Empty(t, mongoLines(*lines), "a fast command says nothing at warn")
}

func TestMongoMonitor_slowCommandIsAWarning(t *testing.T) {
	log, lines := newCapture()
	monitor := NewMongoMonitor(log, MongoLogWarn, 50*time.Millisecond)

	runCommand(t, monitor, "find", bson.D{{Key: "find", Value: "users"}}, 500*time.Millisecond, "")

	got := mongoLines(*lines)
	require.Len(t, got, 1)
	assert.Equal(t, "warn", got[0].level)
	assert.Equal(t, "slow mongo command", got[0].msg)
	assert.Equal(t, int64(50), fieldValue(got[0], "threshold_ms"), "the line says what it was measured against")
	assert.Equal(t, int64(500), fieldValue(got[0], "duration_ms"))
}

func TestMongoMonitor_failureIsAnError(t *testing.T) {
	log, lines := newCapture()
	monitor := NewMongoMonitor(log, MongoLogError, time.Second)

	runCommand(t, monitor, "insert", bson.D{{Key: "insert", Value: "users"}},
		time.Millisecond, "E11000 duplicate key error")

	got := mongoLines(*lines)
	require.Len(t, got, 1)
	assert.Equal(t, "error", got[0].level)
	assert.Equal(t, "mongo command failed", got[0].msg)
	// driver v2 reports the failure as an error, and the field keeps it one
	// rather than flattening it to a string
	failure, ok := fieldValue(got[0], "err").(error)
	require.True(t, ok, "the err field carries the driver's error")
	assert.Contains(t, failure.Error(), "duplicate key")
	assert.Equal(t, "users", fieldValue(got[0], "collection"))
}

func TestMongoMonitor_silentBuildsNoMonitorAtAll(t *testing.T) {
	log, _ := newCapture()
	assert.Nil(t, NewMongoMonitor(log, MongoLogSilent, time.Second),
		"silent must cost nothing, not record events and drop them")
}

func TestMongoMonitor_skipsTheDriversHousekeeping(t *testing.T) {
	// a readiness probe every few seconds would otherwise bury the queries
	log, lines := newCapture()
	monitor := NewMongoMonitor(log, MongoLogInfo, time.Second)

	for _, name := range []string{"ping", "hello", "isMaster", "endSessions", "saslStart"} {
		runCommand(t, monitor, name, bson.D{{Key: name, Value: 1}}, time.Millisecond, "")
	}

	assert.Empty(t, mongoLines(*lines))
}

func TestMongoMonitor_forgetsCommandsItHasLogged(t *testing.T) {
	// the started-command map must not grow with the traffic
	log, _ := newCapture()
	m := newMongoMonitor(log, MongoLogInfo, time.Second)
	monitor := m.commandMonitor()

	for range 100 {
		runCommand(t, monitor, "find", bson.D{{Key: "find", Value: "users"}}, time.Millisecond, "")
	}
	runCommand(t, monitor, "insert", bson.D{{Key: "insert", Value: "users"}}, time.Millisecond, "boom")

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Empty(t, m.started, "every command was taken off the map when it finished")
}

func TestMongoMonitor_survivesAReplyWithNoStart(t *testing.T) {
	// a command that began before the monitor existed, or was evicted
	log, lines := newCapture()
	monitor := NewMongoMonitor(log, MongoLogInfo, time.Second)

	assert.NotPanics(t, func() {
		monitor.Succeeded(context.Background(), &event.CommandSucceededEvent{
			CommandFinishedEvent: event.CommandFinishedEvent{RequestID: 999, CommandName: "find"},
		})
		monitor.Failed(context.Background(), &event.CommandFailedEvent{
			CommandFinishedEvent: event.CommandFinishedEvent{RequestID: 999, CommandName: "find"},
		})
	})
	assert.Empty(t, mongoLines(*lines))
}

func TestMongoMonitor_boundsTheInFlightMap(t *testing.T) {
	log, _ := newCapture()
	m := newMongoMonitor(log, MongoLogInfo, time.Second)
	monitor := m.commandMonitor()

	// replies that never arrive: without a cap this is an unbounded map
	for i := range maxMongoInFlight + 500 {
		monitor.Started(context.Background(), &event.CommandStartedEvent{
			Command:     mongoCommandDoc(t, bson.D{{Key: "find", Value: "users"}}),
			CommandName: "find",
			RequestID:   int64(i),
		})
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.LessOrEqual(t, len(m.started), maxMongoInFlight)
}

func TestMongoMonitor_truncatesAHugeCommand(t *testing.T) {
	log, lines := newCapture()
	monitor := NewMongoMonitor(log, MongoLogInfo, time.Second)

	runCommand(t, monitor, "insert", bson.D{
		{Key: "insert", Value: "users"},
		{Key: "documents", Value: bson.D{{Key: "blob", Value: strings.Repeat("x", 8000)}}},
	}, time.Millisecond, "")

	got := mongoLines(*lines)
	require.Len(t, got, 1)
	query, _ := fieldValue(got[0], "query").(string)
	assert.LessOrEqual(t, len(query), maxMongoCommandBytes+len("…(truncated)"))
	assert.Contains(t, query, "truncated", "the line says it was cut rather than looking complete")
}

// --- pool ----------------------------------------------------------------

func TestMongoPoolMonitor_logsACheckoutFailure(t *testing.T) {
	// the failure that is invisible in the command log: the time went into
	// waiting for a connection, so no command was ever slow
	log, lines := newCapture()
	monitor := NewMongoPoolMonitor(log, MongoLogWarn)
	require.NotNil(t, monitor)

	monitor.Event(&event.PoolEvent{
		Type:     event.ConnectionCheckOutFailed,
		Address:  "mongo-1:27017",
		Reason:   event.ReasonTimedOut,
		Duration: 3 * time.Second,
		Error:    errors.New("timed out while checking out a connection"),
	})

	got := mongoLines(*lines)
	require.Len(t, got, 1)
	assert.Equal(t, "error", got[0].level)
	assert.Equal(t, "mongo connection checkout failed", got[0].msg)
	assert.Equal(t, "mongo-1:27017", fieldValue(got[0], "address"))
	assert.Equal(t, int64(3000), fieldValue(got[0], "waited_ms"), "how long the caller waited is the whole story")
	assert.Equal(t, event.ReasonTimedOut, fieldValue(got[0], "reason"))
}

func TestMongoPoolMonitor_logsAClearedPool(t *testing.T) {
	log, lines := newCapture()
	monitor := NewMongoPoolMonitor(log, MongoLogWarn)

	monitor.Event(&event.PoolEvent{
		Type:         event.ConnectionPoolCleared,
		Address:      "mongo-1:27017",
		Interruption: true,
	})

	got := mongoLines(*lines)
	require.Len(t, got, 1)
	assert.Equal(t, "warn", got[0].level)
	assert.Equal(t, "mongo connection pool cleared", got[0].msg)
	assert.Equal(t, true, fieldValue(got[0], "interrupted"),
		"in-use connections were killed, so operations in flight failed rather than finishing")
}

func TestMongoPoolMonitor_ignoresTheOrdinaryTraffic(t *testing.T) {
	// a pair of these per operation would bury the command log it is meant to
	// complement
	log, lines := newCapture()
	monitor := NewMongoPoolMonitor(log, MongoLogInfo)

	for _, kind := range []string{
		event.ConnectionPoolCreated, event.ConnectionPoolReady, event.ConnectionCreated,
		event.ConnectionReady, event.ConnectionCheckOutStarted, event.ConnectionCheckedOut,
		event.ConnectionCheckedIn, event.ConnectionClosed, event.ConnectionPoolClosed,
	} {
		monitor.Event(&event.PoolEvent{Type: kind, Address: "mongo-1:27017"})
	}

	assert.Empty(t, mongoLines(*lines))
}

func TestMongoPoolMonitor_atErrorOnlyTheFailure(t *testing.T) {
	log, lines := newCapture()
	monitor := NewMongoPoolMonitor(log, MongoLogError)

	monitor.Event(&event.PoolEvent{Type: event.ConnectionPoolCleared, Address: "mongo-1:27017"})
	assert.Empty(t, mongoLines(*lines), "a cleared pool is a warning, and warnings are off")

	monitor.Event(&event.PoolEvent{Type: event.ConnectionCheckOutFailed, Address: "mongo-1:27017"})
	assert.Len(t, mongoLines(*lines), 1, "a failed checkout is a failure, and failures are never off")
}

func TestMongoPoolMonitor_silentBuildsNoMonitorAtAll(t *testing.T) {
	log, _ := newCapture()
	assert.Nil(t, NewMongoPoolMonitor(log, MongoLogSilent))
}

// --- topology ------------------------------------------------------------

func serverChange(from, to string) *event.ServerDescriptionChangedEvent {
	return &event.ServerDescriptionChangedEvent{
		Address:             "mongo-1:27017",
		PreviousDescription: event.ServerDescription{Addr: "mongo-1:27017", Kind: from},
		NewDescription:      event.ServerDescription{Addr: "mongo-1:27017", Kind: to},
	}
}

func TestMongoServerMonitor_onlyAChangeOfKindIsNews(t *testing.T) {
	// the driver re-describes a server on every heartbeat; most of those events
	// differ only in a timestamp
	log, lines := newCapture()
	monitor := NewMongoServerMonitor(log, MongoLogWarn)
	require.NotNil(t, monitor)

	monitor.ServerDescriptionChanged(serverChange("RSPrimary", "RSPrimary"))
	assert.Empty(t, mongoLines(*lines), "the same server described again is not an event")

	monitor.ServerDescriptionChanged(serverChange("RSPrimary", mongoServerUnknown))
	got := mongoLines(*lines)
	require.Len(t, got, 1)
	assert.Equal(t, "warn", got[0].level)
	assert.Equal(t, "mongo server lost", got[0].msg)
	assert.Equal(t, "mongo-1:27017", fieldValue(got[0], "address"))
	assert.Equal(t, "RSPrimary", fieldValue(got[0], "from"))
}

func TestMongoServerMonitor_comingBackIsNotAWarning(t *testing.T) {
	log, lines := newCapture()
	monitor := NewMongoServerMonitor(log, MongoLogWarn)

	// the zero value is where every server starts, so this is also what boot
	// looks like
	monitor.ServerDescriptionChanged(serverChange("", "RSSecondary"))

	got := mongoLines(*lines)
	require.Len(t, got, 1)
	assert.Equal(t, "info", got[0].level)
	assert.Equal(t, "mongo server changed", got[0].msg)
	assert.Equal(t, mongoServerUnknown, fieldValue(got[0], "from"), "a server not yet heard from is Unknown, not blank")
	assert.Equal(t, "RSSecondary", fieldValue(got[0], "to"))
}

// topology builds the description of a set whose primary is at primary, or one
// with no primary when it is empty.
func topology(primary string) event.TopologyDescription {
	td := event.TopologyDescription{
		SetName: "rs0",
		Servers: []event.ServerDescription{
			{Addr: "mongo-1:27017", Kind: "RSSecondary"},
			{Addr: "mongo-2:27017", Kind: "RSSecondary"},
		},
	}
	if primary != "" {
		td.Servers = append(td.Servers, event.ServerDescription{
			Addr: mongoaddr.Address(primary), Kind: "RSPrimary",
		})
	}
	return td
}

func TestMongoServerMonitor_followsThePrimary(t *testing.T) {
	log, lines := newCapture()
	monitor := NewMongoServerMonitor(log, MongoLogWarn)

	changed := func(from, to string) {
		monitor.TopologyDescriptionChanged(&event.TopologyDescriptionChangedEvent{
			PreviousDescription: topology(from),
			NewDescription:      topology(to),
		})
	}

	changed("mongo-1:27017", "mongo-1:27017")
	assert.Empty(t, mongoLines(*lines), "the same primary described again is not an event")

	changed("", "mongo-1:27017")
	got := mongoLines(*lines)
	require.Len(t, got, 1)
	assert.Equal(t, "info", got[0].level)
	assert.Equal(t, "mongo primary elected", got[0].msg)
	assert.Equal(t, "rs0", fieldValue(got[0], "replica_set"))

	changed("mongo-1:27017", "")
	got = mongoLines(*lines)
	require.Len(t, got, 2)
	assert.Equal(t, "warn", got[1].level)
	assert.Equal(t, "mongo primary lost", got[1].msg,
		"no primary is what a write is about to fail against")

	changed("mongo-1:27017", "mongo-2:27017")
	got = mongoLines(*lines)
	require.Len(t, got, 3)
	assert.Equal(t, "warn", got[2].level)
	assert.Equal(t, "mongo primary changed", got[2].msg)
	assert.Equal(t, "mongo-1:27017", fieldValue(got[2], "from"))
	assert.Equal(t, "mongo-2:27017", fieldValue(got[2], "to"))
}

func TestMongoServerMonitor_offBelowWarn(t *testing.T) {
	log, _ := newCapture()
	assert.Nil(t, NewMongoServerMonitor(log, MongoLogSilent))
	assert.Nil(t, NewMongoServerMonitor(log, MongoLogError),
		"a topology change is not a failure, so error-only means off")
}

func TestMongoCommandCollection(t *testing.T) {
	assert.Equal(t, "users", mongoCommandCollection(&event.CommandStartedEvent{
		Command: mongoCommandDoc(t, bson.D{{Key: "find", Value: "users"}}),
	}))
	// getMore names a cursor id first, not a collection
	assert.Empty(t, mongoCommandCollection(&event.CommandStartedEvent{
		Command: mongoCommandDoc(t, bson.D{{Key: "getMore", Value: int64(7)}}),
	}))
	assert.Empty(t, mongoCommandCollection(&event.CommandStartedEvent{Command: bson.Raw{}}))
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 5))
	assert.Equal(t, "abcde…(truncated)", truncate("abcdefgh", 5))
}

func TestMongoMonitor_carriesTheRequestItBelongsTo(t *testing.T) {
	// the point of taking the driver's context: a query line is findable by the
	// request that issued it
	var buf strings.Builder
	// a query line is logged at debug, so the logger has to be listening there
	base := NewLoggerTo(&buf, mustEnv(t, map[string]string{"LOG_LEVEL": "debug"}))
	monitor := NewMongoMonitor(base, MongoLogInfo, time.Second)

	ctx := context.WithValue(context.Background(), requestIDKey, "req-123")
	monitor.Started(ctx, &event.CommandStartedEvent{
		Command:     mongoCommandDoc(t, bson.D{{Key: "find", Value: "users"}}),
		CommandName: "find",
		RequestID:   1,
	})
	monitor.Succeeded(ctx, &event.CommandSucceededEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{RequestID: 1, CommandName: "find"},
	})

	assert.Contains(t, buf.String(), "req-123")
}
