# Best Practices & Recipes

สิ่งที่ระบบ job ไม่ได้บังคับ แต่ service ที่รันมานานทุกตัวจบลงที่การทำแบบนี้

## กฎสิบข้อ

| # | กฎ | ทำไม |
|---|---|---|
| 1 | handler ต้อง **idempotent** | run ถูกส่งถึงอย่างน้อยหนึ่งครั้ง — worker ตาย = run กลับเข้าคิว |
| 2 | job ที่ตั้งเวลาถี่ ใช้ `ConcurrencySkip` | `Enqueue` + รอบที่สั้นกว่างาน = คิวโตไม่มีที่สิ้นสุด |
| 3 | ตั้ง `Timeout` ให้ตรงความจริง | 5 นาที default คือค่ากลาง ไม่ใช่ค่าที่ถูกสำหรับ job ของคุณ |
| 4 | loop ยาวต้องเช็ค `c.IsStopping()` | ไม่งั้น deploy ทุกครั้งคือการรองาน 30 วินาทีแล้วทิ้งมัน |
| 5 | ใช้ `IdemKey` ทุกครั้งที่ trigger จากปุ่มหรือ webhook | กดสองครั้ง = run เดียว |
| 6 | จับคู่ `Queue` ของ job กับ `WithQueues` ของ runner เสมอ | คิวที่ไม่มีใครหยิบ = ค้างเงียบ ไม่มี error |
| 7 | ใช้ [SQL backend](./jobs-store.md) ตั้งแต่ก่อนขึ้น production | in-memory = ประวัติหายทุก deploy |
| 8 | ตั้ง purge ตั้งแต่วันแรก | `job_runs` + `job_run_logs` โตเงียบๆ จนหน้า admin ช้า |
| 9 | เก็บ parameter เป็น **id ไม่ใช่ก้อนข้อมูล** | parameter ถูกเก็บถาวรใน database และไม่ได้ถูกมาสก์ |
| 10 | job ที่แตะเงิน ตั้ง `Replayable: core.BoolPtr(false)` | ปุ่ม replay ในหน้า admin ไม่ควรจ่ายเงินซ้ำได้ |

## Idempotency

run เดิมอาจถูกรันซ้ำจากสามทาง: retry, requeue ตอน shutdown และ replay ของคน วิธีกันคือ
ทำให้ **ผลลัพธ์ของการรันซ้ำเท่ากับรันครั้งเดียว** ไม่ใช่พยายามกันไม่ให้ซ้ำ

```go
// ✅ เขียนแบบมีเงื่อนไข — รันซ้ำแล้วไม่มีอะไรเปลี่ยน
repository.New[Invoice](ctx).
    Where("id = ? AND status = ?", id, "pending").
    Updates(map[string]any{"status": "settled", "settled_at": time.Now()})

// ✅ ทำเครื่องหมายไว้ในตารางของงานเอง
_, err := repository.New[Settlement](ctx).DB().
    Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "period"}}, DoNothing: true}).
    Create(&Settlement{Period: period}).Rows()
```

งานที่ผลลัพธ์อยู่ **นอก** database ของเรา (ส่งเมล, ยิง API, โอนเงิน) กันซ้ำที่ตัว
provider — idempotency key ของ payment gateway, `MessageID` ของ MQ, หรือแถวใน
database ที่บันทึกว่าส่งไปแล้วก่อนส่งจริง

⚠️ อย่าพึ่ง `c.Attempt() == 1` เป็นตัวบอกว่า "ครั้งแรก" — run ที่ถูก requeue ตอน shutdown
กลับมาด้วย attempt เดิม

## แบ่งงานใหญ่ให้เดินต่อได้

job สามชั่วโมงที่ทำรวดเดียวคือ job ที่ deploy ทุกครั้งแล้วเริ่มใหม่จากศูนย์

```go
// ❌ ล้มที่แถว 900,000 = เสียทั้งหมด
func rebuild(c core.ICronjobContext) error {
    rows, _ := repository.New[Order](c).FindAll()
    for _, o := range rows { … }
}

// ✅ ทำเป็นก้อน บันทึกความคืบหน้า และหยุดได้
func rebuild(c core.ICronjobContext) error {
    for {
        if c.IsStopping() {
            return c.Err()      // deploy = หยุดตรงนี้ รอบหน้าทำต่อจากเดิม
        }
        batch, err := repository.New[Order](c).
            Where("rebuilt_at IS NULL").Order("id").Limit(500).FindAll()
        if err != nil || len(batch) == 0 {
            return err
        }
        if err := process(c, batch); err != nil {
            return err
        }
        c.Progress(done*100/total, fmt.Sprintf("%d/%d", done, total))
    }
}
```

