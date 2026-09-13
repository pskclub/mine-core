# Recipes & testing

## Storage is the bytes, the database is the index

The arrangement that stays healthy at every size:

```go
type Document struct {
    ID        string    `gorm:"column:id;primaryKey"`
    TenantID  string    `gorm:"column:tenant_id"`
    Key       string    `gorm:"column:s3_key"`        // the join
    Filename  string    `gorm:"column:filename"`      // what the user called it
    Size      int64     `gorm:"column:size"`
    Type      string    `gorm:"column:content_type"`
    CreatedAt *time.Time `gorm:"column:created_at"`
}
```

Everything queryable lives in the row; the object holds only bytes. Listing a
user's documents is then a `WHERE`, not a bucket scan
([why](./storage-download.md#listing-is-not-a-database-index)).

The two states this arrangement can get into, and what each costs:

| | Cost | Fix |
|---|---|---|
| object with no row | a few wasted bytes | a lifecycle rule on `tmp/` |
| row with no object | a broken page | upload **before** you commit |

Which is the whole reason for the ordering in
[Uploads and transactions](./storage-upload.md#uploads-and-transactions).

## Per-tenant isolation

```go
func tenantStore(c core.IHTTPContext) core.IStorage {
    // ContextUser มีแค่ ID/Email/Username/Name/Segment/Token/Data —
    // อะไรที่เป็นของ domain เก็บใน Data
    return c.Storage().WithPrefix("tenants/" + c.GetUser().Data["tenant_id"])
}
```

```go
store := tenantStore(c)
store.Put("avatar.png", src)         // tenants/42/avatar.png
objects, _ := store.List("")         // only this tenant's
n, _ := store.DeleteByPrefix("")     // only this tenant's
```

Building the handle once, from the authenticated user, is what makes the
isolation hard to forget: no call site ever writes the tenant id into a key, so
no call site can get it wrong.

It is still a namespace and not a permission — see
[Prefixes](./storage-setup.md#prefixes). For real isolation, the bucket policy
has to say so too.

## Generating a report a user downloads

```go
func ExportOrders(c core.ICronjobContext, tenantID string) error {
    key := fmt.Sprintf("tenants/%s/exports/%s.csv", tenantID, utils.NewUUID())

    pr, pw := io.Pipe()
    go func() {
        _ = pw.CloseWithError(writeOrdersCSV(c, pw, tenantID))
    }()

    if err := c.Storage().Put(key, pr, core.StoragePutOptions{
        ContentType: "text/csv",
        Attachment:  "orders.csv",
    }); err != nil {
        return err
    }

    export := Export{TenantID: tenantID, Key: key, Status: "ready"}
    if err := repository.New[Export](c).Create(&export); err != nil {
        return err
    }
    return notifyReady(c, export)
}
```

The report never exists as a file on disk and never exists whole in memory. The
row is what the user's page reads; the link is
[signed on demand](./storage-presign.md#presigned-get-temporary-download) when
they click.

## Cleaning up

```go
// user action: delete everything this tenant owns
n, err := ctx.Storage().WithPrefix("tenants/"+id).DeleteByPrefix("")

// deleting one document: the row and the object
if err := repository.New[Document](ctx).Where("id = ?", id).Delete(); err != nil {
    return err
}
_ = ctx.Storage().Delete(doc.Key)   // best effort — an orphan is cheap
```

Delete the row first. A row pointing at a deleted object is a broken page; an
object with no row is a few wasted bytes that a lifecycle rule sweeps.

Routine cleanup (`tmp/`, expired exports, abandoned uploads) belongs in a
**bucket lifecycle rule**, not a job — it runs whether or not the service is
deployed.

## Copying an object between environments

```go
prod, err := core.NewStorage(prodEnv)
if err != nil {
    return err
}
dev := ctx.Storage()

body, err := prod.Get(key)
if err != nil {
    return err
}
defer body.Close()
return dev.Put(key, body)
```

Streams end to end; nothing is buffered. Within one bucket use
[`Copy`](./storage-download.md#copying-and-moving), which does not move the bytes
at all.

## Testing

```go
store := core.NewMemoryStorage()
app, _ := core.NewApp(env, core.WithStorage(store))

// ... exercise the upload path, then assert on what was stored:
info, err := store.Stat("avatars/u1.png")
require.NoError(t, err)
require.Equal(t, "image/png", info.ContentType)
require.Equal(t, int64(1234), info.Size)
```

The memory backend is a real store in-process: keys, content types, metadata,
prefixes, listing and `Stat` all behave. Most storage tests need nothing else.

### Asserting on the whole path

```go
func TestUploadStoresAndRecords(t *testing.T) {
    store := core.NewMemoryStorage()
    app, _ := core.NewApp(env, core.WithStorage(store), core.WithSQL("default", db))

    rec := postMultipart(t, app, "/uploads", "file", "cv.pdf", pdfBytes)
    require.Equal(t, http.StatusOK, rec.Code)

    var body struct{ Key string }
    require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

    // the object is there
    got, err := store.GetBytes(body.Key)
    require.NoError(t, err)
    require.Equal(t, pdfBytes, got)

    // the key was generated, not taken from the filename
    require.NotContains(t, body.Key, "cv.pdf")

    // and the row points at it
    doc, err := repository.New[Document](app.NewContext(context.Background())).
        FindOne("s3_key = ?", body.Key)
    require.NoError(t, err)
    require.Equal(t, "cv.pdf", doc.Filename)
}
```

See [HTTP tests](./testing-http.md) for the request helpers.

### Testing the disabled path

Storage failing loudly is a *feature*, so it is worth a test:

```go
app, _ := core.NewApp(env, core.WithStorage(core.NewNoopStorage()))
ctx := app.NewContext(context.Background())

err := ctx.Storage().Put("k", strings.NewReader("v"))
require.ErrorIs(t, err, core.ErrStorageDisabled)
```

### What the memory backend does not prove

| Passes in memory, can still be wrong | Because |
|---|---|
| presigned URLs | there is no signer and no server to honour the signature |
| `PublicURL` shape | it is built from configuration the memory backend does not have |
| multipart uploads of large bodies | no parts, no part size |
| bucket policies, ACLs, `Public` | there is no bucket |
| `S3()` | it returns **nil** |

Run the real driver against MinIO with `make test-integration` and the `S3_*`
keys set — see the header of `s3_integration_test.go` and
[Integration tests](./testing-integration.md).

```sh
docker run -p 9000:9000 minio/minio server /data
APP_S3_ENDPOINT=127.0.0.1:9000 APP_S3_BUCKET=coretest \
  APP_S3_ACCESS_KEY=minioadmin APP_S3_SECRET_KEY=minioadmin \
  APP_S3_FORCE_PATH_STYLE=true make test-integration
```

## Best practices

- **database เก็บ index, storage เก็บ bytes** — แถวในตารางคือความจริงว่าไฟล์มีอยู่
  ส่วน bucket คือที่ที่ไบต์นอนอยู่ ลำดับที่ถูกคือ upload ให้สำเร็จก่อน แล้วค่อยบันทึกแถว
- **key มีโครงสร้างตั้งแต่แรก** — `tenant/<id>/orders/<order>/<uuid>.pdf` ทำให้ลบทั้งชุด,
  แยกสิทธิ์ และย้าย environment ได้โดยไม่ต้องเดา
- **อย่าใช้ชื่อไฟล์ของผู้ใช้เป็น key** — สร้าง uuid แล้วเก็บชื่อเดิมไว้ในฐานข้อมูล
  (ชื่อไฟล์คือ input ที่ไม่ควรเชื่อ ทั้งเรื่อง path traversal และเรื่องชนกัน)
- **presign TTL สั้นที่สุดเท่าที่ใช้งานได้** — ลิงก์คือสิทธิ์เข้าถึงที่ส่งต่อกันได้
- **ตรวจ content type และขนาดจากไบต์จริง** ไม่ใช่จากสิ่งที่ client บอก
- **ไฟล์ใหญ่ต้อง stream** ไม่ใช่โหลดทั้งก้อนเข้า memory — และงานแปลงไฟล์ควรเป็น
  [job](./jobs.md) ไม่ใช่ทำในคำขอ
- **มี job เก็บกวาด orphan** — ไฟล์ที่ upload สำเร็จแต่ transaction rollback จะไม่มีใคร
  อ้างถึงมันอีกเลย
- **storage ไม่ degrade เงียบ** — ไม่ได้ตั้งค่าแล้วทุกคำสั่ง fail ซึ่งเป็นสิ่งที่ต้องการ
