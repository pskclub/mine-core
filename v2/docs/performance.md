# Performance & Tuning

ตัวเลขทั้งหมดที่ v2 ให้ตั้งได้ อยู่ที่เดียวกัน พร้อมคำตอบว่า**ดูจากอะไรถึงจะรู้ว่าต้องปรับ**

หลักที่ใช้ได้กับทุกหัวข้อในหน้านี้: **อย่าปรับตัวเลขก่อนมีตัววัด** ค่า default ถูกเลือกมา
ให้ service ขนาดกลางใช้ได้โดยไม่ต้องแตะ การเพิ่มตัวเลขโดยไม่รู้ว่าคอขวดอยู่ตรงไหน
มักย้ายปัญหาไปที่อื่นแทนที่จะแก้มัน — pool ที่ใหญ่ขึ้นแปลว่า database รับ connection
มากขึ้น ไม่ได้แปลว่ามันตอบเร็วขึ้น

## ตารางค่า default ทั้งหมด

### HTTP

| ค่า | default | ตั้งที่ |
|---|---|---|
| Read timeout | 5 นาที | `HTTPOptions.ReadTimeout` |
| Read header timeout | 20 วินาที | `HTTPOptions.ReadHeaderTimeout` |
| Write timeout | **ไม่ตั้ง** | `HTTPOptions.WriteTimeout` |
| Idle timeout | 120 วินาที | `HTTPOptions.IdleTimeout` |
| Body limit | 10 MB | `HTTPOptions.BodyLimit` หรือ `core.BodyLimit(n)` ต่อ route |
| Graceful timeout | 10 วินาที | `WithDrainTimeout` ของ [Runner](./runner.md) |

`ReadTimeout` กว้าง (5 นาที) เพราะการอัปโหลดจากมือถือ, long-poll และ SSE เป็นเรื่องปกติ
ของ service จริง ส่วนช่องโหว่ slow-loris ถูกปิดด้วย `ReadHeaderTimeout` แทน — header
ไม่มีทางใช้เวลาเป็นนาที

**ไม่มี default ของ `WriteTimeout` โดยตั้งใจ**: มันคือ deadline เด็ดขาดของทั้ง exchange
ค่าที่ใหญ่พอสำหรับการดาวน์โหลดช้าๆ ย่อมใหญ่เกินกว่าจะป้องกันอะไร และค่าที่เล็กพอจะ
ป้องกันก็ตัด SSE ทิ้ง ตั้งมันเฉพาะ server ที่ไม่ได้เสิร์ฟทั้งสองอย่าง

### SQL

| ค่า | default | ตั้งที่ |
|---|---|---|
| Max open conns | 20 | `core.WithMaxOpenConns(n)` |
| Max idle conns | 5 | `core.WithMaxIdleConns(n)` |
| Conn max lifetime | 1 ชั่วโมง | `core.WithConnMaxLifetime(d)` |

```go
db, err := core.NewDatabase(env,
    core.WithMaxOpenConns(20),
    core.WithMaxIdleConns(5),
    core.WithConnMaxLifetime(time.Hour),
)
```

**คำนวณจากฝั่ง database ไม่ใช่ฝั่ง service**: postgres ที่ตั้ง `max_connections=100`
และมี 6 replica แปลว่า 16 connection ต่อ replica คือเพดานจริง ไม่ใช่ 20 — เกินกว่านั้น
คือ replica ที่ deploy ตัวที่เจ็ดแล้วทุกตัวเริ่ม connect ไม่ได้พร้อมกัน

ที่ต้องนับด้วย: job worker กับ MQ consumer ใช้ pool เดียวกับ HTTP — process ที่รันทั้ง
สามบทบาทใช้ connection มากกว่า process ที่เสิร์ฟ API อย่างเดียว

