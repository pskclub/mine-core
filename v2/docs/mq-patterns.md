# Best Practices & Recipes

รวมสิ่งที่ต้องออกแบบเอง เพราะ broker ไม่ได้ให้มา: retry ที่มีระยะห่าง, การกันงานซ้ำ,
DLQ ที่ใช้งานได้จริง, การเปลี่ยน schema และการทดสอบ

## กฎสิบข้อ

| # | กฎ | ทำไม |
|---|---|---|
| 1 | handler ต้อง **idempotent** | at-least-once ไม่ใช่ทางเลือก มันคือสิ่งที่ได้มา |
| 2 | ทุกคิวที่สำคัญต้องมี **DLX + คิวปลายทางที่ bind จริง** | ไม่งั้นความล้มเหลวคือความเงียบ |
| 3 | **publish หลัง commit** เสมอ | consumer แข่งชนะ transaction ได้ |
| 4 | อย่า `Requeue` ความล้มเหลวถาวร | มันคือ busy loop ที่พา database ลงไปด้วย |
| 5 | payload ข้าม service = **API สาธารณะ** | เติมได้ ลบไม่ได้ |
| 6 | routing key เป็นอดีตกาล (`order.created`) | คำสั่งผูก publisher เข้ากับ consumer |
| 7 | **monitor ความลึกของคิว** ไม่ใช่แค่ error rate | consumer ที่ช้าไม่มี error ให้เห็น |
| 8 | concurrency > 1 = ยอมสละลำดับ | ตัดสินใจตอนออกแบบ ไม่ใช่ตอน incident |
| 9 | อย่าใส่ความลับดิบลง payload | message ค้างอยู่ในคิวได้เป็นวัน และอ่านได้จาก UI |
| 10 | consumer เป็นคนประกาศคิวของตัวเอง | คิวเป็นของคนที่อ่านมัน |

## Idempotency

ลำดับความน่าเชื่อถือ จากมากไปน้อย:

**1 · ให้ database เป็นคนกัน** — ดีที่สุด เพราะมันคือความจริงเดียวกับที่งานเขียนลงไป
unique index บน `order_id` บวก `ON CONFLICT DO NOTHING` ทำให้ message ใบที่สอง
กลายเป็น no-op ในหนึ่ง statement ที่ไม่มี race:

```go
err := repository.New[Shipment](ctx).DB().
    Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "order_id"}}, DoNothing: true}).
    Create(&Shipment{OrderID: order.ID}).Error
```

**2 · upsert หรือเขียนแบบมีเงื่อนไข** — เมื่อการทำซ้ำควรได้ผลลัพธ์เดิม ไม่ใช่ error

```go
// อัปเดตเฉพาะเมื่อ event ใหม่กว่าสิ่งที่บันทึกไว้ — ทนต่อการสลับลำดับด้วย
repository.New[Order](ctx).
    Where("id = ? AND updated_at < ?", ev.ID, ev.UpdatedAt).
    Updates(map[string]any{"status": ev.Status, "updated_at": ev.UpdatedAt})
```

**3 · `SetNX` ด้วย `MessageID`** — เมื่อผลลัพธ์ไม่ได้ลง database ของเรา (ส่งเมล, เรียก API
ภายนอก)

```go
fresh, err := ctx.Cache().WithPrefix("mq:seen").SetNX(d.MessageID, 1, 24*time.Hour)
if err != nil {
    return core.Requeue(err)     // cache ตอบไม่ได้ ยังไม่ควรเสี่ยงทำซ้ำ
}
if !fresh {
    return nil                   // เคยเห็นใบนี้แล้ว
}
if err := mailer.Send(ctx, mail); err != nil {
    ctx.Cache().WithPrefix("mq:seen").Del(d.MessageID)   // ไม่สำเร็จ ปล่อยให้ลองใหม่ได้
    return err
}
return nil
```

ข้อควรรู้: cache ที่ไม่ได้ตั้งค่าไว้ **degrade เงียบๆ** ตามดีไซน์ของมัน วิธีนี้จึงเป็น
best-effort เสมอ — สิ่งที่ห้ามทำซ้ำจริงๆ ต้องมีร่องรอยใน database ไม่ใช่ใน cache

