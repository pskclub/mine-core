# Logger

`core.ILogger` — same name/verbs as v1, backed by the standard library's
`log/slog`. Structured logging with `msg, key, value, …` arguments.

## From a context (recommended)

`ctx.Log()` returns a logger that automatically tags each line with the request's
`request_id` (and trace attributes when present):

```go
ctx.Log().Info("user created", "user_id", id, "email", email)
ctx.Log().Warn("retrying", "attempt", n)
ctx.Log().Error("charge failed", "err", err, "order_id", orderID)
```

## Log กับ Sentry เป็นระบบเดียวกัน

เมื่อตั้ง `APP_SENTRY_DSN` แล้ว ทุกบรรทัดที่ log ผ่าน **context** จะ:

- กลายเป็น **breadcrumb** ของ request/run นั้น (category มาจาก attr `component` ถ้ามี)
- และถ้าบรรทัดนั้นพก error ที่ status ≥ 500 ไว้ใต้ key `err` / `error`
  → กลายเป็น **event** ด้วย
- และถ้าเปิด `APP_SENTRY_ENABLE_LOGS=true` → ถูกส่งเข้า **Sentry Logs** ทุกบรรทัด
  (ค้นหาได้เองโดยไม่ต้องมี error, ผูก trace เดียวกับ request นั้น)

```go
log := ctx.Log().With("component", "billing")
log.Info("เริ่มตัดบัตร", "order_id", id)          // breadcrumb (+ Sentry Logs)
log.Error("ดึงเรตไม่ได้", "err", err)             // breadcrumb + event (ถ้า err เป็น 5xx)
```

ทุกบรรทัดที่เขียนผ่าน context ยังพก **identity ของ unit of work** ไปเองด้วย —
`service`, `mode`, `request_id`, `http.route` / `job`, `run_id` — โดยที่ handler
ไม่ต้องส่งอะไรเพิ่ม (framework ปักไว้บน scope ของ request/run นั้นครั้งเดียว)

attr ที่ส่งไป Sentry Logs **คงชนิดข้อมูล** (int/float/bool เป็นตัวเลข/boolean จริง
ไม่ใช่ string) จึงกรองและ aggregate ได้ และผ่าน scrubber ชุดเดียวกับ event —
ค่าที่ key เข้าข่ายอ่อนไหว (`password`, `token`, …) ถูก mask ให้เสมอ

จึงไม่ต้องเขียน `ctx.Sentry().CaptureMessage(...)` หรือ `Breadcrumb(...)` ซ้ำ และ
error ตัวเดียวจะไม่ถูกรายงานสองครั้งแม้ log ก่อนแล้ว `return` ทีหลัง —
ดู [Sentry](./sentry.md)

## Log ทั้ง struct

ส่ง struct / map / slice เป็นค่าได้ตรงๆ:

```go
ctx.Log().Info("user loaded", "user", user)      // struct
ctx.Log().Error("charge failed", "err", err, "payload", req)
```

| ปลายทาง | ได้อะไร |
|---|---|
| JSON (default) | nested JSON จริง ใช้ `json:` tag ของ struct |
| text (`LOG_SIMPLE`) | JSON แบบบรรทัดเดียว — ไม่ใช่ `{u-1 a@b.co}` ของ Go ที่ทิ้งชื่อ field |
| Sentry Logs | attribute เป็น JSON string |
| Sentry event | context เป็น structured data (กดดูทีละ field ได้) |

> **ค่าที่ render ตัวเองได้จะไม่ถูกแตะ** — `error`, `fmt.Stringer`, `time.Time`
> ยังใช้รูปแบบของตัวเอง เพราะมันตั้งใจไว้แล้วว่าจะให้อ่านยังไง

⚠️ **field ที่อ่อนไหวข้างใน struct ถูก mask เฉพาะขาที่ออกไป Sentry** — scrubber
เดินเข้าไปใน JSON ของมัน ทำให้ `password`, `card_number`, `token` ที่ซ้อนอยู่
กลายเป็น `[redacted]` แต่ **stdout พิมพ์ครบทุก field ตามที่สั่ง** เพราะ log ของ
เครื่องตัวเองไม่ใช่การส่งข้อมูลออกนอก — ถ้า log pipeline ของคุณส่งต่อไปที่อื่น
ต้องกรองที่ชั้นนั้นเอง หรืออย่าใส่ struct ที่มีความลับลงไปตั้งแต่แรก

