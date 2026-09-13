# Context & App

`App` owns long-lived resources (connection pools, base logger, shared HTTP
client) and is built once at startup. `IContext` is the per-request/per-job
container derived from the App — it carries every capability and **is** the
ambient `context.Context`.

## App (bootstrap)

```go
env, _  := core.NewEnv()
db, _   := core.NewDatabase(env)
redis, _ := core.NewCache(env)

app, err := core.NewApp(env,
    core.WithSQL("default", db),
    core.WithSQL("readonly", replicaDB), // named connections
    core.WithCache("default", redis),
    core.WithMongo("default", mongo),
    core.WithMQ(mq),
    // core.WithLogger(customLogger), core.WithRequester(customClient)
)
defer app.Shutdown(context.Background()) // closes pools once, at exit
```

`App` methods: `Config() *ENVConfig`, `ENV() IENV`, `Log() ILogger`,
`NewContext(ctx, mode...) IContext`, `Shutdown(ctx) IError`.

> Pools live for the lifetime of the App — never per request. This fixes v1's bug
> where `IContext.Close()` tore down shared pools on every request.

## IContext (per request/job)

Delivery layers build it for you (`WithHTTPContext`, the scheduler, MQ consumers).
To create one manually:

```go
ctx := app.NewContext(context.Background(), core.ModeCron)
```

Capabilities — all return handles **already bound to the request context**:

```go
ctx.DB()                 // *gorm.DB (default connection, ctx-bound)
ctx.DBS("readonly")      // named connection
ctx.Cache()              // ICache      / ctx.Caches("name")
ctx.PubSub()             // IPubSub (the default cache's)
ctx.DBMongo()            // IMongoDB    / ctx.DBSMongo("name")
ctx.MQ()                 // IMQ
ctx.Storage()            // IStorage (S3)
ctx.ENV()                // IENV
ctx.Log()                // ILogger (with request_id/trace attrs)
ctx.Mode()               // ModeHTTP | ModeCron | ModeMQ | ModeTest
```

มีแค่ `ctx.DB()` / `ctx.DBS()` ที่คืน `nil` เมื่อไม่ได้ตั้งค่า — `*gorm.DB` ไม่มีรูปแบบ
"ปิดอยู่" ให้คืน ที่เหลือคืน handle เสมอ ไม่มีวัน panic เพราะ nil interface:

- `ctx.Cache()` / `ctx.PubSub()` คืน handle ที่ปิดอยู่ อ่าน miss เขียนทิ้ง —
  cache-aside code จึงรันได้ทั้งที่มีและไม่มี redis
- `ctx.MQ()` / `ctx.Storage()` / `ctx.DBSMongo()` คืน handle ที่ทุกคำสั่ง fail พร้อม code
  ที่บอกว่า config ตัวไหนหาย (`MQ_DISABLED`, `STORAGE_DISABLED`, `MONGO_DISABLED`)
  กลุ่มนี้ **ไม่** degrade เงียบๆ: cache miss คำนวณใหม่ได้ แต่ไฟล์ที่ถูกทิ้งและ message ที่
  ไม่มีใครได้รับ เอาคืนไม่ได้

`Enabled()` tells a real one from a disabled one.

## method บน context หรือ function ที่รับ context

```go
// method — capability ที่ request "มี"
ctx.DB()  ctx.Cache()  ctx.Log()  ctx.Sentry()  ctx.ENV()

// function — สิ่งที่โค้ด "ไปทำ" กับข้างนอก
core.Requester(ctx)          // เรียก HTTP ออกไป
core.Mailer(ctx)             // ส่งเมล
core.Pusher(ctx)             // ส่ง push
repository.New[User](ctx)    // อ่าน/เขียน model
```

เหตุผลไม่ใช่เรื่องรสนิยม: **function form ใช้ได้ทุกที่ที่ method ใช้ได้ บวกอีกที่หนึ่ง** —
ฟังก์ชันที่ถือแค่ `context.Context` มันได้ทั้ง context binding, ไม่ต้องส่ง App ลงไปทีละชั้น,
และ handle ที่ไม่มีวันเป็น nil เหมือนกันหมด แต่ไม่บังคับให้ caller ถือ `IContext`

ผลคือ domain service ไม่ต้อง import framework เพื่อจะส่งเมล:

```go
// ไม่ต้องเปลี่ยนเป็น core.IContext เพียงเพื่อจะส่งอะไรออกไป
func (s *NotificationService) SendWelcome(ctx context.Context, u User) error {
    return core.Mailer(ctx).SendTemplate(msg, "welcome", u)
}
```

ใช้ได้เพราะ **App ติดอยู่บน context เอง** — `app.NewContext` ปัก App ลงไป และ context
ทุกตัวที่ derive ต่อจากนั้นยังพกมันไป แม้จะผ่านโค้ดที่ไม่รู้จัก core เลยก็ตาม context ที่ไม่มี
App (เช่น `context.Background()` ใน script) ได้ handle ที่ปิดอยู่ ไม่ใช่ nil

> ⚠️ `ctx.MQ()` กับ `ctx.Storage()` ยังเป็น method ด้วยเหตุผลทางประวัติศาสตร์ ทั้งคู่คือ
> "สิ่งที่โค้ดไปทำ" ตามกฎข้างบน และจะได้ `core.MQ(ctx)` / `core.Storage(ctx)` คู่กันใน
> major ถัดไป โค้ดใหม่ที่เขียนใน service layer ใช้ผ่าน parameter ไปก่อนได้

> ⚠️ **`ctx.Requester()` ถูกถอดออกแล้ว** เปลี่ยนเป็น `core.Requester(ctx)` ซึ่งให้
> client ตัวเดียวกัน ผูก context เดียวกัน และ instrument เหมือนเดิมทุกอย่าง —
> แก้ที่ call site อย่างเดียว ไม่ต้องแตะ wiring

## Users & scoped data

```go
ctx.SetUser(&core.ContextUser{ID: "u1", Email: "a@b.com"})
u := ctx.GetUser()
```

Type-safe per-request values (replaces v1's `interface{}` GetData/SetData):

```go
core.Set(ctx, "trace_id", "abc")
id, ok := core.Get[string](ctx, "trace_id") // id is a string, no cast
```

The untyped `ctx.GetData/SetData/GetAllData` are still available.

## Errors

```go
return ctx.NewError(err, errmsgs.DBError)   // wraps err, logs at >=500
```

See [error-handling.md](./error-handling.md).

## App มีอายุเท่า process

`App` ถูกสร้างครั้งเดียวตอน start และปิดครั้งเดียวตอนจบ — การประกอบมัน, การเลือก
role (`api` / `worker` / `all`) และลำดับการ shutdown ที่ถูกต้อง อยู่ที่
[Lifecycle & Roles](./lifecycle.md)

## Custom context (escape hatch)

```go
bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
child := ctx.WithContext(bg) // same capabilities, different underlying context
```
