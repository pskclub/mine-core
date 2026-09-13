# Connections & configuration

A connection is opened once, at startup, and registered on the `App`. Nothing
opens one per request — that was v1's bug, and it is why `IContext.Close()` no
longer exists.

```go
func main() {
    env, err := core.NewEnv()
    if err != nil {
        panic(err)
    }

    db, err := core.NewDatabase(env)
    if err != nil {
        panic(err)          // a misconfigured database is a boot failure
    }

    app, err := core.NewApp(env, core.WithSQL("default", db))
    if err != nil {
        panic(err)
    }
    defer app.Shutdown(context.Background())
    // ...
}
```

`NewDatabase` connects eagerly, so a wrong password is an error at boot rather
than a 500 on the first request that happens to need the database.

## Drivers

**postgres**, **mysql**, **sqlserver** and **oracle**. The driver comes from
`DB_DRIVER`, or is inferred from the scheme of a connection string:

| Scheme | Driver | `DB_DRIVER` |
|---|---|---|
| `postgres://`, `postgresql://` | postgres | `postgres` |
| `mysql://` | mysql | `mysql` |
| `sqlserver://`, `mssql://` | SQL Server | `sqlserver` |
| `oracle://` | Oracle | `oracle` |

sqlite ใช้ได้ในเทสต์ผ่าน [`coretest`](./testing.md) ไม่ใช่ผ่าน `NewDatabase` — schema
ที่ deploy จริงไม่เคยเป็น sqlite

### SQL Server

```sh
DB_DRIVER=sqlserver
DB_HOST=mssql DB_PORT=1433 DB_NAME=app DB_USER=sa DB_PASSWORD='p@ss/word'
```

รหัสผ่านถูก escape ผ่าน userinfo ของ URL ตัวที่มี `@` หรือ `/` (ซึ่งรหัสผ่านที่ generate
มามักมี) จึงไม่ทำให้ DSN แตกผิดที่

### Oracle

```sh
DB_DRIVER=oracle
DB_HOST=oracle DB_PORT=1521      # 1521 คือ default ไม่ต้องใส่ก็ได้
DB_NAME=ORCLPDB1                 # service name
DB_SID=ORCL                      # ใส่เมื่อเชื่อมด้วย SID แทน service name
DB_USER=app DB_PASSWORD=secret
```

Oracle ตั้งชื่อ **service** หรือ **SID** ไม่ใช่ database: `DB_NAME` คือ service name,
`DB_SID` คือ SID

## Configuring the DSN

Two ways, and the first one wins outright:

```sh
# 1. one URI — every other DB_* field is ignored
DB_CONNECTION_STRING=postgres://user:pass@host:5432/mydb?sslmode=disable

# 2. discrete fields
DB_DRIVER=postgres
DB_HOST=localhost
DB_PORT=5432
DB_NAME=mydb
DB_USER=app
DB_PASSWORD=secret
DB_SSLMODE=disable        # postgres only
```

Use the URI when the DSN needs an option the discrete fields do not cover — a
connection timeout, a search path, a client certificate. Use the fields when the
deployment already builds the parts separately (a Kubernetes secret per value,
say), because assembling them into a URI only adds a place to get quoting wrong.

> A `mysql://…` URI is converted to the Go DSN the mysql driver expects
> (`user:pass@tcp(host:port)/db`), with `parseTime=True`, `charset=utf8mb4` and
> `loc=UTC` filled in when they are not already there. `parseTime` matters: a
> `time.Time` column scans as `[]byte` without it.

Every key, with defaults, is in [Configuration → Database](./env.md#database-sql).

## The pool

Pool sizes are **not** environment keys — they are options on `NewDatabase`,
because the right numbers depend on what the process is (an API serving 200
concurrent requests and a cron worker running one query at a time do not want the
same pool) rather than on which environment it runs in:

```go
db, err := core.NewDatabase(env,
    core.WithMaxOpenConns(50),                 // default 20
    core.WithMaxIdleConns(10),                 // default 5
    core.WithConnMaxLifetime(30*time.Minute),  // default 1h
)
```

| Option | What it bounds | Getting it wrong |
|---|---|---|
| `WithMaxOpenConns` | connections this process may hold at once | too high and *N* replicas exhaust the server's `max_connections`; too low and requests queue behind each other |
| `WithMaxIdleConns` | connections kept open while idle | too low and a bursty service reconnects constantly |
| `WithConnMaxLifetime` | how long one connection is reused | too long and a connection outlives a failover or a proxy's idle timeout, failing the query that finds out |

The arithmetic worth doing once: `MaxOpenConns × replicas` must stay comfortably
under the server's connection limit, with room left for migrations, other
services and a human with `psql`.

## Named connections

Register as many as the service needs. `"default"` is what `ctx.DB()` returns;
everything else is `ctx.DBS(name)`:

```go
primary, _ := core.NewDatabase(env)   // from DB_*

// a second connection is a second *gorm.DB — NewDatabase reads one set of
// DB_* keys, so anything else is opened directly
replica, _ := gorm.Open(postgres.Open(env.String("DB_REPLICA_CONNECTION_STRING")),
    &gorm.Config{NowFunc: func() time.Time { return time.Now().UTC() }})

app, _ := core.NewApp(env,
    core.WithSQL("default", primary),
    core.WithSQL("readonly", replica),
)
```

`env.String` reads any key that is not part of `ENVConfig`, so a second DSN needs
no change to the framework — see [Configuration](./env.md).

```go
ctx.DB()                 // primary
ctx.DBS("readonly")      // the replica
repository.NewWithDB[User](ctx, ctx.DBS("readonly")).FindAll()
```

Reads sent to a replica are reads that arrive **behind** the primary. That is
fine for a report and wrong for "create it, then read it back" — route by whether
the caller can tolerate staleness, not by whether the statement is a `SELECT`.

`ctx.DBS` on a name that was never registered returns `nil`, which panics at the
first use. It is a wiring mistake, and the fastest way to find one is for it to
be loud.

## Health checks

```go
sqlDB, err := ctx.DB().DB()
if err == nil {
    err = sqlDB.PingContext(ctx)
}
```

Use it in a readiness probe, not a liveness probe: a database that is briefly
unreachable should stop traffic being routed to the pod, not restart it.

## Shutdown

`app.Shutdown(ctx)` closes the pools it was given, after the HTTP server has
stopped accepting and in-flight work has drained. Handing a `*gorm.DB` to
`core.WithSQL` transfers that responsibility — do not also close it yourself.

```go
defer app.Shutdown(context.Background())
```

## Using a connection the framework did not open

`core.WithSQL` takes any `*gorm.DB`, so a service with its own dialector,
plugins or an existing `*sql.DB` wires that one instead:

```go
gdb, _ := gorm.Open(postgres.New(postgres.Config{Conn: existingSQLDB}), &gorm.Config{})
app, _ := core.NewApp(env, core.WithSQL("default", gdb))
```

The framework's SQL logger and the UTC `NowFunc` are set up by `NewDatabase` — a
hand-built connection has whatever you configured on it and nothing more.

## Tracing queries

Query spans and `db.*` breadcrumbs in Sentry are opt-in, on any connection:

```go
_ = core.InstrumentGorm(db)
app, _ := core.NewApp(env, core.WithSQL("default", db))
```

See [Sentry](./sentry.md) for what it records and how to tune it.
