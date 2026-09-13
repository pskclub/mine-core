# Mailer (Email)

`core.IMailer` — SMTP email over [go-mail](https://github.com/wneessen/go-mail).
`core.Mailer(ctx)` คืน handle ที่ผูกกับ request แล้ว การส่งจึงถูกยกเลิกไปพร้อมกับงานที่สั่งมัน

> เป็น function ไม่ใช่ `ctx.Mailer()` ด้วยเหตุผลเดียวกับ
> [`core.Requester`](./requester.md) — ส่งเมลคือ "สิ่งที่โค้ดไปทำ" ไม่ใช่ capability ที่
> request มี และการรับแค่ `context.Context` ทำให้ domain service ส่งเมลได้โดยไม่ต้อง
> import framework:
>
> ```go
> func (s *NotificationService) SendWelcome(ctx context.Context, u User) error {
>     return core.Mailer(ctx).SendTemplate(msg, "welcome", u)
> }
> ```
>
> ดู [Context & App](./context.md#method-บน-context-หรือ-function-ที่รับ-context)

## Setup

```go
mailer, err := core.NewMailer(env)
app, _ := core.NewApp(env, core.WithMailer(mailer))
```

```
EMAIL_SERVER=smtp.example.com
EMAIL_PORT=587
EMAIL_USERNAME=...
EMAIL_PASSWORD=...
EMAIL_SENDER=no-reply@example.com
EMAIL_SENDER_NAME=Example
EMAIL_TLS_POLICY=mandatory   # mandatory (default) | opportunistic | none
EMAIL_SSL=false              # implicit TLS, สำหรับ port 465
EMAIL_AUTH=auto              # plain | login | cram-md5 | xoauth2 | none | auto
EMAIL_TIMEOUT=30             # วินาที
```

**`EMAIL_TLS_POLICY` default เป็น `mandatory`** — credentials วิ่งบน connection นี้
relay ที่ทำ STARTTLS ไม่ได้คือ relay ที่ควรแก้ ไม่ใช่ที่ควรยอมตาม

**`EMAIL_AUTH=auto`** (default) = `plain` เมื่อมี username, ไม่ authenticate เมื่อไม่มี —
internal relay ที่ไม่รับ auth จะพังถ้าถูกยื่น AUTH ที่มันปฏิเสธ

## Sending

```go
err := core.Mailer(ctx).Send(core.EmailMessage{
    To:      []string{"user@example.com"},
    Cc:      []string{"ops@example.com"},
    Subject: "Welcome",
    HTML:    "<h1>Hello</h1><p>Thanks for signing up.</p>",
    Text:    "Hello — thanks for signing up.",
})
```

ใส่ `HTML`, `Text` หรือทั้งคู่ (ทั้งคู่ = multipart/alternative) **ส่งทั้งสองเมื่อทำได้** —
HTML สำหรับ client ที่ render ได้, text สำหรับที่ไม่ได้ และสำหรับ spam filter ซึ่งนับ
ข้อความที่มีแต่ HTML เป็นคะแนนลบเล็กๆ

ชื่อผู้รับ:

```go
core.EmailMessage{
    ToAddresses: []core.EmailAddress{{Name: "สมชาย ใจดี", Address: "somchai@example.com"}},
    ReplyTo:     "support@example.com",
    Priority:    core.PriorityHigh,
    Headers:     map[string]string{"List-Unsubscribe": "<https://example.com/u/abc>"},
}
```

`To`/`Cc`/`Bcc` (string ล้วน) กับ `ToAddresses`/`CcAddresses`/`BccAddresses` (มีชื่อ) ใช้
ร่วมกันได้ ระบบรวมให้ ชื่อที่มี comma หรืออักขระพิเศษถูก quote ให้เอง — ไม่งั้นมันกลาย
เป็นผู้รับสองคน

## Template

```go
//go:embed templates/email
var emailFS embed.FS

tpl, err := core.NewMailTemplates(emailFS, core.MailTemplateOptions{
    Root:  "templates/email",
    Funcs: template.FuncMap{"money": formatMoney},
})
mailer, err := core.NewMailer(env, core.WithMailTemplates(tpl))
```

ไฟล์ `.html` เข้าชุด HTML, `.txt` เข้าชุด text โดยใช้ **path ที่ตัดนามสกุลออก** เป็นชื่อ
ดังนั้น `welcome.html` กับ `welcome.txt` คือสองส่วนของ template ชื่อ `welcome`:

```
templates/email/
  welcome.html
  welcome.txt
  order/shipped.html      → ชื่อ "order/shipped"
```

```go
err := core.Mailer(ctx).SendTemplate(core.EmailMessage{
    To:      []string{"user@example.com"},
    Subject: "Welcome",
}, "welcome", map[string]any{"Name": "สมชาย"})
```

body ที่ caller เซ็ตเองไว้แล้วจะไม่ถูกทับ — `SendTemplate` เติมเฉพาะที่ยังว่าง

ทุกไฟล์ parse เข้าชุดเดียวกัน layout หรือ partial ที่ define ในไฟล์หนึ่งจึงใช้จากไฟล์อื่นได้:

```html
{{/* templates/email/welcome.html */}}
{{define "content"}}<p>สวัสดี {{.Name}}</p>{{end}}
{{template "layout" .}}
```

### HTML escape

ชุด HTML ใช้ `html/template` ชุด text ใช้ `text/template` — ตั้งใจให้ต่างกัน: HTML body
escape สิ่งที่ interpolate เข้าไป (ชื่อที่มี `<` จึงเขียน markup รอบตัวมันเองไม่ได้) ส่วน
text body ไม่ถูก escape ให้เละ

### Template จาก string

```go
tpl := core.NewMailTemplateSet(template.FuncMap{"upper": strings.ToUpper})
tpl.Add("otp", `<b>{{.Code}}</b>`, `รหัสของคุณคือ {{.Code}}`)
```

### Preview โดยไม่ส่ง

```go
html, text, err := core.Mailer(ctx).Render("welcome", data)
```

## Attachment กับรูปในเนื้อเมล

```go
core.EmailMessage{
    To:   []string{"user@example.com"},
    HTML: `<p>ใบเสร็จแนบมาแล้ว <img src="cid:logo.png"></p>`,
    Attachments: []core.Attachment{
        {Name: "invoice.pdf", Content: pdfBytes, ContentType: "application/pdf"},
        {Name: "report.csv", Reader: s3Object},   // stream ไม่โหลดเข้า memory
    },
    Embeds: []core.Attachment{
        {Name: "logo.png", Content: logoBytes},   // อ้างด้วย cid:logo.png
    },
}
```

`Embeds` เป็น inline part — `ContentID` ที่ว่างจะใช้ชื่อไฟล์แทน ซึ่งคือสิ่งที่ HTML อ้างอยู่แล้ว
`Reader` อ่านครั้งเดียวตอนส่ง จึงส่งไฟล์ที่ใหญ่กว่า memory ได้

## ทดสอบ

```go
m := core.NewMemoryMailer(templates)
app, _ := core.NewApp(env, core.WithMailer(m))

// ... รันสิ่งที่ควรส่งเมล ...

sent := core.SentMail(m)
require.Len(t, sent, 1)
assert.Equal(t, "Welcome", sent[0].Subject)
assert.Contains(t, sent[0].HTML, "สวัสดี สมชาย")

core.ResetMail(m)
```

memory mailer ตรวจ message แบบเดียวกับตัวจริง — message ที่ SMTP server จะปฏิเสธ
จึงทำให้ test fail แทนที่จะผ่าน

## ไม่ได้ตั้งค่า SMTP ไว้

`core.Mailer(ctx)` **ไม่เคยเป็น nil** — ทุกคำสั่งคืน `503 MAILER_DISABLED`
(`errors.Is(err, core.ErrMailerDisabled)`)

mail ไม่ degrade เงียบๆ: password reset ที่ถูกทิ้งไปเฉยๆ คือ user ที่เข้าระบบไม่ได้ โดย
ไม่มีอะไรใน log บอกว่าทำไม

## Error

| code | เมื่อไหร่ |
|---|---|
| `MAIL_NO_RECIPIENT` | ไม่มีผู้รับ — ตรวจก่อน dial |
| `MAIL_NO_BODY` | ไม่มีทั้ง HTML และ Text |
| `MAIL_INVALID_ATTACHMENT` | attachment ไม่มีชื่อ |
| `MAIL_TEMPLATE_NOT_FOUND` | ไม่มี template ชื่อนั้นทั้งสองชุด |
| `MAIL_NO_TEMPLATES` | เรียก `SendTemplate` โดยไม่ได้ลงทะเบียน template |
| `MAILER_DISABLED` | ไม่ได้ตั้ง `EMAIL_SERVER` |

## Interface

```go
type IMailer interface {
    Send(msg EmailMessage) IError
    SendTemplate(msg EmailMessage, name string, data any) IError
    Render(name string, data any) (html string, text string, err IError)
    Ping() IError
    Enabled() bool
    WithContext(ctx context.Context) IMailer
    Close() IError
}
```

`Ping()` เปิด connection แล้วปิด — เป็นวิธีเดียวที่จะรู้ว่า SMTP server ยังตอบและยังยอม
คุยกับ credentials ชุดนี้ [readiness probe](./health.md) ใช้มันให้อัตโนมัติ

## Best practices

- **ส่งเมลผ่าน [job](./jobs.md) ไม่ใช่ในคำขอ** — SMTP ช้าและล่มได้ ผู้ใช้ไม่ควรรอ และ
  การสมัครสมาชิกไม่ควร fail เพราะ mail server ล่ม
- **กันส่งซ้ำที่ระดับงาน** — `IdemKey` ของ job หรือแถวใน database ที่บันทึกว่าส่งแล้ว
  (retry ที่ไม่มีตัวกันคือผู้ใช้ที่ได้เมลเดียวกันห้าฉบับ)
- **template แยกจากโค้ด** และมี fallback แบบ plain text เสมอ — client บางตัวไม่แสดง HTML
- **validate ที่อยู่ผู้รับก่อนส่ง** และตัดที่อยู่ที่ bounce ซ้ำๆ ออกจากรายการ
- **อย่าใส่ token ที่ใช้ได้นานในลิงก์** — ลิงก์ในเมลอยู่ใน inbox ตลอดไป ใช้ token ที่
  หมดอายุเร็วและใช้ได้ครั้งเดียว
- **attachment ใหญ่ให้ส่งเป็นลิงก์** ([presigned URL](./storage-presign.md)) ไม่ใช่แนบไป
  ทั้งไฟล์
- **dev และเทสใช้ `NewMemoryMailer`** — ไม่มีอะไรออกไปข้างนอกโดยไม่ตั้งใจ
- **mailer ไม่ degrade เงียบ** — เมลที่ถูกทิ้งเงียบๆ คือสิ่งที่ไม่มีใครรู้ว่าหาย
