# AI · Testing

ทุกอย่างในหน้านี้ทำงานได้ **โดยไม่ต้องมี API key ไม่ต้องมีเน็ต และไม่มีค่าใช้จ่าย**

```go
m := core.NewMemoryLLM("Bangkok")
app, _ := core.NewApp(env, core.WithLLM(m))

// ... รัน service ...

calls := core.LLMCalls(m)
require.Len(t, calls, 1)
assert.Contains(t, calls[0].System, "ตอบสั้น ๆ")
```

| ฟังก์ชัน | ใช้ทำอะไร |
|---|---|
| `NewMemoryLLM(replies...)` | model ในหน่วยความจำ |
| `QueueLLMReply(m, reply)` | สคริปต์ usage, error, tool call หรือ source ของคำตอบที่ ground |
| `MemoryToolCall(name, json)` | ย่อสำหรับสคริปต์ tool call หนึ่งครั้ง |
| `LLMCalls(m)` | request ที่ถูกบันทึกไว้ ตามลำดับ |
| `ResetLLM(m)` | ล้างและ rewind |
| `NewMemoryEmbedder(dims)` | embedder ในหน่วยความจำ |
| `EmbeddedTexts(e)` | ข้อความที่ถูกส่งไป embed |
| `EmbeddedRequests(e)` | คำขอทั้ง request รวม `Dimensions` / `Task` |

## กติกาของ reply

reply ถูกใช้ **ตามลำดับ** และ **หมดแล้วเรียกอีกจะ error** ไม่ใช่วนซ้ำ:

```
LLM_MEMORY_EXHAUSTED: llm: memory model was called 3 times but only 2 replies were scripted
```

จงใจ — service ที่เรียก model บ่อยกว่าที่ test เขียนไว้คือ **พฤติกรรมที่เปลี่ยนไป** การแอบตอบให้อีกครั้งคือวิธีที่ทำให้ไม่มีใครรู้

ถ้าไม่สคริปต์อะไรเลย:

| สถานการณ์ | memory model ทำอะไร |
|---|---|
| ปกติ | echo ข้อความ user ตัวสุดท้าย |
| request มี `Schema` | ตอบ `{}` |
| request มี `Tools` + `MaxSteps` > 1 | **เรียกทุก tool หนึ่งครั้ง** แล้วค่อยตอบ |
| tool ที่ provider รันเอง (`ProviderType`) | ข้าม — ไม่มี handler ให้รันที่ฝั่งนี้ |

แถวที่สามมีประโยชน์กว่าที่คิด — service ที่เพิ่ม tool ใหม่จะได้ handler ถูกรันทันที โดยไม่ต้องมีใครไปเขียนสคริปต์ให้ ส่วนแถวสุดท้ายทำให้ test ที่ใช้ `goai.GoogleSearch()` ไม่พังเพราะ stand-in ไปเรียกสิ่งที่ไม่มีตัวตนในเครื่อง

---

## หา memory model เจอแม้ App จะห่อไว้

`NewApp` ห่อ model ที่ลงทะเบียนด้วย instrumentation สำหรับนับ token — handle ที่ test ถืออยู่จึงไม่ใช่ตัวเดียวกับที่ส่งเข้าไป

`LLMCalls`, `QueueLLMReply`, `ResetLLM` แกะชั้นห่อให้เอง จึงใช้กับ `m` ตัวเดิมที่ส่งเข้า `WithLLM` ได้ตรง ๆ

---

## Test typed extraction

สคริปต์เป็น JSON ตรง ๆ:

