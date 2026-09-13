# Runner & Concurrency

`JobRunner` คือ worker pool ที่หยิบ run จากคิวมารัน — ทุก run เดินผ่านมันเส้นเดียว
ไม่ว่าจะมาจาก schedule, จากปุ่มกด, จาก retry หรือจาก replay

```go
runner := core.NewJobRunner(app, reg,
    core.WithWorkers(4),
    core.WithQueues("default", "heavy"),
)
runner.Start()          // ไม่ block
defer runner.Stop(ctx)  // หรือปล่อยให้ Runner จัดการ
```

`NewJobRunner` ใช้งานได้ทันทีโดยไม่ต้องมี option เลย: queue กับ store อยู่ใน memory,
limiter นับในโปรเซส, ไม่เก็บ log ลง database

## Options ทั้งหมด

| option | default | ทำอะไร |
|---|---|---|
| `WithWorkers(n)` | 4 | กี่ run ที่ **process นี้** รันพร้อมกัน |
| `WithQueues(names…)` | `["default"]` | คิวที่ worker นี้หยิบงาน |
| `WithQueueLimit(queue, n)` | ไม่จำกัด | เพดานของทั้งคิว (ข้าม job) |
| `WithJobStore(s)` | in-memory | ที่เก็บ run + log — [ดู backend](./jobs-store.md) |
| `WithJobQueue(q)` | in-memory | ตัวส่งงานถึง worker |
| `WithJobLimiter(l)` | นับในโปรเซส | ตัวนับ slot — เปลี่ยนเป็น redis เมื่อมีหลาย replica |
| `WithLogPolicy(p)` | `LogOff` | นโยบาย log ตั้งต้นของทุก job |
| `WithLogLimits(l)` | 1000 บรรทัด / 256 KB / ทุก level / tail 50 | เพดานของ log ต่อ run |
| `WithSlotBackoff(d)` | 2 วินาที | รออีกนานแค่ไหนก่อนลองขอ slot ใหม่ |
| `WithWorkerID(id)` | uuid 8 ตัวอักษร | ชื่อที่ปรากฏใน `JobRun.WorkerID` |

`WithWorkerID` คุ้มที่จะตั้งใน Kubernetes ให้เป็นชื่อ pod — `JobRun.WorkerID` จะได้บอก
ได้ทันทีว่า run ที่ค้างอยู่นั้นอยู่บน pod ไหน

```go
core.WithWorkerID(os.Getenv("HOSTNAME"))
```

## สามชั้นของการจำกัด

จำกัดได้สามระดับ และมันคนละคำถามกัน:

```go
core.NewJobRunner(app, reg,
    core.WithWorkers(4),               // ชั้น 1: process นี้รันพร้อมกันได้ 4
    core.WithQueueLimit("heavy", 2),   // ชั้น 2: คิว heavy รวมกันได้ 2
)

core.JobDef{Name: "sync", MaxConcurrent: 1}   // ชั้น 3: job นี้ทีละ 1 (singleton)
```

| ชั้น | ตอบคำถาม | นับที่ไหน |
|---|---|---|
| `WithWorkers` | process นี้ทำงานหนักได้แค่ไหน | ในโปรเซส เสมอ |
| `WithQueueLimit` | ทรัพยากรร่วม (database, API ปลายทาง) รับได้แค่ไหน | ผ่าน limiter |
| `JobDef.MaxConcurrent` | job นี้ทับซ้อนตัวเองได้ไหม | ผ่าน limiter |