## ทุกบรรทัดบอกว่าเขียนมาจากไฟล์ไหน บรรทัดที่เท่าไหร่

ทุก log line พก `source` ของ **จุดที่เรียก log** ไปด้วยเสมอ (เปิดโดย default):

```json
{"time":"...","level":"ERROR","source":"services/payment.go:88","msg":"charge failed","err":"..."}
```

```
21:15:15.481 ERROR charge failed err=... services/payment.go:88
```

- เก็บ **ชื่อ directory สุดท้าย** ไว้ด้วย (`services/payment.go:88`) — ชื่อไฟล์เปล่าๆ
  ชนกันข้าม package ส่วน path เต็มเป็นของเครื่องที่ build ไม่ใช่ของ service
- เป็นจุดที่ **โค้ดคุณเรียก** ไม่ใช่ไฟล์ข้างในของ framework — รวมถึงตอน log ผ่าน
  `ctx.Log()`, `.With(...)` และ logger ของ job (ที่ tee เข้า run log)
- `ctx.Log().Slog()` ก็ได้ `source` เหมือนกัน (slog หาจุดเรียกเอง)

### บรรทัดที่ framework เขียนให้ ก็ชี้ที่โค้ดคุณ

log ที่ core เขียนแทนคุณ — SQL ที่ repository สั่ง, mongo command, HTTP call ขาออก,
LLM completion, และบรรทัด `request error` ตอน `ctx.NewError(...)` คืน 5xx — เคยชี้ที่
ไฟล์ของ core เอง (`v2@v2.8.6/database_logger.go:155` เหมือนกันหมดทุก query ทุก service)
ซึ่งไม่ตอบอะไรเลย ตอนนี้ทุกบรรทัดพวกนี้ชี้ที่ **บรรทัดในโค้ดคุณที่เป็นต้นเหตุ**:

```
21:15:15.481 DEBUG query sql=SELECT * FROM "users" ... modules/user/repository.go:64
21:15:15.502 ERROR GET api.corpusx.com/search 401 ... modules/juristic/service.go:118
21:15:15.503 ERROR request error code=INTERNAL_SERVER_ERROR ... modules/juristic/service.go:121
```

วิธีหา: เดินขึ้น stack ไปหาเฟรมแรกที่ **ไม่ใช่ machinery** — ไม่ใช่ mine-core, ไม่ใช่
standard library และไม่ใช่ module ที่ binary นี้ dependency อยู่ (อ่านจาก
`debug.ReadBuildInfo`) ที่เหลือคือโค้ดของ service เอง ใช้วิธีคัดออกแบบนี้แทนการนับ
จำนวนเฟรม เพราะความลึกของ stack ข้างใน gorm ไม่เท่ากันในแต่ละ statement และแทนการ
เขียนรายชื่อ package ที่ต้องข้าม เพราะรายการแบบนั้นต้องเพิ่มบรรทัดทุกครั้งที่ core ห่อ
driver ตัวใหม่ วันที่ลืมเพิ่มมันจะโทษ internal ของ driver ตัวนั้นแบบเงียบ ๆ

กรณีที่ยังชี้ที่ไฟล์ core ซึ่งถูกต้องแล้ว: log ที่เป็น **เรื่องของ framework เอง**
(`http server started`, `job run failed`, `mongo primary elected`, pool/topology
event) — ไม่มีโค้ดคุณอยู่บน stack ให้ชี้ และถ้าหาเฟรมของ service ไม่เจอจริง ๆ
จะ fallback ไปที่เฟรมของ core ตามเดิม ไม่เดามั่ว

**access line เป็นกรณีพิเศษ** — มันถูกเขียน *หลัง* handler return ไปแล้ว เฟรมของ
handler จึงหายไปจาก stack และ `source` ของมันคือ `http_server.go` เสมอ สิ่งที่ตอบ
คำถาม "request นี้ใครเสิร์ฟ" ได้คือ field `handler` ซึ่งเก็บชื่อไว้ตอน register route:

```
ERROR GET /juristics/corpusx/search 500 479ms INTERNAL_SERVER_ERROR
      status=500 ... handler=juristic.(*CorpusXController).Search  v2/http_server.go:420
```

