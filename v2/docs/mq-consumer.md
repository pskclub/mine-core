# Consumers

consumer แปลงคิวเป็น handler แบบเดียวกับที่ HTTP server แปลง route เป็น handler และ
เป็นเจ้าของสิ่งที่ไม่ควรต้องเขียนเองซ้ำๆ: connection, prefetch, ack, context ต่อ message,
panic recovery, reconnect และการ drain ตอน shutdown

```go
c := app.NewMQConsumer(
    core.WithMQPrefetch(20),
    core.WithMQConcurrency(4),
    core.WithMQHandlerTimeout(30*time.Second),
)

c.OnQueue(core.ConsumeQueue{
    Queue:       core.QueueConfig{Name: "orders.shipping", DeadLetterExchange: "orders.dlx"},
    Exchange:    &core.ExchangeConfig{Name: "orders", Kind: core.ExchangeTopic},
    BindingKeys: []string{"order.created", "order.paid"},
}, func(ctx core.IMQContext, d *core.Delivery) error {
    order, err := core.BindDelivery[Order](d)
    if err != nil {
        return err        // payload พัง — ส่งใหม่กี่ครั้งก็พังเหมือนเดิม, dead-letter ไป
    }
    return service.Ship(ctx, order)
})

if err := c.Start(); err != nil {
    return err
}
```