`ConnMaxLifetime` มีไว้เพื่อให้ connection หมุนเวียน: load balancer และ proxy หลายตัว
ตัด connection ที่เปิดค้างนานอยู่แล้ว การให้ pool ปิดเองก่อนเป็นทางที่เจ็บน้อยกว่า

### Cache (redis)

| ค่า | default | ตั้งที่ |
|---|---|---|
| Pool size | ตามที่ go-redis เลือก (10 × CPU) | `APP_CACHE_POOL_SIZE` |

cache ที่ตั้ง pool เล็กเกินไปแสดงอาการเป็น latency ของ `Get` ที่กระโดดเป็นช่วงๆ ตอน
traffic สูง ไม่ใช่ error — ดู [Cache](./cache-connection.md)

### Message Queue

| ค่า | default | ตั้งที่ |
|---|---|---|
| Publish channels | 8 | `core.WithMQChannels(n)` |
| Publish timeout | 5 วินาที | `core.WithMQPublishTimeout(d)` |
| Consumer prefetch | 10 | `core.WithMQPrefetch(n)` |
| Consumer concurrency | 1 | `core.WithMQConcurrency(n)` |
| Handler timeout | 30 วินาที | `core.WithMQHandlerTimeout(d)` |
| Reconnect delay | 2 วินาที | `core.WithMQReconnectDelay(d)` |

