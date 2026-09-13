# Devtools

หน้าตรวจสอบภายในที่ mount เข้า service ที่กำลังรันอยู่ — process นี้ต่อกับอะไรจริงๆ,
route ไหนมีอยู่บ้างและใครเป็นคน serve, config ที่โหลดมาจริงคืออะไร, และ job ที่ผ่านมา
ทำอะไรไปบ้าง

```go
srv := core.NewHTTPServer(app, nil)

if err := devtools.Mount(srv, devtools.Options{
    Runner: runner,
}); err != nil {
    log.Fatal(err)   // ดูหัวข้อ "ทำไมต้องเช็ค error"
}
```

เปิดที่ `http://localhost:8080/_dev`

บน staging/prod ต้องมี guard — ที่ง่ายที่สุดคือ basic auth ผ่าน environment:

```sh
APP_DEVTOOLS_USER=ops
APP_DEVTOOLS_PASSWORD=s3cret
```

เต็มรูปแบบ — เพิ่ม action ที่แก้ของได้ กับ request trace:

```go
trace := devtools.NewTrace(devtools.TraceOptions{})

app, err := core.NewApp(env,
    core.WithLogTap(trace.Log),   // ต้องมี ไม่งั้น trace ไม่เห็น log
    core.WithSQL("default", db),
)

devtools.Mount(srv, devtools.Options{
    Runner:     runner,
    AllowWrite: true,
    Trace:      trace,
    Auth:       []echo.MiddlewareFunc{middlewares.RequireAdmin()},
})
```

## ทำไมต้องมีในตัว framework

คำถามพวกนี้ตอบได้จาก**ในตัว process เท่านั้น**:

- **"ทำไม cache ไม่ทำงาน"** — capability ทุกตัวถูกออกแบบให้ degrade ไม่ใช่ refuse to boot
  ([capabilities ไม่เคยเป็น nil](./architecture.md)) service ที่ไม่มี `CACHE_*`
  ยังรันได้ปกติบน cache ที่ miss ทุกครั้ง มองจากข้างนอกแยกไม่ออกจาก cache ที่ทำงานอยู่
- **"route นี้ใครเป็นคน serve"** — handler ที่ echo ถืออยู่คือ closure ที่ `WithHTTPContext`
  สร้าง ไม่มีชื่อของตัวเอง framework จึงจดชื่อไว้ตอน register
- **"ตัวแปรนี้ตั้งค่าไว้จริงไหม"** — สะกด `APP_` ผิดหนึ่งตัวไม่มี error ไม่มี log
  มีแค่ค่า default ที่เงียบ
- **"job ที่ fail เมื่อคืนพังตรงไหน"** — run log อยู่ใน store อยู่แล้ว แต่ไม่มีที่ให้เปิดดู

ทางเลือกเดิมคืออ่าน boot log แล้วเดา

::: tip "API รับอะไร ตอบอะไร" ไม่ใช่คำถามของหน้านี้
devtools ตอบเรื่องของ **process** — ต่ออะไรอยู่, route ไหนมีใครถือ, job ไปถึงไหน
ส่วนเอกสารว่า endpoint รับ field อะไร มี rule อะไร และกดยิงดูเลยได้ไหม อยู่ที่
[API Reference](/v2/apidocs) ซึ่ง mount แยกกันและใช้รหัสคนละชุด
:::

## security

Devtools เปิดเผยสิ่งที่คนบุกรุกอยากอ่านเป็นอย่างแรกพอดี — key ของ config, ชื่อ
connection, ทุก route ของ service — และ debug endpoint คือ route ที่ไม่มีใครจำได้ว่า
ต้องเอาไปไว้หลัง gateway

กติกาจึงเป็นแบบนี้:

| APP_ENV | ไม่มี guard | มี guard |
|---|---|---|
| `dev` | mount ได้ | mount ได้ |
| อื่นๆ | **`Mount` คืน error และไม่ register อะไรเลย** | mount ได้ |

