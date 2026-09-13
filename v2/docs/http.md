# HTTP Layer

Built on **Echo v5**. `core.IHTTPContext` คือ `core.IContext` (ทุก capability)
บวกกับผิวของ echo (request/response)

> **echo v5 เปลี่ยน `echo.Context` จาก interface เป็น struct** — `IHTTPContext`
> จึง embed มันไม่ได้อีก แต่ประกาศ method ที่ handler ใช้จริงไว้เองแทน
> (`JSON`, `Param`, `QueryParam`, `Bind`, `Request`, …) **โค้ด handler เดิมจึงไม่ต้องแก้**
> สิ่งที่ต้องแก้มีสองอย่าง ดู [Migrating to echo v5](#migrating-to-echo-v5) ท้ายหน้า

## Server

```go
e := core.NewHTTPServer(app, &core.HTTPOptions{
    AllowOrigins: []string{"https://app.example.com"},
})
e.POST("/users", controller.CreateUser)
core.StartHTTPServer(e, env) // blocking in dev; graceful shutdown otherwise
```

```go
e := core.NewHTTPServer(app, &core.HTTPOptions{
    DisableRequestLog: core.BoolPtr(true),   // ปิด access log ของ server นี้
})
```

ปิดทั้งระบบด้วย `APP_LOG_REQUEST=false` — option ชนะ env เสมอ

The stack applies request-id, structured request logging, panic recovery
(panic → 500, never a crash), and CORS. The error handler renders any
`core.IError` as `{code, message, fields}` with the right status.

`NewHTTPServer` returns a `*core.Server` that **remembers the App** — route
methods take the handler directly (no `app` threaded through every call). It
embeds `*echo.Echo`, so all Echo methods (`Use`, `Static`, `Pre`, …) still work.

## Route groups

```go
e := core.NewHTTPServer(app, nil)

api := e.Group("/api", authMiddleware)   // *core.Group, also carries the App
api.GET("/users", ListUsers)
api.POST("/users", CreateUser)

v2 := api.Group("/v2")                    // nested groups work too
v2.GET("/users/:id", GetUser)
```

Route methods: `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, each accepting a
`core.HandlerFunc` plus optional `echo.MiddlewareFunc`.

> Escape hatch: for raw Echo registration you can still use
> `e.Echo.POST(path, core.WithHTTPContext(app, h))`.

## Handlers

```go
type HandlerFunc func(c core.IHTTPContext) error

func CreateUser(c core.IHTTPContext) error {
    var req CreateUserRequest
    if err := c.BindWithValidate(&req); err != nil {
        return err
    }
    // c.DB(), c.Cache(), repository.New[T](c) — all ambient-context
    return c.JSON(201, result)
}
```

## Binding

`BindOnly` / `BindWithValidate` bind **every request source** (like echo), using
struct tags:

| Source | Tag | Example |
|---|---|---|
| Path params | `param:"id"` | `/users/:id` |
| Query params | `query:"q"` | `?q=foo` (all methods, incl. GET) |
| JSON body | `json:"name"` | `Content-Type: application/json` |
| Form / multipart | `form:"name"` | `application/x-www-form-urlencoded`, `multipart/form-data` |
| XML body | `xml:"name"` | `application/xml` |

```go
type UpdateUserReq struct {
    ID    string  `param:"id" json:"-"`
    Q     string  `query:"q"`
    Name  *string `json:"name" form:"name"`
}

func UpdateUser(c core.IHTTPContext) error {
    var req UpdateUserReq
    if err := c.BindWithValidate(&req); err != nil { // binds path+query+body, then Valid(ctx)
        return err
    }
    // req.ID from :id, req.Q from ?q, req.Name from JSON/form body
    ...
}
```

- Uploaded files: `c.FormFile("avatar")` / `c.MultipartForm()` (echo methods).
- Malformed JSON → `{code: "INVALID_JSON"}` (400).
- JSON type mismatch → `{code: "INVALID_PARAMS", fields: {<field>: INVALID_TYPE}}`.
- Bad path/query/form value → `{code: "INVALID_PARAMS"}` (400).
- `BindOnly` binds without validating; `BindWithValidate` = bind + `req.Valid(ctx)`.

## Form (`application/x-www-form-urlencoded`)

tag `form:` bind ให้เหมือน JSON ทุกอย่าง — struct เดียวรับได้ทั้งสอง content-type
และ `Valid()` ตัวเดียวกันทำงานทั้งคู่:

```go
type LoginRequest struct {
    Email    *string `json:"email" form:"email"`
    Password *string `json:"password" form:"password"`
    Remember *bool   `json:"remember" form:"remember"`
}

func (r *LoginRequest) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Str("email", r.Email).Required().Email()
    v.Str("password", r.Password).Required()

    return v.Error()
}
```

ค่าเดี่ยวๆ อ่านตรงๆ ก็ได้ด้วย `c.FormValue("email")` / `c.FormValues()` (แต่ไม่มี
validation ให้)

## File upload (`multipart/form-data`)

ไฟล์ **ไม่ถูก bind ลง struct** — ดึงเองด้วย `c.FormFile` ส่วน field ที่เป็น text
ที่ส่งมาพร้อมกัน bind ตามปกติด้วย tag `form:`:

```go
type UploadRequest struct {
    Title *string `form:"title"`
    Kind  *string `form:"kind"`
}

