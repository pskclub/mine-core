# Best Practices

หน้าอื่นในหมวดนี้บอกว่า API ทำอะไรได้ หน้านี้บอกว่า **ควรใช้มันยังไงใน service จริง** —
เรื่องที่ราคาแพงที่สุดของ AI ไม่ใช่โค้ดที่เขียนผิด แต่คือค่าใช้จ่ายที่ไม่มีใครดู, prompt
ที่หลุดลง log และลูปที่ไม่มีใครหยุด

## กฎสิบข้อ

| # | กฎ | ทำไม |
|---|---|---|
| 1 | อย่ายิง model ตรงๆ ใน HTTP handler ที่ user รออยู่ | generation กินเวลาเป็นนาที — [stream](./ai-streaming.md) หรือทำเป็น [job](./jobs.md) |
| 2 | เลือกชั้นให้ตรงงาน: `llm.New[T]` เมื่อต้องการ **ค่า** ไม่ใช่ข้อความ | parse ข้อความเองคือ bug ที่รอเกิด — [Typed Values](./ai-typed.md) |
| 3 | ตั้ง `MaxSteps` ทุกครั้งที่มี tool | ไม่ตั้ง = ไม่วนลูป, ตั้งสูงเกิน = บิลที่ไม่มีเพดาน |
| 4 | tool ที่ **เขียน** ข้อมูลต้องมี `Approve` | model ตัดสินใจเรียกเอง จุดอนุมัติจึงต้องเป็นของเรา |
| 5 | อย่าเปิด `AI_LOG_PROMPT` / `AI_LOG_COMPLETION` บน production | prompt คือสิ่งที่ผู้ใช้พิมพ์ |
| 6 | retry เฉพาะ 429 / 5xx | ที่เหลือยิงซ้ำก็ได้ผลเดิม แถมจ่ายเงินซ้ำ |
| 7 | ตั้ง `MaxTokens` ให้ตรงกับที่ต้องการจริง | มันคือเพดานค่าใช้จ่ายต่อ call ที่บังคับได้จริงที่สุด |
| 8 | เก็บ `System` ให้คงที่ไบต์ต่อไบต์ | prefix ที่เปลี่ยนคือ [prompt cache](./ai-providers.md#prompt-caching) ที่ไม่เคยติด |
| 9 | ดูต้นทุนจาก [metric](./ai.md#metric) ไม่ใช่จาก log | log ถูก sample และหายตามอายุ |
| 10 | เทสด้วย `NewMemoryLLM` ไม่ใช่ยิงของจริงใน CI | เทสที่จ่ายเงินและ flaky คือเทสที่คนจะปิดทิ้ง |

## เลือกชั้นให้ถูก

| ต้องการ | ใช้ | อย่าใช้ |
|---|---|---|
| ข้อความให้คนอ่าน | `core.LLM(ctx).Generate` | — |
| ค่าที่โค้ดเอาไปใช้ต่อ (หมวดหมู่, คะแนน, field ที่สกัดมา) | `llm.New[T]` | `Generate` แล้วมา parse เอง |
| ตอบทีละ token ให้ user เห็น | `Stream` | `Generate` แล้วรอครบ |
| ให้ model ทำงานหลายขั้น | tool loop + `MaxSteps` | เขียน loop เรียก `Generate` เอง |
| ค้นด้วยความหมาย | `core.Embedder(ctx)` | ให้ model อ่านทั้งฐานข้อมูล |

typed value คือกฎที่คุ้มที่สุดในตาราง: `llm.New[Category](ctx)` ได้ struct ที่ compiler
ตรวจให้ ส่วนการ prompt ว่า "ตอบเป็น JSON" แล้ว unmarshal เองคือทางที่พังเงียบวันที่ model
เติมคำอธิบายมาหน้า JSON

## ออกแบบให้ล้มได้

model จะล่ม จะช้า จะโดน rate limit — คำถามคือ service ทำอะไรตอนนั้น

```go
resp, err := core.LLM(ctx).Generate(req)
switch {
case err == nil:
case err.GetStatus() == 429, err.GetStatus() >= 500:
    return s.enqueueRetry(ctx, req)      // ชั่วคราว → job ที่ retry ให้เอง
default:
    return ctx.NewError(err, errmsgs.InternalServerError)   // ถาวร → บอก user
}
```

สามชั้นที่ควรมีเรียงตามความคุ้ม:

1. **fallback ที่ไม่ใช่ AI** — ค้นหาแบบเดิม, template สำเร็จรูป, หรือบอกตรงๆ ว่าตอนนี้
   ใช้ไม่ได้ ดีกว่าปล่อยให้ทั้งหน้าค้าง
2. **คิวแทนการ retry ในที่** — งานที่ไม่ต้องตอบทันทีควรกลายเป็น job ที่มี backoff
3. **cache คำตอบที่ซ้ำได้** — คำถามยอดฮิตของ FAQ ไม่ควรจ่ายเงินทุกครั้ง

```go
// คำถามซ้ำ = คำตอบเดิม, ไม่ต้องเรียก model
key := "ai:faq:" + hash(question)
if err := ctx.Cache().Get(key, &answer); err == nil {
    return answer, nil
}
```

⚠️ อย่า cache คำตอบที่ผูกกับผู้ใช้คนใดคนหนึ่งด้วย key ที่ไม่มี id ของเขาอยู่ —
คำตอบของคนอื่นโผล่ข้ามบัญชีคือ incident ไม่ใช่ bug

## คุมค่าใช้จ่าย

| คุมที่ | วิธี |
|---|---|
| ต่อ call | `MaxTokens` ให้ตรงกับความยาวที่ต้องการจริง |
| ต่อ loop | `MaxSteps` — หนึ่ง step คือหนึ่ง generation ที่ถูกคิดเงิน |
| ต่อ prompt | `CacheSystem: true` เมื่อ system prompt ยาวและซ้ำ |
| ต่องาน | เลือก model เล็กสำหรับงานง่าย — `req.Model` เปลี่ยนได้รายคำขอ |
| ต่อผู้ใช้ | rate limit ด้วย [counter](./cache-counters.md) ก่อนถึง model |

```go
// งานง่ายใช้ model เล็ก งานยากค่อยขึ้น — ในโปรเซสเดียวกัน
req.Model = "claude-haiku-4-5-20251001"
```

การนับต้นทุนจริงมาจาก metric `llm.tokens.*` ที่ core บันทึกให้ทุก call แยกตาม
`provider`/`model`/`operation` — ตั้ง dashboard ตั้งแต่วันแรกที่เปิดใช้ ไม่ใช่วันที่บิลมา

**`cached_input` แยกจาก `input` เพราะคิดเงินคนละเรต** — เป็นตัวเดียวที่บอกได้ว่า prompt
caching ติดจริงไหม

## Prompt

- **แยก system ออกจาก user เสมอ** — system คือกติกา, message คือสิ่งที่ผู้ใช้พิมพ์
  การเอาทั้งสองมาต่อเป็น string เดียวคือช่องทางของ prompt injection ที่ง่ายที่สุด
- **สิ่งที่ผู้ใช้ส่งมาคือข้อมูล ไม่ใช่คำสั่ง** — ถ้าจะให้ model อ่านเนื้อหาจากภายนอก
  (อีเมล, หน้าเว็บ, ไฟล์ที่อัปโหลด) ให้ระบุใน system ว่าเนื้อหาในส่วนนั้นห้ามถือเป็นคำสั่ง
  และอย่าให้ tool ที่เขียนข้อมูลทำงานได้จากเส้นทางนั้นโดยไม่มีคนอนุมัติ
- **อย่าใส่ความลับลง prompt** — key, token, ข้อมูลบัตร ไม่ควรเดินทางไปหา provider
  และไม่ควรอยู่ในที่ที่ `AI_LOG_PROMPT` จะเขียนมันออกมาได้
- **byte-stable** — prompt ที่มี timestamp หรือชื่อผู้ใช้ต่อท้าย ทำให้ cache prefix
  เปลี่ยนทุกครั้ง เอาส่วนที่เปลี่ยนไปไว้ใน message แทน

## Tool use

```go
req := core.LLMRequest{
    Messages: msgs,
    Tools:    []core.LLMTool{getOrder, refundOrder},
    MaxSteps: 5,
    Approve: func(ctx context.Context, call core.LLMToolCall) core.IError {
        if call.Name == "refund_order" {
            return core.New(403, "NEEDS_HUMAN", "การคืนเงินต้องให้คนอนุมัติ")
        }
        return nil
    },
}
```

- **แยก tool ที่อ่านกับที่เขียนออกจากกัน** — อ่านให้ model เรียกได้อิสระ เขียนต้องผ่าน
  `Approve` หรือกลายเป็นคำขอที่รอคนกดยืนยัน
- **`MaxSteps` คือเพดานทั้งเวลาและเงิน** — 5 พอสำหรับงานส่วนใหญ่ ลูปที่ต้องการมากกว่า
  10 มักแปลว่า tool ยังอธิบายตัวเองไม่ดีพอ
- **description บอกว่า "เมื่อไหร่ควรใช้" ไม่ใช่แค่ "คืออะไร"** — [Tool Use](./ai-tools.md)
- **จำกัดจำนวน tool ที่เสนอต่อครั้ง** — tool 30 ตัวทำให้ model เลือกผิดบ่อยขึ้นและ
  prompt ยาวขึ้นทุก call
- error ของ tool **ถูกส่งกลับไปให้ model** เพื่อให้มันแก้ทางได้ ระวังอย่าให้ข้อความ error
  มีข้อมูลภายในที่ไม่ควรออกไปถึง provider

agent ที่วนยาวควร[รันเป็น job](./ai-agent.md#run-as-job) — ได้ timeout, retry,
run log และปุ่ม cancel มาฟรี แทนที่จะเป็น goroutine ที่ไม่มีใครมองเห็น

## Embeddings & RAG

- **ล็อกทั้งสามอย่างเข้าด้วยกัน**: model, `AI_EMBED_DIMENSIONS` และคอลัมน์ในฐานข้อมูล —
  เปลี่ยน model แปลว่าต้อง re-embed ทั้งชุด เวกเตอร์ข้าม model เทียบกันไม่ได้
- **เก็บเวอร์ชันของ embedding ไว้ข้างเวกเตอร์** เพื่อให้ migrate ทีละส่วนได้
- **embed เป็น job ไม่ใช่ในคำขอ** — เอกสารหนึ่งพันหน้าไม่ควรอยู่ใน request เดียว
- **chunk ให้ตรงกับความหมาย** ไม่ใช่ตรงกับจำนวนตัวอักษร และเก็บ metadata (source, หน้า)
  ไว้ด้วยเสมอ — คำตอบที่อ้างที่มาไม่ได้คือคำตอบที่ตรวจสอบไม่ได้

## Observability

สิ่งที่ควรมีก่อนเปิดใช้จริง:

- [ ] dashboard ของ `llm.tokens.input` / `output` / `cached_input` แยกตาม model
- [ ] alert ที่ `llm.errors` โดยเฉพาะ code `LLM_RATE_LIMITED`
- [ ] `AI_LOG_SLOW` ตั้งให้ตรงกับความคาดหวังจริง (default 30 วินาที)
- [ ] `AI_LOG_LEVEL=debug` เปิดได้แยกจาก log ทั้งระบบเมื่อต้องไล่ปัญหา
- [ ] audit trail ของ tool (`resp.ToolCalls`) ถูกเก็บลงที่ที่ค้นย้อนหลังได้ ถ้า tool
      แตะข้อมูลจริง

## Testing

```go
model := core.NewMemoryLLM("คำตอบที่ตั้งไว้")
app, _ := core.NewApp(env, core.WithLLM(model))
```

- เทส **โค้ดของเรา** ไม่ใช่เทส model — สิ่งที่ควรยืนยันคือ prompt ถูกประกอบถูก, error
  ถูกจัดการถูก, tool ถูกอนุมัติ/ปฏิเสธตามนโยบาย และผลลัพธ์ถูกบันทึกถูกที่
- ยิง provider จริงเฉพาะใน integration test ที่มี build tag — ไม่ใช่ใน CI ปกติ
- เคสที่คุ้มที่สุด: **429 แล้วเข้าคิว**, **tool ที่ถูกปฏิเสธไม่ถูกเรียก** และ
  **typed value ที่ model ตอบผิดรูปกลายเป็น error ไม่ใช่ค่าว่าง**

รายละเอียดที่ [Testing](./ai-testing.md)

## Checklist ก่อนขึ้น production

- [ ] `AI_LOG_PROMPT` / `AI_LOG_COMPLETION` ปิดอยู่
- [ ] ทุกเส้นทางที่เรียก model มี fallback หรือมีคิวรองรับเมื่อ provider ล่ม
- [ ] `MaxTokens` และ `MaxSteps` ตั้งไว้ทุกที่ที่เรียก
- [ ] tool ที่เขียนข้อมูลผ่าน `Approve`
- [ ] มี rate limit ต่อผู้ใช้ก่อนถึง model
- [ ] dashboard ต้นทุน + alert error พร้อมใช้
- [ ] เทสไม่ยิง provider จริง
- [ ] `AI_TIMEOUT` สอดคล้องกับ timeout ของ job/HTTP ที่ห่อมันอยู่
