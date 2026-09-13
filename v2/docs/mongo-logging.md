# Query logging

Every command is logged the way SQL statements are, into the same stream and
carrying the same `request_id` and trace as the request that issued it — the
driver hands each event the operation's context, so a query line is findable from
the request it belongs to.

```json
{"level":"DEBUG","msg":"mongo command","command":"find","collection":"users",
 "query":"{\"find\": \"users\",\"filter\": {\"status\": \"active\"}}",
 "duration_ms":3,"request_id":"01HX…"}
{"level":"WARN","msg":"slow mongo command","command":"aggregate",...,"duration_ms":412,"threshold_ms":200}
{"level":"ERROR","msg":"mongo command failed","command":"insert",...,"err":"E11000 duplicate key error"}
```

## How much is logged

`LOG_LEVEL` sets the baseline and `DB_MONGO_LOG_LEVEL` overrides it
independently — the same pair as `DB_LOG_LEVEL` for SQL, for the same reason:
turning the application up to debug to read one flow should not bury it under
every query the driver makes.

| Level | What is logged |
|---|---|
| `silent` | nothing (no monitor is installed at all) |
| `error` | failed commands |
| `warn` | failures and commands slower than the threshold — **the default** |
| `info` / `debug` | every command, at debug |

The same setting gates the pool and topology lines below: `silent` installs no
monitors at all, `error` keeps only the failed connection checkout, and `warn`
adds the rest.

```sh
# every Mongo command, without turning the whole service up to debug
APP_DB_MONGO_LOG_LEVEL=info

# read a handler's flow without drowning in the queries a background job makes
APP_LOG_LEVEL=debug APP_DB_MONGO_LOG_LEVEL=warn

# nothing at all — including failures
APP_DB_MONGO_LOG_LEVEL=silent
```

⚠️ `info` puts **filter values** in the log: emails, ids, tokens, anything in a
`$match`. Do not leave it on in a production environment whose logs are retained.

## Per connection, in Go

Go options win over the environment, which is how a second connection is kept
quieter than the main one:

```go
m, _ := core.NewMongoDB(env,
    core.WithMongoLogLevel(core.MongoLogInfo),      // every command
    core.WithMongoSlowQuery(500*time.Millisecond),  // default 200ms
    core.WithMongoLogger(myLogger),
)
```

| Option | Default | Use it when |
|---|---|---|
| `WithMongoLogLevel` | `LOG_LEVEL` / `DB_MONGO_LOG_LEVEL` | one connection should be louder or quieter than the rest |
| `WithMongoSlowQuery` | `200ms` | a workload where 200ms is normal (reports, batch imports), so the log is not full of "slow" commands that are not |
| `WithMongoLogger` | the logger from config | sending Mongo traffic to its own stream |

## What is left out

The driver's own housekeeping — `hello`, `ping`, `endSessions`, the
authentication handshake — is excluded. A readiness probe alone would otherwise
log every few seconds and bury what the service actually asked for.

A command document longer than **2KB** is truncated, so one large `insertMany`
cannot push everything else out of a log viewer. When you need the whole thing,
you need the profiler, not the log.

## The pool and the topology

Two failures never appear in the command log, because no command is involved:
the connection **pool running dry**, and the **primary moving**. Both are logged
as well, and both are quiet enough to leave on in production — which is the
point, since the moment to turn on debug logging is the moment nobody is there
to do it.

```json
{"level":"ERROR","msg":"mongo connection checkout failed","address":"mongo-1:27017",
 "waited_ms":3000,"reason":"timeout","err":"timed out while checking out a connection"}
{"level":"WARN","msg":"mongo connection pool cleared","address":"mongo-1:27017","interrupted":true}
{"level":"WARN","msg":"mongo server lost","address":"mongo-2:27017","from":"RSSecondary","to":"Unknown"}
{"level":"WARN","msg":"mongo primary changed","from":"mongo-1:27017","to":"mongo-2:27017","replica_set":"rs0"}
```

| Line | Means | Logged from |
|---|---|---|
| `mongo connection checkout failed` | the pool is exhausted or the server is unreachable | `error` |
| `mongo connection pool cleared` | the driver threw away every connection to a server it decided is unhealthy | `warn` |
| `mongo server lost` / `mongo server changed` | a member became unreachable, or came back | `warn` |
| `mongo primary lost` / `elected` / `changed` | the set has no primary, or a different one | `warn` |

**`checkout failed` is the one to watch for.** When the pool is exhausted the
time goes into *waiting for a connection*, not into running a command — so every
query is slow while the slow-command log stays empty, and nothing else in the
log explains it. It is also the line that says whether `MaxPoolSize` is the
problem.

Everything else the pool does — created, ready, checked out, checked in — is
dropped. At a pair per operation it would bury the command log it is meant to
complement. Heartbeats are dropped for the same reason: a node that is down
fails one every few seconds, and the same line forever is not information. The
description changing to `Unknown` says it once.

These lines carry **no `request_id`**: the driver's pool and topology events
have no context to take one from, unlike command events.

## Something other than logging

All three monitors are replaceable, which is the hook for tracing or metrics:

```go
core.NewMongoDB(env,
    core.WithMongoMonitor(myCommandMonitor),
    core.WithMongoPoolMonitor(myPoolMonitor),      // nil to record nothing
    core.WithMongoServerMonitor(myServerMonitor),
)
```

Replacing one removes its logging — a monitor is one object, and this one is
yours. Compose the two yourself if you want both.

## When the log is not enough

Slow-command lines tell you *which* command was slow, not *why*. For that:

- `explain` shows the plan a query actually used — see
  [Indexes](./mongo-indexes.md#compound-indexes-and-order). A `COLLSCAN` means no
  index was used.
- `Comment` on an [aggregation](./mongo-aggregation.md#options) makes a
  long-running pipeline identifiable in `db.currentOp()`.
- Mongo's own profiler (`db.setProfilingLevel`) records what the driver never
  sees, including work the server did after the response was sent.
