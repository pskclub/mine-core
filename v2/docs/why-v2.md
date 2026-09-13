# ทำไมต้อง v2

> **TL;DR** — mine-core v2 คือ Go backend framework ที่รื้อใหม่หมดบน
> Echo v5, GORM, go-redis, resty, koanf, slog (Go 1.27) โดยยัง**คงชื่อ interface
> เดิมของ v1 ไว้เกือบทั้งหมด** (`IContext`, `IError`, `IENV`, `ICache`,
> `IRepository`, ...) แต่แก้ปัญหาเชิงโครงสร้างที่ v1 แก้ไม่ได้: context ไหลไปทุกที่,
> ไม่มี global state, ไม่ panic ใน error path, และ shutdown ตามลำดับที่ถูก
>
> module path: `github.com/pskclub/mine-core/v2`

หน้านี้คือภาพรวมว่า v2 คืออะไรและต่างจาก v1 ตรงไหน — อยากรู้ว่ามันประกอบกันยังไง
ให้ไปต่อที่ [Architecture](./architecture.md) อยากลงมือเลยข้ามไปที่
[Getting Started](./getting-started.md)

---

## 1. ทำไมต้องมี v2

v1 ใช้งานได้ และใช้กันมานาน แต่ปัญหาที่เจอซ้ำ ๆ ไม่ใช่ปัญหา bug — มันเป็นปัญหา
**โครงสร้าง** ที่แก้ทีละจุดไม่ได้:

| ปัญหาใน v1 | อาการที่เจอจริงตอนรัน |
|---|---|
| capability ไม่รับ `context.Context` | timeout / cancel ของ request ไม่ไหลลงไปถึง query — client ตัดสายไปแล้ว database ยังทำงานอยู่ |
| global mutable state (viper, validator flag) | เทสรันขนานไม่ได้, `-race` ไม่สะอาด |
| `newError` type-assert แล้ว panic ได้ | error path พัง = process ตาย ทั้งที่มันคือทางที่ควรปลอดภัยที่สุด |
| pool ผูกกับ context ต่อ request | ปิด connection pool ผิดจังหวะ |
| package `core` ก้อนเดียว 60+ ไฟล์ | `IContext` 20+ เมธอด → mock ยาก, coupling สูง |
| ไม่มีเจ้าภาพเรื่อง lifecycle | ทุก service เขียน graceful shutdown เองคนละแบบ และผิดลำดับเกือบทุกที่ |

v2 เลือกเป็น **clean break** — ไม่มี shim, ไม่ backward-compatible กับ v1, แต่
**คงชื่อที่ทีมคุ้นไว้** เพื่อไม่ต้องจำ API ใหม่ทั้งหมด ผลคือโค้ดที่ย้ายมาแล้ว
"หน้าตาเหมือนเดิม แต่ทำสิ่งที่ถูกต้องอยู่ข้างใน"

---

## 2. หลักการที่ยึดทุกโมดูล

1. **Ambient context** — `IContext` embed `context.Context` และ handle ทุกตัวที่ดึงผ่าน
   `ctx` ถูก bind context ไว้แล้ว จึง**ไม่ต้องโยน `ctx` ทุกครั้งที่จะทำอะไร**
   (ดู [Context & App](./context.md))
2. **No global mutable state** — ทุก capability มาจาก constructor คืน instance
   (`go test -race ./...` สะอาด)
3. **Generics over `interface{}`** — ไม่ต้อง cast, ไม่ต้องเดา type
4. **Functional options** — ประกอบ App ด้วย `With...` แทน struct ยักษ์ที่ field ครึ่งหนึ่งเป็น nil
5. **Errors are values** — typed error, `errors.Is/As`, **ห้าม panic ใน library error path**
   (ดู [Error Handling](./error-handling.md))
6. **Testability by construction** — ทุกอย่างรับ `IContext` ตัวเดียวกัน โค้ดใน handler,
   ใน job และในเทส จึงเป็นโค้ดชุดเดียวกันจริง ๆ

---

## 3. หน้าตาโค้ดจริง