เป็น **ชื่อ function ไม่ใช่ file:line** เพราะ route ส่วนใหญ่ register ด้วย method
value (`ctrl.Search`) ซึ่ง Go คอมไพล์เป็น wrapper ที่รายงานตำแหน่งเป็น
`<autogenerated>:1` — ไม่มีไฟล์ให้ชี้ แต่ชื่อยังอยู่ (core ตัด `-fm` กับ import path
ข้างหน้าออกให้)

ปิดได้ด้วย `APP_LOG_SOURCE=false` — จะไม่จับ program counter เลย เหมาะกับ service
ที่ log ถี่มากจนคิดเรื่อง `runtime.Callers` ต่อบรรทัด

> `source` เป็นคนละเรื่องกับ stack trace ของ error: บรรทัดนี้บอกว่า *ใครเขียน log*
> ส่วน stack ของ `core.Error` บอกว่า *error เกิดที่ไหน* (ดู [Error Handling](./error-handling.md))

## บรรทัดไหนควรมี

API หน้านี้ตอบว่า *log ยังไง* ส่วน *log อะไร* — level ไหนใช้ตอนไหน, อะไรที่
framework บันทึกให้แล้ว (เขียนซ้ำ = เหตุการณ์เดียวดูเหมือนสอง) และอะไรที่ห้ามเข้า
log เด็ดขาด — อยู่ที่ [Logging Practices](./logging-practices.md)

## Standalone

```go
log := core.NewLogger(env)          // JSON, level from LOG_LEVEL
log := core.NewLoggerSimple()       // plain text, info level (no config)
log := core.NewLoggerTo(w, env)     // write to any io.Writer (tests)
```

## Interface

```go
type ILogger interface {
    Debug(msg string, args ...any)
    Info(msg string, args ...any)
    Warn(msg string, args ...any)
    Error(msg string, args ...any)
    With(args ...any) ILogger    // child logger with pinned attributes
    Slog() *slog.Logger          // underlying slog for advanced use
}
```

## Configuration

| Env | Effect |
|---|---|
| `LOG_LEVEL` | `debug` / `info` / `warn` / `error` (default `info`) |
| `LOG_SIMPLE` | `true` → human-readable text handler; otherwise JSON |
| `LOG_SOURCE` | `false` → ไม่ต้องแนบไฟล์:บรรทัดที่เขียน log (default `true`) |

```go
// pin fields on a child logger
reqLog := ctx.Log().With("component", "billing")
reqLog.Info("started")
```

## Output

JSON handler (prod):
```json
{"time":"...","level":"INFO","source":"services/user.go:42","msg":"user created","user_id":"u1","request_id":"..."}
```

Text handler (`LOG_SIMPLE=true`, dev) — coloured when the destination is a
terminal:
```
21:15:15.481 INFO  user created user_id=u1 request_id=IBVyeNJ... services/user.go:42
```

The record's own fields come first, so the payload of a line starts at a
predictable column; correlation ids (`request_id`, `trace_id`, `job`) trail at
the end, where they are read only when tracing. Timestamps and keys are dimmed,
the level is coloured, and anything under `err` / `cause` / `panic` is red.

### Colour

| Signal | Result |
|---|---|
| `NO_COLOR` set (any value) | off — [no-color.org](https://no-color.org) |
| `LOG_COLOR` set | exactly that |
| otherwise | on when writing to a terminal |

Getting this wrong is worse than having no colour: escape codes in a redirected
file or a log pipeline turn every line into noise. **JSON output is never
coloured** — that mode exists for machines.

### SQL highlighting

At `LOG_LEVEL=debug` every statement is logged (see
[Database](./database-logging.md)). When colour is on, the `sql=` value is
syntax-highlighted so the shape of a query is visible without reading it word by
word:

- **keywords** — bold blue (`SELECT`, `FROM`, `WHERE`, `JOIN`, `ORDER`, …)
- **string literals** — green, including any SQL-looking text inside them
- **numbers** — cyan
- **identifiers** — left plain: they are the names you are scanning for, and
  colouring them competes with the keywords

Only escape codes are added. Strip them and the statement is character-for-character
what the database received, so a query can still be copied out of a log and run:

```
DEBUG query sql=SELECT count(*) FROM "users" WHERE LOWER(full_name) LIKE '%alice%' rows=1 duration_ms=11
```

A `stack` value is left alone — it is already structured, and colouring it would
only add noise.
