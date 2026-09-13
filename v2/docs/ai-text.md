# AI · Text Generation

ชั้นล่างสุด — ส่งข้อความเข้า ได้ข้อความออก

```go
func (s Service) Summarize(ctx core.IContext, text string) (string, core.IError) {
    resp, err := core.LLM(ctx).Generate(core.LLMRequest{
        System:   "สรุปเป็นภาษาไทย ไม่เกิน 3 บรรทัด",
        Messages: []core.LLMMessage{core.LLMUser(text)},
    })
    if err != nil {
        return "", err
    }
    return resp.Text, nil
}
```

## ทำไมเป็นฟังก์ชัน ไม่ใช่เมธอดบน IContext

`core.LLM(ctx)` เป็นฟังก์ชันด้วยเหตุผลเดียวกับ `core.Requester` และ `core.Mailer`:

> **การเรียก model ไม่ใช่ "ความสามารถของ request" แต่เป็น "สิ่งที่เราทำ โดยเอา deadline ของ request ติดไปด้วย"**

และ `IContext` เป็น interface ที่ทุกอย่างใน service พึ่งพา — ยิ่งเล็กยิ่งดี

ผลพลอยได้ที่สำคัญ: `ctx` รับ `context.Context` ธรรมดาก็ได้

```go
// ฟังก์ชันนี้ไม่รู้จัก framework เลย แต่เรียก model ได้ และยังได้ deadline ของ request
func classify(ctx context.Context, text string) (string, core.IError) {
    resp, err := core.LLM(ctx).Generate(core.LLMRequest{
        Messages: []core.LLMMessage{core.LLMUser(text)},
    })
    if err != nil {
        return "", err
    }
    return resp.Text, nil
}
```

context ที่ไม่ได้มาจาก App เลย (script, test ต้น ๆ) จะได้ model ที่ปิดอยู่ ไม่ใช่ `nil` — call site จึงไม่ต้อง nil-check

---

## สิ่งที่ได้กลับมา

```go
resp.Text          // คำตอบ
resp.FinishReason  // stop · length · content_filter · tool_use · error · other
resp.Usage         // InputTokens · OutputTokens · CachedInputTokens · ReasoningTokens
resp.Steps         // จำนวนเทิร์น — 1 คือตอบตรง ๆ, มากกว่านั้นแปลว่าเรียก tool ระหว่างทาง
resp.ToolCalls     // tool ที่ถูกเรียก ตามลำดับ
resp.Sources       // สิ่งที่คำตอบอ้างอิง — มีเมื่อ ground ด้วย search/URL context
resp.Model         // model ที่ตอบจริง (ตรงกับ req.Model ถ้าระบุมา)
resp.Provider      // ใครเป็นคนตอบ
resp.Raw           // payload ดิบของ driver (escape hatch)
```

### `FinishReason` ไม่ใช่ของประดับ

`length` แปลว่า **คำตอบโดนตัดกลางคัน** ไม่ใช่จบเอง — ถ้าไม่เช็ค คุณจะเอาคำตอบครึ่งเดียวไปใช้เป็นของจริง

```go
switch resp.FinishReason {
case core.LLMFinishStop:
    return resp.Text, nil

case core.LLMFinishLength:
    // เพิ่ม MaxTokens หรือสั่งให้ตอบสั้นลง — อย่าใช้ค่าที่ได้
    return "", ctx.NewError(nil, emsgs.AIResponseTruncated)

case core.LLMFinishContentFilter:
    return "", ctx.NewError(nil, emsgs.AIContentBlocked)

case core.LLMFinishToolUse:
    // หมด MaxSteps ทั้งที่ model ยังจะเรียก tool ต่อ — ดู Agent Loops
    return "", ctx.NewError(nil, emsgs.AgentDidNotFinish)
}
```

### Usage

```go
resp.Usage.InputTokens        // prompt ที่ส่งไป
resp.Usage.CachedInputTokens  // ส่วนที่อ่านจาก prompt cache (ถูกกว่ามาก)
resp.Usage.OutputTokens       // คำตอบ
resp.Usage.ReasoningTokens    // token ที่ใช้คิด (ถ้า model รองรับ)
resp.Usage.Total()            // รวมทุกอย่าง
```

แยก `CachedInputTokens` ออกจาก `InputTokens` เพราะคิดเงินคนละเรต — ถ้ารวมกันรายงานต้นทุนจะผิดในทางที่ *ดูเหมือนถูก*

---

