# External services

[Requester](./requester.md) ตอบว่า**ยิงยังไง** หน้านี้ตอบว่า**โค้ดนั้นอยู่ที่ไหน และใครเรียกได้** —
คำถามที่เริ่มสำคัญตอนที่ module มากกว่าหนึ่งตัวต้องใช้ upstream เดียวกัน

## upstream คือตารางของ module ตัวหนึ่ง

โครงไม่เปลี่ยน — `handler → service → store` ยังใช้ได้ทั้งดุ้น แค่ชั้นล่างสุดไม่ใช่ SQL

```
modules/exchange/
    exchange.module.go
    exchange.http.go
    handler/            bind → convert → call → render
    service/            "จะเรียกยังไงให้รอด" + business rule
                        ไม่มี store/ — repository ของมันคือ HTTP call
```

กฎข้อ 1 ของ [Project Structure](./structure.md) ("module เป็นเจ้าของตารางของตัวเอง")
อ่านใหม่เป็น **module เป็นเจ้าของ upstream ของตัวเอง**: ไม่มี module อื่นยิงไปที่ provider
นั้นตรงๆ อยากได้ rate ก็ถามผ่าน `IExchangeService` เหตุผลเดียวกันเป๊ะกับตาราง — วันที่เปลี่ยน
provider คุณอยากแก้ที่เดียว ไม่ใช่ไล่ grep หา base URL

## `client/` — คู่สมมาตรของ `store/`

module ที่เรียก endpoint เดียวเขียนทุกอย่างไว้ใน `service/` ได้เลย แต่พอ upstream มี 6–8
endpoint มันจะปนกันระหว่าง "auth header ใส่ยังไง, timeout เท่าไหร่, error ของเขาแปลว่าอะไร"
กับ business rule จริงๆ ตรงนั้นให้แยก `client/` ออกมา:

| | ของเรา | ของเขา |
|---|---|---|
| package เดียวที่แตะได้ | `store/` | `client/` |
| สิ่งที่ลืมไม่ได้ | scope/WHERE (`OwnedBy`) | base URL, auth header, timeout |
| คืนอะไร | `models.X` | **type ของ module เรา ไม่ใช่ DTO ของเขา** |

ผลคือ `service/` อ่านเหมือนกันไม่ว่าข้อมูลจะมาจากแถวหรือจากสาย และ DTO ของ upstream
(`ratesResponse` ที่มี field ชื่อแปลกๆ ตามที่เขาตั้ง) เป็น unexported อยู่ใน `client/`
ไม่มีทางหลุดขึ้นไปถึง handler — ดู [Data shapes](./data-shapes.md) สำหรับ type
ทุกชนิดที่ข้ามขอบเขตใน service หนึ่งตัว

เกณฑ์: **1–2 call ใช้ `service/` พอ — มากกว่านั้น หรือ upstream มี error taxonomy
ของตัวเอง ให้แยก `client/`**

## module ที่ไม่มี route

เมื่อ upstream นั้นเป็นของที่ module อื่นเรียกใช้ ไม่ใช่ของที่ client ภายนอกเรียก มันก็ยัง
เป็น module — แค่เป็น module ที่ไม่ expose อะไรออกไปข้างนอกเลย

```
modules/kyc/
    kyc.module.go      Module + New + Name + HealthChecks()
    kyc.api.go         สิ่งที่ module อื่นเรียกได้
    service/           การเรียก + การ map error
    client/            ถ้า upstream มีหลาย endpoint
                       ไม่มี handler/ ไม่มี kyc.http.go
```

ผู้ใช้ประกาศ interface **ของตัวเอง** ไม่ import `kyc` ตรงๆ ตาม
[กฎข้อ 2](./structure.md) เหมือนที่ auth ทำกับ user:

::: code-group

```go [modules/order/service/order.deps.go]
// order บอกว่าตัวเองต้องการแค่นี้ — ไม่ใช่ทั้ง IKYCService
type KYC interface {
    Verify(nationalID string) (*models.KYCResult, core.IError)
}

type KYCFor func(core.IContext) KYC
```

