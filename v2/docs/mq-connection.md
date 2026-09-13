# Connection & Config

## Wiring

```go
mq, err := core.NewMQ(env)
if err != nil {
    return err
}
app, err := core.NewApp(env, core.WithMQ(mq))
```

`NewMQ` **เชื่อมต่อทันทีตอน boot แล้ว fail ถ้าต่อไม่ได้** — เหมือน cache กับ database:
config ที่ผิดควรรู้ตั้งแต่ boot ไม่ใช่ตอน publish ครั้งแรกตอนตีสาม

## Configuration

```
MQ_CONNECTION_STRING=amqp://user:pass@host:5672/vhost   # amqps:// สำหรับ TLS
# หรือแยกเป็นฟิลด์
MQ_HOST=rabbit.internal
MQ_PORT=5672
MQ_USER=orders
MQ_PASSWORD=…
```

| key | ใช้เมื่อ |
|---|---|
| `MQ_CONNECTION_STRING` | มีค่านี้เมื่อไหร่ ตัวอื่นจะถูกมองข้ามทั้งหมด |
| `MQ_HOST` / `MQ_PORT` / `MQ_USER` / `MQ_PASSWORD` | ประกอบเป็น `amqp://user:pass@host:port/` (vhost `/`) |

ต้องการ vhost ที่ไม่ใช่ `/` หรือ TLS → ใช้ `MQ_CONNECTION_STRING` เพราะฟิลด์แยกประกอบ
vhost ไม่ได้ vhost ใน URI ต้อง URL-encode ด้วย: `/orders` เขียนเป็น `%2Forders`

```
MQ_CONNECTION_STRING=amqps://svc:pw@rabbit.internal:5671/%2Forders
```

## Options

```go
mq, err := core.NewMQ(env,
    core.WithMQChannels(16),                    // publish พร้อมกันได้กี่ตัว (default 8)
    core.WithMQPublishTimeout(3*time.Second),   // เพดานต่อหนึ่ง publish (default 5s)
)
```

**`WithMQChannels`** — หนึ่ง AMQP channel ใช้ได้ทีละ goroutine เท่านั้น ตัวเลขนี้จึงเป็น
ทั้งขนาด pool และเพดาน concurrency ของการ publish publish ที่เกินโควตาจะ *รอคิว* ไม่ใช่
เปิด channel เพิ่ม — ที่เป็นแบบนี้เพราะ burst ของ request ที่เปิด channel คนละอันจะชน
`channel_max` ของ broker (default 2047) แล้วพังทั้งชุดแทนที่จะช้าลง

ตั้งเท่าไหร่: ประมาณ "จำนวน request ที่ publish พร้อมกันตอน peak" ไม่ใช่จำนวน request
ทั้งหมด — publish หนึ่งครั้งใช้เวลาระดับมิลลิวินาที channel 8 ตัวจึงรองรับได้มากกว่าที่
ตัวเลขทำให้รู้สึก ถ้าเห็น latency ของ endpoint บวมพร้อมกันทั้งหมดตอน burst นั่นคือสัญญาณ
ว่าควรเพิ่ม

**`WithMQPublishTimeout`** — เพดานของหนึ่ง publish *รวมเวลารอ confirm จาก broker*
context ของ request ที่สั้นกว่ายังชนะอยู่ อันนี้เป็นเพดาน ไม่ใช่ deadline

## Health & readiness

`Ping()` เปิด channel จริงบน connection จริง จึงทั้งใช้เป็น readiness probe และทำให้
publisher ต่อกลับได้ในตัว:

```go
e.GET("/readyz", func(c core.IHTTPContext) error {
    if err := c.MQ().Ping(); err != nil {
        return err
    }
    return c.NoContent(http.StatusOK)
})
```

⚠️ **อย่าใส่ `Ping()` ไว้ใน liveness probe** — broker ล่มชั่วคราวไม่ใช่เหตุผลให้
Kubernetes ฆ่า pod ที่ยังเสิร์ฟ request อื่นได้อยู่ readiness (ถอนออกจาก load balancer)
คือความหมายที่ถูก ดู [Health](./health.md)

## Broker restart

