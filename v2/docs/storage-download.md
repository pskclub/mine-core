# Reading & listing

## Reading

```go
body, err := ctx.Storage().Get("avatars/u1.png")   // streams
if errors.Is(err, core.ErrObjectNotFound) {
    return c.NewError(err, errmsgs.NotFound)       // a 404, not a server error
}
defer body.Close()
io.Copy(dst, body)

data, err := ctx.Storage().GetBytes("small.json")  // whole object in memory
```

`Get` returns an `io.ReadCloser`. **Close it** — an unclosed body holds an HTTP
connection from the SDK's pool, and a handler that leaks one per request runs out
of connections rather than out of memory, which is a much more confusing outage.

`GetBytes` is for small objects only. It is exactly `Get` plus `io.ReadAll`, and
the size limit is however much memory you are willing to spend per concurrent
caller.

### Serving an object through the service

```go
func Download(c core.IHTTPContext) error {
    info, err := c.Storage().Stat(key)
    if errors.Is(err, core.ErrObjectNotFound) {
        return c.NewError(err, errmsgs.NotFound)
    }
    if err != nil {
        return err
    }

    body, err := c.Storage().Get(key)
    if err != nil {
        return err
    }
    defer body.Close()

    c.Response().Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
    return c.Stream(http.StatusOK, info.ContentType, body)
}
```

Do this when access has to be **checked** — a private document, a per-user file.
Otherwise prefer a [presigned link](./storage-presign.md): proxying moves every
byte through your service, and one large download holds a request slot for its
whole duration.

## Metadata

```go
info, err := ctx.Storage().Stat("avatars/u1.png")
```

```go
type StorageObject struct {
    Key          string    // without the storage prefix
    Size         int64
    ContentType  string
    ETag         string
    LastModified time.Time
    Metadata     map[string]string
}
```

`Stat` transfers no object body, so it is the cheap way to answer "how big is
it", "when did it change", "what did we store alongside it".

```go
ok, err := ctx.Storage().Exists("avatars/u1.png")   // absence is not an error
```

`Exists` is `Stat` with the not-found case folded into `false`. Use it for a
check; use `Stat` when you want the answer *and* the metadata, so you do not pay
for two round trips.

> An `Exists` before a `Get` is two round trips to answer one question, and
> the object can disappear between them. Just `Get` and handle
> `ErrObjectNotFound`.

## Listing

```go
objects, err := ctx.Storage().List("users/1/")
objects, err = ctx.Storage().List("users/", core.StorageListOptions{Limit: 100})
```

`List` follows pagination for you. Without a `Limit` it returns **every** object
under the prefix, which for a large bucket is many round trips and a large slice
— pass one unless the prefix is known to be small.

Keys come back **without** the storage prefix, the same names they were written
under, so a listing can be fed straight back into `Get` or `Delete`.

```go
tenant := ctx.Storage().WithPrefix("tenants/" + id)
for _, obj := range mustList(tenant.List("exports/")) {
    tenant.Get(obj.Key)      // the same name that came back
}
```

### Listing is not a database index

S3 lists keys in lexicographic order. There is no "sort by date", no "the ten
largest", no filtering beyond the prefix — anything else means listing everything
and sorting in memory.

When you need to query the objects, keep a row per object:

| Question | Ask |
|---|---|
| "the files in this folder" | `List` |
| "this user's five most recent uploads" | the database |
| "everything larger than 10MB" | the database |
| "does this key exist" | `Exists` |

The design that stays healthy: the database is the index, storage holds the
bytes, and the key is the join. A service that treats `List` as a query gets
slower every month whether or not anybody changed it.

## Copying and moving

```go
err := ctx.Storage().Copy("tmp/upload.png", "avatars/u1.png")   // server-side
err = ctx.Storage().Move("tmp/upload.png", "avatars/u1.png")    // copy + delete
```

`Copy` happens **inside S3** — the bytes never travel to your service, so copying
a 2GB object costs one API call rather than 2GB of transfer in each direction.

`Move` is a copy followed by a delete, because S3 has no rename. Which means it
is not atomic: a failure between the two leaves both keys. For the promote-a-
temporary-upload pattern, that failure mode is harmless — the temporary key is
swept by a lifecycle rule.

## Deleting

```go
err := ctx.Storage().Delete("a.png", "b.png")          // batched, 1000 per call
n, err := ctx.Storage().DeleteByPrefix("users/1/")     // a whole "folder"
```

`Delete` takes any number of keys and batches them into calls of 1000, which is
S3's limit. Absent keys are not an error — deleting something twice is fine, and
that makes cleanup code idempotent for free.

`DeleteByPrefix` lists and then deletes, so it is *O(objects)*: a prefix holding
a hundred thousand objects is a hundred round trips. Fine for a user action
("delete this tenant"); wrong for a routine sweep, which belongs in a
[bucket lifecycle rule](./storage-setup.md#lifecycle-rules-belong-on-the-bucket).

> `DeleteByPrefix("")` on a handle with no prefix deletes the **whole bucket's
> contents**. There is no confirmation and no undo unless the bucket has
> versioning. Build the prefix explicitly, and never from user input.

### Deleting is forever

Unless the bucket has versioning or object lock, a delete is permanent — there is
no recycle bin. For anything a user can trigger, a soft delete is usually the
right shape: mark the row deleted, and let a lifecycle rule remove the object
after a grace period.

## Streaming between two stores

Reading and writing both stream, so moving an object between buckets or backends
needs no temporary file and no full buffer:

```go
body, err := src.Get(key)
if err != nil {
    return err
}
defer body.Close()

info, err := src.Stat(key)
if err != nil {
    return err
}
return dst.Put(key, body, core.StoragePutOptions{
    ContentType: info.ContentType,
    Metadata:    info.Metadata,
})
```

Within one bucket, use `Copy` instead — it does not move the bytes at all.