สองชั้นล่างผ่าน `ILimiter` — ซึ่ง**ค่า default นับเฉพาะในโปรเซสนี้** พอมีหลาย replica
`MaxConcurrent: 1` จึงแปลว่า "หนึ่งต่อ pod" เงียบๆ วิธีแก้อยู่ที่
[Cluster-wide limits](#cluster-wide-limits)

### Queue: แยกงานหนักออกจากงานเบา

`Queue` บน `JobDef` เป็นแค่ป้าย ส่วน `WithQueues` บน runner คือคนเลือกว่าจะหยิบป้ายไหน
สองอย่างนี้รวมกันคือวิธีแยก worker pool โดยไม่ต้องแยก binary:

```go
// deployment A — งานสั้นๆ ตอบเร็ว
core.NewJobRunner(app, reg, core.WithQueues("default"), core.WithWorkers(8))

// deployment B — งานหนัก แยก resource ออกไปเลย
core.NewJobRunner(app, reg, core.WithQueues("heavy"), core.WithWorkers(2))
```

⚠️ **run ที่อยู่ในคิวที่ไม่มี worker ไหนหยิบ จะค้างอยู่ตลอดกาล** โดยไม่มี error — เพิ่ม
`Queue: "heavy"` ให้ job แล้วลืมเพิ่มใน `WithQueues` ของ deployment ไหนเลย คืออาการ
"job ไม่ทำงาน" ที่หาสาเหตุยากที่สุด boot log พิมพ์ทั้ง `queues` ของ runner และ `queue`
ของแต่ละ job ไว้ให้เทียบกันด้วยเหตุผลนี้

### เมื่อชนเพดาน: สามนโยบาย

| policy | what happens | use it for |
|---|---|---|
| `ConcurrencyEnqueue` *(default)* | waits its turn, nothing is lost | ordinary work |
| `ConcurrencySkip` | finishes as `skipped` | **anything on a short schedule** |
| `ConcurrencyReplace` | cancels the older run and takes over | "latest wins" rebuilds |

> A frequent cron job with `Enqueue` is the classic trap: if a run takes longer
> than the interval, the queue grows forever. Use `Skip` there.

ภายใต้ `ConcurrencyEnqueue` run ที่ยังไม่ได้ slot จะกลับไปรอในคิวแล้วลองใหม่ทุก
`WithSlotBackoff` (default 2 วินาที) — ปรับให้ยาวขึ้นถ้ามี job ที่รอ slot นานๆ จนการ
poll ถี่ๆ เริ่มเปลืองไปเปล่าๆ

## Retry

```go
core.JobDef{
    Name:        "recalc",
    MaxAttempts: 3,                                              // รวมครั้งแรก
    Backoff:     core.ExponentialBackoff(time.Second, 2*time.Minute),
}
```

run ที่ fail และยัง `MaxAttempts` เหลือ จะกลับเข้าคิวพร้อม `ScheduledAt` ที่เลื่อนไปตาม
backoff — **เป็น run เดิม attempt ถัดไป** ไม่ใช่ run ใหม่ ประวัติจึงไม่บวมและ
`c.Attempt()` บอกได้ว่านี่ครั้งที่เท่าไหร่

`ExponentialBackoff(base, max)` คูณสองไปเรื่อยๆ จนชนเพดาน เขียน backoff เองก็ได้ —
มันเป็นแค่ `func(attempt int) time.Duration`:

```go
core.JobDef{Backoff: func(attempt int) time.Duration {
    return time.Duration(attempt) * 30 * time.Second   // 30s, 60s, 90s…
}}
```

ที่ต้องคิดก่อนตั้ง `MaxAttempts > 1`: **run ถูกส่งถึงอย่างน้อยหนึ่งครั้ง (at-least-once)**
worker ที่ตายกลางทางทำให้ run กลับเข้าคิวได้ handler จึงต้อง idempotent อยู่ดี ไม่ว่าจะ
ตั้ง retry ไว้เท่าไหร่

## Timeout กับ StopGrace

```go
core.JobDef{Timeout: 10 * time.Minute, StopGrace: time.Minute}
```

- **`Timeout`** — ครบเมื่อไหร่ context ของ run ถูก cancel query, HTTP call, การรอ lock
  ทุกอย่างที่ผูก context ถูกยกเลิกให้เอง
- **`StopGrace`** — หลังจากนั้น runner รอ handler *คืนค่า* อีกเท่านี้ ถ้ายังไม่คืน มันจะ
  ถูกบันทึกว่า canceled แล้วปล่อยทิ้ง พร้อม warning:

```
WARN job ignored cancellation; abandoning it job=import run_id=… grace=30s
```

Go ฆ่า goroutine ไม่ได้ — การยกเลิกจึงเป็นความร่วมมือเสมอ loop ยาวๆ ต้องเช็คเอง:

```go
for _, item := range items {
    if c.IsStopping() {
        return c.Err()
    }
    // …
}
```

handler ที่ไม่เคยเช็คคือ handler ที่ยึด worker ไว้จนกว่าจะจบเอง (แต่ไม่ตลอดกาล — grace
period คือเพดาน)

## Start บอกว่า worker นี้รันอะไรได้บ้าง

`runner.Start()` log ทุก job ใน registry พร้อมค่าที่**มีผลจริง**:

```json
{"level":"INFO","msg":"job runner started","workers":4,"queues":["default"]}
{"level":"INFO","msg":"job registered","job":"nightly-report","queue":"default",
 "timeout":"10m0s","attempts":3,"schedule":"cron(0 2 * * *)",
 "next_run":"2026-07-31T02:00:00+07:00","in":"12h22m29s"}
{"level":"INFO","msg":"job registered","job":"reindex","queue":"default",
 "timeout":"5m0s","attempts":1,"trigger":"manual","max_concurrent":1,"on_conflict":"skip"}
```

"job runner started, workers=4" บอกว่ามี worker ขึ้น แต่ไม่ได้บอกว่ามันเป็น worker
*ของอะไร* — ซึ่งคือสิ่งแรกที่อยากรู้เวลา job ไม่ทำงาน และการไปหาจากโค้ดแปลว่าต้องรู้
ว่า module ไหน register อะไรไว้บ้าง

`timeout` กับ `attempts` เป็นค่าหลัง default แล้ว: job ที่ไม่ตั้ง `Timeout` log
`5m0s` ไม่ใช่ `0s` เพราะ `0s` อธิบาย struct ไม่ได้อธิบายพฤติกรรม

registry ที่ว่างเปล่าได้ **warning** — worker แบบนั้นสตาร์ทได้ปกติแล้วไม่ทำอะไรเลย

ดู [Boot log](./logging-practices.md#boot-log) สำหรับภาพรวมทั้งกระบวนการ start

## Shutdown

```go
runner.Stop(ctx)   // หรือ core.NewRunner(app, core.RunJobs(runner))
```

1. หยุดหยิบงานใหม่ทันที
2. รอ run ที่ทำอยู่จนครบ deadline ของ `ctx`
3. ครบแล้วยังไม่เสร็จ → สั่ง cancel (แต่ละ handler ยังได้ `StopGrace` ของตัวเอง)
4. run ที่ไม่จบ **กลับเข้าคิว ไม่ใช่ถูกทำเครื่องหมายว่า fail** — มันยังไม่เคยมีโอกาสได้ fail

```
INFO job run interrupted by shutdown, returned to the queue job=import run_id=…
```

ข้อสุดท้ายมีผลจริงกับการออกแบบ: run ที่กลับเข้าคิวจะถูกรันใหม่ตั้งแต่ต้นโดย worker ตัวไหน
ก็ได้ — อีกเหตุผลที่ handler ต้อง idempotent และเป็นเหตุผลที่ job ยาวๆ ควรแบ่งเป็นช่วง
ที่บันทึกความคืบหน้าไว้ ไม่ใช่ทำรวดเดียวสามชั่วโมง

⚠️ ใช้ in-memory queue อยู่ **run ที่กลับเข้าคิวจะหายไปพร้อม process** ความปลอดภัยตรง
นี้มาจากการเปลี่ยนไปใช้ [SQL backend](./jobs-store.md)

## Cluster-wide limits

Everything above assumes a single replica, where in-process concurrency limits
are exactly correct. Running several replicas means replacing implementations,
not rewriting jobs — handlers and job definitions do not change.

The default limiter counts in this process, so `MaxConcurrent: 1` quietly means
"one run per pod": three replicas, three settlement runs. Counting in redis
instead makes the limit mean what it says.

```go
lim, err := core.NewRedisLimiter(app)
if err != nil {
    return err
}
runner := core.NewJobRunner(app, reg, core.WithJobLimiter(lim))
```

It uses the cache you already configured (`CACHE_*`) — no new infrastructure, and
nothing to migrate. Both limits go through it: `JobDef.MaxConcurrent` and
`WithQueueLimit`.

A slot is held on a **lease** that the limiter renews while the run lasts. That is
what makes it safe to kill a pod: the slot is released by the holder falling
silent, so a crash cannot wedge a singleton job, and no cleanup job is needed.
Leases are timed by redis's own clock, not the caller's, so replicas whose clocks
disagree still agree on who holds what (needs redis 5 or newer).

```go
core.NewRedisLimiter(app,
    core.WithLimiterCache("jobs"),      // a named cache, not the default one
    core.WithLimiterLease(time.Minute), // how long a dead holder keeps its slot
    core.WithLimiterPrefix("group:limiter:"), // share one limit between services
)
```

Two things worth knowing before you deploy it:

- **It refuses to build without redis** rather than falling back to counting in
  one process. A `MaxConcurrent: 1` job that silently runs once per replica is the
  bug this exists to prevent, so the misconfiguration is a boot error. On a single
  replica, keep the default `core.NewInProcessLimiter()`.
- **A redis outage counts as "no slot free"**, never as "go ahead". Under the
  default `ConcurrencyEnqueue` the run simply comes back after the slot backoff,
  so an outage delays work. Under `ConcurrencySkip` the same outage skips runs —
  the log line says which.

By default each service's limits are its own, namespaced by its cache prefix.
`WithLimiterPrefix` is how two services that run the same job count as one.

`ILimiter` เป็น interface สองเมธอด (`Acquire` / `Count`) — backend อื่นเขียนเองได้
ถ้าจำเป็น แต่ทั้ง redis และ in-process ครอบคลุมกรณีที่เจอจริงเกือบทั้งหมด

### ยังต้องคิดเองเมื่อมีหลาย replica

- **scheduler** — arm ไว้ที่ replica เดียว หรือให้ job ที่ตั้งเวลาไว้ใช้
  `MaxConcurrent: 1` + `ConcurrencySkip` เพื่อให้ enqueue ที่ซ้ำกันยุบเหลือหนึ่ง
- **queue** — ยังเป็น polling บน SQL อยู่ ถ้า latency ระดับวินาทีไม่พอ ค่อยเปลี่ยน
  `IJobQueue` เป็น redis
- **cancel** — ส่งผ่าน store แล้ว poll ทุก 2 วินาที ไม่ใช่ push