สามอย่างที่ได้มาพร้อมกัน: หยุดได้ทุกเมื่อ, รันซ้ำแล้วทำต่อจากที่ค้าง, และคนที่เฝ้าหน้า
admin เห็นว่ามันเดินอยู่จริง

## เลือกนโยบายให้ตรงกับชนิดของงาน

| ชนิดงาน | ตั้งแบบนี้ |
|---|---|
| sync/รีเฟรชข้อมูลทุก 1–5 นาที | `MaxConcurrent: 1` + `ConcurrencySkip` + `MaxAttempts: 1` |
| ส่งเมล / push ต่อหนึ่งรายการ | `MaxAttempts: 3–5` + `ExponentialBackoff`, idempotent ที่ provider |
| รายงาน/ตัดยอดรายวัน | `Queue: "heavy"`, `Timeout` ยาว, `LogAlways`, `Replayable: false` ถ้ามีผลทางการเงิน |
| rebuild/reindex ที่ "ล่าสุดชนะ" | `ConcurrencyReplace` |
| งานที่ user กดเอง | `IdemKey` + `TriggerAndWait` ถ้าหน้าจอรอผล |

## Retry ที่มีความหมาย

retry ช่วยเฉพาะความล้มเหลว **ชั่วคราว** — job ที่ fail เพราะ bug หรือเพราะ input ผิด
ยิงซ้ำอีกสามครั้งก็ได้ผลเดิม แถมทำให้ log เต็มไปด้วยเสียงรบกวน

```go
func handler(c core.ICronjobContext) error {
    if err := callProvider(c); err != nil {
        if isTemporary(err) {
            return err                  // ให้ runner retry ตาม MaxAttempts
        }
        c.Log().Error("provider ปฏิเสธคำขอนี้อย่างถาวร", "err", err)
        c.SetResult(map[string]any{"skipped": true})
        return nil                      // ไม่ใช่ความล้มเหลวของระบบ อย่าให้มัน retry
    }
    return nil
}
```

ตั้ง `MaxAttempts` คู่กับ `Backoff` เสมอ — 3 ครั้งติดกันในหนึ่งวินาทีไม่ต่างอะไรกับ
ครั้งเดียว ปลายทางที่ล่มยังไม่ทันฟื้น

## เฝ้าดูอะไร

สาม query นี้ครอบคลุมเกือบทุกปัญหาที่เกิดกับ job ในระบบจริง:

```go
// 1. fail ในชั่วโมงที่ผ่านมา — ตัวเลขนี้ควรเป็น 0 เกือบตลอด
repository.New[core.JobRun](ctx).
    Where("status = ? AND finished_at > ?", core.RunFailed, time.Now().Add(-time.Hour)).
    Count()

// 2. run ที่ค้าง running นานผิดปกติ — worker ตายโดยไม่ได้ requeue
repository.New[core.JobRun](ctx).
    Where("status = ? AND started_at < ?", core.RunRunning, time.Now().Add(-time.Hour)).
    FindAll()

// 3. job ที่ควรรันทุกวันแต่ไม่มี run สำเร็จเลยวันนี้ — เงียบกว่าทุกแบบ
repository.New[core.JobRun](ctx).
    Where("job_name = ? AND status = ? AND finished_at > ?",
        "settlement", core.RunSucceeded, startOfDay).
    Exists()
```

ข้อ 3 คืออันที่คนลืมบ่อยที่สุด: **job ที่ไม่เคยถูกยิงเลย ไม่มี error ให้ alert** —
scheduler ที่ไม่ได้ start, คิวที่ไม่มี worker, job ที่ถูก pause ค้างไว้ ล้วนหน้าตา
เหมือนกันคือ "เงียบ"

ทำให้เป็น job ตัวหนึ่งไปเลย:

```go
_ = reg.Register(core.JobDef{
    Name: "ops.watchdog", Schedule: core.Cron("*/30 * * * *"),
}, func(c core.ICronjobContext) error {
    ok, err := hasSucceededToday(c, "settlement")
    if err != nil {
        return err
    }
    if !ok && time.Now().Hour() > 3 {
        // log ระดับ error = ขึ้น Sentry ผ่าน bridge ของ logger
        c.Log().Error("settlement ยังไม่สำเร็จวันนี้", "job", "settlement")
    }
    return nil
})
```

## Recipes

### งานที่ user กดแล้วรอผล

