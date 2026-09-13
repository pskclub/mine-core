# AI · Typed Values (Structured Output)

`core.LLM(ctx)` คืนข้อความ ซึ่งถูกสำหรับคำตอบแชท และผิดสำหรับเกือบทุกอย่างที่ backend ทำกับ model — สกัดข้อมูล จัดหมวด ให้คะแนน งานพวกนี้อยากได้ **ค่า Go**

```go
import "github.com/pskclub/mine-core/v2/llm"

type Invoice struct {
    Vendor string    `json:"vendor" jsonschema:"description=บริษัทที่ออกใบเสร็จ"`
    Total  float64   `json:"total"`
    Status string    `json:"status" jsonschema:"enum=draft|sent|paid"`
    Due    time.Time `json:"due"`
    Note   string    `json:"note,omitempty"`
}

inv, err := llm.New[Invoice](ctx).
    System("อ่านใบเสร็จนี้ จำนวนเงินเป็นบาท ถ้าไม่มีวันครบกำหนดให้ใช้วันที่ออกบิล").
    Extract(ocrText)
```

type parameter คือ API ทั้งหมด — schema สร้างจาก `Invoice`, ส่งไปบังคับ model, แล้ว unmarshal กลับมาเป็น `Invoice`

เป็น `New[T](ctx)` ไม่ใช่เมธอดบน `ILLM` เพราะ **Go ไม่ให้เมธอดมี type parameter** — และเป็นทรงเดียวกับ `repository.New[M](ctx)` ที่ codebase นี้ใช้อยู่แล้ว

---

## schema tag

| tag | ผล |
|---|---|
| `json:"vendor"` | ชื่อฟิลด์ใน schema |
| `json:"note,omitempty"` | ไม่บังคับ (ไม่อยู่ใน `required`) |
| `json:"-"` | ไม่ส่งเข้า schema เลย |
| `jsonschema:"description=..."` | คำอธิบายฟิลด์ |
| `jsonschema:"enum=a\|b\|c"` | จำกัดค่า |
| `jsonschema:"optional"` | ไม่บังคับ |
| `jsonschema:"required"` | บังคับ แม้เป็น pointer หรือ omitempty |

**`description` คุ้มค่าที่สุดต่อความพยายามที่ใส่ไป** — มันคือสิ่งเดียวที่บอก model ว่าฟิลด์นี้หมายถึงอะไร ฟิลด์ชื่อ `amount` ที่ไม่มีคำอธิบาย จะได้ยอดก่อน VAT หรือหลัง VAT ก็ได้

**ฟิลด์ที่เป็น pointer ไม่บังคับโดยอัตโนมัติ** — เป็นวิธีที่ Go พูดว่า "ค่านี้อาจไม่มี" อยู่แล้ว

> `description` ใส่ comma ไม่ได้ (tag ใช้ comma คั่น) — คำอธิบายยาว ๆ ควรอยู่ใน system prompt อยู่แล้ว

## ชนิดที่รองรับ

```go
type Customer struct {
    Name    string            `json:"name"`
    Age     int64               `json:"age"`      // → integer
    Score   float64           `json:"score"`    // → number
    Active  bool              `json:"active"`   // → boolean
    Since   time.Time         `json:"since"`    // → string / format: date-time
    Tags    []string          `json:"tags"`     // → array of string
    Address Address           `json:"address"`  // → nested object
    Orders  []Order           `json:"orders"`   // → array of object
    Meta    map[string]string `json:"meta"`     // → object ที่ค่าเป็น string
}
```

embedded struct ที่ไม่มี json tag จะถูก **แบนราบ** ให้เหมือนที่ `encoding/json` ทำ — ไม่งั้น model จะตอบมาเป็นชั้นซ้อนที่ unmarshal ไม่ลง

### ที่ไม่รองรับ (และ error ตั้งแต่ก่อนยิง request)

```go
type Node struct {
    Children []Node `json:"children"`   // ❌ recursive
}
```

```
LLM_INVALID_SCHEMA: llm: Node refers to itself (at children[]) —
no provider supports a recursive schema
```

ไม่มี provider เจ้าไหนรองรับ schema แบบวนกลับ — ทางเลือกอื่นคือ stack overflow หรือพ่น `$ref` ให้ provider ปฏิเสธอีกชั้น ซึ่ง error จะอยู่ไกลจากสาเหตุ

`func`, `chan`, `interface{}` ก็ error เช่นกัน และ error จะบอกชื่อฟิลด์ให้ด้วย

### root ต้องเป็น struct

```go
llm.New[[]Invoice](ctx)   // ❌ model ตอบเป็น object เสมอ
llm.New[string](ctx)      // ❌
```

ห่อไว้ในฟิลด์แทน:

```go
type Invoices struct {
    Items []Invoice `json:"items"`
}
```

## สร้าง schema ดูเฉย ๆ

