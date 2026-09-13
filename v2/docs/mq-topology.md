# Topology

exchange, queue และ binding คือสิ่งที่ตัดสินว่า message ที่ publish สำเร็จจะ *ไปถึงใคร*
publisher ไม่รู้เรื่องนี้เลย — มันส่งเข้า exchange แล้วจบ

```
publish("orders", "order.created")
        │
        ▼
   exchange "orders"  ──ตาม binding key──┬──▶ queue "orders.shipping"   ("order.*")
                                         └──▶ queue "orders.analytics"  ("#")
```

ไม่มี binding ที่ตรง = message ถูก confirm แล้วทิ้ง เงียบๆ นี่คือความล้มเหลวที่พบบ่อยที่สุด
ของ RabbitMQ และเป็นเหตุผลที่ทั้งหน้านี้มีอยู่

## ใครควรเป็นคนประกาศ

ประกาศตอน **boot ของ service เอง** จะได้ไม่ต้องพึ่งว่ามีใครรัน script หรือกดใน
management UI ไว้ก่อน — deploy คือสิ่งที่สร้าง topology

| ใคร | ควรประกาศอะไร |
|---|---|
| **consumer** | queue ของตัวเอง + binding ของตัวเอง + DLX ของตัวเอง — ผ่าน `OnQueue` |
| **publisher** | เฉพาะ exchange ที่ตัวเองส่งเข้า |

**publisher ไม่ควรประกาศ queue ของ consumer** — คิวเป็นของคนที่อ่านมัน service ที่
ประกาศคิวให้คนอื่นคือ service ที่ต้อง deploy ใหม่ทุกครั้งที่ consumer เปลี่ยน binding และ
คือคิวที่ค้างอยู่ต่อไปหลัง consumer ถูกลบไปแล้ว

```go
// ฝั่ง publisher: รู้จักแค่ exchange
if err := app.MQ().DeclareExchange(core.ExchangeConfig{
    Name: "orders", Kind: core.ExchangeTopic,
}); err != nil {
    return err
}

// ฝั่ง consumer: ประกาศครบชุดของตัวเอง ตอน Start และตอน reconnect ทุกครั้ง
consumer.OnQueue(core.ConsumeQueue{
    Queue:       core.QueueConfig{Name: "orders.shipping", DeadLetterExchange: "orders.dlx"},
    Exchange:    &core.ExchangeConfig{Name: "orders", Kind: core.ExchangeTopic},
    BindingKeys: []string{"order.created", "order.paid"},
}, handler)
```

ประกาศซ้ำด้วยค่าเดิมไม่มีผลอะไร (idempotent) — ทั้งสองฝั่งประกาศ exchange เดียวกันได้
ขอแค่ `Kind` กับ durability ตรงกัน

## Exchange

```go
app.MQ().DeclareExchange(core.ExchangeConfig{
    Name: "orders",
    Kind: core.ExchangeTopic,
})
```

| `Kind` | routing | ใช้เมื่อ |
|---|---|---|
| `ExchangeTopic` | key แบบมี pattern (`order.*`, `#`) | **default ที่ควรเลือก** — เพิ่ม consumer ใหม่ทีหลังได้โดยไม่แตะ publisher |
| `ExchangeDirect` | key ตรงตัวเป๊ะ | routing แบบ 1:1 ที่รู้ปลายทางแน่นอน |
| `ExchangeFanout` | ทุก queue ที่ bind ไว้ ไม่สนใจ key | broadcast ให้ทุก consumer จริงๆ |
| `ExchangeHeaders` | match จาก headers แทน key | นานๆ ใช้ที — routing ที่ key เดียวไม่พอ |

topic ครอบคลุมทั้ง direct (key เป๊ะ) และ fanout (`#`) อยู่แล้ว ถ้าไม่มีเหตุผลชัดเจนให้
เลือกอันอื่น เลือก topic

| field | ค่า default | หมายเหตุ |
|---|---|---|
| `Transient` | `false` = **durable** | zero value คือ durable เพราะ exchange ที่หายตอน broker restart แทบไม่เคยเป็นสิ่งที่ต้องการ |
| `AutoDelete` | `false` | ลบตัวเองเมื่อ binding สุดท้ายหายไป |
| `Internal` | `false` | ห้าม publish เข้าโดยตรง — ใช้เป็นปลายทางของ exchange-to-exchange binding เท่านั้น |
| `Args` | – | argument ดิบ ส่งตรงถึง broker |

### exchange พิเศษ: `""`

