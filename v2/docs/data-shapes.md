# Data shapes (DTO)

service หนึ่งตัวมี struct ที่หน้าตาคล้ายกันอยู่ 6–8 แบบ และคำถามที่ทำให้เสียเวลาที่สุดคือ
"อันนี้ควรเป็นตัวใหม่ หรือใช้ตัวเดิมซ้ำ" หน้านี้ตอบทีเดียวว่าแต่ละแบบ**อยู่ที่ไหน ทำไม
และเมื่อไหร่ที่ใช้ซ้ำได้**

หลักเดียวที่ใช้ตัดสินทุกข้อ: **type หนึ่งตัวรับใช้ขอบเขตเดียว** — struct ที่ทั้ง bind
จาก HTTP, ทั้ง map ลงคอลัมน์, ทั้ง serialise ออกไป คือ struct ที่เปลี่ยนอะไรไม่ได้เลย
เพราะทุกการเปลี่ยนกระทบสามอย่างพร้อมกัน

## ตารางสรุป

| type | ที่อยู่ | รูปร่าง | ข้าม |
|---|---|---|---|
| **Request** | `handler/<n>.request.go` | pointer + `json` tag + `Valid()` | HTTP → handler |
| **Job params** | `handler/<n>.job.go` | pointer + `json` tag + `Valid()` | trigger → job |
| **Payload** | `service/<n>.dto.go` | value ธรรมดา ไม่มี tag | handler → service |
| **Model** | `models/` | `gorm` + `json` tag | service ↔ store |
| **View** | `handler/<n>.view.go` | `json` tag อย่างเดียว | handler → HTTP |
| **Domain type** | `service/<n>.dto.go` (exported) | ของที่ module สัญญาไว้ | module → module |
| **Wire DTO** | `client/<n>.wire.go` (unexported) | ตามที่ upstream ตั้ง | client → upstream |
| **Event** | `service/<n>.event.go` | `json` tag + version | ข้าม process |

`models/` เป็น shared kernel — ที่เหลืออยู่ใน module ที่เป็นเจ้าของทั้งหมด

## โครงเต็ม

module ที่มีครบทุกอย่าง (ของจริงไม่ค่อยมี — ตัดที่ไม่ใช้ออกได้เลย):

```
modules/order/
    order.module.go        Module + New + Name + HealthChecks
    order.http.go          Routes
    order.jobs.go          Jobs / Cron
    order.mq.go            Consumers
    order.api.go           สิ่งที่ module อื่นเรียกได้

    handler/
        order.handler.go   HTTP handler — bind, convert, call, render
        order.request.go   CreateRequest, UpdateRequest    ← เข้ามาทาง HTTP
        order.view.go      OrderView, orderListView        ← ออกไปทาง HTTP
        order.job.go       JobFunc + ReindexParams         ← เข้ามาทาง scheduler
        order.consumer.go  MQHandler                       ← เข้ามาทางคิว

    service/
        order.service.go   IOrderService + implementation
        order.dto.go       CreatePayload, UpdatePayload    ← handler พูดกับ service
        order.event.go     OrderPlaced                     ← ออกไปทางคิว

    client/
        payment.client.go  การเรียก payment gateway
        payment.wire.go    chargeRequest, chargeResponse   ← unexported

    store/
        order.store.go

models/
    order.model.go         Order — ตาราง
```

ทุกไฟล์ที่ลงท้าย `.request.go` `.view.go` `.dto.go` `.wire.go` `.event.go` คือ **type
ล้วนๆ ไม่มี behaviour** ยกเว้น `Valid()` และตัวช่วยแปลง — ลอจิกอยู่ใน `.service.go`

## เดินตาม request หนึ่งครั้ง

```
POST /orders
  │
  ├─ handler.CreateRequest        pointer, json tag, Valid()   ← bind
  ├─ service.CreatePayload        value ธรรมดา                  ← utils.Copy
  ├─ models.Order                 gorm tag                      ← service สร้าง
  │    └─ client.chargeRequest    ตามที่ gateway ตั้ง            ← ยิงออก
  ├─ service.OrderPlaced          json + version                ← publish
  └─ handler.OrderView            json tag                      ← render
```

ห้าตัวสำหรับสิ่งเดียวกันดูเยอะ จนกว่าจะถึงวันที่ต้องเปลี่ยนอันใดอันหนึ่ง — คอลัมน์
`total_satang` เปลี่ยนชื่อ ไม่ควรทำให้ JSON ที่ mobile app อ่านอยู่เปลี่ยนตาม และ gateway
ที่เปลี่ยน field ไม่ควรทำให้ตารางเราต้อง migrate

