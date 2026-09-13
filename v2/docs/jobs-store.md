# Store & Queue Backends

ระบบ job แยกเป็นสองส่วนที่เปลี่ยนได้อิสระ:

| | หน้าที่ | interface |
|---|---|---|
| **Store** | เก็บ run และ log — สถานะ, ประวัติ, ผลลัพธ์, การขอ cancel | `IJobStore` |
| **Queue** | ส่ง run ที่ถึงเวลาแล้วให้ worker | `IJobQueue` |

ที่แยกกันเพราะมันตอบคนละคำถาม — "ประวัติอยู่ที่ไหน" กับ "งานเดินทางยังไง" — และการ
แยกนี้คือสิ่งที่ทำให้ใช้ queue ใน memory คู่กับ store ที่ durable ได้ หรือเปลี่ยนไปใช้
redis ทีหลังโดยไม่ต้องแตะ handler สักบรรทัด

## Default: in-memory

```go
runner := core.NewJobRunner(app, reg)   // = NewMemoryJobStore() + NewMemoryJobQueue()
```

ไม่ต้องตั้งค่าอะไร ไม่ต้องมีตาราง — เหมาะกับ dev, เทส และ service เล็กที่ยอมให้ run ที่
ค้างในคิวหายไปพร้อม process

สิ่งที่หายไปเมื่อ restart: run ที่ `queued`, run ที่กำลังรันแล้วถูก requeue ตอน shutdown,
และประวัติทั้งหมด `runner.Runs()` หลัง restart จึงว่างเปล่า

## Durable runs (SQL)

For history and restart safety, use the repository-backed store — GORM
underneath, no extra infrastructure. The runs table doubles as the queue.

```go
import "github.com/pskclub/mine-core/v2/jobstore"

_ = jobstore.Migrate(db)   // or take the DDL into your own migration tool

runner := core.NewJobRunner(app, reg,
    core.WithJobStore(jobstore.New(app)),
    core.WithJobQueue(jobstore.NewQueue(app)),
)
```

Placement is configurable, without any global state:

```go
jobstore.New(app,
    jobstore.WithTables("ops_job_runs", "ops_job_run_logs"),
    jobstore.WithConnection("ops"),   // core.IContext.DBS("ops")
)

jobstore.NewQueue(app,
    jobstore.WithPollInterval(500*time.Millisecond),   // default 1s
)
```

| option | default | หมายเหตุ |
|---|---|---|
| `WithTables(runs, logs)` | `job_runs`, `job_run_logs` | ต้องส่งค่าเดียวกันให้ทั้ง `New`, `NewQueue` และ `Migrate` |
| `WithConnection(name)` | connection `default` | แยกประวัติ job ไปคนละ database ได้ |
| `WithPollInterval(d)` | 1 วินาที | queue เท่านั้น — ถี่ขึ้น = latency ต่ำลง แลกกับ query ที่มากขึ้น |

`Migrate` สร้างสองตารางพร้อม index ที่ runner พึ่งพา — `(job_name, status)` สำหรับการ
ไล่ดู, `(status, scheduled_at)` สำหรับ poll ของคิว และ `(run_id, seq)` สำหรับ page log

core **ไม่เคย** รัน migration ให้เอง: service ส่วนใหญ่เป็นเจ้าของ migration ของตัวเอง
เรียก `Migrate(db)` ตอน boot ได้ใน service เล็กๆ ส่วนที่อื่นให้รันมันครั้งเดียวกับ database
เปล่าแล้วเอา DDL ที่ได้เข้าเครื่องมือ migration ของตัวเอง — ดู [Migrations](./migrations.md)

### query ประวัติได้เหมือนตารางอื่น

`core.JobRun` เป็น `core.IModel` ธรรมดา ประวัติ job จึง query ด้วย repository ตัวเดิม
และ join กับตารางของเราเองได้:

```go
repository.New[core.JobRun](ctx).
    Where("job_name = ? AND status = ?", "settlement", core.RunFailed).
    Where("created_at > ?", since).
    Order("created_at desc").
    FindAll()
```

รายงานที่มักได้ใช้จริง: จำนวน fail ต่อวัน, run ที่ค้างสถานะ `running` นานผิดปกติ (worker
ตายไปโดยไม่ได้ requeue), และ p95 ของ `duration_ms` ต่อ job

```go
// run ที่ค้างเกินหนึ่งชั่วโมง — สัญญาณว่ามี pod ตายกลางทาง
repository.New[core.JobRun](ctx).
    Where("status = ? AND started_at < ?", core.RunRunning, time.Now().Add(-time.Hour)).
    FindAll()
```

## เขียน backend เอง

Both are interfaces, so a Mongo or Redis backend is a drop-in replacement.

