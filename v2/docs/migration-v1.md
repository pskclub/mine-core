# Migration from v1

หน้านี้คือคู่มือย้าย service จาก `github.com/pskclub/mine-core` มาเป็น
`github.com/pskclub/mine-core/v2` — เหตุผลเบื้องหลังอยู่ที่ [ทำไมต้อง v2](./why-v2.md)
ส่วนที่นี่คือ **ทำอะไรบ้าง ตามลำดับไหน**

## กติกาสามข้อก่อนเริ่ม

1. **ย้ายทั้ง service ต่อครั้ง ไม่ผสม** — v1 กับ v2 อยู่คนละ module path จึงอยู่ในโปรเจกต์
   เดียวกันได้ก็จริง แต่ `core.IContext` ของสองฝั่งเป็นคนละ type ที่ส่งข้ามกันไม่ได้
   การผสมครึ่งๆ กลางๆ จบลงด้วยการแปลง type ไปมาซึ่งแพงกว่าย้ายให้จบ
2. **ไม่มี shim โดยตั้งใจ** — ตัวแปลง v1↔v2 จะทำให้ทุก service ค้างอยู่ครึ่งทางไปตลอด
3. **v1 ยัง freeze อยู่ ไม่ได้ตาย** — รับ security patch ต่อไป ไม่มี deadline บังคับ
   จังหวะที่คุ้มที่สุดคือตอนเริ่ม service ใหม่ หรือตอนที่มี refactor ก้อนใหญ่อยู่แล้ว

## ลำดับที่แนะนำ

```
1. go.mod + import path          ← เปลี่ยนทีเดียว compile พังทั้งไฟล์ (ตั้งใจ)
2. bootstrap: App + Runner       ← จุดที่โครงสร้างเปลี่ยนจริง
3. HTTP layer                    ← route + handler signature
4. validation                    ← BaseValidator → valid builder
5. repository / query            ← เกือบไม่ต้องแก้
6. cron → jobs                   ← เปลี่ยนความหมาย ไม่ใช่แค่ชื่อ
7. MQ                            ← consumer เขียนใหม่ publisher เกือบเหมือนเดิม
8. tests                         ← mock → memory implementation
```

ข้อ 1 ทำให้ compile พังทั้งโปรเจกต์ ซึ่งเป็นสิ่งที่ต้องการ: compiler จะกลายเป็น checklist
ที่ไล่ให้เองทีละไฟล์

## 1. Module path

```sh
go get github.com/pskclub/mine-core/v2
```

```go
// v1
import core "github.com/pskclub/mine-core"

// v2
import core "github.com/pskclub/mine-core/v2"
```

## 2. Bootstrap: จาก context ต่อ request มาเป็น App

นี่คือความต่างที่ใหญ่ที่สุด ทุกอย่างที่เหลือเป็นผลของมัน

```go
// ❌ v1 — context ถือ pool และปิดมันทิ้งทุก request
ctx := core.NewContext(&core.ContextOptions{
    DB: db, Cache: cache, ENV: env, MQ: mq,
})
defer ctx.Close()

// ✅ v2 — App ถือ pool ตลอดอายุ process, context เป็นแค่ handle ต่อหนึ่งงาน
app, err := core.NewApp(env,
    core.WithSQL("default", db),
    core.WithCache("default", cache),
    core.WithMQ(mq),
)
defer app.Shutdown(context.Background())

ctx := app.NewContext(context.Background(), core.ModeCron)   // เมื่อต้องสร้างเอง
```

| v1 | v2 |
|---|---|
| `core.NewContext(&ContextOptions{...})` | `core.NewApp(env, With...)` + `app.NewContext(ctx, mode)` |
| `ctx.Close()` | ไม่มี — มีแต่ `app.Shutdown(ctx)` ตอน process จบ |
| `consts.ContextType` | `core.Mode` (`ModeHTTP`, `ModeCron`, `ModeMQ`, `ModeTest`) |
| เขียน graceful shutdown เอง | `core.NewRunner(app, RunHTTP(...), RunJobs(...))` |

⚠️ `ctx.Close()` ที่หลงเหลืออยู่คือบั๊กที่ v2 แก้ — ใน v1 มันปิด pool ที่ใช้ร่วมกัน
ทุก request ถ้าเจอในโค้ดที่กำลังย้าย ให้ลบทิ้ง ไม่ต้องหาอะไรมาแทน

