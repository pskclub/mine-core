# Operating Runs

ทุกอย่างที่ทำกับ run **หลังจาก**นิยาม job ไว้แล้ว — สั่งรัน, ดูสถานะ, ยกเลิก, เล่นซ้ำ,
อ่าน log, ล้างของเก่า `JobRunner` เปิด API ครบชุดไว้ให้ ส่วน HTTP อยู่ที่ service ของคุณ
ซึ่งเป็นที่ที่ authentication อยู่แล้ว

## วงจรชีวิตของหนึ่ง run

```
queued ──► running ──► succeeded
   │          │
   │          ├──► failed      (attempts หมด; ถ้ายังเหลือ กลับไป queued)
   │          └──► canceled
   └──► skipped                (ถูก concurrency policy ตัดออก)
```

`RunStatus.IsTerminal()` บอกว่าสถานะนั้นจบแล้วหรือยัง (`succeeded`, `failed`,
`canceled`, `skipped`) — ใช้ตอนเขียน polling หรือ dashboard จะได้ไม่ต้องไล่เทียบเอง

`JobRun.Trigger` บอกว่าใครเป็นคนสั่ง: `schedule`, `manual`, `retry`, `replay`, `event`

## Trigger

```go
run, err := runner.Trigger(ctx, "report", params, core.TriggerOptions{
    By:          userID,                        // ใครสั่ง
    IdemKey:     "report:2026-08-03",           // กันกดซ้ำ
    Delay:       5 * time.Minute,               // เลื่อนเวลาเริ่ม
    Trigger:     core.TriggerEvent,             // บันทึกสาเหตุให้ตรงความจริง
    MaxAttempts: 1,                             // override retry เฉพาะ run นี้
    CaptureLogs: core.LogPolicyPtr(core.LogAlways),
})
```

| option | ใช้เมื่อ |
|---|---|
| `By` | เก็บว่าใครกด — ปรากฏใน `JobRun.TriggeredBy` |
| `IdemKey` | **ขณะที่ยังมี run ที่ key เดียวกันไม่จบ การ trigger ซ้ำจะคืน run เดิม** ไม่สร้างใหม่ |
| `Delay` | เลื่อน `ScheduledAt` ออกไป |
| `Trigger` | default `manual` — ตั้งเป็น `event` เมื่อสิ่งที่สั่งคือระบบ ไม่ใช่คน |
| `MaxAttempts` | run เดียวที่อยากให้ retry (หรือไม่ retry) ต่างจากนิยาม |
| `CaptureLogs` | "รันทีนี้ขอ log เต็ม" — escape hatch ของ operator |

`IdemKey` คือคำตอบของปุ่มที่ถูกกดสองครั้ง และของ webhook ที่ปลายทางส่งซ้ำ ตั้งให้เป็น
สิ่งที่**อธิบายงาน** ไม่ใช่ค่าที่สุ่มใหม่ทุกครั้ง:

```go
// ✅ งานเดียวกัน = key เดียวกัน
IdemKey: fmt.Sprintf("settle:%s", date.Format("2006-01"))

// ❌ ไม่กันอะไรเลย
IdemKey: uuid.NewString()
```

error ที่เป็นไปได้: `404 JOB_NOT_FOUND` (ไม่มี job ชื่อนี้ใน process นี้),
`409 JOB_PAUSED`, และ error จาก store

### รอผลลัพธ์

```go
run, err := runner.TriggerAndWait(ctx, "report", params)   // trigger แล้วรอจนจบ
done, err := runner.Wait(ctx, runID)                       // รอ run ที่มีอยู่แล้ว
```

ทั้งคู่ poll ทุก 50ms จนกว่าสถานะจะ terminal หรือ `ctx` หมดอายุ — เหมาะกับปุ่ม "run now"
ที่คนนั่งดูอยู่ และกับเทส **ไม่เหมาะ**กับ HTTP handler ทั่วไป: request ที่รอ job สาม
นาทีคือ request ที่ timeout

## ดูสถานะ

```go
run, _ := runner.Run(ctx, runID)                    // run เดียว
page, _ := runner.Runs(ctx, core.JobRunFilter{
    JobName:  "report",
    Statuses: []core.RunStatus{core.RunFailed},
    Trigger:  core.TriggerSchedule,
    Queue:    "heavy",
    From:     &since,
    To:       &until,
    Page:     &core.PageOptions{Limit: 20},
})
```

