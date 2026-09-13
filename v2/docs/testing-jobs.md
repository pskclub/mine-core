# Testing — Jobs

`NewJob` รัน job ผ่าน runner จริง เส้นทางเดียวกับที่ schedule หรือ manual trigger
ใช้ — parameter ถูก validate, panic กลายเป็น error, และผลถูกบันทึกเป็นสถานะ

```go
func TestNightlyReport(t *testing.T) {
    j := coretest.NewJob(t, coretest.WithAutoMigrate(&models.Report{}))
    j.Register("nightly-report", jobs.NightlyReport)

    run := j.Run("nightly-report", map[string]any{"date": "2026-01-01"})

    assert.Equal(t, core.RunSucceeded, run.Status)
}
```

`Run` trigger job แล้วรอจนจบ คืน `*core.JobRun` ที่บันทึกไว้ ไม่ต้องจัดการ
goroutine, channel หรือ sleep เอง — runner ถูก start ตอนสร้างและ stop ให้เมื่อ
เทสจบผ่าน `t.Cleanup`

## job ที่ fail ไม่ทำให้เทส fail

จุดนี้สำคัญ: `Run` คืนผลลัพธ์ ไม่ได้ fail เทสให้เมื่อ job พัง เพราะบ่อยครั้ง
ความล้มเหลว**คือสิ่งที่กำลังเทสอยู่**

```go
j.Register("bad", func(c core.ICronjobContext) error { return errmsgs.BadRequest })

run := j.Run("bad", nil)

assert.Equal(t, core.RunFailed, run.Status)
require.NotNil(t, run.Error)
assert.Equal(t, "BAD_REQUEST", run.Error.Code)
```

`Run` จะ fail เทสก็ต่อเมื่อ trigger เองไม่สำเร็จ (ชื่อ job ไม่มีอยู่จริง, runner
ตาย, หรือรอเกิน 30 วินาที) — คือปัญหาของ harness ไม่ใช่ของ job

## parameters

ส่ง params แบบเดียวกับ trigger จริง ซึ่งแปลว่า `Params` และ validation ของ job
ทำงานด้วย:

```go
type ReportParams struct {
    Date *string `json:"date"`
}

func (p *ReportParams) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Str("date", p.Date).Required().Date()
    return v.Error()
}
```

เทสว่า params ที่ผิดถูกปฏิเสธ **ก่อน** handler ถูกเรียก:

```go
called := false
j.Register("report", func(c core.ICronjobContext) error {
    called = true
    return nil
})

run := j.Run("report", map[string]any{"date": "not-a-date"})

assert.Equal(t, core.RunFailed, run.Status)
assert.False(t, called, "handler ต้องไม่ถูกเรียกเมื่อ params ไม่ผ่าน")
assert.Contains(t, run.Error.Fields, "date")
```

## retry, timeout, concurrency

`Register` ใช้ค่า default ทั้งหมด ถ้าต้องเทสพฤติกรรมพวกนี้ให้ใช้ `RegisterDef`
พร้อม `core.JobDef` เต็ม:

```go
attempts := 0
j.RegisterDef(core.JobDef{
    Name:       "flaky",
    MaxRetries: 2,
}, func(c core.ICronjobContext) error {
    attempts++
    if c.Attempt() < 3 {
        return errors.New("not yet")
    }
    return nil
})

run := j.Run("flaky", nil)

assert.Equal(t, core.RunSucceeded, run.Status)
assert.Equal(t, 3, attempts, "ลองใหม่จนสำเร็จในครั้งที่สาม")
```

`c.Attempt()` เป็น 1-based — attempt ที่ 2 คือ retry ครั้งแรก

## panic ไม่ล้ม scheduler

```go
j.Register("boom", func(c core.ICronjobContext) error { panic("kaboom") })

run := j.Run("boom", nil)

assert.Equal(t, core.RunFailed, run.Status)
require.NotNil(t, run.Error)
```

runner จับ panic แล้วแปลงเป็น error พร้อม stack — process ไม่ตาย และ job อื่นยังรัน
ต่อได้ เทสนี้ยืนยันว่าพฤติกรรมนั้นยังอยู่

## job ที่แตะ database

job ใช้ capability ชุดเดียวกับ handler ทุกอย่าง:

```go
j := coretest.NewJob(t, coretest.WithAutoMigrate(&models.User{}))
j.Register("cleanup", jobs.CleanupUsers)

// seed ก่อน
repository.New[models.User](j.Context()).Create(&stale)

run := j.Run("cleanup", nil)
require.Equal(t, core.RunSucceeded, run.Status)

// ตรวจผลหลัง job ทำงาน
n, err := repository.New[models.User](j.Context()).Count()
require.Nil(t, err)
assert.Equal(t, int64(0), n)
```

`j.Context()` คืน context บน pool เดียวกับที่ job ใช้ ทั้ง seed และ assert จึงเห็น
ข้อมูลชุดเดียวกัน

## เทส job ร่วมกับ API

```go
app := coretest.NewApp(t, coretest.WithAutoMigrate(&models.User{}))

srv := coretest.NewServerWithApp(t, app, nil)
job := coretest.NewJobWithApp(t, app)

srv.Post("/users", payload).RequireStatus(201)   // สร้างผ่าน API
run := job.Run("sync-users", nil)                // job เห็นทันที

assert.Equal(t, core.RunSucceeded, run.Status)
```

## เข้าถึง runner ตรงๆ

`j.Runner()` ให้ `*core.JobRunner` สำหรับสิ่งที่ wrapper ไม่ได้ครอบ — ดูคิว, ยกเลิก
run, replay, หรือตรวจ log ของ run

```go
runs, err := j.Runner().Store().List(ctx, core.JobRunFilter{JobName: "report"})
```

## หมายเหตุ

- **หนึ่ง `NewJob` = หนึ่ง runner + หนึ่ง database** เทสไม่รบกวนกัน
- runner ใช้คิวและ store แบบ **in-memory** ตามค่าเริ่มต้น ซึ่งเหมาะกับเทส ถ้าจะเทส
  store ที่ persist จริงต้องประกอบ runner เองด้วย `core.NewJobRunner`
- `Run` รอสูงสุด 30 วินาที — เผื่อไว้สำหรับ query ที่ช้าบน database ที่เพิ่งเริ่ม
  ไม่ใช่เพื่อรองรับ job ที่ค้าง
