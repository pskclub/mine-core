# Architecture

หน้านี้อธิบายว่า service ที่สร้างด้วย v2 **ประกอบด้วยอะไร และอะไรคุยกับอะไร** — เป็นแผนที่
ที่ควรอ่านก่อนลงรายละเอียดของแต่ละโมดูล ถ้าอยากเห็นโค้ดที่รันได้ก่อน ข้ามไป
[Getting Started](./getting-started.md) แล้วค่อยกลับมา

## ภาพรวมหนึ่งภาพ

```
   HTTP request ──▶ HTTPServer (Echo v5) ────┐
   cron tick ─────▶ Scheduler ──▶ JobRunner ─┤
   AMQP delivery ─▶ MQConsumer ──────────────┼──▶  IContext  ──▶  โค้ดของคุณ
   redis message ─▶ PubSub Subscriber ───────┤    (ต่อ 1 หน่วยงาน)      │
   go test ───────▶ coretest ────────────────┘                          │
                              ▲                                         │
                              │ สร้างจาก                                 ▼
                        ┌─────┴──────────────────────────────────────────────┐
                        │  App — อายุเท่า process                            │
                        │  SQL pools · Mongo · Cache/PubSub · MQ · Storage   │
                        │  Mailer · Pusher · LLM · Requester · Sentry · Log  │
                        └────────────────────────────────────────────────────┘
```

อ่านจากภาพนี้ได้สามอย่าง ซึ่งเป็นแกนของทั้ง framework:

1. **`App` คือของที่มีชิ้นเดียวต่อ process** — connection pool ทุกตัวอยู่ที่นี่
2. **`IContext` คือของที่มีชิ้นหนึ่งต่อหนึ่งหน่วยงาน** — หนึ่ง request, หนึ่ง job run,
   หนึ่ง message
3. **ทุกทางเข้าจบที่ `IContext` ตัวเดียวกัน** — โค้ดชั้นในจึงไม่รู้และไม่ต้องรู้ว่างานนี้
   มาจาก HTTP, จาก cron หรือจากคิว

## App กับ IContext

นี่คือการแยกที่ v1 ไม่ได้ทำ และเป็นที่มาของบั๊กที่แก้ทีละจุดไม่ได้:

| | `App` | `IContext` |
|---|---|---|
| อายุ | เท่า process | เท่าหนึ่ง request / job / message |
| ถืออะไร | connection pool, client, capability | *handle* ที่ชี้ไปยัง capability ของ App โดย bind context ไว้แล้ว |
| สร้างโดย | `core.NewApp(env, opts...)` ตอน boot | delivery layer สร้างให้ (หรือ `app.NewContext(ctx, mode)`) |
| ปิดโดย | `app.Shutdown(ctx)` ครั้งเดียวตอนจบ | ไม่ต้องปิด — มันไม่ได้เป็นเจ้าของอะไร |

> ⚠️ ใน v1 `IContext.Close()` ปิด pool ที่ใช้ร่วมกันทุก request — สอง concept นี้ถูก
> ยุบเป็นก้อนเดียว ผลคือ connection ถูกปิดผิดจังหวะโดยที่ไม่มีใครตั้งใจ v2 แยกมันออก
> ก่อนอย่างอื่นทั้งหมด และรายละเอียดอยู่ที่ [Context & App](./context.md)

`App` ประกอบด้วย functional option — capability ที่ไม่ได้ใส่ ไม่ใช่ `nil` แต่เป็น
**implementation ที่ปิดอยู่**:

```go
app, err := core.NewApp(env,
    core.WithSQL("default", db),
    core.WithSQL("readonly", replica),   // named connection
    core.WithCache("default", redis),
    core.WithMQ(mq),
)
```

## Ambient context

handle ทุกตัวที่ดึงผ่าน `ctx` **ถูก bind context ไว้แล้ว** เมธอดของมันจึงไม่รับ `ctx`:

```go
ctx.Cache().Get(key, &v)                  // ไม่ใช่ Get(ctx, key, &v)
repository.New[User](ctx).FindOne(id)
core.Requester(ctx).Get(url)
```

ที่ทำแบบนี้ไม่ใช่เพื่อพิมพ์สั้นลง แต่เพื่อ **ทำให้ลืมส่ง `ctx` ไม่ได้** — deadline, การ
cancel และ trace-id ของงานนั้นจึงไหลลงไปถึง query ชั้นล่างสุดเสมอ ไม่ใช่เฉพาะตอนที่คน
เขียนนึกได้ อาการที่หายไปคือ "client ตัดสายไปแล้ว แต่ database ยังทำงานต่อ"

