# Troubleshooting

อาการที่เจอบ่อย เรียงตามระบบ พร้อมที่ที่ควรไปดูเป็นอันดับแรก

## ก่อนอย่างอื่น: อ่าน boot log

บรรทัดแรกๆ ตอน start ตอบคำถามได้ครึ่งหนึ่งของหน้านี้ไปแล้ว:

```json
{"msg":"app ready","env":"prod","service":"orders","sql":["default"],"mongo":[],
 "cache":["default"],"mq":true,"storage":false,"mailer":false,"sentry":true}
{"msg":"job runner started","workers":4,"queues":["default"]}
{"msg":"job registered","job":"nightly-report","queue":"heavy","schedule":"cron(0 2 * * *)"}
{"msg":"queue consumed","queue":"orders.shipping","exchange":"orders","binding_keys":["order.*"]}
{"msg":"http server started","addr":"[::]:8080","routes":37}
```

`"storage":false` ทั้งที่โค้ดอัปโหลดไฟล์, `queue: "heavy"` ที่ไม่มีอยู่ใน `queues` ของ
runner, `binding_keys` ที่ไม่ตรงกับที่ publisher ส่ง — ทั้งสามอย่างมองเห็นได้จากตรงนี้
ก่อนจะเริ่มไล่โค้ด

## Config

**ตั้ง `APP_XXX` แล้วแต่ค่าไม่เข้า**

- key ต้องมีอยู่ใน `ENVConfig` — v2 bind ตาม field ไม่ได้อ่าน env มั่วๆ ([env.md](./env.md))
- ชื่อ env ต้องมี prefix `APP_` และเป็นตัวพิมพ์ใหญ่: field `mq_host` ← `APP_MQ_HOST`
- ไฟล์ `.env` ถูกอ่านเฉพาะตอนที่มันอยู่ใน working directory ของ process ไม่ใช่ข้างๆ binary

**ค่าที่ตั้งไว้ใน `.env` ถูก env ของระบบทับ** — นั่นคือพฤติกรรมที่ถูก: ของจริงใน
production มาจาก orchestrator ไม่ใช่จากไฟล์ใน image

## HTTP

**404 ทั้งที่เขียน route ไว้แล้ว**

- route ถูก register หลัง server start? — ต้องอยู่ก่อน `Serve`
- path มี prefix ของ group ที่ลืมนับหรือเปล่า (`e.Group("/api")` + `/users` = `/api/users`)
- `routes=37` ใน boot log บอกจำนวนที่ register จริง เทียบกับที่คาดไว้ได้

**handler ได้ `IHTTPContext` ไม่ตรงกับที่คิด / capability เป็น nil**

route ต้องลงทะเบียนผ่าน `e.GET/POST/...` ของ `core.Server` (ซึ่งห่อด้วย `WithHTTPContext`)
ไม่ใช่ผ่าน `e.Echo.GET(...)` ตรงๆ — อันหลังได้ context ของ echo ล้วนๆ ที่ไม่มี App ติดมา

**request ใหญ่ๆ ได้ 413**

body limit default คือ 10 MB — route ที่รับไฟล์ตั้งของตัวเอง:

```go
files := e.Group("/files", core.BodyLimit(100<<20))
```

**upload ยาวๆ ถูกตัดกลางคัน**

`ReadTimeout` default 5 นาที ตั้งเพิ่มที่ `HTTPOptions.ReadTimeout` ถ้าจำเป็น — แต่ถ้า
เป็น SSE หรือ long-poll ที่ถูกตัด ให้ดู `WriteTimeout` ที่ตั้งเองไว้ก่อน (default ไม่มี)

**CORS ไม่ผ่านทั้งที่ตั้ง `AllowOrigins` แล้ว**

preflight ต้องผ่าน method + header ด้วย ไม่ใช่แค่ origin — ตั้ง `AllowHeaders` ให้ครบ
สิ่งที่ frontend ส่งจริง (`Authorization`, `Content-Type`, header ของตัวเอง)

## Database

**`ctx.DB()` เป็น nil**

`ctx.DB()` เป็น capability เดียวที่คืน `nil` ได้ — แปลว่าไม่ได้ตั้ง `DB_*` หรือไม่ได้ส่ง
`core.WithSQL("default", db)` เข้า `NewApp` ดู `"sql":[]` ใน boot log เป็นการยืนยัน

**`FindOne` คืน error ทั้งที่ข้อมูลมีอยู่**

`FindOne` คืน `NOT_FOUND` เมื่อไม่เจอ — เช็คด้วย `errmsgs.IsNotFoundError(err)` ไม่ใช่
เทียบ error ตรงๆ และอย่าลืมว่า soft-delete ทำให้แถวที่ถูกลบไม่ถูกนับ (`Unscoped()` ถ้า
ต้องการ)

**query ช้าลงเรื่อยๆ / connection ตัน**