```go
func TestExtractInvoice(t *testing.T) {
    m := core.NewMemoryLLM(`{"vendor":"ACME","total":1250.5,"status":"paid","due":"2026-08-31T00:00:00Z"}`)
    app, _ := core.NewApp(env, core.WithLLM(m))
    ctx := app.NewContext(context.Background(), core.ModeTest)

    inv, err := llm.New[Invoice](ctx).System(rules).Extract("...")
    require.NoError(t, err)
    assert.Equal(t, "ACME", inv.Vendor)
    assert.InDelta(t, 1250.5, inv.Total, 0.001)

    // ยืนยันว่า schema ถูกส่งไปจริง
    req := core.LLMCalls(m)[0]
    require.NotNil(t, req.Schema)
    assert.Equal(t, "invoice", req.Schema.Name)

    props := req.Schema.Schema["properties"].(map[string]any)
    assert.Equal(t, []string{"draft", "sent", "paid"},
        props["status"].(map[string]any)["enum"])
}
```

### validation ที่ล้ม

```go
m := core.NewMemoryLLM(`{"score":42}`)      // นอกช่วง 0-10

_, err := llm.New[Reviewed](ctx).Extract("x")
require.Error(t, err)
assert.Equal(t, "BAD_SCORE", err.GetCode())
```

### model ตอบไม่เป็น JSON

```go
m := core.NewMemoryLLM("ผมว่าน่าจะดีนะครับ")

_, err := llm.New[Sentiment](ctx).Extract("x")
require.Error(t, err)
assert.Contains(t, err.Error(), "ผมว่าน่าจะดี")   // คำตอบดิบอยู่ใน error
```

---

## Tool

สคริปต์ให้ model "ตัดสินใจ" เรียก tool — เพราะการตัดสินใจเป็นงานของ model และนี่คือทางเดียวที่จะไปถึง handler โดยไม่ต้องมี provider

```go
func TestOrderLookupTool(t *testing.T) {
    var sawOrderID string
    tool, err := llm.Tool("get_order_status", "ดูสถานะออเดอร์...",
        func(_ context.Context, in struct {
            OrderID string `json:"order_id"`
        }) (string, core.IError) {
            sawOrderID = in.OrderID
            return `{"status":"shipped"}`, nil
        })
    require.NoError(t, err)

    m := core.NewMemoryLLM()
    core.QueueLLMReply(m,
        core.MemoryLLMReply{ToolCalls: []core.LLMToolCall{
            core.MemoryToolCall("get_order_status", `{"order_id":"TH-1042"}`),
        }},
        core.MemoryLLMReply{Text: "กำลังจัดส่งอยู่ครับ"},
    )
    app, _ := core.NewApp(env, core.WithLLM(m))
    ctx := app.NewContext(context.Background(), core.ModeTest)

    resp, err := core.LLM(ctx).Generate(core.LLMRequest{
        Messages: []core.LLMMessage{core.LLMUser("ของ TH-1042 อยู่ไหน")},
        Tools:    []core.LLMTool{tool},
        MaxSteps: 5,
    })
    require.NoError(t, err)

    assert.Equal(t, "TH-1042", sawOrderID, "handler ได้ argument ที่ model ส่งมา")
    assert.Equal(t, "กำลังจัดส่งอยู่ครับ", resp.Text)
    assert.Equal(t, 2, resp.Steps)
    require.Len(t, resp.ToolCalls, 1)
}
```

### นโยบายอนุมัติ

```go
func TestRefundNeedsApproval(t *testing.T) {
    ran := false
    tool, _ := llm.Tool("refund_order", "คืนเงิน...", func(...) { ran = true; ... })

    m := core.NewMemoryLLM()
    core.QueueLLMReply(m,
        core.MemoryLLMReply{ToolCalls: []core.LLMToolCall{
            core.MemoryToolCall("refund_order", `{"id":"A1"}`),
        }},
        core.MemoryLLMReply{Text: "ขออภัย ต้องให้เจ้าหน้าที่อนุมัติก่อน"},
    )

    resp, err := core.LLM(ctx).Generate(core.LLMRequest{
        Messages: msgs,
        Tools:    []core.LLMTool{tool},
        MaxSteps: 5,
        Approve:  s.approve,          // นโยบายจริงของ service
    })
    require.NoError(t, err, "การปฏิเสธไม่ใช่ generation ที่ล้ม")
    assert.False(t, ran, "handler ต้องไม่ถูกเรียก")
    assert.Len(t, resp.ToolCalls, 1, "call ที่ถูกปฏิเสธยังอยู่ใน audit trail")
    assert.Contains(t, resp.ToolCalls[0].Output, "ต้องให้เจ้าหน้าที่อนุมัติ",
        "ข้อความที่ model ได้กลับไปคือสิ่งที่อธิบายคำตอบสุดท้าย")
}
```

