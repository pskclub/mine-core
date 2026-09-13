# AI · Agent Loops

"agent loop" คือ [tool loop](./ai-tools.md) ที่มี step เยอะขึ้นและ tool ที่ทำงานจริงจัง — **ไม่มี API แยก** เพราะมันคือของเดียวกันที่ตั้งค่าต่างกัน

```go
tools, _ := llm.NewToolSet().
    Add(llm.Tool("search_docs", "ค้นเอกสารภายในด้วย keyword เรียกก่อนตอบทุกครั้ง", h.searchDocs)).
    Add(llm.Tool("read_doc", "อ่านเอกสารทั้งฉบับจาก id ที่ได้จาก search_docs", h.readDoc)).
    Add(llm.Tool("create_ticket", "เปิด ticket ให้ทีมที่เกี่ยวข้อง เรียกเมื่อตอบเองไม่ได้", h.createTicket)).
    Build()

resp, err := core.LLM(ctx).Generate(core.LLMRequest{
    System: `คุณคือผู้ช่วยฝ่ายสนับสนุน
ค้นเอกสารก่อนตอบเสมอ ถ้าตอบไม่ได้จริง ๆ ให้เปิด ticket
อย่าเดาคำตอบที่ไม่มีในเอกสาร`,
    Messages:  []core.LLMMessage{core.LLMUser(question)},
    Tools:     tools,
    MaxSteps:  12,
    Reasoning: core.LLMReasoningHigh,
    MaxTokens: 8192,
    Approve: func(c context.Context, call core.LLMToolCall) core.IError {
        // อ่านได้เสรี แต่ของที่มีผลข้างเคียงต้องผ่านนโยบาย
        if call.Name == "create_ticket" && !allowTicketCreation(ctx) {
            return core.New(403, "NEEDS_HUMAN", "ผู้ใช้รายนี้ยังเปิด ticket อัตโนมัติไม่ได้")
        }
        return nil
    },
})
if err != nil {
    return err
}

ctx.Log().Info("agent finished",
    "steps", resp.Steps,
    "tools", len(resp.ToolCalls),
    "tokens", resp.Usage.Total())
```

---

## สี่เรื่องที่ต้องคิดก่อนปล่อยลูปยาว

### 1 · ต้นทุน

ลูป 12 step ไม่ได้แพงกว่า 1 step 12 เท่า — **มันแพงกว่านั้น** เพราะทุก step ส่งประวัติทั้งหมดไปใหม่ ยิ่งลูปยาว prompt ยิ่งโต

```go
resp.Usage.Total()              // ต้นทุนจริงของทั้งลูป
resp.Usage.CachedInputTokens    // ส่วนที่ประหยัดไปได้จาก cache
```

metric `llm.tokens.*` แยกตาม `model` อยู่แล้ว — ตั้ง alert ไว้ก่อนจะได้บิล

**ลดต้นทุน:** `CacheSystem: true` ช่วยได้มากเมื่อ system prompt ยาว เพราะมันถูกส่งซ้ำทุก step

### 2 · เวลา

ลูปยาวใช้เวลาเป็นนาที — `AI_TIMEOUT` ค่าเริ่มต้น 120 วินาทีอาจไม่พอ

**อย่ารันใน HTTP handler** ถ้าลูปเกิน ~10 วินาที ผู้ใช้จะเห็นหน้าเว็บค้าง และ load balancer อาจตัดก่อน

### 3 · ลูปไม่จบ

```go
if resp.FinishReason == core.LLMFinishToolUse {
    // หมด MaxSteps ทั้งที่ model ยังจะเรียก tool ต่อ
    // resp.Text ไม่ใช่คำตอบสุดท้าย — อย่าเอาไปแสดง
    ctx.Log().Warn("agent hit the step ceiling",
        "steps", resp.Steps,
        "last_tool", resp.ToolCalls[len(resp.ToolCalls)-1].Name)
    return ctx.NewError(nil, emsgs.AgentDidNotFinish)
}
```

`MaxSteps` คือเพดานแข็ง ไม่ใช่คำแนะนำ — และการชนเพดานเป็นสัญญาณว่า prompt หรือชุด tool มีปัญหา ไม่ใช่ว่าควรเพิ่มเพดาน

### 4 · ผลข้างเคียง

ทุก tool ที่ **เขียนข้อมูลหรือส่งอะไรออกไปข้างนอก** ต้องผ่าน `Approve` ไม่มีข้อยกเว้น

model ที่ทำงานถูกต้อง 99 ครั้งแล้วเปิด ticket ผิด 1 ครั้ง ก็ยังเป็นปัญหาที่ต้องมีคนตามเก็บ

