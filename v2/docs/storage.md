# Storage

`core.IStorage` — object storage over
[aws-sdk-go-v2](https://github.com/aws/aws-sdk-go-v2), compatible with S3 and
S3-compatible services (MinIO, Cloudflare R2, DigitalOcean Spaces).

`ctx.Storage()` returns a handle bound to the request context, so methods take no
`ctx` — and a cancelled request stops the upload it started.

```go
err := ctx.Storage().Put("avatars/u1.png", file)
url, err := ctx.Storage().PresignGet("avatars/u1.png", 15*time.Minute)
```

## What this section covers

| Page | |
|---|---|
| [Setup & backends](./storage-setup.md) | S3, MinIO, R2, credentials, prefixes, the memory backend |
| [Uploading](./storage-upload.md) | `Put`, content types, metadata, uploads from a request |
| [Reading & listing](./storage-download.md) | streaming, `Stat`, `List`, copy, move, delete |
| [Links](./storage-presign.md) | presigned GET and PUT, public URLs, CDNs |
| [Recipes & testing](./storage-patterns.md) | direct browser upload, tenant isolation, cleanup |

## It does not degrade quietly

Set no `S3_*` at all and `ctx.Storage()` still works — but every call returns a
`STORAGE_DISABLED` error naming the missing configuration.

That is deliberately different from the [cache](./cache.md), and the reason is
worth stating: a cache miss is recoverable, because the value can be recomputed.
An upload that was quietly dropped is a file the caller believes it saved and
nobody can get back.

```go
if !ctx.Storage().Enabled() {
    // an honest branch, if a path can genuinely work without a bucket
}
```

## The mental model

Object storage is not a filesystem, and most storage bugs come from treating it
like one:

| It looks like | It actually is |
|---|---|
| directories | a flat key space; `a/b/c.png` is one key containing slashes |
| `mv` | a copy followed by a delete — there is no rename |
| a mutable file | an immutable object; writing "changes" it by replacing it whole |
| `ls` | a paginated scan of keys sharing a prefix |
| a local disk | a network call, with network latency and network failures |

Two practical consequences. **Deleting a "folder"** means listing and deleting
every key under a prefix (`DeleteByPrefix`), which is *N* operations rather than
one. And **listing is not free** — see
[Reading & listing](./storage-download.md#listing).

## Keys

A key is the whole address of an object. Give it a structure, and make the parts
you did not choose unguessable:

```
avatars/<user-id>.png              stable: overwriting replaces the avatar
uploads/<uuid><ext>                unguessable: a user cannot address another's
tenants/<tenant-id>/exports/<uuid>.csv
tmp/<uuid>                         swept by a lifecycle rule
```

Never build a key from a filename a user supplied. `../../etc/passwd` is not a
path traversal here — the key is flat — but `report.pdf` from two users is one
object, and the second overwrites the first. Generate the name; keep the
original in `Content-Disposition` or in your database.

```go
key := "uploads/" + utils.NewUUID() + path.Ext(file.Filename)
```

## Errors

| Condition | What comes back |
|---|---|
| the key is absent | error wrapping `core.ErrObjectNotFound` |
| no `S3_*` configured | error wrapping `core.ErrStorageDisabled` (`STORAGE_DISABLED`) |

```go
body, err := ctx.Storage().Get(key)
if errors.Is(err, core.ErrObjectNotFound) {
    return c.NewError(err, errmsgs.NotFound)   // a 404, not a server error
}
```

`Exists` reports absence as `false` rather than an error, so a check is a check.

## The interface

```go
type IStorage interface {
    Put(key string, body io.Reader, opts ...StoragePutOptions) IError
    PutBytes(key string, data []byte, opts ...StoragePutOptions) IError
    Get(key string) (io.ReadCloser, IError)
    GetBytes(key string) ([]byte, IError)
    Stat(key string) (StorageObject, IError)
    Exists(key string) (bool, IError)
    List(prefix string, opts ...StorageListOptions) ([]StorageObject, IError)
    Copy(srcKey, dstKey string) IError
    Move(srcKey, dstKey string) IError
    Delete(keys ...string) IError
    DeleteByPrefix(prefix string) (int64, IError)
    PresignGet(key string, ttl time.Duration, opts ...StoragePresignOptions) (string, IError)
    PresignPut(key string, ttl time.Duration, opts ...StoragePutOptions) (string, IError)
    PublicURL(key string) string
    Ping() IError                              // readiness probe
    Enabled() bool
    Bucket() string
    Key(key string) string                     // full key, incl. prefix
    WithPrefix(prefix string) IStorage
    WithContext(ctx context.Context) IStorage  // escape hatch
    S3() *s3.Client                            // raw client; nil for memory/disabled
}
```

`Get` streams the object (`io.ReadCloser`) — remember to `Close()` it.