"guard" คือ **อย่างใดอย่างหนึ่ง** ใน `BasicAuth`, `APP_DEVTOOLS_PASSWORD` หรือ `Auth`

### วิธีที่ง่ายที่สุด: basic auth

ตั้ง environment variable สองตัว จบ — ไม่ต้องแก้โค้ดเลย:

```sh
APP_DEVTOOLS_USER=ops           # ไม่ใส่ = "devtools"
APP_DEVTOOLS_PASSWORD=s3cret    # ตัวนี้แหละที่เปิดสวิตช์
```

เปิดเว็บแล้ว browser จะขึ้นกล่อง login ให้เอง กรอกครั้งเดียวจบทั้ง session

เขียนในโค้ดก็ได้ ถ้าอยากอ่านรหัสมาจากที่อื่น:

```go
devtools.Mount(srv, devtools.Options{
    BasicAuth: &devtools.BasicAuth{
        User:     "ops",
        Password: os.Getenv("DEV_PASSWORD"),
    },
})
```

**โค้ดชนะ config** — คนที่เขียน password ไว้ในโค้ดได้ password นั้น ส่วนคนที่ไม่ได้
เขียนอะไรเลยได้ตัวที่ deployment ตั้งไว้ ซึ่งคือเหตุผลที่การเปิดใช้บน staging เหลือ
แค่เพิ่ม env ตัวเดียว

`BasicAuth` ที่มี user แต่ไม่มี password จะทำให้ `Mount` fail — ล็อกที่ไม่มีกุญแจ
จะปฏิเสธทุกคนรวมทั้งคนที่ตั้งมันเอง และอาการจะดูเหมือน "ลืมรหัส" มากกว่า
"ไม่ได้ตั้งรหัส"

::: warning
basic auth ส่ง user:password (base64 ไม่ใช่การเข้ารหัส) ไปกับ**ทุก request**
ใช้หลัง HTTPS เท่านั้น และอย่าใช้รหัสซ้ำกับที่อื่น — มันคือ shared secret ของ
เครื่องมือภายใน ไม่ใช่ระบบ identity

browser ไม่มีปุ่ม logout สำหรับ basic auth ต้องปิด browser หรือใช้ private window
:::

### หรือใช้ middleware ของ service เอง

```go
devtools.Mount(srv, devtools.Options{
    Auth: []echo.MiddlewareFunc{middlewares.RequireAdmin()},
})
```

`Auth` เป็น echo middleware ธรรมดา — อะไรก็ตามที่ service ใช้ป้องกันหน้า admin
อยู่แล้วใช้ได้ทันที และมันครอบทั้ง UI และ JSON API

ใส่ทั้งสองอย่างพร้อมกันได้ — จะทำงานทั้งคู่ โดย **basic auth รันก่อน** เพื่อไม่ให้
request ที่รหัสผิดไปถึง guard ของ service ที่อาจต้อง query database ทุกครั้ง

### สวิตช์ที่สอง: `AllowWrite`

**ค่าเริ่มต้นคือ read-only** action ที่แก้ของได้ (trigger / cancel / replay /
pause / resume) ต้องเปิด `AllowWrite: true` แยกอีกชั้น และต้องส่ง `Runner` มาด้วย

แยกจาก `Auth` โดยตั้งใจ — "ให้คนที่ on-call ดูได้ว่า process นี้ทำอะไรอยู่" กับ
"ให้เขากดรัน job คิดเงินของเมื่อคืนซ้ำได้" ไม่ใช่สิทธิ์เดียวกัน และอันแรกคืออันที่
จะถูกแจกกว้าง

endpoint พวกนี้ถูก register เสมอไม่ว่า `AllowWrite` จะเปิดหรือไม่ ปิดอยู่จะตอบ
403 `DEVTOOLS_READ_ONLY` ไม่ใช่ 404 — 404 อ่านแล้วเหมือนเวอร์ชันไม่ตรงกัน
แล้วคนจะไปตามหาผิดที่

