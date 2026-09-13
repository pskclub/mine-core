# Sentry (Error Tracking)

`core.ISentry` — ระบบ track error ที่ผูกกับ context เหมือนทุก capability อื่น
(`ctx.Sentry()`) ตั้ง **DSN ตัวเดียว** แล้ว error / panic / job ที่ fail /
breadcrumb ถูกส่งครบโดยไม่ต้องเขียนโค้ดเพิ่ม

```sh
APP_SENTRY_DSN=https://xxx@sentry.io/123
```

เท่านี้ — ไม่ต้องแก้ handler, ไม่ต้อง init เอง, ไม่ต้อง `defer sentry.Recover()`

> **ไม่มี DSN = no-op** ทุก method ของ `ctx.Sentry()` เรียกได้ปลอดภัยเสมอ
> (คืน `""`) จึงเขียนโค้ดแบบเดียวกันได้ทั้ง dev / test / prod โดยไม่ต้อง `if`

## สิ่งที่ถูกส่งอัตโนมัติ

| เหตุการณ์ | ทำอะไรให้ | จุดที่ทำงาน |
|---|---|---|
| `ctx.NewError(...)` ที่ status ≥ 500 | capture ทันทีตรงจุดที่สร้าง error (ยังมี context ครบที่สุด) พร้อมผูก event id กลับเข้า error | [context.go](../context.go) |
| error ที่ handler `return` ออกมา | capture ถ้ายังไม่เคยถูก capture + ใส่ tag `http.method` / `http.route` | HTTP middleware |
| **`ctx.Log()` ที่พก error status ≥ 500** | capture — error ที่ "จัดการเองแล้ว log ทิ้งไว้" จึงไม่หายไปจาก Sentry | logger bridge |
| **panic** ใน handler | capture ที่ level `fatal` + tag `panic=true` แล้วตอบ 500 (ไม่ crash) | recover middleware |
| **job run fail** | capture พร้อม tag `job` / `run_id` / `attempt` / `queue` / `trigger` + log tail | job runner |
| **scheduler enqueue fail** | capture (tick ที่ไม่ได้กลายเป็น run จะไม่มีที่อื่นบันทึกเลย) | scheduler |
| ทุกบรรทัด `ctx.Log()` | กลายเป็น **breadcrumb** ของ request/run นั้น | logger bridge |
| ทุกบรรทัด `ctx.Log()` | ส่งเข้า **Sentry Logs** (ต้อง opt-in — ดูหัวข้อ Logs) | logger bridge |
| query ผ่าน GORM ตอนเปิด tracing | กลายเป็น **span** ใน trace ของ request นั้น | `InstrumentGorm` |
| HTTP call ขาออก (`core.Requester(ctx)`) | breadcrumb `http.client` (method, url, status, duration) | resty hook |
| `mq.Publish` | breadcrumb `mq.publish` | mq |
| query ผ่าน GORM | breadcrumb `db.*` (ต้อง opt-in — ดูหัวข้อ Database) | `InstrumentGorm` |

ทุก event ไม่ว่ามาทางไหน มาพร้อมชุดเดียวกัน: user (`ctx.GetUser()`), scoped data
(`ctx.GetAllData()`), config ที่ scrub แล้ว, `request_id`, `mode`
(`http`/`cron`/`mq`), request + body ที่ scrub แล้ว, breadcrumb และ stack trace
ของ **จุดที่ error เกิด** (ไม่ใช่จุดที่ capture)

## กติกาข้อเดียว: status ≥ 500 = incident

ตัวตัดสินว่าจะยิงเข้า Sentry ไหมคือ **status ของ error** ไม่ใช่ log level และ
ไม่ใช่ว่าคุณเรียก method ไหน

