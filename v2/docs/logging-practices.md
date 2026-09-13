# Logging Practices

[Logger](./logger.md) อธิบาย API หน้านี้ตอบคำถามที่ยากกว่า: **บรรทัดไหนควรมี และ
บรรทัดไหนไม่ควร** — log ที่มีทุกอย่างกับ log ที่ไม่มีอะไรเลย มีค่าเท่ากันตอนตีสาม

## Logger มาจาก context เสมอ

```go
s.ctx.Log().Info("user created", "user_id", user.ID)
```

```json
{"level":"INFO","msg":"user created","request_id":"raBcQ…","user_id":"3830…"}
{"level":"INFO","msg":"POST /auth/register 201 41ms","status":201,"request_id":"raBcQ…"}
```

`request_id` คือสิ่งที่เชื่อมสองบรรทัดนี้เข้าด้วยกัน — service จึงไม่ต้องมี logger
ของตัวเอง ไม่ต้องเก็บไว้ใน struct และไม่ต้องรับเป็น parameter ใต้ HTTP มันพก
request id, ใต้ scheduler มันพกชื่อ job, run id และ attempt มาให้เอง

**key-value เสมอ ไม่ใช่ `fmt.Sprintf`** — output เป็น JSON, field ค้นและ aggregate
ได้ ประโยคทำไม่ได้:

```go
ctx.Log().Info("note created", "note_id", id, "owner_id", ownerID)   // ✅
ctx.Log().Info(fmt.Sprintf("created note %s for %s", id, ownerID))   // ❌
```

`ctx.Log().With(...)` มีไว้สำหรับกรณีที่หลายบรรทัดในฟังก์ชันเดียวใช้ field ร่วมกัน

## สิ่งที่ log ให้แล้ว — เขียนซ้ำ = เหตุการณ์เดียวดูเหมือนสอง