**ทุก action ถูก log พร้อมคนสั่ง** ที่ระดับ warn (`devtools trigger`,
`devtools cancel`, …) พร้อม `by` กับ `ip` — เอาจาก `c.GetUser()` ถ้าไม่มีก็เป็น
`devtools@<ip>` ไม่เคยว่าง เพราะ run ที่ `triggered_by` ว่างแยกไม่ออกจาก run ที่
scheduler สร้าง และนั่นคือแถวที่จะมีคนมาถามพอดี

### ทำไมต้องเช็ค error

`Mount` คืน `IError` ที่ควรทำให้ startup ล้ม ไม่ใช่ปล่อยผ่าน — เคสที่มันรายงานคือ
service ที่**กำลังจะ**เผยแพร่ config ของตัวเองออกอินเทอร์เน็ต ไม่มีอะไรพังให้เห็น
ข้อมูลแค่อ่านได้เฉยๆ โดยใครก็ตามที่เจอ path

## secret ไม่ถูกส่งออกไป

แท็บ config ไม่เคย serialize ค่าของ key ที่เป็นความลับ — ไม่ได้ปิดบังฝั่ง UI แต่
**ค่านั้นไม่เคยออกจาก process** JSON ที่ส่งกลับมามีแค่ `secret: true` กับ `set: true/false`

`set` สำคัญพอๆ กับการปิดบัง เพราะคำถามจริงคือ "password ผิด" หรือ "ไม่มี password"
ซึ่งเป็นคนละปัญหากัน และตอบได้โดยไม่ต้องพิมพ์ password ออกมา

key ที่ถือว่าเป็นความลับคือ key ที่มีคำเหล่านี้อยู่ข้างใน:

```
password  secret  token  credential  private
api_key   access_key  secret_key  dsn  connection_string
```

รายการนี้เอนไปทางปิดบังไว้ก่อน — connection string มี password อยู่ข้างใน, DSN มี key
อยู่ข้างใน และค่าที่ถูกซ่อนผิดเสียแค่ต้องไปเปิดดูที่ deployment ส่วนค่าที่ถูกแสดงผิด
เอากลับคืนไม่ได้

## แท็บที่มี

### overview

capability ทุกตัวของ process นี้ ทั้งตัวที่ต่ออยู่และ**ตัวที่ disabled** — ตัวหลังคือ
เหตุผลทั้งหมดที่หน้านี้มีอยู่ เพราะมันไม่เคย fail ตอน boot

connection SQL แต่ละตัวมีสถานะ pool มาด้วย (`in use / open / idle / max`) service ที่
request ค้างคิวรอ connection ดูเหมือน service ที่ database ช้าทุกประการ ถ้าไม่มีตัวเลขนี้

พร้อมกับ goroutine count, heap, GC และ `GOMAXPROCS`

::: tip
`runtime.ReadMemStats` หยุดโลกชั่วขณะ — หน้านี้จึงเป็นหน้าที่คนกดเปิด ไม่ใช่หน้าที่
poll ทุกวินาที
:::

### routes

ทุก route ที่ service serve พร้อมชื่อ handler ที่ register ไว้ — ค่าเดียวกับที่
[access log](./middleware.md) รายงานในฟิลด์ `handler`

route ที่ register ตรงกับ `*echo.Echo` (static file, third-party mount) จะไม่มีชื่อ
handler ซึ่งตัวมันเองก็เป็นข้อมูลที่ควรเห็น

### modules

เปิดเมื่อส่ง `Options.Modules` — [module](./modules.md) แต่ละตัว**ต่ออะไรเข้ากับ process นี้
จริงบ้าง**: route, job, cron, queue, health check

