# mine-core

[![CI](https://github.com/pskclub/mine-core/actions/workflows/ci.yml/badge.svg)](https://github.com/pskclub/mine-core/actions/workflows/ci.yml)
[![Integration](https://github.com/pskclub/mine-core/actions/workflows/integration.yml/badge.svg)](https://github.com/pskclub/mine-core/actions/workflows/integration.yml)
[![Security](https://github.com/pskclub/mine-core/actions/workflows/security.yml/badge.svg)](https://github.com/pskclub/mine-core/actions/workflows/security.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/pskclub/mine-core/v2.svg)](https://pkg.go.dev/github.com/pskclub/mine-core/v2)
[![Release](https://img.shields.io/github/v/release/pskclub/mine-core?filter=v2.*&label=release)](https://github.com/pskclub/mine-core/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](./LICENSE)

A batteries-included Go framework for backend services — HTTP, SQL, MongoDB,
cache, queues, jobs, storage, mail, push, AI and observability behind one
consistent `context`, with an in-memory implementation of every capability, so
tests need no mocks.

```sh
go get github.com/pskclub/mine-core/v2
```

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

The full walkthrough, with a validated controller, is in
[Getting started](./v2/docs/getting-started.md).

## What's inside

| Capability | Built on |
|---|---|
| HTTP server, routing, binding, validation | Echo v5 |
| SQL repositories | GORM — Postgres, MySQL, SQL Server, Oracle, SQLite |
| MongoDB repositories and aggregation | mongo-driver v2 |
| Cache, distributed locks, counters | go-redis v9, or in-memory |
| Message queue | RabbitMQ (amqp091) |
| Pub/sub | Redis, or in-memory |
| Jobs, cron, durable job store | gocron v2 |
| Object storage, presigned URLs | AWS SDK v2 — S3, MinIO — or in-memory |
| Mail and push notifications | go-mail, Firebase |
| AI — text, typed output, tools, streaming, embeddings | multi-provider, or in-memory |
| Errors, logging, tracing | slog, Sentry |
| Health probes, graceful shutdown, dev tools | built in |
| Postman and OpenAPI generation | `go tool postmangen` |

## Documentation

The framework documentation lives in [`v2/docs/`](./v2/docs) and is written in
Thai. Good places to start:

- [Why v2](./v2/docs/why-v2.md) — what changed from v1, and why
- [Getting started](./v2/docs/getting-started.md) — wiring and a full controller
- [Architecture](./v2/docs/architecture.md) — the App, the context, and the path of a request
- [Testing](./v2/docs/testing.md) — unit, integration and end-to-end
- [Deployment](./v2/docs/deployment.md) — images, config, probes, shutdown
- [Migrating from v1](./v2/docs/migration-v1.md)

The complete index is in [v2/README.md](./v2/README.md). API reference:
[pkg.go.dev](https://pkg.go.dev/github.com/pskclub/mine-core/v2).

## Versions

| Module | Import path | Status |
|---|---|---|
| **v2** | `github.com/pskclub/mine-core/v2` | ✅ **Active** — all new development |
| v1 | `github.com/pskclub/mine-core` | ⚠️ Maintenance — critical fixes only; deprecated in favour of v2 |

Both modules live in this repository: v2 under [`v2/`](./v2), v1 at the root.
They are versioned and released independently. If you are on v1, the
[migration guide](./v2/docs/migration-v1.md) walks through the move.

## Contributing

Contributions are welcome — bug reports, docs fixes and code alike.

- [CONTRIBUTING.md](./CONTRIBUTING.md) — setup, tests, conventions and the PR process
- [Code of Conduct](./CODE_OF_CONDUCT.md)
- [Security policy](./SECURITY.md) — please report vulnerabilities privately

```sh
make help     # list the development targets
make check    # run what the required CI checks run
```

## License

[MIT](./LICENSE)
