# AI · Embeddings

เปลี่ยนข้อความเป็นเวกเตอร์ สำหรับ semantic search และ RAG

```go
embedder, err := goai.NewEmbedder(env)
if err != nil { log.Fatal(err) }

app, _ := core.NewApp(env,
    core.WithLLM(model),
    core.WithEmbedder(embedder),
)
```

```sh
APP_AI_EMBED_MODEL=gemini-embedding-001

# ตัวเลือก — ไม่ตั้งจะใช้ค่าเดียวกับฝั่ง generation
APP_AI_EMBED_PROVIDER=google
APP_AI_EMBED_API_KEY=...
APP_AI_EMBED_BASE_URL=...

# ความยาวเวกเตอร์ที่จะขอทุกครั้ง — ต้องตรงกับคอลัมน์ที่ประกาศไว้
APP_AI_EMBED_DIMENSIONS=768
```

## ทำไมเป็น capability แยก ไม่ใช่เมธอดบน ILLM

เพราะ **Anthropic ไม่มี embedding API เลย**

service ที่ generate ด้วย Claude ต้องใช้เจ้าอื่น embed — ซึ่งแปลว่าคนละ model id คนละ key คนละ base URL การยัดเป็นเมธอดบน `ILLM` จะบังคับให้ทั้งสองอย่างมาจาก provider เดียวกัน ซึ่งใช้ไม่ได้จริง

```sh
# ปกติของ service ที่ใช้ Claude
APP_AI_PROVIDER=anthropic
APP_AI_MODEL=claude-opus-5
APP_AI_API_KEY=sk-ant-...

APP_AI_EMBED_PROVIDER=openai            # คนละเจ้า
APP_AI_EMBED_MODEL=text-embedding-3-small
APP_AI_EMBED_API_KEY=sk-...             # คนละ key
```

ถ้าตั้ง `AI_EMBED_PROVIDER=anthropic` driver จะบอกตั้งแต่ boot:

```
EMBED_INVALID_CONFIG: embed: anthropic has no embedding API —
set AI_EMBED_PROVIDER to openai, google, ollama or compat
```

ดีกว่าปล่อยให้ไปเจอ 404 จาก URL ที่ไม่มีใครตั้งใจเรียก

---

## ใช้งาน

```go
// index — ส่งเป็นชุด
vecs, err := core.Embedder(ctx).Embed(texts...)
if err != nil {
    return err
}
for i, doc := range docs {
    doc.Vector = vecs[i]      // vecs[i] คู่กับ texts[i] เสมอ
}

// search
q, err := core.Embedder(ctx).EmbedOne(query)
```

**ส่งเป็นชุดถูกกว่าและเร็วกว่ายิงทีละครั้งมาก** — provider คิดเงินตาม token ไม่ใช่ตาม request และสำหรับข้อความสั้น ๆ เวลาส่วนใหญ่หมดไปกับ round trip

driver รับประกันว่า **จำนวนเวกเตอร์เท่ากับจำนวนข้อความเสมอ** — ถ้า provider คืนมาไม่ครบจะได้ `EMBED_PROVIDER_ERROR` ไม่ใช่การจับคู่ผิดเงียบ ๆ ซึ่งไม่มีอะไรข้างล่างตรวจจับได้

### ข้อความว่างถูกปฏิเสธ

```go
_, err := core.Embedder(ctx).Embed("ok", "")
// EMBED_INVALID_REQUEST: embed: texts[1] is empty
```

provider ส่วนใหญ่ปฏิเสธอยู่แล้ว ส่วนเจ้าที่รับจะคืนเวกเตอร์ที่ *ใกล้กับทุกอย่างเท่า ๆ กัน* — ซึ่งแย่กว่า เพราะ index ยังทำงานได้แต่เลิกมีประโยชน์

---

## ตัวเลือกต่อครั้ง — `EmbedWith`