ไม่ใช่ "module นี้ทำอะไร" (อ่านจากโค้ดได้) แต่คือสิ่งที่อ่านจากโค้ดไม่ได้ — `worker` role
ที่ไม่เคย mount route ของ module กับ `api` role ที่ไม่เคย arm cron ของมัน ดูปกติดีจากทุกแท็บ
ที่เหลือ

แต่ละ card อ่านคู่กันสองอย่าง: module **ประกาศ**อะไรไว้ (`declares` — optional interface
ที่ type นั้น implement) กับมัน**ต่ออะไรได้จริง**ใน process นี้ ต่างกันตรงนี้คือสิ่งที่ตามหา:

- `routes declared, none attached here` — role นี้ไม่มี HTTP server ให้ mount
- `declares nothing` — module ที่ไม่ได้ต่ออะไรเลย
- **no background work** — module ที่ไม่มี `ILifecycleModule` ซึ่งคือเกือบทุกตัว
  ไม่ใช่ความผิดปกติ ส่วน `not started` (จุดแดง) คือ module ที่มี `Start` แต่ยังไม่ถูกเรียก

### config

ทุก key ที่ koanf โหลดมาได้จริง เรียงตามชื่อ กรองได้ และมีสวิตช์ "เฉพาะ key ที่มีค่า"

### jobs

- job ที่ register ไว้ทั้งหมด: schedule, queue, concurrency policy, จำนวน attempt
  และ **parameter schema** (ตัวเดียวกับที่ admin UI ใช้สร้างฟอร์ม —
  ดู [Defining Jobs](./jobs-defining.md))
- run ล่าสุด กรองตาม job และ status ได้
- คลิกที่ run เพื่อดู params, result, error และ log ของ run นั้น

log จะว่างถ้า job นั้นใช้ `LogOff` (ค่า default) — ดู [Job Store](./jobs-store.md)

ถ้าไม่ได้ส่ง `Registry` หรือ `Store` เข้ามา หน้านี้จะ**บอกว่าขาดอะไร** ไม่ใช่แสดง
list ว่าง เพราะ "service ไม่มี job" กับ "คนที่ mount ลืมส่ง store" เป็นคนละบั๊กกัน
แต่หน้าตาเหมือนกันเป๊ะ

เมื่อเปิด `AllowWrite`:

- **run** — ฟอร์มถูกสร้างจาก `[]ParamField` ของ job นั้นตรงๆ ฟอร์มจึงเสนอ field
  ที่ job รับไม่ได้ไม่ได้ พร้อม idempotency key (กดสองทีได้ run เดียว) และ
  "เก็บ log ทุกบรรทัดของ run นี้" ซึ่งเป็นเหตุผลที่คนกด trigger เองอยู่แล้ว
  ฟอร์มเคารพ schema ที่ประกาศไว้: `Default` ถูกเติมให้ (รวมถึง checkbox และ select),
  `Enum`/`Options` เป็น dropdown, `Min`/`Max`/`Pattern` ติดไปกับ input และ field ที่
  `Required` จะถูกทักก่อนส่ง ไม่ใช่ให้ server ตอบ 400 กลับมา
- **raw JSON** — ใต้ฟอร์มเสมอ สำหรับ job ที่**ไม่ได้ประกาศ schema** (`reg.Register`
  แทน `core.RegisterJob`) — ก่อนหน้านี้หน้านี้บอกว่า "job นี้ไม่รับ parameter" ทั้งที่มันรับ —
  และสำหรับ params ที่ไม่ใช่ object เช่น job ที่รับ `[]string` ถ้าช่องนี้ไม่ว่าง
  มันจะถูกส่งแทนฟอร์มด้านบน
- **pause / resume** — หยุด job ไม่ให้ถูก schedule หรือ trigger run ที่อยู่ในคิวแล้ว
  ไม่ถูกแตะ