## 3. HTTP layer

```go
// v1
e := core.NewHTTPServer(&core.HTTPContextOptions{ContextOptions: opts})
e.GET("/users", func(c echo.Context) error {
    ctx := core.NewHTTPContext(c, opts)
    ...
})
core.StartHTTPServer(e, env)

// v2
e := core.NewHTTPServer(app, &core.HTTPOptions{AllowOrigins: []string{"*"}})
e.GET("/users", func(c core.IHTTPContext) error {   // context มาให้เลย
    ...
})
core.StartHTTPServer(e, env)      // หรือ core.NewRunner(app, core.RunHTTP(e)).Run()
```

| v1 | v2 |
|---|---|
| handler เป็น `echo.HandlerFunc` แล้วสร้าง context เอง | handler เป็น `core.HandlerFunc = func(IHTTPContext) error` |
| `NewHTTPServer(*HTTPContextOptions) *echo.Echo` | `NewHTTPServer(app, *HTTPOptions) *core.Server` |
| bind + validate แยกกัน | `c.BindWithValidate(&req)` |
| paging เขียนเอง | `c.GetPageOptions()` / `GetPageOptionsWithAllowed(...)` |

⚠️ route ต้องลงทะเบียนผ่าน `e.GET/POST/...` ของ `core.Server` — ที่ผ่าน `e.Echo.GET(...)`
ตรงๆ จะไม่ได้ `IHTTPContext` ดู [HTTP Layer](./http.md)

## 4. Validation

```go
// v1
type CreateUserRequest struct {
    core.BaseValidator
    Email *string `json:"email"`
}

func (r *CreateUserRequest) Valid() core.IError {
    if ok, msg := r.IsRequired(r.Email, "email"); !ok {
        r.AddValidator(msg)
    }
    return r.Error()
}

// v2
func (r *CreateUserRequest) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Str("email", r.Email).Required().Email().Unique("users", "email")
    return v.Error()
}
```

| v1 | v2 |
|---|---|
| embed `core.BaseValidator` ใน struct | ไม่ต้อง embed อะไร |
| `IsRequired`, `IsEmail`, … คืน `(bool, *IValidMessage)` | chain: `.Required().Email()` |
| กฎที่ต้องใช้ database ต้องส่ง ctx เอง | `Unique`/`Exists` ใช้ ctx ที่ builder ถืออยู่แล้ว |
| `Valid() IError` | `Valid(ctx) IError` (`IValidate` แบบไม่มี ctx ยังใช้ได้) |

ทั้ง [valid](./validation.md) เขียนใหม่หมด แต่ mapping เป็นแบบหนึ่งต่อหนึ่งเกือบทั้งหมด —
ส่วนที่ต้องคิดคือกฎที่เคยเขียนเป็น if ยาวๆ ซึ่งตอนนี้มี `When`, `Must`, `Nested`,
`EachNested` รองรับแล้ว

## 5. Repository และ query

เกือบไม่ต้องแก้ — ชื่อเดิม ความหมายเดิม:

```go
repository.New[User](ctx).Where("email = ?", email).FindOne()
```

| v1 | v2 |
|---|---|
| `repository.New[M](ctx)` | เหมือนเดิม |
| terminal method บางตัวรับ ctx | **ไม่รับแล้ว** — ctx ผูกมาแล้วตั้งแต่ `New` |
| `base.repo_mock.go` | ไม่มี mock — ใช้ database จริงใน [coretest](./repository-testing.md) |
| แต่ละ builder แก้ตัวเอง | **immutable** — ทุกเมธอด clone ก่อน ต่อ chain แล้วเก็บไว้ใช้ซ้ำได้ |

Mongo เปลี่ยนมากกว่า: v2 มี [`mongorepo`](./mongo-repository.md) ที่เป็น typed operator
และ typed pipeline แทนการประกอบ `bson.M` เอง — ย้ายทีละ query ได้ เพราะ
[driver layer](./mongo-queries.md) ยังเข้าถึงได้ตรงๆ

## 6. Cron → Jobs

v1 มีแต่ "ฟังก์ชันที่ถูกเรียกตามเวลา" — v2 ทำให้มันเป็น **run ที่มีตัวตน**

