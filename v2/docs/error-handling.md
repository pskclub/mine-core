# Error Handling

mine-core v2 มี error แบบ structured ตัวเดียวที่ไหลผ่านทุก layer — `core.IError`
(interface) กับ `core.Error` (concrete) ชื่อและ method เหมือน v1 แต่ข้างในแก้บั๊ก:
ไม่ panic, รองรับ `errors.Is/As`, และเก็บ stack trace ให้อัตโนมัติ

> **ทุก module คืน `core.IError`** — cache, database, mongo, mq, storage, mailer,
> push, jwt, csv, validation, repository, requester, auth, scheduler ล้วนคืน
> `IError` (ไม่ใช่ `error` เปล่า) จึง `return err` จาก handler ได้ตรงๆ และเรียก
> `err.GetCode()` / `err.GetStatus()` ได้โดยไม่ต้อง type-assert
> (ยกเว้น callback ที่ผู้ใช้เขียนเอง เช่น `JobFunc`, `TokenVerifier`,
> `Remember`'s loader ที่ยังรับ `error` เพื่อความยืดหยุ่น)

## แนวคิด

- ทุก error ที่คืนออกจาก framework/handler เป็น `core.IError`
- มี `GetCode()` (machine-readable), `GetStatus()` (HTTP status), `GetMessage()` (ข้อความ)
- serialise เป็น JSON ด้วย `JSON()` → `{ "code": ..., "message": ..., "fields"?: ... }`

## สร้าง error

```go
import core "github.com/pskclub/mine-core/v2"

// ระบุ status + code + message เอง
err := core.New(http.StatusBadRequest, "BAD_REQUEST", "bad request")

// แบบมี format
err := core.Newf(http.StatusConflict, "CONFLICT", "user %d already exists", id)
```

## ห่อ error จากภายนอก (Wrap)

`Wrap` เปลี่ยน error ธรรมดาให้เป็น `*core.Error` (default 500) โดย **ไม่ panic** และ
เก็บ cause ไว้ให้ `errors.Is/As` ตามได้:

```go
row, dbErr := db.Query(...)
if dbErr != nil {
    return core.Wrap(dbErr, "loading user")     // 500 + cause = dbErr
}
```

ถ้า error ที่ห่ออยู่แล้วเป็น `*core.Error` (เช่น sentinel) → **คง status/code/fields เดิม**
แค่เติมข้อความ context เข้าไป:

```go
return core.Wrap(errmsgs.NotFound, "loading user")  // ยังเป็น 404 NOT_FOUND
```

## Sentinel ที่ใช้ซ้ำได้ (package `errmsgs`)

```go
import "github.com/pskclub/mine-core/v2/errmsgs"

return errmsgs.NotFound          // 404 NOT_FOUND
return errmsgs.BadRequest        // 400 BAD_REQUEST
return errmsgs.Unauthorized      // 401 UNAUTHORIZED
return errmsgs.DBError           // 500 DATABASE_ERROR
return errmsgs.NotFoundCustomError("user")   // 404 USER_NOT_FOUND
```

## เทียบ error ด้วย errors.Is / errors.As

Sentinel เทียบกันด้วย **code** จึง match ได้แม้ถูกห่อหลายชั้น:

```go
if errors.Is(err, errmsgs.NotFound) {
    // จัดการ 404
}

var e *core.Error
if errors.As(err, &e) {
    log.Println(e.GetStatus(), e.GetCode())
}
```

## ปรับแต่งแบบ chain (ไม่ mutate ตัวเดิม)

builder ทุกตัวคืน copy ใหม่ ปลอดภัยต่อการใช้ sentinel ร่วมกัน:

```go
return errmsgs.BadRequest.
    WithCode("INVALID_EMAIL").
    WithMessage("email is not valid").
    WithFields(map[string]any{"email": "REQUIRED"})
```

## แปลง panic เป็น error (Recover)

`core.Recover(&err)` ใน `defer` เปลี่ยน panic เป็น `*core.Error` พร้อม stack —
ต่างจาก v1 ที่ re-panic แล้วทิ้ง stack:

```go
func doWork() (err error) {
    defer core.Recover(&err)
    // ... code ที่อาจ panic ...
    return nil
}
```

## ดู stack trace

```go
var e *core.Error
if errors.As(err, &e) {
    fmt.Println(e.StackString())   // "func\n\tfile:line" ต่อ frame
}
```

stack มาจาก **จุดที่ error เกิด** เสมอ — คือบรรทัดที่เรียก `ctx.NewError(...)` /
`core.Wrap(...)` / `core.New(...)` ไม่ใช่จุดที่มัน capture และไม่ใช่จุดที่ประกาศ
sentinel

sentinel อย่าง `errmsgs.DBError` เป็น **ตัวบอกชนิดของ error ไม่ใช่ตำแหน่ง** — มันถูก
สร้างตอน package init จึงไม่เก็บ stack ของตัวเองไว้เลย (`e.StackTrace()` ว่าง)
ถ้าเก็บไว้ ทุก error ที่ใช้ sentinel ตัวเดียวกันจะรายงาน trace เดียวกันหมด
(`errmsgs.init` → `runtime.main`) และ Sentry จะจับรวมเป็น issue เดียวกัน:

```go
errmsgs.DBError.StackTrace()                    // ว่าง — เป็นแค่ template
ctx.NewError(dbErr, errmsgs.DBError)            // stack = บรรทัดนี้
core.Wrap(errmsgs.DBError, "loading user 42")   // stack = บรรทัดนี้
```

## ส่งเข้า Sentry อัตโนมัติ

error ที่ status ≥ 500 ถูกส่งเข้า Sentry ตั้งแต่ตอนที่ `ctx.NewError(...)` สร้างมันขึ้นมา
(จุดที่ยังมี context ครบที่สุด) พร้อม user / scoped data / breadcrumb / stack แล้วผูก
event id กลับเข้า error เพื่อไม่ให้ layer บนรายงานซ้ำ:

```go
err := c.NewError(dbErr, errmsgs.DBError)

var e *core.Error
errors.As(err, &e)
e.EventID()      // event id ใน Sentry ("" ถ้าไม่ได้ตั้ง DSN)
```

error ที่ **ไม่ได้ return** แต่ log ทิ้งไว้ก็ถูกส่งเหมือนกัน ถ้า status ≥ 500:

```go
ctx.Log().Error("ดึงเรตไม่ได้ ใช้เรตเก่า", "err", err)   // ส่งถ้า err เป็น 5xx
```

และ error ตัวเดียวถูกรายงาน **ครั้งเดียว** เสมอ ไม่ว่าจะ log ก่อนแล้ว `return` ทีหลัง
หรือกลับกัน (event id ผูกอยู่กับตัว error) — รายละเอียดทั้งหมดอยู่ที่ [Sentry](./sentry.md)

## ข้อความจริงตอน dev (`ENV=dev`)

เมื่อ `ENV=dev` ฟิลด์ `message` ใน response จะถูกแทนด้วยข้อความของ **root cause** —
ข้อความที่ driver/library รายงานจริง ไม่ใช่ป้ายที่ layer ระหว่างทางห่อไว้:

```jsonc
// ENV=dev
{ "code": "DATABASE_ERROR",
  "message": "ERROR: duplicate key value violates unique constraint \"users_email_key\"" }

// ENV อื่น (รวมถึงไม่ได้ตั้ง) — ใช้ message ของ error type เท่านั้น
{ "code": "DATABASE_ERROR", "message": "repository" }
```

- ใช้กับ error ที่ **มี cause** เท่านั้น — validation error กับ sentinel ที่ไม่มี cause
  ยังคงข้อความและ `fields` เดิมทุก environment
- panic ก็เข้ากติกาเดียวกัน: dev เห็นข้อความ panic, environment อื่นเห็น
  `Internal server error` ตามเดิม
- `code` ไม่เปลี่ยนตาม environment — client จึงยัง match ด้วย code ได้เหมือนกันทุกที่
- **ไม่ได้ตั้ง `ENV` = ไม่ใช่ dev** จึงไม่รั่วโดยบังเอิญ (แต่ควรตั้ง `ENV` เสมออยู่ดี)

อยากได้ root cause ในโค้ดเองใช้ `core.RootCause(err)`

## Error ของ service คุณเอง

`code` คือสิ่งที่ client เอาไปเขียน `if` — มันจึงควรถูกประกาศไว้ที่เดียว ไม่ใช่
พิมพ์ซ้ำที่ call site แบบแผนที่ใช้กันอยู่ (หนึ่ง package หนึ่งไฟล์ต่อ module,
validation code คู่กับข้อความของมัน) อยู่ที่ [Service Errors](./service-errors.md)

## JSON response shape

```go
b, _ := json.Marshal(err.(core.IError).JSON())
// { "code": "INVALID_PARAMS", "message": "Invalid parameters", "fields": {...} }
```

- `Status` ไม่ถูก serialise (ใช้ตอน set HTTP status code เท่านั้น)
- `fields` จะหายไปถ้าไม่มีค่า (`omitempty`) — ใช้กับ validation error (ดู validation docs)

## Best practices

- **ห้าม return error ดิบจากชั้นไหนก็ตาม** — `ctx.NewError(err, errmsgs.X)` คือสิ่งที่
  แนบ scope ของ request และตัดสินใจว่าจะรายงานหรือไม่
- **log หรือ return อย่างใดอย่างหนึ่ง** — logger มี bridge ไป Sentry อยู่แล้ว
- **status คือการตัดสินใจ ไม่ใช่การตกแต่ง**: 4xx = ผู้เรียกทำผิด (ไม่ขึ้น Sentry),
  5xx = เราทำผิด (ขึ้น) การใส่ 500 ให้ทุกอย่างทำให้ alert ไร้ความหมายภายในสัปดาห์เดียว
- **`Wrap` เพื่อเก็บ cause** แล้วเทียบด้วย `errors.Is/As` — อย่าเทียบข้อความ error
- **message ที่ตอบออกไปห้ามมีรายละเอียดภายใน** (ชื่อตาราง, SQL, path) — รายละเอียดอยู่ใน
  log ส่วน client ได้ `code` ที่ branch ได้
- **ประกาศ error ของ domain ไว้ที่เดียว** ใน `errmsgs` ของ service —
  [Service Errors](./service-errors.md)
- **ไม่ panic ในเส้นทาง error** — panic เก็บไว้สำหรับความผิดพลาดตอน boot ที่ไม่ควรรันต่อ
- **error ที่ผู้ใช้แก้ได้ ต้องบอกว่าให้แก้ยังไง** — "invalid input" ไม่ช่วยใครเลย