ต้องการงานที่อยู่นานกว่า request จริงๆ มี escape hatch ที่ต้องเขียนออกมาให้เห็น:

```go
pub := ctx.MQ().WithContext(context.Background())
```

## ทางเข้าสี่ทาง โค้ดชุดเดียว

| ทางเข้า | ใครสร้าง context | `ctx.Mode()` | อ่านต่อ |
|---|---|---|---|
| HTTP request | `WithHTTPContext` ใน `NewHTTPServer` | `ModeHTTP` | [HTTP Layer](./http.md) |
| cron tick / manual trigger | Scheduler enqueue → JobRunner worker | `ModeCron` | [Jobs](./jobs.md) · [Scheduler](./scheduler.md) |
| AMQP delivery | MQ consumer | `ModeMQ` | [Message Queue](./mq.md) |
| redis pub/sub message | Subscriber | `ModeMQ` | [Pub/Sub](./pubsub.md) |
| เทส | `coretest.NewContext(t, ...)` | `ModeTest` | [Testing](./testing.md) |

mode ไม่ได้เปลี่ยนความสามารถของ context — มันคือป้ายบอกที่มา ที่ Sentry ติดเป็น tag ให้
ทุก event (งานที่มาจากคิวทั้งสองแบบใช้ป้ายเดียวกัน)

ผลของการที่ทุกทางเข้าจบที่ interface เดียวกัน คือ service layer เขียนครั้งเดียวแล้วใช้ได้
จากทุกที่:

```go
// รู้จักแค่ IContext — ไม่รู้ว่าใครเรียก
func (s *OrderService) Ship(ctx core.IContext, orderID string) core.IError {
    order, err := repository.New[Order](ctx).FindOne(orderID)
    if err != nil {
        return err
    }
    return ctx.MQ().Publish("orders", "order.shipped", order)
}
```

```go
// เรียกจาก HTTP
func ShipOrder(c core.IHTTPContext) error { return svc.Ship(c, c.Param("id")) }

// เรียกจาก job
func shipPending(c core.ICronjobContext) error { return svc.Ship(c, id) }

// เรียกจาก consumer
func onPaid(c core.IMQContext, d *core.Delivery) error { return svc.Ship(c, d.MessageID) }

// เรียกจากเทส — ไม่ต้อง mock อะไรเลย
ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&Order{}))
err := svc.Ship(ctx, "o1")
```

`IHTTPContext`, `ICronjobContext`, `IMQContext` คือ `IContext` + ของเฉพาะทางของมัน
(request/response, job run, delivery) — ไม่ใช่ interface คนละสายกัน

## เส้นทางของหนึ่ง HTTP request

```
request
  │
  ├─ RequestID        ← สร้าง/รับ X-Request-ID แล้วปักลง context
  ├─ Sentry           ← เปิด hub + transaction ก่อน recover เพื่อให้ panic ถูกจับได้ครบ
  ├─ Request logger   ← บรรทัด access log ต่อ request
  ├─ Recover          ← panic → 500 ที่มี stack ของจุดที่ panic จริง
  ├─ CORS / BodyLimit
  │
  ├─ WithHTTPContext  ← ★ ที่นี่คือจุดที่ IContext ถูกสร้างจาก App
  │     │
  │     ├─ c.BindWithValidate(&req)   → 400 {code, message, fields} ถ้าไม่ผ่าน
  │     ├─ service(c, ...)            → repository / cache / mq — ทั้งหมด ctx-bound
  │     └─ return c.JSON(200, out)  หรือ  return err
  │
  └─ HTTPErrorHandler ← แปลง IError เป็น response body มาตรฐาน + log + Sentry
```

สองจุดที่ควรจำ:

- **`WithHTTPContext` คือเส้นแบ่ง** ข้างนอกเป็นโลกของ Echo ข้างในเป็นโลกของ framework
  handler ที่ลงทะเบียนผ่าน `e.GET/POST/...` ได้ `IHTTPContext` เสมอ
- **การ return error คือการรายงาน** ไม่ต้อง log เอง ไม่ต้อง capture Sentry เอง —
  ทำเพิ่มคือได้สองใบต่อหนึ่งเหตุการณ์

งานที่มาจาก job หรือ message เดินเส้นเดียวกันในรูปแบบของตัวเอง: worker/consumer สร้าง
context ที่มี timeout ของตัวเอง มี Sentry transaction ของตัวเอง และ panic จบที่หน่วยงาน
นั้น ไม่ใช่ที่ process

## Capabilities: ไม่มีอะไรเป็น nil