### คำตอบที่ ground มา

`Sources` สคริปต์ได้ ทดสอบ UI ที่ต้องแสดงที่มาได้โดยไม่ต้องมี provider:

```go
core.QueueLLMReply(m, core.MemoryLLMReply{
    Text: "Go เวอร์ชันล่าสุดคือ 1.26.5",
    Sources: []core.LLMSource{
        {Type: "url", URL: "https://go.dev/doc/devel/release", Title: "Releases"},
    },
})

resp, _ := core.LLM(ctx).Generate(req)
assert.Len(t, resp.Sources, 1)
```

### ชนเพดาน step

```go
m := core.NewMemoryLLM()
core.QueueLLMReply(m,
    core.MemoryLLMReply{ToolCalls: []core.LLMToolCall{core.MemoryToolCall("loop", "{}")}},
    core.MemoryLLMReply{ToolCalls: []core.LLMToolCall{core.MemoryToolCall("loop", "{}")}},
    core.MemoryLLMReply{ToolCalls: []core.LLMToolCall{core.MemoryToolCall("loop", "{}")}},
)

resp, _ := core.LLM(ctx).Generate(core.LLMRequest{
    Messages: msgs, Tools: tools, MaxSteps: 2,
})
assert.Equal(t, core.LLMFinishToolUse, resp.FinishReason)
```

---

## Error และต้นทุน

```go
// rate limit
core.QueueLLMReply(m, core.MemoryLLMReply{
    Err: core.New(429, "LLM_RATE_LIMITED", "slow down"),
})

// usage ที่ service ต้องบันทึกลง DB
core.QueueLLMReply(m, core.MemoryLLMReply{
    Text:  `{"score":8}`,
    Usage: core.LLMUsage{InputTokens: 1200, OutputTokens: 40, CachedInputTokens: 3000},
})

// คำตอบที่โดนตัด
core.QueueLLMReply(m, core.MemoryLLMReply{
    Text:         "คำตอบครึ่ง",
    FinishReason: core.LLMFinishLength,
})
```

ทั้งสามเคสคือ path ที่รอเจอใน production ได้ยาก แต่ต้องทำงานถูกเมื่อเจอ

---

## Embeddings

```go
e := core.NewMemoryEmbedder(64)
app, _ := core.NewApp(env, core.WithEmbedder(e))

// ... รัน service ...

assert.Equal(t, []string{"doc one", "doc two"}, core.EmbeddedTexts(e))
```

ถ้าใช้ `EmbedWith` — ตรวจได้ว่าฝั่ง index กับฝั่งค้นหาใช้ `Task` คู่กันจริง และขอความยาวตรงกับคอลัมน์ ซึ่งเป็นคู่ที่พังแบบเงียบที่สุด:

```go
reqs := core.EmbeddedRequests(e)
assert.Equal(t, core.EmbedDocument, reqs[0].Task)
assert.Equal(t, core.EmbedQuery, reqs[1].Task)
assert.Equal(t, 768, reqs[0].Dimensions)
```

memory embedder คืนเวกเตอร์ยาวตาม `Dimensions` ที่ขอมา — stand-in ที่คืนความยาวของตัวเองจะทำให้ test ผ่านทั้งที่ของจริง insert ไม่ลง

memory embedder ให้ผล **เหมือนเดิมทุกครั้ง** (hash ของคำ) — test ที่ assert ลำดับผลลัพธ์จึงไม่ flaky:

```go
vecs, _ := e.Embed(
    "the cat sat on the mat",
    "a cat sat on a mat today",
    "quarterly revenue exceeded forecast",
)

related := core.CosineSimilarity(vecs[0], vecs[1])
unrelated := core.CosineSimilarity(vecs[0], vecs[2])
assert.Greater(t, related, unrelated)
```

