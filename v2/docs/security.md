# Security Checklist

หน้านี้รวมสิ่งที่ต้องตัดสินใจเรื่องความปลอดภัยของ service หนึ่งตัว — อะไรที่ framework
ทำให้แล้ว อะไรที่ต้องตั้งเอง และอะไรที่มักถูกลืมจนกลายเป็นเรื่อง

[Authentication](./auth.md) กับ [JWT](./jwt.md) อธิบายกลไก หน้านี้คือ checklist

## ที่ framework ทำให้แล้ว

| | รายละเอียด |
|---|---|
| **มาสก์ความลับก่อนส่ง Sentry** | key ที่มีคำว่า `password`, `secret`, `token`, `authorization`, `api_key`, `private`, `credential`, `dsn`, `cookie`, `session`, `signature`, `otp`, `pin`, `cvv`, `card_number`, `jwt`, `refresh`, `salt`, `seed`, `mnemonic` ถูกแทนด้วย `[redacted]` |
| **จำกัดขนาด body** | 10 MB โดย default → `413 REQUEST_TOO_LARGE` แทนที่จะอ่านเข้า memory จนหมดเครื่อง |
| **ปิดช่อง slow-loris** | header ต้องส่งครบใน 20 วินาที, idle connection ถูกปิดใน 120 วินาที |
| **error ไม่รั่วรายละเอียดภายใน** | `IError` ที่ตอบออกไปมีแค่ `code`/`message`/`fields` ส่วน cause จริงอยู่ใน log |
| **ไม่ panic ใน error path** | ทางที่ควรปลอดภัยที่สุดไม่ใช่ทางที่ทำให้ process ตาย |
| **query ผูก context** | request ที่ถูกตัดสาย ยกเลิก query ที่ค้างด้วย ไม่กลายเป็นภาระค้างที่ database |

v1 ส่ง configuration ทั้งก้อนขึ้น Sentry ตรงๆ — รวม password ของ database, JWT secret
และ S3 key การมาสก์ใน v2 จึงเป็น default ที่**เพิ่มได้ แต่ถอดออกเงียบๆ ไม่ได้**

## ที่ต้องตั้งเอง

### 1. Authentication บนทุก route ที่ไม่ใช่ public

```go
auth := core.NewJWTAuth(env.Config().JWTSecret)

api := e.Group("/api", auth.Middleware())
api.GET("/me", h.Me)

e.POST("/login", h.Login)          // public โดยตั้งใจ
```