service ที่ไม่มี `CACHE_*` ต้องรันได้ ทุก capability จึงมี implementation แบบ "ปิดอยู่"
เสมอ — และ**พฤติกรรมของมันถูกเลือกมาแล้วว่าจะเงียบหรือจะดัง**:

| กลุ่ม | ไม่ได้ตั้ง config แล้วเป็นยังไง | ทำไม |
|---|---|---|
| Cache / Pub-Sub | **degrade เงียบ** — อ่าน miss, เขียนทิ้ง | cache miss คำนวณใหม่ได้ โค้ด cache-aside จึงรันได้ทั้งที่มีและไม่มี redis |
| MQ / Storage / Mongo / Mailer / Pusher / LLM | **fail ดัง** — ทุกคำสั่งคืน error ที่บอกว่า config ตัวไหนหาย | ไฟล์ที่หาย, เมลที่ไม่ได้ส่ง, message ที่ไม่มีใครได้รับ — เอาคืนไม่ได้ และไม่มีใครรู้ว่ามันหาย |
| Sentry | no-op ทุกเมธอด | โค้ดชุดเดียวรันได้ทั้ง dev และ prod |
| SQL (`ctx.DB()`) | **คืน `nil`** — เป็นข้อยกเว้นเดียว | `*gorm.DB` ไม่มีรูปแบบ "ปิดอยู่" ให้คืน |

`Enabled()` แยกของจริงออกจากของที่ปิดอยู่ และบรรทัดแรกของ boot log บอกว่า process นี้
ประกอบมาด้วยอะไรบ้าง — เพราะ capability ที่ degrade เงียบๆ แปลว่า config ที่หายไปหนึ่ง
บรรทัดจะไม่มีใครเห็นจนกว่าจะมี request แรกที่ต้องใช้:

```json
{"level":"INFO","msg":"app ready","env":"prod","service":"orders",
 "sql":["default","readonly"],"mongo":[],"cache":["default"],"mq":true,
 "storage":true,"mailer":false,"sentry":true}
```

## Error path: หนึ่งเหตุการณ์ หนึ่งรายงาน

```
service คืน core.IError  ──▶  handler คืนมันต่อ  ──▶  HTTPErrorHandler
                                                        ├─ body: {code, message, fields}
                                                        ├─ log บรรทัดเดียว (≥500 = error)
                                                        └─ Sentry (ผ่าน bridge ของ logger)
```

กติกาที่ทั้ง framework ยึด:

- **ห้ามคืน error ดิบจากชั้นไหนก็ตาม** — ใช้ `ctx.NewError(err, errmsgs.X)` ซึ่งเป็นตัวที่
  แนบ scope ของ request ไปด้วย
- **log แล้วห้าม return ซ้ำ / return แล้วห้าม log ซ้ำ** — logger มี bridge ไป Sentry อยู่แล้ว
  ทำสองครั้งคือหนึ่ง incident กลายเป็นสองใบ
- **ไม่มี panic ใน error path** — ทางที่ควรปลอดภัยที่สุดต้องไม่ใช่ทางที่ฆ่า process

รายละเอียด: [Error Handling](./error-handling.md) · [Service Errors](./service-errors.md) ·
[Logging Practices](./logging-practices.md)

## Observability

`request_id` ถูกปักลง context ตั้งแต่ middleware ตัวแรก จากนั้น **ทุกอย่างที่ derive จาก
context นั้นพกมันไปเอง** — log ทุกบรรทัด, query log ของ GORM/Mongo, HTTP call ที่ยิงออก
ผ่าน requester, breadcrumb และ event ของ Sentry

```
X-Request-ID ──▶ context ──┬──▶ ctx.Log()          request_id=…
                           ├──▶ ctx.DB()           query log ผูก request เดียวกัน
                           ├──▶ core.Requester(ctx) outbound call ผูก request เดียวกัน
                           └──▶ ctx.Sentry()       tag + breadcrumb
```

