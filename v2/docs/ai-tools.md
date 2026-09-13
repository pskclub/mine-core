# AI · Tool Use

Tool คือฟังก์ชันของเราที่ **model ตัดสินใจเรียกเอง** — ดูข้อมูลใน DB, ยิง API, คำนวณ

```go
import "github.com/pskclub/mine-core/v2/llm"

orderTool, err := llm.Tool("get_order_status",
    "ดูสถานะการจัดส่งของออเดอร์ เรียกเมื่อผู้ใช้ถามว่าของอยู่ไหน",
    func(ctx context.Context, in struct {
        OrderID string `json:"order_id" jsonschema:"description=รหัสออเดอร์ เช่น TH-1042"`
    }) (string, core.IError) {
        o, err := repository.New[Order](ctx).FindOne("code = ?", in.OrderID)
        if err != nil {
            return "", err
        }
        return fmt.Sprintf(`{"status":%q,"courier":%q}`, o.Status, o.Courier), nil
    })
```

`llm.Tool[In]` สร้าง JSON Schema จาก `In` ให้เอง (ตัวเดียวกับที่ [`llm.New[T]`](./ai-typed.md) ใช้) แล้ว unmarshal argument ของ model ให้ก่อนเรียกฟังก์ชัน — **tool จึงเป็นโค้ด Go ธรรมดาที่บังเอิญ model เรียกได้**

## description บอก "เมื่อไหร่" ไม่ใช่แค่ "อะไร"

```go
// ❌ model ไม่รู้ว่าควรเรียกตอนไหน
"ค้นหาออเดอร์"

// ✅
"ดูสถานะการจัดส่งของออเดอร์ เรียกเมื่อผู้ใช้ถามว่าของอยู่ไหน ถึงเมื่อไหร่ หรือถามถึงเลขพัสดุ"
```

มันคือสิ่งเดียวที่ model ใช้ตัดสินใจ — คำอธิบายที่บอกแค่ว่าฟังก์ชันทำอะไร จะได้ tool ที่ถูกเรียกมั่ว หรือไม่เคยถูกเรียกเลย

## tool ที่ไม่รับ argument

```go
tool, err := llm.Tool("current_time", "เวลาปัจจุบันตามเขตเวลาไทย",
    func(ctx context.Context, _ struct{}) (string, core.IError) {
        return time.Now().In(bangkok).Format(time.RFC3339), nil
    })
```

---

## รันลูป

```go
resp, err := core.LLM(ctx).Generate(core.LLMRequest{
    System:   "คุณคือแอดมินซัพพอร์ต ใช้เครื่องมือที่มี ตอบสั้น ๆ",
    Messages: []core.LLMMessage{core.LLMUser("ของ TH-1042 อยู่ไหนแล้ว")},
    Tools:    []core.LLMTool{orderTool},
    MaxSteps: 5,
})

resp.Text        // "ออเดอร์ TH-1042 กำลังจัดส่งกับ Kerry ถึงวันนี้ 18:00 น."
resp.Steps       // 2 — เรียก tool หนึ่งเทิร์น ตอบอีกหนึ่งเทิร์น
resp.ToolCalls   // audit trail ว่าลูปทำอะไรไปบ้าง
```

ลำดับที่เกิดขึ้นจริง:

```
1. ส่ง prompt + คำอธิบาย tool ไปหา model
2. model ตอบกลับมาว่า "ขอเรียก get_order_status ด้วย {"order_id":"TH-1042"}"
3. core ตรวจ Approve → รัน handler → ได้ผลลัพธ์
4. ส่งผลลัพธ์กลับไปให้ model
5. model ตอบเป็นข้อความ → จบ
```

### `MaxSteps` ไม่ใส่ = ไม่วนลูป

```go
resp, _ := core.LLM(ctx).Generate(core.LLMRequest{
    Messages: msgs,
    Tools:    []core.LLMTool{orderTool},
    // ไม่มี MaxSteps
})

resp.FinishReason   // core.LLMFinishToolUse
resp.ToolCalls      // สิ่งที่ model อยากเรียก — ยังไม่ได้รัน
```

จงใจให้เป็นแบบนี้: **ลูปที่ไม่มีเพดานคือลูปที่คิดเงินได้ไม่จบ** model ที่เรียก tool ที่พังซ้ำ ๆ จะทำแบบนั้นจนกว่าจะมีอะไรมาหยุด การแอบวนให้เงียบ ๆ เมื่อ caller ลืมใส่เพดาน คือวิธีที่ทำให้ไม่มีใครรู้ว่าเกิดอะไรขึ้น