```go
type IJobStore interface {
    Create(ctx context.Context, run *JobRun) IError
    Update(ctx context.Context, run *JobRun) IError
    Get(ctx context.Context, id string) (*JobRun, IError)
    List(ctx context.Context, f JobRunFilter) (*Page[JobRun], IError)

    FindActive(ctx context.Context, jobName, idemKey string) (*JobRun, IError)
    RunningIDs(ctx context.Context, jobName string) ([]string, IError)

    RequestCancel(ctx context.Context, id, by, reason string) IError
    IsCancelRequested(ctx context.Context, id string) (bool, IError)

    AppendLogs(ctx context.Context, entries []JobLog) IError
    Logs(ctx context.Context, runID string, afterSeq int64, limit int) ([]JobLog, IError)

    Purge(ctx context.Context, runsBefore, logsBefore time.Time) (int64, IError)
    Close() IError
}

type IJobQueue interface {
    Enqueue(ctx context.Context, run *JobRun) IError
    Reserve(ctx context.Context, queues []string) (*JobRun, AckFunc, IError)
    Remove(ctx context.Context, runID string) (bool, IError)
    Len(ctx context.Context) (int, IError)
    Close() IError
}
```

ข้อสัญญาที่ต้องรักษา:

- **`Reserve` บล็อกจนกว่าจะมีงานถึงเวลา** หรือ `ctx` จบ และต้องเคารพ `ScheduledAt`
  (งานที่เลื่อนเวลาไว้ยังไม่ถึงคิว) กับ `queues` ที่ worker สนใจ
- **`AckFunc` ที่ได้กลับมาต้องถูกเรียกเสมอ** — ส่ง `nil` เมื่อ runner จัดการ run นั้นจบแล้ว,
  ส่ง error เมื่อการ *ส่งมอบ* ล้มเหลว เพื่อให้ run กลับมาใหม่ได้
- **`Remove` ต้องลบเฉพาะ run ที่ยังไม่ถูก reserve** — นี่คือวิธีที่ run ที่ยังไม่เริ่มถูก
  cancel โดยไม่เคยรัน
- **`Update` ห้ามทับคอลัมน์ของการ cancel** — worker ถือสำเนาที่อ่านมาก่อนมีคนกด cancel
  เขียนทับทั้งก้อนคือการลบข้อมูลว่าใครสั่งหยุดและเพราะอะไร ตอนที่มันสำคัญที่สุด
  (implementation ของ SQL ใช้ `Omit` สามคอลัมน์นั้นด้วยเหตุผลนี้)

### queue ที่ใช้ storage เดียวกับ store

```go
type IStoreBackedQueue interface {
    IJobQueue
    SharesStore() bool
}
```

A queue whose storage *is* the store implements `core.IStoreBackedQueue`
(`SharesStore() bool`). The runner then skips `Enqueue` after writing a run:
writing it is already what makes it available, and a second write can flip a
run back to queued after another worker claimed it — running it twice.

queue ที่เก็บของตัวเอง (in-memory, redis, RabbitMQ) ไม่ต้อง implement อันนี้ และจะได้
`Enqueue` ตามปกติ

### พิสูจน์ว่ามันถูก

A new backend is correct when it passes the shared conformance suite:

```sh
make test-integration        # or: go test --tags=integration ./...
```

It runs the same contract against every backend — in-memory, SQL through the
repository, and (when `test.env` points at one) the database your service really
uses. Adding a backend means adding one line to `backends()` in
`jobstore/repo_store_test.go`.

ชุดนี้จับสิ่งที่เขียนเองแล้วมักพลาด: run ที่ถูก reserve พร้อมกันโดยสอง worker,
`FindActive` ที่ต้องไม่คืน run ที่จบแล้ว, และ log ที่ต้องเรียงตาม `seq` ข้าม attempt

## เลือกยังไง

| สถานการณ์ | store | queue |
|---|---|---|
| dev, เทส, script | memory | memory |
| single replica, ยอมเสีย run ตอน restart ไม่ได้ | `jobstore.New` | `jobstore.NewQueue` |
| หลาย replica | `jobstore.New` | `jobstore.NewQueue` + [redis limiter](./jobs-running.md#cluster-wide-limits) |
| ต้องการ latency ต่ำกว่าหนึ่งวินาที | `jobstore.New` | เขียน queue บน redis เอง |

ผสมข้ามกันได้ แต่มีคู่ที่ **ไม่ควร**: store แบบ memory + queue แบบ SQL — คิวจะมี run ที่
store ไม่รู้จักหลัง restart แล้ว worker จะหยิบขึ้นมาเจอ `JOB_RUN_NOT_FOUND`