```go
s, err := llm.SchemaOf[Invoice]()
raw, _ := json.MarshalIndent(s.Schema, "", "  ")
fmt.Println(string(raw))
```

มีประโยชน์ตอน debug ว่าทำไม model ตอบไม่ตรง — บ่อยครั้งคำตอบอยู่ใน schema ที่เราส่งไปเอง

---

## เลือก model ต่อการเรียก

งานสกัดค่าเล็ก ๆ ไม่จำเป็นต้องใช้ model ตัวเดียวกับที่ตอบผู้ใช้:

```go
cat, err := llm.New[Category](ctx).
    Model("gemini-flash-lite-latest").      // model อื่นของ provider เดิม
    System(rubric).
    Extract(text)
```

ทางนี้ยังผ่าน handle ของ App — token เข้า `llm.tokens.*` มี log ต่อ generation และ `Result().Model` บอกตัวที่ถูกเรียกจริง ต่างจากการสร้าง client เองไว้ข้าง ๆ ซึ่งไม่มีอะไรนับให้

ถ้าต้องข้าม provider (คนละเจ้าไปเลย) ใช้ handle ตรง ๆ:

```go
lite, _ := goai.NewConfig(goai.Config{Provider: "openai", Model: "gpt-5-mini", APIKey: key})

got, err := llm.New[Category](ctx).Using(lite).Extract(text)
```

`Using` ผูก handle เข้ากับ ctx ของ request ให้เอง — generation จึงหยุดพร้อม request เหมือน `core.LLM(ctx)`

---

## จัดหมวด

```go
type Ticket struct {
    Category string `json:"category" jsonschema:"enum=billing|technical|sales|other"`
    Urgent   bool   `json:"urgent"`
    Reason   string `json:"reason" jsonschema:"description=เหตุผลสั้น ๆ ไม่เกิน 1 ประโยค"`
}

t, err := llm.New[Ticket](ctx).
    System("จัดหมวดอีเมลลูกค้า ถ้าไม่แน่ใจให้ใช้ other").
    Extract(email.Body)
if err != nil {
    return err
}

switch t.Category {
case "billing":
    return s.routeToFinance(ctx, email)
case "technical":
    return s.routeToSupport(ctx, email, t.Urgent)
}
```

`enum` บังคับที่ระดับ schema **ไม่ใช่หวังว่า prompt จะได้ผล** — model ตอบค่านอกลิสต์ไม่ได้ ซึ่งแปลว่า `switch` ข้างบนไม่ต้องมี `default` ที่คอยรับค่าประหลาด

---

## ตรวจค่าหลัง parse

JSON Schema บอกได้แค่ *รูปร่าง* กฎที่มันบอกไม่ได้ — ผลรวมต้องเท่ากับผลบวกของรายการ, วันที่ต้องไม่เกินวันนี้, รหัสต้องมีอยู่จริงใน DB

```go
inv, err := llm.New[Invoice](ctx).
    System(rules).
    Validate(func(v Invoice) core.IError {
        if v.Total < 0 {
            return core.New(422, "NEGATIVE_TOTAL", "ยอดรวมติดลบ")
        }
        if v.Due.Before(time.Now().AddDate(-5, 0, 0)) {
            return core.New(422, "DUE_TOO_OLD", "วันครบกำหนดเก่าเกินไป")
        }
        return nil
    }).
    Extract(ocrText)
```

### type ที่ validate ตัวเองอยู่แล้ว

ถ้า `T` implement `core.IValidateContext` (เช่นเป็น HTTP request payload อยู่ด้วย) มันจะถูกเรียกให้อัตโนมัติ — **กฎอยู่ที่เดียว ไม่ต้องเขียนซ้ำ**

```go
type Reviewed struct {
    Score int64    `json:"score"`
    Note  string `json:"note"`
}

func (r *Reviewed) Valid(ctx core.IContext) core.IError {
    return valid.New(ctx).
        Int("score", &r.Score).Required().Min(0).Max(10).
        Str("note", &r.Note).Max(500).
        Valid()
}

// Valid() ถูกเรียกให้เอง — ได้ IError ที่มี Fields เหมือน binding ปกติ
r, err := llm.New[Reviewed](ctx).Extract(text)
```

ทำงานได้เมื่อ `ctx` เป็น `IContext` (ซึ่งเป็นกรณีปกติใน handler และ job)

---

## ต้นทุนต่อชิ้น

```go
res, err := llm.New[Invoice](ctx).Ask(ocrText).Result()
if err != nil {
    return err
}

res.Value              // Invoice
res.Usage.InputTokens  // ต้นทุนต่อเอกสาร
res.Usage.Total()
res.JSON               // สิ่งที่ model ตอบมาดิบ ๆ
res.Provider, res.Model
```

