# Testing — HTTP

`NewServer` สร้าง HTTP server บน App ของเทส แล้วให้ client สำหรับขับมันในตัวเดียว

```go
func TestCreateUserAPI(t *testing.T) {
    srv := coretest.NewServer(t, coretest.WithAutoMigrate(&models.User{}))
    srv.POST("/users", controller.Create)

    res := srv.Post("/users", `{"email":"a@b.co","full_name":"A"}`)

    res.RequireStatus(http.StatusCreated)
    assert.Equal(t, "a@b.co", res.Map()["email"])
}
```

request วิ่งผ่าน **middleware stack จริง** — request id, panic recovery, CORS,
ตัว render `IError` — สิ่งที่ assert จึงเป็นสิ่งที่ client ได้รับจริง ไม่ใช่ค่าที่
handler คืนออกมาเฉยๆ

ยังเป็นการเรียกในหน่วยความจำ (`httptest`) ไม่มี socket จริง ถ้าต้องการระดับนั้นดู
[End-to-end](./testing-e2e.md)

## ลงทะเบียน route

`*coretest.Server` ฝัง `*core.Server` ไว้ จึงใช้ method เดิมทั้งหมด:

```go
srv.GET("/users", controller.List)
srv.POST("/users", controller.Create)
srv.PUT("/users/:id", controller.Update)

api := srv.Group("/api", authMiddleware)   // group ก็ได้
api.GET("/me", controller.Me)
```

เรียก wiring จริงของ service ได้เลย ซึ่งดีกว่าเพราะครอบคลุมลำดับ route ด้วย:

```go
srv := coretest.NewServer(t, coretest.WithAutoMigrate(&models.User{}))

mods, err := cmd.Modules(srv.App())
require.Nil(t, err)
srv.Mount(mods)                // module ชุดเดียวกับที่ cmd/api.go ประกอบ
```

จะ mount แค่ module เดียวก็ได้ ถ้าเทสต์ไม่ต้องการทั้ง service:

```go
mods, _ := core.NewModules(user.New())
srv.Mount(mods)
```

## ส่ง request

```go
srv.Get("/users?limit=10")
srv.Post("/users", body)
srv.Put("/users/"+id, body)
srv.Patch("/users/"+id, body)
srv.Delete("/users/" + id)
srv.Do(http.MethodHead, "/users", nil)     // method อื่นๆ
```

`body` รับได้สามแบบ:

```go
srv.Post("/users", `{"email":"a@b.co"}`)                    // string ดิบ
srv.Post("/users", []byte(raw))                             // []byte
srv.Post("/users", map[string]any{"email": "a@b.co"})       // encode ให้เป็น JSON
srv.Post("/users", requests.UserCreate{Email: &email})      // struct ก็ได้
```

ส่ง header เฉพาะ request:

```go
srv.Get("/me", map[string]string{"Authorization": "Bearer " + token})
```

## อ่าน response

```go
res := srv.Get("/users/1")

res.RequireStatus(200)          // fail พร้อมพิมพ์ body ถ้าไม่ตรง
res.Map()                       // map[string]any
res.JSON(&dest)                 // decode ลง struct
res.String()                    // body ดิบ
res.Header.Get("X-Request-Id")
res.Code
```

`RequireStatus` พิมพ์ body ออกมาด้วยเมื่อ fail — เพราะเหตุผลที่ status ไม่ตรงมักอยู่
ใน body นั่นเอง ไม่ต้องรันซ้ำเพื่อดู

chain ได้:

```go
body := srv.Post("/users", payload).RequireStatus(201).Map()
```

## ตรวจ error

`Error()` decode body เป็นรูปแบบ error ของ framework ไม่ต้องประกาศ struct ซ้ำใน
ทุก service:

```go
body := srv.Post("/users", `{}`).RequireStatus(400).Error()

assert.Equal(t, "INVALID_PARAMS", body.Code)
assert.Equal(t, "Invalid parameters", body.Message)
assert.Equal(t, "REQUIRED", body.Fields["email"].Code)
```

`Error()` จะ fail เทสถ้า body ไม่ใช่ error (ไม่มี `code`) — กันกรณีที่เทสเขียนผิด
แล้วเงียบ

### field sources

แต่ละ field บอกด้วยว่ามาจากส่วนไหนของ request:

```go
assert.Equal(t, "path",  body.Fields["id"].In)
assert.Equal(t, "query", body.Fields["sort"].In)
assert.Equal(t, "body",  body.Fields["full_name"].In)
```

ค่าตรงกับ `in` ของ OpenAPI — `path` / `query` / `header` / `body`
(ดู [Validation](./validation.md))

### เทียบทั้งชุดทีเดียว

```go
assert.Equal(t, map[string]string{
    "email":     "INVALID_EMAIL",
    "full_name": "REQUIRED",
}, res.FieldCodes())
```

แบบนี้จับได้ทั้ง field ที่**ควรพัง**แต่ไม่พัง และ field ที่**ไม่ควรพัง**แต่ดันพัง
ต่างจากการ assert ทีละตัวซึ่งจับได้แค่อย่างแรก

### field แบบมี index

request ที่มี array จะรายงานเป็น `<field>.<index>.<sub>`:

```go
body := srv.Post("/users/bulk", payload).RequireStatus(400).Error()

assert.Equal(t, "INVALID_EMAIL", body.Fields["users.1.email"].Code)
assert.NotContains(t, body.Fields, "users.0.email")   // ตัวที่ถูกต้องไม่ถูกรายงาน
```

## seed ข้อมูลก่อนยิง

`srv.Context()` คืน context บน pool เดียวกับที่ handler ใช้:

```go
srv := coretest.NewServer(t, coretest.WithAutoMigrate(&models.User{}))
srv.GET("/users", controller.List)

repository.New[models.User](srv.Context()).Create(&user)

res := srv.Get("/users")   // เห็นแถวที่เพิ่ง seed
```

`srv.App()` ให้ App ตรงๆ เมื่อต้องการสร้าง context หลายตัว หรือประกอบ scheduler
บน pool เดียวกัน

## ใช้ App ที่มีอยู่แล้ว

เมื่อเทสเดียวต้องมีทั้ง HTTP และ job บน database เดียวกัน:

```go
app := coretest.NewApp(t, coretest.WithAutoMigrate(&models.User{}))

srv := coretest.NewServerWithApp(t, app, &core.HTTPOptions{AllowOrigins: []string{"*"}})
job := coretest.NewJobWithApp(t, app)

srv.Post("/users", payload).RequireStatus(201)
run := job.Run("sync-users", nil)          // job เห็นสิ่งที่ API เพิ่งสร้าง
```

## panic กลายเป็น 500

recovery middleware ทำงานอยู่ เทสจึงตรวจได้ว่า handler ที่ panic ไม่ทำให้ process ตาย:

```go
srv.GET("/boom", func(c core.IHTTPContext) error { panic("kaboom") })

srv.Get("/boom").RequireStatus(http.StatusInternalServerError)
```

ถ้า `ENV=dev` ข้อความ panic จะปรากฏใน `message` ของ response ด้วย
(ดู [Error handling](./error-handling.md))