```go
func RunReport(c core.IHTTPContext) error {
    run, err := runner.Trigger(c, "report", params, core.TriggerOptions{
        By:      c.GetUser().ID,
        IdemKey: "report:" + c.GetUser().ID + ":" + today,
    })
    if err != nil {
        return err
    }
    return c.JSON(http.StatusAccepted, echo.Map{"run_id": run.ID})   // อย่ารอที่นี่
}
```

ให้ frontend poll `GET /runs/:id` แทนการค้าง request ไว้ — `Progress` ที่ handler
รายงานไว้ทำให้หน้าจอมี progress bar ได้ฟรี

### fan-out: หนึ่ง job แตกเป็นหลาย job

```go
// job แม่: แตกงานแล้วจบเร็ว
func planExports(c core.ICronjobContext) error {
    tenants, err := repository.New[Tenant](c).FindAll()
    if err != nil {
        return err
    }
    for _, t := range tenants {
        if _, err := runner.Trigger(c, "export-tenant", ExportParams{TenantID: t.ID},
            core.TriggerOptions{Trigger: core.TriggerEvent, IdemKey: "export:" + t.ID + ":" + today}); err != nil {
            return err
        }
    }
    return nil
}
```

ดีกว่าการวนทำเองในหนึ่ง job เพราะ tenant ที่ fail ไม่ลาก tenant อื่นลงไปด้วย และ
retry เกิดเฉพาะตัวที่ล้ม

### งานตามเวลาที่ต้องไม่ทับกันข้าม replica

```go
core.JobDef{Name: "settlement", Schedule: core.Cron("0 2 * * *"),
    MaxConcurrent: 1, Concurrency: core.ConcurrencySkip}

runner := core.NewJobRunner(app, reg, core.WithJobLimiter(redisLimiter))
```

scheduler ที่ตื่นพร้อมกันทุก replica จะ enqueue หลาย run — `MaxConcurrent: 1` + `Skip` +
[redis limiter](./jobs-running.md#cluster-wide-limits) ทำให้เหลือรันจริงตัวเดียว ที่เหลือจบ
เป็น `skipped` ซึ่งเป็นสถานะที่อ่านออกว่า "ไม่ได้พัง แค่ไม่ต้องทำ"

## Testing

```go
func TestReport_isIdempotent(t *testing.T) {
    j := coretest.NewJob(t, coretest.WithAutoMigrate(&models.Report{}))
    j.Register("report", jobs.Report)

    first := j.Run("report", map[string]any{"date": "2026-01-01"})
    second := j.Run("report", map[string]any{"date": "2026-01-01"})

    require.Equal(t, core.RunSucceeded, first.Status)
    require.Equal(t, core.RunSucceeded, second.Status)

    n, _ := repository.New[models.Report](j.Context()).Count()
    assert.EqualValues(t, 1, n, "รันซ้ำวันเดิมต้องไม่สร้างรายงานใบที่สอง")
}
```

`coretest.NewJob` รันผ่าน runner จริง — parameter ถูก validate, panic กลายเป็น error
และผลถูกบันทึกเป็นสถานะ เหมือนตอนรันจริงทุกอย่าง ([Reference: Jobs](./testing-jobs.md))

เคสที่คุ้มที่สุดสามอัน: **รันซ้ำสองครั้งต้องได้ผลเท่าเดิม**, **parameter ที่ผิดต้องตกที่
validation ไม่ใช่กลาง handler** และ **`IsStopping()` ถูกเคารพ** (ยิง cancel แล้ว handler
ต้องคืนค่าภายใน grace period)

## Checklist ก่อนขึ้น production

- [ ] store + queue เป็น SQL ไม่ใช่ in-memory
- [ ] มี purge job ตั้งเวลาไว้ และ `RetainLogs` สั้นกว่า `RetainRuns`
- [ ] ทุก job ที่ตั้งเวลาถี่ ตั้ง `ConcurrencySkip`
- [ ] handler ทุกตัวที่มี loop เช็ค `IsStopping()`
- [ ] `Queue` ของทุก job อยู่ใน `WithQueues` ของ deployment สักตัว
- [ ] มี alert ที่ run ซึ่ง fail และที่ job ที่ *ไม่* ทำงาน
- [ ] `WithWorkerID` ตั้งเป็นชื่อ pod
- [ ] job ที่อันตราย ปิด replay
- [ ] admin API อยู่หลัง role ที่แคบ ([Security](./security.md#_9-jobs-admin-api))
- [ ] มีหลาย replica → ใช้ redis limiter
