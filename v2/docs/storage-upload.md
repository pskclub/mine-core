# Uploading

```go
// content type is guessed from the extension
err := ctx.Storage().Put("avatars/u1.png", fileReader)

// ...or given, along with anything else S3 stores
err = ctx.Storage().Put("reports/2026.pdf", r, core.StoragePutOptions{
    ContentType:  "application/pdf",
    Attachment:   "รายงานประจำปี.pdf",   // Content-Disposition, non-ASCII safe
    CacheControl: "public, max-age=31536000",
    Metadata:     map[string]string{"owner": "u-1"},
})

err = ctx.Storage().PutBytes("keys/1.pem", pem)
```

A large or unknown-size body is uploaded in parts, so memory is bounded by the
part size rather than by the file. `Put` therefore takes any `io.Reader` — an
HTTP request body, a `*os.File`, a pipe from a generator — without buffering the
whole thing first.

## Options

```go
type StoragePutOptions struct {
    ContentType        string
    Attachment         string   // Content-Disposition, as a download filename
    ContentDisposition string   // set verbatim, overriding Attachment
    ContentEncoding    string
    CacheControl       string
    Metadata           map[string]string
    Public             bool
}
```

### ContentType

Guessed from the key's extension when empty, falling back to
`application/octet-stream`. It matters more than it looks: it is what the browser
trusts when the object is served, so a PNG stored as `application/octet-stream`
downloads instead of rendering, and an HTML file stored as `text/html` **renders**
— which is a stored-XSS hole if users can upload one.

```go
// ✅ decide the type yourself for anything a user supplied
ctx.Storage().Put(key, src, core.StoragePutOptions{
    ContentType: "application/octet-stream",
    Attachment:  file.Filename,     // forces a download, never renders
})
```

Trusting the browser's `Content-Type` header on an upload is trusting the
uploader. Sniff it, allowlist it, or force a download.

### Attachment

Sets `Content-Disposition` so the object downloads under a chosen filename
instead of rendering inline. Non-ASCII names are encoded correctly, so a Thai
filename arrives as a Thai filename:

```go
core.StoragePutOptions{Attachment: "รายงานประจำปี.pdf"}
```

This is what lets the *key* be a UUID while the user still gets a sensible
filename — which is the right arrangement, because the key must be unguessable
and the filename must be readable, and those are different jobs.

### CacheControl

Baked into the object at write time and served on every read. It is what makes a
CDN in front of the bucket worth having:

```go
// immutable content under a content-addressed key
CacheControl: "public, max-age=31536000, immutable"

// something that can change under a stable key
CacheControl: "public, max-age=60"
```

An object written with a long `max-age` under a **stable** key cannot be updated
in any meaningful sense — caches will hold the old one. Long caching wants
content-addressed keys.

### Metadata

Becomes `x-amz-meta-*` headers, returned again by `Stat`:

```go
Metadata: map[string]string{"owner": "u-1", "original": file.Filename}
```

Useful for small facts that should travel with the object. It is **not** a
database: it cannot be queried, it is only visible per-object, and header values
must be ASCII. Anything you will search by belongs in a row.

### Public

Marks the object world-readable via an object ACL. It only works on buckets that
still allow ACLs — **most modern S3 buckets do not**, and serve public objects
through a bucket policy or a CDN instead. Prefer configuring the bucket over
marking each object.

## Uploading from an HTTP request

```go
func Upload(c core.IHTTPContext) error {
    file, err := c.FormFile("file")
    if err != nil {
        return c.NewError(err, errmsgs.BadRequest)
    }
    src, err := file.Open()
    if err != nil {
        return c.NewError(err, errmsgs.BadRequest)
    }
    defer src.Close()

    key := "uploads/" + utils.NewUUID() + path.Ext(file.Filename)
    if err := c.Storage().Put(key, src, core.StoragePutOptions{
        ContentType: file.Header.Get("Content-Type"),
    }); err != nil {
        return err
    }
    return c.JSON(http.StatusOK, echo.Map{"key": key})
}
```

Four things that version gets right, and that a hand-rolled one usually does not:

1. **The key is generated.** `file.Filename` is attacker-controlled; two users
   uploading `cv.pdf` must not collide.
2. **The reader is closed.** `defer src.Close()`.
3. **Nothing is buffered.** `src` streams straight through.
4. **The key is returned**, so the client can reference the object without the
   service having to guess a URL for it.

### Validating before you store

`emsgs` ในตัวอย่างนี้คือ error ของ service เอง — `FileTooLarge` / `UnsupportedFileType`
ไม่ได้อยู่ใน `errmsgs` ของ core เพราะข้อจำกัดว่าไฟล์ใหญ่แค่ไหนและชนิดไหนที่รับได้
เป็นเรื่องของแต่ละ service ([Service Errors](./service-errors.md))

```go
const maxUpload = 10 << 20   // 10 MiB

if file.Size > maxUpload {
    return c.NewError(nil, emsgs.FileTooLarge)
}

switch file.Header.Get("Content-Type") {
case "image/png", "image/jpeg", "application/pdf":
default:
    return c.NewError(nil, emsgs.UnsupportedFileType)
}
```

`file.Size` comes from the multipart parser, so it is the real size — but the
whole body has already reached your process by then. To reject earlier, bound the
request body in middleware (echo's `BodyLimit`), and for genuinely large files
skip the service entirely with a
[presigned upload](./storage-presign.md#presigned-put-direct-browser-upload).

The `Content-Type` header is the client's claim. For anything where the type
matters, sniff the first 512 bytes with `http.DetectContentType` and compare.

## Writing a generated file

Anything that produces bytes can stream into a `Put` without a temporary file, by
generating on one side of a pipe:

```go
pr, pw := io.Pipe()

go func() {
    err := writeCSV(pw, rows)      // whatever produces the bytes
    _ = pw.CloseWithError(err)     // an error here fails the Put
}()

if err := ctx.Storage().Put(key, pr, core.StoragePutOptions{
    ContentType: "text/csv",
    Attachment:  "report.csv",
}); err != nil {
    return err
}
```

`CloseWithError` is the part that matters: without it, a generator that fails
half way produces a truncated object that uploads *successfully*.

## Uploads and transactions

An upload cannot be rolled back. When a database write and an upload have to
agree, the order that fails safest is:

1. upload to a temporary or content-addressed key
2. commit the row referencing it
3. (optionally) `Move` it into place

An orphaned object is a wasted byte, cleaned up by a lifecycle rule. A row
pointing at an object that was never written is a broken page.

```go
tmpKey := "tmp/" + utils.NewUUID()
if err := ctx.Storage().Put(tmpKey, src); err != nil {
    return err
}

if err := repo.Transaction(func(tx *gorm.DB) error {
    doc.Key = tmpKey
    return repository.NewWithDB[Document](ctx, tx).Create(&doc)
}); err != nil {
    _ = ctx.Storage().Delete(tmpKey)   // best effort; the lifecycle rule is the backstop
    return err
}
```

Never do the upload *inside* the transaction — see
[Transactions](./database-transactions.md#what-belongs-inside).

## Long uploads and the request context

`ctx.Storage()` is bound to the request, so a client that hangs up aborts the
upload. That is usually right. For an upload that must finish regardless — one
started by a request but owned by a [job](./jobs.md) — bind another context:

```go
bg, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
defer cancel()

err := ctx.Storage().WithContext(bg).Put(key, r)
```