| เขียนแบบนี้                                 | ได้อะไร                                  |
| ---------------------------------------------| ------------------------------------------|
| `ctx.Log().Info("...")`                       | breadcrumb                              |
| `ctx.Log().Info("...", "err", err400)`        | breadcrumb — client ผิด ไม่ใช่ incident |
| `ctx.Log().Error("...", "err", err400)`       | breadcrumb — client ผิด ไม่ใช่ incident |
| `ctx.Log().Error("...", "err", err500)`       | **event** + breadcrumb                  |
| `ctx.Log().Error("...", "err", errors.New())` | **event** — ไม่มี status ให้ตัดสิน      |
| `ctx.Log().Error("...")` (ไม่มี error)        | **event** (message) — ไม่มี status      |
| `return ctx.NewError(err, errmsgs.X)` (500)   | **event**                               |
| `return errmsgs.BadRequest`                   | ไม่มีอะไร                               |

ปรับเส้นแบ่งได้ที่ `APP_SENTRY_MIN_STATUS` — เส้นเดียวกันทั้งระบบ

### error ที่ไม่มี status ถูกส่งเสมอ

status คือ**คำตัดสิน**ที่ใครสักคนให้ไว้แล้วว่าความล้มเหลวนั้นเป็นแบบไหน — 404 คือ
ผู้เรียกขอของที่ไม่มี, 400 คือส่งข้อมูลมาผิด `SENTRY_MIN_STATUS` กรองบนคำตัดสินนั้น
และทำงานได้เพราะคำตัดสินมีอยู่

**ไม่มี status = ไม่มีใครตัดสินไว้** — error จาก driver, การ marshal ที่พัง,
`errors.New` จาก library ไม่มีอะไรให้เอาไปเทียบกับเส้นแบ่ง และการตีความว่า
"ไม่รู้" เท่ากับ "ไม่สำคัญพอจะรายงาน" จะซ่อนความล้มเหลวที่ไม่มีใครคาดคิดไว้พอดี
ซึ่งเป็นกลุ่มที่อยากรู้ที่สุด

```go
// ส่งเสมอ ไม่ว่า SENTRY_MIN_STATUS จะตั้งไว้เท่าไหร่
c.Log().Error("อ่าน response ไม่ได้", "err", errors.New("unexpected end of JSON input"))

// ไม่ส่ง — 400 คือคำตัดสินว่าเป็นความผิดฝั่ง client
c.Log().Error("ปฏิเสธคำขอ", "err", errmsgs.BadRequest)
```

`ctx.Log().Error(...)` ที่**ไม่มี error แนบมาเลย**ก็อยู่ในกลุ่มเดียวกัน — ไม่มี status
และโค้ดหยุดเขียน `Error()` เพราะมีเหตุผล จึงถูกส่งเป็น message event

ต่ำกว่าระดับ Error บรรทัดที่ไม่มี error ถือเป็นการบรรยายเฉยๆ ไม่ถูกส่ง

## เขียนโค้ดยังไง (สั้นๆ: เขียนเหมือนเดิม)

**ไม่ต้องมีคำว่า `Sentry` ในโค้ดแอป** — log กับ error ที่เขียนอยู่แล้วคือ input ของ Sentry

```go
func Charge(c core.IHTTPContext) error {
    log := c.Log().With("component", "billing")   // component → breadcrumb category

    log.Info("เริ่มตัดบัตร", "order_id", orderID)  // → breadcrumb พร้อม data

    if err := refreshRate(); err != nil {
        // จัดการเองแล้ว ไม่ได้ fail request — แต่ยังอยากรู้
        log.Error("ดึงเรตไม่ได้ ใช้เรตเก่า", "err", err)   // → event ถ้า err เป็น 5xx
    }

    if err := gateway.Charge(); err != nil {
        // fail + report ในบรรทัดเดียว: args ท้ายสุดกลายเป็น extras ใน Sentry
        return c.NewError(err, errmsgs.PaymentFailed, "order_id", orderID)
    }
    return c.JSON(200, result)
}
```

