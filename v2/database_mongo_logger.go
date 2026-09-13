package core

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/event"
)

// MongoLogLevel decides how much of the traffic to Mongo is logged. It mirrors
// the SQL side: failures always, slow commands at Warn, everything at Info.
type MongoLogLevel int

const (
	// MongoLogSilent logs nothing at all.
	MongoLogSilent MongoLogLevel = iota
	// MongoLogError logs failed commands.
	MongoLogError
	// MongoLogWarn logs failures and slow commands. The default.
	MongoLogWarn
	// MongoLogInfo logs every command.
	MongoLogInfo
)

// defaultMongoSlowQuery is the threshold above which a command is logged as
// slow even when command logging is otherwise off.
const defaultMongoSlowQuery = 200 * time.Millisecond

// maxMongoCommandBytes bounds the logged command document. An insert carries the
// whole document and an $in can carry thousands of ids; past a couple of
// kilobytes the line stops being readable and starts being a cost.
const maxMongoCommandBytes = 2048

// maxMongoInFlight bounds the started-command map. A command whose reply never
// arrives — a connection killed mid-flight — would otherwise be remembered
// forever; past the cap the command document is dropped from the line rather
// than the process growing.
const maxMongoInFlight = 4096

// noisyMongoCommands are the driver's own housekeeping. They say nothing about
// what the service asked for, and at Info they would bury what does: a readiness
// probe alone would log a ping every few seconds.
var noisyMongoCommands = map[string]bool{
	"hello": true, "ismaster": true, "isMaster": true, "ping": true,
	"endSessions": true, "killCursors": true, "buildInfo": true,
	"saslStart": true, "saslContinue": true, "saslSupportedMechs": true,
	"authenticate": true, "getnonce": true,
}

// mongoLogLevelFrom decides how much Mongo traffic is logged:
// DB_MONGO_LOG_LEVEL when it is set, otherwise it follows the app's LOG_LEVEL —
// debug means every command, anything else keeps the log to slow commands and
// failures.
//
// The separate key exists for the same reason DB_LOG_LEVEL does: turning the
// application up to debug to read one flow should not bury it under every query
// the driver makes.
func mongoLogLevelFrom(env IENV) MongoLogLevel {
	if env == nil {
		return MongoLogWarn
	}
	if level, ok := parseMongoLogLevel(env.Config().DBMongoLogLevel); ok {
		return level
	}
	switch parseLevel(env.Config().LogLevel) {
	case slog.LevelDebug:
		return MongoLogInfo
	case slog.LevelError:
		return MongoLogError
	default:
		return MongoLogWarn
	}
}

// parseMongoLogLevel reads DB_MONGO_LOG_LEVEL. "off"/"false" are accepted
// alongside "silent" because the setting is reached for as a switch.
func parseMongoLogLevel(s string) (MongoLogLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "silent", "off", "none", "false":
		return MongoLogSilent, true
	case "error":
		return MongoLogError, true
	case "warn", "warning":
		return MongoLogWarn, true
	case "info", "debug", "all", "true":
		return MongoLogInfo, true
	default:
		return 0, false
	}
}

// mongoMonitor adapts the driver's command monitor to ILogger, so Mongo joins
// the one log stream instead of having a channel of its own: same JSON/text
// format, same level, and — because the driver hands each event the operation's
// context — the same request_id and trace as the request that issued it.
//
// It is the counterpart of gormLogger, and deliberately logs the same shape of
// line so a service using both reads as one log.
type mongoMonitor struct {
	log       ILogger
	level     MongoLogLevel
	slowQuery time.Duration

	mu      sync.Mutex
	started map[int64]mongoCommand
}

// mongoCommand is what the started event knew and the finished event does not:
// which collection, and the command document itself.
type mongoCommand struct {
	name       string
	database   string
	collection string
	command    string
}

// NewMongoMonitor builds the command monitor used by NewMongoDB. Pass it to
// options.Client().SetMonitor when opening a connection by hand.
//
// It returns nil at MongoLogSilent: a monitor that records events and drops them
// still costs a map write and a document render per command.
func NewMongoMonitor(log ILogger, level MongoLogLevel, slowQuery time.Duration) *event.CommandMonitor {
	if level <= MongoLogSilent {
		return nil
	}
	return newMongoMonitor(log, level, slowQuery).commandMonitor()
}