`Embed` ใช้ค่า default ของ model ซึ่งพอสำหรับ prototype แต่ index จริงมักต้องการสองอย่างที่ `Embed` บอกไม่ได้ — **ความยาวเวกเตอร์** กับ **งานที่เวกเตอร์นี้จะถูกใช้**

```go
// index — ฝั่ง corpus
docs, err := core.Embedder(ctx).EmbedWith(core.EmbedRequest{
    Texts:      chunks,
    Dimensions: 768,                  // คอลัมน์เป็น vector(768)
    Task:       core.EmbedDocument,
})

// search — ฝั่ง query: model เดียวกัน ความยาวเดียวกัน แต่คนละ Task
q, err := core.Embedder(ctx).EmbedWith(core.EmbedRequest{
    Texts:      []string{question},
    Dimensions: 768,
    Task:       core.EmbedQuery,
})
```

`Embed(texts...)` คือ `EmbedWith` ที่ request ว่างเปล่า — ทั้งสองเดินทางเดียวกัน driver จึงไม่มีทางรองรับตัวเลือกในทางหนึ่งแล้วเมินในอีกทางหนึ่ง

### `Dimensions` — ทำไมสำคัญกว่าที่คิด

`gemini-embedding-001` คืน **3072 มิติถ้าไม่บอกอะไร** คอลัมน์ที่ประกาศเป็น `vector(768)` จึงปฏิเสธทุกแถว — และถ้าเผลอเขียนลงคอลัมน์ที่ความยาวบังเอิญตรง ก็จะได้เวกเตอร์ที่เทียบกับ corpus เดิมไม่ได้ตลอดไป โดยไม่มี error ใด ๆ

ตัวเลขนี้เป็นของ **index** ไม่ใช่ของ call site จึงตั้งเป็น config ได้ครั้งเดียว:

```sh
APP_AI_EMBED_DIMENSIONS=768
```

ตั้งแล้วทุก call ขอความยาวนี้เอง และ `Dimensions()` ตอบได้ **ตั้งแต่ก่อน call แรก** ซึ่งเป็นตอนที่ migration ต้องใช้ตัวเลข ส่วน `EmbedRequest.Dimensions` ยังใช้ override รายครั้งได้ (เช่น ตอนทดลองเทียบความยาวก่อนย้าย) โดยไม่กระทบค่าที่ `Dimensions()` รายงาน

### `Task` — เวกเตอร์เดียวกันแต่คนละทิศ

model ที่รองรับ task type จะฝัง**ประโยคเดิม**เป็น**เวกเตอร์คนละตัว** ขึ้นกับว่าเวกเตอร์นั้นจะถูกใช้ทำอะไร corpus ที่ index แบบ document แล้วค้นด้วย query ที่สร้างแบบ document จึงได้ผลแย่ลง *อย่างเงียบ ๆ* เพราะทุกคะแนนที่ออกมายังดูเป็นคะแนนความใกล้ที่ปกติดี

| `core.EmbedTask` | Gemini | ใช้เมื่อ |
|---|---|---|
| `EmbedDocument` | `RETRIEVAL_DOCUMENT` | ข้อความที่จะถูก index |
| `EmbedQuery` | `RETRIEVAL_QUERY` | คำค้น — คู่กับอันบนเสมอ |
| `EmbedSimilarity` | `SEMANTIC_SIMILARITY` | เทียบสองข้อความแบบสมมาตร (หา near-duplicate) |
| `EmbedClassification` | `CLASSIFICATION` | ป้อนเข้า classifier |
| `EmbedClustering` | `CLUSTERING` | จัดกลุ่ม |

### provider ไหนรองรับอะไร

| provider | `Dimensions` | `Task` |
|---|---|---|
| google / gemini | ✅ `outputDimensionality` | ✅ `taskType` |
| openai / compat | ✅ `dimensions` | ❌ |
| ollama | ❌ | ❌ |

**สิ่งที่ driver ไม่รองรับจะถูก "ปฏิเสธ" ไม่ใช่ "ตัดทิ้ง"**:

```go
_, err := core.Embedder(ctx).EmbedWith(core.EmbedRequest{
    Texts: []string{"x"}, Task: core.EmbedQuery,
})
// EMBED_UNSUPPORTED: embed: openai does not support task types
```

เหตุผลเดียวกับฝั่ง generation — เวกเตอร์ที่ผิดไม่ใช่ผลลัพธ์ที่ *ด้อยลง* แต่เป็นผลลัพธ์ที่ *ผิด* ซึ่งจัดอันดับออกมาดูสมเหตุสมผลและไม่มีวัน error ตรวจด้วย `errors.Is(err, core.ErrEmbedUnsupported)` ได้

### ProviderOptions — ทางออกสุดท้าย

คีย์ที่ core ยังไม่มี field ให้ ส่งตรงได้ และจะถูก merge ทับค่าที่ map ไว้ (คนเรียกชนะ):

```go
core.Embedder(ctx).EmbedWith(core.EmbedRequest{
    Texts:      chunks,
    Dimensions: 768,
    ProviderOptions: map[string]any{
        "google": map[string]any{"taskType": "FACT_VERIFICATION"},
    },
})
```

แลกมาด้วยการไม่มี compile-time check — มี field ให้ใช้เมื่อไหร่ให้ใช้ field

### ที่ยังส่งไม่ได้: `title` ของ Gemini

Gemini รับ `title` ต่อเอกสารได้เมื่อ `taskType=RETRIEVAL_DOCUMENT` แต่ **ส่งผ่าน core ไม่ได้ และส่งผ่าน `ProviderOptions` ก็ไม่ได้** เพราะ engine ข้างใน (goai v0.9.4) อ่านเฉพาะ `taskType` กับ `outputDimensionality` เท่านั้น คีย์อื่นไม่ถูกใส่ลง request

ไม่ได้เพิ่ม field ไว้ใน `EmbedRequest` เพราะจะเป็น field ที่ตั้งแล้วไม่เกิดอะไรขึ้น — แย่กว่าไม่มี

ผลกระทบน้อย: Google แนะนำให้ใช้ `taskType` แทน `title` อยู่แล้ว ถ้าชื่อเอกสารมีความหมายกับการค้นจริง ๆ ให้ผนวกเข้าไปในข้อความที่ embed ตรง ๆ

```go
core.EmbedRequest{
    Texts: []string{doc.Title + "\n\n" + chunk},   // ชื่อเรื่องกลายเป็นส่วนหนึ่งของสิ่งที่ถูก index
    Task:  core.EmbedDocument,
}
```

---

## วัดความใกล้

```go
score := core.CosineSimilarity(a, b)
// 1 = ชี้ทางเดียวกัน · 0 = ไม่เกี่ยว · -1 = ตรงข้าม
```

อยู่ใน core เพราะทุกคนที่ใช้ embedder ต้องใช้ และเป็นเลขคณิตชิ้นเดียวที่พลาดง่าย — ลืม normalise แล้วคะแนนจะกลายเป็นการเทียบ *ขนาด* ของเวกเตอร์ ซึ่งจะดันเอกสารยาวขึ้นมาก่อนเอกสารที่ตรงคำถาม

```go
core.CosineSimilarity([]float32{1, 2, 3}, []float32{2, 4, 6})  // 1.0 — ทิศเดียวกัน
core.CosineSimilarity([]float32{1, 2}, []float32{1, 2, 3})     // 0 — คนละ model
core.CosineSimilarity([]float32{0, 0}, []float32{1, 1})        // 0 — ไม่มีทิศ
```

เวกเตอร์คนละความยาวคืน 0 เพราะมันมาจากคนละ model — คะแนนใด ๆ ก็ไม่มีความหมาย

## จัดอันดับ

```go
type scored struct {
    Doc   Document
    Score float64
}

ranked := make([]scored, 0, len(docs))
for _, d := range docs {
    ranked = append(ranked, scored{d, core.CosineSimilarity(q, d.Vector)})
}
slices.SortFunc(ranked, func(a, b scored) int {
    return cmp.Compare(b.Score, a.Score)      // มากไปน้อย
})
top := ranked[:min(5, len(ranked))]
```