func (r *UploadRequest) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Str("title", r.Title).Required().Length(1, 120)
    v.Str("kind", r.Kind).Required().In("ID_CARD", "PASSPORT")

    return v.Error()
}

const maxUploadBytes = 5 << 20 // 5MB

func Upload(c core.IHTTPContext) error {
    input := &UploadRequest{}
    if err := c.BindWithValidate(input); err != nil {
        return err
    }

    fh, err := c.FormFile("document")
    if err != nil {
        // ไฟล์ที่ไม่ได้แนบมาเป็นความผิดของ client ไม่ใช่ 500
        return errmsgs.BadRequest.WithMessage("document is required")
    }

    // ตรวจขนาดจาก header ก่อนเปิดอ่าน — ปฏิเสธได้โดยไม่ต้องแตะ byte แม้แต่ตัวเดียว
    if fh.Size > maxUploadBytes {
        return errmsgs.BadRequest.
            WithCode("FILE_TOO_LARGE").
            WithFields(map[string]any{"max_bytes": maxUploadBytes, "size": fh.Size})
    }

    src, oErr := fh.Open()
    if oErr != nil {
        return c.NewError(oErr, errmsgs.InternalServerError)
    }
    defer src.Close()

    // ส่ง reader ต่อให้ service — ไม่ io.ReadAll ไฟล์ 200MB จึงไม่กลายเป็น
    // 200MB ใน heap
    return service.NewDocService(c).Store(*input.Title, fh.Filename, src)
}
```

หลายไฟล์ในคำขอเดียว:

```go
form, err := c.MultipartForm()
if err != nil {
    return errmsgs.BadRequest
}

