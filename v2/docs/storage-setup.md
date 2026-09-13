# Setup & backends

```go
store, err := core.NewStorage(env)
if err != nil {
    panic(err)
}
app, _ := core.NewApp(env, core.WithStorage(store))
```

## Configuration

```sh
S3_BUCKET=my-bucket           # the only required key
S3_REGION=ap-southeast-1      # this is also the default
S3_ACCESS_KEY=...             # omit both to use the AWS default chain
S3_SECRET_KEY=...             #   (instance role, IRSA, ~/.aws, AWS_* env)
S3_ENDPOINT=minio:9000        # optional, for S3-compatible servers
S3_HTTPS=false                # scheme for an endpoint written without one
S3_FORCE_PATH_STYLE=true      # usually needed for MinIO
S3_PREFIX=myservice           # namespace every key
S3_PUBLIC_URL=https://cdn...  # base address PublicURL builds on
```

Every key and its default is in [Configuration → Storage](./env.md#storage-s3-minio).

## Credentials

Leaving `S3_ACCESS_KEY` and `S3_SECRET_KEY` **unset** is the good path in a
deployment: the AWS default chain then finds credentials from the instance role,
IRSA, `~/.aws`, or the `AWS_*` environment variables. Nothing long-lived is in
your configuration, and rotation is somebody else's problem.

Set them explicitly for MinIO, for a local compose file, and for providers with
no role mechanism (R2, Spaces).

| Deployment | Credentials |
|---|---|
| EKS / ECS / EC2 | unset — instance role or IRSA |
| local compose with MinIO | explicit keys |
| Cloudflare R2, DO Spaces | explicit keys + `S3_ENDPOINT` |
| CI | explicit keys from the secret store |

Whatever the source, the policy should be scoped to the bucket and prefix this
service uses. A service that only writes `myservice/` should not be able to read
another's objects — a prefix is not a boundary unless the *policy* makes it one.

## S3-compatible services

**MinIO**

```sh
S3_ENDPOINT=minio:9000
S3_HTTPS=false                 # http://minio:9000
S3_FORCE_PATH_STYLE=true       # MinIO does not do virtual-host style
S3_ACCESS_KEY=minioadmin
S3_SECRET_KEY=minioadmin
S3_BUCKET=dev
S3_REGION=ap-southeast-1       # ignored by MinIO, required by the signer
```

**Cloudflare R2**

```sh
S3_ENDPOINT=https://<account>.r2.cloudflarestorage.com
S3_BUCKET=my-bucket
S3_REGION=auto
S3_ACCESS_KEY=...
S3_SECRET_KEY=...
S3_PUBLIC_URL=https://cdn.example.com
```

`S3_HTTPS` only matters when the endpoint was written **without** a scheme, as it
usually is in a compose file (`minio:9000`). An endpoint that already says
`https://` is used as it is.

`S3_REGION` is ignored by most S3-compatible servers, but the request signer
requires *some* region — leaving it unset gives you `ap-southeast-1` (Singapore).
Against real AWS S3 the region must match the bucket's, so set it when the
bucket lives elsewhere.

## The memory backend

```go
core.NewMemoryStorage()   // in-process, real metadata and prefixes
```

A real store in the same process: keys, content types, metadata, prefixes and
`Stat` all behave. It is what tests and single-instance tools should use — see
[Testing](./storage-patterns.md#testing).

```go
core.NewNoopStorage()     // every call fails with STORAGE_DISABLED
```

`NewNoopStorage` is what a service with no `S3_*` gets. Wiring it explicitly is
how you assert that a code path fails loudly rather than silently skipping an
upload.

## Prefixes

`S3_PREFIX` namespaces every key — set it per service (and per environment) when
several share a bucket:

```sh
S3_PREFIX=orders-staging
```

`WithPrefix` narrows further, which is how a per-tenant folder stays a folder
nobody can escape by crafting a key:

```go
tenant := ctx.Storage().WithPrefix("tenants/" + tenantID)

tenant.Put("avatar.png", r)              // tenants/42/avatar.png
objects, _ := tenant.List("")            // keys come back *without* the prefix
```

Keys come back stripped, so code above the handle never sees the prefix and
cannot accidentally double it. `Key()` shows what an operation would really use:

```go
tenant.Key("avatar.png")   // "<S3_PREFIX>tenants/42/avatar.png"
```

> A prefix is a namespace, not a permission. It stops your own code from
> colliding; it does not stop code that constructs its own handle. When tenants
> must be isolated for real, the bucket policy or separate buckets are what
> enforce it.

## Health checks

```go
if err := ctx.Storage().Ping(); err != nil {
    // readiness
}
```

`Ping` heads the bucket, which needs permission a narrowly object-scoped
credential may not have. A `Ping` that fails while `Get` and `Put` work is a
policy question, not an outage — check before wiring it into a probe that can
take the service out of rotation.

## The raw client

```go
client := ctx.Storage().S3()    // nil for the memory and disabled backends
if client == nil {
    return ctx.NewError(nil, errmsgs.InternalServerError)
}
```

Versioning, tagging, lifecycle rules, object lock, multipart control —
everything the interface does not wrap. Two things to remember: it is **nil** on
the memory and disabled backends, and it does **not** apply the prefix — build
keys with `Key()`.

## Lifecycle rules belong on the bucket

Temporary objects (`tmp/`, expired exports, abandoned uploads) should be swept by
a **bucket lifecycle rule**, not by a job in your service. The rule runs whether
or not the service is deployed, costs nothing, and cannot be the thing that broke
last Tuesday.

Reach for `DeleteByPrefix` for the deletions that are part of a user action —
"delete this tenant's files" — and leave the routine sweeping to the bucket.
