# mine-core v2

A Go backend framework — v2. A clean-break rewrite over Echo,
GORM, go-redis, resty, koanf, slog, and friends. Module path:
`github.com/pskclub/mine-core/v2` (Go 1.27).

> v2 keeps v1's familiar interface names (`IContext`, `IError`, `IENV`, `ICache`,
> `IMongoDB`, `IRequester`, `IMQ`, `ILogger`, `IModel`, `IRepository`) but fixes
> v1's structural issues: context flows everywhere, no global state, no panics in
> the error path, and one library per job.

## Quick start

```go
env, _ := core.NewEnv()
db, _ := core.NewDatabase(env)
redis, _ := core.NewCache(env)

app, _ := core.NewApp(env,
    core.WithSQL("default", db),
    core.WithCache("default", redis),
)
defer app.Shutdown(context.Background())

e := core.NewHTTPServer(app, &core.HTTPOptions{AllowOrigins: []string{"*"}})
e.POST("/users", controller.CreateUser)
core.StartHTTPServer(e, env)
```

See [docs/getting-started.md](./docs/getting-started.md) for a full example.

## Documentation

**Start here**
- [Why v2](./docs/why-v2.md) — what changed from v1, and what you get for it
- [Getting started](./docs/getting-started.md) — wiring + a full controller
- [Testing](./docs/testing.md) — [unit](./docs/testing-unit.md) · [integration](./docs/testing-integration.md) · [e2e](./docs/testing-e2e.md) · [mocks](./docs/testing-mock.md) · reference: [fixtures](./docs/testing-fixtures.md), [database](./docs/testing-database.md), [HTTP](./docs/testing-http.md), [jobs](./docs/testing-jobs.md), [assertions](./docs/testing-assertions.md)
- [API + Cron jobs in one service](./docs/api-with-cron.md) — run HTTP and scheduled jobs together

**Core**
- [Context & App](./docs/context.md) — `IContext`, `App`, ambient context, scoped data
- [Configuration (ENV)](./docs/env.md) — koanf, URI/discrete connections
- [Error handling](./docs/error-handling.md) — `IError`, `errmsgs`, `Recover`
- [Logger](./docs/logger.md) — `ILogger` (slog)
- [Runner](./docs/runner.md) — start HTTP, jobs and consumers together, stop them in order
- [Health & readiness](./docs/health.md) — `/healthz` and `/readyz` over the App's own dependencies
- [Sentry](./docs/sentry.md) — automatic error tracking, breadcrumbs, logs, metrics, cron monitors, tracing

**HTTP & validation**
- [HTTP layer](./docs/http.md) — server, routing, binding (path/query/form/json), groups
- [Authentication](./docs/auth.md) — JWT bearer middleware, roles, `c.GetUser()`
- [Validation](./docs/validation.md) — fluent rules, conditional/cross-field, reusable nested
- [Postman collection & OpenAPI](./docs/postman.md) — `go tool postmangen` generates both from the routes you already wrote
- [API reference](./docs/apidocs.md) — mount the generated OpenAPI as a Scalar page the team can send requests from

**Data**
- [Database (SQL)](./docs/database.md) — GORM connection
- [Repository](./docs/repository.md) — generic `Repo[M]`, pagination
- [Cache](./docs/cache.md) — Redis, JSON helpers, `Remember`, counters, locks
- [MongoDB](./docs/mongo.md) — `IMongoDB`, aggregation, transactions, indexes
- [Mongo repository](./docs/mongo-repository.md) — generic `mongorepo.Repo[D]`, typed pipelines

**Messaging & jobs**
- [Pub/Sub](./docs/pubsub.md) — redis fan-out, subscribers with handlers
- [Message Queue](./docs/mq.md) — RabbitMQ publisher, topology and consumers
- [Scheduler](./docs/scheduler.md) — cron jobs (gocron v2)

**Clients & integrations**
- [Requester](./docs/requester.md) — HTTP client (resty), typed responses
- **AI / Language Model** — [Overview](./docs/ai.md) · [Provider setup](./docs/ai-setup.md) · [Text](./docs/ai-text.md) · [Streaming](./docs/ai-streaming.md) · [Typed values](./docs/ai-typed.md) · [Attachments](./docs/ai-attachments.md) · [Tool use](./docs/ai-tools.md) · [Agent loops](./docs/ai-agent.md) · [Embeddings](./docs/ai-embeddings.md) · [Providers](./docs/ai-providers.md) · [Testing](./docs/ai-testing.md)
- [Storage (S3)](./docs/storage.md) — upload/download, presigned links, prefixes
- [Mailer](./docs/mailer.md) · [Push (FCM)](./docs/push.md)
- [JWT](./docs/jwt.md) · [CSV](./docs/csv.md) · [Utils (pointer/enum helpers)](./docs/utils.md)

