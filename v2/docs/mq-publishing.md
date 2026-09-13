# Publishing

```go
// payload ที่ไม่ใช่ []byte ถูก JSON-encode ให้
err := ctx.MQ().Publish("orders", "order.created", Order{ID: "o1"})

// typed helper — เหมือนกันทุกอย่าง แค่ให้ compiler ช่วยจับ type ของ payload
err = core.PublishAs(ctx.MQ(), "orders", "order.created", order)
```

ค่า default ของหนึ่ง publish: **persistent**, `application/json`, ประทับเวลาให้,
รอ confirm จาก broker

## `err == nil` แปลว่าอะไร {#err-nil}

`Publish` เปิด **publisher confirms** ไว้ และรอ ack จาก broker ก่อนคืนค่า — `nil` จึงแปลว่า
**broker รับผิดชอบ message นั้นแล้ว** ไม่ใช่แค่ "ส่งออกจาก process ไปแล้ว" นั่นคือความต่าง
ระหว่าง *sent* กับ *stored* และเป็นเหตุผลที่ `DeliveryMode: Persistent` เพียงอย่างเดียว
ไม่เคยรับประกันอะไร (มันแปลว่า "ถ้าถึงคิว ให้เขียนลงดิสก์" เท่านั้น)

สิ่งที่มัน **ไม่ได้** แปลว่า:

- มี queue ได้รับ message นั้น — publish เข้า exchange ที่ไม่มี binding ตรงกันจะถูก
  confirm แล้วทิ้ง binding เป็นเรื่องของ [topology](./mq-topology.md) ไม่ใช่ของ publisher
  (จะให้ error ต้องใช้ [`Mandatory`](#mandatory))
- มีใครประมวลผลสำเร็จ — นั่นคือเรื่องของ consumer และ [ack](./mq-consumer.md#ack-nack)

## Errors

| กรณี | ผลลัพธ์ |
|---|---|
| broker ack | `nil` |
| broker nack | `502 MQ_NACK` — `errors.Is(err, core.ErrMQNack)` |
| ไม่มี queue ไหนรับ (เฉพาะ `Mandatory`) | `502 MQ_NO_ROUTE` — `errors.Is(err, core.ErrMQNoRoute)` |
| ต่อ broker ไม่ได้ / timeout / รอ channel ว่างไม่ทัน | error พร้อม cause จาก driver |
| ไม่ได้ตั้ง `MQ_*` | `503 MQ_DISABLED` — `errors.Is(err, core.ErrMQDisabled)` |
| ปิด publisher ไปแล้ว (shutdown) | `503 MQ_CLOSED` — `errors.Is(err, core.ErrMQClosed)` |

`MQ_NACK` คือกรณีที่ต้องคิด: broker รับ message ไว้แล้วปฏิเสธทีหลัง (ดิสก์เต็ม, queue
ปฏิเสธ) message **ไม่ได้** ถูกเก็บ และคนที่ต้องตัดสินใจว่าจะทำยังไงต่อคือ caller

```go
if err := ctx.MQ().Publish("orders", "order.created", order); err != nil {
    if errors.Is(err, core.ErrMQNack) || errors.Is(err, core.ErrMQDisabled) {
        // broker รับไม่ได้ — เก็บไว้ให้ระบบอื่นส่งซ้ำ อย่ากลืน error นี้
        return ctx.NewError(err, errmsgs.MQError)
    }
    return err
}
```

publish ทุกครั้งฝาก breadcrumb ไว้กับ Sentry (`mq.publish`, exchange + key + ขนาด) —
ตอนมี error เกิดทีหลังใน request เดียวกัน จะเห็นว่ามันเคย publish อะไรไปแล้วบ้าง

## `PublishWith`

```go
err := ctx.MQ().PublishWith("orders", "order.created", order, core.PublishOptions{
    MessageID:     order.ID,        // ให้ consumer dedupe ได้
    CorrelationID: c.Request().Header.Get(echo.HeaderXRequestID),
    Type:          "order.created",
    Headers:       map[string]any{"schema": 2, "tenant": tenantID},
    Expiration:    10 * time.Minute,
    Priority:      5,
    Mandatory:     true,
})
```

| field | ผล |
|---|---|
| `MessageID` | ตัวระบุ message ใช้เป็น idempotency key ฝั่ง consumer — **ตั้งให้เป็นค่าที่คงที่ ไม่ใช่ UUID สุ่มใหม่ทุกครั้งที่ retry** |
| `CorrelationID` | ร้อย request → message → งานฝั่ง consumer เข้าด้วยกันตอนไล่ log |
| `ReplyTo` | คิวที่ให้ตอบกลับ (request/reply) |
| `Type` | ชื่อ event สำหรับคิวที่รับหลายชนิดจากคิวเดียว |
| `Headers` | เดินทางไปกับ message — tracing, เวอร์ชัน schema, tenant |
| `ContentType` | default `application/json` (แต่ดู[กับดักของ `[]byte`](#payload-กับ-encoding)) |
| `Priority` | 0–9 มีความหมายเฉพาะคิวที่ประกาศ `MaxPriority` ไว้ |
| `Expiration` | per-message TTL — ไม่ถูกส่งถึงในเวลานี้ก็ทิ้ง (หรือ dead-letter) |
| `Transient` | เก็บใน memory เท่านั้น หายเมื่อ broker restart |
| `Mandatory` | error ถ้าไม่มีคิวไหนรับ |
| `AppID` / `UserID` | ผู้ส่ง — `UserID` ถูก broker ตรวจกับ login ของ connection ตั้งเมื่อมันตรงกันเท่านั้น |
| `Timestamp` | default `time.Now()` |

### `Expiration` กับความคาดหวังที่ผิด

`Expiration` วัดจาก "อยู่ในคิวนานแค่ไหน" ไม่ใช่ "ทำงานเสร็จตอนไหน" และ message ที่หมดอายุ
ระหว่างรออยู่หัวคิว**ถูกลบตอนที่มันขึ้นมาถึงหัวคิวเท่านั้น** — คิวหนึ่งคิวที่มี TTL คนละค่า
จึงหมดอายุตามลำดับที่เข้ามา ไม่ใช่ตามเวลาจริง (head-of-line blocking) ถ้าต้องการ delay
หลายระดับ ใช้คนละคิว — [ดู retry pattern](./mq-patterns.md#retry-backoff)

### `Mandatory`

`Mandatory: true` เปลี่ยน misroute เงียบๆ ให้เป็น error (`MQ_NO_ROUTE`) แลกกับ round trip
เพิ่มอีกหนึ่งครั้ง

ควรใช้กับ: event สำคัญที่ binding หายแล้วต้องรู้ทันที และ path ที่ deploy ใหม่ๆ ซึ่ง
topology อาจยังไม่ครบ

ไม่ควรใช้กับ: event แบบ fan-out ที่ *ตั้งใจ* ให้ไม่มีคนฟังก็ได้ (`analytics.*` ที่ยังไม่มี
consumer) — ไม่งั้นจะได้ error ที่ไม่มีใครควรทำอะไรกับมัน

## Payload กับ encoding

| ส่งอะไรเข้าไป | ลงสายเป็น | `ContentType` |
|---|---|---|
| struct / map / slice / ตัวเลข | JSON | `application/json` |
| `[]byte` | ตามนั้นไม่แตะ | `application/octet-stream` |

⚠️ **กับดัก:** ส่ง `[]byte` ที่เป็น JSON อยู่แล้ว (เช่น `d.Body` ที่ republish ต่อ) จะได้
content type เป็น `application/octet-stream` เพราะแพ็กเกจนี้ไม่ยืนยันแทนคุณว่าไบต์ชุดนั้น
คือ JSON ระบุเองถ้ามันสำคัญ:

```go
ctx.MQ().PublishWith("orders.retry", d.RoutingKey, d.Body, core.PublishOptions{
    ContentType: d.ContentType,     // รักษาของเดิมไว้
})
```

### ใส่อะไรลงใน payload

**id อย่างเดียว** — consumer ไปอ่านสถานะปัจจุบันเอง เล็ก ทันสมัยเสมอ ทนต่อการถูก
ประมวลผลสลับลำดับ แลกกับหนึ่ง read ต่อ message และ consumer ต้องเข้าถึง database ของ
เจ้าของข้อมูลได้ (หรือมี API ให้เรียก)

**ทั้งก้อน** — consumer ไม่ต้องอ่านอะไรเพิ่ม เหมาะกับข้ามขอบเขต service ที่ไม่ควรแชร์
database กัน แต่ payload คือ snapshot: สอง update ติดๆ กันอาจถูกประมวลผลสลับกันแล้ว
ค่าที่เก่ากว่าชนะ ใส่ version หรือ `updated_at` ไว้ให้ consumer เทียบถ้าเรื่องนี้สำคัญ

ข้ามขอบเขต service ให้เอนไปทางก้อนเต็ม + version และให้คิดว่า payload คือ **API สาธารณะ**
ที่แก้แบบ breaking ไม่ได้ — [วิธีเปลี่ยน schema](./mq-patterns.md#เปลี่ยน-schema-ของ-message)

อย่าใส่ความลับดิบ (password, token, เลขบัตร) ลง payload: message ค้างอยู่ในคิวได้เป็น
วันๆ อ่านได้จาก management UI และมักถูก log ตอน dead-letter

## Publish หลัง commit

message ที่ publish อยู่ใน [transaction](./database-transactions.md) ออกไปทันที — broker
ไม่รู้จัก transaction ของ database consumer จึงรับ `order.created` แล้วไปหา order ไม่เจอ
เพราะ transaction ยังไม่ commit (หรือกำลังจะ rollback)

```go
// ❌ consumer ชนะการแข่งกับ commit ได้
repo.Transaction(func(tx *gorm.DB) error {
    if err := repository.NewWithDB[Order](ctx, tx).Create(&order); err != nil {
        return err
    }
    return ctx.MQ().Publish("orders", "order.created", order)   // ออกไปแล้ว
})

// ✅ publish เมื่อ row อยู่จริงแน่นอนแล้ว
if err := repo.Transaction(func(tx *gorm.DB) error {
    return repository.NewWithDB[Order](ctx, tx).Create(&order)
}); err != nil {
    return err
}
if err := ctx.MQ().Publish("orders", "order.created", order); err != nil {
    return err
}
```

แบบหลังยังเหลือช่องอยู่: commit สำเร็จ แล้ว process ตายก่อน publish → ไม่มีใครรู้ว่ามี
order ใหม่ ถ้าช่องนี้รับไม่ได้ ให้เขียน message ลง database ใน transaction เดียวกันแล้วส่ง
ทีหลัง — [transactional outbox](./mq-patterns.md#transactional-outbox)

## Publish จำนวนมาก {#publish-bulk}

`Publish` แต่ละครั้ง **รอ confirm** ดังนั้น loop 50,000 ครั้งคือ 50,000 round trip เรียงกัน

```go
// ❌ ช้าเป็นเส้นตรงตามจำนวนแถว
for _, o := range orders {
    ctx.MQ().Publish("orders", "order.exported", o)
}

// ✅ ทางที่หนึ่ง: หนึ่ง message ที่ consumer แตกเองได้
ctx.MQ().Publish("orders", "orders.exported", ids)

// ✅ ทางที่สอง: publish ขนานกัน — pool คุมเพดานให้อยู่แล้ว
g, _ := errgroup.WithContext(ctx)
g.SetLimit(8)                       // ให้พอๆ กับ WithMQChannels
for _, o := range orders {
    g.Go(func() error { return pub.Publish("orders", "order.exported", o) })
}
if err := g.Wait(); err != nil {
    return err
}
```

ทางที่หนึ่งดีกว่าเกือบเสมอ ไม่ใช่แค่เร็วกว่า แต่ฝั่ง consumer ก็ได้ handler ครั้งเดียวที่
ทำ bulk operation ได้ แทนที่จะเป็น 50,000 ครั้งที่แต่ละครั้งทำทีละแถว

`Publish` เรียกจากหลาย goroutine พร้อมกันได้ปลอดภัย — channel pool จัดคิวให้เอง เกิน
`WithMQChannels` แล้วจะรอ ไม่ใช่ error (จนกว่า context จะหมดอายุ)

## จาก job หรือ consumer

`ctx.MQ()` ใช้ได้เหมือนกันทุกที่ที่มี `IContext` — [job](./jobs.md), [scheduler](./scheduler.md),
consumer ตัวอื่น ไม่มีอะไรพิเศษ

```go
_ = core.RegisterJob(reg, core.JobDef{Name: "export-orders"},
    func(ctx core.ICronjobContext, p *ExportParams) error {
        // …
        return ctx.MQ().Publish("orders", "order.exported", p.ID)
    })
```

ที่ต้องระวังคือ goroutine ที่อยู่นานกว่า context ของมัน — ดู
[Publisher นอก request](./mq-connection.md#publisher-นอก-request)