### Bootstrap ทั้ง service

```go
env, _ := core.NewEnv()
db, _ := core.NewDatabase(env)
redis, _ := core.NewCache(env)

app, _ := core.NewApp(env,
    core.WithSQL("default", db),
    core.WithCache("default", redis),
)
defer app.Shutdown(context.Background())

e := core.NewHTTPServer(app, &core.HTTPOptions{AllowOrigins: []string{"*"}})
e.POST("/users", CreateUser)
core.StartHTTPServer(e, env)
```

### Controller — ไม่มี `ctx` โผล่ในเมธอดสักตัว

```go
func CreateUser(c core.IHTTPContext) error {
    var req CreateUserRequest
    if err := c.BindWithValidate(&req); err != nil {
        return err // ตอบเป็น {code, message, fields} ให้เอง
    }

    user := User{Email: *req.Email, Name: *req.Name}
    if err := repository.New[User](c).Create(&user); err != nil {
        return err
    }
    return c.JSON(201, user)
}

func ListUsers(c core.IHTTPContext) error {
    page, err := repository.New[User](c).
        Order("id desc").
        Pagination(c.GetPageOptionsWithAllowed("id", "email"))
    if err != nil {
        return err
    }
    return c.JSON(200, page)
}
```

`c.DB()`, `c.Cache()`, `repository.New[User](c)` — ทุกตัวพก deadline / cancel /
trace-id ของ request นั้นไปเองอัตโนมัติ อยาก override เฉพาะจุด? มี escape hatch
`.WithContext(bgCtx)` ทุก handle

### Validation ที่ใช้ซ้ำได้ทั้ง HTTP และ job

```go
func (r *CreateUserRequest) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Str("email", r.Email).Required().Email().Unique("users", "email")
    v.Str("name", r.Name).Required().Length(2, 50)
    return v.Error()
}
```

rule ที่แตะ database (`Unique`, `Exists`) ทำงานได้เพราะ validator ถือ `IContext`
อยู่แล้ว — และ payload ของ **job** ก็ validate ด้วย builder ตัวเดียวกันนี้
(ดู [Validation](./validation.md))

---

## 4. ของที่คิดว่าจะชอบที่สุด

### Jobs — cron กับ manual trigger เป็นทางเดียวกัน

scheduler **ไม่เคยรันอะไรเอง** มันแค่ enqueue ดังนั้น run ที่มาจาก cron, มาจากปุ่มกด,
มาจาก retry หรือ replay จึงเดินโค้ดเส้นเดียวกัน และทุก run กลายเป็น `JobRun` ที่
query / ดู log / cancel / รันซ้ำได้

```
  cron tick ──┐
  manual   ───┼─► JobRun{queued} ─► IJobQueue ─► worker ─► handler ของคุณ
  retry ──────┘                                    │
                                                   └─► IJobStore (status, logs)
```

→ [Jobs](./jobs.md) · [Scheduler](./scheduler.md)

### Runner — ปิด service ตามลำดับที่ถูก

การปิด service ไม่ใช่ "ปิดทุกอย่าง" มันคือ**ลำดับ** และการทำผิดลำดับคือสิ่งที่เปลี่ยน
deploy ธรรมดาให้กลายเป็น 500 เป็นชุดกับ job ที่ค้างครึ่งทาง

```go
r := core.NewRunner(app,
    core.RunHTTP(e),
    core.RunScheduler(sc),
    core.RunJobs(sc.Runner()),
)
r.Run()
```

ลำดับที่ Runner เดิน: hook ก่อนหยุด → หยุด scheduler → drain job → drain HTTP →
ปิด pool → hook ปิดท้าย เขียนเองก็ได้ แต่เขียนผิดง่ายกว่ามาก → [Runner](./runner.md)

### Boot log — บรรทัดแรกตอบว่า process นี้คืออะไร