- **cancel** — เฉพาะ run ที่ยังไม่จบ; run ที่ยังอยู่ในคิวจะถูกยกเลิกทันทีโดยไม่เคยเริ่ม
  ส่วน run ที่กำลังรันจะถูก *ขอ* ให้หยุด (handler ต้องเช็ค `ctx.Stopping()` เอง)
- **replay** — เฉพาะ run ที่จบแล้ว สร้าง run **ใหม่** จาก params เดิม ของเดิมไม่ถูกทับ
  เพราะประวัติต้องไม่ถูกเขียนทับ เปิดฟอร์มเดียวกับ run โดยเติม params ของ run เดิมไว้ให้
  **แก้ได้** เพราะครึ่งหนึ่งของเหตุผลที่คน replay คือ input ตัวใดตัวหนึ่งผิด
  ฟอร์มที่ไม่ถูกแตะเลยยังหมายถึง "params เดิม" เสมอ ไม่ใช่ "ไม่ส่ง params"
  ปุ่มจะมีทีละอย่าง ไม่มีทั้งคู่ — มันคือเจตนาเดียวกัน
  ที่คนละจุดของชีวิต run และการเสนอปุ่มผิดตัวคือที่มาของ "กด cancel run ที่จบไปแล้ว
  แล้วไม่มีอะไรเกิดขึ้น"

::: warning
`Runner` ที่ส่งมาไม่จำเป็นต้องเป็นตัวที่รัน worker — API process ที่ job ไปรันที่
worker ส่ง runner ของตัวเองได้ เพราะ trigger คือการเขียนลง queue ที่แชร์กัน
:::

### trace

request N อันหลังสุด และ **ทุกบรรทัดที่ถูก log ระหว่าง request นั้นกำลังทำงาน** —
SQL ที่ repository ยิง, HTTP call ที่ client เรียก, completion ที่ handler ขอ,
ปนกับบรรทัดของ service เอง เรียงตามเวลาพร้อม offset จากต้น request

มันตอบคำถามที่ log file ตอบได้แย่: ไม่ใช่ "ตอน 14:03:11 เกิดอะไรขึ้น" แต่คือ
"**request นี้** ทำอะไรไปบ้าง ตามลำดับ" ซึ่งปกติต้อง grep request id ออกจาก
output ที่ปนกันของทุก request ที่วิ่งพร้อมกัน

ต้องต่อสองที่ ถึงจะทำงาน:

```go
trace := devtools.NewTrace(devtools.TraceOptions{
    Size:      200,              // จำกี่ request
    MaxEvents: 200,              // กี่บรรทัดต่อ request
    Only:      []string{"/egp"}, // เก็บเฉพาะ prefix นี้ (ว่าง = ทุกอัน)
})

app, _ := core.NewApp(env, core.WithLogTap(trace.Log))  // ← ที่หนึ่ง
devtools.Mount(srv, devtools.Options{Trace: trace})      // ← ที่สอง
```

**trace เห็นแค่สิ่งที่ถูก log จริง** บรรทัดที่ต่ำกว่า `LOG_LEVEL` ไม่เคยถูกเขียน และ
logger ของ SQL / outgoing HTTP / model มี level ของตัวเอง (`DB_LOG_LEVEL`,
`HTTP_LOG_LEVEL`, `AI_LOG_LEVEL` — default `warn` คือเฉพาะที่ช้ากับที่พัง) ถ้าอยาก
เห็นทุก query ต้องตั้งเป็น `info` timeline ที่ว่างเปล่าจะบอกเรื่องนี้ไว้ในหน้าเว็บด้วย

ของที่ trace **ไม่** เก็บ:

- request ของ devtools เอง กับ `/healthz` `/readyz` — panel ที่บันทึกตัวเองจะเต็ม
  ไปด้วยตัวเอง
- บรรทัดที่ไม่ได้อยู่ใน request (boot, job run, scheduler) — ไม่มี request id
  ให้จัดกลุ่ม
- cache hit/miss — framework ไม่ได้ log มัน