- error ตัวเดียว = **issue เดียว** ไม่ว่าจะถูก log ก่อนแล้ว return ทีหลัง หรือกลับกัน
  (event id ถูกผูกไว้กับตัว error เอง แล้ว layer ที่เหลือเห็นว่ามีแล้วก็ข้าม)
- `err` / `error` คือ key ที่ bridge มองหา — ซึ่งเป็นสิ่งที่โค้ดเขียนกันอยู่แล้ว

### `ctx.Sentry()` ไว้ใช้เมื่อไร

เหลือไว้เป็น escape hatch จริงๆ ไม่ใช่ API ที่ต้องเรียนรู้:

```go
c.Sentry().SetTag("tenant", tenantID)    // tag ที่ค้นหาได้ (ต้องเป็น low-cardinality)
c.Sentry().StartTransaction("import", "task")
c.Sentry().Hub()                          // ลง SDK ตรงๆ
```

`CaptureError` / `CaptureMessage` / `Breadcrumb` / `SetContextData` มีให้ครบ แต่
**ไม่ต้องใช้ในเส้นทางปกติ** เพราะซ้ำกับ `ctx.NewError` / `ctx.Log()` / `ctx.SetData()`
ที่ทำให้อัตโนมัติอยู่แล้ว

### CaptureOption

```go
c.Sentry().CaptureError(err,
    core.CaptureLevel(core.LevelFatal),
    core.CaptureTag("tenant", tenantID),
    core.CaptureTags(map[string]string{"region": "th"}),
    core.CaptureExtra("payload", payload),
    core.CaptureContext("order", map[string]any{"id": id, "total": total}),
    core.CaptureFingerprint("{{ default }}", "PAYMENT_FAILED"),
)
```

> `CaptureExtra` ยังใช้ชื่อเดิม แต่ค่าไปโผล่ใน context ชื่อ **`extra`** ของ event
> (sentry-go ถอดฟิลด์ `extra` ออกตั้งแต่ 0.46) — attr ของบรรทัด log ที่กลายเป็น
> event ก็ย้ายไปอยู่ใน context ชื่อ `log` ด้วยเหตุผลเดียวกัน

## การจัดกลุ่ม issue (grouping)

- **ชื่อ issue = error code** ไม่ใช่ `*core.Error` — issue list อ่านเป็นภาษาของ API เอง
  (`PAYMENT_FAILED: gateway said no`)
- **fingerprint default** = `["{{ default }}", code]` → ยังจัดกลุ่มตาม stack แต่แยกตาม code
- **job ที่พัง** = `["job", <job name>, <code>]` → job เดียวพัง = issue เดียว ไม่ว่าจะ fail กี่ run

## ข้อมูลลับถูก mask ให้เสมอ

v1 ส่ง config ทั้งก้อนเข้า Sentry (รวม DB password, JWT secret, S3 key)
v2 **scrub ก่อนส่งเสมอ** ที่ `BeforeSend` — จุดสุดท้ายก่อนออกจาก process
จึงไม่มีทางรั่วเพราะลืม scrub ที่ capture path ใหม่

mask ทั้ง key ที่ชื่อเข้าข่าย (`password`, `secret`, `token`, `authorization`,
`api_key`, `credential`, `cookie`, `session`, `jwt`, `otp`, `cvv`, …) ทั้งใน
config, scoped data, header, query string, JSON body, breadcrumb และ URL ขาออก

```go
core.NewSentry(env, core.SentryOptions{
    ScrubKeys:   []string{"citizen_id", "account_no"},  // เพิ่ม key ที่ต้อง mask
    ScrubValues: []string{internalAPIKey},              // mask ค่านี้ทุกที่ที่โผล่
})
```

ค่าที่เป็นความลับใน config (`DB_PASSWORD`, `JWT_SECRET`, `S3_SECRET_KEY`, …)
ถูกใส่ใน `ScrubValues` ให้อัตโนมัติ — ต่อให้มันไปโผล่ใน error message ของ driver
ก็ยังถูก mask

## Request body

