# Requester (HTTP client)

`core.IRequester` — outbound HTTP, backed by [go-resty](https://github.com/go-resty/resty).
`core.Requester(ctx)` returns that client bound to the caller's context
(connection pool kept alive, one client per App).

The API is deliberately thin: you build requests with the **full go-resty API**
via `R()`, and run them with `Send`, which is the only thing the framework wraps —
it binds the request context and converts **every** failure (transport error or
non-2xx status) into a `core.IError`.

> หน้านี้ตอบว่า**ยิงยังไง** ส่วนโค้ดนั้นควรอยู่ที่ไหน — module ไหนเป็นเจ้าของ upstream,
> module อื่นเรียกยังไง, เมื่อไหร่ควรมี `client/` — อยู่ที่
> [External services](./external-services.md)

## `core.Requester(ctx)` ไม่ใช่ `ctx.Requester()`

การยิง HTTP ออกไปข้างนอกไม่ใช่ *capability ของ request* แบบ `ctx.DB()` — มันคือ
สิ่งที่โค้ดทำ โดยเอา deadline และ trace ของ request ติดไปด้วย มันจึงเป็นฟังก์ชัน
ที่รับ context เหมือน `repository.New[T](ctx)` ไม่ใช่ method บน `IContext`

```go
resp, err := core.Requester(c).Send(req, http.MethodGet, url)
```

`ctx` เป็นอะไรก็ได้ที่พา App มาด้วย — `IContext` จาก handler หรือ job, หรือ
`context.Context` ตัวไหนก็ได้ที่แตกมาจากมัน ฟังก์ชันชั้นลึกที่รับแค่
`context.Context` จึงยิง request ได้เองโดยไม่ต้องรับ `IContext` เพิ่ม:

```go
func fetchRate(ctx context.Context, pair string) (*Rate, core.IError) {
    r := core.Requester(ctx)     // client ตัวเดียวกับที่ App ตั้งไว้

    var out Rate
    if _, err := r.Send(r.R().SetResult(&out), http.MethodGet, rateURL(pair)); err != nil {
        return nil, err
    }

    return &out, nil
}
```

ที่ได้เท่าเดิมทุกอย่าง: client ตัวเดิม (pool เดิม, config เดิม), ผูก deadline/cancel
ของ context นั้น และ **Sentry instrumentation ครบ** — breadcrumb `http.client`,
child span และ trace header ที่ส่งต่อไป service ปลายทาง

> context ที่ไม่ได้มาจาก `App` เลย (เช่น `context.Background()` ใน script หรือ
> test เล็กๆ) จะได้ client กลางที่ timeout ตาม default แทนที่จะได้ `nil` —
> call site จึงไม่ต้องเช็ค nil แต่ตัวนั้น**ไม่มี instrumentation** เพราะไม่มี App
> ให้ถามว่าตั้งค่าไว้ยังไงและไม่มี tracker ให้รายงาน

## Basic usage

```go
r := core.Requester(ctx)

var out UserDTO
resp, err := r.Send(r.R().SetResult(&out), http.MethodGet, "https://api/users/1")
if err != nil {
    return err // IError — return it straight from a handler
}
_ = resp // *resty.Response (status, headers, raw body, trace info)
```

`err` is a `core.IError`:
- transport failure → code `NETWORK_ERROR`, status 500
- non-2xx status → the response status, reusing the remote `code`/`message` when
  the JSON body provides them
- success → `nil`

## `SetError` — อ่าน error body ของ upstream

`SetResult` คือ body ตอนสำเร็จ `SetError` คือ body ตอนพัง — resty decode ให้
อัตโนมัติเมื่อ status > 399 และ content-type เป็น JSON/XML:

```go
type chargeError struct {
    Code        string `json:"code"`
    Message     string `json:"message"`
    DeclineCode string `json:"decline_code"`   // สิ่งที่ code/message ไม่ได้พามา
    Retryable   bool   `json:"retryable"`
}

func (s paymentService) Charge(input *ChargePayload) (*Charge, core.IError) {
    r := core.Requester(s.ctx)

    var out Charge
    var fail chargeError

    _, err := r.Send(
        r.R().SetBody(input).SetResult(&out).SetError(&fail),
        http.MethodPost, s.baseURL+"/v1/charges",
    )
    if err != nil {
        // err เป็น IError อยู่แล้ว (422 + CARD_DECLINED จาก body)
        // ส่วน fail คือรายละเอียดที่ต้องใช้ "ตัดสินใจ"
        if fail.Retryable {
            return nil, emsgs.PaymentUnavailable   // 503 ของเราเอง: ให้ client ลองใหม่
        }

        s.ctx.Log().Warn("charge declined",
            "decline_code", fail.DeclineCode, "amount", input.Amount)

        return nil, emsgs.CardDeclined.WithFields(map[string]any{
            "decline_code": fail.DeclineCode,
        })
    }

    return &out, nil
}
```

จุดสำคัญของการใช้คู่กัน:

| | ได้อะไร |
|---|---|
| ค่าที่ `Send` คืน | `core.IError` — status ของ upstream + `code`/`message` จาก body พร้อม `return` ออกจาก handler ได้เลย |
| struct ที่ `SetError` ผูกไว้ | body เต็มๆ ตามชนิดที่เราประกาศ — field ที่ไม่ใช่ `code`/`message` อยู่ที่นี่เท่านั้น |
| struct ที่ `SetResult` ผูกไว้ | **ไม่ถูกแตะ** เมื่อ request พัง — ค่าที่อ่านได้คือ zero value |

> `resp.Error()` คืน pointer ตัวเดียวกับที่ส่งเข้า `SetError` — จะอ่านจากตัวแปรตรงๆ
> หรือจาก response ก็ได้ค่าเดียวกัน
>
> ⚠️ ถ้า upstream ตอบ error เป็น `text/plain` หรือ HTML **resty จะไม่ decode ให้**
> (มันดูจาก content-type) `fail` จะเป็น zero value — ตัว `IError` จาก `Send` ยังใช้ได้
> ตามปกติ และดู body ดิบได้จาก `resp.Body()`

โดยทั่วไป **ไม่ต้องใช้ `SetError`** ถ้า upstream ตอบ `{code, message}` แบบมาตรฐาน —
`Send` แปลงให้ครบแล้ว ใช้มันตอนที่ต้องอ่าน field เพิ่มเพื่อ *ตัดสินใจ* เท่านั้น
(retry ไหม, จะบอก user ว่าอะไร, จะ map เป็น error code ตัวไหนของเรา — ดู
[Service Errors](./service-errors.md))

## Full go-resty when building

`R()` returns a `*resty.Request` already bound to the context, so anything resty
offers is available; `Send` just executes and wraps the error:

```go
// headers, query, auth, typed result
var out UserDTO
resp, err := r.Send(
    r.R().
        SetHeader("X-Token", token).
        SetQueryParam("q", "search").
        SetAuthToken(bearer).
        SetResult(&out),
    http.MethodGet, url,
)

// JSON body
resp, err = r.Send(r.R().SetBody(payload).SetResult(&out), http.MethodPost, url)
```

## Form (`application/x-www-form-urlencoded`)

`SetFormData` — resty ตั้ง content-type ให้เอง ไม่ต้องเซ็ต header:

```go
type token struct {
    AccessToken string `json:"access_token"`
    ExpiresIn   int64    `json:"expires_in"`
}

func (s authService) clientCredentials() (*token, core.IError) {
    r := core.Requester(s.ctx)

    var out token
    var fail oauthError

    if _, err := r.Send(
        r.R().
            SetFormData(map[string]string{
                "grant_type":    "client_credentials",
                "client_id":     s.clientID,
                "client_secret": s.clientSecret,   // ⚠️ ดูหมายเหตุด้านล่าง
                "scope":         "rates:read",
            }).
            SetResult(&out).
            SetError(&fail),
        http.MethodPost, s.tokenURL,
    ); err != nil {
        return nil, s.ctx.NewError(err, emsgs.UpstreamAuthFailed)
    }

    return &out, nil
}
```

> **secret ที่อยู่ใน form ปลอดภัยกว่าที่อยู่ใน query string** — query string ติดไป
> กับ URL ซึ่งลง access log ของทุก proxy ระหว่างทางและติดไปกับ breadcrumb ของ
> Sentry ด้วย (framework scrub key ที่เข้าข่ายให้ แต่ก็ยังไม่ควรใส่ตั้งแต่แรก)
> ส่วน body ไม่ถูก log

ค่าซ้ำ key เดียวกันใช้ `SetFormDataFromValues(url.Values{...})`

## File upload (`multipart/form-data`)

เคสที่เจอบ่อยที่สุดคือ **ส่งต่อไฟล์ที่ client เพิ่ง upload เข้ามา** ไปยัง service
อื่น — ทำได้โดยไม่ต้องเขียนลง disk และไม่ต้องอ่านเข้า memory:

```go
func (m KYCHandler) Upload(c core.IHTTPContext) error {
    fh, err := c.FormFile("document")           // *multipart.FileHeader
    if err != nil {
        return errmsgs.BadRequest.WithMessage("document is required")
    }

    return service.NewKYCService(c).Submit(fh)
}
```

```go
func (s kycService) Submit(fh *multipart.FileHeader) core.IError {
    src, err := fh.Open()
    if err != nil {
        return s.ctx.NewError(err, errmsgs.InternalServerError)
    }
    defer src.Close()

    r := core.Requester(s.ctx)

    var out submitResult

    _, sErr := r.Send(
        r.R().
            // stream ตรงจาก reader — ไม่ io.ReadAll ไม่เขียนไฟล์ชั่วคราว
            // ไฟล์ 200MB จึงไม่กลายเป็น 200MB ใน heap
            SetFileReader("document", fh.Filename, src).
            SetFormData(map[string]string{
                "citizen_id": s.callerID,
                "doc_type":   "ID_CARD",
            }).
            SetResult(&out),
        http.MethodPost, s.baseURL+"/v1/documents",
    )
    if sErr != nil {
        return s.ctx.NewError(sErr, emsgs.KYCUnavailable)
    }

    return nil
}
```

แบบอื่นที่มีให้:

```go
r.R().SetFile("document", "/tmp/id-card.png")                       // จาก path
r.R().SetFiles(map[string]string{"front": frontPath, "back": backPath})

// กำหนด content-type ของ part เอง — บาง upstream ปฏิเสธ
// application/octet-stream ที่ resty เดาให้
r.R().SetMultipartField("document", fh.Filename, fh.Header.Get("Content-Type"), src)
```

⚠️ **`SetTimeout` ของ client เป็นเพดานของทั้ง request รวมเวลาส่ง body** — default
คือ 30 วินาที ไฟล์ใหญ่บนเน็ตช้าจึงถูกตัดกลางคัน และ context ที่ยาวกว่าก็ไม่ช่วย
เพราะอันไหนหมดก่อนชนะ ถ้ามีงาน upload หนัก ให้แยก client ของมันเอง:

```go
uploads := core.NewRequesterWithClient(resty.New().SetTimeout(10 * time.Minute))
```

## ดาวน์โหลดไฟล์

```go
// เขียนลงไฟล์ตรงๆ
_, err := r.Send(r.R().SetOutput("/tmp/report.pdf"), http.MethodGet, url)

// หรือ stream เองทีละ chunk (proxy ต่อให้ client, ตรวจ checksum ระหว่างทาง)
resp, err := r.Send(r.R().SetDoNotParseResponse(true), http.MethodGet, url)
if err != nil {
    return err
}
defer resp.RawBody().Close()

_, _ = io.Copy(c.Response(), resp.RawBody())
```

⚠️ `SetDoNotParseResponse(true)` แปลว่า **body ไม่ถูกอ่าน** — `Send` จึงอ่าน
`{code, message}` จาก error body ไม่ได้ error ที่ได้ตอน upstream ตอบ 4xx/5xx จะ
เป็น `HTTP_ERROR` + status เท่านั้น ถ้าต้องการ code ของ upstream ด้วย ให้เช็ค
`resp.StatusCode()` แล้วอ่าน `resp.RawBody()` เอง

## Log ของ call ขาออก

ทุก call ที่ยิงผ่าน `core.Requester(ctx)` ถูกบันทึกลง log ของ unit นั้น —
**ไม่ต้องมี Sentry** และไม่ต้องเขียนเองสักบรรทัด:

```json
{"level":"DEBUG","msg":"GET api.example.com/v1/rates 200 142ms","method":"GET","url":"https://api.example.com/v1/rates?base=THB","status":200,"duration_ms":142,"component":"http.client","request_id":"raBcQ…"}
{"level":"WARN","msg":"POST api.example.com/v1/charges 200 2310ms","duration_ms":2310,"threshold_ms":1000,"component":"http.client","request_id":"raBcQ…"}
{"level":"ERROR","msg":"GET api.example.com/v1/rates 502 31ms","status":502,"component":"http.client","request_id":"raBcQ…"}
{"level":"ERROR","msg":"GET api.example.com/v1/rates failed","err":"dial tcp: connection refused","component":"http.client","request_id":"raBcQ…"}
```

**message บอกเรื่องได้ด้วยตัวเอง** รูปแบบเดียวกับ access line ของ request ขาเข้า
(`METHOD host/path status durationms`) — เพราะ Sentry Logs และ JSON pipeline โชว์
แค่ message เป็นหัวเรื่อง ถ้าเขียนว่า `http call` เฉยๆ มันไม่ตอบสักคำถามที่กำลังกวาดตาหา:
ยิงไปที่ไหน เรื่องอะไร สำเร็จไหม ช้าแค่ไหน — ส่วนค่าเดิมยังอยู่ครบใน field ซึ่งเป็นตัวที่ใช้ filter

`request_id` เป็นตัวเดียวกับ access line ของ request นั้น — จึงไล่ได้ว่า request
ที่ช้าไป 3 วินาที เสียเวลาไปกับ upstream เจ้าไหน และ `component=http.client` คือ
ตัวกรองเอา call ขาออกทั้งหมด

> ใน message ตัด scheme และ **query string** ทิ้ง เหลือ host+path — Sentry จึงจัดกลุ่ม
> call ที่ยิงไป endpoint เดียวกันเข้าด้วยกัน แทนที่จะแตกเป็นพันกลุ่มตาม parameter
> และไม่มีทางที่ secret ใน query จะไปโผล่บนหัวเรื่องที่แสดงทุกที่ (url เต็มที่ scrub
> แล้วอยู่ใน field `url`)

เลือก level แบบเดียวกับ [SQL logger](./database-logging.md):

| เหตุการณ์ | Level |
|---|---|
| ต่อไม่ติด / timeout | `Error` + `err` |
| upstream ตอบ 5xx | `Error` |
| ช้ากว่า threshold (default 1s) | `Warn` + `threshold_ms` |
| นอกนั้น (รวม 4xx) | `Debug` |

> **ทำไม 4xx ถึงเป็น Debug ไม่ใช่ Warn** — 404 จาก upstream คือ *คำตอบ* ไม่ใช่
> ความล้มเหลว (เช่นถามว่า "มี user นี้ไหม") service ที่ถามเป็นคนตัดสินว่ามันแปลว่า
> อะไร แล้ว log Warn ของตัวเองถ้ามันสำคัญ — เหมือนที่ GORM logger ไม่นับ
> `ErrRecordNotFound` เป็น error

| Key | ค่า |
|---|---|
| `HTTP_LOG_LEVEL` | `silent` / `error` / `warn` (default) / `debug` — ไม่ตั้ง = ตามLOG_LEVEL |
| `HTTP_LOG_BODY` | `true` เพื่อใส่ body ทั้งขาส่งและขารับ (default ปิด) |

`LOG_LEVEL=debug` จึงเห็นทุก call โดยไม่ต้องตั้งอะไรเพิ่ม และตั้ง
`HTTP_LOG_LEVEL=silent` เพื่อปิดเฉพาะส่วนนี้ตอนที่ต้อง debug อย่างอื่น

### Body

```
APP_HTTP_LOG_BODY=true
```

```json
{"level":"DEBUG","msg":"http call","method":"POST","url":"https://api/v1/login",
 "req_body":"{\"email\":\"a@b.co\",\"password\":\"[redacted]\"}",
 "res_body":"{\"id\":\"u1\",\"token\":\"[redacted]\"}","status":200}
```

- **ปิดเป็น default** — body พก credential และข้อมูลส่วนบุคคล และเป็นวิธีที่ง่ายที่สุด
  ที่จะเปลี่ยน log store ให้กลายเป็นที่เก็บความลับ เปิดตอนต่อ integration ใหม่
  แล้วปิดกลับ
- ผ่าน scrubber ชุดเดียวกับ Sentry — key ที่เข้าข่าย (`password`, `token`,
  `secret`, …) กลายเป็น `[redacted]` ทั้งใน request และ response
- ตัดที่ 2KB ต่อ body
- **ไฟล์ที่ upload ไม่ถูก dump** — multipart เก็บ part ไว้คนละที่กับ `Body` จึงไม่มี
  ทางที่ไฟล์ 200MB จะไหลลง log
- `SetDoNotParseResponse(true)` → ไม่มี `res_body` เพราะ body ยังไม่ถูกอ่าน (การอ่าน
  ตรงนี้จะกินสตรีมที่ caller กำลังจะใช้)

URL ถูก scrub เสมอไม่ว่าจะเปิด body หรือไม่ — `?api_key=…` ใน log คือ credential
ใน log store ซึ่งมีคนอ่านได้มากกว่า database

## Interface

```go
type IRequester interface {
    R() *resty.Request                                                  // full go-resty builder, ctx-bound
    Send(req *resty.Request, method, url string) (*resty.Response, IError) // execute → IError
    WithContext(ctx context.Context) IRequester
    Resty() *resty.Client                                               // shared client — configure at startup
}
```

## Client-level configuration (retries, TLS, base URL, middleware)

Configure the shared resty client once at startup and wire it in:

```go
rc := resty.New().
    SetTimeout(10 * time.Second).
    SetRetryCount(3).
    SetBaseURL("https://api.example.com").
    OnBeforeRequest(func(c *resty.Client, req *resty.Request) error { /* trace */ return nil })

app, _ := core.NewApp(env, core.WithRequester(core.NewRequesterWithClient(rc)))
```

Then `core.Requester(ctx)` uses that client, still ctx-bound per call.

## Custom timeout

จำกัดเวลาของ call เดียวโดยไม่แตะ timeout ของ client ที่ใช้ร่วมกัน — **แตกจาก
context ของ request เสมอ**:

```go
ctx, cancel := context.WithTimeout(c, 5*time.Second)   // c คือ IContext ของ request
defer cancel()

r := core.Requester(ctx)
r.Send(r.R(), http.MethodGet, url)
```

⚠️ **อย่าเริ่มจาก `context.Background()`** — มันไม่ได้พา App มาด้วย จึงได้ client
กลางที่ไม่มี config และไม่มี instrumentation แทนที่จะได้ตัวที่ตั้งไว้ตอน startup
และที่แย่กว่านั้นคือ call นั้นหลุดจาก cancellation ของ request ไปเลย —
request ที่ client ทิ้งไปแล้วจะยังค้างสาย upstream ต่อจนครบ timeout

การแตกจาก `c` ยังทำให้ deadline ตัวไหนสั้นกว่าชนะเสมอ: request ที่เหลือเวลา 2
วินาทีจะไม่รอ call นี้ถึง 5 วินาที

## Best practices

- **ตั้ง timeout ตามปลายทางแต่ละเจ้า** ไม่ใช่ปล่อย 30 วินาทีเท่ากันหมด — API ที่ปกติตอบ
  ใน 200ms ไม่ควรได้เวลา 30 วินาทีก่อนจะยอมแพ้
- **retry เฉพาะที่ปลอดภัย**: `GET`/`PUT`/`DELETE` ที่ idempotent เจอ 429 หรือ 5xx
  — `POST` ที่สร้างของต้องมี idempotency key ของปลายทางก่อนถึงจะ retry ได้
- **ตั้งค่า client ตอน boot ครั้งเดียว** (base URL, retry, TLS, header ประจำ) ผ่าน
  `Resty()` ไม่ใช่ตั้งใหม่ทุก request
- **ใช้ `core.Requester(ctx)` เสมอ** เพื่อให้ call ขาออกพก deadline และ trace ของ request
  นั้นไปด้วย — client ที่สร้างเองไม่มีทั้งสองอย่าง
- **อ่าน error body ของ upstream ด้วย `SetError`** แล้วแปลงเป็น `IError` ของเรา อย่าโยน
  ข้อความดิบของเขาให้ผู้ใช้ของเรา
- **อย่ายิง HTTP ออกไปข้างใน transaction** — lock ถูกถือไว้ตลอดเวลาที่ปลายทางคิด
- **ปลายทางที่ไม่เสถียรควรมี fallback หรือคิว** ไม่ใช่ retry ถี่ๆ ที่ทำให้มันฟื้นช้าลง
- **อย่า log body ที่มี token หรือข้อมูลส่วนบุคคล** — [log ของ call ขาออก](#log-ของ-call-ขาออก)
  ตัดให้ระดับหนึ่งแล้ว แต่คนเขียนโค้ดคือคนที่รู้ว่า field ไหนอ่อนไหว