ตรวจด้วยตาว่า route ไหน public บ้าง — [middleware เขียนบรรทัดต่อ route](./middleware.md#เขียน-middleware-บรรทัดต่อ-route)
มีไว้เพื่อให้อ่านออกจากไฟล์ route โดยไม่ต้องไล่ว่า group ไหนครอบอะไร

`ChainVerifiers` ใช้เมื่อมีหลายชนิด token (JWT ของผู้ใช้ + API key ของ service) และ
`HashedTokenVerifier` เมื่อ token ถูกเก็บเป็น hash ใน database — ซึ่ง**ควรเป็นแบบนั้น**
เสมอสำหรับ API key: token ที่เก็บเป็น plaintext คือ database dump ที่กลายเป็นสิทธิ์เข้าถึง

### 2. Authorization ไม่ใช่แค่ authentication

authentication ตอบว่า "คุณคือใคร" — สิ่งที่ทำให้ระบบพังคือการไม่ตอบว่า "คุณแตะอันนี้ได้ไหม"

```go
// ❌ ใครที่ login แล้วก็ดู order ของคนอื่นได้
order, err := repository.New[Order](c).FindOne("id = ?", c.Param("id"))

// ✅ ขอบเขตอยู่ใน query ไม่ใช่ใน if ที่ลืมได้
order, err := repository.New[Order](c).
    Where("id = ? AND user_id = ?", c.Param("id"), c.GetUser().ID).
    FindOne()
```

ใส่เงื่อนไขความเป็นเจ้าของ **ใน query** ไม่ใช่หลังจากอ่านมาแล้ว — วิธีหลังคือที่มาของ
IDOR ที่พบบ่อยที่สุด และมันไม่มีอาการอะไรให้เห็นจน pentest เจอ

### 3. ความลับอยู่ใน ENV ไม่ใช่ใน repo

```
APP_JWT_SECRET=…
APP_DB_PASSWORD=…
APP_STORAGE_SECRET_KEY=…
```

- `.env` อยู่ใน `.gitignore` แล้ว — `test.env` ที่ commit ได้ ต้องมีแต่ค่าปลอม
- secret ที่หลุดเข้า git แล้ว **ต้องหมุน ไม่ใช่ลบ commit** — มันอยู่ใน history ของทุกคน
  ที่เคย clone ไปแล้ว
- ค่าที่ไม่อยากให้โผล่ใน Sentry แม้จะไม่ตรงกับ key ที่มาสก์ไว้ ใส่เพิ่มได้ที่
  [Sentry scrubbing](./sentry.md#ข้อมูลลับถูก-mask-ให้เสมอ)

### 4. Rate limit ที่ทางเข้าที่แพง

login, OTP, ส่งเมล, endpoint ที่เรียก AI — ทุกอันที่ราคาสูงต่อครั้งควรมีเพดาน

```go
n, err := ctx.Cache().Incr("login:"+ip, 1, time.Minute)
if err == nil && n > 10 {
    return core.New(http.StatusTooManyRequests, "TOO_MANY_REQUESTS", "ลองใหม่อีกครั้งในหนึ่งนาที")
}
```

หนึ่ง `Incr` พร้อม TTL = fixed-window rate limiter ที่ atomic —
[Counters & Rate Limits](./cache-counters.md)

⚠️ cache ที่ไม่ได้ตั้งค่าไว้ **degrade เงียบ** ตามดีไซน์ แปลว่า rate limit ที่พึ่ง cache
จะไม่ทำงานเลยใน environment ที่ไม่มี redis — สิ่งที่ต้องกันจริงๆ (จำนวนครั้งที่กรอก OTP
ผิด) ต้องนับใน database

### 5. Validation คือด่านแรก ไม่ใช่ด่านเดียว

```go
v.Str("email", r.Email).Required().Email().Max(255)
v.Int("limit", r.Limit).Between(1, 100)
```

[valid](./validation.md) กันข้อมูลผิดรูปได้ แต่ไม่ได้กันข้อมูลที่ *ถูกรูปแต่ไม่ควรทำ* —
`limit` ที่ไม่จำกัดเพดานคือ query ที่อ่านทั้งตาราง, `order_by` ที่รับอะไรก็ได้คือ SQL
injection ผ่านทางที่ไม่มีใครนึกถึง ใช้ `GetPageOptionsWithAllowed` เพื่อจำกัดคอลัมน์ที่
เรียงได้:

```go
c.GetPageOptionsWithAllowed("id", "created_at")   // นอกรายการนี้ถูกปฏิเสธ
```

### 6. Raw SQL ต้องผูกค่าเสมอ

repository และ GORM ผูกค่าให้อยู่แล้ว สิ่งที่อันตรายคือการต่อ string เอง:

```go
// ❌
db.Raw("SELECT * FROM users WHERE email = '" + email + "'")

// ✅
db.Raw("SELECT * FROM users WHERE email = ?", email)
```

ชื่อตารางและชื่อคอลัมน์ผูกเป็น parameter ไม่ได้ — ถ้ามันมาจาก input ต้องเทียบกับ
allow-list ที่เขียนไว้ในโค้ด

### 7. ไฟล์และลิงก์ที่มีอายุ

```go
url, err := ctx.Storage().PresignGet("private/report.pdf", 15*time.Minute)
```

- **ตั้ง TTL ให้สั้นที่สุดเท่าที่ใช้งานได้** — presigned URL คือสิทธิ์เข้าถึงที่ส่งต่อกันได้
  ในแชท ในอีเมล ใน log ของ proxy
- bucket ที่เก็บของส่วนตัว **ต้องไม่ public** และเข้าถึงผ่าน presign เท่านั้น
- ตรวจ content type และขนาดของไฟล์ที่รับเข้ามา ไม่ใช่เชื่อชื่อไฟล์ที่ client ส่งมา

ดู [Links (Presigned & Public)](./storage-presign.md)

### 8. อย่าใส่ความลับลง payload ของ message

message ใน RabbitMQ ค้างอยู่ในคิวได้เป็นวัน อ่านได้จาก management UI และมักถูก log
ตอน dead-letter ส่ง **id** แล้วให้ consumer ไปอ่านของจริงเอง —
[Publishing](./mq-publishing.md#ใส่อะไรลงใน-payload)

หลักเดียวกันใช้กับ [pub/sub](./pubsub.md) และกับ parameter ของ [job](./jobs-defining.md#parameters)
ซึ่งถูกเก็บลงตาราง `job_runs` เป็น JSON

### 9. Jobs admin API

หน้า admin ของ job คือ endpoint ที่ **รันโค้ดฝั่ง server ตามชื่อที่ผู้ใช้ส่งมา** ปฏิบัติกับ
มันเหมือนของที่อันตรายที่สุดใน service:

- อยู่หลัง authentication จริง และหลัง role ที่แคบกว่าผู้ใช้ทั่วไป
- อย่า mount บน path สาธารณะ แม้จะ "เดาไม่ถูก" ก็ตาม
- job ที่ replay แล้วอันตราย ปิดด้วย `Replayable: core.BoolPtr(false)`
- บันทึกว่าใครสั่ง (`TriggerOptions.By`) ทุกครั้ง — [Operating Runs](./jobs-operations.md#admin-api)

### 10. CORS ให้แคบเท่าที่ใช้

```go
core.NewHTTPServer(app, &core.HTTPOptions{
    AllowOrigins: []string{"https://app.example.com"},
})
```

default คือ `*` ซึ่งเหมาะกับ dev และไม่เหมาะกับ production ที่ API ใช้ cookie — ตั้งให้
เป็นรายการ origin จริงตั้งแต่ก่อนขึ้น

## Checklist ก่อนขึ้น production

- [ ] ทุก route ที่ไม่ใช่ public อยู่หลัง auth middleware และตรวจด้วยตาแล้ว
- [ ] query ที่อ่าน/แก้ข้อมูลของผู้ใช้ มีเงื่อนไขความเป็นเจ้าของอยู่ **ใน** query
- [ ] `APP_JWT_SECRET` ไม่ใช่ค่า default และไม่ได้อยู่ใน git
- [ ] API key ที่เก็บใน database เก็บเป็น hash
- [ ] endpoint ที่แพง (login, OTP, AI, ส่งเมล) มี rate limit
- [ ] `order_by` / `sort` จำกัดด้วย allow-list
- [ ] route ที่รับอัปโหลดตั้ง `BodyLimit` ของตัวเอง และตรวจ content type
- [ ] presigned URL มี TTL สั้น และ bucket ส่วนตัวไม่ public
- [ ] payload ของ message และ parameter ของ job ไม่มีความลับดิบ
- [ ] admin API ของ job อยู่หลัง role ที่แคบ
- [ ] `AllowOrigins` เป็นรายการ origin จริง ไม่ใช่ `*`
- [ ] `APP_SENTRY_DSN` ตั้งแล้ว และดู event จริงหนึ่งใบเพื่อยืนยันว่าไม่มีความลับหลุด
- [ ] dependency scan / `go vet` อยู่ใน CI

## เมื่อมีอะไรหลุด

1. **หมุน secret ก่อน** — ก่อนสืบสวน ก่อนแก้โค้ด token ที่หลุดคือ token ที่ใช้ได้อยู่
2. ดูขอบเขตจาก log: `request_id` ร้อยทุกอย่างของ request เดียวกันเข้าด้วยกัน และ
   [Sentry](./sentry.md) เก็บ breadcrumb ของสิ่งที่เกิดก่อนหน้าไว้ให้
3. แก้ที่ต้นเหตุแล้วค่อย replay งานที่ค้าง — [DLQ](./mq-patterns.md#dead-letter-queue-ที่ใช้งานได้จริง)
   กับ [replay ของ job](./jobs-operations.md#retry-replay-idempotency) มีไว้สำหรับขั้นนี้