จุดตั้งต้น: `prefetch ≈ concurrency × 2` — รายละเอียดและวิธีอ่านอาการอยู่ที่
[MQ consumers](./mq-consumer.md#prefetch-กับ-concurrency)

### Jobs

| ค่า | default | ตั้งที่ |
|---|---|---|
| Workers | 4 | `core.WithWorkers(n)` |
| Queues | `["default"]` | `core.WithQueues(...)` |
| Job timeout | 5 นาที | `JobDef.Timeout` |
| Stop grace | 30 วินาที | `JobDef.StopGrace` |
| Max attempts | 1 | `JobDef.MaxAttempts` |
| Slot backoff | 2 วินาที | `core.WithSlotBackoff(d)` |
| Queue poll (SQL) | 1 วินาที | `jobstore.WithPollInterval(d)` |
| Log retention | runs 30 วัน / logs 7 วัน | `JobDef.RetainRuns` / `RetainLogs` |

[Runner & Concurrency](./jobs-running.md) อธิบายว่าสามชั้นของการจำกัดต่างกันยังไง

### อื่นๆ

| ค่า | default | ตั้งที่ |
|---|---|---|
| Requester timeout | 30 วินาที | `core.Requester(ctx).Resty().SetTimeout(d)` ตอน boot |
| Page limit | 30 (สูงสุด 10,000) | `PageOptions.Limit` |

## หาคอขวดก่อนปรับ

| อาการ | มักเป็นเพราะ | ดูที่ |
|---|---|---|
| latency สูงขึ้นพร้อมกันทุก endpoint | pool ตัน (SQL หรือ MQ channel) | `sql.DBStats.WaitCount`, latency ของ publish |
| latency สูงเฉพาะ endpoint เดียว | query ไม่มี index หรือ N+1 | [Query logging](./database-logging.md) |
| CPU ต่ำ แต่ throughput ตัน | concurrency ต่ำเกิน (worker / prefetch) | ความลึกของคิว |
| memory ไต่ขึ้นเรื่อยๆ | อ่านทั้งตารางเข้า memory, body ไม่จำกัดขนาด | heap profile, `PageOptions` ที่ไม่ได้ตั้ง |
| database CPU สูงจาก query เดียวกันซ้ำๆ | ไม่มี cache | [Cache-aside](./cache-patterns.md) |

การวัดที่ให้ผลตอบแทนสูงสุดสามอย่าง เรียงตามความคุ้ม:

1. **ความลึกของคิว** (MQ และ job) — บอกว่าตามงานทันไหม ก่อนที่ user จะรู้สึก
2. **query log ที่ช้ากว่าเกณฑ์** — เกือบทุกปัญหา performance ของ service แบบนี้คือ query
3. **p95 ของ endpoint** ไม่ใช่ค่าเฉลี่ย — ค่าเฉลี่ยซ่อน request ที่ช้าที่สุดไว้เสมอ

## รูปแบบที่ทำให้เร็วขึ้นจริง

**อ่านทีละหน้า ไม่ใช่ทั้งตาราง** — `FindAll()` บนตารางที่โตขึ้นเรื่อยๆ คือ OOM ที่รอเวลา
ใช้ [pagination](./database-pagination.md) หรือ `FindInBatches`

```go
// ❌ ตารางโตขึ้นทุกวัน โค้ดไม่เปลี่ยน แล้ววันหนึ่งก็ล้ม
rows, _ := repository.New[Order](ctx).FindAll()

// ✅ ทำทีละก้อน
repository.New[Order](ctx).FindInBatches(&batch, 500, func(tx *gorm.DB, n int) error { … })
```

**เลือกเฉพาะคอลัมน์ที่ใช้** — `Select("id, status")` ลดทั้ง I/O ของ database และ memory
ของ service โดยเฉพาะตารางที่มีคอลัมน์ text ใหญ่ๆ

**preload แทนที่จะวนอ่าน** — N+1 คือปัญหา performance ที่พบบ่อยที่สุดในทุก service
ที่ใช้ ORM ดู [Repository: Relations](./repository-relations.md)

**cache-aside ตรงที่ราคาแพงและเปลี่ยนไม่บ่อย** — `Remember` ทำ pattern นี้ให้ในบรรทัด
เดียว และ cache ที่ปิดอยู่ก็ยังรันได้ ([Cache patterns](./cache-patterns.md))

**ยกงานหนักออกจาก request** — สิ่งที่ user ไม่ต้องรอผลควรเป็น [job](./jobs.md)
request ที่ตอบใน 50ms แล้วทำงานต่อเบื้องหลัง ดีกว่า request ที่ตอบใน 3 วินาที เกือบเสมอ

**ทำทีเดียวหลายรายการ** — `MSet`/`MGet` ของ cache, `CreateInBatches` ของ repository,
และหนึ่ง message ที่ consumer แตกเอง แทน N message ([MQ](./mq-publishing.md#publish-bulk))

## สิ่งที่ framework ทำให้แล้ว ไม่ต้องปรับ

- **connection pool อยู่ที่ `App` อายุเท่า process** — ไม่มีการเปิด/ปิดต่อ request
- **channel pool ของ MQ** จำกัด concurrency ให้อยู่แล้ว publish ที่เกินโควตาจะรอคิว
  ไม่ใช่เปิด channel ใหม่จนชน `channel_max` ของ broker
- **pagination ของ Mongo** เปลี่ยนกลยุทธ์ตามขนาดหน้าเอง (single `$facet` สำหรับหน้าเล็ก,
  แยกสอง query สำหรับหน้าใหญ่) เพื่อไม่ให้ชนเพดาน BSON
- **log ของ job ปิดไว้เป็น default** เพราะมันคือส่วนที่กินพื้นที่มากที่สุดของระบบ

## ก่อนจะเพิ่มตัวเลข ให้ถามสามข้อ

1. **ตัวไหนคือคอขวดจริง** — เพิ่ม worker ในขณะที่ database เป็นคอขวด ทำให้แย่ลง ไม่ใช่ดีขึ้น
2. **ปลายทางรับไหวไหม** — pool 100 connection ต่อ database ที่ตั้ง `max_connections=100`
   คือการทำ outage ให้ตัวเอง เช่นเดียวกับ concurrency ที่ยิง API ภายนอกจนโดน rate limit
3. **ถ้าคิดจะปรับเพราะช้า วัดแล้วหรือยัง** — ถ้าคำตอบคือ "ยัง" นั่นคือสิ่งที่ควรทำก่อน
