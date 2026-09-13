# Contributing to mine-core

Thanks for taking the time to contribute.

mine-core is a **library**, not a service. Everything merged here ends up in
every service that imports it, so a change that looks small in `IContext` or
`IError` touches all of them. This guide covers what to know before you open a
pull request.

- [Ground rules](#ground-rules)
- [One repository, two modules](#one-repository-two-modules)
- [Setting up](#setting-up)
- [Running tests](#running-tests)
- [Conventions reviewers will ask about](#conventions-reviewers-will-ask-about)
- [Documentation](#documentation)
- [Commits and pull requests](#commits-and-pull-requests)
- [Releasing (maintainers)](#releasing-maintainers)

## Ground rules

- Follow the [Code of Conduct](./CODE_OF_CONDUCT.md).
- **Security issues never go in a public issue.** See [SECURITY.md](./SECURITY.md).
- For anything larger than a bug fix, **open an issue first** so the approach is
  agreed before you write it. It saves rewriting a finished PR.
- **New work goes into v2.** v1 accepts fixes only, for bugs that services which
  have not migrated yet actually hit.

## One repository, two modules

| Module | Import path | Code | Tags | Go |
|---|---|---|---|---|
| **v2** | `github.com/pskclub/mine-core/v2` | [`v2/`](./v2) | `v2.x.y` | 1.27 |
| v1 | `github.com/pskclub/mine-core` | repository root | `v1.x.y` | 1.27 in CI |

**v2 is a nested module.** `go test ./...` at the repository root never
descends into `v2/` — a nested module is invisible to its parent's package
patterns. Either `cd v2` first, or use the `make` targets, which do it for you.

## Setting up

You need:

- **Go 1.27** or newer
- **make**
- **Python 3** — for the markdown link check
- **Docker** with Compose — only for integration tests

```sh
git clone https://github.com/<your-fork>/mine-core.git
cd mine-core
make help     # every target, with a one-line description
make check    # what the required CI checks run
```

mine-core is a public module: no `GOPRIVATE` or git credential setup is needed.

## Running tests

| Command | Runs | Needs |
|---|---|---|
| `make test` | v2 unit tests | nothing |
| `make test-race` | v2 unit tests with the race detector — **what CI runs** | a C compiler (cgo) |
| `make test-integration` | v2 unit + integration tests | `make services-up` |
| `make test-v1` | v1 vet + race tests | a C compiler |
| `make lint` | golangci-lint on v2, at CI's pinned version | nothing |
| `make vuln` | govulncheck on both modules | nothing |
| `make links` | every relative link in every `.md` resolves | Python 3 |
| `make check` | build, vet, race tests, lint, tidy and links for both modules | a C compiler, Python 3 |

### Unit and integration tests

Test files **without** a build tag are unit tests: they need no infrastructure,
and they run against the memory backends. Files that start with
`//go:build integration` need a real Redis, MongoDB, MinIO or Postgres.

`make test` only sees the first group. **Code covered only by integration tests
is code the required CI checks never exercise**, so new behaviour always needs a
unit test against a memory backend as well.

### Integration services

```sh
make services-up        # redis, mongo (single-node replica set), minio, postgres
make test-integration
make services-down      # stops them and deletes their data
```

The services listen on their default ports — 6379, 27017, 9000 and 5432 — so
stop any local instances first. Everything is defined in
[`v2/compose.test.yaml`](./v2/compose.test.yaml), and the variables the tests
read are in the `Makefile`'s `INTEGRATION_ENV`.

Locally, an integration test whose service is unreachable **skips** rather than
fails. CI's integration workflow is stricter: a skip caused by a missing service
fails the run, so a broken setup cannot pass as green.

Two opt-ins:

- **Postgres for `coretest`.** `coretest` runs on SQLite by default;
  `TEST_DATABASE_URL` switches it to Postgres (`make test-integration` sets it).
  That is worth running before merging anything that touches a repository,
  because SQLite accepts SQL that Postgres rejects.
- **Real LLM provider.** The AI suite needs `APP_AI_PROVIDER`, `APP_AI_MODEL` and
  `APP_AI_API_KEY`. It never runs on pull requests.

## Conventions reviewers will ask about

### Comments explain *why*, not *what*

The code already says what it does. What it cannot say is why it isn't written
some other way.

```go
// ❌ increment the counter and set the ttl
// ✅ INCR then EXPIRE as two calls can lose the expiry to a crash in between,
//    leaving a counter that never resets. Doing both in one script is what
//    makes the fixed-window limiter correct.
```

The most valuable comment explains the alternative that was **not** chosen.

### A capability is never nil

A service that does not configure `CACHE_*` must still start, not panic. Every
capability therefore has a disabled implementation — and which kind is a
deliberate choice:

| Behaviour when unconfigured | Used for | Why |
|---|---|---|
| degrade silently | cache | a miss recomputes the value, which is still correct |
| fail loudly | mq, storage, mailer, pusher, mongo | a lost file or an unsent mail cannot be recovered |

A new capability always ships with its noop implementation.

### Method on the context, or a function taking a context

```go
ctx.DB()  ctx.Cache()  ctx.Log()        // capabilities the request *has*
core.Requester(ctx)  core.Mailer(ctx)   // things the code *does* to the outside
repository.New[User](ctx)
```

The function form works everywhere the method does, **plus** in functions that
only hold a `context.Context` — so a domain service does not have to import the
framework to send a mail. See [Context & App](./v2/docs/context.md).

### Errors are `IError`

Never return a raw error from any layer. `ctx.NewError(err, errmsgs.X)` is what
reports to Sentry with the request's scope — and logging again after returning
it makes one event look like two. See [Service errors](./v2/docs/service-errors.md).

### No global mutable state

No package-level `var` mutated at runtime; everything is built by a
constructor. `go test -race ./...` must pass cleanly.

### No generated mocks

Each capability exports a **memory implementation** instead —
`NewMemoryCache()`, `NewMemoryStorage()`, `NewMemoryMailer()`,
`NewMemoryPusher()`, `NewRecordingSentry()`. A new capability needs one too. It
is what lets services built on the framework write tests without mocking
anything.

### Tests use testify

`require` for preconditions the rest of the test cannot run without; `assert`
for the values being checked. **An assertion's message should say why the value
must be that**, not that it differs — testify already prints the difference.

```go
assert.Equal(t, DefaultJobTimeout.String(), got,
    "the effective default, not the zero the struct carries")
```

### Lint

`make lint` must pass. The config is [`v2/.golangci.yml`](./v2/.golangci.yml).
A `//nolint` must name its linter and give a reason. The rules at the bottom of
the config pin a handful of findings carried over from the original import;
fixing one of those and deleting its rule is a welcome first contribution.

## Documentation

- The framework docs live in [`v2/docs/`](./v2/docs), are plain Markdown, and
  are written in Thai. Match the language of the page you are editing.
- Link between pages with **relative paths including `.md`**, e.g.
  `[Cache](./cache.md)`. `make links` checks every one; CI does too.
- A new page must be linked from the index in [`v2/README.md`](./v2/README.md),
  or nobody will find it.
- Anything a user of the framework would notice needs a docs change in the same
  PR.

## Commits and pull requests

1. Fork, branch from `master`, and open the PR against `master`.
2. Fill in the PR template's checklist.
3. Make sure `make check` passes.

### The PR title is a Conventional Commit

PRs are **squash-merged**: the title becomes the commit on `master` and the
line in the release notes. A check enforces the format.

```
feat(v2): add retry budget to the requester
fix(v2): stop the consumer acking on handler panic
docs(v2): explain replica-set requirements for transactions
fix(v1): handle nil pagination options
chore(deps): bump go-redis to v9.23
```

| Type | Use for | Release notes |
|---|---|---|
| `feat` | new capability or API | ✨ Features |
| `fix` | bug fix | 🐛 Fixes |
| `perf` | performance improvement | ⚡ Performance |
| `docs` | documentation only | 📚 Documentation |
| `refactor`, `test`, `build`, `ci`, `chore`, `revert` | everything else | 🔧 Other changes |

Scopes: `v2`, `v1`, `deps`, `ci`, `docs`, `release`. Use `(v2)` or `(v1)` for code
changes, so a release note says which module a line belongs to.

### Breaking changes

Add `!` to the title and a `BREAKING CHANGE:` section to the PR description:

```
feat(v2)!: rename IStorage.Put to Upload

BREAKING CHANGE: IStorage.Put is removed. Replace calls with
IStorage.Upload(key, body, opts) — the arguments are unchanged.
```

That section is what people read when they upgrade. Write **how to migrate**,
not only what changed. Within v2, breaking changes are exceptional: the module
path promises compatibility across `v2.x` minors.

### The description explains the problem

Say what was wrong or missing, not only what you changed. The PR description is
the one place that context survives for whoever runs `git blame` in six months.

### What CI runs

| Workflow | When | Required |
|---|---|---|
| **CI** — v2 test (race + coverage), cross-platform build, lint, tidy, docs links, v1 test | every PR | ✅ via `CI OK` |
| **Pull request** — Conventional title; labels | every PR | ✅ `Conventional title` |
| **Integration** — v2 against real services | PRs touching `v2/` | — |
| **Security** — govulncheck, CodeQL | every PR, weekly | — |

Labels are applied automatically from the title and the paths you touched;
they are what groups the release notes.

## Releasing (maintainers)

Each module line follows [Semantic Versioning](https://semver.org)
independently. A breaking change to v2 would be a **v3** — a new module path
and a new directory, not a tag — which is why neither `./tag` nor the workflow
offers a major bump.

### Patch or minor release

**Actions → Tag release → Run workflow**, from `master`:

- **line** — `v2` or `v1`
- **level** — `patch` or `minor`
- **dry run** — tick it first to see the tag it would create

The workflow tests the module, pushes the tag, then calls **Release**, which:

1. checks the tagged tree's `go.mod` declares the module the tag claims;
2. builds and runs the race tests on exactly that tree;
3. publishes a GitHub release — notes generated since the previous release of
   *the same module*, grouped by label; v2 releases are marked *Latest*;
4. asks `proxy.golang.org` for the version, so `go get` and pkg.go.dev see it
   immediately.

### By hand

A pre-release or a backport can be tagged directly. Pushing the tag runs
Release; a suffix such as `-rc.1` marks it a pre-release.

```sh
git tag -a v2.3.0-rc.1 -m v2.3.0-rc.1
git push origin v2.3.0-rc.1
```

If a release run fails after its tag exists, re-run it from **Actions → Release
→ Run workflow** with that tag. It is safe to repeat.

### Never move or delete a published tag

The Go module proxy caches a version permanently the first time anyone requests
it. A moved tag leaves users and the proxy disagreeing about its contents, and
checksum verification fails for everyone. Fix forward with the next patch.

### The stale `v2.0.0`–`v2.1.0` tags

Those date from 2022, before `v2/` existed, so the v2 module cannot resolve at
them. Release refuses a v2 tag whose tree does not hold the v2 module, and skips
those tags when generating notes. **The first real v2 release is `v2.2.0`** —
run Tag release with `line: v2`, `level: minor`.

### One-time repository settings

The pipeline assumes these are configured under **Settings**:

- **Branches** — protect `master`: require a PR, and require the `CI OK` and
  `Conventional title` checks.
- **General → Pull Requests** — allow squash merging only, with the default
  commit message set to the PR title.
- **Code security** — enable private vulnerability reporting, Dependabot alerts
  and Dependabot security updates.
- **Secrets and variables → Actions** — optionally the `APP_AI_API_KEY` secret
  and the `APP_AI_PROVIDER` / `APP_AI_MODEL` variables, to run the AI integration
  suite.