body ถูกอ่าน **แบบ tee ตามที่ handler อ่านจริง** (ไม่ได้อ่านซ้ำ, ไม่ได้อ่านก่อน)
เก็บไม่เกิน 16 KiB, ข้าม multipart/binary, และ scrub ก่อนส่ง

```sh
APP_SENTRY_CAPTURE_BODY=false     # ปิด
APP_SENTRY_MAX_BODY_BYTES=65536   # ขยายเพดาน
```

## Jobs และ Cron monitors

job run = unit of work เหมือน request จึงได้ hub, breadcrumb และ transaction
ของตัวเอง ไม่ปนกับ run อื่นที่รันพร้อมกัน

```go
// retry ที่ fail ระหว่างทางไม่ถือเป็น incident — รายงานเมื่อ attempt หมดแล้ว
// เปิดให้รายงานทุก attempt ได้ด้วย:
APP_SENTRY_CAPTURE_RETRIES=true
```

**Sentry Crons** — เปิดแล้ว job ที่มี `Schedule` จะส่ง check-in
(`in_progress` → `ok`/`error`) ทุกครั้งที่ตารางเวลาสั่งให้รัน ทำให้ Sentry
เตือนได้เมื่อ **run ที่ควรเกิดแต่ไม่เกิด** (process ตาย, scheduler ไม่ทำงาน)

```sh
APP_SENTRY_ENABLE_CRONS=true
```

- monitor slug มาจากชื่อ job (`Nightly Report` → `nightly-report`)
- ตารางเวลาถูกส่งไปด้วย (cron expression หรือ interval) Sentry จึงรู้ว่า "สาย" คือเมื่อไร
- นับเฉพาะ run ที่ trigger จาก **schedule** เท่านั้น — trigger เองหรือ replay ไม่ถูกนับ
- ⚠️ เปิดแล้ว Sentry จะสร้าง monitor ในองค์กรอัตโนมัติ (มีโควตาแยก) จึง default = ปิด

## Tracing (performance)

```sh
APP_SENTRY_ENABLE_TRACING=true
APP_SENTRY_TRACES_SAMPLE_RATE=0.1     # 10% ของ request
```

เปิดแล้วจะได้:
- transaction ต่อ request (`GET /users/:id` — ใช้ route pattern ไม่ใช่ path จริง
  จึงไม่ระเบิดเป็นล้าน transaction)
- transaction ต่อ job run (`job nightly`)
- span ลูกของทุก HTTP call ขาออก พร้อมส่ง `sentry-trace` / `baggage` header ต่อ
  → service ปลายทางที่ใช้ Sentry เดียวกันจะอยู่ใน trace เดียวกัน
- `trace_id` ถูกแปะในทุกบรรทัด log ของ request นั้น → กระโดดจาก log ไป trace ไป issue ได้

> ⚠️ SDK **ไม่ส่ง transaction ของ request ที่ตอบ 404** ให้โดย default (ค่า
> `TraceIgnoreStatusCodes` ของ sentry-go ตั้งแต่ 0.37) — 404 จาก path ที่ไม่มีอยู่จริง
> เป็น noise ที่กิน quota มากที่สุดใน service ที่เปิดอินเทอร์เน็ต

สร้าง span เองได้:

```go
span := c.Sentry().StartTransaction("import ledger", "task")
defer span.Finish(err)

child := span.Child("db.query", "SELECT ledger")
child.Finish(nil)
```

## Logs (opt-in)

```sh
APP_SENTRY_ENABLE_LOGS=true
APP_SENTRY_LOG_LEVEL=info      # (ไม่บังคับ) ส่ง Sentry น้อยกว่าที่พิมพ์ออก stdout
```

เปิดแล้วทุกบรรทัดที่เขียนผ่าน `ctx.Log()` / `app.Log()` ถูกส่งเข้า **Sentry Logs**
ด้วย — ยังพิมพ์ออก stdout เหมือนเดิมทุกประการ ไม่ต้องแก้โค้ดสักบรรทัด

