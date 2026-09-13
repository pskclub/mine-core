# Middleware & Routing

`NewHTTPServer` ติดตั้ง middleware ชุดมาตรฐานให้แล้ว หน้านี้คือลำดับของมัน,
วิธีเขียนตัวใหม่บน echo v5 และแบบแผนการวาง route ที่ทำให้ตอบได้ว่า "route นี้
ป้องกันไว้หรือยัง" จากบรรทัดนั้นเอง

## Stack ที่ได้มาให้แล้ว

```go
e := core.NewHTTPServer(app, &core.HTTPOptions{AllowOrigins: []string{"https://app.example.com"}})
```

| ลำดับ | Middleware | ทำอะไร |
|---|---|---|
| 1 | `RequestID` | อ่าน/สร้าง `X-Request-Id` |
| 2 | request-id context | ปัก id ลง context — ทุก log line และทุก Sentry event ของ request นั้นพกไปเอง |
| 3 | Sentry | สร้าง hub + transaction ของ request นี้ |
| 4 | request logger | access line หนึ่งบรรทัดต่อ request (ปิดได้) |
| 5 | recover | panic → 500 ไม่ทำให้ server ตาย |
| 6 | CORS | default `*`, ทับด้วย `HTTPOptions` |
| 7 | body limit | ตัด request ที่ body เกิน 10 MB ด้วย `413 REQUEST_TOO_LARGE` |
| — | error handler | render `core.IError` เป็น `{code, message, fields}` พร้อม status ที่ถูก |

Sentry ต้องมา**ก่อน** recover: hub ต้องมีอยู่แล้วตอน panic ถูกจับ และ transaction
ต้องคลุมทั้ง handler และการ recover ของมัน

ปิด access log ของ server นี้:

```go
core.NewHTTPServer(app, &core.HTTPOptions{DisableRequestLog: core.BoolPtr(true)})
```

หรือปิดทั้งระบบด้วย `APP_LOG_REQUEST=false` — option ชนะ env เสมอ

## เขียน middleware เอง

echo v5 เปลี่ยน `echo.Context` จาก interface เป็น **struct** — signature จึงเป็น
`*echo.Context`:

```go
func RequireAPIKey(key string) echo.MiddlewareFunc {
    return func(next echo.HandlerFunc) echo.HandlerFunc {
        return func(ec *echo.Context) error {
            if ec.Request().Header.Get("X-API-Key") != key {
                // คืน IError ตรงๆ — error handler ของ framework render ให้เอง
                return core.New(http.StatusUnauthorized, "INVALID_API_KEY", "invalid api key")
            }

            return next(ec)
        }
    }
}
```

ส่งค่าต่อให้ handler ผ่าน `ec.Set` แล้วอ่านด้วย `c.Get`:

```go
ec.Set("tenant_id", tenant)          // ใน middleware
tenant, _ := c.Get("tenant_id").(string)   // ใน handler
```

> middleware ทำงานบน `*echo.Context` ไม่ใช่ `IHTTPContext` — capability ของ
> framework (`c.DB()`, `c.Log()`, repository) อยู่ในชั้น handler ถ้า middleware
> ต้องคิวรี database จริงๆ ให้ส่ง `*core.App` เข้าไปตอนสร้างมัน แล้วเปิด context
> เองด้วย `app.NewContext(ec.Request().Context())`

ติดตั้งได้สามระดับ:

```go
e.Use(RequireAPIKey(key))                         // ทุก route
api := e.Group("/api", RequireAPIKey(key))        // ทั้ง group
e.GET("/reports", ListReports, RequireAPIKey(key)) // route เดียว
```

## Auth ที่มีให้ในตัว

```go
a := core.NewAuth(core.HashedTokenVerifier(lookup))   // opaque token ในตาราง
a := core.NewJWTAuth(secret)                          // JWT

e.GET("/me", Me, a.Middleware())            // ต้องมี token
e.GET("/feed", Feed, a.Optional())          // ไม่มีก็ได้ — c.GetUser() เป็น nil
e.DELETE("/users/:id", Delete, a.Middleware(), a.RequireRole("admin"))
```