- เปิด [query logging](./database-logging.md) ดูว่ามี query ไหนช้าหรือถูกยิงซ้ำ (N+1)
- `sql.DBStats.WaitCount` ที่ไต่ขึ้น = pool เล็กเกินไปหรือมี query ที่ค้างนาน
- ดู [Performance](./performance.md#sql)

**เขียนแล้วอ่านไม่เจอในทันที**

อ่านจาก connection `readonly` ที่เป็น replica? replication lag ไม่ใช่บั๊ก — อ่านหลังเขียน
ให้ใช้ connection หลัก

**transaction ไม่ rollback อย่างที่คิด**

ทุก query ใน transaction ต้องใช้ `repository.NewWithDB[M](ctx, tx)` — repository ที่
สร้างจาก `ctx` เฉยๆ ใช้ connection ปกติ ไม่ได้อยู่ใน transaction นั้น
([Transactions](./database-transactions.md))

## Cache

**เขียนแล้วอ่านไม่เจอ / cache ไม่ทำงานเลย**

`ctx.Cache().Enabled()` — cache ที่ไม่ได้ตั้งค่า **degrade เงียบตามดีไซน์**: เขียนทิ้ง
อ่าน miss เสมอ ไม่มี error ให้เห็น ตรวจ `"cache":[]` ใน boot log

**key ชนกันข้าม environment**

`CACHE_PREFIX` namespace ทั้ง key และ channel ของ pub/sub — staging กับ production ที่
ใช้ redis ตัวเดียวกันต้องตั้งคนละค่า

**lock ไม่ปล่อย**

`Lock` มี TTL เสมอ — process ที่ตายไม่ทำให้ lock ค้างตลอดกาล แต่ TTL ที่สั้นกว่างานจริง
แปลว่ามีสองคนถือ lock พร้อมกันได้ ([Locks](./cache-locks.md))

## Message Queue

**publish สำเร็จ แต่ consumer ไม่ได้รับ**

อาการอันดับหนึ่งของ RabbitMQ และเกือบทุกครั้งคือ **binding**:

1. `nil` จาก `Publish` แปลว่า broker รับไว้ ไม่ได้แปลว่ามีคิวรับ — ตั้ง `Mandatory: true`
   ชั่วคราวเพื่อให้ misroute กลายเป็น error
2. เทียบ `binding_keys` ใน boot log ของ consumer กับ routing key ที่ publisher ส่งจริง
3. `topology=external` ใน log แปลว่า consumer ใช้ `On` — มันไม่ได้สร้าง binding ให้
4. `QueueInfo(name).Messages` > 0 แต่ไม่มีใครกิน = consumer ไม่ได้ผูกกับคิวนั้น

**consumer เงียบไปหลัง broker restart**

ไม่ควรเกิด — consumer reconnect เอง มองหา `mq consumer: connection lost, reconnecting`
กับ `reconnected` ใน log ถ้าไม่มีเลยแปลว่า consumer ไม่ได้ `Start()` หรือ process นี้ไม่ใช่
role ที่รัน consumer

**message วนซ้ำไม่หยุด / database ล่มตาม**

`core.Requeue(err)` กับความล้มเหลวถาวร = busy loop ใช้ dead-letter หรือ
[delay queue](./mq-patterns.md#retry-backoff) แทน

**message หายไปเฉยๆ**

คิวไม่มี DLX — message ที่ handler reject ถูกทิ้ง ([Topology](./mq-topology.md#dead-letter-exchange))

**`MQ_DISABLED`**

ไม่ได้ตั้ง `MQ_*` — ต่างจาก cache ตรงที่ MQ **fail ดัง** โดยตั้งใจ

## Jobs

**job ไม่ยิงตามเวลา**

ไล่ตามลำดับนี้:

1. `scheduler` ถูก `Start()` หรือเปล่า — และ process นี้เป็น role ที่รัน scheduler ไหม
2. boot log มี `job registered ... schedule=cron(...) next_run=...` ไหม
3. `queue` ของ job อยู่ใน `queues` ของ runner ไหม — **คิวที่ไม่มี worker หยิบ = ค้างเงียบ**
4. job ถูก `Pause` ไว้หรือเปล่า (`runner.Registry().Info()` มี `paused`)
5. timezone ของ cron — เทียบ `"timezone"` บนบรรทัด `scheduler started` กับที่ตั้งใจไว้
   ถ้าไม่ได้ตั้ง `WithSchedulerLocation` มันคือ timezone ของ **process** ซึ่ง
   container ที่ไม่ได้ตั้ง `TZ` เป็น UTC — ทั้งตารางจะเลื่อนไป 7 ชั่วโมงพร้อมกัน
   (job สี่ทุ่มไปยิงตี 5) ดู [Scheduler → Timezone](./scheduler.md#timezone)

**trigger แล้วได้ `JOB_NOT_FOUND`**

job ถูก register ใน process ที่รับ HTTP แต่ worker อยู่คนละ process ที่ registry ไม่มี job
นั้น — registry ต้องเหมือนกันทุก role ที่เกี่ยวข้อง

**run ค้างสถานะ `running` ตลอดกาล**

worker ตายไปกลางทางโดยไม่ได้ requeue หา run พวกนี้ด้วย query เดียว:

```go
repository.New[core.JobRun](ctx).
    Where("status = ? AND started_at < ?", core.RunRunning, time.Now().Add(-time.Hour)).
    FindAll()
```

`WorkerID` บอกว่ามันอยู่บน pod ไหนตอนตาย

**run หายหมดหลัง restart**

ยังใช้ in-memory store อยู่ — เปลี่ยนเป็น [jobstore](./jobs-store.md#durable-runs-sql)

**cancel แล้วไม่หยุด**

handler ไม่ได้เช็ค `c.IsStopping()` ใน loop Go ฆ่า goroutine ไม่ได้ หลัง `StopGrace`
มันจะถูกบันทึกว่า canceled แล้วปล่อยทิ้งพร้อม warning

**`MaxConcurrent: 1` แต่รันพร้อมกันหลายตัว**

limiter default นับเฉพาะในโปรเซส — หลาย replica ต้องใช้
[redis limiter](./jobs-running.md#cluster-wide-limits)

**job ที่ตั้ง schedule ไว้ fail ทุกครั้งด้วย `INVALID_PARAMS`**

scheduled run ไม่ส่ง parameter มา — parameter ที่ `Required()` จะพังทุกครั้ง ตรวจแค่
รูปแบบแล้ว default ค่าใน handler แทน ([Parameters](./jobs-defining.md#parameters))

## Logging & Sentry

**ไม่มี event ขึ้น Sentry**

- `APP_SENTRY_DSN` ตั้งหรือยัง (`"sentry":true` ใน boot log)
- error ที่ status < 500 **ไม่ถูกส่งโดยตั้งใจ** — 400 คือ client ผิด ไม่ใช่ incident
- process ที่จบเร็ว (script, job สั้นๆ) อาจปิดก่อน flush — `app.Shutdown()` จัดการให้แล้ว
  ถ้าเรียก

**หนึ่ง error ขึ้นสองใบ**

log แล้ว return error เดิมต่อ — เลือกอย่างใดอย่างหนึ่ง
([Logging Practices](./logging-practices.md))

**source ของ log ชี้ไปที่ไฟล์ของ framework**

เกิดกับโค้ดที่ห่อ logger เองอีกชั้น — ใช้ `ctx.Log()` ตรงๆ

## Testing

**เทสล้มเมื่อรันพร้อมกัน แต่ผ่านเมื่อรันเดี่ยว**

- แชร์ database เดียวกันโดยไม่ได้แยก schema — `coretest.NewContext(t, ...)` ให้ database
  ของเทสนั้นเอง
- ใช้ `time.Now()` เทียบค่าตรงๆ หรือ sleep แล้วหวังว่าทัน

**`go test ./...` ที่ root ไม่เห็นเทสของ v2**

`v2/` เป็น module แยก — ต้อง `cd v2` ก่อนเสมอ

**integration test ถูกข้าม**

ต้องมี build tag: `go test --tags=integration ./...` และต้องมี service จริงตามที่ `APP_*`
ชี้ไว้ ([Integration tests](./testing-integration.md))

## Deploy

**pod ถูกฆ่ากลาง drain / เห็น 500 ตอน deploy**

`WithDrainTimeout` ต้อง **ต่ำกว่า** `terminationGracePeriodSeconds` ไม่งั้นลำดับ shutdown
ไม่มีวันเดินจนจบ ([Runner](./runner.md) · [Deployment](./deployment.md))

**`/readyz` ล้มทั้งที่ service ยังใช้งานได้**

readiness ตรวจ dependency ทั้งหมด — dependency ที่ไม่ critical ควรทำให้เป็น `degraded`
ไม่ใช่ `down` และ **liveness ต้องไม่แตะ dependency เลย** ([Health](./health.md))

**module ดึงไม่ลง (`410 Gone` / `unknown revision`)**

tag ยังไม่ขึ้นถึง proxy — บังคับให้ดึงตรงจาก GitHub แทน cache ของ proxy

```sh
GOFLAGS=-mod=mod GONOSUMDB='github.com/pskclub/*' GOPROXY=direct   go get github.com/pskclub/mine-core/v2@latest
```

## ยังไม่หาย

1. เปิด `APP_LOG_LEVEL=debug` แล้วยิงซ้ำ — query log, MQ ack, job dispatch ล้วนอยู่ที่
   ระดับ debug
2. ไล่ด้วย `request_id` เส้นเดียว: log ของ request, query ที่มันยิง, call ที่มันเรียกออก
   และ event ใน Sentry ใช้ id เดียวกันหมด
3. ตัดให้เล็กที่สุดที่ยังเกิดอาการ — เทสหนึ่งตัวที่รันได้ด้วย `coretest` มีค่ามากกว่า
   การเดาบน production