**ต่างจาก breadcrumb ตรงไหน** — breadcrumb มีชีวิตอยู่ได้ก็ต่อเมื่อมี event มาแนบ
service ที่ไม่เคยพังจึงไม่เหลือร่องรอยว่าทำอะไรไปบ้าง ส่วน log ที่ส่งเข้า Sentry Logs
ค้นหาได้ด้วยตัวเอง ไม่ต้องรอให้มี error

| | breadcrumb | Sentry Logs |
|---|---|---|
| เห็นได้เมื่อ | มี event เกิดขึ้น | เสมอ |
| ขอบเขต | 50 บรรทัดล่าสุดของ request นั้น | ทั้งหมด ตาม retention |
| ค้นหา | ในหน้า issue | ค้นข้าม service ได้ |
| โควตา | นับรวมกับ event | **แยกต่างหาก** |

สิ่งที่ติดไปกับแต่ละบรรทัด:

- **ชนิดข้อมูลจริง** — `"attempt", 2` เป็น integer, `"paid", true` เป็น boolean
  จึงกรอง/aggregate ได้ ไม่ใช่ string ทั้งหมด (`time.Duration` → `"150ms"`)
- `trace_id` / `span_id` / `request_id` (เมื่อเปิด tracing) → กระโดดจาก log ไป
  trace ไป issue ที่เกี่ยวข้องได้ในคลิกเดียว
- `error.code` เมื่อบรรทัดนั้นพก `IError` ไว้ใต้ `err` — filter ด้วย code ได้ตรงๆ
- user จาก `ctx.GetUser()` และ `release` / `environment` / `server_name`

```go
c.Log().Info("charging card", "order_id", "o-7", "attempt", 2)
c.Log().Error("charge failed", "err", err)   // Logs + breadcrumb + event (ถ้า 5xx)
```

> **scrub เหมือนกันทุกช่องทาง** — log ที่ส่งเข้า Sentry ผ่าน scrubber ชุดเดียวกับ
> event: key ที่เข้าข่ายอ่อนไหวถูกแทนด้วย `[redacted]` และ secret จาก config ที่โผล่
> กลางข้อความก็ถูก mask ปรับเพิ่มได้ที่ `SentryOptions.BeforeSendLog`

บรรทัดที่เขียนนอก request/run (ตอน boot, goroutine ของ worker เอง) ก็ถูกส่งเช่นกัน
— ขาดแค่การผูก trace

**กติกาข้อเดียวยังเหมือนเดิม**: Logs ไม่เกี่ยวกับการตัดสินว่าอะไรคือ incident
บรรทัดที่พก error 5xx ยังกลายเป็น event เหมือนเดิม การเปิด Logs แค่ทำให้ทุกบรรทัด
ค้นหาได้ ไม่ได้เปลี่ยนว่าอะไรจะเด้งเป็น issue

## Metrics (opt-in)

```sh
APP_SENTRY_ENABLE_METRICS=true
```

`ctx.Meter()` บันทึกตัวเลขที่อยากดูย้อนหลังเป็นกราฟ — ไปโผล่ข้าง trace และ log
ของ request เดียวกัน ตัวเลขที่พุ่งจึง**เปิดดูได้ว่าเกิดจาก request ไหน**

```go
c.Meter().Count("orders.paid", 1, core.MetricAttr("gateway", "scb"))
c.Meter().Gauge("queue.depth", float64(n))
c.Meter().Distribution("upload.size", float64(size), core.MetricUnit(core.UnitByte))
c.Meter().Duration("gateway.latency", time.Since(start))   // เป็น ms เสมอ
```