⚠️ **`RequireRole` ต้องต่อท้าย `Middleware()` เสมอ** — มันอ่านผู้ใช้ที่ `Middleware()`
วางไว้ใน context ไม่ได้ verify token เอง เขียนมันเดี่ยวๆ จะได้ `401` ทุก request แม้จะ
ส่ง token ที่ถูกต้องมา เพราะไม่มีใคร resolve token นั้นเลย

รายละเอียดของ verifier แต่ละแบบอยู่ใน [Authentication](./auth.md)

## เขียน middleware บรรทัดต่อ route

```go [modules/note/note.http.go]
func (m *Module) Routes(e *core.Server) {
    c := &handler.NoteHandler{}

    e.GET("/notes", c.Pagination, middlewares.AuthRequire(e))
    e.GET("/notes/:id", c.Find, middlewares.AuthRequire(e))
    e.POST("/notes", c.Create, middlewares.AuthRequire(e))
    e.PUT("/notes/:id", c.Update, middlewares.AuthRequire(e))
    e.DELETE("/notes/:id", c.Delete, middlewares.AuthRequire(e))
}
```

group ปลอดภัยกว่า — route ที่เพิ่มทีหลังถูกป้องกันโดยอัตโนมัติ — แต่แลกมาด้วยการที่
วิธีเดียวที่จะรู้ว่า route ถูกป้องกันไหมคือเลื่อนขึ้นไปดู เขียนเต็มแล้วแต่ละบรรทัด
ตอบตัวเอง และ path เต็มยัง grep เจอจาก log ได้ตรงๆ

ราคาที่จ่ายคือ route ที่ลืมใส่ = route ที่เปิดโล่ง จึงต้องมี test คู่กันเสมอ (ข้างล่าง)

## Guard ที่ไม่ import module ไหนเลย

การ verify token ต้องอ่านสองตาราง ที่เป็นของสอง module: `access_tokens` เป็นของ
auth, `users` เป็นของ user ถ้า package `middlewares` import ทั้งสอง วงจะปิดทันที
เพราะ route ของทั้งคู่อยู่หลัง middleware นี้

ทางออกคือ **ไม่ import อะไรเลย แล้วรับ lookup ตอน startup**:

```go
// middlewares/auth.go
type TokenResolver func(ctx context.Context, tokenHash string) (*core.ContextUser, error)

// guards เก็บ *core.Auth หนึ่งตัวต่อหนึ่ง App
//
// key ด้วย App ไม่ใช่ตัวแปร global ตัวเดียว เพราะ process หนึ่งมีได้หลาย App:
// ทุก test สร้างของตัวเองบน database ของตัวเอง global ตัวเดียวจะใช้ได้จนถึง
// t.Parallel() ตัวแรก แล้วจะเริ่ม authenticate request ของ test หนึ่งด้วยข้อมูล
// ของอีก test — ความพังที่อ่านออกเป็นอย่างอื่นได้ทุกอย่างยกเว้นสิ่งที่มันเป็น
var guards sync.Map // *core.App -> *core.Auth

func RegisterAuth(app *core.App, resolve TokenResolver) {
    guards.Store(app, core.NewAuth(core.HashedTokenVerifier(resolve)))
}

func AuthRequire(e *core.Server) echo.MiddlewareFunc {
    return authFor(e, (*core.Auth).Middleware)
}
```

```go
// cmd/api.go — ที่เดียวที่รู้จัก module ทั้งหมด
usersFor := func(ctx core.IContext) auth.Users { return user.NewUserService(ctx) }
middlewares.RegisterAuth(app, auth.ResolveToken(app, usersFor))
```

`*core.Server` ที่ส่งเข้า `AuthRequire(e)` มีไว้เพื่อหยิบ guard ของ App นั้น —
`e.App()` คือคำตอบ ไม่ใช่ตัวแปร global

server ที่สร้างโดยไม่ได้เรียก `RegisterAuth` จะได้ middleware ที่ปฏิเสธทุก request
พร้อม log บอกวิธีแก้ **ตั้งแต่ตอนลงทะเบียน route** ไม่ใช่ nil dereference ใน
request แรก:

```go
func authFor(e *core.Server, pick func(*core.Auth) echo.MiddlewareFunc) echo.MiddlewareFunc {
    stored, found := guards.Load(e.App())
    if !found {
        e.App().Log().Error("authentication is not configured",
            "hint", "call middlewares.RegisterAuth(app, ...) before registering routes")

        return notConfigured
    }

    return pick(stored.(*core.Auth))
}
```

ไฟล์เต็ม: [middlewares/auth.go](https://github.com/pskclub/mine-core-template/blob/main/middlewares/auth.go)

## Testing protected routes

route ที่ลืมใส่ guard ต้องเป็น test ที่ fail ไม่ใช่ incident — เขียน test หนึ่งตัว
ต่อ module ที่ไล่ชื่อ **ทุก** route แล้ว assert ว่า anonymous ถูกปฏิเสธ:

```go
func TestNoteAPI_requiresAuthentication(t *testing.T) {
    anon, _ := testkit.Serve(t)   // ประกอบ service จริงด้วย cmd.NewAPI

    const id = "00000000-0000-0000-0000-000000000000"

    anon.Get("/notes").RequireStatus(http.StatusUnauthorized)
    anon.Post("/notes", map[string]any{"title": "x", "body": "y"}).
        RequireStatus(http.StatusUnauthorized)
    anon.Get("/notes/" + id).RequireStatus(http.StatusUnauthorized)
    anon.Put("/notes/"+id, map[string]any{"title": "x", "body": "y"}).
        RequireStatus(http.StatusUnauthorized)
    anon.Delete("/notes/" + id).RequireStatus(http.StatusUnauthorized)
}
```

`testkit.Serve` ประกอบ service **ทั้งตัว** ด้วยฟังก์ชันเดียวกับที่ `cmd` ใช้
(`cmd.NewAPI`) — module ที่ลงทะเบียนโดยลืม guard หรือมี dependency ขาด จึงพังที่นี่
ไม่ใช่ที่ production ถ้าลงทะเบียน route เองในเทสต์ มันจะผ่านทั้งที่ `cmd.NewAPI`
ยังผิดอยู่

ดู [Testing: HTTP](./testing-http.md) สำหรับ client และ assertion ทั้งชุด

## Best practices

- **middleware ทำเรื่องตัดขวางเท่านั้น** — auth, log, trace, rate limit ตรรกะทางธุรกิจ
  ที่ซ่อนอยู่ใน middleware คือตรรกะที่ไม่มีใครหาเจอตอนอ่าน handler
- **เขียนบรรทัดต่อ route เมื่อมันสำคัญ** — สิทธิ์ที่อ่านออกจากไฟล์ route โดยไม่ต้องไล่ว่า
  group ไหนครอบอะไร คือสิ่งที่ทำให้ audit เร็วขึ้นจริง
- **ลำดับมีความหมาย** — auth ก่อน guard ที่ใช้ผู้ใช้, rate limit ก่อนงานที่แพง,
  body limit ก่อน bind
- **middleware ที่อ่าน body ต้องคืน body ให้ handler อ่านต่อได้** ไม่งั้น bind จะได้ค่าว่าง
  โดยไม่มี error
- **`Optional()` สำหรับ route ที่ล็อกอินก็ได้ ไม่ล็อกอินก็ได้** อย่าเขียน auth เองซ้ำ
- **guard ต้องไม่ import module อื่น** — ไม่งั้นมันกลายเป็นทางลัดที่ทำให้ขอบเขต module
  พังทีละนิด
- **middleware ที่เพิ่มเข้ามาทุกตัวคือค่าใช้จ่ายของทุก request** — อันที่ใช้เฉพาะบาง
  เส้นทางควรอยู่ที่ group ของมัน ไม่ใช่ที่ server

## อ่านต่อ

- [HTTP Layer](./http.md) — binding, pagination, การ render error
- [Authentication](./auth.md) — verifier, JWT, role
- [Project Structure](./structure.md) — ทำไม `middlewares/` ถึงอยู่ใต้ทุก module
