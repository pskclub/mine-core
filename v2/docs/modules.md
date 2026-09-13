# Modules

`core.IModule` ให้ **feature หนึ่งตัวประกาศทุกอย่างที่มันต่อเข้า service ไว้ใน package เดียว** —
route, job, cron, MQ consumer, health check, background work — แล้ว composition root เสียบ
เข้า `Runner` บรรทัดเดียว

```go
mods, err := core.NewModules(home.New(), auth.New(usersFor), user.New(), note.New())

core.NewRunner(app,
    core.RunHTTP(e),
    core.RunScheduler(sc),
    core.RunJobs(sc.Runner()),
    core.RunModules(mods),
).Run()
```

## ทำไมต้องมี

[Project Structure](./structure.md) จัดโค้ดตาม feature ไปแล้ว แต่ตัว composition root ยัง
จัดตาม **layer** อยู่ — การเพิ่ม feature หนึ่งตัวต้องแตะ 3–4 ไฟล์:

```go
// cmd/api.go
note.NewNoteHTTP(e)                        // route

// cmd/worker.go
note.RegisterNoteJobs(sc)                  // cron

// cmd/consumer.go
note.RegisterNoteConsumers(c)              // MQ

// cmd/api.go (อีกที่)
core.RegisterHealthRoutes(e, core.HealthOptions{Checks: []core.HealthCheck{note.IndexCheck()}})
```

ปัญหาไม่ใช่ความยาว แต่คือ **มันลืมได้เงียบๆ**: `NewNoteHTTP(e)` ที่ลืมเพิ่ม = 404 ที่เจอตอน QA,
`RegisterNoteJobs` ที่ลืม = cron ที่ไม่เคยยิงและไม่มี log บรรทัดไหนหายไป (เพราะไม่เคยมีบรรทัดนั้น)
ทั้งคู่ compile ผ่าน และ test ของ module เองก็ผ่าน เพราะ test ของ module ไม่ได้ประกอบ `cmd/`

และเมื่อ service มีหลาย role, `api` กับ `worker` ต่างถือ list ของตัวเอง — ไม่มีอะไรบังคับให้
สองอันตรงกัน

## Interface

บังคับแค่ `Name()` ที่เหลือเป็น **optional interface** — implement เฉพาะที่ module นั้นมีจริง

```go
type IModule interface {
    Name() string      // [a-z0-9][a-z0-9_-]* และห้ามซ้ำใน set เดียวกัน
}
```

| interface | เมธอด | ได้เมื่อ role มี |
|---|---|---|
| `IHTTPModule` | `Routes(e *core.Server)` | `RunHTTP` |
| `IJobModule` | `Jobs(reg *core.JobRegistry) core.IError` | `RunJobs` หรือ `RunScheduler` |
| `ICronModule` | `Cron(sc *core.Scheduler) core.IError` | `RunScheduler` |
| `IMQModule` | `Consumers(c core.IMQConsumer)` | `RunMQ` |
| `IHealthModule` | `HealthChecks() []core.HealthCheck` | เสมอ |
| `ILifecycleModule` | `Start(app) core.IError` / `Stop(ctx) core.IError` | เสมอ |

`Routes` ไม่คืน error เพราะของจริงข้างใต้ไม่คืน (`e.GET` คืน `echo.RouteInfo`) ส่วน `Jobs`
กับ `Cron` คืน เพราะ `reg.Register` และ `sc.Add` คืน — ชื่อ job ซ้ำ, cron expression ผิด

## เขียน module

จุดต่อทั้งหมดอยู่ที่ root ของ package แยกไฟล์ตามชนิดของสิ่งที่ต่อ: `.module.go` ประกาศตัว
module, `.http.go` มีแต่ route, `.jobs.go` มีแต่ job/cron ([Project Structure](./structure.md)
อธิบายโครงเต็ม) module ที่มีแต่ route จะเขียน `Routes` ไว้ใน `.module.go` เลยก็ได้ — สิ่งที่
บังคับคือ**ไม่มีจุดต่อไหนอยู่นอก package นี้** ไม่ใช่จำนวนไฟล์

::: code-group