buffer อยู่ใน memory ล้วน ไม่เขียนลงที่ไหน หายเมื่อ process restart และเก่าสุดถูก
ทิ้งเมื่อเต็ม

::: danger
trace เก็บ path กับ log attributes ไว้ใน memory ซึ่งแปลว่าอาจมี PII จึงเป็น
opt-in สองชั้น (ต้องสร้างเองและต้องส่งเข้าทั้ง App และ Mount) และไม่ควรเปิดบน
production ที่ไม่มี `Auth`
:::

### health

รัน readiness probe ตัวเดียวกับ `/readyz` ([Health & Readiness](./health.md)) พร้อม
รายละเอียด error ของแต่ละ dependency

probe เปิด connection จริงไปทุก dependency จึงไม่รันอัตโนมัติตอนโหลดหน้า — ต้องกดเอง

## next run: "ทำไม job ไม่รัน"

แท็บ jobs มีคอลัมน์ **next run** ที่แยกสามกรณีซึ่งช่องว่างๆ แยกให้ไม่ได้:

| ที่เห็น | แปลว่า |
|---|---|
| เวลา + "in 3h 20m" | armed อยู่ใน process นี้ และจะยิงตอนนั้น |
| `elsewhere` | process นี้ไม่ได้ arm cron เลย (เช่น API replica ที่ worker อยู่คนละ process) |
| **`not armed`** | process นี้ arm cron อยู่ แต่ job นี้ไม่ได้ถูก arm — **อันนี้คือบั๊ก** |
| `—` | job นี้เป็น manual ไม่มี schedule |

เวลามาจาก gocron ผ่าน `runner.NextRun(name)` จึงเป็นเวลาจริงที่ scheduler จะยิง
ไม่ใช่การตีความ cron expression ซ้ำอีกรอบ

## logs สดของ job ที่กำลังรัน

เปิด run ที่ยังไม่จบ แล้ว panel จะต่อ SSE ไปที่ `{prefix}/api/runs/:id/tail`
แล้วบรรทัดใหม่จะไหลเข้ามาเรื่อยๆ

อ่านจาก **live hub ของ runner ไม่ใช่จาก store** ซึ่งเป็นประเด็นทั้งหมด: log policy
default คือ `LogOff` แปลว่า run ที่กำลังทำงานไม่ได้เขียนอะไรลง store เลย ถ้าอ่านจาก
store จะเห็น panel ว่างเปล่าสำหรับ run ที่คนกำลังนั่งดูอยู่พอดี — ซึ่งคือ run ที่ยังไม่จบ
และเป็นตัวที่เขากังวล

ข้อจำกัด: ใช้ได้เฉพาะใน **process ที่รัน job นั้นจริงๆ** API replica ที่ worker อยู่
คนละที่ไม่มี hub ให้อ่าน stream จะปิดทันทีพร้อมบอกเหตุผล แทนที่จะค้างเงียบๆ ซึ่งอ่าน
แล้วเหมือน job ไม่ทำอะไร

connection ถูกปิดที่ 10 นาที (`DefaultTailTimeout`) พร้อม keepalive ทุก 20 วินาที
กัน proxy ตัด และ `X-Accel-Buffering: no` กัน nginx buffer ทั้ง stream ไว้ส่งทีเดียวตอนจบ

## pprof

```go
devtools.Mount(srv, devtools.Options{Pprof: true})
```

เปิด `net/http/pprof` ไว้ที่ `{prefix}/debug/pprof` **หลัง guard เดียวกับที่เหลือ**

overview บอกได้ว่ามี goroutine กี่ตัว แต่บอกไม่ได้ว่าตัวไหน ซึ่งคือคำถามจริงเวลา
process ค้าง วิธีที่คนทำกันคือเปิด listener ตัวที่สองใน `main` แล้วลืมปิด — อันนี้คือ
ข้อมูลชุดเดียวกันหลังล็อกที่มีอยู่แล้ว

