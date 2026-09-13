# AI · Providers & Tuning

## Provider ที่ driver รู้จัก

| ค่า `AI_PROVIDER` | หมายเหตุ |
|---|---|
| `anthropic` | Claude |
| `openai` | รวม Azure OpenAI ผ่าน `AI_BASE_URL` |
| `google` / `gemini` | |
| `ollama` | ในเครื่อง — ไม่ต้องมี key |
| `compat` | **ทุกเจ้าที่พูด OpenAI protocol** — ต้องมี `AI_BASE_URL` |

ระบุชื่อไว้เฉพาะเจ้าที่ **wire shape หรือวิธี auth ต่างกันจริง** ที่เหลือพูดโปรโตคอลเดียวกันหมด `compat` จึงถึงได้ทันทีโดยที่ switch ไม่ต้องโตขึ้นหนึ่ง case ต่อหนึ่ง vendor

```sh
# Groq
APP_AI_PROVIDER=compat
APP_AI_BASE_URL=https://api.groq.com/openai/v1
APP_AI_MODEL=llama-3.3-70b-versatile
APP_AI_API_KEY=gsk_...

# DeepSeek
APP_AI_BASE_URL=https://api.deepseek.com/v1
APP_AI_MODEL=deepseek-chat

# OpenRouter
APP_AI_BASE_URL=https://openrouter.ai/api/v1
APP_AI_MODEL=anthropic/claude-opus-5

# vLLM / LiteLLM / อะไรก็ตามที่รัน OpenAI-compatible
APP_AI_BASE_URL=http://10.0.0.7:8000/v1
```

`bedrock` / `vertex` / `azure` ในโหมด native ยังไม่รองรับ — ต้องมี config เพิ่ม (region, project, deployment) เป็นงานแยกที่ตั้งใจทำ ระหว่างนี้ Azure ใช้ `openai` + `AI_BASE_URL` ได้

### dev บนเครื่องด้วย Ollama

```sh
APP_AI_PROVIDER=ollama
APP_AI_BASE_URL=http://127.0.0.1:11434
APP_AI_MODEL=llama3.2
```

ไม่ต้องมี key ไม่ต้องมีเน็ต โค้ดไม่ต้องแก้สักบรรทัด

---

## ฟีเจอร์ที่ provider ทำไม่ได้

**จุดที่ multi-provider abstraction ส่วนใหญ่พัง** คือบีบ API ลงเหลือส่วนที่ทุกเจ้าทำได้ แล้วเสีย prompt caching / structured output / reasoning ไปหมด

v2 ทำกลับกัน — `LLMRequest` มีทุกฟิลด์ ส่วน driver ที่ทำไม่ได้จะ **คืน error ไม่ใช่เงียบ ๆ ทิ้งฟิลด์**

```go
_, err := core.LLM(ctx).Generate(core.LLMRequest{
    Messages:  msgs,
    Reasoning: core.LLMReasoningHigh,
})
if errors.Is(err, core.ErrLLMUnsupported) {
    // model นี้ไม่รองรับ — ไม่ใช่ provider ล่ม
}
```

เหตุผล: **คำตอบจาก model ที่ไม่ได้คิดตามที่สั่ง หน้าตาเหมือนคำตอบปกติทุกประการ** ถ้าไม่ error ก็ไม่มีทางรู้ว่าจ่ายเงินค่าคุณภาพที่ไม่ได้รับ

เช็คก่อนส่งได้ด้วย `Capabilities()`:

```go
if !core.LLM(ctx).Capabilities().Reasoning {
    // เตรียมทางอื่นไว้
}
```

---

## Reasoning

```go
core.LLMRequest{
    Messages:  msgs,
    Reasoning: core.LLMReasoningHigh,
}
```

| ค่า | ความหมาย |
|---|---|
| `LLMReasoningDefault` (`""`) | ปล่อยตามค่าเริ่มต้นของ provider |
| `LLMReasoningOff` | ไม่ให้คิด |
| `LLMReasoningLow` · `Medium` · `High` · `Max` | ระดับความลึก |

เป็น **level ไม่ใช่ token budget** เพราะแต่ละเจ้าเขียนคนละแบบ — effort enum, token budget, หรือ boolean — และ level เป็นรูปเดียวที่ทุกเจ้าแปลได้