```go [cmd/modules.go]
// ที่เดียวที่รู้ว่าใครให้ใคร
kycForOrder := func(ctx core.IContext) order.KYC { return kyc.NewKYCService(ctx) }
kycForUser := func(ctx core.IContext) user.KYC { return kyc.NewKYCService(ctx) }

return core.NewModules(
    kyc.New(),
    order.New(kycForOrder),
    user.New(kycForUser),
)
```

:::

closure ซ้ำหนึ่งตัวต่อคู่คือราคาที่จ่าย และมันคุ้ม: `order.KYC` มี 1 เมธอด ส่วน
`IKYCService` จริงอาจมี 6 — order จึงคอมไพล์ไม่พังเวลามีคนแก้เมธอดที่มันไม่ได้ใช้

## มันต้องมี `HealthChecks()`

module ที่ไม่มี route ไม่มี job จะขึ้นว่า *declares nothing* ใน
[Devtools](./devtools.md) — ซึ่งเป็นข้อความที่มีไว้เตือน misconfiguration ปล่อยไว้มันจะ
เป็น false positive ถาวรที่สอนให้คนเลิกดูสัญญาณนี้

ทางออกไม่ใช่การกลบ แต่คือให้มันมี health check ซึ่งเป็นสิ่งที่ `IHealthModule` ถูกเขียน
มาเพื่อเคสนี้ตรงๆ:

```go
func (m *Module) HealthChecks() []core.HealthCheck {
    return []core.HealthCheck{{
        Name:     "upstream",              // → รายงานเป็น "kyc.upstream"
        Check:    m.ping,
        Critical: false,                   // ← สำคัญ
    }}
}
```

`Critical: false` = **degraded (200) ไม่ใช่ not-ready (503)** — provider ล่มไม่ควรทำให้
Kubernetes ถอน pod ออกจาก load balancer แล้ว restart วนไป เพราะ restart ไม่ได้ทำให้ของ
เขากลับมา แต่มันต้องเห็นใน `/healthz` ตอนตีสอง ดู [Health](./health.md)

## state ที่มีอายุเท่า process อยู่บน `Module`

`NewKYCService(ctx)` ผูกกับ context เดียวและสร้างใหม่ทุก request — ของที่ต้องมีตัวเดียวทั้ง
process (token ที่ refresh เอง, SDK client, circuit breaker, rate limiter) เก็บที่นั่นไม่ได้

มันอยู่บน `Module` และเปิด/ปิดด้วย `ILifecycleModule`:

```go
type Module struct {
    creds *credentialCache                 // ตัวเดียวทั้ง process
}

func (m *Module) Start(app *core.App) core.IError    { return m.creds.Load(app) }
func (m *Module) Stop(ctx context.Context) core.IError { return m.creds.Close() }

func (m *Module) service(ctx core.IContext) IKYCService {
    return service.NewKYCService(ctx, m.creds)   // per-context + per-process
}
```

`Stop` ถูกเรียก**ท้ายสุดของ drain** — หลัง request จบ ก่อน pool ถูกปิด — จึงยัง revoke
token กับ upstream ได้ ดู [Runner](./runner.md)

ถ้าไม่ต้องการอะไรนอกจาก HTTP ธรรมดา ก็ไม่ต้องมีเลย: `core.Requester(ctx)` แชร์ pool
ให้อยู่แล้ว

## error ของเขาห้ามเป็นคำตอบของเรา

`Send` คืน `IError` ที่พก **status และ code ของ upstream** มาด้วย ซึ่งเป็นสิ่งที่ห้ามส่งต่อ:
client ที่ `switch` กับ error code ของ provider คือ client ที่พังวันที่เราเปลี่ยน provider

ทุกทางออกต้อง map เป็น error ที่ [`emsgs/`](./service-errors.md) ของเราเป็นเจ้าของ แล้วเก็บ
ของเดิมไว้เป็น *cause* สำหรับ Sentry:

```go
switch {
case fail.IsUnknownCurrency() || err.GetStatus() == http.StatusNotFound:
    // 404 ของเขา = 400 ของเรา: request ถูกต้อง แต่คู่เงินนั้นเขาไม่ได้ quote
    // caller แก้เองได้ ไม่มีอะไรผิดฝั่งเรา
    return emsgs.UnknownCurrency

case err.GetStatus() == http.StatusTooManyRequests:
    // เราเกินโควตาที่ซื้อ ไม่ใช่ความผิด caller — 503 บอกให้กลับมาใหม่ ซึ่งจริง
    // โดยไม่ต้องเปิดเผยว่าเราซื้อข้อมูลนี้มา
    return s.ctx.NewError(err, emsgs.ExchangeUnavailable)

default:
    // 5xx, timeout, connection refused — NewError รายงาน Sentry พร้อม scope
    // ของ request เพราะ dependency ที่ล่มเป็นเรื่องที่เราต้องรู้
    return s.ctx.NewError(err, emsgs.ExchangeUnavailable)
}
```