ใช้ได้จริงกับเอกสารหลักพัน ถ้ามากกว่านั้นควรใช้ vector index ของฐานข้อมูล

---

## RAG ครบวงจร

```go
func (s Service) Answer(ctx core.IContext, question string) (string, core.IError) {
    q, err := core.Embedder(ctx).EmbedOne(question)
    if err != nil {
        return "", err
    }

    passages, err := s.searchTopK(ctx, q, 5)
    if err != nil {
        return "", err
    }
    if len(passages) == 0 {
        return "", ctx.NewError(nil, emsgs.NoRelevantDocuments)
    }

    var b strings.Builder
    for i, p := range passages {
        fmt.Fprintf(&b, "[%d] %s\n\n", i+1, p.Text)
    }

    resp, err := core.LLM(ctx).Generate(core.LLMRequest{
        System: `ตอบจากเอกสารที่ให้เท่านั้น อ้างอิงหมายเลข [n] ทุกครั้งที่อ้างข้อมูล
ถ้าเอกสารไม่มีคำตอบ ให้บอกว่าไม่พบข้อมูล ห้ามเดา`,
        CacheSystem: true,
        Messages: []core.LLMMessage{
            core.LLMUser("เอกสาร:\n" + b.String() + "\nคำถาม: " + question),
        },
    })
    if err != nil {
        return "", err
    }
    return resp.Text, nil
}
```

### RAG ที่ได้ค่าเป็นโครงสร้าง

รวมกับ [typed generation](./ai-typed.md) เมื่ออยากได้ทั้งคำตอบและแหล่งอ้างอิงแบบ machine-readable:

```go
type Answer struct {
    Text    string `json:"text"`
    Sources []int  `json:"sources" jsonschema:"description=หมายเลขเอกสารที่ใช้ตอบ"`
    Found   bool   `json:"found" jsonschema:"description=false เมื่อเอกสารไม่มีคำตอบ"`
}

a, err := llm.New[Answer](ctx).
    System(ragRules).
    CacheSystem().
    Extract(context)

if !a.Found {
    return s.escalate(ctx, question)
}
```

---

## ขนาดเวกเตอร์

```go
core.Embedder(ctx).Dimensions()   // เช่น 3072 สำหรับ gemini-embedding-001
```

ถ้าตั้ง `AI_EMBED_DIMENSIONS` ไว้จะได้ตัวเลขนั้นทันที **ตั้งแต่ก่อน call แรก** — ถ้าไม่ตั้ง จะรู้ได้ **หลังเรียกครั้งแรก** เพราะ provider บอกความยาวไว้ในเอกสาร ไม่ได้บอกใน API ส่วนคนที่ต้องประกาศคอลัมน์ต้องการตัวเลข ไม่ใช่เอกสาร

```sql
ALTER TABLE documents ADD COLUMN vector vector(3072);
```

⚠️ **เปลี่ยน embedding model = ต้อง index ใหม่ทั้งหมด** เวกเตอร์เก่ากับใหม่เทียบกันไม่ได้ (และ `CosineSimilarity` จะคืน 0 ถ้าความยาวต่างกัน ซึ่งอย่างน้อยก็ทำให้เห็นทันทีแทนที่จะได้อันดับมั่ว ๆ)

---

## ที่ core ยังไม่ทำ — vector storage

`IEmbedder` คืนเวกเตอร์ให้ service เก็บเอง — pgvector, Mongo Atlas vector search หรือคำนวณในหน่วยความจำ

ยังไม่ตัดสินใจว่าจะยกเข้ามาแบบไหน เพราะสองทางเลือกกระทบ `repository` กับ `mongorepo` คนละแบบ และการเลือกผิดตอนที่ยังไม่มี service จริงใช้ จะแพงกว่าการรอ

---

ต่อไป: [Providers & Tuning](./ai-providers.md) · [ทดสอบ embeddings](./ai-testing.md#embeddings)