## บทสนทนาหลายเทิร์น

```go
msgs := []core.LLMMessage{
    core.LLMUser("ขอสูตรผัดกะเพรา"),
    core.LLMAssistant(previousAnswer),
    core.LLMUser("ทำแบบไม่เผ็ดได้ไหม"),
}

resp, err := core.LLM(ctx).Generate(core.LLMRequest{
    System:   systemPrompt,
    Messages: msgs,
})
```

### ทำไมไม่มี role `system` ในรายการข้อความ

`System` เป็นฟิลด์แยก เพราะแต่ละ provider **วาง system prompt คนละที่**:

| provider | วางไว้ที่ |
|---|---|
| Anthropic | ฟิลด์ `system` ระดับบนสุด |
| OpenAI | ข้อความแรกใน `messages` ที่ role = `system`/`developer` |
| Google | ฟิลด์ `systemInstruction` แยกออกมา |

driver จะวางถูกได้ก็ต่อเมื่อรู้ว่ามันคือ system prompt — ถ้าปนอยู่ในรายการข้อความ บางเจ้าจะถูกวางผิดที่แล้วมีน้ำหนักน้อยกว่าที่ควร โดยไม่มีอะไรบอก

`LLMAssistant` มีไว้ **replay ประวัติ** ไม่ใช่ prefill คำตอบ — model รุ่นใหม่หลายตัวปฏิเสธบทสนทนาที่จบด้วย assistant turn

---

## คุมความยาว

```go
core.LLMRequest{
    Messages:      msgs,
    MaxTokens:     512,                      // 0 = ใช้ AI_MAX_TOKENS
    StopSequences: []string{"\n---", "END"}, // หยุดเมื่อเจอ
}
```

`MaxTokens` เป็นเพดานแข็ง — ชนแล้วได้ `FinishReason == length` ไม่ใช่คำตอบที่สั้นลงอย่างสวยงาม ถ้าอยากได้คำตอบสั้นให้บอกใน system prompt แล้วใช้ `MaxTokens` เป็นตาข่ายกันพัง

## Temperature

```go
temp := 0.2
core.LLMRequest{
    Messages:    msgs,
    Temperature: &temp,    // pointer เพราะ 0 เป็นค่าที่มีความหมาย
}
```

เป็น pointer เพราะ **0 คือค่าที่ตั้งใจได้** และ model รุ่นใหม่หลายตัว (Claude Opus 5, Fable 5) **ปฏิเสธพารามิเตอร์นี้ไปเลย** — `nil` จึงแปลว่า "อย่าส่งไป" ไม่ใช่ "ส่ง 0"

---

## รูปแบบที่เจอบ่อย

### สรุปเอกสารเป็นชุด

```go
base := core.LLMRequest{
    System:      summaryRules,   // ยาว
    CacheSystem: true,           // จ่ายเต็มครั้งเดียว
    MaxTokens:   256,
}

for _, doc := range docs {
    req := base
    req.Messages = []core.LLMMessage{core.LLMUser(doc.Body)}

    resp, err := core.LLM(ctx).Generate(req)
    if err != nil {
        ctx.Log().Warn("summarise failed", "doc", doc.ID, "code", err.GetCode())
        continue
    }
    doc.Summary = resp.Text
}
```

### เรียกจาก job แทน HTTP handler

generation ที่ยาวไม่ควรอยู่ใน request cycle — job runner มี retry/timeout/concurrency ให้อยู่แล้ว

```go
func (h Handler) Summarize(ctx core.ICronjobContext, p *SummarizeParams) error {
    resp, err := core.LLM(ctx).Generate(core.LLMRequest{
        System:   summaryRules,
        Messages: []core.LLMMessage{core.LLMUser(p.Body)},
    })
    if err != nil {
        return err        // 429/5xx → job runner retry ให้เอง
    }
    return h.store(ctx, p.DocID, resp.Text)
}

_ = core.RegisterJob(registry, core.JobDef{
    Name: "summarize", Queue: "ai", Timeout: 2 * time.Minute, MaxAttempts: 3,
}, h.Summarize)
```

---

## แนบรูปหรือเอกสาร

```go
core.LLMUser("ยอดรวมเท่าไหร่").With(core.LLMImage(scan, "image/jpeg"))
```

ดู [Attachments](./ai-attachments.md)

---

ต่อไป: [Streaming](./ai-streaming.md) · [Typed Values](./ai-typed.md) · [Attachments](./ai-attachments.md)