**publisher เชื่อมต่อใหม่เอง** connection กับ channel ที่ตายไปพร้อม broker จะถูกเปิดใหม่
ตอน publish ครั้งแรกหลัง broker กลับมา — service ไม่ต้อง restart ตาม

การเชื่อมต่อใหม่เป็นแบบ **on-demand ไม่ใช่ background loop**: service ที่ไม่ได้ publish
อะไรเลยจะไม่ถือ connection ค้างไว้พยายามต่อ ผลข้างเคียงที่ควรรู้คือ publish
*ครั้งแรก* หลัง broker กลับมาจะเป็นตัวที่จ่ายค่า dial — ถ้า path นั้นคือ request ของ user
ก็คือ request นั้นช้ากว่าปกติหนึ่งครั้ง (มี `Ping()` ใน readiness probe อยู่แล้วก็มักจะ
เป็นตัวที่จ่ายค่านี้แทน)

[consumer มี supervisor เป็นของตัวเอง](./mq-consumer.md#broker-restart) และใช้
connection คนละอันกับ publisher — prefetch กับการ reconnect ของสองฝั่งไม่ควรมาแย่ง
connection เดียวกัน

## ไม่ได้ตั้งค่า broker ไว้

`ctx.MQ()` **ไม่เคยเป็น nil** — service ที่ไม่มี `MQ_*` ได้ publisher ที่ปิดอยู่ ซึ่งทุกคำสั่ง
คืน `503 MQ_DISABLED` พร้อมบอกว่า config ตัวไหนหาย

ตรงนี้ต่างจาก cache **โดยตั้งใจ**: cache miss ยัง recover ได้ด้วยการคำนวณใหม่ แต่ message
ที่ถูกทิ้งเงียบๆ คืองานที่ไม่มีใครหยิบไปทำ และไม่มีใครรู้ว่ามันหาย — เป็นการแลกแบบเดียวกับ
storage และ mailer

```go
if !ctx.MQ().Enabled() {
    // เส้นทางที่ต้องมี broker จริงๆ ตรวจได้ล่วงหน้า แทนที่จะไปพังกลางทาง
}
```

ใช้ `NewNoopMQ()` ตรงๆ ได้ใน test หรือใน binary ที่ role ของมันไม่ publish อะไรเลย

## Lifecycle

| ใครปิด | เมื่อไหร่ |
|---|---|
| `app.Shutdown(ctx)` | ปิด publisher ให้ — และหยุด consumer **ก่อน** ปิด pool ที่ handler ใช้อยู่ |
| `mq.Close()` | เรียกเองเฉพาะ publisher ที่สร้างนอก `App` (เรียกซ้ำได้ ปลอดภัย) |

publish ที่กำลังค้างอยู่ตอน `Close` จะ fail ด้วย `503 MQ_CLOSED` แทนที่จะถ่วง shutdown ไว้ —
ระหว่าง shutdown นี่คือคำตอบที่ตรงไปตรงมาที่สุด

ใช้ [`Runner`](./runner.md) ก็อยู่ในลำดับที่ถูกต้องอยู่แล้ว: หยุดรับงานใหม่ → รอของที่ค้าง →
ค่อยปิด connection

```go
core.NewRunner(app,
    core.RunHTTP(e),
    core.RunJobs(jobs),
).Run()      // consumer ที่ Start() ไว้แล้วถูก App จำไว้ และหยุดให้ตามลำดับ
```

## Publisher นอก request

`ctx.MQ()` ผูกกับ context ของ request — goroutine ที่อยู่นานกว่า request ต้องมี context
ที่อยู่นานกว่าด้วย ไม่งั้น publish จะโดนยกเลิกพร้อม request:

```go
pub := ctx.MQ().WithContext(context.Background())
go func() {
    if err := pub.Publish("reports", "report.ready", id); err != nil {
        // log เอง — ไม่มี request ให้คืน error กลับไปหาแล้ว
    }
}()
```

แต่ก่อนจะเขียนแบบนี้: goroutine ที่ยิงทิ้งไว้แล้ว process ถูก kill พอดีคืองานที่หายไปเงียบๆ
ถ้ามันสำคัญพอที่จะ publish ก็มักจะสำคัญพอที่จะเป็น [job](./jobs.md)