exchange ชื่อว่างคือ default exchange ที่ RabbitMQ ให้มา ซึ่ง route ด้วย "ชื่อคิว" ตรงๆ:

```go
ctx.MQ().Publish("", "orders.shipping", order)   // ตรงเข้าคิวชื่อนั้น
```

สะดวกสำหรับสคริปต์และ test แต่ **ไม่เหมาะกับ production**: publisher ผูกกับชื่อคิวของ
consumer ทันที และวันที่มี consumer เจ้าที่สองอยากได้ message เดียวกัน ต้องแก้ publisher

## Queue

```go
info, err := app.MQ().DeclareQueue(core.QueueConfig{
    Name:               "orders.shipping",
    DeadLetterExchange: "orders.dlx",
    TTL:                24 * time.Hour,
    MaxLength:          100_000,
    Quorum:             true,
})
// info.Messages / info.Consumers = สถานะ ณ ตอนประกาศ
```

| field | ผล |
|---|---|
| `Transient` | `false` (default) = queue อยู่รอด broker restart |
| `AutoDelete` | ลบคิวเมื่อ consumer ตัวสุดท้ายหลุด |
| `Exclusive` | คิวเป็นของ connection นี้เท่านั้น และหายไปเมื่อปิด — สำหรับคิวชั่วคราวเฉพาะ instance |
| `TTL` | message ที่รอนานเกินนี้ถูกทิ้ง (หรือ dead-letter ถ้ามี DLX) |
| `MaxLength` / `MaxBytes` | เพดานคิว เกินแล้วตัวเก่าสุดโดนทิ้งก่อน (หรือ dead-letter) |
| `MaxPriority` | 1–255 ทำให้เป็น priority queue — ต้องมีค่านี้ `PublishOptions.Priority` ถึงจะมีความหมาย |
| `DeadLetterExchange` | ปลายทางของ message ที่ถูก reject / หมดอายุ / ล้นคิว |
| `DeadLetterRoutingKey` | เปลี่ยน routing key ตอน dead-letter (default: ใช้ key เดิม) |
| `Quorum` | quorum queue — replicate ข้าม node แลกกับ throughput |
| `Args` | argument ดิบ merge ทับค่าข้างบน |

### Dead-letter exchange

⚠️ **ประกาศ DLX ให้ทุก queue ที่ความล้มเหลวของมันมีความหมาย**

ไม่มี DLX แปลว่า message ที่ consumer reject **หายไปเฉยๆ** — ไม่มี log ของ broker
ไม่มีที่ให้ไปดู ไม่มีทางเล่นซ้ำ ค่าใช้จ่ายของการมี DLX คือคิวเปล่าอีกหนึ่งคิว ค่าใช้จ่ายของ
การไม่มีคือคำถาม "order นี้หายไปไหน" ที่ตอบไม่ได้

```go
// DLX + คิวที่รองรับมันจริงๆ — DLX ที่ไม่มีคิว bind อยู่ ทิ้ง message เหมือนไม่มี DLX
app.MQ().DeclareExchange(core.ExchangeConfig{Name: "orders.dlx", Kind: core.ExchangeTopic})
app.MQ().DeclareQueue(core.QueueConfig{
    Name: "orders.dead",
    TTL:  14 * 24 * time.Hour,      // ให้เวลาไปดูสองสัปดาห์ ไม่ใช่เก็บไว้ตลอดกาล
})
app.MQ().BindQueue("orders.dead", "orders.dlx", "#")
```