## Request ≠ Payload

สองอันนี้คนถามบ่อยที่สุดว่าทำไมไม่ใช้ตัวเดียว

::: code-group

```go [handler/order.request.go]
// pointer เพราะต้องแยก "ไม่ได้ส่งมา" (nil) ออกจาก "ส่งค่าว่างมา" ("")
// — PATCH ที่ไม่มี pointer จะเซ็ตทุก field ที่ไม่ได้ส่งเป็นค่าว่าง
type CreateRequest struct {
    Note  *string `json:"note"`
    Items *[]Item `json:"items"`
}

func (r *CreateRequest) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Str("note", r.Note).Max(500)
    return v.Error()
}
```

```go [service/order.dto.go]
// value ธรรมดา ไม่มี json tag เพราะไม่เคยถูก serialise: มันคือสิ่งที่ operation
// ต้องการ ไม่ใช่สิ่งที่มาถึงทาง HTTP
//
// นี่คือสิ่งที่ทำให้ job และ module อื่นเรียก Create ได้โดยไม่ต้องปั้น request ปลอม
type CreatePayload struct {
    Note  string
    Items []Item
}
```

:::

handler แปลงด้วย [`utils.Copy`](./utils.md) ซึ่งจับคู่ตามชื่อ field และ dereference
pointer ให้:

```go
payload, _ := utils.Copy[service.CreatePayload](input)
```

## Model เป็น response ได้ถึงเมื่อไหร่

โครงตั้งต้นคืน `models.Order` ตรงๆ แล้วใช้ `json:"-"` ปิดสิ่งที่ไม่ควรออก:

```go
type User struct {
    BaseModel
    Email    string `json:"email"    gorm:"column:email"`
    Password string `json:"-"        gorm:"column:password"`   // ← opt-out
}
```

ใช้ได้จริงและประหยัดโค้ดแปลงไปเยอะ **แต่ต้องรู้ว่ากำลังรับอะไรอยู่: มันเป็น opt-out**
คอลัมน์ใหม่ที่ไม่ได้ใส่ tag จะโผล่ใน API ทันที โดยไม่มีใครตัดสินใจ และไม่มี test ไหนพัง

เพิ่ม `handler/<n>.view.go` เมื่อเจอข้อใดข้อหนึ่ง — ก่อนหน้านั้นไม่ต้อง:

| อาการ | ทำไม model ทำไม่ได้ |
|---|---|
| ต้องการสอง shape (list ย่อ / detail เต็ม) | struct เดียวมี tag ชุดเดียว |
| มี field ที่ไม่ใช่คอลัมน์ (`is_owner`, `unread_count`) | ใส่ลง model แล้ว gorm จะพยายาม map |
| ซ่อนตามสิทธิ์ (admin เห็น `email` คนอื่นไม่เห็น) | `json` tag เป็น static ตัดสินใจตอน runtime ไม่ได้ |
| API เป็นสัญญากับ mobile app ที่ deploy ไม่พร้อมกัน | rename คอลัมน์ = break API |

ข้อที่สามคือข้อที่ทำให้ต้องเปลี่ยนจริงๆ เพราะมันไม่มีทางเลี่ยง

```go [handler/order.view.go]
// view เป็น opt-in: field ที่ไม่ได้เขียนไว้ตรงนี้ ไม่ออกไป
type OrderView struct {
    ID      string `json:"id"`
    Total   int64  `json:"total"`
    IsOwner bool   `json:"is_owner"`     // ไม่ใช่คอลัมน์
}

func newOrderView(o *models.Order, callerID string) OrderView {
    return OrderView{ID: o.ID, Total: o.Total, IsOwner: o.UserID == callerID}
}
```

แล้ว page แปลงด้วย `core.MapPage` ไม่ต้องวน loop เอง:

```go
return c.JSON(http.StatusOK, core.MapPage(page, func(o models.Order) OrderView {
    return newOrderView(&o, callerID)
}))
```

การย้ายจาก model มาเป็น view เป็นการแก้**ใน module เดียว** — นั่นคือเหตุผลที่เริ่มจาก
model ได้โดยไม่ต้องกลัว

## module อื่นควรได้ model หรือ type ของเรา

`models/` เป็น shared kernel ทุกคนเห็นอยู่แล้ว การคืน `*models.Order` ให้ module อื่น
จึงถูกกฎ และเป็นค่าตั้งต้นที่ใช้ได้