| บันทึกให้แล้ว | โดย |
|---|---|
| method, path, status, latency, request id, error code, `handler` ที่เสิร์ฟ | request middleware (level ตาม status) |
| ทุก 5xx พร้อม user และ scope ของ request | Sentry ผ่าน `ctx.NewError` |
| SQL ที่ fail และที่ช้า — ทุก statement เมื่อ `LOG_LEVEL=debug` | GORM logger |
| HTTP ขาออกที่ fail และที่ช้า — ทุก call เมื่อ `LOG_LEVEL=debug` | requester ([รายละเอียด](./requester.md)) |
| job เริ่ม, ล้มเหลว, retry | job runner |
| panic | recover middleware (fatal) |
| **ตอน boot: process นี้ต่ออะไรไว้ มี job อะไร ฟัง queue ไหน** | ดู [Boot log](#boot-log) ข้างล่าง |

ดังนั้น **handler ไม่ต้องเขียนอะไร store ไม่ต้องเขียนอะไร และ error ที่ `return`
แล้วห้าม log ซ้ำ**

```go
if err != nil {
    s.ctx.Log().Error("failed to create note", "err", err)   // ❌ ซ้ำ
    return nil, s.ctx.NewError(err, err)
}

return nil, s.ctx.NewError(err, err)                          // ✅ รายงานให้แล้ว
```

## What is worth a line

### Info — งานที่เปลี่ยนข้อมูล

`user created`, `note deleted`, `signed in` — และ **scheduled run ต้องรายงานทุกครั้ง
รวมทั้งครั้งที่นับได้ศูนย์**: job ที่พูดเฉพาะตอนมีอะไรให้ทำ แยกไม่ออกจาก job ที่
ตายไปแล้ว

```go
func Heartbeat(c core.ICronjobContext) error {
    // c.Log() พก job, run_id, attempt มาให้แล้ว — ใส่ซ้ำได้แค่พิมพ์สองรอบ
    c.Log().Info("heartbeat")

    return nil
}
```

```go
c.Log().Info("expired tokens swept", "deleted", n)   // n = 0 ก็ยัง log
```

### Warn — service ทำงานถูกต้องแต่ปฏิเสธคนเรียก

`sign-in failed` พร้อมเหตุผล, business rule ที่ปฏิเสธ request ที่ถูกต้องทุกอย่าง,
token ที่อ้างถึง account ที่ไม่มีแล้ว — **บรรทัดพวกนี้คือตัวที่ควรตั้ง alert ด้วย
อัตรา** (rate) ไม่ใช่ด้วยการมีอยู่

```go
if count >= maxNotesPerUser {
    // rule ปฏิเสธ request ที่ well-formed: Warn ไม่ใช่ Error เพราะระบบทำงานตามที่
    // ออกแบบไว้ทุกประการ — แต่บรรทัดนี้คือสิ่งที่เปลี่ยน "user บอกว่าเซฟไม่ได้"
    // ให้กลายเป็นคำตอบภายในการค้นครั้งเดียว
    s.ctx.Log().Warn("note rejected", "reason", "limit_reached", "owner_id", ownerID, "count", count)

    return nil, emsgs.NoteLimitReached
}
```

### Error — เฉพาะสิ่งที่ไม่มีใครอื่นรายงาน

ถ้า error ถูก `return` ออกไป แปลว่ามีคนรายงานแล้ว (framework + Sentry) `Error`
จึงเหลือไว้สำหรับความผิดพลาดเชิงโครงสร้างที่ไม่มีใครเห็น — ใน golang-template
มีอยู่ **สองที่**เท่านั้น: route ที่ลงทะเบียนโดยไม่มี middleware ของมัน และ
`RegisterAuth` ที่ไม่เคยถูกเรียก

### Debug — รายละเอียดสำหรับสิบนาทีที่มีคนกำลังไล่

list query กรองด้วยอะไรจริงๆ, token ถูกปฏิเสธด้วยเหตุผลไหนในสามข้อ — **อะไรที่
ทำงานทุก request อยู่ตรงนี้**

## ตัวอย่างที่ชัดที่สุด: sign-in

```go
user, err := s.users.FindByEmail(email)
if err != nil {
    s.ctx.Log().Warn("sign-in failed", "reason", "unknown_email", "email_domain", domainOf(email))

    return nil, emsgs.InvalidCredentials
}

if !utils.ComparePassword(user.Password, password) {
    s.ctx.Log().Warn("sign-in failed", "reason", "wrong_password", "user_id", user.ID)

    return nil, emsgs.InvalidCredentials
}
```

response บอกแค่ `INVALID_CREDENTIALS` เพราะการแยก "ไม่มี account นี้" ออกจาก
"รหัสผ่านผิด" คือการยื่นเครื่องมือไล่หาอีเมลที่สมัครไว้ให้คนเรียก — ส่วน **log
บันทึกว่ามันเป็นอันไหน** มันไม่เคยออกจาก service และคนที่กำลังดู sign-in ที่ล้มเหลว
เป็นพรวดต้องการข้อมูลนี้พอดี

นี่คือหลักการทั่วไป: *สิ่งที่บอกคนเรียกไม่ได้ ไม่ได้แปลว่าห้ามบันทึก*

## สิ่งที่ห้ามเข้า log เด็ดขาด

- **token และ digest ของมัน, password hash** และอะไรก็ตามที่เป็น credential —
  log store มีคนอ่านได้มากกว่า database เสมอ
- **ข้อมูลส่วนบุคคลใส่เป็น id ไม่ใช่ค่า**: `user_id` ไม่ใช่อีเมล, `note_id`
  ไม่ใช่เนื้อ note

```go
// เก็บ id ไม่ใช่สิ่งที่ user เขียน — สิ่งที่เขาเขียนเป็นของเขา
s.ctx.Log().Info("note created", "note_id", note.ID, "owner_id", ownerID)
```

> ⚠️ scrubber ของ Sentry mask field อ่อนไหวให้เฉพาะ**ขาที่ออกไป Sentry** —
> **stdout พิมพ์ครบทุก field ตามที่สั่ง** ถ้า log pipeline ส่งต่อไปที่อื่น ต้อง
> กรองที่ชั้นนั้นเอง หรืออย่าใส่ความลับลงไปตั้งแต่แรก ([Logger](./logger.md))

## ชื่อ field ให้เหมือนกันทั้ง service

field ที่สะกดไม่เหมือนกันคือ field ที่ query ข้าม service ไม่ได้:

| ใช้ | ไม่ใช่ |
|---|---|
| `user_id` | `userId`, `uid`, `user` |
| `err` | `error`, `e`, `msg` |
| `reason` | `why`, `cause` |
| `duration_ms` (ตัวเลข) | `"41ms"` (string) |

snake_case ให้เหมือนกับที่ framework ปักมาให้ (`request_id`, `trace_id`, `run_id`)
และค่าที่เป็นตัวเลขให้ส่งเป็นตัวเลขจริง — Sentry Logs คงชนิดข้อมูลไว้ จึงกรองด้วย
`duration_ms > 500` ได้ก็ต่อเมื่อมันไม่ใช่ string

## Boot log

ทุก capability ของ framework **degrade แทนที่จะไม่ยอม boot** — ไม่มี `CACHE_*`
ได้ cache ที่ miss ทุกครั้ง, ไม่มี `S3_*` ได้ storage ที่ error ทุกครั้ง เป็นการ
ออกแบบที่ตั้งใจ (service ไม่ควรสตาร์ทไม่ขึ้นเพราะของที่มันอาจไม่เคยแตะ) แต่แลกมา
ด้วยว่า **connection ที่หายไปจะมองไม่เห็นจนกว่าจะมี request แรกที่ต้องใช้มัน** และ
อาการตอนนั้น ("ไม่มีอะไรถูก cache เลย", "queue ไม่เคยได้อะไร") อยู่ห่างจากสาเหตุ
หลายชั้น ทั้งที่สาเหตุคือ config บรรทัดเดียว

boot log ตอบคำถามพวกนี้ตั้งแต่บรรทัดแรก:

```json
{"level":"INFO","msg":"app ready","env":"prod","service":"orders",
 "sql":["default","replica"],"mongo":[],"cache":["default"],
 "mq":true,"storage":false,"mailer":true,"pusher":false,"sentry":true}

{"level":"INFO","msg":"job runner started","workers":4,"queues":["default"]}
{"level":"INFO","msg":"scheduler started","scheduled":2,"timezone":"+07"}
{"level":"INFO","msg":"job registered","job":"heartbeat","queue":"default",
 "timeout":"5m0s","attempts":1,"schedule":"every(1m0s)",
 "next_run":"2026-07-30T13:55:40+07:00","in":"1m0s"}
{"level":"INFO","msg":"job registered","job":"nightly-report","queue":"default",
 "timeout":"10m0s","attempts":3,"schedule":"cron(0 2 * * *)",
 "next_run":"2026-07-31T02:00:00+07:00","in":"12h22m29s"}
{"level":"INFO","msg":"job registered","job":"reindex","queue":"default",
 "timeout":"5m0s","attempts":1,"trigger":"manual","max_concurrent":1,"on_conflict":"skip"}

{"level":"INFO","msg":"mq consumer started","queues":2,"prefetch":20,
 "concurrency":4,"handler_timeout":"30s"}
{"level":"INFO","msg":"queue consumed","queue":"orders.shipping","exchange":"orders",
 "exchange_kind":"topic","binding_keys":["order.created","order.paid"],
 "dead_letter":"orders.dlx"}

{"level":"INFO","msg":"pubsub subscriber started","channels":["user.updated"],
 "patterns":["order.*"],"concurrency":1,"handler_timeout":"30s",
 "cache":"default","prefix":"orders:"}

{"level":"INFO","msg":"http server started","addr":"[::]:8080","routes":37}
```

สิ่งที่แต่ละบรรทัดตอบ:

| บรรทัด | ตอบคำถาม |
|---|---|
| `app ready` | process นี้ต่ออะไรไว้บ้าง — และ**ไม่ได้**ต่ออะไร |
| `job registered` | worker นี้รันอะไรได้บ้าง พร้อม timeout/retry/concurrency ที่**มีผลจริง** และ**ครั้งต่อไปเมื่อไหร่** |
| `queue consumed` | ฟัง queue ไหน ผูกกับ exchange ไหน ด้วย key อะไร |
| `pubsub subscriber started` | subscribe channel ไหน ภายใต้ prefix อะไร |
| `http server started` | bind ที่ไหน มีกี่ route |

### จุดที่ควรรู้

**`timeout` กับ `attempts` เป็นค่าที่มีผลจริง ไม่ใช่ค่าใน struct** — job ที่ไม่ตั้ง
`Timeout` จะ log `5m0s` (`DefaultJobTimeout`) ไม่ใช่ `0s` เพราะ `0s` อธิบาย struct
ไม่ได้อธิบายพฤติกรรม

**`next_run` กับ `in`** — cron expression ไม่ใช่สิ่งที่คนอ่านแล้วแปลงเป็นเวลาได้ในหัว
และคำถามแรกเวลา job เงียบคือ "มันไม่ทำงาน หรือยังไม่ถึงเวลา" gocron รู้คำตอบตั้งแต่
วินาทีที่มันเริ่ม เลยพิมพ์ไว้เลย

**list ทุกอันเรียงแล้ว** — `channels`, `queues`, ชื่อ connection ทั้งหมดมาจาก map
ซึ่งลำดับไม่แน่นอน boot log ที่สลับลำดับทุกครั้งที่ restart เอาไป diff ไม่ได้

**`job runner has no jobs registered` เป็น warning** — worker ที่ registry ว่างเปล่า
สตาร์ทได้ปกติแล้วไม่ทำอะไรเลยตลอดไป

**route มีแค่จำนวน ไม่มีตาราง** — routing table หาได้จากโค้ดและจาก
[Postman collection](./postman.md) อยู่แล้ว ส่วนการพิมพ์มันออกมาดันบรรทัดอื่นของ
boot log หายหมด โดยเฉพาะใน dev ที่ `LOG_LEVEL=debug` คือค่าที่คนรันจริง

### ได้มาเมื่อไหร่

`app ready` ออกโดย `StartHTTPServer` และ `core.Runner` — ไม่ใช่ `NewApp` เพราะการ
สร้าง container ไม่ใช่เหตุการณ์เดียวกับการสตาร์ท process (และทุก test สร้าง App)
ถ้าเขียน lifecycle เอง เรียก `app.LogCapabilities()` ได้ตรงๆ

ที่เหลือออกจากตัวมันเองตอน `Start()` จึงได้เหมือนกันไม่ว่าจะรันผ่าน
[`Runner`](./runner.md) หรือเรียกเอง

## อ่านต่อ

- [Logger](./logger.md) — API, handler, สี, การ log ทั้ง struct
- [Sentry](./sentry.md) — log กลายเป็น breadcrumb/event ยังไง
- [Service errors](./service-errors.md) — error ที่ return แล้วรายงานตัวเอง
