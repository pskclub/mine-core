# Connection & configuration

```go
mongo, err := core.NewMongoDB(env)
if err != nil {
    panic(err)          // a misconfigured Mongo is a boot failure
}
app, _ := core.NewApp(env, core.WithMongo("default", mongo))
defer app.Shutdown(context.Background())
```

`NewMongoDB` pings before returning, so a wrong password or an unreachable
replica set stops the process at startup rather than producing a 500 on the first
request that needs a document.

## Configuration

```sh
DB_MONGO_NAME=mydb                  # always required — not read from the URI
DB_MONGO_CONNECTION_STRING=mongodb://user:pass@host:27017
# or
DB_MONGO_HOST=a,b,c                 # a comma-separated list is a replica set
DB_MONGO_PORT=27017
DB_MONGO_USERNAME=app
DB_MONGO_PASSWORD=secret
DB_MONGO_REPLICA_NAME=rs0
DB_MONGO_TLS=true
DB_MONGO_MAX_POOL_SIZE=100
DB_MONGO_MIN_POOL_SIZE=5
DB_MONGO_TIMEOUT=30                 # seconds, per operation
```

`DB_MONGO_NAME` is separate from the URI on purpose: a connection string points
at a *server*, and which database on it this service uses is a different
decision — one that a staging deploy sharing a cluster has to be able to change
without rewriting the URI.

Credentials are percent-encoded before the URI is assembled, so a password
holding `@`, `/` or `:` connects instead of producing a parse error that names
the password in the message.

Every key and its default is in [Configuration → MongoDB](./env.md#mongodb).

## Replica sets

A replica set is not only about availability — it is what makes
[transactions](./mongo-transactions.md) and
[change streams](./mongo-indexes.md#change-streams) possible at all. A standalone
server refuses both.

```sh
DB_MONGO_HOST=mongo-0,mongo-1,mongo-2
DB_MONGO_REPLICA_NAME=rs0
```

Ports per host are allowed in the list (`a:27018,b,c`), which is what a local
three-node set on one machine needs.

## Anything the fields do not cover

Use the connection string. Read preferences in the URI, `retryWrites`,
`authSource`, `directConnection`, a `tlsCAFile`, an SRV record — none of them have
a discrete field, and adding one per driver option would be a worse version of
the URI:

```sh
DB_MONGO_CONNECTION_STRING=mongodb+srv://u:p@cluster.mongodb.net/?retryWrites=true&w=majority&authSource=admin
DB_MONGO_NAME=mydb
```

## Timeouts

`DB_MONGO_TIMEOUT` is the driver's deadline for one operation, in seconds. It is
what stops a query started from a context with **no** deadline — a cron run, a
queue consumer, a subscriber — from hanging forever:

```sh
DB_MONGO_TIMEOUT=30
```

Requests already carry a deadline from the HTTP layer, so this mostly matters for
the paths that do not. Set it anyway: the failure mode it prevents is a worker
that looks alive and is doing nothing.

For one query that needs a different bound, `MaxTime` bounds it **on the server**,
so a slow one is killed there rather than merely abandoned here:

```go
err := m.Find(&rows, "events", filter, core.MongoFindOptions{MaxTime: 5 * time.Second})
```

## The pool

```sh
DB_MONGO_MAX_POOL_SIZE=100     # driver default is 100
DB_MONGO_MIN_POOL_SIZE=5
```

The same arithmetic as SQL: `max pool × replicas` must stay under what the server
will accept, with room left for other services and an operator's shell.

## Read preferences

Send heavy, staleness-tolerant reads to a secondary without changing the
connection every other query uses:

```go
reports := ctx.DBMongo().WithReadPreference(readpref.SecondaryPreferred())
err := reports.Aggregate(&rows, "events", pipeline)
```

`WithReadPreference` returns a handle; it does not mutate the one it was called
on. A read from a secondary is a read from **behind** the primary — right for a
dashboard, wrong for "insert it, then read it back".

## Named connections

```go
app, _ := core.NewApp(env,
    core.WithMongo("default", primary),
    core.WithMongo("audit", auditMongo),
)
```

```go
ctx.DBMongo()             // "default"
ctx.DBSMongo("audit")     // a named one
mongorepo.NewIn[Event](ctx.DBSMongo("audit"))
```

`ctx.DBSMongo` on a name that was never registered returns the disabled handle
rather than nil, so the failure is a `MONGO_DISABLED` error naming the problem
instead of a panic in an unrelated place.

## Health checks

```go
if err := ctx.DBMongo().Ping(); err != nil {
    return err       // readiness, not liveness
}
```

Housekeeping commands — `ping`, `hello`, `endSessions`, the auth handshake — are
excluded from the query log, so a probe running every few seconds does not bury
what the service actually asked for. See [Query logging](./mongo-logging.md).

## Shutdown

`app.Shutdown(ctx)` closes the client it was given. Handing a handle to
`core.WithMongo` transfers that responsibility — do not also `Close()` it.

## Escape hatch

```go
m := ctx.DBMongo()

m.Collection("users")   // *mongo.Collection
m.Database()            // *mongo.Database
m.Client()              // *mongo.Client
m.Context()             // the context every helper is using
```

Use `m.Context()` with them. Inside a [transaction](./mongo-transactions.md) that
is the *session's* context, and a driver call made on any other context silently
lands outside the transaction — which is the one Mongo mistake that produces
data no rollback removes.