## Module status

| Capability | API | Status |
|---|---|---|
| Errors | `core.IError` / `core.Error` / `errmsgs` | ✅ tested |
| Config | `core.IENV` / `core.ENVConfig` (koanf) | ✅ tested |
| Logger | `core.ILogger` (slog) | ✅ tested |
| Context / App | `core.IContext` / `core.App` | ✅ tested |
| Sentry | `core.ISentry` (sentry-go) | ✅ tested (recording transport) |
| Cache | `core.ICache` (redis / in-memory) | ✅ tested (memory backend); `--tags=integration` for redis |
| Pub/Sub | `core.IPubSub` / `core.ISubscriber` | ✅ tested (memory backend); `--tags=integration` for redis |
| SQL | `core.NewDatabase` (gorm: postgres, mysql, sqlserver, oracle) | ✅ tested (sqlite, DSN building) |
| Repository | `repository.Repo[M]` | ✅ tested (sqlite) |
| Validation | `valid.Validator` | ✅ tested |
| Requester | `core.IRequester` (resty) | ✅ compiled |
| HTTP | `core.IHTTPContext` / `NewHTTPServer` (echo v5) | ✅ tested (httptest) |
| Scheduler | `core.Scheduler` (gocron v2) | ✅ tested |
| JWT | `core.JWTSign` / `core.JWTVerify` (golang-jwt v5) | ✅ tested |
| CSV | `core.CSVMarshal` / `core.CSVUnmarshal` | ✅ tested |
| Mongo | `core.IMongoDB` (mongo-driver) | ✅ tested (URI, sorting, ids, paging); `--tags=integration` for mongo |
| Mongo repository | `mongorepo.Repo[D]` / `mongorepo.Aggregate[T]` | ✅ tested (query building); `--tags=integration` for mongo |
| MQ | `core.IMQ` / `core.IMQConsumer` (amqp091) | ✅ tested (pool, reconnect, topology, ack policy); integration needs rabbitmq |
| Storage | `core.IStorage` (aws-sdk-go-v2 s3 / in-memory) | ✅ tested (memory backend); `--tags=integration` for s3/minio |
| Mailer | `core.IMailer` (go-mail) | ✅ tested (memory backend, templates); integration needs smtp |
| Push | `core.IPusher` (firebase v4) | ✅ tested (memory backend, payload building); integration needs fcm |
| AI / LLM | `core.ILLM` / `llm/goai` (zendev-sh/goai) | ✅ tested (memory backend + conformance suite over a fake provider) |
| AI typed output | `llm.New[T](ctx)` / `llm.SchemaOf[T]()` | ✅ tested (schema reflection, parsing, validation) |
| AI attachments | `core.LLMImage` / `LLMFile` on a message | ✅ tested (validation, wire mapping; live vision against a real provider) |
| AI tool use | `core.LLMTool` / `llm.Tool[In]` + `Approve` gate | ✅ tested (loop, approval, panic guard; live against a real provider) |
| AI provider tools | `goai.GoogleSearch()` / `URLContext()` / `CodeExecution()` → `LLMResponse.Sources` | ✅ tested (wire mapping, grounding citations) |
| Embeddings | `core.IEmbedder.EmbedWith` / `llm/goai.NewEmbedder` | ✅ tested (memory backend, cosine similarity, per-provider `Dimensions`/`Task` mapping) |
| Health probes | `core.LiveHandler` / `core.ReadyHandler` | ✅ tested |
| Runner | `core.Runner` | ✅ tested (start/stop ordering) |
| Postman & OpenAPI generator | `cmd/postmangen` (`go tool postmangen`) | ✅ tested (golden collection and golden OpenAPI document over one fixture repo) |
| API reference | `apidocs.Mount(srv, apidocs.Options{…})` | ✅ tested (guard, spec loading, servers, rendered page) |

## Contributing

Setup, build tags, the conventions a reviewer will push back on, and the release
flow: [CONTRIBUTING.md](../CONTRIBUTING.md).

## Testing

Tests use **[stretchr/testify](https://github.com/stretchr/testify)** as the
standard: `require` for preconditions (stops the test) and `assert` for value
checks. Prefer `require.NoError` / `assert.Equal` / `assert.ErrorIs` over
hand-written `if ... t.Fatalf`.

```go
import (
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestThing(t *testing.T) {
    got, err := DoThing()
    require.NoError(t, err)
    assert.Equal(t, want, got)
}
```

```sh
go test ./...            # unit tests (no external services)
go test -race ./...      # race detector (clean — no global state)
```

Consuming services build their fixtures with
[`coretest`](./docs/testing.md) — a context on a real database, an HTTP client
that speaks the framework's error shape, and a job runner, so none of that
wiring is rewritten per repository.