`On(queue, handler)` ใช้เมื่อ topology ถูกสร้างไว้ที่อื่นแล้ว — แต่ค่า default ที่ควรใช้คือ
`OnQueue` เพราะ[คิวควรเป็นของคนที่อ่านมัน](./mq-topology.md#ใครควรเป็นคนประกาศ)

## `Start` และสิ่งที่มันรับประกัน

`Start` **ต่อ broker จริงก่อนคืนค่า** — broker ที่ล่มอยู่จึงเป็น error ที่ caller ทำอะไรกับมัน
ได้ตั้งแต่ boot ไม่ใช่ goroutine ที่ retry อยู่เงียบๆ ใน log ที่ไม่มีใครอ่านตอน deploy

| กรณี | ผลลัพธ์ |
|---|---|
| สำเร็จ | คืนทันที งานเดินอยู่เบื้องหลัง |
| ยังไม่ได้ลงทะเบียน handler | `400 MQ_CONSUMER_EMPTY` |
| `Start` ซ้ำทั้งที่ยังรันอยู่ | `409 MQ_CONSUMER_RUNNING` |
| ต่อ broker ไม่ได้ / declare topology ไม่ผ่าน | error จาก driver — และ consumer กลับไปสถานะไม่ทำงาน |

**consumer ใช้ connection ของตัวเอง คนละอันกับ publisher** และอ่าน `MQ_*` จาก env
โดยตรง — service ที่มี consumer แต่ไม่ได้ตั้ง `MQ_*` จะ fail ตอน `Start()` ไม่ใช่เงียบๆ
แล้วไม่ได้ยินอะไรเลย

ตอน start มันจะ log ว่ากำลังฟังอะไรอยู่ พร้อม binding — เพราะ "consumer started" เฉยๆ
บอกได้แค่ว่ามีอะไรบางอย่างฟังอยู่ ไม่ได้บอกว่ามันจะ *ได้ยิน* อะไรไหม คิวที่ bind ผิด key
หรือไม่ได้ bind กับ exchange ไหนเลย หน้าตาเหมือนกันทุกประการจากข้างนอกจนกว่าจะพบว่า
message ไม่มา:

```
INFO mq consumer started queues=2 prefetch=20 concurrency=4 handler_timeout=30s
INFO queue consumed queue=orders.shipping exchange=orders exchange_kind=topic binding_keys=[order.created order.paid] dead_letter=orders.dlx
INFO queue consumed queue=orders.legacy topology=external
```

`topology=external` แปลว่าลงทะเบียนด้วย `On` — เราไม่รู้จัก binding ของมัน และมันจะไม่ถูก
สร้างใหม่ให้ตอน reconnect

## Handler

handler ได้ `IMQContext` ซึ่งคือ `IContext` เต็มๆ (logger, database, cache, Sentry, ENV)
บวก `Delivery()` — สร้างใหม่ต่อหนึ่ง message มี timeout ของตัวเอง มี Sentry transaction
ของตัวเอง (`mq <queue>`) และ **panic จบที่ message ไม่ใช่ที่ process**

```go
func handle(ctx core.IMQContext, d *core.Delivery) error {
    order, err := core.BindDelivery[Order](d)      // ถอด JSON body ออกมาเป็น Order
    if err != nil {
        return err
    }

    ctx.Log().Info("shipping", "order", order.ID, "attempt_redelivered", d.Redelivered)

    return repository.New[Shipment](ctx).Create(&Shipment{OrderID: order.ID})
}
```

`Delivery` มีทั้ง body และ metadata ที่ publisher ส่งมา:

| field | ใช้ทำอะไร |
|---|---|
| `Body` | ไบต์ดิบ — `d.Bind(&dest)` หรือ `core.BindDelivery[T](d)` ถอดให้ |
| `RoutingKey` / `Exchange` / `Queue` | มาจากไหน — คิวที่รับหลาย key ใช้ตัวนี้แยกทาง |
| `MessageID` | [idempotency key](./mq-patterns.md#idempotency) |
| `CorrelationID` | ร้อยกลับไปหา request ต้นทางตอนไล่ log |
| `Type` | ชนิด event สำหรับคิวที่รับหลายชนิด |
| `Headers` / `HeaderString(name)` | schema version, tenant, ตัวนับ retry ที่เราใส่เอง |
| `Redelivered` | **broker เคยส่งใบนี้มาแล้ว** — สัญญาณเดียวที่บอกว่ากำลังรันซ้ำ |
| `Timestamp` / `Priority` / `ReplyTo` / `AppID` | ตามชื่อ |

การถอด `Body` ใช้กติกาเดียวกับ cache: `*string`, `*[]byte`, `*json.RawMessage` ได้ไบต์ดิบ
อย่างอื่น JSON-decode

### หนึ่งคิว หลายชนิด event

```go
c.OnQueue(cfg, func(ctx core.IMQContext, d *core.Delivery) error {
    switch d.RoutingKey {
    case "order.created":
        return onCreated(ctx, d)
    case "order.paid":
        return onPaid(ctx, d)
    default:
        // ไม่รู้จัก = ack ทิ้ง ไม่ใช่ error: binding ที่กว้างเกินไปไม่ควรทำให้
        // DLQ เต็มไปด้วย message ที่ไม่มีอะไรผิด
        ctx.Log().Warn("mq: unhandled routing key", "key", d.RoutingKey)
        return nil
    }
})
```

## ack / nack

| handler คืน | ผลลัพธ์ |
|---|---|
| `nil` | **ack** — broker ลบ message ทิ้ง |
| error | **reject ไม่ requeue** → ไป DLX (หรือหายถ้าไม่มี DLX) |
| `core.Requeue(err)` | **reject แล้ว requeue** — broker ส่งกลับมาใหม่ทันที |
| panic | จับได้ กลายเป็น error → เท่ากับกรณี error |

การ requeue ความล้มเหลวถาวรคือวิธีคลาสสิกที่ทำให้ consumer ระเบิด: message กลับมาทันที
fail อีก วนเร็วเท่าที่ broker ส่งไหว พา database ลงไปด้วย — default จึง **ไม่** requeue

ใช้ `Requeue` กับความล้มเหลวที่เกี่ยวกับ *ตอนนี้*:

```go
if errors.Is(err, sql.ErrConnDone) || errors.Is(err, context.DeadlineExceeded) {
    return core.Requeue(err)   // database ล่ม/ช้า — ของเดิมจะสำเร็จเมื่อมันกลับมา
}
return err                     // dead-letter, มีคนไปดู
```

⚠️ **`Requeue` ไม่มีตัวนับและไม่มี backoff** — มันคือ "ส่งกลับมาใหม่เดี๋ยวนี้" ถ้าปัญหา
ยังไม่หายภายในเสี้ยววินาที มันจะวนเป็นสิบครั้งต่อวินาที ต้องการ retry ที่มีระยะห่างจริงๆ
ให้ใช้ [delay queue](./mq-patterns.md#retry-backoff)

ตารางตัดสินใจแบบสั้น:

| ความล้มเหลว | ทำอะไร |
|---|---|
| payload ถอดไม่ได้ / validate ไม่ผ่าน | `return err` — ส่งซ้ำก็พังเหมือนเดิม |
| business rule ปฏิเสธ (order ถูกยกเลิกไปแล้ว) | `return nil` + log — มันไม่ใช่ error และไม่ควรไปนอนใน DLQ |
| database / API ปลายทางล่มชั่วคราว | `Requeue` หรือ delay queue |
| bug ในโค้ดเรา | `return err` แล้วไป fix — DLQ คือที่เก็บหลักฐาน |

## Idempotency

at-least-once แปลว่า handler ต้องทนต่อการถูกเรียกซ้ำด้วย message เดิม `d.Redelivered`
บอกได้ว่า *อาจ* ซ้ำ แต่ไม่ใช่การรับประกัน (consumer คนละตัว, message ที่ republish ใหม่
`Redelivered` เป็น false ได้)

ที่พึ่งพาได้จริงคือ **สถานะฝั่งเรา** — unique constraint, upsert, หรือ
`SetNX` ด้วย `MessageID` — [ดูรูปแบบที่ใช้ได้](./mq-patterns.md#idempotency)

## Prefetch กับ concurrency

```go
app.NewMQConsumer(
    core.WithMQPrefetch(20),      // default 10
    core.WithMQConcurrency(4),    // default 1
)
```

**`WithMQPrefetch`** คือปุ่ม backpressure: broker จะส่ง message ที่ยังไม่ ack ให้ได้มากที่สุด
เท่านี้ น้อยไป consumer รอ network ระหว่าง message มากไป instance เดียวจองงานที่มันทำไม่ทัน
ขณะที่อีกตัวว่าง (แล้วงานก็ไม่กระจาย ถึงจะ scale out ไปแล้วก็ตาม)

**`WithMQConcurrency`** default เป็น **1 ซึ่งรักษาลำดับของ message ในคิว** เพิ่มขึ้นคือแลก
ลำดับกับ throughput — เพิ่มเมื่อ handler เป็นอิสระต่อกันเท่านั้น ("อิสระ" แปลว่า
`order.updated` สองใบของ order เดียวกันสลับลำดับกันแล้วผลลัพธ์ยังถูก)

จุดตั้งต้นที่ใช้ได้: `prefetch ≈ concurrency × 2` ให้มี message รออยู่พอที่ worker ไม่ว่าง
ระหว่างรอ network แต่ไม่มากจนกองอยู่ที่ instance เดียว

| งาน | ตั้งประมาณ |
|---|---|
| I/O เยอะ เร็ว (เขียน database) | concurrency 4–10, prefetch 2× |
| ช้าและหนัก (เรียก API ภายนอก, render) | concurrency 1–4, prefetch เท่าๆ concurrency |
| ต้องรักษาลำดับ | concurrency 1 (และมี consumer instance เดียวต่อคิว) |

ทั้งสองค่าเป็นของ **consumer ทั้งตัว ไม่ใช่ต่อคิว** — คิวที่ลงทะเบียนไว้ด้วยกันแชร์เพดาน
concurrency เดียวกัน คิวที่งานหนักคนละแบบจึงควรแยกเป็นคนละ consumer:

```go
fast := app.NewMQConsumer(core.WithMQConcurrency(10), core.WithMQPrefetch(20))
fast.On("orders.events", onEvent)

slow := app.NewMQConsumer(core.WithMQConcurrency(2), core.WithMQHandlerTimeout(5*time.Minute))
slow.On("orders.reports", onReport)
```

## Handler timeout

`WithMQHandlerTimeout` (default 30 วินาที) ยกเลิก context ของ handler เมื่อหมดเวลา

สิ่งที่เกิดคือ **ctx ถูก cancel** ไม่ใช่ goroutine ถูกฆ่า: query ที่ผูกกับ ctx จะถูกยกเลิก
และ handler ควรคืน error ตามมา แต่โค้ดที่ไม่สนใจ ctx เลย (loop ที่คำนวณอยู่, driver ที่ไม่รับ
ctx) จะทำต่อจนจบ ตัว timeout จึงเป็น "สัญญาณที่ทุกอย่างในเส้นทางควรเคารพ" ไม่ใช่กำแพง

error ที่ตามมาก็เดินตามกติกาปกติ: dead-letter เว้นแต่จะห่อด้วย `Requeue` — งานที่ปกติ
ทำนานกว่านี้ควรเพิ่ม timeout ให้ตรงความจริง แทนที่จะให้ทุกใบ dead-letter ทุกครั้ง

## Broker restart

consumer เชื่อมต่อใหม่เอง — connection ที่หลุดถูกสร้างใหม่หลัง `WithMQReconnectDelay`
(default 2 วินาที) พร้อม **declare topology และเริ่ม consume ใหม่ทั้งหมด**

```go
app.NewMQConsumer(core.WithMQReconnectDelay(5 * time.Second))
```

consumer ที่ไม่ทำแบบนี้คือ consumer ที่เงียบไปหลัง broker restart ครั้งแรก โดยไม่มีอะไรใน
log นอกจากความเงียบ — คิวบวม แล้วสิ่งแรกที่ใครจะรู้คือ alert เรื่องความลึกของคิว

ระหว่างที่หลุด จะเห็นใน log:

```
WARN mq consumer: connection lost, reconnecting err=… in=2s
INFO mq consumer: reconnected
```

message ที่กำลังทำอยู่ตอน connection หลุด **ack ไม่ได้** — broker จะส่งใหม่ให้ consumer
ตัวไหนก็ได้เมื่อกลับมา นี่คือกรณีที่ `Redelivered` เป็นจริงและเป็นเหตุผลข้อสองที่ handler
ต้อง idempotent

## Shutdown

`App` จำ consumer ไว้ตั้งแต่ตอนสร้าง `app.Shutdown()` จึงหยุดมัน **ก่อน** ปิด connection
ที่ handler ใช้อยู่ ไม่ต้องทำอะไรเพิ่ม — และถ้าใช้ [`Runner`](./runner.md) ก็อยู่ในลำดับที่
ถูกต้องอยู่แล้ว

ลำดับตอน stop:

1. ยกเลิก consumer ที่ broker — ไม่มี message ใหม่เข้ามาอีก
2. message ที่ถูกหยิบมาแล้วแต่ยังไม่เริ่มทำ ถูก **nack แบบ requeue** ทันที — ไม่ ack งาน
   ที่ process นี้จะไม่มีวันทำ
3. รอ handler ที่ค้างจนครบ deadline
4. ถึง deadline แล้วปิดทับ — message ที่ยังไม่ ack กลับไปหา broker แทนที่จะถ่วง
   shutdown ไว้

deadline มาจาก `WithDrainTimeout` ของ Runner (หรือ context ที่ส่งให้ `Stop`) ตั้งให้
**ต่ำกว่า** `terminationGracePeriodSeconds` ของ Kubernetes ไม่งั้น process ถูกฆ่ากลางคัน
และลำดับทั้งหมดนี้ก็ไม่เคยได้เกิด

จะหยุดเองก็ได้ (rolling deploy ที่อยากหยุดรับงานก่อน แต่ยังเสิร์ฟ HTTP ต่อ):

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
if err := consumer.Stop(ctx); err != nil {
    log.Error("consumer did not drain in time", "err", err)
}
```

`Stop` เรียกซ้ำได้ปลอดภัย และ `Running()` บอกสถานะปัจจุบัน

## Observability

ทุก message ได้:

- **Sentry transaction** ชื่อ `mq <queue>` op `queue.process` พร้อม tag
  `mode=mq`, `queue`, `routing_key`
- **log บรรทัดเดียวตอนสำเร็จ** (`debug`): `mq message handled` พร้อม `took`
- **log บรรทัดเดียวตอนล้มเหลว** (`error`): `mq handler failed` พร้อม `queue`,
  `routing_key`, `message_id`, `redelivered`, `requeued`, `took`, `err`

บรรทัด error นั้น**คือ**สิ่งที่รายงานเข้า Sentry (logger มี bridge อยู่) — handler จึงไม่ควร
log error ของตัวเองซ้ำอีกก่อน return ไม่งั้นหนึ่งเหตุการณ์จะกลายเป็นสองใบ ดู
[Logging practices](./logging-practices.md)

สิ่งที่ควร monitor ฝั่ง ops คือ **ความลึกของคิว** ไม่ใช่ error rate อย่างเดียว: consumer ที่
ทำงานช้ากว่าที่ message เข้ามา ไม่มี error ให้เห็นเลยจนกว่าจะสาย —
[วิธี export](./mq-topology.md#เครื่องมือฝั่ง-ops)