สิ่งที่ interface ทำได้คือ**จำกัด operation** — `order.KYC` ประกาศ 1 เมธอดจาก 6 ที่
`IKYCService` มี ([External services](./external-services.md)) แต่มัน **ไม่จำกัด field**
ผู้เรียกเห็นทุกคอลัมน์ และ rename คอลัมน์จะทำให้เขาพัง

ต้องมี type ของตัวเองเมื่อ:

- **ไม่มีตาราง** — `exchange.Rate` ไม่มี `models.Rate` ให้คืน
- **อยากให้ module อื่นเห็นน้อยกว่าที่ตารางมี** — เช่นให้ `order` รู้แค่ว่า KYC ผ่านไหม
  ไม่ต้องเห็นเลขบัตรและวันหมดอายุ
- **สิ่งที่คืนไม่ตรงกับหนึ่งแถว** — ผลรวม, ผลจากสามตาราง

## Wire DTO ของ upstream — unexported เสมอ

```go [client/payment.wire.go]
// ไม่ export: ไม่มีใครนอก package นี้ควรขึ้นกับชื่อ field ที่ gateway ตั้ง
type chargeResponse struct {
    ChargeID string `json:"charge_id"`
    Status   string `json:"status"`
}
```

`client/` คืน type ของเรา ไม่ใช่ `chargeResponse` — วันที่เปลี่ยน gateway การแก้จบใน
package เดียว ถ้ามันหลุดออกไป การเปลี่ยน gateway จะกลายเป็นการแก้ทั้ง module

เหตุผลเต็มและเรื่องการแปลง error อยู่ใน [External services](./external-services.md)

## Event ต่างจากทุกอันข้างบน

message ข้าม **process** และถูก **เก็บไว้** — มันจึงอาจถูกอ่านโดยโค้ดคนละเวอร์ชัน
(ตอน deploy ครึ่งทาง) หรือคนละ service เลย

::: warning payload คือ API สาธารณะ
เอา model หรือ payload มาใช้ซ้ำเป็น message ไม่ได้ ต่อให้ field ตรงกันวันนี้ — วันที่
rename คอลัมน์ consumer ที่ยังไม่ deploy จะ decode ไม่ออก และ message ที่ค้างในคิวก่อน
deploy ก็เป็นของเวอร์ชันเก่า