```go [modules/note/note.module.go]
type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string { return "note" }

func (m *Module) HealthChecks() []core.HealthCheck {
    return []core.HealthCheck{{
        Name:  "search-index",                    // → รายงานเป็น "note.search-index"
        Check: func(ctx context.Context) error { return search.Ping(ctx) },
    }}
}
```

```go [modules/note/note.http.go]
func (m *Module) Routes(e *core.Server) {
    c := &handler.NoteHandler{}

    e.GET("/notes", c.Pagination, middlewares.AuthRequire(e))
    e.GET("/notes/:id", c.Find, middlewares.AuthRequire(e))
    e.POST("/notes", c.Create, middlewares.AuthRequire(e))
}
```

```go [modules/note/note.jobs.go]
// handler ไม่ใช่ service: ตัว JobFunc ทำ bind → convert → call → SetResult
// เหมือน HTTP handler ทุกประการ service จึงไม่ต้องรู้จัก ICronjobContext
func (m *Module) Jobs(reg *core.JobRegistry) core.IError {
    return reg.Register(core.JobDef{Name: "note.reindex"}, handler.Reindex)
}

func (m *Module) Cron(sc *core.Scheduler) core.IError {
    return sc.Add(core.JobDef{
        Name:     "note.cleanup",
        Schedule: core.Cron("0 3 * * *"),
    }, handler.Cleanup)
}
```

```go [cmd/modules.go]
// ที่เดียวที่รู้จัก module ทั้งหมด — ยังเป็นความจริงเหมือนเดิม
func Modules(app *core.App) (*core.ModuleSet, core.IError) {
    usersFor := func(ctx core.IContext) auth.Users { return user.NewUserService(ctx) }

    return core.NewModules(
        home.New(),
        auth.New(usersFor),      // dependency inject ตอน construct เหมือนเดิม
        user.New(),
        note.New(),
    )
}
```

```go [cmd/api.go]
func NewAPI(app *core.App, mods *core.ModuleSet, opts *core.HTTPOptions) *core.Server {
    e := core.NewHTTPServer(app, opts)

    core.RegisterHealthRoutes(e, core.HealthOptions{Checks: mods.HealthChecks()})
    mods.MountHTTP(e)      // test เข้าทางนี้ — ไม่ผ่าน Runner

    return e
}
```

:::

`auth.Users` เป็น interface **ของ auth เอง** ไม่ใช่ของ user — module ห้าม import module อื่น
แม้แต่เพื่อเอา type ([Project Structure ข้อ 2](./structure.md)) ถ้า note ต้องใช้ users ด้วย
มันประกาศ `note.Users` ของตัวเองแล้ว `cmd/modules.go` เขียน adapter ให้อีกตัว — ความซ้ำตรงนั้น
คือราคาที่จ่ายเพื่อให้ลูกศรชี้ทางเดียว

::: warning `Jobs` กับ `Cron` ห้ามลงทะเบียนชื่อเดียวกัน
`Scheduler.Add` ทั้ง register **และ** arm ในเมธอดเดียว job ที่ arm ใน `Cron` แล้วไปประกาศใน
`Jobs` อีก = `DUPLICATE_JOB` ตอน boot (error บอกด้วยว่า module ไหน)

แบ่งแบบนี้: `Cron` = ยิงตามเวลา, `Jobs` = trigger เอาเท่านั้น
:::

## Role กับสิ่งที่ถูกข้าม

module list ชุดเดียวใช้ได้ทุก role — `Runner` เรียกเฉพาะสิ่งที่ตัวเองมีที่ให้ต่อ

```
modules mounted  modules=[auth home note user] routes=37 jobs=6 cron=4 queues=0
```

role ที่ไม่มี scheduler จะได้บรรทัดนี้แทน:

```
modules mounted  modules=[auth home note user] routes=37 jobs=6 cron=0 queues=0 skipped=[cron]
```

`skipped=[cron]` คือส่วนที่สำคัญ — ใน `api` role มันคือเรื่องปกติ แต่ใน process ที่ตั้งใจจะ
arm schedule พวกนั้น มันคือ misconfiguration ที่ไม่มี log บรรทัดอื่นบอก