::: warning status code โกหกได้
provider ที่เริ่มบังคับ API key ตอบ `200` พร้อม body คนละหน้าตา ไม่ใช่ `401`

เส้นแบ่งที่ต้องเขียนเอง: **body ไม่มี key ที่ควรมี = ปัญหาของเขา** (503 + `Log().Error`
เพราะไม่มีใครนอกทีมแก้ได้) **body มี key แต่ไม่มีรายการที่ขอ = ปัญหาของ caller** (400)

ยุบสองอันเป็นอันเดียวเมื่อไหร่ credential ที่หายไปจะถูกรายงานให้ user ว่า "ไม่มีคู่เงินนี้"
:::

## Test: อย่า mock `IRequester`

ยิง `httptest.Server` จริงแล้วชี้ config ไปที่มันด้วย `coretest.WithEnv`:

```go
up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.Write([]byte(`{"date":"2026-01-01","rates":{"THB":36.5}}`))
}))
defer up.Close()

app := coretest.NewApp(t, coretest.WithEnv(map[string]string{"exchange_base_url": up.URL}))
```

สิ่งที่ต้องพิสูจน์คือ**เราอ่านคำตอบของเขาถูกไหม** — 200 ที่ไม่มี key, 429, timeout,
body ที่เปลี่ยนรูป — ซึ่ง mock ที่คืน struct สำเร็จรูปไม่เคยครอบคลุม mock พิสูจน์ได้แค่ว่า
เราเรียกฟังก์ชันถูกตัว

## ทางเลือกที่ไม่เลือก

**เอาไปไว้ `repo/` หรือ `helpers/`** — [arch](./structure.md) อนุญาต (มันไม่ import module)
และดูเบากว่า แต่ให้ผลตรงข้ามกับกฎทั้งสี่ข้อ: ไม่มีเจ้าของ, ไม่มี health check, ไม่มี
`.api.go` ที่บอกว่าอะไรเรียกได้ ใครก็เพิ่มเมธอดได้ และวันที่ upstream เปลี่ยน error format
ไม่มีใครรู้ว่าใครพังบ้าง เป็นเรื่องเดียวกับ shared repository package ต่างแค่ตารางเป็นของคนอื่น

**ทำเป็น capability บน `App`** (`ctx.KYC()` แบบ `ctx.Mailer()`) — ทำไม่ได้ และตั้งใจ:
`App` มี option ปิดตาย เพราะ capability คือของที่ framework มี memory implementation ให้
([Testing](./testing-mock.md)) ไม่ใช่ของที่แต่ละ service มีไม่เหมือนกัน

## เส้นแบ่งสั้นๆ

| module อื่นใช้ | ทำยังไง |
|---|---|
| **operation เดียวกัน** ("verify คนนี้ที") | module + `.api.go` — ผู้ใช้ประกาศ interface ของตัวเอง |
| **upstream เดียวกัน คนละ operation** | แชร์แค่ `client/` แล้วต่างคนต่างมี service เพราะ business rule ไม่ได้ใช้ร่วมกัน |

## อ่านต่อ

- [Requester](./requester.md) — timeout, retry, `SetError`, log ของ call ขาออก
- [Data shapes](./data-shapes.md) — wire DTO, domain type และอีก 6 แบบ อยู่ไฟล์ไหน
- [Project Structure](./structure.md) — กฎ 4 ข้อที่หน้านี้อ้างถึง
- [Modules](./modules.md) — optional interface ทั้งหมดที่ module มีให้เลือก
- [Health & Readiness](./health.md) — critical กับ degraded ต่างกันยังไง
- [Service Errors](./service-errors.md) — `emsgs/` จัดยังไง
- [MQ](./mq.md) — call ที่พังไม่ได้ ควรอยู่หลังคิวไม่ใช่ใน handler