ทุก capability ของ framework **degrade แทนที่จะไม่ยอม boot** (ไม่มี `CACHE_*` ก็ได้
cache ที่ miss ทุกครั้ง) ซึ่งดี — แต่แลกมาด้วยว่า config ที่หายไปหนึ่งบรรทัดจะมองไม่
เห็นจนกว่าจะมี request แรกที่ต้องใช้ boot log ปิดช่องนั้น:

```json
{"level":"INFO","msg":"app ready","env":"prod","service":"orders",
 "sql":["default","replica"],"cache":["default"],"mq":true,"sentry":true}
{"level":"INFO","msg":"job registered","job":"nightly-report","queue":"default",
 "timeout":"10m0s","attempts":3,"schedule":"cron(0 2 * * *)",
 "next_run":"2026-07-31T02:00:00+07:00","in":"12h22m29s"}
{"level":"INFO","msg":"queue consumed","queue":"orders.shipping","exchange":"orders",
 "binding_keys":["order.created","order.paid"],"dead_letter":"orders.dlx"}
{"level":"INFO","msg":"http server started","addr":"[::]:8080","routes":37}
```

→ [Logging Practices](./logging-practices.md#boot-log)

### Health — `/healthz` ที่ไม่แตะ dependency โดยตั้งใจ

```go
core.RegisterHealthRoutes(e)   // GET /healthz + GET /readyz
```

liveness probe ที่ ping database คือ probe ที่ restart ทุก instance พร้อมกันตอน
database สะดุด — เปลี่ยน outage เดียวเป็นสองอัน `/healthz` จึงตอบแค่ "process นี้ค้าง
ไหม" ส่วน `/readyz` ping ทุก dependency พร้อมกัน แยก `up` / `degraded` (non-critical
ล่ม, ยัง 200) / `down` (critical ล่ม, 503) → [Health & Readiness](./health.md)

### Sentry — DSN ตัวเดียว จบ

```sh
APP_SENTRY_DSN=https://xxx@sentry.io/123
```

ไม่ต้องแก้ handler, ไม่ต้อง init เอง, ไม่ต้อง `defer sentry.Recover()` — error ที่
status ≥ 500, panic, job ที่ fail, scheduler ที่ enqueue ไม่สำเร็จ ถูกส่งครบ พร้อม
user, request id, breadcrumb และ stack trace ของ**จุดที่ error เกิด** ไม่ใช่จุดที่
capture · ไม่มี DSN = no-op ทุกเมธอด เขียนโค้ดชุดเดียวได้ทั้ง dev / test / prod
→ [Sentry](./sentry.md)

### Postman collection ที่ไม่มีวันเป็นของเก่า

```sh
go get -tool github.com/pskclub/mine-core/v2/cmd/postmangen
go tool postmangen
```

อ่าน AST จาก route ที่เขียนไว้ตรง ๆ — ได้ body ตัวอย่าง, query param, path variable,
header auth และ example response ทั้งฝั่งสำเร็จและฝั่ง error โดยไม่ต้อง annotate อะไร
เพิ่ม และไม่ต้องรัน server · output deterministic จะ commit แล้วให้ CI ตรวจว่าตรงกับ
โค้ดก็ได้ → [Postman Collection](./postman.md)

### เทสที่ไม่ต้อง mock

```go
func TestCreateUser(t *testing.T) {
    ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&models.User{}))

    user, err := services.NewUserService(ctx).Create(payload)

    coretest.RequireNoError(t, err)
    assert.Equal(t, "a@b.co", user.Email)
}
```

`coretest` ให้ `IContext` ที่ผูกกับ database แยกของเทสนั้น — service รับ `IContext`
อยู่แล้วจึงเทสได้ตรง ๆ ไม่ต้องมี interface พิเศษเพื่อเทสอย่างเดียว → [Testing](./testing.md)

---

## 5. มีอะไรพร้อมใช้บ้าง