ลำดับที่ `RunModules` เดิน:

| # | ทำอะไร | ทำเมื่อ |
|---|---|---|
| 1 | `Start(app)` ทุก module ตามลำดับที่ประกาศ | เสมอ |
| 2 | `Jobs(registry)` | มี `RunJobs` หรือ `RunScheduler` |
| 3 | `Cron(scheduler)` | มี `RunScheduler` |
| 4 | `Routes(server)` | มี `RunHTTP` |
| 5 | `Consumers(consumer)` | มี `RunMQ` |

นิยาม job มาก่อน arm เสมอ — job ที่ API role ต้อง trigger ได้ ต้องอยู่ใน registry ของ role
นั้นด้วย ทั้งที่ role นั้นไม่มี scheduler

ตอนปิด `Stop(ctx)` ถูกเรียก **ท้ายสุดของ drain** — หลัง request จบ ก่อน pool ถูกปิด
module ที่กำลังหยุดจึงยังใช้ database ได้ (และหยุด**ย้อนลำดับ** module ที่ประกาศทีหลังหยุดก่อน)

## Health check

check ที่ module คืนมาถูก prefix ด้วยชื่อ module อัตโนมัติ ตาม convention เดียวกับ
`database.replica` ใน [Health](./health.md)

| module | `HealthCheck.Name` | key ที่รายงาน |
|---|---|---|
| `note` | `search-index` | `note.search-index` |
| `payment` | *(ว่าง)* | `payment` |

prefix ไม่ใช่ความสวยงาม — ถ้าไม่มี module สองตัวที่ตั้งชื่อ check ว่า `api` เหมือนกัน จะเขียนทับ
กันใน `map[string]CheckResult` ของ `HealthReport` แล้วหนึ่งในนั้นหายไปเงียบๆ

checks ต้องส่งเข้า probe เองที่ composition root:

```go
core.RegisterHealthRoutes(e, core.HealthOptions{Checks: mods.HealthChecks()})
```

**ไม่ได้เก็บไว้บน `App`** ทั้งที่ดูจะสะดวกกว่า เพราะจะทำให้ `ReadyHandler(app)` ขึ้นกับลำดับ:
probe ที่ลงทะเบียนก่อน module จะรายงานไม่ครบ และไม่มีอะไรบอกว่ามันไม่ครบ

## mount ซ้ำ = no-op

`cmd.NewAPI` เรียก `mods.MountHTTP(e)` เอง (test ประกอบ service ผ่านฟังก์ชันนั้น ไม่ผ่าน Runner)
แล้ว `RunModules` ก็เรียกอีกรอบ — **การ mount target เดิมซ้ำจึงเป็น no-op** ไม่ใช่ route ซ้ำ

mount ไป target **คนละตัว** (server ตัวที่สอง, scheduler ของ test) ยังทำงานตามปกติ

## Test

```go
func TestNotes(t *testing.T) {
    srv := coretest.NewServer(t, coretest.WithAutoMigrate(&models.Note{}))

    mods, err := cmd.Modules(srv.App())
    require.Nil(t, err)
    srv.Mount(mods)                          // route ชุดเดียวกับ production

    srv.Get("/notes").RequireStatus(http.StatusOK)
}
```

ประกอบ set ชุดเดียวกับที่ `cmd/` ประกอบคือประเด็นทั้งหมด: module ที่ลงทะเบียนโดยมี dependency
ขาด จะพังใน test ไม่ใช่ใน production

## Devtools

```go
devtools.Mount(e, devtools.Options{Modules: mods, Runner: sc.Runner()})
```

เพิ่มแท็บ **modules** ที่ตอบคำถามที่ทำให้ระบบนี้มีอยู่ — ไม่ใช่ "module นี้ทำอะไร" แต่คือ
**"มันต่ออะไรเข้ากับ process นี้จริงบ้าง"**:

```json
{
  "name": "note",
  "declares": ["jobs", "cron", "routes", "health"],
  "started": false,
  "routes": ["GET /notes", "GET /notes/:id", "POST /notes"],
  "jobs": ["note.reindex"],
  "cron": ["note.cleanup"],
  "queues": [],
  "health": ["note.search-index"]
}
```

