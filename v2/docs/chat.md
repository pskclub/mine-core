# Chat (Slack / Discord / LINE)

`core.IChat` — ส่งข้อความเข้า chat platform `core.Chat(ctx)` คืน handle ที่ผูกกับ request
แล้ว การโพสต์จึงถูกยกเลิกไปพร้อมกับงานที่สั่งมัน

ตอนนี้มี driver เดียวคือ **Slack** (`chat/slack`) — Discord กับ LINE ใช้ interface เดิม
เมื่อเพิ่มเข้ามา

> เป็น function ไม่ใช่ `ctx.Chat()` ด้วยเหตุผลเดียวกับ [`core.Mailer`](./mailer.md) —
> การโพสต์ข้อความคือ "สิ่งที่โค้ดไปทำ" ไม่ใช่ capability ที่ request มี และการรับแค่
> `context.Context` ทำให้ domain service โพสต์ได้โดยไม่ต้อง import framework:
>
> ```go
> func (s *DeployService) Announce(ctx context.Context, d Deploy) error {
>     _, err := core.Chat(ctx).Send(core.ChatMessage{Text: "deployed " + d.Version})
>     return err
> }
> ```
>
> ดู [Context & App](./context.md#method-บน-context-หรือ-function-ที่รับ-context)

## Setup

```go
import "github.com/pskclub/mine-core/v2/chat/slack"

chat, err := slack.New(env)
app, _ := core.NewApp(env, core.WithChat("default", chat))
```

```
CHAT_SLACK_TOKEN=xoxb-...        # bot token (ต้องมี scope chat:write)
CHAT_SLACK_CHANNEL=#ops          # ปลายทาง default เมื่อข้อความไม่ระบุเอง
CHAT_SLACK_TIMEOUT=10            # วินาที
```

ไม่ตั้ง `CHAT_SLACK_TOKEN` = provider ปิดอยู่ **แต่ service ยัง boot ได้** — ความล้มเหลว
ไปโผล่ตอนเรียกใช้พร้อมบอกว่าขาดอะไร ไม่ใช่ตอน start ซึ่งจะทำให้ service ที่อาจไม่เคย
โพสต์อะไรเลยขึ้นไม่ได้

### ทำไมรับแค่ bot token ไม่รับ incoming webhook

incoming webhook เก็บ credential ไว้ใน **path ของ URL** และ framework นี้ log URL ของทุก
outgoing call — ทั้งใน [call log](./requester.md), ใน headline ของ log line และใน Sentry
breadcrumb การรองรับ mode นั้นจึงมีทางเลือกแค่ "พิมพ์ credential ลง 3 ที่" หรือ
"สร้างระบบ redact path มารองรับ mode เดียว"

bot token เดินทางใน `Authorization` header ซึ่งถูก mask อยู่แล้วทุกที่ และได้สิ่งที่
webhook ทำไม่ได้อยู่ดี: โพสต์ได้ทุก channel, ได้ message id กลับมาไว้ตอบใน thread,
มี `auth.test` ให้ [readiness probe](./health.md) เรียก และมี error code ที่เจาะจงพอจะ
เอาไปทำอะไรต่อได้

## ส่งข้อความ

```go
_, err := core.Chat(ctx).Send(core.ChatMessage{
    Text: "deployed v2.31.0",
})
```

ไม่ใส่ `To` = ไปที่ `CHAT_SLACK_CHANNEL` — service ที่โพสต์ที่เดียวตลอดจึงไม่ต้องเอ่ยถึง
channel เลย

ข้อความแบบ alert:

```go
_, err := core.Chat(ctx).Send(core.ChatMessage{
    To:    "#ops",
    Title: "Import failed",
    Text:  "the run stopped before it finished",
    Level: core.ChatError,
    Fields: []core.ChatField{
        {Name: "file", Value: "orders.csv", Inline: true},
        {Name: "rejected", Value: "3", Inline: true},
    },
    Links: []core.ChatLink{{Text: "run log", URL: "https://example.com/runs/7"}},
})
```

`Fields` คือสิ่งที่ทำให้ alert **อ่านกวาดได้** — key เดิม เรียงเดิม ทุกครั้ง ดีกว่าประโยค
ที่คนต้องนั่งอ่านตอนตีสาม ส่วน `Level` ทำให้มันเป็นสีแดงโดยที่แต่ละ service ไม่ต้อง
คิดสีเอง

| `Level` | สี |
|---|---|
| `core.ChatInfo` (default) | น้ำเงิน |
| `core.ChatSuccess` | เขียว |
| `core.ChatWarn` | เหลือง |
| `core.ChatError` | แดง |

ข้อความ `ChatInfo` ที่ไม่มี `Title` และไม่มี `Fields` จะถูกส่งเป็นบรรทัดธรรมดา ไม่ห่อ
attachment — แถบสีกับ indent ที่ไม่ได้สื่ออะไรไม่ควรมี

## Thread

`Send` คืน `ChatResult.ID` กลับมา ส่งกลับเป็น `ThreadID` เพื่อตอบใต้ข้อความเดิม:

```go
started, err := core.Chat(ctx).Send(core.ChatMessage{To: "#ops", Title: "import started"})
if err != nil {
    return err
}

_, err = core.Chat(ctx).Send(core.ChatMessage{
    To:       "#ops",
    Text:     "halfway",
    ThreadID: started.ID,
})
```

งานที่รันนานถ้าไม่ทำแบบนี้จะเขียน 20 บรรทัดลง channel และกลบเรื่องอื่นที่คนกำลังคุยกันอยู่

## Native — Block Kit / Embed / Flex

field ที่อยู่เหนือ `Native` คือ subset ที่ทุก provider render ได้ อะไรที่มากกว่านั้นใส่
`Native` ซึ่งเป็น request body ของ provider ตรงๆ:

```go
_, err := core.Chat(ctx).Send(core.ChatMessage{
    To: "#ops",
    Native: map[string]any{
        "text": "all queues drained",   // ยังควรใส่: เป็น notification preview
        "blocks": []any{
            map[string]any{"type": "section", "text": map[string]any{
                "type": "mrkdwn", "text": "*all queues drained*",
            }},
        },
    },
})
```

`Native` **ชนะ field พอร์เทเบิลทั้งหมด** (ไม่ merge กัน) แต่ `To` กับ `ThreadID` ยังเติมให้
ถ้า payload ไม่ได้เขียนไว้เอง — thread จึงไม่พังทันทีที่หันไปใช้ blocks

ข้อความที่มี `Native` ผูกกับ provider นั้นและย้ายไม่ได้ ซึ่งเป็นการแลกที่ตั้งใจ:
**ไม่มี builder กลางสำหรับ Block Kit / Discord embed / LINE Flex** เพราะทั้งสามเป็น model
ของข้อความที่ต่างกันจริงๆ ตัวหารร่วมของมันจะเขียนยากกว่าการเขียนตรงๆ ทั้งสามแบบ และ
สุดท้ายก็ยังต้องมีทางหนีอยู่ดี

## หลาย provider

ตั้งชื่อแบบเดียวกับ SQL connection และ cache เพราะ service หนึ่งมักโพสต์หลายที่:

```go
app, _ := core.NewApp(env,
    core.WithChat("default", slackChat),   // alert ภายใน
    core.WithChat("line", lineChat),       // ตอบลูกค้า
)

core.Chat(ctx).Send(msg)             // default
core.Chats(ctx, "line").Send(msg)    // ตามชื่อ
```

ชื่อที่ไม่ได้ลงทะเบียนคืน provider ที่ปิดอยู่ ไม่ใช่ default — พิมพ์ชื่อผิดแล้วข้อความ
ตอบลูกค้าไปโผล่ห้อง ops แย่กว่าการที่มันไม่ถูกส่ง

## ไม่ได้ตั้งค่าไว้

`core.Chat(ctx)` **ไม่เคยเป็น nil** — ทุกคำสั่งคืน `503 CHAT_DISABLED`
(`errors.Is(err, core.ErrChatDisabled)`)

chat ไม่ degrade เงียบๆ เหมือน [cache](./cache.md): alert ที่ถูกทิ้งไปเฉยๆ คือเหตุการณ์ที่
ไม่มีใครได้รับแจ้ง และ log ก็ไม่ได้บอกอะไรเช่นกัน เพราะการเป็นสิ่งที่คนได้อ่านคือ
เหตุผลทั้งหมดของข้อความนั้น

## Error

| code | เมื่อไหร่ |
|---|---|
| `CHAT_NO_BODY` | ไม่มีทั้ง `Title`, `Text`, `Fields`, `Native` — ตรวจก่อนยิง |
| `CHAT_NO_CHANNEL` | ไม่มีทั้ง `To` และ `CHAT_SLACK_CHANNEL` |
| `CHAT_NOT_IN_CHANNEL` | bot ยังไม่ได้ถูก invite เข้าห้อง — `/invite @your-app` |
| `CHAT_CHANNEL_NOT_FOUND` | ไม่มีห้องนั้น (ห้อง private ก็ขึ้นแบบนี้จนกว่าจะ invite bot) |
| `CHAT_UNAUTHORIZED` | token ผิด ถูกเพิกถอน หรือ app ถูกปิด |
| `CHAT_MISSING_SCOPE` | token ขาด scope — `chat:write` คือตัวที่ใช้โพสต์ |
| `CHAT_CHANNEL_ARCHIVED` | ห้องถูก archive |
| `CHAT_MESSAGE_TOO_LONG` | ยาวเกินลิมิตของ Slack |
| `CHAT_RATE_LIMITED` | 429 — ข้อความแนบ `Retry-After` ที่ Slack บอกมา |
| `CHAT_INVALID_MESSAGE` | payload ผิดรูป (blocks/attachments) |
| `CHAT_SEND_FAILED` | network fail หรือ error อื่นของ Slack |
| `CHAT_DISABLED` | ไม่ได้ตั้ง `CHAT_SLACK_TOKEN` |

Slack ตอบการปฏิเสธมาเป็น **200 พร้อม `ok:false`** — status code เพียงอย่างเดียวไม่เคย
บอกว่าข้อความถูกโพสต์จริงหรือไม่ driver อ่าน envelope เสมอ

## Interface

```go
type IChat interface {
    Send(msg ChatMessage) (ChatResult, IError)
    Ping() IError
    Enabled() bool
    Provider() string
    WithContext(ctx context.Context) IChat
    Close() IError
}
```

`Ping()` เรียก `auth.test` — เช็ค token โดยไม่โพสต์อะไรลง channel
[readiness probe](./health.md) ใช้มันให้อัตโนมัติ (non-critical: service ยังทำงานได้
ถ้า Slack ล่ม)

## Testing

```go
c := core.NewMemoryChat()
app, _ := core.NewApp(env, core.WithChat("default", c))

require.NoError(t, reportImportFailure(app.NewContext(ctx), "orders.csv", 3, url))

sent := core.SentChatMessages(c)
require.Len(t, sent, 1)
assert.Equal(t, core.ChatError, sent[0].Level)   // assert ที่ตัว alert ไม่ใช่ว่า method ถูกเรียก

core.ResetChatMessages(c)
```

`NewMemoryChat` validate เหมือน provider จริง — ข้อความที่ Slack จะปฏิเสธจึงทำให้เทส
fail แทนที่จะผ่าน และมันคืน id ที่ไม่ซ้ำกันทุกครั้ง เทสที่ครอบ "โพสต์แล้วตอบใน thread"
จึงได้เดินทั้งสองขั้นจริง

## Best practices

- **โพสต์ผ่าน [job](./jobs.md) ไม่ใช่ในคำขอ** — Slack เป็นบริการของคนอื่นและมันล่มได้
  order ไม่ควร save ไม่สำเร็จเพราะโพสต์ห้องแชทไม่ผ่าน
- **กันโพสต์ซ้ำด้วย `IdemKey`** — retry ที่ไม่มีตัวกันคือห้องที่มี alert เดียวกันห้าอัน
- **ใช้ `Fields` แทนการยัดทุกอย่างลง `Text`** — alert มีไว้ให้กวาดตา ไม่ได้มีไว้ให้อ่าน
- **ใส่ `Links` ไปที่ของจริง** — run log, dashboard, order คนที่ถูกปลุกมาจะได้ไม่ต้องหาเอง
- **`Level` ให้ตรงความจริง** — ถ้าทุกอย่างเป็น `ChatError` สุดท้ายจะไม่มีใครสนใจสีแดง
- **อย่าใส่ข้อมูลส่วนบุคคลหรือ secret ลงข้อความ** — ห้องแชทเก็บ history ไว้ตลอด และคน
  ในห้องกว้างกว่าคนที่เข้าถึง log ได้
- **dev และเทสใช้ `NewMemoryChat`** — ไม่มีอะไรหลุดเข้าห้องจริงโดยไม่ตั้งใจ
