# Getting Started

A complete HTTP service with mine-core v2: wire the `App`, define a validated
request, and write a controller that uses the repository.

## 1. Bootstrap

::: code-group

```go{18-21,24} [main.go]
package main

import (
    "context"

    core "github.com/pskclub/mine-core/v2"
)

func main() {
    env, err := core.NewEnv()
    if err != nil {
        panic(err)
    }

    db, err := core.NewDatabase(env)
    if err != nil {
        panic(err)
    }
    redis, err := core.NewCache(env)
    if err != nil {
        panic(err)
    }

    app, err := core.NewApp(env,
        core.WithSQL("default", db),
        core.WithCache("default", redis),
    )
    if err != nil {
        panic(err)
    }
    defer app.Shutdown(context.Background())

    e := core.NewHTTPServer(app, &core.HTTPOptions{AllowOrigins: []string{"*"}})

    c := &handler.UserHandler{}
    e.POST("/users", c.Create)
    e.GET("/users", c.List)

    core.StartHTTPServer(e, env)
}
```

:::

`NewApp` เป็นที่เดียวที่รู้จัก connection pool ทั้งหมด และมันมีอายุเท่า process —
ไม่ใช่ต่อ request ([Context & App](./context.md))

> route ผูกใน `main.go` ตรงนี้เพราะมี module เดียว พอมีหลาย module แล้ว แต่ละตัวจะ
> ประกาศ route ของตัวเองเป็น `Routes` แล้ว `main.go` เสียบทีเดียว — ดู [Modules](./modules.md)

## 2. Model, request และ handler

สามไฟล์นี้คือรูปที่โค้ดจริงถูกวางไว้: model เป็นของ `models/` ที่ทุก module ใช้ร่วมกัน
ส่วน request กับ handler อยู่ใน `handler/` ของ module นั้น — วางติดกันเพราะ request
หนึ่งตัวมี handler เดียวที่ bind มัน ([Project Structure](./structure.md))

::: code-group

```go{4-5,8} [models/user.go]
package models

type User struct {
    ID        int64      `json:"id"         gorm:"column:id;primaryKey"`
    Email     string     `json:"email"      gorm:"column:email;uniqueIndex"`
    Name      string     `json:"name"       gorm:"column:name"`
    CreatedAt *time.Time `json:"created_at" gorm:"column:created_at"`
    UpdatedAt *time.Time `json:"updated_at" gorm:"column:updated_at"`
}

func (User) TableName() string { return "users" }
```

```go{5-6,11-12} [modules/user/handler/user_create.request.go]
package handler

// field เป็น pointer เพื่อให้แยก "ไม่ได้ส่งมา" ออกจาก "ส่งค่าว่างมา" ได้
type UserCreate struct {
    Email *string `json:"email"`
    Name  *string `json:"name"`
}

func (r *UserCreate) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Str("email", r.Email).Trim().Lower().Required().Email().Unique("users", "email")
    v.Str("name", r.Name).Trim().Required().Length(2, 50)
    return v.Error()
}
```

```go{11,19-21} [modules/user/handler/user.handler.go]
package handler

// handler เป็น struct ที่ไม่มี field เพื่อให้ route อ้างเป็น c.Create ไม่ใช่ชื่อ
// package-level ยาวๆ — และเมื่อ handler ตัวไหนต้องการ dependency ก็เพิ่มเป็น field
// ได้โดยไม่ต้องแก้ทุกจุดที่เรียก
type UserHandler struct{}

func (h UserHandler) Create(c core.IHTTPContext) error {
    input := &UserCreate{}

    // bind + validate ในขั้นเดียว: ไม่ผ่าน = 400 {code, message, fields} ให้เอง
    if err := c.BindWithValidate(input); err != nil {
        return err
    }

    user := models.User{Email: *input.Email, Name: *input.Name}
    if err := repository.New[models.User](c).Create(&user); err != nil {
        return err
    }
    return c.JSON(http.StatusCreated, user)
}

func (h UserHandler) List(c core.IHTTPContext) error {
    // allow-list คือสิ่งที่กัน SQL injection ผ่าน order_by
    page, err := repository.New[models.User](c).
        Order("id desc").
        Pagination(c.GetPageOptionsWithAllowed("id", "email", "created_at"))
    if err != nil {
        return err
    }
    return c.JSON(http.StatusOK, page)
}
```

:::

สองอย่างที่เป็นคอนเวนชันของ model ในทุกหน้าถัดจากนี้: **เวลาใช้ `*time.Time`** เพราะ
คอลัมน์ที่ยัง null ต้องอ่านได้ว่า "ยังไม่มีค่า" ไม่ใช่ปี 0001 และ **ตัวเลขใช้ชนิด 64 บิต**
(`int64`/`float64`) เพราะ id ที่โตข้าม 32 บิตและยอดเงินที่ overflow คือบั๊กที่แก้ทีหลังแพง

handler ไม่มี `ctx` โผล่ในเมธอดไหนเลย — `c.DB()`, `repository.New[User](c)` พก
deadline/cancel/trace ของ request นั้นไปเองอยู่แล้ว

## 3. What you get for free

- **Ambient context** — `c.DB()`, `c.Cache()`, `repository.New[User](c)` all carry
  the request's context (deadline/cancel/trace) automatically.
- **Uniform errors** — return any `core.IError`; the server renders
  `{code, message, fields}` with the right HTTP status.
- **Panic safety** — a panic in a handler becomes a 500, never crashes the server.
- **Graceful shutdown** — outside dev, `StartHTTPServer` drains in-flight requests
  on SIGINT/SIGTERM and then closes the pools.

## 4. Next: a service that grows

สี่ไฟล์ข้างบนคือ service ที่เล็กที่สุดที่ยังวางถูกที่ — พอมันโตขึ้น สิ่งที่เปลี่ยนคือจำนวน module
ไม่ใช่รูปของแต่ละไฟล์ โครงเต็มของ service จริง (module ที่เป็นเจ้าของตารางตัวเอง,
composition root เดียว, role และกฎที่ทำให้ import graph เป็นต้นไม้) อยู่ที่
[Project Structure](./structure.md) และ
golang-template (internal) คือโครงนั้น
ที่ประกอบไว้ให้แล้ว

| Question | Page |
|---|---|
| ไฟล์ควรอยู่ที่ไหน, module คุยกันยังไง | [Project Structure](./structure.md) |
| ประกอบ App ครั้งเดียว, role, ปิดยังไงให้ครบ | [Lifecycle & Roles](./lifecycle.md) |
| middleware, route ที่ป้องกันแล้ว | [Middleware & Routing](./middleware.md) |
| error code ของ service ตัวเอง | [Service Errors](./service-errors.md) |
| บรรทัดไหนควร log บรรทัดไหนไม่ควร | [Logging Practices](./logging-practices.md) |
| ตารางถูกสร้างจากอะไร | [Schema & Migrations](./migrations.md) |
| ขึ้น production ต้องตั้งอะไร | [Deployment](./deployment.md) |
