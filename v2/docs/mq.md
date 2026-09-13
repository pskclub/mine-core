# Message Queue (RabbitMQ)

RabbitMQ ผ่าน `amqp091` มาเป็นสองฝั่ง:

- **`core.IMQ`** — publisher `ctx.MQ()` คืน handle ที่ผูกกับ context ของ request แล้ว
  จึงไม่ต้องส่ง `ctx` ต่อ
- **`core.IMQConsumer`** — consumer ที่แปลง "คิว" ให้เป็น "handler" แบบเดียวกับที่
  HTTP server แปลง route ให้เป็น handler พร้อม context ต่อ message, timeout,
  panic recovery, reconnect และ graceful shutdown

```go
// ฝั่งส่ง — คืน nil ก็ต่อเมื่อ broker ยืนยันว่ารับ message ไว้แล้ว
ctx.MQ().Publish("orders", "order.created", order)

// ฝั่งรับ — คืน nil = ack, คืน error = dead-letter
c.OnQueue(cfg, func(ctx core.IMQContext, d *core.Delivery) error { … })
```

## What this section covers

| Page | |
|---|---|
| [Connection & Config](./mq-connection.md) | ต่อ broker, options, readiness, lifecycle, service ที่ไม่มี broker |
| [Topology](./mq-topology.md) | exchange / queue / binding, DLX, quorum, ใครควรเป็นคนประกาศ |
| [Publishing](./mq-publishing.md) | payload, `PublishOptions`, confirms, error ที่เป็นไปได้, publish หลัง commit |
| [Consumers](./mq-consumer.md) | handler, ack/nack, prefetch/concurrency, reconnect, shutdown |
| [Best Practices & Recipes](./mq-patterns.md) | idempotency, retry แบบมี backoff, DLQ, ordering, monitoring, testing |

## เลือกให้ถูกเครื่องมือ

สามอย่างนี้หน้าตาคล้ายกัน แต่รับประกันคนละเรื่อง:

| | [Jobs](./jobs.md) | MQ | [Pub/Sub](./pubsub.md) |
|---|---|---|---|
| เก็บที่ไหน | ตาราง SQL ของ service เอง | broker | ไม่เก็บ |
| ใครทำงาน | process ของเราเอง | consumer ตัวไหนก็ได้ที่ bind คิวนั้น | ทุกคนที่ subscribe อยู่ *ตอนนั้น* |
| retry | มีในตัว (backoff, max attempts) | ต้องออกแบบเอง — [ดูวิธี](./mq-patterns.md#retry-backoff) | ไม่มี |
| ข้าม service | ไม่ (คิวเป็นของ service) | **ใช่ — นี่คือเหตุผลหลักของมัน** | ได้ แต่หายได้ |
| หายได้ไหม | ไม่ | ไม่ (persistent + confirm) | หาย |

เลือกแบบสั้นๆ:

- งานของ **service เราเอง** ที่ห้ามหาย → **jobs** ไม่ต้องมี broker ไม่ต้องมี topology
  และดู run log ย้อนหลังได้
- งานที่ **service อื่น** ต้องรับไปทำต่อ หรือ event ที่มีผู้รับหลายเจ้าแบบไม่รู้จักกัน →
  **MQ**
- แค่บอกให้รู้ ไม่ต้องมีใครรับผิดชอบ (invalidate cache, live update) → **pub/sub**

## สัญญาที่ MQ ให้ และไม่ให้

**at-least-once** — เท่านั้น การส่งซ้ำเกิดขึ้นได้เสมอ (consumer ตายหลังทำงานเสร็จแต่ก่อน
ack, connection หลุดกลาง handler, หรือ requeue) จึงเป็นเรื่องของ **handler** ที่จะต้อง
idempotent ไม่ใช่เรื่องของ broker — [วิธีทำ](./mq-patterns.md#idempotency)

| ให้ | ไม่ให้ |
|---|---|
| message ที่ confirm แล้วอยู่รอด broker restart (persistent + `DeclareQueue` แบบ durable) | exactly-once |
| ส่งใหม่ถ้า consumer ไม่ ack | ลำดับ ถ้าตั้ง concurrency > 1 หรือมี consumer หลายตัว |
| แยกงานให้ consumer หลายตัวโดยไม่ต้องรู้จักกัน | การรับประกันว่ามีคิวใดรับ message ไปจริง (นั่นคือเรื่องของ [binding](./mq-topology.md)) |
| dead-letter สำหรับสิ่งที่ทำไม่สำเร็จ | retry policy สำเร็จรูป |

## Quick start

ครบวงจรหนึ่งชุด — service ที่ทั้งประกาศ topology, consume และ publish:

```go
func main() {
    env, err := core.NewEnv()
    if err != nil {
        panic(err)
    }

    mq, err := core.NewMQ(env)           // MQ_CONNECTION_STRING หรือ MQ_HOST/...
    if err != nil {
        panic(err)
    }
    app, err := core.NewApp(env, core.WithMQ(mq))
    if err != nil {
        panic(err)
    }
    defer app.Shutdown(context.Background())

    consumer := app.NewMQConsumer(
        core.WithMQPrefetch(20),
        core.WithMQConcurrency(4),
    )

    // ประกาศ exchange/queue/binding ให้ตัวเอง — deploy service คือสิ่งที่สร้าง topology
    consumer.OnQueue(core.ConsumeQueue{
        Queue: core.QueueConfig{
            Name:               "orders.shipping",
            DeadLetterExchange: "orders.dlx",
        },
        Exchange:    &core.ExchangeConfig{Name: "orders", Kind: core.ExchangeTopic},
        BindingKeys: []string{"order.created", "order.paid"},
    }, func(ctx core.IMQContext, d *core.Delivery) error {
        order, err := core.BindDelivery[Order](d)
        if err != nil {
            return err        // payload พัง — ส่งใหม่ก็พังเหมือนเดิม, dead-letter ไป
        }
        return service.Ship(ctx, order)
    })

    if err := consumer.Start(); err != nil {
        panic(err)            // broker ไม่พร้อม = boot ไม่ผ่าน ไม่ใช่เงียบแล้วไม่ได้ยินอะไร
    }

    e := core.NewHTTPServer(app, nil)
    e.POST("/orders", createOrder)

    core.NewRunner(app, core.RunHTTP(e)).Run()
}

func createOrder(c core.IHTTPContext) error {
    // …บันทึกลง database ให้เสร็จก่อน แล้วค่อย publish
    if err := c.MQ().Publish("orders", "order.created", order); err != nil {
        return err
    }
    return c.JSON(http.StatusCreated, order)
}
```

สองบรรทัดที่ถือเป็นหลักของทั้ง section:

1. **`Publish` คืน nil = broker รับผิดชอบ message นั้นแล้ว** ไม่ใช่แค่ "ส่งออกไปแล้ว" —
   [ทำไม](./mq-publishing.md#err-nil)
2. **handler คืน error = message ไป dead-letter ไม่ใช่วนกลับมาใหม่** —
   [ทำไม](./mq-consumer.md#ack-nack)

## Interfaces

```go
type IMQ interface {
    Publish(exchange, key string, msg any) IError
    PublishWith(exchange, key string, msg any, opts PublishOptions) IError

    DeclareExchange(cfg ExchangeConfig) IError
    DeclareQueue(cfg QueueConfig) (QueueInfo, IError)
    BindQueue(queue, exchange, key string, args ...map[string]any) IError
    UnbindQueue(queue, exchange, key string, args ...map[string]any) IError
    DeleteQueue(name string) IError
    PurgeQueue(name string) (int, IError)
    QueueInfo(name string) (QueueInfo, IError)

    Ping() IError
    Enabled() bool
    WithContext(ctx context.Context) IMQ
    Close() IError
}

type IMQConsumer interface {
    On(queue string, h MQHandler) IMQConsumer
    OnQueue(cfg ConsumeQueue, h MQHandler) IMQConsumer
    Start() IError
    Stop(ctx context.Context) IError
    Running() bool
}

type MQHandler func(ctx IMQContext, d *Delivery) error

type IMQContext interface {
    IContext            // logger, database, cache, Sentry, ENV — ครบเหมือน request
    Delivery() *Delivery
}
```