ข้าม process ใช้ `CorrelationID` ของ message เป็นตัวร้อยต่อ —
[MQ](./mq-publishing.md#publishwith) · [Logger](./logger.md) · [Sentry](./sentry.md)

## Shutdown เป็นลำดับ ไม่ใช่เหตุการณ์

การปิด service ไม่ใช่ "ปิดทุกอย่าง" แต่คือลำดับ และการทำผิดลำดับคือสิ่งที่เปลี่ยน deploy
ธรรมดาให้เป็น 500 เป็นชุดกับงานที่ค้างครึ่งทาง [`Runner`](./runner.md) เป็นเจ้าภาพ:

```
signal (SIGTERM)
  │
  1. BeforeStop hooks            ← ถอนตัวออกจาก service registry ฯลฯ
  2. หยุดผลิตงานใหม่              ← scheduler หยุด tick ก่อนใคร
  3. drain งานที่ทำอยู่           ← job runner → services → HTTP server (ตาม DrainTimeout)
  4. app.Shutdown()              ← ★ ที่นี่เท่านั้นที่ pool ถูกปิด
       ├─ หยุด subscriber / MQ consumer ก่อน  (ไม่งั้น handler จะวิ่งเข้าหา database ที่ปิดไปแล้ว)
       ├─ cache → mongo → mq → mailer → llm → sql
       └─ Sentry ปิดท้าย         ← event ที่เกิดระหว่าง shutdown ยังส่งออกทัน
  5. AfterStop hooks
```

⚠️ `DrainTimeout` ต้อง **ต่ำกว่า** `terminationGracePeriodSeconds` ของ orchestrator ไม่งั้น
process ถูกฆ่ากลางคัน และลำดับทั้งหมดนี้ก็ไม่เคยได้เกิด → [Deployment](./deployment.md)

## จุดที่ออกแบบไว้ให้ทดสอบ

v2 **ไม่มี generated mock และไม่มี mock generator** — แต่ละ capability ส่ง
implementation ในหน่วยความจำมาให้แทน (`NewMemoryCache`, `NewMemoryStorage`,
`NewMemoryMailer`, `NewMemoryPusher`, `NewRecordingSentry`) และ `coretest` ประกอบมัน
เป็น fixture ระดับ service ให้แล้ว

เหตุผลคือ mock ที่ generate มาพิสูจน์ได้แค่ว่า "เมธอดถูกเรียก" ส่วน implementation ใน
หน่วยความจำพิสูจน์ได้ว่า "ผลลัพธ์ถูก" — และมันคือโค้ดที่ compile คู่ไปกับ interface จริง
จึงพังทันทีที่ interface เปลี่ยน แทนที่จะเงียบไปจนกว่าจะมีใคร regenerate

```go
ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&models.Order{}))
err := services.NewOrderService(ctx).Ship(ctx, "o1")
```

→ [Testing](./testing.md) · [Mocks & fakes](./testing-mock.md)

## แผนที่: จากคำถามไปหาหน้าที่ตอบ

| อยากรู้ว่า… | อ่าน |
|---|---|
| App ประกอบยังไง / context มีอะไรบ้าง | [Context & App](./context.md) |
| config มาจากไหน เพิ่ม key ยังไง | [Configuration (ENV)](./env.md) |
| ไฟล์ในโปรเจกต์ควรวางยังไงเมื่อมันโต | [Project Structure](./structure.md) |
| หนึ่ง binary ควรมีกี่ role และ boot ยังไง | [Lifecycle & Roles](./lifecycle.md) |
| start/stop ทุกอย่างพร้อมกัน | [Runner](./runner.md) |
| route, middleware, bind, response | [HTTP Layer](./http.md) · [Middleware](./middleware.md) |
| error ควรหน้าตายังไงและใครเป็นคนรายงาน | [Error Handling](./error-handling.md) |
| งานเบื้องหลัง: cron, retry, run log | [Jobs](./jobs.md) |
| คุยกับ service อื่น | [Message Queue](./mq.md) · [Pub/Sub](./pubsub.md) |
| อ่าน/เขียนข้อมูล | [Database](./database.md) · [MongoDB](./mongo.md) · [Cache](./cache.md) |
| เทสโดยไม่ต้องต่อของจริง | [Testing](./testing.md) |
| ขึ้น production ต้องตั้งอะไร | [Deployment](./deployment.md) · [Health](./health.md) |

## หลักการที่ยึดทั้งหมดนี้ไว้ด้วยกัน

1. **Ambient context** — capability ถูก bind context แล้ว จึงลืมส่งไม่ได้
2. **No global mutable state** — ทุกอย่างมาจาก constructor, `go test -race ./...` สะอาด
3. **Capability ไม่เคยเป็น nil** — มีแต่ "ของจริง" กับ "ของที่ปิดอยู่" ที่พฤติกรรมถูกเลือกไว้แล้ว
4. **Errors are values** — typed, `errors.Is/As`, ไม่ panic ใน error path
5. **หนึ่งเหตุการณ์ หนึ่งรายงาน** — log กับ Sentry เป็นเส้นเดียวกัน
6. **Shutdown คือลำดับ** — หยุดรับงาน → drain → ค่อยปิด pool

ที่มาของแต่ละข้อและสิ่งที่มันแก้จาก v1 อยู่ที่ [ทำไมต้อง v2](./why-v2.md)