`res.JSON` คือสิ่งที่ควรแนบไปกับ incident ตอนค่าออกมาผิด — โดยไม่มีมัน คุณจะเหลือแค่ค่าที่ผิดโดยไม่รู้ว่า model ตอบอะไรมาจริง ๆ

---

## ใช้ base ซ้ำ

**ทุกเมธอดของ builder คืนสำเนา** ค่าที่ตั้งไว้ครึ่งทางจึงเก็บไว้ใช้ซ้ำได้อย่างปลอดภัย — และ system prompt ยาว ๆ ควรถูก cache เพราะงาน extraction มักรันคำสั่งเดิมกับ input หลายพันชิ้น

```go
base := llm.New[Invoice](ctx).
    System(longExtractionRules).   // ยาว 3,000 token
    CacheSystem().                 // จ่ายเต็มครั้งเดียว
    Reasoning(core.LLMReasoningLow).
    MaxTokens(1024)

for _, doc := range docs {
    inv, err := base.Extract(doc.Text)   // base ไม่ถูกแก้
    if err != nil {
        ctx.Log().Warn("extract failed", "doc", doc.ID, "code", err.GetCode())
        continue
    }
    results = append(results, inv)
}
```

⚠️ system prompt ต้องนิ่งทุกไบต์ — timestamp หรือ request id ที่แทรกอยู่ข้างในทำให้ cache ไม่ติดทั้งก้อน ดู [Prompt caching](./ai-providers.md#prompt-caching)

---

## เมธอดทั้งหมด

| เมธอด | ทำอะไร |
|---|---|
| `System(s)` | system prompt |
| `Ask(text)` | ต่อข้อความ user |
| `Messages(msgs...)` | แทนที่บทสนทนาทั้งหมด |
| `MaxTokens(n)` | เพดานคำตอบ |
| `Reasoning(level)` | ระดับการคิด |
| `Model(id)` | เปลี่ยนไปใช้ model อื่นของ provider เดิม |
| `Using(handle)` | รันกับ `core.ILLM` ตัวที่ระบุ (คนละ provider) |
| `CacheSystem()` | ขอ cache system prompt |
| `ProviderOptions(m)` | ของเฉพาะ provider |
| `SchemaName(s)` | ชื่อ schema (บาง provider เอาไปใส่ใน error) |
| `Validate(fn)` | ตรวจค่าหลัง parse |
| `Lenient()` | ยอมรับคำตอบที่ห่อด้วยข้อความ |
| `Extract(text)` | = `Ask(text).Generate()` |
| `Generate()` | รันแล้วคืน `T` |
| `Result()` | รันแล้วคืน `Result[T]` (มี usage) |

### บทสนทนาที่ต้องได้ค่า

```go
plan, err := llm.New[Plan](ctx).
    System(planningRules).
    Messages(
        core.LLMUser("ช่วยวางแผนทริปเชียงใหม่ 3 วัน"),
        core.LLMAssistant(previousDraft),
        core.LLMUser("ตัดวันที่ 2 ออก แล้วจัดใหม่"),
    ).
    Generate()
```

### model ที่ JSON mode ไม่แข็งแรง

```go
got, err := llm.New[Sentiment](ctx).Lenient().Extract(review)
```

`Lenient()` ยอมรับคำตอบที่ห่อด้วยข้อความหรือ ```` ```json ```` — **ปิดไว้เป็นค่าเริ่มต้น** เพราะ model ที่ไม่ทำตาม schema มักแปลว่า prompt ต้องแก้ ถ้าซ่อมให้เงียบ ๆ ก็จะไม่มีใครไปแก้ เปิดเมื่อใช้ model เล็ก ๆ ในเครื่องที่ทางเลือกคือไม่ได้ใช้เลย

---

## เมื่อ model ตอบไม่เป็น JSON

```
llm: google answered with something that is not the requested JSON: I think it's positive, actually!
```

error พก **คำตอบดิบ** มาด้วยเสมอ — เพราะลำพัง `invalid character 'I'` ทำอะไรต่อไม่ได้ และคำตอบก็หายไปแล้ว

สาเหตุที่เจอบ่อย: model ไม่รองรับ structured output (เช็ค `Capabilities().StructuredOutput`), schema ใหญ่เกิน `MaxTokens` จน JSON โดนตัด, หรือ system prompt สั่งให้ "ตอบเป็นข้อความสั้น ๆ" ซึ่งขัดกับ schema

---

## สกัดจากรูป

```go
r, err := llm.New[Receipt](ctx).
    System("อ่านใบเสร็จ").
    Messages(core.LLMUser("อ่านนี่").With(core.LLMImage(scan, "image/jpeg"))).
    Generate()
```

ดู [Attachments](./ai-attachments.md#รวมกับ-typed-extraction)

---

ต่อไป: [Tool Use](./ai-tools.md) · [Testing](./ai-testing.md#test-typed-extraction)