| กลุ่ม | ของที่มี |
|---|---|
| Core | `IContext` / `App`, config (koanf), logger (slog), error + `errmsgs`, Sentry, Runner, health probes |
| HTTP | Echo v5 server, middleware stack, binding (path/query/form/json), JWT auth + roles, validation, Postman generator |
| Data | GORM (postgres, mysql, sqlserver, oracle), generic `repository.Repo[M]`, Mongo + `mongorepo.Repo[D]`, Redis cache (JSON helper, `Remember`, counter, lock) |
| Messaging & Jobs | RabbitMQ publisher/consumer + topology, Redis pub/sub, scheduler (gocron v2), job runner พร้อม queue/store/retry/timeout |
| Integrations | S3 storage, mailer (go-mail), FCM push, requester (resty), JWT, CSV, utils |
| Testing | `coretest` — context บน database จริง, HTTP client, job runner, fixtures |

ทุกโมดูลมี unit test และมี in-memory backend (cache, pubsub, storage, mailer, push,
sentry) ให้เทสได้โดยไม่ต้องต่อของจริง ส่วน integration test gate ด้วย build tag
เหมือนเดิม

---

## 6. เริ่มยังไง

```sh
go get github.com/pskclub/mine-core/v2
```

- [Architecture](./architecture.md) — App กับ context, ทางเข้าทั้งสี่, เส้นทางของหนึ่ง request
- [Getting Started](./getting-started.md) — wiring + controller เต็มตัวอย่าง
- [Project Structure](./structure.md) — service จริงวางไฟล์ยังไงเมื่อมันโตขึ้น
- [API + Cron ในตัวเดียว](./api-with-cron.md) — service ที่มีทั้ง API และ scheduled job
- อยากได้โครงที่ประกอบไว้แล้ว → golang-template (internal)

---

## 7. ย้ายจาก v1 ยังไง

- v1 กับ v2 อยู่**คนละ module path** จึงอยู่พร้อมกันได้ — service ที่ยังไม่ย้ายใช้ v1 ต่อได้เลย
- **ไม่มี shim** ระหว่าง v1↔v2 โดยตั้งใจ → ย้าย**ทั้ง service ต่อครั้ง** ไม่ mix ครึ่ง ๆ กลาง ๆ
- v1 **freeze** รับแค่ security patch จนกว่าทุก service จะย้ายครบ ไม่มี deadline บังคับ
- ชื่อ interface หลักคงเดิมเกือบทั้งหมด งานย้ายส่วนใหญ่จึงเป็นการแทน API ตรง ๆ:

| v1 | v2 |
|---|---|
| `ctx.NewError(err, errmsgs.X)` | เหมือนเดิม — แต่ไม่ panic แล้ว |
| `repository.New[M](ctx)` | เหมือนเดิม — แต่ terminal method **ไม่รับ** ctx (ambient) |
| `BaseValidator.IsX(...)` | `valid.New(ctx).Str(...).X()` |
| `ctx.Requester()` (heimdall) | `ctx.Requester()` (resty) |
| เขียน graceful shutdown เอง | `core.Runner` |

---

## 8. คำถามที่น่าจะถาม

**ต้องย้ายเลยไหม?** ไม่ — v1 ยังอยู่ ย้ายตอนเริ่ม service ใหม่ หรือตอนที่มีจังหวะ
refactor ก้อนใหญ่อยู่แล้วคุ้มที่สุด

**v2 พร้อม production หรือยัง?** ทุก capability มีเทส และมี service ใช้อยู่จริง
ดูของที่เปลี่ยนแต่ละเวอร์ชันได้ที่
[CHANGELOG](https://github.com/pskclub/mine-core/blob/master/v2/CHANGELOG.md)

**อยากได้ของที่ยังไม่มี / เจอ bug?** อ่าน
[CONTRIBUTING.md](https://github.com/pskclub/mine-core/blob/master/CONTRIBUTING.md)
— setup, build tag, convention ที่ reviewer จะทัก และ release flow อยู่ในนั้นครบ

---

*Framework ที่ดีไม่ได้วัดกันที่มีของเยอะแค่ไหน แต่วัดที่ว่า — เรื่องที่ทุก service
ต้องทำเหมือนกันทุกครั้ง มันทำให้แล้วหรือยัง v2 ตั้งใจตอบคำถามนั้น*