func newMongoMonitor(log ILogger, level MongoLogLevel, slowQuery time.Duration) *mongoMonitor {
	if slowQuery <= 0 {
		slowQuery = defaultMongoSlowQuery
	}
	return &mongoMonitor{
		// blameApp: the driver runs this monitor on the goroutine that issued the
		// command, so the repository call behind it is still on the stack — unlike
		// the pool and topology monitors below, which fire on the driver's own
		// goroutines and have nothing to blame but this file
		log:       blameApp(log),
		level:     level,
		slowQuery: slowQuery,
		started:   map[int64]mongoCommand{},
	}
}

func (m *mongoMonitor) commandMonitor() *event.CommandMonitor {
	return &event.CommandMonitor{
		Started:   m.onStarted,
		Succeeded: m.onSucceeded,
		Failed:    m.onFailed,
	}
}

func (m *mongoMonitor) onStarted(_ context.Context, e *event.CommandStartedEvent) {
	if e == nil || noisyMongoCommands[e.CommandName] {
		return
	}

	cmd := mongoCommand{
		name:       e.CommandName,
		database:   e.DatabaseName,
		collection: mongoCommandCollection(e),
	}
	// the document is only rendered when it will be read: at Warn it is needed
	// only for the commands that turn out to be slow, but which those are is not
	// known until they finish
	cmd.command = truncate(collapseSpace(e.Command.String()), maxMongoCommandBytes)

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.started) >= maxMongoInFlight {
		return
	}
	m.started[e.RequestID] = cmd
}

func (m *mongoMonitor) onSucceeded(ctx context.Context, e *event.CommandSucceededEvent) {
	if e == nil {
		return
	}
	cmd, ok := m.take(e.RequestID)
	if !ok {
		return
	}

	elapsed := e.Duration
	switch {
	case elapsed >= m.slowQuery && m.level >= MongoLogWarn:
		m.at(ctx).Warn("slow mongo command",
			append(cmd.fields(elapsed), "threshold_ms", m.slowQuery.Milliseconds())...)
	case m.level >= MongoLogInfo:
		m.at(ctx).Debug("mongo command", cmd.fields(elapsed)...)
	}
}

func (m *mongoMonitor) onFailed(ctx context.Context, e *event.CommandFailedEvent) {
	if e == nil {
		return
	}
	cmd, ok := m.take(e.RequestID)
	if !ok {
		return
	}
	if m.level < MongoLogError {
		return
	}
	m.at(ctx).Error("mongo command failed",
		append(cmd.fields(e.Duration), "err", e.Failure)...)
}

// take removes a started command, so the map does not grow with the traffic.
func (m *mongoMonitor) take(requestID int64) (mongoCommand, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd, ok := m.started[requestID]
	delete(m.started, requestID)
	return cmd, ok
}

// fields lays the line out the way the SQL logger does: what ran first, then how
// long it took, so the eye can run down a column of commands.
func (c mongoCommand) fields(elapsed time.Duration) []any {
	fields := []any{"command", c.name}
	if c.collection != "" {
		fields = append(fields, "collection", c.collection)
	}
	return append(fields,
		"query", c.command,
		"duration_ms", elapsed.Milliseconds(),
	)
}

// at binds the operation's context so the line carries the request it belongs
// to. A background command (no request) simply logs without those attributes.
func (m *mongoMonitor) at(ctx context.Context) ILogger {
	if l, ok := m.log.(*logger); ok && ctx != nil {
		return l.withContext(ctx)
	}
	return m.log
}

// mongoCommandCollection reads the collection out of a command document. Every
// CRUD command names it in the first element ({"find": "users", …}); the ones
// that do not — getMore, a database-level aggregate — simply log without it.
func mongoCommandCollection(e *event.CommandStartedEvent) string {
	value, err := e.Command.IndexErr(0)
	if err != nil {
		return ""
	}
	name, ok := value.Value().StringValueOK()
	if !ok {
		return ""
	}
	return name
}

// --- pool and topology ---------------------------------------------------

// mongoServerUnknown is the driver's word for a server it cannot currently
// reach. It is also the state every server starts in, which is why a change
// *out* of it is ordinary and a change *into* it is not.
const mongoServerUnknown = "Unknown"

// mongoServerPrimary is the description kind of the replica set member that
// takes the writes.
const mongoServerPrimary = "RSPrimary"

