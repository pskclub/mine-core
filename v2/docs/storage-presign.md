# Links

A presigned URL lets a client read or write **one** object, for a limited time,
without credentials. It is the mechanism that keeps large files out of your
service entirely.

## Presigned GET (temporary download)

```go
url, err := ctx.Storage().PresignGet("docs/1.pdf", 15*time.Minute)

// ...that downloads under a chosen filename
url, err = ctx.Storage().PresignGet("docs/1.pdf", 15*time.Minute,
    core.StoragePresignOptions{Attachment: "report.pdf"})

// ...or is served as a different type than it was stored as
url, err = ctx.Storage().PresignGet("docs/1.pdf", 15*time.Minute,
    core.StoragePresignOptions{ContentType: "application/octet-stream"})
```

The pattern in a handler: check permission, then hand back a link.

```go
func DocumentURL(c core.IHTTPContext) error {
    doc, err := repository.New[Document](c).FindOne("id = ?", c.Param("id"))
    if err != nil {
        return err
    }
    if doc.OwnerID != c.GetUser().ID {
        return c.NewError(nil, errmsgs.Forbidden)
    }

    url, err := c.Storage().PresignGet(doc.Key, 5*time.Minute,
        core.StoragePresignOptions{Attachment: doc.Filename})
    if err != nil {
        return err
    }
    return c.JSON(http.StatusOK, echo.Map{"url": url})
}
```

Your service does the authorisation; S3 does the transfer. A 500MB download then
costs you one request that returns 200 bytes.

### Choosing the TTL

A presigned URL is a **bearer token in a query string**. Anyone holding it can
use it until it expires — it will appear in browser history, in `Referer`
headers, in the chat message where somebody pasted it, and in whatever logs sat
in between.

| TTL | Fits |
|---|---|
| 1–5 minutes | a link the page uses immediately |
| 15–60 minutes | something a human will click |
| hours or days | almost never — use a public object or a proxy endpoint |

Short and re-issued beats long and convenient. Re-issuing costs one API call and
re-checks permission; a long link cannot be revoked at all.

> Signing a URL does **not** read the object, so `PresignGet` on a key that does
> not exist succeeds and returns a link that 404s. Call `Exists` first if the
> caller needs to be told now rather than later.

## Presigned PUT (direct browser upload)

The file never passes through the service:

```go
url, err := ctx.Storage().PresignPut("uploads/new.png", 5*time.Minute,
    core.StoragePutOptions{ContentType: "image/png"})
```

The client uploading through a `PresignPut` link **must** send the same
`Content-Type` the link was signed with, or S3 rejects the signature. That is the
single most common thing to get wrong here, and the error message from S3 says
almost nothing about it.

```js
await fetch(url, {
  method: 'PUT',
  headers: { 'Content-Type': 'image/png' },   // must match exactly
  body: file,
})
```

### The full flow

```go
// 1. the client asks for a slot; the service decides the key
func RequestUpload(c core.IHTTPContext) error {
    var body struct {
        ContentType string `json:"content_type" validate:"required,oneof=image/png image/jpeg"`
    }
    if err := c.BindWithValidate(&body); err != nil {
        return err
    }

    key := fmt.Sprintf("tenants/%s/uploads/%s", c.GetUser().ID, utils.NewUUID())

    url, err := c.Storage().PresignPut(key, 5*time.Minute,
        core.StoragePutOptions{ContentType: body.ContentType})
    if err != nil {
        return err
    }
    return c.JSON(http.StatusOK, echo.Map{"url": url, "key": key})
}

// 2. the client PUTs the file straight to S3

// 3. the client tells the service it is done; the service verifies
func ConfirmUpload(c core.IHTTPContext) error {
    var body struct {
        Key string `json:"key" validate:"required"`
    }
    if err := c.BindWithValidate(&body); err != nil {
        return err
    }
    if !strings.HasPrefix(body.Key, "tenants/"+c.GetUser().ID+"/") {
        return c.NewError(nil, errmsgs.Forbidden)   // not this user's key
    }

    info, err := c.Storage().Stat(body.Key)
    if errors.Is(err, core.ErrObjectNotFound) {
        return c.NewError(err, errmsgs.BadRequest)  // nothing was uploaded
    }
    if err != nil {
        return err
    }
    if info.Size > 10<<20 {
        _ = c.Storage().Delete(body.Key)
        return c.NewError(nil, emsgs.FileTooLarge)
    }

    doc := Document{Key: body.Key, Size: info.Size, Type: info.ContentType}
    return repository.New[Document](c).Create(&doc)
}
```

Three things that flow gets right:

- **The service chooses the key.** A client that names its own key can overwrite
  somebody else's object.
- **Step 3 re-checks ownership from the key**, because step 2 happened without
  the service watching.
- **`Stat` verifies the upload actually happened**, and its size — the only size
  limit you can enforce after the fact, since the bytes never came through you.

A presigned PUT cannot enforce a maximum size. If that matters, use a **POST
policy** (which can), or accept the upload through the service.

## Public URLs

For objects that are public anyway, `PublicURL` builds the unsigned address —
from `S3_PUBLIC_URL` when set, so a CDN in front of the bucket is one config key
rather than a code change:

```go
url := ctx.Storage().PublicURL("avatars/u1.png")
```

```sh
S3_PUBLIC_URL=https://cdn.example.com
```

`PublicURL` says **nothing** about whether the object is actually readable — it
is a string built from configuration, not a permission check. Whether it works is
the bucket policy's decision.

| Use | For |
|---|---|
| `PublicURL` | avatars, logos, marketing assets — anything genuinely public |
| `PresignGet` | anything where the answer to "who can read this" is not "everyone" |

Public objects are also the ones that benefit most from a long `CacheControl` at
[upload time](./storage-upload.md#cachecontrol) — with a CDN in front, they stop
reaching the bucket at all.

## Presigning from a background context

Signing is local — it makes no network call — so it works from any context. But
the handle is still request-bound, and a URL signed for 15 minutes outlives the
request that produced it, which is fine: the *link* carries no context.

```go
// a job that emails a download link
url, err := ctx.Storage().WithContext(context.Background()).
    PresignGet(key, time.Hour, core.StoragePresignOptions{Attachment: name})
```

An hour is at the outer edge of reasonable. Prefer emailing a link to *your*
endpoint, which checks permission and re-signs a short URL each time it is
clicked.