| เมธอด | ใช้เมื่อ |
|---|---|
| `Count` | นับจำนวนครั้ง — order ที่จ่ายสำเร็จ, retry, cache miss |
| `Gauge` | ค่าที่ขึ้นๆ ลงๆ — ความลึกของคิว, ขนาด connection pool |
| `Distribution` | ค่าที่สนใจการกระจาย — latency, ขนาด payload (Sentry เก็บ percentile ให้) |
| `Duration` | `Distribution` ของเวลา บันทึกเป็น **มิลลิวินาทีเสมอ** เพื่อให้ทุก timing เทียบกันได้ |

- นอก request/run ใช้ `app.Meter()` (ยังส่งได้ แค่ไม่ผูก trace)
- `MetricAttr` / `MetricAttrs` คือมิติที่ใช้ breakdown — **ระวังค่าที่ไม่จำกัด**
  (customer id = จำนวน series เท่าจำนวนลูกค้า)
- attr ผ่าน scrubber ชุดเดียวกับทุกช่องทาง และคงชนิดข้อมูล (int/float/bool)
- ปิดอยู่ = ทุก method เป็น no-op ไม่ต้อง `if` และเช็คได้ด้วย `ctx.Meter().Enabled()`

## Database (opt-in)

```go
db, _ := core.NewDatabase(env)
_ = core.InstrumentGorm(db, core.SentryGormOptions{
    SlowQuery:  200 * time.Millisecond,  // ช้ากว่านี้ = breadcrumb ระดับ warning
    AllQueries: false,                   // true = ทุก query (ระวัง breadcrumb เต็ม)
    ExcludeSQL: false,                   // true = ไม่ส่งตัว statement
})
app, _ := core.NewApp(env, core.WithSQL("default", db))
```

default บันทึกเฉพาะ query ที่ **ช้า** หรือ **fail** เพราะ breadcrumb มีโควตา 50 อัน
ต่อ event — ถ้าใส่ทุก `SELECT` บรรทัดที่อธิบายสาเหตุจริงจะถูกดันหายไป
(`ErrRecordNotFound` ไม่นับเป็น fail)

**และเมื่อเปิด tracing ทุก query จะกลายเป็น span ใน trace ของ request นั้นด้วย**
(`db.query` / `db.create` / `db.update` / `db.delete` / `db.raw`) — breadcrumb บอกว่า
query เกิดขึ้น แต่ span บอกว่า request หมดเวลาไปกับมันกี่มิลลิวินาที ถ้าไม่มี span
waterfall จะเห็นแค่ว่าเวลาหายไป แต่ไม่บอกว่าหายไปไหน

- description ของ span คือ statement จริง (bind variable ไม่เคยถูกแทนค่าลงไป
  ปิดด้วย `ExcludeSQL: true` ถ้า SQL เองเป็นความลับ)
- data: `db.table`, `db.rows_affected` และ `db.error` เมื่อ fail
- ไม่เปิด tracing = ไม่มี parent span = ไม่สร้างอะไรเลย (ค่าใช้จ่ายคือ context lookup เดียว)

## Event id ในหน้าเว็บ / support

- response ที่ error จะมี header `X-Sentry-Id`
- ฝั่ง Go อ่านได้จาก error โดยตรง:

```go
var e *core.Error
if errors.As(err, &e) {
    fmt.Println(e.EventID())   // "" ถ้าไม่ได้ส่ง
}
```

## เขียน test ว่า error ถูกรายงานจริง

```go
tracker, rec, _ := core.NewRecordingSentry(env)
app, _ := core.NewApp(env, core.WithSentry(tracker))

// ... ยิง request / รัน job ...

require.Equal(t, 1, rec.Len())
require.Equal(t, "PAYMENT_FAILED", rec.Codes()[0])
require.Equal(t, "u-1", rec.Last().User.ID)
require.True(t, rec.HasBreadcrumb("เริ่มตัดบัตร"))

// Sentry Logs (ถ้าเปิด) — SDK ส่งเป็น batch จึงต้อง flush ก่อน assert
app.Sentry().Flush(time.Second)
require.True(t, rec.HasLog("charging card"))
require.Equal(t, int64(2), rec.Logs()[0].Attributes["attempt"].AsInt64())
```

