# Jobs

A job is a named function you can put on a schedule **and** run by hand, with
parameters. Every run — scheduled, manual, retried or replayed — becomes a
`JobRun` record you can query, watch, cancel and re-run.

> Migrating from the old scheduler? `AddByCron` / `AddByDuration` and the
> `JobFunc` signature are unchanged. What changed is that a tick now *queues* a
> run instead of calling your function directly, so scheduled jobs get status and
> logs for free.

## The shape of it

```
  cron tick ──┐
  manual   ───┼─► JobRun{queued} ─► IJobQueue ─► worker ─► your handler
  retry ──────┘                                    │
                                                   └─► IJobStore (status, logs)
```

The scheduler never executes anything; it only enqueues. That is why manual and
scheduled runs behave identically — there is one code path, not two.

## What this section covers

| Page | |
|---|---|
| [Defining Jobs & Parameters](./jobs-defining.md) | `JobDef` ทุก field, typed params + validation, schema สำหรับ admin UI |
| [Runner & Concurrency](./jobs-running.md) | worker, queue, limit ทั้งสามชั้น, retry/backoff, timeout, boot log |
| [Operating Runs](./jobs-operations.md) | trigger, cancel, pause/resume, replay, progress, logs, retention, admin API |
| [Store & Queue Backends](./jobs-store.md) | in-memory vs SQL, เขียน backend เอง, query ประวัติด้วย repository |
| [Best Practices & Recipes](./jobs-patterns.md) | idempotency, งานใหญ่ที่เดินต่อได้, fan-out, เฝ้าดูอะไร, checklist |
| [Scheduler (Cron)](./scheduler.md) | ตัว tick ที่ป้อนงานเข้า runner |
| [Testing](./testing-jobs.md) | รัน job ในเทสโดยไม่ต้องมี worker จริง |

## Smallest useful setup

```go
reg := core.NewJobRegistry()

_ = reg.Register(core.JobDef{
    Name:     "nightly-report",
    Schedule: core.Cron("0 2 * * *"),
}, func(c core.ICronjobContext) error {
    c.Log().Info("building report")
    return nil
})

runner := core.NewJobRunner(app, reg)   // in-memory queue + store, no config
runner.Start()

sc, _ := core.NewScheduler(app, runner)
_ = sc.Start()
```

Run it by hand, right now:

```go
run, err := runner.Trigger(ctx, "nightly-report", nil, core.TriggerOptions{By: userID})
```

ค่า default ทั้งชุดใช้งานได้จริงโดยไม่ต้องตั้งอะไรเลย — in-memory store + queue,
worker 4 ตัว, timeout 5 นาที, ไม่ retry, ไม่เก็บ log ลง database สิ่งที่ต้อง
เปลี่ยนเมื่อขึ้น production คือ[เปลี่ยน backend เป็น SQL](./jobs-store.md) ซึ่งเป็นการ
เพิ่ม option สองบรรทัด

## Jobs หรือเครื่องมืออื่น

| อยากได้ | ใช้ |
|---|---|
| งานของ service เราเอง ที่ต้องมีประวัติ/retry/สั่งรันเองได้ | **jobs** |
| งานที่ต้องยิงตามเวลา | **jobs + [scheduler](./scheduler.md)** — scheduler ไม่ได้รันเอง มันแค่ enqueue |
| งานที่ **service อื่น** ต้องรับไปทำ | [Message Queue](./mq.md) |
| แจ้งให้รู้เฉยๆ ไม่ต้องมีใครรับผิดชอบ | [Pub/Sub](./pubsub.md) |
| งานสั้นๆ ที่ผลลัพธ์ต้องกลับไปใน response | ทำใน request ไปเลย — job ที่ caller ต้องรอ คือ request ที่ช้าลงบวกกับ record ที่ไม่มีใครดู |

จุดที่คนมักเลือกผิด: ใช้ MQ ส่งงานให้ **ตัวเอง** เพราะอยากได้ retry — jobs ให้ retry,
backoff, run log, cancel และหน้า admin มาให้แล้ว โดยไม่ต้องมี broker และไม่ต้องมี
topology ให้ดูแล

## ครบวงจรหน้าตาแบบนี้

```go
// 1. นิยาม
type ReportParams struct {
    Date *string `json:"date"`
}

_ = core.RegisterJob(reg, core.JobDef{
    Name:        "sales-report",
    Description: "สรุปยอดขายรายวัน",
    Schedule:    core.Cron("0 2 * * *"),
    Queue:       "heavy",
    Timeout:     10 * time.Minute,
    MaxAttempts: 3,
    Backoff:     core.ExponentialBackoff(time.Second, 2*time.Minute),
    MaxConcurrent: 1,
    Concurrency:   core.ConcurrencySkip,
    Logs:          core.LogPolicyPtr(core.LogOnFailure),
}, func(c core.ICronjobContext, p *ReportParams) error {
    c.Progress(50, "อ่านข้อมูลเสร็จ")
    c.SetResult(map[string]any{"rows": n})
    return nil
})

// 2. worker ที่รันมัน
runner := core.NewJobRunner(app, reg,
    core.WithWorkers(4),
    core.WithQueues("default", "heavy"),
    core.WithJobStore(jobstore.New(app)),
    core.WithJobQueue(jobstore.NewQueue(app)),
)
runner.Start()

// 3. สั่งรันเอง
run, _ := runner.Trigger(ctx, "sales-report", &ReportParams{Date: &d},
    core.TriggerOptions{By: userID, IdemKey: "sales-report:2026-08-03"})
```

แต่ละบรรทัดในนั้นมีหน้าอธิบายของตัวเอง — [นิยาม](./jobs-defining.md) ·
[runner](./jobs-running.md) · [การสั่งงาน](./jobs-operations.md) ·
[backend](./jobs-store.md)

## Examples

Runnable, one file per topic, in [`examples/jobs/`](../examples/jobs/):

| file | topic |
|---|---|
| `01_basic.go` | scheduled and manual-only jobs |
| `02_params.go` | typed, validated parameters |
| `03_concurrency.go` | limits and the three overlap policies |
| `04_stop.go` | cancellable jobs, pause, shutdown |
| `05_logs.go` | log policies, live tail, reading logs |
| `06_replay.go` | replay, retry, idempotency |
| `07_store.go` | durable runs, custom tables, reporting |
| `08_http_admin.go` | the admin API |
| `09_cluster.go` | what changes with more than one replica |