`JobRun` ที่ได้กลับมามีทุกอย่างที่หน้า admin ต้องใช้: `Status`, `Attempt`/`MaxAttempts`,
`StartedAt`/`FinishedAt`/`DurationMS`, `Progress`/`ProgressMessage`, `Result`, `Error`,
`WorkerID`, `TriggeredBy`, `ReplayOf`/`RootID`

handler รายงานความคืบหน้าและผลลัพธ์เองได้:

```go
c.Progress(50, "halfway")
c.SetResult(map[string]any{"rows": n})
```

`SetResult` รับอะไรก็ได้ที่ marshal เป็น JSON ได้ — ถ้า marshal ไม่ได้ จะได้ warning ใน
log แทนที่จะทำให้ run พัง

## หยุด: สามอย่างที่ไม่เหมือนกัน

```go
runner.Cancel(ctx, runID, by, reason)  // one run
runner.Pause("report")                 // the whole job: no schedule, no triggers
runner.Resume("report")                // undo
runner.Stop(shutdownCtx)               // the process: drain, then requeue
```

A **queued** run is pulled out of the queue and never starts. A **running** one
is asked to stop — and because Go cannot kill a goroutine, your handler has to
cooperate:

```go
for _, item := range items {
    if c.IsStopping() {
        return c.Err()
    }
    ...
}
```

Everything that goes through the context (DB, cache, HTTP, MQ) is cancelled for
you; only your own loops need the check. A handler that ignores it is recorded as
canceled after `StopGrace` and abandoned, with a warning — it will not pin a
worker forever.

On shutdown, runs that do not finish in time go **back on the queue**. They are
not failures; they never got the chance to fail.

**Pause** หยุดที่ต้นทาง: job ที่ pause อยู่จะไม่ถูก schedule และ `Trigger` จะได้
`409 JOB_PAUSED` — แต่ run ที่อยู่ในคิวแล้วยังเดินต่อจนจบ ใช้ตอนที่ปลายทางล่มและอยาก
หยุดเลือดก่อนโดยไม่ต้อง deploy

⚠️ **สถานะ pause อยู่ใน registry ของโปรเซส ไม่ได้อยู่ใน database** — pause บน pod หนึ่ง
ไม่มีผลกับ pod อื่น และหายไปเมื่อ restart ถ้าต้องการ pause ทั้ง cluster ให้เก็บ flag ไว้
เองแล้วเช็คใน handler หรือ pause ทุก replica ผ่าน admin API ของแต่ละตัว

## Retry, replay, idempotency

Four ideas people all call "run it again":

| | who does it | result |
|---|---|---|
| **Retry** | the runner, after a failure, while `MaxAttempts` remain | same run, next attempt |
| **Replay** | a person, on a *finished* run | a **new** run, linked via `ReplayOf`/`RootID` |
| **Cancel** | a person, on an unfinished run | `canceled` |
| **IdemKey** | you, at trigger time | no duplicate run gets created |

```go
runner.Replay(ctx, oldRunID, core.ReplayOptions{By: user})           // same params
runner.Replay(ctx, oldRunID, core.ReplayOptions{Params: newParams})  // fixed input
```

Replay always creates a new record — history is never overwritten. Jobs that are
dangerous to repeat opt out:

```go
core.JobDef{Name: "payout", Replayable: core.BoolPtr(false)}   // replay → 403
```

`RootID` ชี้ไป run แรกสุดของสายเสมอ (replay ของ replay ก็ยังชี้กลับไปที่ต้นฉบับ) —
ใช้กรองประวัติทั้งสายในคำสั่งเดียว:

```go
repository.New[core.JobRun](ctx).Where("root_id = ?", rootID).Order("created_at").FindAll()
```

Runs are delivered **at least once** (a worker can die and the run comes back):
handlers must be idempotent.

## Logs — off by default

Persisted logs are the most expensive part of the system. One run is one row; its
logs are N. A job running every 30s that logs 20 lines writes ~1.7M rows a month
by itself. So `LogOff` is the default.

Off does not mean blind. Under `LogOff` you still get:

- the lines on stdout, tagged with `job` / `run_id` / `attempt`
- **live tailing** while the run is in flight
- the **last ~50 lines attached to `JobRun.Error`** when a run fails