```sh
go tool pprof https://api.example.com/_dev/debug/pprof/heap
```

**ปิดเป็นค่าเริ่มต้น** ไม่ใช่เพราะสิ่งที่มันเปิดเผย (แท็บ config เปิดเผยมากกว่า) แต่เพราะ
CPU/block profile กินเวลาจริงบน process ที่รันอยู่ และ `/debug/pprof/profile` ค้าง
request ไว้ 30 วินาทีระหว่างเก็บ — ถ้า `HTTPOptions.WriteTimeout` สั้นกว่านั้นจะถูกตัด
ทิ้งโดยไม่ได้อะไรเลย ใช้ `?seconds=` ลดลงได้

## Options

| field | ค่า default | ความหมาย |
|---|---|---|
| `Prefix` | `/_dev` | base path ทั้ง UI และ API (`"admin/debug"`, `"/admin/debug/"` ให้ผลเดียวกัน) |
| `BasicAuth` | `APP_DEVTOOLS_*` | user/password ที่ browser ถาม — วิธีที่สั้นที่สุดในการมี guard |
| `Auth` | ไม่มี | echo middleware ที่ครอบทุก route — ใช้แทนหรือใช้คู่กับ `BasicAuth` ก็ได้ |
| `Runner` | `nil` | เปิดแท็บ jobs และเป็นตัวที่ action ทำงานผ่าน (`Registry`/`Store` ดึงจากมันเอง) |
| `AllowWrite` | `false` | เปิด trigger / cancel / replay / pause / resume |
| `Trace` | `nil` | เปิดแท็บ trace (ต้องคู่กับ `core.WithLogTap`) |
| `Pprof` | `false` | เปิด `net/http/pprof` ที่ `{prefix}/debug/pprof` |
| `Registry` | `nil` | อ่าน job อย่างเดียว สำหรับคนที่มีมันโดยไม่มี `Runner` |
| `Store` | `nil` | อ่าน run และ log อย่างเดียว |
| `Queue` | `nil` | เพิ่มความลึกของคิวใน overview |
| `Health` | `nil` | ปรับ probe ของแท็บ health (ค่า default คือสิ่งที่ `App` ถืออยู่ พร้อม details) |
| `Modules` | `nil` | เปิดแท็บ modules — ส่ง `*core.ModuleSet` ตัวเดียวกับที่ให้ `Runner` |

## JSON API

UI เป็นแค่ผู้ใช้รายหนึ่งของ API — ทุก endpoint เรียกตรงได้ ถ้าอยากได้ CLI หรือ
dashboard ของตัวเอง

```
GET    {prefix}/api/overview             capability, runtime, การต่อสายของ job, สวิตช์ที่เปิดอยู่
GET    {prefix}/api/routes               ทุก route พร้อมชื่อ handler
GET    {prefix}/api/config               ทุก config key (secret ถูกกัน)
GET    {prefix}/api/modules              module แต่ละตัวประกาศอะไร และต่ออะไรได้จริง
GET    {prefix}/api/health               รัน readiness probe
GET    {prefix}/api/jobs                 job ที่ register ไว้ พร้อม parameter schema
GET    {prefix}/api/runs                 run (?job= &status= &queue= &trigger= &page= &limit=)
GET    {prefix}/api/runs/:id             run เดียว
GET    {prefix}/api/runs/:id/logs        log ของ run (?after=<seq> &limit=)
GET    {prefix}/api/runs/:id/tail        SSE: log สดของ run ที่กำลังทำงาน
GET    {prefix}/api/pprof                รายการ profile + base path (ต้องมี Pprof)
GET    {prefix}/debug/pprof/…            net/http/pprof (ต้องมี Pprof)
GET    {prefix}/api/trace                request ล่าสุด (ไม่มี event)
GET    {prefix}/api/trace/:id            request เดียว พร้อม timeline
DELETE {prefix}/api/trace                ล้าง buffer

# ต้องมี AllowWrite
POST   {prefix}/api/jobs/:name/trigger   {"params":…,"idem_key":"…","capture_logs":true}
POST   {prefix}/api/jobs/:name/pause
POST   {prefix}/api/jobs/:name/resume
POST   {prefix}/api/runs/:id/cancel      {"reason":"…"}
POST   {prefix}/api/runs/:id/replay      {"params":…,"capture_logs":true}
```