// NewMongoPoolMonitor builds the connection-pool monitor used by NewMongoDB.
// Pass it to options.Client().SetPoolMonitor when opening a connection by hand.
//
// Of the eleven pool events it logs two. A pool being created, becoming ready,
// or handing a connection out and taking it back says nothing the command log
// does not already say, and at a pair per operation it would bury it. The two
// that are kept are the ones that only happen when something is wrong and that
// nothing else in the log accounts for:
//
//   - a checkout that failed — the pool is exhausted or the server is
//     unreachable. This is the failure that is invisible today: the time goes
//     into waiting for a connection rather than running a command, so every
//     query is slow and the slow-command log stays empty.
//   - a pool that was cleared — the driver threw away every connection to a
//     server it decided is unhealthy, which is what a failover looks like from
//     the client.
//
// Both are rare enough to leave on in production, which is the point: the
// evidence has to already be in the log when someone comes to read it, because
// the moment to turn on debug logging is the moment nobody is there to do it.
//
// Unlike the command monitor, pool events carry no context, so these lines
// cannot name the request that was waiting.
func NewMongoPoolMonitor(log ILogger, level MongoLogLevel) *event.PoolMonitor {
	if log == nil || level <= MongoLogSilent {
		return nil
	}
	return &event.PoolMonitor{Event: func(e *event.PoolEvent) {
		if e == nil {
			return
		}
		switch e.Type {
		case event.ConnectionCheckOutFailed:
			// a failure, so it is logged at every level that logs failures at all
			fields := []any{"address", e.Address, "waited_ms", e.Duration.Milliseconds()}
			if e.Reason != "" {
				fields = append(fields, "reason", e.Reason)
			}
			if e.Error != nil {
				fields = append(fields, "err", e.Error)
			}
			log.Error("mongo connection checkout failed", fields...)
		case event.ConnectionPoolCleared:
			if level < MongoLogWarn {
				return
			}
			fields := []any{"address", e.Address}
			if e.Interruption {
				// in-use connections were killed too, so operations in flight
				// failed rather than finishing
				fields = append(fields, "interrupted", true)
			}
			log.Warn("mongo connection pool cleared", fields...)
		}
	}}
}

// NewMongoServerMonitor builds the topology monitor used by NewMongoDB. Pass it
// to options.Client().SetServerMonitor when opening a connection by hand.
//
// It answers one question the rest of the log cannot: was the deployment itself
// the problem. A member going unreachable and a primary moving are what turn
// into "server selection timeout" and "not primary" further up, where the cause
// is no longer visible.
//
// Heartbeats are deliberately not logged. A node that is down fails one every
// few seconds, and the same line repeated forever is not information; the
// description changing to Unknown says it once.
func NewMongoServerMonitor(log ILogger, level MongoLogLevel) *event.ServerMonitor {
	if log == nil || level < MongoLogWarn {
		return nil
	}
	return &event.ServerMonitor{
		// the driver re-describes a server on every heartbeat, so most of these
		// events differ only in a timestamp: only a change of kind is news
		ServerDescriptionChanged: func(e *event.ServerDescriptionChangedEvent) {
			if e == nil || e.PreviousDescription.Kind == e.NewDescription.Kind {
				return
			}
			fields := []any{
				"address", string(e.NewDescription.Addr),
				"from", mongoServerKind(e.PreviousDescription.Kind),
				"to", mongoServerKind(e.NewDescription.Kind),
			}
			if mongoServerKind(e.NewDescription.Kind) == mongoServerUnknown {
				log.Warn("mongo server lost", fields...)
				return
			}
			log.Info("mongo server changed", fields...)
		},
		// this callback runs with the topology locked, so it only reads and logs
		// — anything needing server selection here would deadlock
		TopologyDescriptionChanged: func(e *event.TopologyDescriptionChangedEvent) {
			if e == nil {
				return
			}
			before := mongoPrimaryOf(e.PreviousDescription)
			after := mongoPrimaryOf(e.NewDescription)
			if before == after {
				return
			}
			switch {
			case after == "":
				log.Warn("mongo primary lost", "from", before,
					"replica_set", e.NewDescription.SetName)
			case before == "":
				log.Info("mongo primary elected", "to", after,
					"replica_set", e.NewDescription.SetName)
			default:
				log.Warn("mongo primary changed", "from", before, "to", after,
					"replica_set", e.NewDescription.SetName)
			}
		},
	}
}

// mongoServerKind reports a description kind, calling the zero value what the
// driver would: a server it has not heard from yet is Unknown, not blank.
func mongoServerKind(kind string) string {
	if kind == "" {
		return mongoServerUnknown
	}
	return kind
}

// mongoPrimaryOf finds the address taking the writes, or "" when the set has no
// primary — which is itself the thing worth logging.
func mongoPrimaryOf(td event.TopologyDescription) string {
	for _, server := range td.Servers {
		if server.Kind == mongoServerPrimary {
			return string(server.Addr)
		}
	}
	return ""
}

// truncate cuts an over-long value and says so, rather than letting one insert
// push everything else out of a log viewer.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}