เช็คตอนจบเสมอ:

```go
if resp.FinishReason == core.LLMFinishToolUse {
    // หมด step แต่ model ยังจะเรียกต่อ — resp.Text ไม่ใช่คำตอบสุดท้าย
    return ctx.NewError(nil, emsgs.AgentDidNotFinish)
}
```

---

## Approve — เหตุผลที่ tool use ต้องอยู่ใน core

```go
resp, err := core.LLM(ctx).Generate(core.LLMRequest{
    Messages: msgs,
    Tools:    []core.LLMTool{orderTool, refundTool},
    MaxSteps: 5,
    Approve: func(c context.Context, call core.LLMToolCall) core.IError {
        ctx.Log().Info("tool requested", "name", call.Name, "args", string(call.Input))

        if call.Name == "refund_order" {
            return core.New(403, "NEEDS_HUMAN", "การคืนเงินต้องให้เจ้าหน้าที่อนุมัติ")
        }
        return nil
    },
})
```

| | |
|---|---|
| คืน `nil` | อนุญาต — handler ทำงาน |
| คืน error | **ปฏิเสธ** — handler ไม่ถูกเรียกเลย |
| generation | **ไม่ล้ม** — ข้อความ error ถูกส่งกลับให้ model แทน |
| audit | call ที่ถูกปฏิเสธยังอยู่ใน `resp.ToolCalls` |

ที่ต้องส่งข้อความกลับให้ model แทนที่จะล้มทั้ง generation เพราะ **model ที่รู้ว่าทำไมโดนปฏิเสธจะอธิบายกับผู้ใช้ได้ ส่วน model ที่ไม่รู้อะไรเลยจะเรียกซ้ำจนหมด step**

ทดสอบกับ Gemini จริงแล้ว — model ขอเรียก `delete_everything`, gate ปฏิเสธ, handler ไม่ทำงาน, แล้ว model ตอบว่า:

> *"I'm sorry, I cannot fulfill this request. Destructive actions need an operator's approval."*

### นโยบายที่ใช้ได้จริง

```go
Approve: func(c context.Context, call core.LLMToolCall) core.IError {
    // อ่านได้เสรี
    if readOnly[call.Name] {
        return nil
    }

    // เขียนได้เฉพาะที่ผู้ใช้มีสิทธิ์
    user := ctx.GetUser()
    if user == nil || user.Segment != "staff" {
        return core.New(403, "NEEDS_STAFF", "การกระทำนี้ต้องเป็นเจ้าหน้าที่")
    }

    // อ่าน argument เพื่อตัดสินใจได้ ไม่ใช่อนุญาตทั้ง tool
    var p struct{ Amount float64 `json:"amount"` }
    _ = json.Unmarshal(call.Input, &p)
    if p.Amount > 10000 {
        return core.New(403, "NEEDS_APPROVAL", "ยอดเกิน 10,000 ต้องอนุมัติก่อน")
    }
    return nil
},
```

---

## ความปลอดภัย

### ข้อความ error ถูกส่งไปให้ provider

ทั้งผลลัพธ์และข้อความ error ของ tool ถูกส่งกลับไปเป็น tool result — นั่นแปลว่ามันออกไปนอกกระบวนการ

```go
// ❌
return "", core.New(500, "DB_ERROR", "connect postgres://user:pass@10.0.0.5 failed")

// ✅
return "", core.New(500, "DB_ERROR", "ไม่สามารถเข้าถึงข้อมูลออเดอร์ได้")
```

### tool ที่ panic ไม่ทำให้ process ตาย

core ครอบ recover ไว้แล้วส่งเป็น error ให้ model แทน — tool คือโค้ดที่ถูกเรียกจากการตัดสินใจของ model จึงเป็น path ที่มีโอกาส "ไม่เคยรันมาก่อน" สูงที่สุดในระบบ

### tool ที่ไม่รู้จัก

model ที่ขอเรียก tool ที่ไม่มี จะได้ข้อความบอกชื่อกลับไป ไม่ใช่ความเงียบ — มันจะได้เปลี่ยนทางแทนที่จะเดาต่อ

---

## tool หลายตัว