`params` เป็น JSON อะไรก็ได้ ไม่จำเป็นต้องเป็น object เพราะ parameter type ของ job
เป็น type อะไรก็ได้ — job ที่รับ `[]string` จึง trigger จากที่นี่ได้ ไม่ส่งมา (หรือส่ง `{}`)
แปลว่า "ไม่ระบุ" คือ trigger จะได้ default ของ job เอง และ replay จะได้ params ของ run เดิม

error ใช้ shape เดียวกับ error ของ framework ทุกตัว

`DELETE /api/trace` ไม่ต้องใช้ `AllowWrite` — การล้าง buffer สำหรับ debug ไม่ได้
เปลี่ยนอะไรของ service และทางเลือกอื่นคือให้คน restart process เพื่อให้ trace อ่านรู้เรื่อง

## หน้า UI

HTML, CSS และ JavaScript อยู่ในไฟล์เดียวที่ `go:embed` เข้าไปใน binary

ไม่มี build step และไม่มี CDN โดยตั้งใจ — Go library ที่ต้องลง node ก่อนถึงจะ compile
ได้คือ library ที่ไม่มีใครอัปเกรด และหน้า debug ที่ต้องโหลด framework จากอินเทอร์เน็ต
คือหน้าที่เปิดไม่ขึ้นบนเครือข่ายที่ต้องใช้มันมากที่สุด — staging box ที่ไม่มี egress

## API ของ core ที่เกี่ยวข้อง

Devtools อ่านผ่าน API สาธารณะทั้งหมด — ใช้เองได้โดยไม่ต้อง mount อะไร

```go
// capability ทุกตัว พร้อมสถานะ pool ของ SQL — ไม่มี I/O
for _, c := range app.Capabilities() {
    fmt.Println(c.Kind, c.Name, c.Enabled, c.Detail)
}

// "METHOD /path" -> ชื่อ handler
names := srv.HandlerNames()

// job นี้จะยิงเมื่อไหร่ใน process นี้ และ process นี้ arm cron ไว้ไหม
next, ok := runner.NextRun("nightly-report")
armed := runner.Scheduled()

// principal ที่ auth middleware แปะไว้ อ่านได้ก่อนที่ framework context จะมี
user := core.ContextUserOf(c)

// request id ที่ correlate log / Sentry / trace เข้าด้วยกัน
id := core.RequestID(ctx)
ctx = core.WithRequestID(ctx, id)   // พางานที่ออกนอก request ให้ยังผูกกันอยู่
```

`Capabilities()` รายงานสิ่งที่ **wire ไว้** ไม่ใช่สิ่งที่ **ต่อติด** — คำถามหลังคือ
`core.CheckHealth`

`WithLogTap` เป็น hook ที่ trace ยืนอยู่บน: ทุกบรรทัดที่ logger เขียน ถูกส่งให้
observer พร้อม context ที่มันถูกเขียนภายใต้ — ไม่ใช่ทางสำหรับส่ง log ไป
aggregator (นั่นคือ slog handler ผ่าน `WithLogger`) มันครอบที่ระดับ
`slog.Handler` ไม่ใช่ครอบ `ILogger` เพราะ `ILogger` ที่ถูกห่อจะไม่ใช่ type ของ
framework อีกต่อไป แล้ว `ctx.Log()` จะเลิกผูก request id เข้ากับบรรทัดแบบเงียบๆ
