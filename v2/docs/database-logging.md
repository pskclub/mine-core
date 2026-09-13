# Query logging

SQL ไปอยู่ใน log stream เดียวกับทุกอย่าง (format เดียว level เดียว) และพก
`request_id` / trace ของ request ที่สั่ง query นั้นไปด้วย — บรรทัด query จึงตามกลับไปหา
request ที่เป็นต้นเหตุได้เสมอ

```json
{"level":"DEBUG","msg":"query","sql":"SELECT * FROM users WHERE status = $1",
 "rows":12,"duration_ms":3,"request_id":"01HX…"}
{"level":"WARN","msg":"slow query","sql":"SELECT …","rows":4,
 "duration_ms":412,"threshold_ms":200,"request_id":"01HX…"}
{"level":"ERROR","msg":"query failed","sql":"INSERT …","err":"duplicate key value…"}
```

## คุมปริมาณด้วย env

| ตั้ง | ได้อะไร |
|---|---|
| *(ไม่ตั้ง)* | ตาม `LOG_LEVEL` — `debug` = ทุก statement, นอกนั้น = เฉพาะ slow query กับ query ที่ fail |
| `DB_LOG_LEVEL=info` | ทุก statement โดยไม่ต้องเปิด `LOG_LEVEL=debug` ทั้งระบบ |
| `DB_LOG_LEVEL=warn` | เฉพาะ slow query กับ fail — **ใช้คู่กับ `LOG_LEVEL=debug` เมื่ออยาก debug flow แต่ไม่อยากจมกับ SELECT** |
| `DB_LOG_LEVEL=silent` | **ปิดสนิท** ไม่เหลือแม้แต่ slow query หรือ query ที่ fail (`off` / `false` ก็ได้) |

เหตุผลที่ต้องแยก `DB_LOG_LEVEL` ออกจาก `LOG_LEVEL`: การเปิด debug เพื่ออ่าน flow เดียว
ไม่ควรทำให้ทุก query ของ driver ท่วมจนหา flow นั้นไม่เจอ — เหมือนที่ MongoDB มี
[`DB_MONGO_LOG_LEVEL`](./mongo-logging.md) และ requester มี `HTTP_LOG_LEVEL`

## ปรับต่อ connection ใน Go

ค่าที่ตั้งใน Go **ชนะ env** เสมอ — ใช้กับ connection ที่ไม่ควรพูดเท่ากับตัวหลัก
(replica ที่ยิง query เยอะ, connection ของ job runner):

```go
db, _ := core.NewDatabase(env,
    core.WithDBLogLevel(gormlogger.Silent),   // ปิดเฉพาะ connection นี้
    core.WithSlowQuery(500*time.Millisecond), // เส้นแบ่ง slow query (default 200ms)
    core.WithDBLogger(myLogger),              // ส่งไป logger อื่น
)
```

| Option | Default | ใช้เมื่อ |
|---|---|---|
| `WithDBLogLevel` | ตาม `LOG_LEVEL` / `DB_LOG_LEVEL` | อยากให้ connection นี้เงียบ (หรือดังกว่า) ตัวอื่น |
| `WithSlowQuery` | `200ms` | workload ที่ 200ms เป็นเรื่องปกติ (report, batch) ไม่งั้น log จะเต็มไปด้วย "slow" ที่ไม่ได้ช้า |
| `WithDBLogger` | logger จาก config | แยก SQL ออกไปอีก stream หนึ่ง |

## `ErrRecordNotFound` ไม่นับเป็น fail

"ไม่เจอ" คือคำตอบ ไม่ใช่ error — ผู้เรียกเป็นคนตัดสินเองว่ามันคือ 404 หรือเรื่องปกติ
ถ้ามันถูก log เป็น error ทุกครั้ง log ของ endpoint ที่ "เช็คก่อนว่ามีไหม" จะเต็มไปด้วย
error ปลอมจนของจริงถูกกลบ

## ใช้ยังไงตอน debug

```sh
# อยากเห็น SQL ทั้งหมดของ service นี้ โดยที่ log อย่างอื่นยังเท่าเดิม
APP_DB_LOG_LEVEL=info

# อยากอ่าน flow ของ handler แต่ไม่อยากเห็น SELECT ของ job ที่รันอยู่เบื้องหลัง
APP_LOG_LEVEL=debug APP_DB_LOG_LEVEL=warn

# production ที่ log storage แพง: เหลือแค่ที่ผิดปกติจริงๆ
APP_DB_LOG_LEVEL=warn
```

⚠️ `DB_LOG_LEVEL=info` จะพา **ค่า parameter** ไปอยู่ใน log ด้วย — รวมถึง email,
เลขบัตร, token ที่อยู่ใน `WHERE` อย่าเปิดค้างไว้ใน production ที่ log ถูกเก็บยาว

## ต่อไปกว่านั้น: trace และ metric

Log บอกว่า query ไหนช้า แต่ไม่ได้บอกว่ามันช้า *ตรงไหนของ request* — อันนั้นคือ span:

```go
_ = core.InstrumentGorm(db)   // query กลายเป็น span + breadcrumb ใน Sentry
```

ดู [Sentry](./sentry.md) สำหรับสิ่งที่มันบันทึกและวิธีปรับ