| provider | แปลเป็น |
|---|---|
| anthropic | `thinking: {type: adaptive}` + `output_config.effort` |
| openai / compat | `reasoning_effort` |
| google — `gemini-3*` ขึ้นไป และ alias `*-latest` | `thinkingConfig.thinkingLevel` (`low` · `medium` · `high`) |
| google — `gemini-2.5*` | `thinkingConfig.thinkingBudget` (`0` · 2048 · 8192 · 24576 · `-1` แบบ dynamic) |
| google — รุ่นอื่น (Gemma, 1.5, 2.0) / ollama | ไม่รองรับ → `LLM_UNSUPPORTED` |

ฝั่ง Google ต้องดู **ชื่อ model ไม่ใช่แค่ provider** เพราะสองตระกูลเขียนคนละแบบและ **API บังคับใช้จริง ไม่ได้เมินเงียบ ๆ** — ยิงกับของจริงแล้วได้:

```
gemini-2.5-flash + thinkingLevel      400 Thinking level is not supported for this model
gemini-3.x       + thinkingBudget: 0  400 Budget 0 is invalid. This model only works in thinking mode
gemini-3.x       + thinkingBudget: n  ผ่านและมีผลจริง
```

การ map จึงไม่ได้มีไว้กันจ่ายเงินฟรี แต่มีไว้ให้ฟิลด์นี้ **ใช้ได้จริงกับทั้งสองตระกูล** โดยไม่ต้องให้ call site รู้ว่ากำลังคุยกับรุ่นไหน

**alias (`gemini-flash-latest`, `gemini-pro-latest`, `gemini-flash-lite-latest`) ใช้ `Reasoning` ได้** — alias ชี้ไปที่รุ่นปัจจุบันเสมอ และรุ่นปัจจุบันคิดได้ทั้งหมด (ตรวจแล้ว: `gemini-flash-latest` → `gemini-3.6-flash`, `gemini-pro-latest` → `gemini-3.1-pro-preview`) เวอร์ชันถูก **อ่านเป็นตัวเลข** ไม่ใช่ไล่ prefix ทีละรุ่น `gemini-4` ในอนาคตจึงไม่ต้องมาแก้ตรงนี้อีก

`Capabilities().Reasoning` ตอบตามรุ่นที่ config ไว้จริง — เช็คตอน boot ได้ว่า model ที่ตั้งไว้คิดได้ไหม ก่อนจะไปเจอตอน request

`LLMReasoningOff` บน OpenAI คืน unsupported — reasoning model ของเขาคิดเสมอ ไม่มีปุ่มปิด การส่งไปเฉย ๆ แล้วบอกว่าปิดแล้วคือการโกหก เช่นเดียวกับ `gemini-3*` ที่ปิดการคิดไม่ได้ (ใช้ `LLMReasoningLow` แทน) ส่วน `gemini-2.5*` ปิดได้ด้วย budget 0

ตัวเลข budget ของ 2.5 ไม่ใช่ mapping ที่ Google ประกาศไว้ (ไม่มี) — เลือกให้ระดับที่สูงกว่าได้ budget มากกว่าเสมอ ถ้าต้องการตัวเลขเป๊ะ ๆ ส่งเองผ่าน `ProviderOptions` ได้ และค่าที่ส่งเองจะ merge ทับค่าที่ map ไว้:

```go
core.LLMRequest{
    Messages:  msgs,
    Reasoning: core.LLMReasoningHigh,
    ProviderOptions: map[string]any{
        "google": map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": 12000}},
    },
}
```

กับ typed:

```go
llm.New[Answer](ctx).Reasoning(core.LLMReasoningMax).Extract(hardProblem)
```

---

## Prompt caching

```go
core.LLMRequest{
    System:      longSystemPrompt,
    CacheSystem: true,
    Messages:    msgs,
}
```

เป็น **hint** — เจ้าที่ cache อัตโนมัติจะเมิน และเจ้าที่ cache ไม่ได้ก็เมิน เพราะ cache ที่พลาดทำให้จ่ายแพงขึ้น แต่ **ไม่เคยทำให้คำตอบเปลี่ยน** จึงไม่ใช่เรื่องที่ควร error

### ตรวจว่าติดจริงไหม

```go
resp.Usage.CachedInputTokens   // 0 ตลอด = ไม่ติด
```

ถ้าเป็น 0 ตลอดทั้งที่ prompt ควรจะเหมือนเดิม แปลว่ามีอะไรเปลี่ยนอยู่ข้างใน — ตัวที่เจอบ่อยที่สุด:

```go
// ❌ prompt เปลี่ยนทุก request → cache ไม่ติดทั้งก้อน
System: fmt.Sprintf("วันนี้คือ %s\n%s", time.Now().Format("2006-01-02"), rules)

// ✅ ของที่เปลี่ยนไปไว้ท้ายสุด
System: rules,
Messages: []core.LLMMessage{
    core.LLMUser("วันนี้คือ " + today + "\n\n" + question),
},
```

prompt cache ทำงานแบบ **prefix match** — ไบต์เดียวที่เปลี่ยนตรงต้น ทำให้ทุกอย่างหลังจากนั้นใช้ไม่ได้

---

## ของเฉพาะ provider

```go
core.LLMRequest{
    Messages: msgs,
    ProviderOptions: map[string]any{
        "thinking": map[string]any{"type": "adaptive", "display": "summarized"},
    },
}
```

ค่าที่ใส่เอง **ชนะ** ค่าที่ core แปลงให้ เพราะถือว่าตั้งใจเรียกด้วยภาษาของ provider นั้นโดยตรง

แลกมาด้วยการไม่มี compile-time check — ใช้ฟิลด์ที่มีชนิดชัดเจนก่อนเสมอ แล้วค่อยลงมาที่นี่เมื่อไม่มีทางอื่น

---

## หลาย model ในโปรเซสเดียว

model ที่ลงทะเบียนกับ App มีตัวเดียว — ตัวที่สองสร้างเองแล้วถือไว้ใน service

```go
type Service struct {
    cheap core.ILLM     // จัดหมวด/คัดกรอง
}

func NewService(env core.IENV) (*Service, core.IError) {
    cheap, err := goai.New(env,
        goai.WithProvider("compat"),
        goai.WithBaseURL("https://api.groq.com/openai/v1"),
        goai.WithModel("llama-3.3-70b-versatile"),
        goai.WithAPIKey(env.Config().GroqKey),
    )
    if err != nil {
        return nil, err
    }
    return &Service{cheap: cheap}, nil
}

func (s Service) Triage(ctx core.IContext, text string) (string, core.IError) {
    resp, err := s.cheap.WithContext(ctx).Generate(core.LLMRequest{
        System:   "ตอบคำเดียว: spam หรือ ham",
        Messages: []core.LLMMessage{core.LLMUser(text)},
    })
    if err != nil {
        return "", err
    }
    return resp.Text, nil
}
```

⚠️ `WithContext(ctx)` สำคัญ — ไม่งั้น generation ไม่ผูกกับ deadline ของ request และจะไม่ถูกยกเลิกเมื่อ client หลุด

### เปลี่ยน model รายคำขอด้วย `req.Model`

ส่ง model id ของ provider เดิมได้เลย — driver สร้าง client ให้ครั้งเดียวแล้วใช้ซ้ำ:

```go
resp, err := core.LLM(ctx).Generate(core.LLMRequest{
    Model:    "gemini-flash-lite-latest",   // งานถูก ๆ ไม่ต้องใช้ตัวแพง
    Messages: msgs,
})
resp.Model   // "gemini-flash-lite-latest" — ตัวที่ถูกเรียกจริง ไม่ใช่ตัวที่ config ไว้
```

กับ typed ก็มี:

```go
llm.New[Category](ctx).Model("gemini-flash-lite-latest").Extract(text)
```

**เหตุผลที่ควรทำแบบนี้แทนการสร้าง client ตัวที่สองเอง**: handle ที่สร้างนอก `core.NewApp` ไม่ถูก instrument — token ไม่เข้า `llm.tokens.*` ไม่มี log ต่อ generation และไม่มี metric ให้ตามหลัง เส้นทางนี้ผ่าน handle เดิมทั้งหมด ทุก metric จึงถูก tag ด้วย model ที่เรียกจริง

ยังคงเป็น model id ของ provider ตรง ๆ — logical alias (`"fast"` → `groq/llama-3.3-70b`) เป็นเรื่องของ configuration ไม่ใช่ของ request

::: warning
`goai.NewModel(...)` — handle ที่ห่อ client ที่เราสร้างเอง — ยังปฏิเสธ `req.Model` อยู่ (`LLM_INVALID_REQUEST`) เพราะ key กับ endpoint อยู่ใน closure ของคนเรียก ไม่มีอะไรให้สร้างตัวที่สองจาก

การ **ข้าม provider** ก็ยังต้องสร้าง handle ที่สอง — model id มีความหมายเฉพาะกับ provider ของมันเอง ฝั่ง typed ใช้ `llm.New[T](ctx).Using(handle)` ได้
:::