`declares` คือ optional interface ที่ type นั้น implement — เป็นตัวส่วนที่ทำให้ list ว่าง
มีความหมาย: module ที่ **ไม่ได้ประกาศ** `routes` กับ module ที่ประกาศแล้วแต่ role นี้
ไม่เคย mount ให้ ต่างก็ได้ list ว่างเหมือนกัน แต่มีแค่อย่างหลังที่เป็นปัญหา

`started` ก็เช่นกัน มันมีความหมายเฉพาะกับ module ที่ `declares` มี `"lifecycle"` เท่านั้น —
module ส่วนใหญ่ไม่มี `Start` ให้เรียก devtools จึงขึ้นว่า *no background work* ไม่ใช่จุดสีเทา
ที่อ่านแล้วเหมือนพัง ส่วน module ที่ขึ้นว่า *declares nothing* คือสิ่งที่ตามหา —
ดู [Devtools](./devtools.md)

ยกเว้นกรณีเดียว: module ที่มีแต่ service ให้ module อื่นเรียก (เช่นตัวที่ห่อ API ของ
ผู้ให้บริการรายอื่น) ไม่ได้ลงทะเบียนอะไรจริงๆ มันจึงควรมี `HealthChecks()` ของ upstream
นั้น — ไม่ใช่เพื่อกลบข้อความ แต่เพราะเป็น dependency ที่ไม่มีใครเห็นถ้าไม่ประกาศ ดู
[External services](./external-services.md)

## Postman

[postmangen](./postman.md) สแกนไฟล์ที่ลงท้ายด้วย `.http.go` **และ `.module.go`** — เมธอด
`Routes` จึงถูกอ่านไม่ว่าจะแยกไว้ใน `.http.go` หรือเขียนรวมใน `.module.go` โดยไม่ต้อง
ตั้งค่าอะไร ถ้าโปรเจกต์ตั้งชื่อไฟล์อื่น ระบุใน `postmangen.json`:

```json
{"route_file_suffixes": [".http.go", ".module.go", ".routes.go"]}
```

## ทางเลือกที่ไม่เลือก

**auto-register ด้วย `init()`** — `import _ "myservice/modules/note"` แล้วให้ module ลงทะเบียน
ตัวเองเข้า global registry จะทำให้ `cmd/` เลิกเป็นที่ที่รู้จัก module ทั้งหมด ผลคือ test
ที่อยากรัน service โดยตัด module หนึ่งออกทำไม่ได้เลย, ลำดับกลายเป็นลำดับ `init()` ซึ่ง Go
ไม่รับประกันข้าม package, และ dependency (เช่น `usersFor`) ส่งเข้าไปไม่ได้เพราะ `init()`
ไม่รับ argument

**`Migrate()` hook** — [Schema & Migrations](./migrations.md) อธิบายไว้แล้วว่าทำไม framework
ไม่ migrate และ module ยิ่งแย่กว่า: ลำดับ migration ข้าม module บน database เดียวกัน
แก้ในโปรเซสไม่ได้ ในเมื่อ module ประกาศตัวเองว่าอิสระต่อกัน

**Go `plugin` (`.so` โหลดตอน runtime)** — ต้อง build ด้วย compiler และ dependency เวอร์ชัน
เดียวกันเป๊ะ, unload ไม่ได้, และเปลี่ยน compile error เป็น panic ตอน runtime module ที่นี่
เป็น compile-time ทั้งหมด

## อ่านต่อ

- [Project Structure](./structure.md) — โครง `modules/` ที่ module หนึ่งตัวหน้าตาเป็นยังไง
- [External services](./external-services.md) — module ที่ห่อ API ของคนอื่น และไม่มี route
- [Lifecycle & Roles](./lifecycle.md) — role อ่านจาก config ยังไง
- [Runner](./runner.md) — ลำดับ start/stop ทั้งเส้น
- [Health & Readiness](./health.md) — critical กับ degraded ต่างกันยังไง
- [Devtools](./devtools.md) — แท็บอื่นๆ ในหน้าเดียวกัน
