# Health & Readiness

สอง probe ที่ทุก deployment ต้องมี — และเหตุผลว่าทำไมมันต้องเป็นคนละอย่างกัน

```go
e := core.NewHTTPServer(app, nil)
core.RegisterHealthRoutes(e)   // GET /healthz และ GET /readyz
```

เรียกบน server ไม่ใช่บน group ที่มี auth — orchestrator ไม่มี token

## liveness ≠ readiness

| | ถามว่าอะไร | ตอบผิดแล้วเกิดอะไร |
|---|---|---|
| `/healthz` | process ค้างไหม | pod ถูก **restart** |
| `/readyz` | instance นี้รับ traffic ได้ไหม | pod ถูก **ถอดออกจาก load balancer** |

`/healthz` ของ framework **ไม่แตะ dependency ใดๆ เลย** โดยตั้งใจ — liveness probe ที่
ping database คือ probe ที่ restart ทุก instance พร้อมกันตอน database สะดุด เปลี่ยน
outage เดียวเป็นสองอัน คำตอบเดียวที่ถูกสำหรับ "process นี้ค้างหรือเปล่า" คือคำตอบที่
ล้มเหลวด้วยเหตุผลอื่นไม่ได้

`/readyz` ping ทุก dependency ที่ `App` ถืออยู่ **พร้อมกัน** และรายงานทีละตัว

```json
{
  "status": "up",
  "service": "orders",
  "checks": {
    "database": {"status": "up", "critical": true, "took_ms": 2},
    "cache":    {"status": "up", "took_ms": 1},
    "storage":  {"status": "up", "took_ms": 14}
  },
  "took_ms": 15
}
```

## critical กับ degraded

| ผลลัพธ์    | เมื่อไหร่                           | HTTP    |
| ------------| -------------------------------------| ---------|
| `up`       | ทุกตัวตอบ                           | 200     |
| `degraded` | dependency ที่ **ไม่** critical ล่ม | 200     |
| `down`     | dependency ที่ critical ล่ม         | **503** |

`degraded` ตอบ 200 เพราะ service ยัง serve ได้ แค่ไม่ครบ — ถอด instance ออกจาก
rotation เพราะ redis ล่ม แปลว่าถอดทุก instance พร้อมกัน

default: **database กับ mongo เป็น critical, ที่เหลือไม่** ซึ่งเป็นเคสที่พบบ่อย ไม่ใช่กฎ
เปลี่ยนได้ด้วย `Only`

## dependency ที่ถูกตรวจ

`AppHealthChecks(app)` สร้าง check จากสิ่งที่ App ถืออยู่จริง — SQL ทุก connection,
Mongo ทุก connection, cache ทุกตัว, MQ, storage, mailer

**เฉพาะตัวที่ config ไว้เท่านั้นที่ปรากฏ** service ที่ไม่มี redis จะไม่มี cache check
ไม่ใช่มี cache check ที่ fail ตลอด — probe รายงานสิ่งที่ deployment นี้พึ่งพาจริง

connection ชื่อ `default` ใช้ชื่อสั้น (`database`) ที่เหลือมีชื่อกำกับ (`database.replica`)

## เพิ่ม check ของตัวเอง

```go
core.RegisterHealthRoutes(e, core.HealthOptions{
    Checks: []core.HealthCheck{{
        Name:     "partner-api",
        Critical: false,
        Check: func(ctx context.Context) error {
            _, err := core.Requester(ctx).Send(req, http.MethodGet, healthURL)
            return err
        },
    }},
})
```

`Only` แทนที่ list ของ App ทั้งหมด สำหรับ service ที่อยากระบุเองว่า probe อะไรบ้าง:

```go
core.HealthOptions{Only: []core.HealthCheck{ /* ... */ }}
```

## Options

```go
type HealthOptions struct {
    Timeout time.Duration   // ครอบทั้ง probe (default 3s)
    Details *bool           // ใส่ error text ในคำตอบ — nil = ตาม environment
    Checks  []HealthCheck   // เพิ่มจากของ App
    Only    []HealthCheck   // แทนที่ของ App ทั้งหมด
}
```

**`Details`** default คือ **เปิดนอก production ปิดใน production** — error text ตั้งชื่อ
host, user หรือ bucket ได้ และ probe route มักเป็น route เดียวที่ลืมเอาไปไว้หลัง gateway
บังคับเปิดด้วย `Details: core.BoolPtr(true)`

**`Timeout`** สั้นโดยตั้งใจ (3 วินาที) — probe ที่ค้างคือ probe ที่ถูกฆ่า และ instance ที่
database ใช้เวลาสิบวินาทีตอบ ก็ไม่ได้พร้อมอยู่ดีไม่ว่าผลจะออกมายังไง

check ทุกตัวรันพร้อมกัน ถ้ารันเรียงกัน probe จะใช้เวลาเท่ากับผลรวมของทุกตัว แล้ว
timeout ที่ควรจะป้องกันมันก็ต้องหลวมจนไม่มีประโยชน์ และ check ที่ panic ถูกจับไว้ —
probe เป็นสิ่งสุดท้ายที่ควรทำให้ process ตาย

## ใช้เอง โดยไม่ผ่าน HTTP

```go
report := core.CheckHealth(ctx, app)
if report.Status == core.HealthDown {
    return fmt.Errorf("dependencies are not ready: %+v", report.Checks)
}
```

เหมาะกับ startup gate, CLI หรือ route ที่อยากตอบในรูปแบบของตัวเอง

## ใน Postman collection

[`postmangen`](./postman.md) อ่าน `RegisterHealthRoutes` ออก — ทั้งสอง probe โผล่ใน
collection พร้อม example body ที่ core ตอบเอง รวมถึง `503` ของ `/readyz` ตอน
dependency ที่ critical ล่ม ทั้งที่ไม่มี handler ใน project ให้มันอ่านสักตัว

probe ที่ mount เองด้วย `core.LiveHandler()` / `core.ReadyHandler(app)` ที่ path
ของตัวเองก็ได้ example ชุดเดียวกัน

## Kubernetes

```yaml
livenessProbe:
  httpGet: { path: /healthz, port: 8080 }
  periodSeconds: 10
  failureThreshold: 3
readinessProbe:
  httpGet: { path: /readyz, port: 8080 }
  periodSeconds: 5
  failureThreshold: 2
```

`readinessProbe` ควรถี่กว่าและอ่อนไหวกว่า: ถอดออกจาก rotation แล้วใส่กลับ ถูกกว่า
restart มาก