⚠️ มันเป็น **bag-of-words ไม่ใช่ embedding จริง** — เข้าใจว่ามีคำอะไรบ้าง ไม่เข้าใจว่าแปลว่าอะไร ใช้เทสต์ท่อ (index ถูกไหม, จับคู่ index ถูกไหม, จัดอันดับถูกไหม) **ไม่ใช่เทสต์คุณภาพการค้นหา** — อันนั้นต้องใช้ของจริงใน integration test

---

## Conformance suite

`llmtest` คือ contract ที่ทุก driver ต้องผ่าน — **รวมถึง `memoryLLM` ด้วย**

```go
func TestConformance(t *testing.T) {
    llmtest.RunSuite(t, func(t *testing.T) core.ILLM { return newMyDriver(t) })
}
```

มันตรวจอะไร:

| เคส | ยืนยันว่า |
|---|---|
| `Generate` | มีข้อความ, มี `FinishReason`, มี usage |
| `RejectsEmptyConversation` | request ผิดรูปไม่ถูกส่งไป provider และเป็น 400 ไม่ใช่ 5xx |
| `RejectsUnknownRole` | role `system` ในรายการข้อความถูกปฏิเสธ |
| `Stream` | delta ที่ต่อกันเท่ากับ `Response().Text` |
| `StreamCloseIsIdempotent` | `Close()` ซ้ำได้ |
| `StreamCloseBeforeDrain` | ปิดกลางคันไม่รั่ว |
| `CapabilitiesAreHonest` | บอกว่าทำได้ = ทำได้จริง, บอกว่าไม่ได้ = คืน `ErrLLMUnsupported` |
| `StructuredOutputIsJSON` | JSON mode คืน JSON ที่ parse ได้ |
| `ToolLoop` | tool ถูกเรียกจริง, มี audit trail |
| `ToolApprovalIsEnforced` | call ที่ถูกปฏิเสธไม่ถึง handler |
| `PerRequestModelIsHonouredOrRefused` | `req.Model` ถูกใช้จริงและรายงานกลับ หรือถูกปฏิเสธ — ไม่มีทางที่สามคือเงียบ ๆ ใช้ตัวที่ config ไว้ |
| `HonoursCancelledContext` | handle ที่ผูกกับ request ที่ตายแล้วไม่เผา token ต่อ |

**เหตุผลที่ memory model รัน suite เดียวกับ driver จริง:** test ที่สลับ provider เป็น `NewMemoryLLM` จะเชื่อถือได้ก็ต่อเมื่อสองอย่างประพฤติเหมือนกันที่ขอบ — error code เดียวกัน, usage เหมือนกัน, semantics ของ stream และ `Close` เหมือนกัน ตรงไหนที่ต่างกัน คือตรงที่ test ผ่านแล้ว production พัง

---

## Integration test กับ provider จริง

```sh
APP_AI_PROVIDER=google APP_AI_MODEL=gemini-2.5-flash \
  APP_AI_EMBED_MODEL=gemini-embedding-001 \
  APP_AI_API_KEY=... make test-integration
```

อยู่ใน `v2/llm/goai/goai_integration_test.go` หลัง build tag `integration` — skip เองเมื่อไม่มี key จึงไม่พัง CI

มันครอบคลุมสิ่งที่ fake server ตรวจไม่ได้: request ที่ driver สร้างเป็นสิ่งที่ API ยอมรับจริงไหม, SSE ของจริง parse ได้ไหม, JSON mode บังคับ schema จริงไหม, และ tool approval gate อยู่ก่อน handler จริงไหม

ใช้กับ provider ไหนก็ได้:

```sh
APP_AI_PROVIDER=compat APP_AI_BASE_URL=https://api.groq.com/openai/v1 \
  APP_AI_MODEL=llama-3.3-70b-versatile APP_AI_API_KEY=... make test-integration
```

---

กลับไป: [Overview](./ai.md)