for _, fh := range form.File["pages"] {
    ...
}
```

⚠️ **`fh.Filename` และ content-type มาจาก client ห้ามเชื่อ** — อย่าเอาไปต่อเป็น
path เพื่อเขียนไฟล์ (`../../etc/...`) และอย่าใช้ตัดสินว่าไฟล์เป็นชนิดอะไร ถ้า
ต้องรู้ชนิดจริงให้ sniff จากไม่กี่ byte แรก:

```go
head := make([]byte, 512)
n, _ := src.Read(head)
mime := http.DetectContentType(head[:n])
_, _ = src.Seek(0, io.SeekStart)   // multipart.File เป็น io.Seeker
```

เก็บไฟล์ขึ้น S3 ดู [Storage](./storage.md) — ส่งต่อไป service อื่นดู
[Requester](./requester.md#file-upload-multipart-form-data)

> body ที่ใหญ่กว่า memory limit ของ echo จะถูก spill ลง temp file ให้เอง และ
> `core.DefaultReadTimeout` ถูกตั้งไว้ที่ 5 นาที (ไม่ใช่ 30 วินาทีของ echo v5)
> พอดีสำหรับ upload ผ่านเน็ตมือถือ — ดู [Timeout ของ server](#timeout-ของ-server)
>
> ⚠️ เพดาน body ทั้ง server คือ **10 MB** route ที่รับไฟล์ต้องตั้งของตัวเอง:
> `e.POST("/avatars", h.Upload, core.BodyLimit(50<<20))` — ดู
> [ขนาดของ body](#ขนาดของ-body)

## Pagination

```go
opts := c.GetPageOptions()                          // limit/page/q/order_by
opts := c.GetPageOptionsWithAllowed("name", "created_at")  // order-by allowlist
```

`order_by=name asc,created_at desc` parses to `["name asc", "created_at desc"]`.

ค่าที่ไม่ใช่ชื่อ column ถูกทิ้งเสมอ ทั้งสองแบบ — `order_by` กลายเป็น SQL `ORDER BY`
จริงๆ จึงไม่มีทางที่ client จะยัด subquery หรือ statement ที่สองเข้าไปได้ ส่วน allowlist
เป็นชั้นที่สอง: จำกัดว่า sort ด้วย column ไหนได้บ้าง ใช้กับทุก endpoint ที่ client
เข้าถึงได้ (ดู [Pagination](./database-pagination.md#allowlisting-what-can-be-sorted))

## Returning errors

Return any `core.IError` from a handler and the framework renders it:

```go
return errmsgs.NotFound
return core.New(400, "INVALID_STATE", "cannot transition")
return ctx.NewError(dbErr, errmsgs.DBError) // logs at 500, wraps cause
```

## Migrating to echo v5

v2 ใช้ **echo v5** แล้ว (จาก v4) ของเดิมส่วนใหญ่ไม่ต้องแก้ — `IHTTPContext`
ประกาศ method ของ echo ไว้เองทั้งชุด `c.JSON` / `c.Param` / `c.QueryParam` /
`c.Bind` / `c.Request()` ใช้ได้เหมือนเดิมทุกตัว

สิ่งที่ **ต้องแก้** ในโค้ด service มี 3 จุด:

| เดิม (v4) | ใหม่ (v5) | เพราะอะไร |
|---|---|---|
| `func(ec echo.Context) string` ใน `WithTokenLookup` / middleware ที่เขียนเอง | `func(ec *echo.Context) string` | `echo.Context` เป็น struct แล้ว |
| `c.Response().Status` / `.Committed` / `.Size` | `echo.UnwrapResponse(c.Response())` | `Response()` คืน `http.ResponseWriter` ตรงๆ |
| `res.Flush()` (SSE) | `if f, ok := res.(http.Flusher); ok { f.Flush() }` | เหตุผลเดียวกัน |

`c.Response().Header().Set(...)` และ `res.Write(...)` **ยังใช้ได้เหมือนเดิม**
(เป็น method ของ `http.ResponseWriter` อยู่แล้ว)

สิ่งที่ framework จัดการให้แล้ว ไม่ต้องทำอะไร:

- **`e.Shutdown(ctx)` ยังอยู่** — echo v5 ถอด `Echo.Shutdown` ออก แต่ `core.Server`
  มีให้เอง (พร้อม `e.Serve(ctx, echo.StartConfig{...})` ถ้าอยากคุม listener/TLS เอง)
- **404 / 405 ยังตอบเป็น `{code, message}` เหมือนเดิม** — v5 เปลี่ยนไปใช้ sentinel
  ที่ไม่ใช่ `*echo.HTTPError` ถ้าไม่ดักจะกลายเป็น 500 (framework ดักให้แล้ว)
- **CORS default ยังเป็น `*`** — v5 ถอด `DefaultCORSConfig` ทิ้งและ *บังคับ* ให้ระบุ
  origin อย่างน้อยหนึ่งค่า ไม่งั้น panic
- `HideBanner` / `HidePort` ย้ายไปอยู่ใน `StartConfig` — `StartHTTPServer` ตั้งให้แล้ว

### Timeout ของ server

echo v5 ตั้ง `ReadTimeout: 30s` ให้ทุก server (v4 ไม่มี timeout เลย) ซึ่งสั้นเกินไป
สำหรับ upload ผ่านเน็ตมือถือ, long-poll และ SSE — framework จึงตั้งชุดของตัวเองแทน:

| ค่า | default | กันอะไร |
|---|---|---|
| `DefaultReadTimeout` | 5 นาที | request ที่ส่งไม่จบสักที (header + body) — กว้างพอสำหรับ upload |
| `DefaultReadHeaderTimeout` | 20 วินาที | slow loris จริงๆ — header ไม่มีวันใช้เวลาเป็นนาที |
| `DefaultIdleTimeout` | 120 วินาที | keep-alive ที่ค้างสะสมจน fd หมด |
| WriteTimeout | **ไม่ตั้ง** | — ดูเหตุผลข้างล่าง |

`ReadHeaderTimeout` คือตัวที่ปิดช่อง slow-loris จริง ส่วน `ReadTimeout` ต้องกว้าง
เพราะ upload ที่ถูกต้องก็ใช้เวลาเป็นนาทีได้ สองค่านี้ทำคนละหน้าที่กัน

**WriteTimeout ไม่ถูกตั้งโดยตั้งใจ** — มันเป็น deadline ครอบทั้งการตอบกลับ ค่าที่ใหญ่
พอสำหรับ download ช้าๆ ก็ใหญ่เกินกว่าจะกันอะไรได้ ส่วนค่าที่เล็กพอจะกันได้ ก็ตัด SSE
กับ long-poll ทิ้งกลางคัน — ซึ่งคือสิ่งที่ `ReadTimeout` ถูกขยายมาเพื่อรองรับตั้งแต่ต้น
service ที่ไม่มีทั้งสองอย่างตั้งเองได้

ตั้งค่าเองผ่าน `HTTPOptions` — `0` = ใช้ default, ค่าติดลบ = ปิด timeout นั้น:

```go
e := core.NewHTTPServer(app, &core.HTTPOptions{
    ReadHeaderTimeout: 5 * time.Second,
    WriteTimeout:      30 * time.Second,  // API ที่ไม่มี SSE
    IdleTimeout:       -1,                // ปิด
})
```

หรือใช้ `BeforeServeFunc` เมื่อต้องแตะ setting ที่ `HTTPOptions` ไม่ได้ตั้งชื่อไว้
(ทำงานทีหลัง จึงทับค่าของ framework):

```go
go e.Serve(ctx, echo.StartConfig{
    Address: ":8080",
    BeforeServeFunc: func(srv *http.Server) error {
        srv.MaxHeaderBytes = 1 << 20
        return nil
    },
})
```

### ขนาดของ body

`NewHTTPServer` ใส่เพดานให้ทุก request ที่ **`core.DefaultBodyLimit` = 10 MB** —
ไม่มีเพดาน แปลว่า request ที่ใหญ่ที่สุดที่ใครส่งมา คือ memory ที่ process ใช้ และ
client เดียวจบ service ได้ทั้งตัว

เกินเพดานจะได้ `413 REQUEST_TOO_LARGE` ในรูปแบบ error ปกติของ framework โดยเช็ค
ทั้ง `Content-Length` ที่ประกาศมา และจำนวน byte ที่อ่านได้จริง — upload แบบ chunked
ที่ไม่บอกขนาดจึงถูกตัดระหว่างสตรีม ไม่ใช่หลังจากมาถึงครบแล้ว

```go
core.NewHTTPServer(app, &core.HTTPOptions{BodyLimit: 2 << 20})  // 2 MB ทั้ง server
core.NewHTTPServer(app, &core.HTTPOptions{BodyLimit: -1})       // ปิดเพดาน
```

route ที่รับไฟล์ตั้งเพดานของตัวเองได้ — **สูงหรือต่ำกว่าของ server ก็ได้** เพราะค่าถูก
อ่านตอนอ่าน body ซึ่งตอนนั้น middleware ชั้นในสุดตั้งค่าไว้แล้ว:

```go
e.POST("/avatars", h.Upload, core.BodyLimit(50<<20))
files := e.Group("/files", core.BodyLimit(200<<20))
```

## Best practices

- **handler บาง**: bind+validate → เรียก service → คืน response ตรรกะทางธุรกิจอยู่ใน
  service ที่รับ `IContext` เพื่อให้ job และ consumer เรียกโค้ดชุดเดียวกันได้
- **`BindWithValidate` ไม่ใช่ `Bind` แล้วเช็คเอง** — การตรวจที่กระจายอยู่ใน handler คือ
  การตรวจที่ลืมได้
- **return error อย่า log เอง** — [error handler](./error-handling.md) แปลง `IError` เป็น
  response, log และรายงาน Sentry ให้แล้ว ทำเพิ่มคือหนึ่งเหตุการณ์กลายเป็นสองใบ
- **อย่าคืน model ตรงๆ** เมื่อมันมีคอลัมน์ภายใน (hash, flag, ราคาทุน) — ประกาศ response
  type แยก แล้วให้ compiler เป็นคนกันข้อมูลรั่ว
- **`GetPageOptionsWithAllowed` ทุกครั้ง**ที่คอลัมน์เรียงมาจาก client
- **route ที่รับไฟล์ตั้ง `core.BodyLimit` ของตัวเอง** แทนการยกเพดานทั้ง server
- **status ให้ตรงความหมาย**: `201` เมื่อสร้าง, `204` เมื่อไม่มี body, `409` เมื่อชนสถานะ
  ไม่ใช่ `400` สำหรับทุกอย่างที่ไม่ใช่ 200
- **goroutine ที่อยู่นานกว่า request ต้องใช้ context ของตัวเอง** (`ctx.WithContext(bg)`)
  ไม่งั้นงานถูกยกเลิกพร้อม response