```go
// v1 — gocron ดิบๆ ผ่าน ICronjobContext
ctx := core.NewCronjobContext(&core.CronjobContextOptions{ContextOptions: opts})
ctx.AddJob(ctx.Job().Every(1).Day().At("02:00"), func(c core.ICronjobContext) error { ... })
ctx.Start()

// v2 — JobFunc signature เดิม ความหมายใหม่
reg := core.NewJobRegistry()
_ = reg.Register(core.JobDef{Name: "nightly-report", Schedule: core.Cron("0 2 * * *")},
    func(c core.ICronjobContext) error { ... })

runner := core.NewJobRunner(app, reg)
runner.Start()
sc, _ := core.NewScheduler(app, runner)
_ = sc.Start()
```

`JobFunc` (`func(c ICronjobContext) error`) เหมือนเดิมทุกตัวอักษร และ Scheduler ของ v2
มีทางลัด `sc.AddByCron(name, expr, fn)` / `sc.AddByDuration(name, d, fn)` ให้สำหรับ job
ที่ไม่ต้องตั้งค่าอะไรเพิ่ม สิ่งที่เปลี่ยนคือ **tick ไม่ได้เรียกฟังก์ชันตรงๆ อีกแล้ว
มันสร้าง run เข้าคิว** ผลที่ได้มาฟรี: status, log, retry,
cancel, replay และหน้า admin — ดู [Jobs](./jobs.md)

สิ่งที่ต้องตัดสินใจตอนย้าย:

- job ที่รันนานกว่ารอบของมัน ควรตั้ง `Concurrency: core.ConcurrencySkip`
- job ที่แตะเงิน ควรตั้ง `Replayable: core.BoolPtr(false)`
- ถ้าต้องการประวัติที่อยู่รอด restart ให้ใช้ [jobstore](./jobs-store.md) ตั้งแต่แรก

⚠️ **`CronjobContextOptions.TimeLocation` ย้ายเป็น `WithSchedulerLocation`**

```go
// v1
core.NewCronjobContext(&core.CronjobContextOptions{TimeLocation: bangkok, ...})

// v2 — ตั้งครั้งเดียวทั้ง scheduler เหมือนเดิม
sc, _ := core.NewScheduler(app, runner, core.WithSchedulerLocation(bangkok))

// v2 — ทับเป็นราย job ได้ด้วย (v1 ทำไม่ได้)
sc.Add(core.JobDef{Name: "nightly", Schedule: core.CronIn(bangkok, "0 22 * * *")}, fn)
```

