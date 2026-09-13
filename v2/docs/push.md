# Push Notifications (FCM)

`core.IPusher` — Firebase Cloud Messaging over
[firebase-admin v4](https://firebase.google.com/go). `core.Pusher(ctx)` คืน handle ที่ผูกกับ
request แล้ว

> เป็น function ไม่ใช่ `ctx.Pusher()` ด้วยเหตุผลเดียวกับ [`core.Mailer`](./mailer.md)
> และ [`core.Requester`](./requester.md) — การรับแค่ `context.Context` ทำให้ service
> ชั้นในส่ง notification ได้โดยไม่ต้องเปลี่ยน signature เป็น `core.IContext`
> ดู [Context & App](./context.md#method-บน-context-หรือ-function-ที่รับ-context)

## Setup

```go
pusher, err := core.NewPusherFromEnv(env)      // FIREBASE_CREDENTIAL
app, _ := core.NewApp(env, core.WithPusher(pusher))
```

`FIREBASE_CREDENTIAL` คือ service-account JSON ปล่อยว่างเพื่อใช้ credentials ของ
เครื่อง ซึ่งคือสิ่งที่ workload identity บน GKE ให้มา

```go
core.NewPusher(credentialJSON,
    core.WithPushTimeout(10*time.Second),
    core.WithPushProjectID("my-project"),   // สำหรับ credentials ที่ไม่ระบุ project
)
```

## ส่ง

```go
// เครื่องเดียว
err := core.Pusher(ctx).Send(deviceToken, core.PushMessage{
    Title: "ข้อความใหม่",
    Body:  "คุณมีข้อความใหม่",
    Data:  map[string]string{"chat_id": "c1"},
})

// หลายเครื่อง — แบ่งเป็นชุดละ 500 ให้เอง
res, err := core.Pusher(ctx).SendMulticast(tokens, core.PushMessage{
    Title: "ลดราคา", Body: "วันนี้ลด 50%",
})

// topic
err = core.Pusher(ctx).SendToTopic("news", msg)
err = core.Pusher(ctx).SendToCondition("'news' in topics && 'th' in topics", msg)
```

`SendMulticast` รับ token กี่ตัวก็ได้ — เพดาน 500 ของ provider ถูกจัดการให้ในนี้ ไม่ต้อง
ให้ caller รู้

## Notification กับ Data

`Title`/`Body` คือสิ่งที่ **เครื่องแสดง** `Data` คือสิ่งที่ **แอปได้รับ**

message ที่มีแต่ `Data` คือ silent notification — ส่งถึงแอปโดยระบบไม่วาดอะไร ซึ่งเป็น
วิธีสั่งให้ background sync ทำงาน framework mark `content-available` ให้อัตโนมัติ ไม่งั้น
iOS ทิ้งมัน

## ตั้งค่าต่อ platform

สี่อย่างที่ใช้บ่อยพอที่จะไม่ต้องสร้าง config เอง:

```go
badge := 3
core.PushMessage{
    Title:       "ออเดอร์ถูกจัดส่งแล้ว",
    Priority:    core.PushPriorityHigh,   // ปลุกเครื่องทันที
    Sound:       "ping.caf",
    Badge:       &badge,                  // pointer เพราะ 0 = ล้าง badge
    ChannelID:   "orders",                // Android notification channel
    TTL:         5 * time.Minute,         // ไม่ส่งถ้าช้าเกินไป
    CollapseKey: "order-1",               // ทับ message เก่าที่ยังไม่ถึง
}
```

⚠️ `PushPriorityHigh` เก็บไว้ใช้กับ message ที่คนกำลังรออยู่จริงๆ — provider throttle
sender ที่ mark ทุกอย่างเป็น high

ต้องการมากกว่านั้นใส่ config ของ platform นั้นตรงๆ — ตัวที่ใส่เองจะถูกใช้ทั้งก้อน และ
field ย่อข้างบนจะไม่ยุ่งกับ platform นั้น:

```go
core.PushMessage{
    Android: &messaging.AndroidConfig{ /* ... */ },
    APNS:    &messaging.APNSConfig{ /* ... */ },
    Web:     &messaging.WebpushConfig{ /* ... */ },
}
```

## Topic

```go
res, err := core.Pusher(ctx).Subscribe("news", tokens...)     // ชุดละ 1000 ให้เอง
res, err = core.Pusher(ctx).Unsubscribe("news", tokens...)
```

## Token ที่ตายแล้ว

แอปที่ถูกลบทิ้ง หรือ token ที่ถูกเปลี่ยน — **ลบมันออกจาก database** token ที่ตายแล้ว
จะไม่มีวันกลับมาใช้ได้ และ store ที่เต็มไปด้วยมันทำให้ทุก broadcast ช้าลงเรื่อยๆ

```go
res, err := core.Pusher(ctx).SendMulticast(tokens, msg)
if err != nil {
    return err
}
if dead := res.UnregisteredTokens(); len(dead) > 0 {
    if err := repo.DeleteTokens(dead); err != nil {
        return err
    }
}
```

ต่อ token ก็เช็คได้: `errors.Is(result.Error, core.ErrPushUnregistered)`

## ตรวจ payload โดยไม่ส่ง

```go
err := core.Pusher(ctx).Validate(token, msg)   // FCM dry run
```

## Error

| code | status | เมื่อไหร่ |
|---|---|---|
| `PUSH_TOKEN_UNREGISTERED` | 410 | token ตายแล้ว — ลบทิ้ง |
| `PUSH_INVALID_MESSAGE` | 400 | payload ผิด |
| `PUSH_RATE_LIMITED` | 429 | เกินโควตา |
| `PUSH_SENDER_MISMATCH` | 403 | token เป็นของ sender อื่น |
| `PUSH_NO_TOKEN` / `PUSH_NO_TOPIC` / `PUSH_NO_CONDITION` | 400 | ไม่ได้ระบุปลายทาง |
| `PUSH_DISABLED` | 503 | ไม่ได้ตั้ง `FIREBASE_CREDENTIAL` |

ใน `SendMulticast` **token ที่ fail ไม่ใช่ error** — อ่านจาก `BatchResult` error ที่คืนมา
หมายถึงตัว call เองล้มเหลว แปลว่าทั้ง batch นั้นไม่ได้ถูกส่งเลย

## ทดสอบ

```go
p := core.NewMemoryPusher()
app, _ := core.NewApp(env, core.WithPusher(p))

// ... รันสิ่งที่ควรส่ง push ...

sent := core.SentPushes(p)
require.Len(t, sent, 1)
assert.Equal(t, "ออเดอร์ถูกจัดส่งแล้ว", sent[0].Message.Title)
assert.Equal(t, []string{"tok-1"}, sent[0].Tokens)

assert.Equal(t, []string{"tok-1"}, core.PushTopicTokens(p, "news"))
core.ResetPushes(p)
```

## ไม่ได้ตั้งค่า Firebase ไว้

`core.Pusher(ctx)` **ไม่เคยเป็น nil** — ทุกคำสั่งคืน `503 PUSH_DISABLED`
(`errors.Is(err, core.ErrPushDisabled)`)

## Interface

```go
type IPusher interface {
    Send(token string, msg PushMessage) IError
    SendMulticast(tokens []string, msg PushMessage) (*BatchResult, IError)
    SendToTopic(topic string, msg PushMessage) IError
    SendToCondition(condition string, msg PushMessage) IError
    Subscribe(topic string, tokens ...string) (*BatchResult, IError)
    Unsubscribe(topic string, tokens ...string) (*BatchResult, IError)
    Validate(token string, msg PushMessage) IError
    Enabled() bool
    WithContext(ctx context.Context) IPusher
}
```

## Best practices

- **ส่งเป็น [job](./jobs.md)** — การยิงหาผู้ใช้หลักหมื่นคนไม่ใช่งานของ request และ
  ต้องการ retry ที่มีเพดาน
- **ลบ token ที่ตายแล้วทันทีที่รู้** — token ที่ไม่ถูกล้างทำให้ทุกครั้งที่ส่งมีขยะปนไป
  และตัวเลข "ส่งสำเร็จ" ก็เชื่อไม่ได้
- **payload ไม่ควรมีข้อมูลอ่อนไหว** — notification ขึ้นบนหน้าจอล็อกและผ่านมือ Google/Apple
  ส่งแค่ id แล้วให้แอปไปอ่านของจริง
- **topic สำหรับข่าวสารกว้างๆ token สำหรับเรื่องส่วนตัว** — topic ยกเลิกรายคนไม่ได้
- **เคารพการปิดแจ้งเตือนของผู้ใช้** ที่ฝั่งเราด้วย ไม่ใช่พึ่งว่า OS จะกันให้
- **ตรวจ payload ด้วย dry-run ก่อนส่งจริง** เมื่อเปลี่ยนโครงสร้าง — payload ที่ผิดรูป
  ล้มเงียบที่เครื่องผู้ใช้ ไม่ใช่ที่ server ของเรา
- **เทสด้วย `NewMemoryPusher`** และ assert payload ไม่ใช่ assert ว่าเมธอดถูกเรียก