**DLX ที่ไม่มีใคร bind = ถังขยะ** ประกาศคู่กันเสมอ และตั้ง alert ที่ความลึกของคิว dead —
[ดูวิธีใช้งานจริง](./mq-patterns.md#dead-letter-queue-ที่ใช้งานได้จริง)

### Quorum queue

`Quorum: true` ทำให้คิว replicate ข้าม node — ทนต่อการเสีย node ได้ แลกกับ throughput
ที่ต่ำลงและ memory ที่มากขึ้น

ใช้กับคิวที่ "message หายหนึ่งใบ = มีคนต้องมานั่งไล่หา" (payment, order, งานที่มีเงิน
เกี่ยวข้อง) ไม่ใช่กับคิวของ notification ที่ตกไปหนึ่งใบแล้วไม่มีใครเดือดร้อน

ข้อจำกัดที่ควรรู้: quorum queue ไม่รองรับ `MaxPriority` และไม่รองรับ `Exclusive`

## Bindings

```go
mq.BindQueue("orders.shipping", "orders", "order.created")
mq.BindQueue("orders.shipping", "orders", "order.paid")     // หนึ่งคิว หลาย key ได้
mq.UnbindQueue("orders.shipping", "orders", "order.paid")
```

pattern ของ topic exchange:

| key | จับ |
|---|---|
| `order.created` | เป๊ะๆ อันเดียว |
| `order.*` | หนึ่งคำต่อท้าย — `order.created`, `order.paid` แต่ไม่ใช่ `order.item.added` |
| `order.#` | ศูนย์คำขึ้นไป — `order.item.added` ด้วย |
| `#` | ทุกอย่างใน exchange นั้น |

### ตั้งชื่อ

```
exchange   orders                 ชื่อ domain
routing    order.created          entity.past-tense-verb
queue      orders.shipping        <domain>.<ใครกิน> — ไม่ใช่ <ทำอะไร>
DLX        orders.dlx
```

- **routing key เป็นอดีตกาล** — `order.created` คือคำบอกเล่าที่ใครจะทำอะไรกับมันก็ได้
  ส่วน `send.email` คือคำสั่งที่ผูก publisher เข้ากับสิ่งที่ consumer ต้องทำ วันที่เพิ่ม
  consumer เจ้าที่สอง ชื่อแบบแรกยังใช้ได้ ชื่อแบบหลังจะเริ่มโกหก
- **ชื่อคิวบอกว่าใครกิน** — `orders.shipping` (คิวของ shipping service) ไม่ใช่
  `orders.send-label` เพราะสิ่งที่ consumer ทำเปลี่ยนได้ ส่วนตัวตนของมันไม่ค่อยเปลี่ยน
- **อย่าใส่ id ลงใน routing key** (`order.42.created`) — consumer ทุกตัวต้องใช้ pattern
  ทันที และ id ควรอยู่ใน payload ที่ type ได้

## เปลี่ยน topology ที่ประกาศไปแล้ว

ประกาศ queue/exchange ที่มีอยู่แล้วด้วย setting ที่ต่างไป จะได้ **error จาก broker**
(`PRECONDITION_FAILED`) ซึ่งเป็นเรื่องดี: มันจับ topology ที่เปลี่ยนในโค้ดแล้วแต่ของจริง
ยังเป็นของเดิม แทนที่จะปล่อยให้สอง environment ต่างกันเงียบๆ

ผลคือ **argument ของคิว (TTL, DLX, quorum, max-length) แก้ทีหลังไม่ได้** ทางเลือกมีสาม:

| วิธี | เมื่อ |
|---|---|
| ตั้งชื่อคิวใหม่ (`orders.shipping.v2`) แล้วย้าย binding | ทำได้เสมอ ไม่มี downtime — คิวเก่าค่อยลบเมื่อว่าง |
| ใช้ [policy](https://www.rabbitmq.com/parameters.html#policies) ของ RabbitMQ ฝั่ง ops | ค่าที่ policy ทำได้ (TTL, DLX, length) แก้ได้โดยไม่ต้องแตะโค้ด |
| `DeleteQueue` แล้วประกาศใหม่ | เฉพาะตอนที่คิวว่างและยอมเสีย message ที่ค้างได้ |

ทางที่หลีกเลี่ยง: ใส่ `Args` ทับให้มันเงียบ — นั่นคือการปิดเสียงสัญญาณที่กำลังบอกว่า
production กับโค้ดไม่ตรงกัน

## เครื่องมือฝั่ง ops

```go
info, err := mq.QueueInfo("orders.shipping")   // ลึกแค่ไหน มี consumer กี่ตัว
n, err := mq.PurgeQueue("orders.shipping")     // ล้างคิว คืนจำนวนที่ทิ้ง
err = mq.DeleteQueue("orders.legacy")          // ลบคิวและทุกอย่างในนั้น
```

`QueueInfo` ใช้ passive declare — **ไม่สร้างอะไร** และ error ถ้าคิวไม่มีอยู่ จึงใช้เป็น
metric/probe ได้โดยไม่เผลอสร้างคิวผีจากชื่อที่พิมพ์ผิด

```go
// export ความลึกของคิวเป็น metric — สัญญาณที่บอกได้เร็วที่สุดว่า consumer ตามไม่ทัน
info, err := app.MQ().QueueInfo("orders.shipping")
if err == nil {
    queueDepth.WithLabelValues("orders.shipping").Set(float64(info.Messages))
}
```

`PurgeQueue` กับ `DeleteQueue` ทิ้ง message จริงและกู้คืนไม่ได้ — ใน production ควรอยู่
หลัง flag หรืออยู่ในสคริปต์ที่ตั้งใจรัน ไม่ใช่ในเส้นทาง boot