เป็นข้อที่ compiler ไม่ช่วย เพราะ struct ทั้งก้อนหายไปพร้อมกัน: ลืมย้ายแล้ว
`core.Cron(expr)` เฉย ๆ ยัง compile ผ่านและ "ทำงาน" แต่อ่านเวลาตาม timezone ของ
process ซึ่งใน container ที่ไม่ได้ตั้ง `TZ` คือ UTC — ทั้งตารางเลื่อนพร้อมกัน
โดยไม่มี error ให้เห็น ดู [Scheduler → Timezone](./scheduler.md#timezone)

## 7. Message Queue

publisher เกือบเหมือนเดิม ส่วน consumer เขียนใหม่:

```go
// v1 — ไม่มี reconnect, ack/nack policy ไม่ชัด
mq.AddConsumer(...)

// v2
c := app.NewMQConsumer(core.WithMQPrefetch(20))
c.OnQueue(core.ConsumeQueue{
    Queue:       core.QueueConfig{Name: "orders.shipping", DeadLetterExchange: "orders.dlx"},
    Exchange:    &core.ExchangeConfig{Name: "orders", Kind: core.ExchangeTopic},
    BindingKeys: []string{"order.created"},
}, handler)
_ = c.Start()
```

| v1 | v2 |
|---|---|
| publish ไม่รอ confirm | **รอ publisher confirm** — `nil` = broker รับผิดชอบแล้ว |
| ไม่มี reconnect ชัดเจน | ทั้งสองฝั่ง reconnect เอง |
| ack/nack policy ไม่ชัด | error = dead-letter, `core.Requeue(err)` = ส่งใหม่ |
| topology สร้างนอกโค้ด | `OnQueue` ประกาศให้ตอน start และตอน reconnect |

ดู [Message Queue](./mq.md) — โดยเฉพาะ [best practices](./mq-patterns.md) เพราะ retry
กับ DLQ เป็นสิ่งที่ v1 ไม่เคยบังคับให้คิด

## 8. Tests

```go
// v1 — mock ที่ต้องอัปเดตทุกครั้งที่ interface เปลี่ยน
ctx := core.NewMockContext(...)

// v2 — context จริงบน database ของเทสนั้น
ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&models.User{}))
```

| v1 | v2 |
|---|---|
| `*_mock.go` เขียนมือ (cache, mq, logger, env, …) | memory implementation: `NewMemoryCache()`, `NewMemoryStorage()`, `NewMemoryMailer()`, `NewMemoryPusher()`, `NewRecordingSentry()` |
| `e2e_context.go` | [`coretest`](./testing.md) ครบทั้ง context, HTTP client, job runner, fixtures |

## เทียบ API แบบเร็ว

| v1 | v2 | หมายเหตุ |
|---|---|---|
| `ctx.NewError(err, errmsgs.X)` | เหมือนเดิม | **ไม่ panic แล้ว** |
| `ctx.Requester()` | `core.Requester(ctx)` | heimdall → resty |
| `ctx.Cache()` / `ctx.DB()` / `ctx.MQ()` | เหมือนเดิม | handle ผูก ctx มาแล้ว |
| `ctx.GetData/SetData` | ยังมี + `core.Set[T]` / `core.Get[T]` | แบบใหม่ไม่ต้อง cast |
| `ctx.Close()` | — | ลบทิ้ง |
| `ctx.Type()` | `ctx.Mode()` | |
| Viper + `envKeys` | koanf | เพิ่ม key = เพิ่ม field ที่ `ENVConfig` อย่างเดียว |
| `ctx.WinRM()` | **ไม่มีใน v2** | service ที่ใช้จริงต้องคุยกันก่อนย้าย |
| `abci_context.go` | **ไม่มีใน v2** | เช่นเดียวกัน |

## กับดักที่เจอบ่อยตอนย้าย

- **ลืมลบ `defer ctx.Close()`** — v2 ไม่มีเมธอดนี้ compile ไม่ผ่าน แต่ที่เจอจริงคือคนไป
  เรียก `app.Shutdown()` แทนในทุก request ซึ่งปิด pool ทิ้งทั้ง process
- **route ที่ยังลงทะเบียนกับ `e.Echo`** — compile ผ่าน แต่ handler ไม่ได้ `IHTTPContext`
- **validation ที่ย้ายไม่ครบ** — `BaseValidator` ที่ยัง embed อยู่จะ compile ไม่ผ่าน
  ซึ่งดี ส่วนที่เงียบกว่าคือกฎที่เคยเขียนเองแล้วลืมย้าย ทำให้ endpoint รับค่าที่ไม่ควรรับ
- **cron ที่คาดว่าจะรันทันที** — tick ใน v2 คือการ enqueue การรันเกิดที่ worker
  process ที่มี scheduler แต่ไม่มี runner จะเข้าคิวไว้แล้วไม่มีใครทำ
- **`TimeLocation` ที่ไม่ได้ย้ายเป็น `WithSchedulerLocation`** — เงียบที่สุดในลิสต์นี้
  เพราะ job ยังรันทุกวันตรงเวลาเป๊ะ แค่คนละชั่วโมงกับที่ตั้งไว้
- **เทสที่พึ่ง mock พฤติกรรม** — memory implementation ทำงานจริง ไม่ใช่ตอบค่าที่ตั้งไว้
  เทสที่เคย assert ว่า "เมธอดถูกเรียก" ควรเปลี่ยนไป assert ผลลัพธ์แทน
- **`go test ./...` ที่ root ไม่เห็น v2** — `v2/` เป็น module แยก ต้อง `cd v2`

## ตรวจก่อนถือว่าเสร็จ

- [ ] `grep -r "mine-core\"" ` ไม่เหลือ import ของ v1
- [ ] ไม่มี `ctx.Close()` เหลืออยู่
- [ ] `go test -race ./...` ใน `v2/` สะอาด
- [ ] boot log แสดง capability ครบตามที่ service ต้องใช้จริง
- [ ] endpoint ที่มี validation ถูกยิงทดสอบด้วยค่าที่ผิด แล้วได้ `fields` กลับมาถูก
- [ ] job ที่เคยเป็น cron รันได้ทั้งจาก schedule และจาก `Trigger`
- [ ] consumer กลับมาทำงานเองหลัง restart broker
- [ ] อ่าน [Deployment](./deployment.md) แล้วตั้ง drain timeout ให้ต่ำกว่า grace period