---

## รันเป็น job (แนะนำ) {#run-as-job}

job runner มี retry, timeout, concurrency, run log ให้อยู่แล้ว — ไม่ต้องเขียนใหม่

```go
type AgentParams struct {
    ConversationID string `json:"conversation_id"`
    Question       string `json:"question"`
}

func (h Handler) RunAgent(ctx core.ICronjobContext, p *AgentParams) error {
    resp, err := core.LLM(ctx).Generate(core.LLMRequest{
        System:    supportRules,
        Messages:  []core.LLMMessage{core.LLMUser(p.Question)},
        Tools:     h.tools,
        MaxSteps:  12,
        MaxTokens: 8192,
        Approve:   h.approve,
    })
    if err != nil {
        // 429 / 5xx → job runner retry พร้อม backoff ให้เอง
        return err
    }
    if resp.FinishReason == core.LLMFinishToolUse {
        return core.New(500, "AGENT_INCOMPLETE", "agent ทำงานไม่จบใน 12 step")
    }
    return h.reply(ctx, p.ConversationID, resp.Text)
}
```

ลงทะเบียนพร้อม timeout และ concurrency ที่เหมาะกับลูปยาว:

```go
_ = core.RegisterJob(registry, core.JobDef{
    Name:        "support-agent",
    Queue:       "ai",
    Timeout:     10 * time.Minute,
    MaxAttempts: 3,
    // ลูปยาวที่ทับกันเองคือบิลสองเท่าโดยไม่ได้อะไรเพิ่ม
    MaxConcurrent: 1,
    Concurrency:   core.ConcurrencySkip,
}, h.RunAgent)
```

คุม rate limit ของ provider ด้วย `job_limiter` แทนที่จะให้ agent 50 ตัวชน 429 พร้อมกัน

---

## รูปแบบที่ใช้ได้จริง

### ให้ agent อ่านอย่างเดียว แล้วให้คนกดยืนยัน

ปลอดภัยที่สุดสำหรับงานที่มีผลข้างเคียง — agent เสนอ คนอนุมัติ

```go
resp, err := core.LLM(ctx).Generate(core.LLMRequest{
    System:   "วิเคราะห์ปัญหาแล้วเสนอสิ่งที่ควรทำ อย่าลงมือทำเอง",
    Messages: msgs,
    Tools:    readOnlyTools,   // ไม่มี tool ที่เขียนอะไรเลย
    MaxSteps: 8,
})

// เก็บข้อเสนอไว้ให้เจ้าหน้าที่กดยืนยันในหน้าจอ
return h.saveProposal(ctx, resp.Text, resp.ToolCalls)
```

### เก็บ audit trail ลง DB

core เขียน log ให้ทุก call อยู่แล้ว (`llm tool ran|denied|failed <name>`) — ถ้าต้องการเก็บถาวรและ query ได้ ก็เขียนลง DB เพิ่ม:

```go
for _, call := range resp.ToolCalls {
    if err := repository.New[AgentAudit](ctx).Create(&AgentAudit{
        ConversationID: p.ConversationID,
        Tool:           call.Name,
        Outcome:        string(call.Outcome),
        Input:          string(call.Input),
    }); err != nil {
        return err
    }
}
```

เมื่อ agent ทำอะไรแปลก ๆ นี่คือสิ่งเดียวที่บอกได้ว่ามันทำอะไรไปบ้าง

### จำกัดจำนวน tool

model ที่มี tool 20 ตัวเลือกผิดบ่อยกว่า model ที่มี 5 ตัว — ถ้ามีเยอะ ให้แยกเป็นหลาย agent ที่แต่ละตัวมี tool เฉพาะงานของมัน แล้วให้ชั้นบนตัดสินใจว่าจะเรียกตัวไหน (ตัดสินใจด้วยโค้ด ไม่ใช่ด้วย model)

---

## ที่ core ไม่ทำให้

- **multi-agent orchestration** — ให้ service ประกอบเอง หรือใช้ job runner ที่มีอยู่
- **conversation store** — เก็บประวัติเองใน DB แล้วส่งเข้า `Messages`
- **planner / memory / reflection** — เป็นรูปแบบ prompt ไม่ใช่ฟีเจอร์ของ framework

ถ้าต้องการ hook ละเอียดกว่านี้ (`OnStepFinish`, `OnBeforeStep` ฯลฯ) เข้าถึงได้ผ่าน [escape hatch](./ai-providers.md#escape-hatch)

---

ต่อไป: [Embeddings](./ai-embeddings.md) · [Providers & Tuning](./ai-providers.md)