`MessageID` ต้องมาจาก **ตัวตนของงาน** (`order.ID`, `payment.ID+attempt`) ไม่ใช่ UUID ที่
publisher สุ่มใหม่ทุกครั้งที่ retry — ไม่งั้นสองใบที่เป็นงานเดียวกันจะมีคนละ id และ dedupe
ไม่เจอกัน

## Retry ที่ไม่ทำ consumer ระเบิด {#retry-backoff}

`core.Requeue` = "ส่งกลับมาใหม่เดี๋ยวนี้" ไม่มีตัวนับ ไม่มี backoff ดีสำหรับสะดุดสั้นๆ
ไม่ดีสำหรับ API ปลายทางที่ล่มไปสิบนาที

รูปแบบที่ใช้ได้จริงคือ **delay queue**: คิวที่ไม่มีใคร consume มี TTL และ dead-letter กลับ
เข้า exchange เดิม — message นอนครบเวลาแล้วเด้งกลับมาเอง

```
orders ──▶ orders.shipping ──(fail)──▶ [handler republish] ──▶ orders.retry.30s
                  ▲                                                    │ TTL 30s
                  └────────────────── DLX กลับเข้า orders ◀────────────┘
```

ประกาศครั้งเดียวตอน boot:

```go
mq := app.MQ()

mq.DeclareExchange(core.ExchangeConfig{Name: "orders.retry", Kind: core.ExchangeTopic})

for _, d := range []struct {
    name  string
    delay time.Duration
}{{"orders.retry.10s", 10 * time.Second}, {"orders.retry.1m", time.Minute}, {"orders.retry.10m", 10 * time.Minute}} {
    if _, err := mq.DeclareQueue(core.QueueConfig{
        Name:               d.name,
        TTL:                d.delay,
        DeadLetterExchange: "orders",   // หมดเวลาแล้วเด้งกลับเข้า exchange เดิม ด้วย key เดิม
    }); err != nil {
        return err
    }
    if err := mq.BindQueue(d.name, "orders.retry", d.name); err != nil {
        return err
    }
}
```

ฝั่ง handler:

```go
const maxAttempts = 5

func handle(ctx core.IMQContext, d *core.Delivery) error {
    if err := doWork(ctx, d); err != nil {
        if !transient(err) {
            return err                       // ถาวร → DLQ ทันที ไม่ต้องเสียเวลา retry
        }
        return scheduleRetry(ctx, d, err)
    }
    return nil
}

func scheduleRetry(ctx core.IMQContext, d *core.Delivery, cause error) error {
    attempt := attemptOf(d) + 1
    if attempt > maxAttempts {
        return cause                         // หมดสิทธิ์ → DLQ พร้อม log
    }

    delay := []string{"orders.retry.10s", "orders.retry.1m", "orders.retry.10m"}
    queue := delay[min(attempt-1, len(delay)-1)]

    ctx.Log().Warn("mq: retrying", "attempt", attempt, "in", queue, "err", cause)

    // ack ใบเดิม (โดยการคืน nil) แล้วส่งสำเนาเข้า delay queue
    return ctx.MQ().PublishWith("orders.retry", queue, d.Body, core.PublishOptions{
        ContentType:   d.ContentType,        // d.Body เป็น []byte — ไม่ระบุจะกลายเป็น octet-stream
        MessageID:     d.MessageID,          // คงตัวตนไว้ ไม่งั้น dedupe พัง
        CorrelationID: d.CorrelationID,
        Type:          d.Type,
        Headers:       withAttempt(d.Headers, attempt),
    })
}

// นับความพยายามด้วย header ของเราเอง: x-death ที่ broker ใส่ให้อ่านได้ก็จริง
// แต่ต้องแตะ type ของ amqp091 โดยตรง ซึ่งเป็นสิ่งที่ทั้ง layer นี้ตั้งใจกันไว้
func attemptOf(d *core.Delivery) int {
    if v, ok := d.Headers["x-attempt"]; ok {
        switch n := v.(type) {
        case int32:
            return int(n)
        case int64:
            return int(n)
        case int:
            return n
        }
    }
    return 0
}

func withAttempt(h map[string]any, n int) map[string]any {
    out := map[string]any{}
    for k, v := range h {
        out[k] = v
    }
    out["x-attempt"] = n
    return out
}
```

สามอย่างที่รูปแบบนี้ซื้อมาให้:

- **ระยะห่างจริง** — consumer ไม่วน busy loop และปลายทางที่ล่มได้เวลาฟื้น
- **ตัวนับที่เชื่อถือได้** — เก็บใน header ของเราเอง ไม่ต้องพึ่ง `x-death` ที่อ่านยาก
- **DLQ ที่มีความหมาย** — สิ่งที่ตกไปที่นั่นคือสิ่งที่ retry จนหมดแล้วจริงๆ

⚠️ **หนึ่ง delay ต่อหนึ่งคิว** อย่าใช้ `PublishOptions.Expiration` ค่าต่างกันในคิวเดียว:
RabbitMQ ลบ message ที่หมดอายุตอนที่มันขึ้นถึงหัวคิวเท่านั้น ใบที่ TTL สั้นกว่าแต่อยู่หลัง
ใบที่ TTL ยาว จะติดอยู่จนใบหน้าหมดอายุ (head-of-line blocking)

## Dead-letter queue ที่ใช้งานได้จริง

DLQ ที่ไม่มีใครดูคือ `/dev/null` ที่กินดิสก์ ทำสามอย่างนี้ให้ครบ:

**1 · bind คิวจริงไว้กับ DLX** — [ดูวิธี](./mq-topology.md#dead-letter-exchange)

**2 · alert ที่ความลึกของมัน** ไม่ใช่ที่ error rate

```go
_ = reg.Register(core.JobDef{
    Name: "mq-dlq-watch", Schedule: core.Cron("*/5 * * * *"),
}, func(ctx core.ICronjobContext) error {
    info, err := ctx.MQ().QueueInfo("orders.dead")
    if err != nil {
        return err
    }
    if info.Messages > 0 {
        // log ระดับ error = ขึ้น Sentry ผ่าน bridge ของ logger
        ctx.Log().Error("mq: dead letters waiting", "queue", "orders.dead", "count", info.Messages)
    }
    return nil
})
```

**3 · มีวิธี replay** — consumer แยกตัวหนึ่งที่อ่าน DLQ แล้วส่งกลับเข้า exchange เดิม
เปิดใช้ตอนที่แก้ต้นเหตุแล้วเท่านั้น

```go
replay := app.NewMQConsumer(core.WithMQConcurrency(1), core.WithMQConsumerTag("dlq-replay"))
replay.On("orders.dead", func(ctx core.IMQContext, d *core.Delivery) error {
    // ⚠️ ต้องระบุ exchange ปลายทางเอง ห้ามใช้ d.Exchange — message ที่ถูก
    // dead-letter รายงาน exchange ที่มันถูกส่งเข้า *ครั้งล่าสุด* ซึ่งคือ DLX
    // การ republish เข้า d.Exchange จึงวนกลับมาที่คิว dead เดิมไม่รู้จบ
    return ctx.MQ().PublishWith("orders", d.RoutingKey, d.Body, core.PublishOptions{
        ContentType: d.ContentType,
        MessageID:   d.MessageID,
        Headers:     withAttempt(d.Headers, 0),   // เริ่มนับใหม่
    })
})
// อย่า Start() ทิ้งไว้ตลอด — ตั้ง flag ใน ENV แล้วเปิดเฉพาะตอนที่ตั้งใจ replay
if env.Bool("DLQ_REPLAY") {
    _ = replay.Start()
}
```

⚠️ replay ทั้งคิวโดยไม่แก้ต้นเหตุก่อน = message กลับมาลง DLQ อีกรอบพร้อมภาระอีกชุด
อ่านหนึ่งใบ ดูให้เข้าใจ แล้วค่อย replay

## Transactional outbox

`Publish` หลัง commit ยังเหลือช่องอยู่: commit ผ่าน → process ตาย → ไม่มีใครรู้ว่า order
เกิดขึ้นแล้ว ถ้าช่องนั้นรับไม่ได้ ให้ **เขียน message ลง database ใน transaction เดียวกัน**
แล้วส่งทีหลัง

```go
// 1. ใน transaction เดียวกับงานจริง
err := repo.Transaction(func(tx *gorm.DB) error {
    if err := repository.NewWithDB[Order](ctx, tx).Create(&order); err != nil {
        return err
    }
    return repository.NewWithDB[Outbox](ctx, tx).Create(&Outbox{
        Exchange:   "orders",
        RoutingKey: "order.created",
        Payload:    payload,       // JSON
        MessageID:  order.ID,
    })
})

// 2. job ที่รันถี่ๆ เป็นคนส่ง — ทำงานซ้ำได้ ไม่มีอะไรหาย
_ = reg.Register(core.JobDef{
    Name: "outbox-drain", Schedule: core.Every(5 * time.Second),
}, func(ctx core.ICronjobContext) error {
    rows, err := repository.New[Outbox](ctx).
        Where("sent_at IS NULL").Order("id").Limit(100).FindAll()
    if err != nil {
        return err
    }
    for _, row := range rows {
        if err := ctx.MQ().PublishWith(row.Exchange, row.RoutingKey, row.Payload,
            core.PublishOptions{ContentType: "application/json", MessageID: row.MessageID}); err != nil {
            return err        // ค้างไว้ รอบหน้าเอาใหม่ ไม่มีอะไรหาย
        }
        if err := repository.New[Outbox](ctx).
            Where("id = ?", row.ID).Update("sent_at", time.Now()); err != nil {
            return err
        }
    }
    return nil
})
```

ราคาที่จ่าย: ตารางเพิ่มหนึ่งตาราง, latency เพิ่มตามรอบของ job, และ **ส่งซ้ำได้** (ส่งสำเร็จ
แล้วอัปเดต `sent_at` ไม่ทัน) — ซึ่งวนกลับไปที่กฎข้อ 1: consumer ต้อง idempotent อยู่ดี

ใช้เมื่อ message นั้นคือเงินหรือคือสัญญากับ service อื่น ไม่ใช่กับทุก event

## Ordering

RabbitMQ รักษาลำดับ **ต่อหนึ่งคิว ต่อหนึ่ง consumer ที่ทำทีละใบ** เท่านั้น สิ่งที่ทำลาย
ลำดับ: `WithMQConcurrency(n>1)`, consumer หลาย instance, requeue, และ retry ผ่าน
delay queue

ทางที่มักจะถูกกว่าการพยายามรักษาลำดับคือ **ทำให้ handler ไม่แคร์ลำดับ**:

```go
// เขียนทับเฉพาะเมื่อ event ใหม่กว่าที่มีอยู่ — ใบเก่าที่มาช้าจะไม่ทำอะไร
Where("id = ? AND version < ?", ev.ID, ev.Version).Updates(...)
```

ถ้าจำเป็นต้องเรียงจริงๆ ให้เรียงเฉพาะ *ในขอบเขตของ entity* ด้วยการแยกคิวตาม key
(`orders.shipping.shard-0..n` bind ด้วย routing key ที่มี shard ของ order id) แล้วให้แต่ละ
shard มี consumer เดียว concurrency 1 — ราคาคือ topology ที่ซับซ้อนขึ้นและ scale ได้เป็น
ขั้นเท่านั้น

## เปลี่ยน schema ของ message

payload ที่ข้าม service คือ contract ที่ deploy ไม่พร้อมกัน — จะมีช่วงที่ publisher เวอร์ชัน
ใหม่ส่งให้ consumer เวอร์ชันเก่าเสมอ

| ทำได้ | ทำไม่ได้ |
|---|---|
| เพิ่ม field ที่มี default | ลบหรือเปลี่ยนชื่อ field |
| เพิ่ม routing key ใหม่ | เปลี่ยนความหมายของ field เดิม |
| เพิ่ม event ชนิดใหม่ | เปลี่ยน type (string → int) |

การเปลี่ยนแบบ breaking ทำเป็นสองขั้น: **ส่ง key ใหม่คู่กับของเดิมไปก่อน** (`order.created`
+ `order.created.v2`) → ให้ consumer ทุกเจ้าย้ายไป v2 → ค่อยเลิกส่งของเดิม

ติด version ไว้ใน header ตั้งแต่วันแรก จะได้มีที่ให้ยืนตอนต้องแตกทาง:

```go
ctx.MQ().PublishWith("orders", "order.created", order, core.PublishOptions{
    Headers: map[string]any{"schema": 2},
})

// ฝั่ง consumer
if v, _ := d.Headers["schema"].(int32); v > 2 {
    return nil   // ใหม่เกินกว่าที่เรารู้จัก — ack ทิ้งดีกว่าถอดผิดแล้วเขียนข้อมูลเสีย
}
```

## Testing

### Unit — ไม่ต้องมี broker

`IMQ` เป็น interface ธรรมดา service ที่รับ `IContext` จึงทดสอบได้ด้วยตัวปลอมเล็กๆ ที่
บันทึกสิ่งที่ถูก publish (v2 ยังไม่มี in-memory MQ มาให้เหมือน cache/storage — เขียนเองสิบ
บรรทัดตามที่ test ต้องรู้จริงๆ):

```go
type recordingMQ struct {
    core.IMQ                       // embed noop ไว้ เมธอดที่ไม่ได้ใช้จะ fail ชัดเจน
    published []published
}

type published struct {
    Exchange, Key string
    Body          any
}

func (m *recordingMQ) Publish(exchange, key string, msg any) core.IError {
    m.published = append(m.published, published{exchange, key, msg})
    return nil
}

func TestCreateOrder_publishesEvent(t *testing.T) {
    mq := &recordingMQ{IMQ: core.NewNoopMQ()}
    app, err := core.NewApp(env, core.WithMQ(mq))
    require.NoError(t, err)

    // …เรียก service…

    require.Len(t, mq.published, 1, "การสร้าง order ต้องประกาศให้ระบบอื่นรู้หนึ่งครั้ง")
    assert.Equal(t, "order.created", mq.published[0].Key)
}
```

ฝั่ง handler ทดสอบได้ตรงๆ เพราะมันคือฟังก์ชันที่รับ context กับ delivery:

```go
// IMQContext = IContext + Delivery() — ประกอบเองได้ในสามบรรทัด
type mqCtx struct {
    core.IContext
    d *core.Delivery
}

func (c mqCtx) Delivery() *core.Delivery { return c.d }

func TestHandle_ackOnValidOrder(t *testing.T) {
    d := &core.Delivery{
        Body:       []byte(`{"id":"o1"}`),
        RoutingKey: "order.created",
        MessageID:  "o1",
    }

    err := handle(mqCtx{IContext: ctx, d: d}, d)
    require.NoError(t, err, "order ที่ถูกต้องต้อง ack ไม่ใช่ตกไป dead-letter")
}
```

เคสที่คุ้มที่สุดในการเขียน test: **payload พังต้องคืน error** (ไป DLQ), **ข้อผิดพลาด
ชั่วคราวต้องคืน `Requeue`** และ **เรียกซ้ำด้วย delivery เดิมสองครั้งต้องได้ผลลัพธ์เดียวกัน**

### Integration — ต้องมี broker จริง

reconnect, DLQ routing, prefetch และ TTL ทดสอบด้วยของปลอมไม่ได้เลย ใส่ไว้หลัง build tag
`integration` แล้วชี้ไปที่ RabbitMQ จริง:

```go
//go:build integration
```

```sh
APP_MQ_CONNECTION_STRING=amqp://guest:guest@127.0.0.1:5672/ make test-integration
```

สิ่งที่ควรมี test ระดับนี้: message เข้าคิวถูกใบตาม binding, handler ที่คืน error ไปโผล่ที่
DLQ, และ consumer กลับมาทำงานเองหลัง broker restart — สามอย่างนี้คือสิ่งที่พังเงียบที่สุด
ใน production ดู [Integration tests](./testing-integration.md)

## Checklist ก่อนขึ้น production

- [ ] ทุกคิวมี DLX และ DLX นั้นมีคิวที่ bind อยู่จริง
- [ ] มี alert ที่ความลึกของคิวหลักและของ DLQ
- [ ] handler idempotent และมี test ที่พิสูจน์ว่ารันซ้ำแล้วผลไม่เปลี่ยน
- [ ] retry มีเพดานและมีระยะห่าง ไม่ใช่ `Requeue` เปล่าๆ
- [ ] publish อยู่หลัง commit (หรือมี outbox)
- [ ] `WithMQHandlerTimeout` ยาวกว่างานที่ช้าที่สุดจริงๆ
- [ ] drain timeout ของ Runner ต่ำกว่า grace period ของ orchestrator
- [ ] readiness probe ใช้ `Ping()` ส่วน liveness ไม่ใช้
- [ ] คิวที่งานหนักคนละแบบ แยกเป็นคนละ consumer
- [ ] ตัดสินใจแล้วว่าคิวนี้ต้องการลำดับหรือไม่ และ concurrency ตั้งไว้สอดคล้องกัน