```go
tools, err := llm.NewToolSet().
    Add(llm.Tool("get_order", "ดูข้อมูลออเดอร์จากรหัส เรียกเมื่อ...", h.getOrder)).
    Add(llm.Tool("list_couriers", "รายชื่อขนส่งที่ใช้ได้ เรียกเมื่อ...", h.listCouriers)).
    Add(llm.Tool("estimate_shipping", "ประเมินค่าส่ง เรียกเมื่อ...", h.estimate)).
    Build()
if err != nil {
    return err       // type ที่อธิบายเป็น schema ไม่ได้ = บั๊กตั้งแต่ startup
}
```

`Add` รับค่าสองตัวจาก `llm.Tool` ได้ตรง ๆ เพราะ Go ยอมให้ส่งผลลัพธ์หลายค่าเป็น argument ชุดเดียว — จึงไม่ต้องเช็ค error ทีละบรรทัด

**สร้างครั้งเดียวตอน startup แล้วเก็บไว้** — ไม่ต้องสร้างใหม่ทุก request:

```go
type Service struct {
    tools []core.LLMTool
}

func NewService() (*Service, core.IError) {
    tools, err := llm.NewToolSet().Add(...).Build()
    if err != nil {
        return nil, err
    }
    return &Service{tools: tools}, nil
}
```

### ชื่อซ้ำถูกปฏิเสธ

provider จับคู่ผลลัพธ์กลับด้วยชื่อ — tool สองตัวชื่อเดียวกันแปลว่าฟังก์ชันผิดตัวถูกเรียก ซึ่งแย่กว่าการโดนปฏิเสธมาก จึง error ตั้งแต่ตอน validate request

---

## tool ที่ provider รันเอง

บาง tool ไม่ได้อยู่ในโค้ดเรา — provider รันให้ที่ฝั่งเขาเอง: Google Search grounding, การไปอ่าน URL, sandbox รัน Python เราไม่ต้องเขียน `Execute` เพราะ**ไม่มีอะไรให้รันที่ฝั่งนี้**

```go
import "github.com/pskclub/mine-core/v2/llm/goai"

resp, err := core.LLM(ctx).Generate(core.LLMRequest{
    Messages: []core.LLMMessage{core.LLMUser("รุ่นล่าสุดของ Go คืออะไร")},
    Tools:    []core.LLMTool{goai.GoogleSearch()},
})

resp.Text                    // คำตอบที่ ground ด้วยผลค้นหา
for _, s := range resp.Sources {
    fmt.Println(s.Title, s.URL)
}
```

| constructor | ทำอะไร | ต้องการ |
|---|---|---|
| `goai.GoogleSearch(opts...)` | ค้น Google แล้ว ground คำตอบ | Gemini 2.0+ |
| `goai.GoogleSearchWebOnly()` | เหมือนกันแต่เฉพาะผลเว็บ | Gemini 2.0+ |
| `goai.GoogleSearchSince(start, end)` | จำกัดช่วงเวลา (RFC3339) | Gemini 2.0+ |
| `goai.URLContext()` | ให้ model ไปดึง URL ใน prompt มาอ่านเอง | Gemini 2.0+ |
| `goai.CodeExecution()` | เขียน Python แล้วรันใน sandbox ของ Google | Gemini 2.0+ |

เป็น constructor ไม่ใช่ตารางสตริงที่ต้องพิมพ์เอง เพราะนั่นคือความต่างระหว่าง**พิมพ์ผิดแล้ว compile ไม่ผ่าน** กับ **request ที่ provider รับ เมิน แล้วคิดเงิน** — คำตอบที่ไม่เคย ground อ่านแล้วเหมือนคำตอบที่ ground ทุกประการ

รวมหลายตัวใน request เดียวได้ และใช้ `ToolSet` ประกอบได้ตามปกติ:

```go
tools, err := llm.NewToolSet().
    AddTool(goai.GoogleSearch(), goai.URLContext()).   // provider รันเอง
    Build()
```

::: warning ข้อจำกัด (ทดสอบกับ Gemini ของจริงแล้ว)
- **ผสม built-in tool กับ tool ของเราเองใน request เดียวไม่ได้** Gemini ปฏิเสธเอง:
  - `gemini-2.5*` → `Built-in tools ({google_search}) and Function Calling cannot be combined in the same request`
  - `gemini-3*` → ต้องเปิด `tool_config.include_server_side_tool_invocations` ซึ่ง goai v0.9.4 ยังไม่มีทางส่ง

  แยกเป็นสองรอบ — รอบแรก ground เอาข้อเท็จจริง รอบสองค่อยให้เรียก tool ของเรา — เป็นทางที่ใช้ได้วันนี้ (driver ส่งทั้งสองแบบออกไปได้ ตัวที่ปฏิเสธคือ provider จึงได้ `LLM_REQUEST_REJECTED` 400 พร้อมข้อความข้างบน ไม่ใช่ 500 และไม่ควร retry)
