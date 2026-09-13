# Runner

`core.Runner` สตาร์ททุกส่วนของ service และ — ที่สำคัญกว่า — **ปิดมันตามลำดับที่ถูก**

```go
r := core.NewRunner(app,
    core.RunHTTP(e),
    core.RunScheduler(sc),
    core.RunJobs(sc.Runner()),
)
if err := r.Run(); err != nil {
    log.Fatal(err)
}
```

## ทำไมต้องมี

การปิด service ไม่ใช่ "ปิดทุกอย่าง" มันคือลำดับ และการทำผิดลำดับคือสิ่งที่เปลี่ยน deploy
ธรรมดาให้กลายเป็น 500 เป็นชุดกับ job ที่ค้างครึ่งทาง

1. **หยุดรับงานใหม่** — ปฏิเสธ connection ใหม่, หยุด cron tick, หยุดดึงจาก queue
2. **ปล่อยให้งานที่ค้างอยู่จบ** ภายใน deadline
3. **แล้วค่อยปิด pool** ที่งานพวกนั้นใช้อยู่

[`StartHTTPServer`](./http.md) ทำให้แล้วสำหรับ process ที่ serve HTTP อย่างเดียว
`Runner` ทำให้สำหรับ process ที่มี job, scheduler และ pub/sub subscriber ด้วย — ซึ่งเป็น
เคสที่เขียนเองได้ง่าย และเขียนผิดง่ายกว่า

ลำดับที่ Runner เดิน:

| ลำดับ | ทำอะไร | ทำไมตรงนี้ |
|---|---|---|
| 1 | `BeforeStop` hooks | deregister จาก service registry ก่อน traffic จะยังมาอยู่ |
| 2 | หยุด scheduler + MQ consumer | ทั้งคู่คือแหล่งงานใหม่ — tick หรือ message ระหว่าง drain คืองานที่จะถูก cancel ในอีกไม่กี่วินาที |
| 3 | drain job runner | run ที่กำลังทำอยู่ได้ทำจนจบ |
| 4 | drain service อื่นๆ + HTTP + `ModuleSet.Stop` | request ที่ค้างได้ตอบ แล้ว module ค่อยหยุด |
| 5 | `app.Shutdown()` | หยุด subscriber แล้วปิด pool — ตอนนี้ไม่มีใครถืออยู่แล้ว |
| 6 | `AfterStop` hooks | สิ่งสุดท้ายก่อน process จบ |

## Boot log

`Runner` เรียก `app.LogCapabilities()` เป็นอย่างแรก แล้วแต่ละส่วนรายงานตัวเองตอน
start — process นี้ต่ออะไรไว้, มี job อะไร, ยิงครั้งต่อไปเมื่อไหร่, ฟัง queue ไหน,
bind ที่ port ไหน ดู [Boot log](./logging-practices.md#boot-log)

## Options

```go
core.RunHTTP(e)              // *core.Server
core.RunScheduler(sc)        // *core.Scheduler
core.RunJobs(sc.Runner())    // *core.JobRunner — ตัวเดียวกับที่ scheduler ป้อน
core.RunMQ(c...)             // core.IMQConsumer — start ท้ายสุด, หยุดแรกสุด
core.RunModules(mods)        // *core.ModuleSet — ดู Modules
core.RunService(s...)        // อะไรก็ได้ที่มี Start() error / Stop(ctx) error

core.WithDrainTimeout(20*time.Second)
core.WithCloseTimeout(10*time.Second)
core.BeforeStop(func(ctx context.Context) error { return registry.Deregister(ctx) })
core.AfterStop(func() { log.Println("bye") })
```

⚠️ **`WithDrainTimeout` ต้องน้อยกว่า `terminationGracePeriodSeconds` ของ Kubernetes**
ไม่งั้น process ถูกฆ่ากลาง drain แล้วลำดับทั้งหมดข้างบนก็ไม่เคยเกิดขึ้นจริง

## RunnerService

อะไรก็ได้ที่มี start กับ stop — gRPC server, consumer ของ library อื่น, metrics
exporter `Start` ต้องไม่ block

```go
type RunnerService interface {
    Start() error
    Stop(ctx context.Context) error
}
```

`IMQConsumer` กับ `ISubscriber` ใส่ตรงนี้ **ไม่ได้** — เมธอดคืน `IError` ไม่ใช่ `error`
(`IError` ที่เป็น nil ไม่เท่ากับ `error` ที่เป็น nil) `App` จำ subscriber ไว้ตั้งแต่ตอนสร้าง
และ `app.Shutdown()` หยุดให้ในขั้นที่ 5 อยู่แล้ว ส่วน consumer ที่ต้องการให้ Runner
**start** ให้ด้วย และหยุดตั้งแต่ขั้นที่ 2 ใช้ `core.RunMQ(c)`

## Modules

`core.RunModules(mods)` mount [module](./modules.md) ทุกตัวเข้ากับสิ่งที่ Runner ถืออยู่ —
job กับ cron เมื่อมี scheduler, route เมื่อมี server, consumer เมื่อมี `RunMQ` — ก่อนอะไร
จะ start ทั้งสิ้น extension point ที่ role นี้ไม่มีที่ให้ต่อ จะถูกข้ามและ**บอกชื่อไว้ใน boot
log** (`skipped=[cron]`)

## role เดียว หรือทั้งหมดใน process เดียว

```go
switch app.ENV().String("role") {
case "worker":
    core.NewRunner(app, core.RunScheduler(sc), core.RunJobs(sc.Runner())).Run()
case "all":
    core.NewRunner(app, core.RunHTTP(e), core.RunScheduler(sc), core.RunJobs(sc.Runner())).Run()
default:
    core.NewRunner(app, core.RunHTTP(e)).Run()
}
```

ดู [Lifecycle & roles](./lifecycle.md) สำหรับเรื่อง replica ของแต่ละ role

## start ไม่สำเร็จ

ถ้าอะไรสตาร์ทไม่ขึ้น Runner **หยุดสิ่งที่สตาร์ทไปแล้ว** ก่อนคืน error — start ครึ่งๆ ไม่
ทิ้ง scheduler ที่ tick เข้า service ที่จะไม่มีวัน serve

## หยุดจากข้างนอก

`Run()` block จนได้ SIGINT/SIGTERM `RunContext(ctx)` block จนกว่า ctx จะถูก cancel —
สำหรับ test หรือ process ที่ตัดสินใจหยุดเอง `Stop()` สั่งปิดจากที่ไหนก็ได้ และเรียกซ้ำได้
