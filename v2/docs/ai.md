# AI / Language Model

v2 มองภาษาโมเดลเป็น **capability หนึ่งของ App** เหมือน `IStorage` หรือ `IMailer` — ไม่ใช่ framework ใหม่ที่ซ้อนเข้ามา

| ชั้น | ใช้เมื่อ | หน้า |
|---|---|---|
| `core.LLM(ctx)` | อยากได้ **ข้อความ** — ตอบแชท สรุป เขียนใหม่ | [Text Generation](./ai-text.md) |
| `llm.New[T](ctx)` | อยากได้ **ค่า Go** — สกัดข้อมูล จัดหมวด ให้คะแนน | [Typed Values](./ai-typed.md) |
| `.With(core.LLMImage(...))` | อยากให้ model **ดูรูปหรืออ่านเอกสาร** | [Attachments](./ai-attachments.md) |
| `core.LLMTool` | อยาก **ให้ model เรียกฟังก์ชันของเรา** | [Tool Use](./ai-tools.md) |
| `core.Embedder(ctx)` | อยากได้ **เวกเตอร์** — semantic search, RAG | [Embeddings](./ai-embeddings.md) |

เหตุผลการออกแบบทั้งหมดอยู่ใน [design/v2/17-ai.md](https://github.com/pskclub/mine-core/blob/master/design/v2/17-ai.md)

---

## core ให้อะไร และไม่ให้อะไร

**ให้:** ท่อไปหา model + สิ่งที่ทุกทีมต้องทำซ้ำแล้วมักทำไม่เหมือนกัน

| เรื่อง                 | ทำไมต้องอยู่ใน core                                                                                                                 |
| ------------------------| -------------------------------------------------------------------------------------------------------------------------------------|
| นับ token / ค่าใช้จ่าย | ถ้าไม่มี คำถาม *"เดือนนี้จ่ายค่า AI ไปเท่าไหร่ แยกตาม service และ model"* จะไม่มีคำตอบ — และกว่าจะมีคนถาม call ก็หายไปแล้ว          |
| แปลง error             | ถ้าไม่แปลง ทุก failure จะเป็น 500 เหมือนกันหมด rate limit แยกจาก API key ผิดไม่ได้ คนเลย retry อันที่ไม่ควร และเลิก retry อันที่ควร |
| ปกปิด prompt           | prompt คือสิ่งที่ผู้ใช้พิมพ์ — ห้าม log เนื้อเป็นค่าเริ่มต้น                                                                        |
| นโยบาย timeout         | generation กินเวลาเป็นนาที ถ้าใช้ timeout ทรง HTTP จะตัดคำตอบที่กำลังจะมาถึง (และโดนคิดเงินอยู่ดี)                                  |
| gate ก่อนรัน tool      | tool loop คือโค้ดที่ model ตัดสินใจเรียกฟังก์ชันของเราเอง — ต้องมีจุดเดียวที่อนุมัติ บันทึก และเทสต์ได้                             |
| memory implementation  | ทุก capability ใน v2 มี ตัวนี้ก็ต้องมี                                                                                              |

**ไม่ให้:** conversation store, RAG pipeline สำเร็จรูป, MCP client, สร้างรูปภาพ — ปล่อยให้ service ประกอบเอง (ดู [Providers & Tuning](./ai-providers.md#escape-hatch))

---

## ตั้งค่า

```sh
APP_AI_PROVIDER=anthropic
APP_AI_MODEL=claude-opus-5
APP_AI_API_KEY=sk-ant-...
```

| key | ค่าเริ่มต้น | คำอธิบาย |
|---|---|---|
| `AI_PROVIDER` | — | `anthropic` · `openai` · `google` · `ollama` · `compat` |
| `AI_MODEL` | — | model id ของ provider นั้น |
| `AI_API_KEY` | — | key |
| `AI_BASE_URL` | — | endpoint อื่น (gateway/proxy/local) — **จำเป็น** เมื่อใช้ `compat` |
| `AI_MAX_TOKENS` | `4096` | เพดานคำตอบเมื่อ request ไม่ได้ระบุ |
| `AI_TIMEOUT` | `120` | วินาที — generation กินเวลาเป็นนาทีได้ อย่าตั้งทรง HTTP |
| `AI_MAX_RETRIES` | `2` | retry เมื่อเจอ 429 / 5xx / network |
| `AI_EMBED_MODEL` | — | embedding model — ดู [Embeddings](./ai-embeddings.md) |
| `AI_EMBED_PROVIDER` | ตาม `AI_PROVIDER` | เจ้าที่ใช้ embed ถ้าต่างจากเจ้าที่ใช้ generate |
| `AI_EMBED_API_KEY` | ตาม `AI_API_KEY` | |
| `AI_EMBED_BASE_URL` | ตาม `AI_BASE_URL` | |
| `AI_EMBED_DIMENSIONS` | — | ความยาวเวกเตอร์ที่ขอทุกครั้ง — ต้องตรงกับคอลัมน์ที่ประกาศไว้ |
| `AI_LOG_LEVEL` | ตาม `LOG_LEVEL` | `silent` · `error` · `warn` · `info`/`debug` |
| `AI_LOG_PROMPT` | `false` | เขียนขาเข้า (prompt, argument ของ tool) ลง log — **เครื่อง dev เท่านั้น** |
| `AI_LOG_COMPLETION` | `false` | เขียนขาออก (คำตอบ, ผลลัพธ์ tool, source) — **เครื่อง dev เท่านั้น** |
| `AI_LOG_SLOW` | `30` | วินาที — เกินแล้วขึ้น warn

**ตั้งค่าแยกรายเจ้า — คีย์เอามาจากไหน, model id ตัวไหน, อะไรใช้ไม่ได้บ้าง: [Provider Setup](./ai-setup.md)

ไม่ตั้ง `AI_PROVIDER` หรือ `AI_MODEL` → service ยัง boot ได้ปกติ** แต่ทุกการเรียกจะ fail ด้วย `LLM_DISABLED` (503)

ตั้งใจให้ดัง — cache ที่ miss แล้วคำนวณใหม่ได้จึง degrade เงียบได้ แต่ **คำตอบว่างเปล่าที่ caller เชื่อว่าจริง** กู้คืนไม่ได้ ที่นี่จึงทำตัวเหมือน storage/mq/mailer ไม่ใช่เหมือน cache

---

## Wiring

```go
env, _ := core.NewEnv()

model, err := goai.New(env)          // github.com/pskclub/mine-core/v2/llm/goai
if err != nil {
    log.Fatal(err)                    // AI_PROVIDER สะกดผิด = error ตั้งแต่ boot ไม่ใช่ตอน request แรก
}

embedder, err := goai.NewEmbedder(env)   // ถ้าใช้ embeddings
if err != nil {
    log.Fatal(err)
}

app, _ := core.NewApp(env,
    core.WithLLM(model),
    core.WithEmbedder(embedder),
)
```

boot log บอกว่า process นี้ผูกกับ model ไหน — เพราะ `llm=true` ไม่ใช่คำถามที่ใครมี แต่ *"จะโดนคิดเงินค่าอะไร"* คือคำถามจริง:

```
INFO app ready  env=prod service=api llm=anthropic/claude-opus-5 embedder=google/gemini-embedding-001 ...
```

---

## เช็คว่า model ทำอะไรได้บ้าง

```go
caps := core.LLM(ctx).Capabilities()

caps.Streaming         // stream ได้ไหม
caps.StructuredOutput  // บังคับ JSON schema ได้ไหม
caps.Tools             // เรียก tool ได้ไหม
caps.Reasoning         // ตั้งระดับการคิดได้ไหม
caps.PromptCaching     // cache prompt ได้ไหม
caps.TokenCounting     // นับ token ล่วงหน้าได้ไหม
caps.Vision            // ส่งรูป/เอกสารแนบไปได้ไหม
```

ใช้เมื่ออยากแตกทางก่อนส่ง — เพราะฟีเจอร์ที่ provider ทำไม่ได้จะ **คืน error ไม่ใช่เงียบ ๆ ข้าม** (ดู [Providers & Tuning](./ai-providers.md))

---

## Error

| code | status | ความหมาย | retry ช่วยไหม |
|---|---|---|---|
| `LLM_DISABLED` | 503 | ไม่ได้ตั้ง `AI_PROVIDER`/`AI_MODEL` | ไม่ |
| `LLM_INVALID_CONFIG` | 400 | provider ไม่รู้จัก / `compat` ไม่มี base URL — error ตั้งแต่ boot | ไม่ |
| `LLM_INVALID_REQUEST` | 400 | request ผิดรูป — ไม่ส่งไป provider | ไม่ |
| `LLM_INVALID_SCHEMA` | 400 | type อธิบายเป็น schema ไม่ได้ (recursive / ชนิดไม่รองรับ) | ไม่ |
| `LLM_INVALID_TOOL` | 400 | tool ไม่มีชื่อ/คำอธิบาย หรือ input type อธิบายไม่ได้ | ไม่ |
| `LLM_ATTACHMENT_TOO_LARGE` | 413 | attachment รวมกันเกิน `LLMMaxAttachmentBytes` | ไม่ |
| `LLM_UNSUPPORTED` | 400 | model ทำสิ่งที่ขอไม่ได้ | ไม่ |
| `LLM_UNAUTHORIZED` | 401/403 | key ผิดหรือหมดสิทธิ์ | ไม่ |
| `LLM_MODEL_NOT_FOUND` | 404 | model id ผิด | ไม่ |
| `LLM_RATE_LIMITED` | 429 | โดน rate limit | **ใช่** |
| `LLM_PROVIDER_ERROR` | 5xx | provider ล่ม | **ใช่** |
| `LLM_REQUEST_REJECTED` | 4xx | provider ปฏิเสธ | ไม่ |
| `LLM_TOOL_NOT_LOCAL` | 500 | driver ส่ง tool ที่ provider รันเองกลับเข้าลูปในเครื่อง | ไม่ |
| `EMBED_*` | เหมือนกัน | ฝั่ง embedding ใช้รหัสชุดเดียวกันแต่ขึ้นต้น `EMBED_` (`EMBED_UNSUPPORTED` = ตัวเลือกที่ provider นั้นไม่มี) | |

status ไม่ได้มีไว้สวย ๆ — มันคือคำตอบว่า **retry แล้วมีโอกาสสำเร็จไหม**

```go
resp, err := core.LLM(ctx).Generate(req)
switch {
case err == nil:
case err.GetStatus() == 429, err.GetStatus() >= 500:
    return s.enqueueRetry(ctx, req)          // ลองใหม่ได้
default:
    return ctx.NewError(err, errmsgs.InternalServerError)
}
```

sentinel สำหรับ `errors.Is`:

```go
core.ErrLLMDisabled       // ไม่ได้ตั้งค่า
core.ErrLLMUnsupported    // model ทำไม่ได้ — ไม่ใช่ provider ล่ม
core.ErrEmbedderDisabled
core.ErrEmbedUnsupported  // embedder ไม่รองรับตัวเลือกที่ขอ (Dimensions / Task)
```

---

## Log

หนึ่งบรรทัดต่อหนึ่ง generation — **ระดับตามผลลัพธ์ ไม่ใช่ debug ตายตัว**

```
DEBUG generate google/gemini-2.5-flash stop 3.341s
      provider=google model=gemini-2.5-flash operation=generate took_ms=3340
      prompt_chars=89 input_tokens=373 output_tokens=53 cached_input_tokens=0
      finish_reason=stop steps=2 tool_calls=1
```

หัวข้อของบรรทัดบอกเรื่องได้ด้วยตัวเอง (`generate google/gemini-2.5-flash stop 3.3s`) เพราะ JSON pipeline กับ Sentry Logs แสดงแค่ `msg` จนกว่าจะกดเปิด — `"llm call"` ไม่ตอบคำถามที่คนกำลังกวาดสายตาหาสักข้อ

| สถานการณ์ | ระดับ |
|---|---|
| provider ล่ม (5xx) | `ERROR` + `error.code` |
| ถูกปฏิเสธ (4xx) | `WARN` + `error.code` |
| ช้าเกิน `AI_LOG_SLOW` | `WARN` + `threshold_ms` |
| สำเร็จ | `DEBUG` |

จุดสำคัญ: **generation ที่ล้มต้องไม่จมอยู่ที่ debug** — บน production ที่ `LOG_LEVEL=info` แปลว่าไม่มีอะไรออกมาเลย

### ระดับแยกจากทั้งระบบ

```sh
APP_LOG_LEVEL=info
APP_AI_LOG_LEVEL=debug     # เห็นทุก generation โดยไม่ต้องเปิด debug ทั้งระบบ
```

เข้าชุดกับ `DB_LOG_LEVEL` / `HTTP_LOG_LEVEL` / `DB_MONGO_LOG_LEVEL`

### Tool call มี audit trail เสมอ

```
DEBUG llm tool ran get_order_status     tool=get_order_status outcome=ran ...
WARN  llm tool denied refund_order      tool=refund_order outcome=denied ...
```

`outcome` เป็น `ran` · `denied` · `failed` · `unknown_tool`

**`denied` / `failed` ขึ้นที่ `WARN`** จึงเห็นได้โดยไม่ต้องเปิด debug — การปฏิเสธคือ security control ที่กำลังทำงาน และ tool ที่พังคือของที่ model จะลองใหม่

เขียนให้จาก core ไม่ใช่ปล่อยให้ service ทำเอง เพราะ **service ที่ไม่ได้ใส่ `Approve` คือ service ที่ audit trail สำคัญที่สุด**

`resp.ToolCalls` เก็บครบทั้ง `Input` (model ขออะไร) `Outcome` (เกิดอะไรขึ้น) และ `Output` (model ได้อะไรกลับไป) — สามอย่างนี้ตอบคนละคำถาม และ trail ที่มีแค่สองอย่างแรกอธิบายคำตอบสุดท้ายไม่ได้

::: warning tool ที่ provider รันเอง ไม่มีบรรทัดนี้
`goai.GoogleSearch()` / `URLContext()` / `CodeExecution()` ไม่ผ่าน `RunTool` จึงไม่มี `llm tool ...` และ `resp.ToolCalls` ว่าง สิ่งที่บอกได้คือ **`sources=N` ในบรรทัด generation** ซึ่งเขียนให้เสมอ — generation ที่ไป ground มาจะแยกจาก generation ธรรมดาได้ตรงนี้ (แยกไม่ได้อย่างเดียวคือ "เสนอ tool ไปแล้ว model ไม่ใช้" กับ "ไม่ได้เสนอ" — provider ไม่ได้บอก)
:::

### เนื้อหาไม่ถูกเขียน เว้นแต่จะสั่ง — และแยกขาเข้า/ขาออก

```sh
APP_AI_LOG_PROMPT=true       # ขาเข้า: system, messages, argument ของ tool
APP_AI_LOG_COMPLETION=true   # ขาออก: คำตอบของ model, ผลลัพธ์ของ tool, URL ของ source
```

```
DEBUG generate ...  system="you are a helpful assistant"  msg.0.user="what is 2+2"
DEBUG generate ...  completion="4"  sources=1  source.0="https://go.dev/..."
DEBUG llm tool ran get_order  input={"id":"A1"}  output={"status":"shipped"}
```

**เป็นคนละคีย์กันโดยตั้งใจ** เพราะเปิดเผยคนละอย่าง — prompt คือสิ่งที่ผู้ใช้พิมพ์ ส่วน completion คือสิ่งที่ model พูดและสิ่งที่ tool **อ่านออกมาจากฐานข้อมูล** เพื่อบอก model การเปิด prompt log ไว้จึงต้องไม่ทำให้ผลลัพธ์ query เริ่มไหลลง log ตามไปด้วย

ปิดทั้งคู่เป็นค่าเริ่มต้น ด้วยเหตุผลเดียวกับ `HTTP_LOG_BODY` — log store คือที่ที่ข้อมูลจะไปโผล่ในที่ที่ไม่มีใครตั้งใจได้ง่ายที่สุด

ต่อให้เปิด: ตัดที่ 2,000 ตัวอักษรต่อฟิลด์ และ **ไม่เขียน bytes ของ attachment เลย** (ขึ้นเป็น `[+1 attachment(s)]` แทน) เพราะ base64 จะกลบข้อความที่เปิดมาเพื่อจะอ่าน

ยกเว้นเดียวที่เขียนเต็มคือ **URL ของ source** — มันคือสิ่งที่คำตอบอ้างว่าอ้างอิงมา และคำตอบที่ไล่กลับไปหาที่มาไม่ได้คือสิ่งที่ grounding มีไว้ป้องกันตั้งแต่แรก

### ปิดทั้งหมด

```sh
APP_AI_LOG_LEVEL=silent
```

สำหรับ service ที่บันทึกการเรียก model ไว้ที่อื่นอยู่แล้ว

---

## Metric

ทุก generation ถูกบันทึกอัตโนมัติ แยกตาม `provider` / `model` / `operation`:

| metric | หน่วย |
|---|---|
| `llm.latency` | ms — วัดถึง token สุดท้าย ทั้ง `Generate` และ `Stream` จึงเทียบกันได้ |
| `llm.tokens.input` | token |
| `llm.tokens.output` | token |
| `llm.tokens.cached_input` | token — คิดเงินคนละเรตกับ input ปกติ จึงแยกไว้ |
| `llm.tokens.reasoning` | token |
| `llm.errors` | นับ พร้อม attribute `code` |

stream ที่ถูกทิ้งกลางคันก็ยังถูกบันทึก — token ที่ใช้ไปแล้วก็คือใช้ไปแล้ว

การนับต้นทุนควรมาจาก metric ไม่ใช่การ grep log

---

## อ่านต่อ

- [Provider Setup](./ai-setup.md) — Claude · OpenAI · Gemini · Ollama · Vertex/Bedrock
- [Text Generation](./ai-text.md) — `Generate`, บทสนทนาหลายเทิร์น, `FinishReason`
- [Streaming](./ai-streaming.md) — iterator, SSE, การปิดกลางคัน
- [Typed Values](./ai-typed.md) — `llm.New[T]`, schema จาก struct, validation
- [Attachments](./ai-attachments.md) — รูปภาพ, PDF, ต้นทุนของรูป
- [Tool Use](./ai-tools.md) — `llm.Tool[In]`, ลูป, `Approve`
- [Agent Loops](./ai-agent.md) — ลูปยาว, ต้นทุน, รันเป็น job
- [Embeddings](./ai-embeddings.md) — เวกเตอร์, similarity, RAG
- [Best Practices](./ai-patterns.md) — เลือกชั้นให้ถูก, คุมต้นทุน, prompt, tool, checklist
- [Providers & Tuning](./ai-providers.md) — reasoning, prompt caching, หลาย model, escape hatch, MCP
- [Testing](./ai-testing.md) — memory model, conformance suite