- `resp.Sources` ใช้ได้ทั้ง `Generate` และ `Stream` — ฝั่ง stream citation จะครบเมื่อ **stream จบแล้ว** เพราะ Gemini ส่ง source มาเป็น chunk ที่ไม่มีข้อความ (จึงไม่โผล่เป็น delta) อ่านจาก `s.Response().Sources` หลังลูปจบ:

  ```go
  s, err := core.LLM(ctx).Stream(req)
  defer s.Close()
  for s.Next() {
      fmt.Print(s.Text())
  }
  if err := s.Err(); err != nil { return err }

  for _, src := range s.Response().Sources {   // ครบตอนนี้
      render(src.Title, src.URL)
  }
  ```

  เรียก `Response()` กลางสตรีมได้ แต่ `Sources` จะยังว่าง — ยังไม่รู้จนกว่าจะจบ
- **การแสดง source ให้ผู้ใช้เห็นเป็นเงื่อนไขการใช้บริการ grounding ของ Google** ไม่ใช่ของแถม
- `URLContext()` ให้ model ไปดึง URL ที่อยู่ใน prompt เอง — ระวังกับ prompt ที่มี URL จากผู้ใช้
:::

`ProviderType` เป็นชื่อของ provider นั้นตรง ๆ (`"google.google_search"`) core ไม่ตีความ — driver ที่ไม่รู้จักจะปฏิเสธ ไม่ใช่เมินเงียบ ๆ และ tool แบบนี้ **ห้ามมี `Execute`**:

```
LLM_INVALID_REQUEST: llm: tool "google_search" is provider-defined
(google.google_search) and cannot have an Execute — the provider runs it,
so this function would never be called
```

เพราะคนที่เขียน `Execute` ไว้จะเชื่อว่าโค้ดตัวเองเป็นด่านคุม ทั้งที่ provider รันอยู่นอกมือ

---

## อ่าน audit trail

core เขียน log ให้เองหนึ่งบรรทัดต่อหนึ่ง call — ไม่ต้องทำอะไรเพิ่ม:

```
DEBUG llm tool ran get_order_status     tool=get_order_status outcome=ran
WARN  llm tool denied refund_order      tool=refund_order outcome=denied
```

และอ่านจาก response ได้ด้วย:

```go
for _, call := range resp.ToolCalls {
    fmt.Println(call.Name, call.Outcome)   // ran · denied · failed · unknown_tool
    fmt.Println(string(call.Input))        // model ขออะไร
    fmt.Println(call.Output)               // model ได้อะไรกลับไป
}
```

`resp.ToolCalls` เก็บ **ทุก** call ตามลำดับ รวมทั้งที่ถูกปฏิเสธ พร้อม `Outcome` ว่าเกิดอะไรขึ้นจริง — ไม่ใช่แค่ step สุดท้าย ซึ่งเป็นสิ่งที่ SDK ข้างล่างให้มาโดยธรรมชาติ

`Output` คือข้อความที่ **ถูกส่งกลับไปให้ model** จริง ๆ — ผลลัพธ์ของ tool, ข้อความบอกเหตุผลตอนถูกปฏิเสธ, หรือ error ที่เกิดขึ้น ตัวนี้แหละที่อธิบายว่าทำไมคำตอบสุดท้ายออกมาแบบนั้น

`denied` กับ `failed` ขึ้นที่ระดับ `WARN` จึงเห็นได้โดยไม่ต้องเปิด debug ส่วน `input` / `output` จะลง log ก็ต่อเมื่อเปิด [`AI_LOG_PROMPT` / `AI_LOG_COMPLETION`](./ai.md#log) — ขาเข้าและขาออกแยกคีย์กัน เพราะผลลัพธ์ของ tool มักเป็นแถวจากฐานข้อมูล ไม่ใช่คำพูดของผู้ใช้

---

ต่อไป: [Agent Loops](./ai-agent.md) · [ทดสอบ tool โดยไม่มี provider](./ai-testing.md#tool)