```go
core.NewJobRunner(app, reg,
    core.WithLogPolicy(core.LogOff),                       // default
    core.WithLogLimits(core.LogLimits{MaxLines: 1000, MinLevel: "info"}),
)

core.JobDef{Name: "sync",       Logs: core.LogPolicyPtr(core.LogOnFailure)}
core.JobDef{Name: "settlement", Logs: core.LogPolicyPtr(core.LogAlways)}
```

| policy | cost | when |
|---|---|---|
| `LogOff` | none | the default |
| `LogOnFailure` | only failed runs | **start here** — successful runs are free |
| `LogAlways` | every line | important, infrequent jobs |

`LogLimits` คือเพดานต่อหนึ่ง run: `MaxLines` (1000), `MaxBytes` (256 KB),
`MinLevel` (`debug` = ทุกบรรทัด) และ `TailLines` (50) ซึ่งคือจำนวนบรรทัดท้ายที่ติดไปกับ
`JobRun.Error` ตอน fail แม้จะปิด log ไว้

One run at a time can ask for more — the operator's escape hatch:

```go
runner.Trigger(ctx, "sync", nil, core.TriggerOptions{
    CaptureLogs: core.LogPolicyPtr(core.LogAlways),
})
```

Reading them:

```go
entries, _ := runner.Logs(ctx, runID, afterSeq, 200)   // persisted, paged by seq
lines, stop := runner.TailLogs(runID)                  // live, no storage needed
defer stop()
```

`Seq` เรียงบรรทัดของ run และเป็น cursor ในตัว — retry ไม่ทับของเดิมเพราะแต่ละ attempt
จองช่วง seq ของตัวเอง `TailLogs` อ่านจากหน่วยความจำของโปรเซสที่รัน run นั้นอยู่ **จึงใช้
ได้เฉพาะ run ที่กำลังรันบน replica เดียวกับที่รับ request** — หน้า admin ที่มีหลาย replica
ควร fallback ไปอ่าน `Logs` เมื่อ tail ว่าง

## Retention

```go
core.JobDef{RetainRuns: 365 * 24 * time.Hour, RetainLogs: 30 * 24 * time.Hour}
```

Keep logs for less time than runs: the statistics are worth keeping, the chatter
is not. Register the purge on a schedule:

```go
sc.Add(core.JobDef{Name: "core.job.purge", Schedule: core.Cron("0 3 * * *")},
    func(c core.ICronjobContext) error {
        _, err := runner.Purge(c)
        return err
    })
```

ไม่ตั้ง purge ไว้ = ตาราง `job_runs` กับ `job_run_logs` โตไปเรื่อยๆ เงียบๆ จนวันที่
query หน้า admin ช้าจนใช้ไม่ได้ ตั้งไว้ตั้งแต่วันแรกถูกกว่าตามล้างทีหลัง

## Admin API

The runner exposes everything an operator UI needs, so the HTTP layer stays in
your service, where your authentication lives. A complete example is in
[`examples/jobs/08_http_admin.go`](../examples/jobs/08_http_admin.go): list jobs
(with their parameter schema), run, pause/resume, list runs, read or stream logs,
cancel, replay.

โครงที่ mapping ตรงไปตรงมา:

| endpoint | เรียก |
|---|---|
| `GET /jobs` | `runner.Registry().Info()` |
| `GET /jobs/:name/params` | `runner.Registry().Params(name)` |
| `POST /jobs/:name/run` | `runner.Trigger(...)` |
| `POST /jobs/:name/pause` · `/resume` | `runner.Pause(name)` · `Resume(name)` |
| `GET /runs` | `runner.Runs(ctx, filter)` |
| `GET /runs/:id` | `runner.Run(ctx, id)` |
| `GET /runs/:id/logs` | `runner.Logs(...)` / `runner.TailLogs(...)` |
| `POST /runs/:id/cancel` | `runner.Cancel(...)` |
| `POST /runs/:id/replay` | `runner.Replay(...)` |

> **These endpoints run arbitrary business jobs.** Mount them behind real
> authentication — never on a public path.

การ trigger job จากหน้า admin คือการรันโค้ดฝั่ง server ตามชื่อที่ผู้ใช้ส่งมา — ปฏิบัติกับ
มันเหมือน endpoint ที่อันตรายที่สุดใน service ดู [Security](./security.md#_9-jobs-admin-api)