---

## Escape hatch

`Unwrap()` คืน model ของ engine ข้างใน (goai) ตรง ๆ — ทางเข้าสู่ทุกอย่างที่ core ไม่ได้ห่อไว้

```go
import (
    sdk "github.com/zendev-sh/goai"
    "github.com/zendev-sh/goai/provider"
)

raw, ok := core.LLM(ctx).Unwrap().(provider.LanguageModel)
if !ok {
    return core.LLMDisabledError()
}

res, err := sdk.GenerateText(ctx, raw, sdk.WithPrompt(q), sdk.WithMaxSteps(5))
```

### MCP (Model Context Protocol)

```go
import "github.com/zendev-sh/goai/mcp"

client := mcp.NewClient("my-service", "1.0.0",
    mcp.WithTransport(mcp.NewStdioTransport("node", []string{"./mcp-server.js"})),
    mcp.WithRequestTimeout(20*time.Second),
)
if err := client.Connect(ctx); err != nil {
    return err
}
defer client.Close()

list, err := client.ListTools(ctx, nil)
if err != nil {
    return err
}
tools := mcp.ConvertTools(client, list.Tools)

raw, _ := core.LLM(ctx).Unwrap().(provider.LanguageModel)
res, gerr := sdk.GenerateText(ctx, raw,
    sdk.WithSystem("ใช้เครื่องมือที่มี"),
    sdk.WithPrompt(question),
    sdk.WithTools(tools...),
    sdk.WithMaxSteps(5),
)
```

transport มีทั้ง `NewStdioTransport` (spawn process), `NewHTTPTransport` และ `NewSSETransport` — ทดสอบบน Windows แล้ว stdio ใช้ได้ปกติ

**ทำไมไม่ยกเข้า core:** MCP เป็นการเชื่อมต่อระดับ service (จะต่อ server ไหน ด้วย transport อะไร) ไม่ใช่นโยบายที่ทุก service ใช้ร่วมกัน — ต่างจาก [tool loop](./ai-tools.md) ที่ต้องมีจุดอนุมัติกลาง

### สร้างรูปภาพ

```go
res, err := sdk.GenerateImage(ctx, imageModel, sdk.WithPrompt("a cat wearing a hat"))
```

**ทำไมไม่ยกเข้า core:** surface ใหญ่ (ชนิดไฟล์, ที่เก็บผล, presigned URL) แลกกับ use case ที่แคบ

### ของอื่น

web search, code execution, computer use, provider-defined tools (X search, file search), lifecycle hooks ทั้ง 9 ตัว — ผูกกับ vendor ตัวเดียวหรือกว้างเกินกว่าจะ normalise ใช้ `ProviderOptions` หรือ `Unwrap()`

### สิ่งที่หายไปเมื่อใช้ `Unwrap()`

| ผ่าน `core.LLM(ctx)` | ผ่าน `Unwrap()` |
|---|---|
| error เป็น `IError` แยก retry ได้/ไม่ได้ | error ดิบของ goai |
| usage เข้า `ctx.Meter()` อัตโนมัติ | **ไม่มีใครนับ token** |
| ผูก deadline ของ request ให้ | ต้องส่ง ctx เอง |
| `NewMemoryLLM` เทสต์ได้โดยไม่มี key | ไม่มี test double |
| `Approve` ก่อน tool ทำงาน | **ไม่มี** |

**ถ้าโค้ดที่ใช้ `Unwrap()` เริ่มเยอะขึ้นเรื่อย ๆ นั่นคือสัญญาณว่าควรยกฟีเจอร์นั้นเข้า core**

---

## เปลี่ยน engine

`Unwrap()` เป็นจุดเดียวที่ผูกกับ goai — โค้ดอื่นทั้งหมดคุยผ่าน `core.ILLM` ถ้าวันหนึ่งต้องเปลี่ยน engine (goai เป็น pre-1.0 และปล่อย release ทุกไม่กี่วัน) จะแก้แค่ `v2/llm/goai/` และ conformance suite จะบอกทันทีว่าอะไรเพี้ยน

นั่นคือเหตุผลทั้งหมดที่ interface นี้มีอยู่ — รายละเอียดใน [design doc](https://github.com/pskclub/mine-core/blob/master/design/v2/17-ai.md)

---

ต่อไป: [Testing](./ai-testing.md)