เปลี่ยนแบบ **เพิ่ม field เท่านั้น** ส่วนการเปลี่ยนที่ breaking ต้องขึ้น version หรือ
routing key ใหม่ — [วิธีเปลี่ยน schema](./mq-patterns.md#เปลี่ยน-schema-ของ-message)
:::

```go [service/order.event.go]
type OrderPlaced struct {
    Version   int    `json:"version"`     // consumer เก่าเช็คได้ว่าอ่านไหวไหม
    OrderID   string `json:"order_id"`
    PlacedAt  string `json:"placed_at"`
}
```

จะส่ง id อย่างเดียวหรือส่งทั้งก้อน เป็นคนละเรื่องกัน — [ใส่อะไรลงใน
payload](./mq-publishing.md#ใส่อะไรลงใน-payload) เทียบข้อดีข้อเสียไว้แล้ว

## Job params เป็น request ชนิดหนึ่ง

`c.Params(dest)` decode **และ** validate ด้วย `Valid()` ตัวเดียวกับ HTTP — มันจึงมีกฎ
เดียวกันทุกข้อ (pointer, json tag, `Valid`) และอยู่ติดกับ job handler ที่ bind มัน:

```go [handler/order.job.go]
type ReindexParams struct {
    Since *string `json:"since"`
}

func (p *ReindexParams) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Str("since", p.Since).Date()
    return v.Error()
}

func Reindex(c core.ICronjobContext) error {
    params := &ReindexParams{}
    if err := c.Params(params); err != nil {
        return err              // validation ไม่ผ่าน = run fail ก่อนเข้า handler
    }
    ...
}
```

ดู [Jobs: defining](./jobs-defining.md)

## กฎที่เหลือ

- **`models/` ไม่ import อะไรในโปรเจกต์เลย** เป็น shared kernel ที่มีแต่ type
  ([Project Structure ข้อ 3](./structure.md))
- **request ตรวจสิ่งที่ตอบได้จาก payload อย่างเดียว** — "ยาวเกิน 50 ตัวไหม" อยู่ใน
  `Valid()` ส่วน "ชื่อนี้ซ้ำกับของ user คนนี้ไหม" ต้องอ่านแถว จึงอยู่ใน service และคืน
  error จาก [`emsgs/`](./service-errors.md)
- **ห้ามให้ `service/` รู้จัก `IHTTPContext` หรือ `ICronjobContext`** — การแปลง
  request/params → payload เป็นหน้าที่ handler ซึ่งเป็นสิ่งที่ทำให้ service เดียวรันได้
  ทั้งใน HTTP, job และ test

## ทางเลือกที่ไม่เลือก: แยก `request/` `view/` `dto/` เป็น directory

คำถามที่ตามมาเสมอ เพราะ `handler/` มีไฟล์หลายชนิดปนกัน คำตอบคือ **ไม่แยก** และเหตุผล
ไม่ใช่เรื่องความสวยงาม

**1. มันคือ group by layer ซ้อนใน group by feature** ซึ่งเป็นสิ่งที่โครงนี้ปฏิเสธไว้ตั้งแต่
ระดับบนสุด เพิ่ม field เดียวใน `POST /orders` จะกลายเป็น: `request/`, `dto/`, `view/`,
`service/`, `store/`, `models/` — หกที่ ซึ่งคือ "เปิดสี่ directory เพื่อเพิ่ม field เดียว"
ที่เลิกทำไปแล้ว แค่ย้ายลงมาหนึ่งชั้น

**2. directory ใน Go คือขอบเขต dependency ไม่ใช่โฟลเดอร์** และ sub-package ที่มีอยู่
ทุกตัวมี**กฎให้บังคับ**:

| package | มีอยู่เพื่อ |
|---|---|
| `store/` | arch ห้ามทุกอย่างนอก module import — ownership ของตารางจึงบังคับได้ |
| `service/` | เป็นประตูเดียว module อื่นเข้าได้ทาง interface เท่านั้น |
| `client/` | wire DTO เป็น **unexported** ได้ ชื่อ field ของ upstream จึงหลุดออกไม่ได้ |

`request/` และ `view/` ไม่มีอะไรจะกันออก — ไม่มีกฎไหนที่เขียนลง `arch_test` ได้เลย
package ที่ไม่ได้บังคับอะไรคือต้นทุนล้วน และต้นทุนนั้นจับต้องได้: **ทุกอย่างต้อง export**
`newOrderView()` ที่เป็น lowercase อยู่ตอนนี้ ต้องเปิดให้ `handler` เรียกได้ทันทีที่ย้ายออกไป

**3. request กับ handler เปลี่ยนพร้อมกัน 100% ของครั้ง** ไม่ใช่ "บ่อย" — เพิ่ม field
ในฟอร์มคือแก้ทั้งคู่เสมอ ของสองอย่างที่เปลี่ยนพร้อมกันตลอดควรอยู่ติดกัน

ส่วนสิ่งที่อยากได้จากการแยก directory — รู้ทันทีว่าไฟล์นี้เป็นชนิดไหน — **suffix ให้แล้ว**
และเป็นกฎเดียวกับที่แยก `.http.go` / `.jobs.go` ที่ระดับบน

### แล้วถ้า `handler/` ใหญ่จริง

แยกได้ แต่แยก**ตาม feature ย่อย ไม่ใช่ตามชนิด**:

```
handler/
    order.handler.go          order.request.go          order.view.go
    order_refund.handler.go   order_refund.request.go
    order_export.handler.go   order_export.request.go
```

กฎเดิมที่ระดับล่าง: ของที่เปลี่ยนพร้อมกันอยู่ด้วยกัน ทำ refund ก็เปิดไฟล์ที่ขึ้นต้นเหมือนกัน

ถ้าใหญ่จนคนละทีมดูแล นั่นแปลว่ามันควรเป็น **module คนละตัว** ไม่ใช่ directory ย่อย

> `dto/` ที่คิดถึงมีอยู่แล้ว: payload อยู่ใน `service/` และ wire อยู่ใน `client/`
> ทั้งคู่เป็น package จริงที่มีกฎรองรับ สิ่งที่ไม่มี package ของตัวเองคือ request กับ view
> ซึ่งเป็นสองอันที่ผูกกับ handler แน่นที่สุดพอดี

## อ่านต่อ

- [Project Structure](./structure.md) — ไฟล์แต่ละอันอยู่ layer ไหน
- [Validation](./validation.md) — `Valid()` เขียนยังไง
- [External services](./external-services.md) — `client/` และการแปลง error ของ upstream
- [MQ: publishing](./mq-publishing.md) — ใส่อะไรลง payload
- [Jobs: defining](./jobs-defining.md) — params schema และ trigger
- [Utils](./utils.md) — `utils.Copy` จับคู่ field ยังไง
