# Scheduler (Cron Jobs)

`core.Scheduler` wraps [gocron v2](https://github.com/go-co-op/gocron). Each run
gets a fresh `ICronjobContext` (a full `IContext` in `ModeCron`), a panic is
turned into a logged error (never crashes the scheduler), and failures are logged.

> A tick **queues** a run rather than calling the handler directly, so every
> scheduled job also gets a status record, retries, concurrency limits and
> cancellation. The API on this page is unchanged — see [jobs.md](./jobs.md) for
> manual triggers, parameters, queueing, status and logs.

## Setup

```go
sc, err := core.NewScheduler(app)
if err != nil { panic(err) }

sc.AddByCron("nightly-report", "0 2 * * *", SendReport)   // 5/6-field cron
sc.AddByDuration("heartbeat", 30*time.Second, Heartbeat)

sc.Start()          // non-blocking
defer sc.Stop()     // graceful shutdown
```

## A job

A job is a `func(c core.ICronjobContext) error` — it has every capability:

```go
func SendReport(c core.ICronjobContext) error {
    c.Log().Info("building report", "job", c.JobName())

    users, err := repository.New[User](c).Where("active = ?", true).FindAll()
    if err != nil {
        return err // logged automatically
    }
    // ... use c.DB(), c.Cache(), c.MQ(), core.Requester(c) as usual
    return nil
}
```

## Timezone

โดย default expression ถูกอ่านตาม timezone ของ **process** — ซึ่งใน container ที่
ไม่ได้ตั้ง `TZ` คือ UTC ไม่ใช่เวลาที่คนตั้ง schedule ใช้ชีวิตอยู่ `0 22 * * *` จึง
กลายเป็นตี 5 ของเช้าวันถัดไปตามเวลาไทย โดยไม่มี error ให้เห็นสักตัว

### ตั้งครั้งเดียวทั้ง scheduler

```go
bangkok, _ := time.LoadLocation("Asia/Bangkok")

sc, err := core.NewScheduler(app, runner, core.WithSchedulerLocation(bangkok))

sc.AddByCron("seed-projects", "0 22 * * *", SeedProjects)   // สี่ทุ่มตามเวลาไทย
sc.AddByCron("sync-watchlist", "0 1 * * *", SyncWatchlist)  // ตี 1 ตามเวลาไทย
```

ทุก schedule ที่ไม่ได้ระบุ timezone ของตัวเองจะใช้ค่านี้ — รวมถึง `AddByCron` และ
`core.Cron(...)` ที่เขียนไว้ก่อนหน้าแล้ว ไม่ต้องไล่แก้ทีละจุด

> `runner` เป็น optional เหมือนเดิม `core.NewScheduler(app, runner)` ที่มีอยู่แล้ว
> ยังใช้ได้ทุกตัวอักษร — พารามิเตอร์ตัวหลังรับทั้ง runner และ option

### ระบุเป็นราย job

`core.CronIn(loc, expr)` ทับค่าของ scheduler เฉพาะ job นั้น — ใช้กับ service ที่
เก็บ schedule ไว้ในตาราง database และให้ operator แก้คอลัมน์ `timezone` ได้เอง:

```go
tz, err := time.LoadLocation(row.Timezone)
if err != nil {
    tz = bangkok // ค่าที่ตั้งใจ ดีกว่า timezone ของ container ที่บังเอิญเป็น UTC
}
sc.Add(core.JobDef{Name: row.Key, Schedule: core.CronIn(tz, row.Schedule)}, fn)
```

ทั้งสองทางส่ง timezone ให้ Sentry cron monitor ด้วย — ไม่งั้น Sentry อ่านชั่วโมงเป็น
UTC แล้วแจ้ง missed run ทุกวันที่ job รันตรงเวลาเป๊ะ

### บรรทัดที่บอกว่าตอนนี้ใช้ timezone อะไร

บรรทัด `scheduler started` บอก timezone ที่ใช้จริงไว้ (`"timezone":"Asia/Bangkok"`
หรือ `"timezone":"UTC"` ถ้าไม่ได้ตั้ง) — ตารางที่เพี้ยนไป 7 ชั่วโมงทั้งชุดหน้าตา
เหมือนตารางที่ปกติดีทุกประการ ยกเว้นบรรทัดนี้บรรทัดเดียว

> ย้ายมาจาก v1: `CronjobContextOptions.TimeLocation` คือ `WithSchedulerLocation`
> ตัวนี้ ดู [migration-v1.md](./migration-v1.md)

## Behaviour

- **Fresh context per run** — `ModeCron`, independent of any request.
- **Panic safety** — a panicking job returns an error (with stack) instead of
  taking down the scheduler.
- **Errors logged** — a non-nil return is logged with the job name.
- Invalid cron expressions are rejected by `AddByCron` at registration time.
- **Timezone** — default คือของ process, `WithSchedulerLocation` ตั้งทั้งก้อน,
  `CronIn` ทับเป็นราย job (ดูหัวข้อข้างบน)

## Start บอกว่าอะไรจะยิงเมื่อไหร่

```json
{"level":"INFO","msg":"scheduler started","scheduled":2,"timezone":"+07"}
{"level":"INFO","msg":"job registered","job":"nightly-report","queue":"default",
 "timeout":"10m0s","attempts":3,"schedule":"cron(0 2 * * *)",
 "next_run":"2026-07-31T02:00:00+07:00","in":"12h22m29s"}
```

cron expression ไม่ใช่สิ่งที่คนอ่านแล้วแปลงเป็นเวลาได้ในหัว และคำถามแรกเวลา job
เงียบคือ "มันไม่ทำงาน หรือยังไม่ถึงเวลา" — `next_run` ตอบให้ตั้งแต่ตอน boot

`next_run` อยู่บน **บรรทัดเดียวกับ `job registered`** ไม่ใช่บรรทัดแยก: job runner
จะรอ scheduler ก่อนพิมพ์รายการ เพราะเวลาครั้งต่อไปมีอยู่ก็ต่อเมื่อ gocron เริ่มเดิน
แล้ว — พิมพ์สองรอบ (รอบแรกไม่มีเวลา รอบสองมี) คือสองบรรทัดที่พูดเรื่องเดียวกัน

job ที่ไม่มี `Schedule` ก็อยู่ในรายการนั้นด้วย โดยขึ้น `trigger=manual` แทน
ดู [Boot log](./logging-practices.md#boot-log)

## API

```go
// runner เป็น optional: ไม่ส่งมา scheduler จะสร้าง JobRunner ของตัวเองให้
// *JobRunner เป็น SchedulerOption ตัวหนึ่ง — NewScheduler(app, runner) จึงยังเขียนได้เหมือนเดิม
func NewScheduler(app *core.App, opts ...core.SchedulerOption) (*core.Scheduler, IError)
func WithSchedulerLocation(loc *time.Location) SchedulerOption       // timezone ของทั้ง scheduler

func (s *Scheduler) Add(def JobDef, fn JobFunc) IError               // ตั้งค่าได้ครบทุก field
// Schedule ที่ใส่ใน JobDef ได้
func Cron(expr string) Schedule                                      // timezone ของ process
func CronIn(loc *time.Location, expr string) Schedule                // timezone ที่ระบุ
func CronWithSeconds(expr string) Schedule                           // 6 field, field แรกเป็นวินาที
func CronWithSecondsIn(loc *time.Location, expr string) Schedule
func Every(d time.Duration) Schedule

func (s *Scheduler) AddByCron(name, cronExpr string, fn JobFunc) IError
func (s *Scheduler) AddByDuration(name string, d time.Duration, fn JobFunc) IError
func (s *Scheduler) Runner() *core.JobRunner                          // ตัวที่รันจริง
func (s *Scheduler) Start() IError
func (s *Scheduler) Stop() IError

type ICronjobContext interface {
    core.IContext
    JobName() string
}
```