`NewRecordingSentry` รัน path จริงทั้งหมด (scope, scrub, fingerprint, breadcrumb,
log) เปลี่ยนแค่ transport — assertion จึงเชื่อถือได้

## Configuration

> คำอธิบายละเอียดของทุก key (พร้อมกติกา prefix `APP_` และไฟล์ `.env`)
> อยู่ที่ [Configuration (ENV)](./env.md#sentry) — ตารางข้างล่างเป็นสรุปย่อ

| Env | Default | ความหมาย |
|---|---|---|
| `SENTRY_DSN` | — | ว่าง = ปิดทั้งระบบ (no-op) |
| `SENTRY_ENVIRONMENT` | ค่า `ENV` | environment ใน Sentry |
| `SENTRY_RELEASE` | — | version/commit สำหรับ regression tracking |
| `SENTRY_SERVER_NAME` | hostname | ชื่อเครื่อง |
| `SENTRY_DEBUG` | `false` | log การทำงานของ SDK เอง |
| `SENTRY_SAMPLE_RATE` | `1.0` | สัดส่วน error ที่ส่ง |
| `SENTRY_TRACES_SAMPLE_RATE` | `0` | สัดส่วน transaction ที่ส่ง |
| `SENTRY_ENABLE_TRACING` | `false` | เปิด transaction (sample rate = 1.0 ถ้าไม่ได้ตั้ง) |
| `SENTRY_ENABLE_CRONS` | `false` | ส่ง check-in ของ job ที่มี schedule |
| `SENTRY_ENABLE_LOGS` | `false` | ส่งทุกบรรทัด log เข้า Sentry Logs |
| `SENTRY_LOG_LEVEL` | ตาม `LOG_LEVEL` | ระดับต่ำสุดที่ส่งเข้า Sentry Logs |
| `SENTRY_ENABLE_METRICS` | `false` | เปิด `ctx.Meter()` (counter / gauge / distribution) |
| `SENTRY_MIN_STATUS` | `500` | status ต่ำสุดที่ถือว่าเป็น incident |
| `SENTRY_CAPTURE_RETRIES` | `false` | รายงานทุก attempt ของ job ไม่ใช่เฉพาะครั้งสุดท้าย |
| `SENTRY_BREADCRUMB_LEVEL` | `info` | ระดับ log ต่ำสุดที่กลายเป็น breadcrumb (`off` = ปิด) |
| `SENTRY_MAX_BREADCRUMBS` | `50` | จำนวน breadcrumb ต่อ event |
| `SENTRY_ATTACH_STACKTRACE` | `true` | แนบ stack แม้กับ message |
| `SENTRY_SEND_DEFAULT_PII` | `false` | ส่ง IP / cookie ตามค่า default ของ SDK |
| `SENTRY_CAPTURE_BODY` | `true` | แนบ request body (scrub แล้ว) |
| `SENTRY_MAX_BODY_BYTES` | `16384` | เพดาน body |
| `SENTRY_SEND_ENV` | `true` | แนบ config (scrub แล้ว) |
| `SENTRY_IGNORE_ERRORS` | — | รายการ regex คั่นด้วย `,` |
| `SENTRY_IGNORE_TRANSACTIONS` | — | รายการ regex คั่นด้วย `,` |
| `SENTRY_FLUSH_TIMEOUT` | `5` | วินาทีที่รอตอน shutdown |

ทุกค่าตั้งผ่าน `core.SentryOptions` ได้ด้วย (มีลำดับสูงกว่า env):

```go
tracker, err := core.NewSentry(env, core.SentryOptions{
    Release:     buildVersion,
    ScrubKeys:   []string{"citizen_id"},
    BeforeSend: func(e *sentry.Event, hint *sentry.EventHint) *sentry.Event {
        if e.Tags["tenant"] == "loadtest" {
            return nil          // ทิ้ง event นี้
        }
        return e
    },
})
app, err := core.NewApp(env, core.WithSentry(tracker))
```

## Shutdown

`app.Shutdown(ctx)` flush event ที่ค้างอยู่ให้เป็นขั้นตอนสุดท้าย — error ที่เกิด
ระหว่างปิดระบบจึงยังส่งทัน ไม่ต้องเรียก `sentry.Flush` เอง

## เทียบกับ v1

| v1 | v2 |
|---|---|
| `sentry.ConfigureScope` (global scope) — request ที่รันพร้อมกันเขียนทับ scope กัน | hub ต่อ request/run แยกกันจริง |
| ส่ง `ENV().All()` ดิบๆ รวม password/secret | scrub ที่ `BeforeSend` เสมอ |
| ต้องเรียก `CaptureError(...)` เอง | error ≥ 500, panic, job fail, log ที่พก 5xx ส่งอัตโนมัติ |
| log กับ Sentry เป็นคนละระบบ ต้องเขียนสองรอบ | `ctx.Log()` = breadcrumb + event (ถ้าเป็น 5xx) + Sentry Logs |
| ส่ง log ทุกบรรทัดเข้า Sentry Logs ดิบๆ ปิดไม่ได้ทีละระดับ ไม่ scrub | opt-in, มีระดับต่ำสุดของตัวเอง, scrub และคง type ของ attr |
| error เดียวถูกรายงานซ้ำหลายชั้น | event id ผูกกับตัว error → issue เดียวเสมอ |
| ไม่มี tracing / cron monitor | transaction + check-in + **span ของทุก query** ในตัว |
| ไม่มี metric | `ctx.Meter()` — counter / gauge / distribution ผูกกับ trace เดียวกัน |
| issue ชื่อ `*core.Error` | issue ชื่อ error code |
| ทดสอบไม่ได้ | `NewRecordingSentry` |

## Best practices

- **ปล่อยให้ framework เป็นคนรายงาน** — return `IError` แล้วจบ การ capture เองเพิ่มทำให้
  หนึ่งเหตุการณ์กลายเป็นสองใบและ stack trace ชี้ผิดที่
- **4xx ไม่ใช่ incident** — ถ้าอยากให้ error ของ domain ขึ้น Sentry ให้มันเป็น 5xx
  ตามความจริง อย่าฝืน capture 400 ทุกใบ
- **ตั้ง `release` และ `environment` ให้ตรงกับที่ deploy จริง** — issue ที่บอกไม่ได้ว่า
  มาจากเวอร์ชันไหน แก้แล้วก็ไม่รู้ว่าหายจริงหรือเปล่า
- **เพิ่ม key ที่ต้อง mask ให้ครบตามโดเมนของเรา** (เลขบัตรประชาชน, เลขกรมธรรม์) —
  ค่าเริ่มต้นครอบคำทั่วไปไว้แล้ว แต่ไม่รู้จักคำเฉพาะของธุรกิจ
- **ดู event จริงหนึ่งใบก่อนขึ้น production** — เป็นวิธีเดียวที่จะรู้ว่ามีอะไรหลุดไปบ้าง
- **tag ไว้เท่าที่ค้นจริง** (`tenant`, `role`) และ **อย่าใส่ PII ลง tag** เพราะ tag ถูก
  index และค้นได้ทั้งองค์กร
- **sample rate ของ tracing ต้องต่ำใน production** — trace ทุก request คือค่าใช้จ่ายและ
  noise ที่ไม่มีใครอ่าน
- **cron monitor สำหรับงานที่ "ไม่ทำงาน" แล้วเงียบ** — job ที่ไม่เคยยิงไม่มี error ให้ alert

## ดูเพิ่ม

- [Error handling](./error-handling.md) — `IError`, `Wrap`, sentinel
- [Logger](./logger.md) — บรรทัด log ที่กลายเป็น breadcrumb
- [Jobs](./jobs.md) — job runner, retry, replay
