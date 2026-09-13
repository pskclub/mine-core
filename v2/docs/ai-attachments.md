# AI · Attachments (รูปภาพ · เอกสาร)

```go
resp, err := core.LLM(ctx).Generate(core.LLMRequest{
    System: "อ่านใบเสร็จ ตอบเฉพาะยอดรวม",
    Messages: []core.LLMMessage{
        core.LLMUser("ยอดรวมเท่าไหร่").With(core.LLMImage(scan, "image/jpeg")),
    },
})
```

`With()` แนบของเข้ากับข้อความ — **ข้อความยังสำคัญอยู่** เพราะ model ที่ได้รูปมาเปล่า ๆ จะบรรยายรูปนั้น ซึ่งแทบไม่เคยเป็นสิ่งที่คนเรียกต้องการ

## ตัวช่วย

```go
core.LLMImage(data []byte, mediaType string)            // รูปจาก bytes
core.LLMImageURL(url string)                            // ให้ provider ไปดึงเอง
core.LLMFile(data []byte, mediaType, filename string)   // PDF / เอกสาร
```

หรือประกอบเองเมื่อต้องการฟิลด์เพิ่ม:

```go
core.LLMPart{
    Type:      core.LLMPartImage,
    Data:      thumb,
    MediaType: "image/webp",
    Detail:    "low",     // low | high | auto
}
```

| ฟิลด์ | หมายเหตุ |
|---|---|
| `Data` / `URL` | **ใส่ได้อย่างใดอย่างหนึ่ง** ไม่ใช่ทั้งคู่ |
| `MediaType` | **จำเป็นเมื่อใช้ `Data`** — เดาจาก bytes ไม่ได้ |
| `Filename` | บาง provider ใช้เป็นใบ้ว่าไฟล์คืออะไร — `invoice-2026-08.pdf` มีค่ากว่า `upload` |
| `Detail` | `low` ใช้ token น้อยกว่ามาก พอสำหรับดูเลย์เอาต์ แต่ไม่พอสำหรับอ่านตัวเล็ก ๆ |

---

## แนบหลายอย่าง

```go
core.LLMUser("เทียบสองใบนี้ ต่างกันตรงไหน").With(
    core.LLMImage(before, "image/png"),
    core.LLMImage(after, "image/png"),
)
```

หรือหลายเทิร์น:

```go
[]core.LLMMessage{
    core.LLMUser("นี่คือใบเสร็จเดือนที่แล้ว").With(core.LLMImage(july, "image/jpeg")),
    core.LLMAssistant("รับทราบครับ ยอดรวม 980 บาท"),
    core.LLMUser("แล้วเดือนนี้ล่ะ").With(core.LLMImage(august, "image/jpeg")),
}
```

---

## รวมกับ typed extraction

คู่ที่ใช้งานจริงได้ทันที — รูปเข้า ค่า Go ออก

```go
type Receipt struct {
    Vendor string    `json:"vendor" jsonschema:"description=ชื่อร้านที่พิมพ์อยู่ด้านบน"`
    Total  float64   `json:"total" jsonschema:"description=ยอดรวมเป็นตัวเลข ไม่ต้องมีตัวคั่นหลักพัน"`
    Date   time.Time `json:"date"`
    Items  []Item    `json:"items"`
}

r, err := llm.New[Receipt](ctx).
    System("อ่านใบเสร็จแล้วสกัดข้อมูลตาม schema").
    Messages(core.LLMUser("อ่านใบเสร็จนี้").
        With(core.LLMImage(scan, "image/jpeg"))).
    Generate()
```

ทดสอบกับ Gemini จริงแล้ว — ภาพใบเสร็จที่มีคำว่า `ACME` และ `1,250.50` ได้ `{Vendor:ACME Total:1250.5}` กลับมา

### แบตช์เอกสาร

```go
base := llm.New[Receipt](ctx).
    System(extractionRules).
    CacheSystem()

for _, f := range files {
    img, err := core.Storage(ctx).GetBytes(f.Key)
    if err != nil {
        return err
    }

    r, err := base.
        Messages(core.LLMUser("อ่านใบเสร็จนี้").With(core.LLMImage(img, f.MediaType))).
        Generate()
    if err != nil {
        ctx.Log().Warn("extract failed", "key", f.Key, "code", err.GetCode())
        continue
    }
    results = append(results, r)
}
```

---

## เอกสาร (PDF)

```go
core.LLMUser("สรุปสัญญาฉบับนี้").With(
    core.LLMFile(pdfBytes, "application/pdf", "contract-2026.pdf"),
)
```

⚠️ รองรับไม่เท่ากันมากกว่ารูปภาพเยอะ — บางเจ้าอ่าน PDF ได้เอง บางเจ้าปฏิเสธ ถ้า provider ปฏิเสธจะได้ `LLM_REQUEST_REJECTED` (4xx) ซึ่งบอกได้ว่าไม่ต้อง retry

ทางที่ชัวร์กว่าถ้าต้องรองรับหลาย provider: แปลง PDF เป็นรูปทีละหน้าฝั่งเราแล้วส่งเป็น `LLMImage`

---

## ขนาดและต้นทุน

### เพดานขนาด

```go
core.LLMMaxAttachmentBytes   // 15 MiB — รวมทุก attachment ใน request เดียว
```

เกินแล้วได้ `LLM_ATTACHMENT_TOO_LARGE` (413) **ตั้งแต่ก่อนยิง request**

เป็น guard ของเราเอง ไม่ใช่เพดานของ provider — ของจริงต่างกัน (Anthropic 32MB ต่อ request, OpenAI/Google ราว 20MB) และนับ **หลัง** base64 ซึ่งทำให้บวมขึ้นอีกหนึ่งในสาม การล้มที่นี่บอกได้ว่าปัญหาคืออะไร ส่วนการล้มที่ provider คือ error เรื่องขนาดของ request ที่มี prompt ปนอยู่ด้วย หลังรอไปหลายวินาทีและโดนคิดเงินแล้วหนึ่งรอบ

```go
if len(scan) > core.LLMMaxAttachmentBytes {
    scan = resize(scan)        // ย่อก่อนส่ง
}
```

### รูปแพงกว่าที่คิด

```
in=279 out=9     // ใบเสร็จ 260×120 px
in=17  out=1     // คำถามข้อความล้วน
```

รูปเล็ก ๆ ยังกิน input token เป็นร้อย รูปความละเอียดสูงกินได้ถึงหลักพัน — และ **`prompt_chars` ใน log ไม่สะท้อนเรื่องนี้เลย** เพราะมันนับแต่ตัวอักษร

log จึงแยกออกมาให้:

```
DEBUG llm call  provider=google model=gemini-2.5-flash prompt_chars=42
                attachments=1 attachment_bytes=342 input_tokens=279
```

**ลดต้นทุน:** ย่อรูปก่อนส่ง (model อ่าน 1024px ได้พอ ๆ กับ 4096px สำหรับงานส่วนใหญ่) และใช้ `Detail: "low"` เมื่อไม่ต้องอ่านตัวหนังสือเล็ก ๆ

---

## ตรวจสอบก่อนส่ง

```go
if !core.LLM(ctx).Capabilities().Vision {
    return ctx.NewError(nil, errmsgs.AIVisionUnavailable)
}
```

`Vision` บอกว่า **driver ส่ง attachment ให้ provider นี้ได้** ไม่ได้บอกว่า model ที่ตั้งไว้อ่านรูปเป็น — สองเรื่องนี้ต่างกัน และมีแค่เรื่องแรกที่ driver รู้คำตอบ เพราะการรองรับ vision ขึ้นกับ **model ไม่ใช่ provider** (`gpt-4o` เห็น, `o1-mini` ไม่เห็น)

สิ่งที่มันรับประกันคือสิ่งที่สำคัญ: **`Vision` เป็น true แปลว่ารูปจะถูกส่งหรือถูกรายงาน ไม่มีทางหายเงียบ ๆ**

model ที่อ่านรูปไม่เป็นจะถูกปฏิเสธจาก provider เอง (`LLM_REQUEST_REJECTED`)

---

## ทำไม attachment ที่ผิดรูปถึง error

```go
_, err := core.LLM(ctx).Generate(core.LLMRequest{
    Messages: []core.LLMMessage{
        core.LLMUser("นี่อะไร").With(core.LLMPart{Type: core.LLMPartImage, Data: img}),
        //                                          ไม่มี MediaType ↑
    },
})
// LLM_INVALID_REQUEST: llm: messages[0].Parts[0] needs a MediaType
// ("image/jpeg", "application/pdf") — it cannot be guessed from the bytes
```

เพราะชั้นล่างจะ **ข้าม part ที่ parse ไม่ได้แบบเงียบ ๆ** — ข้อความออกไปโดยมีรูโหว่ตรงที่ควรจะเป็นรูป แล้ว model ก็ตอบจากข้อความอย่างเดียวเหมือนไม่มีอะไรขาด คำตอบที่ได้ดูสมเหตุสมผลทุกประการ ไม่มีอะไรบอกว่ารูปไม่เคยไปถึง

error บอกด้วยว่าเป็น part ไหน (`messages[0].Parts[0]`) เพราะ request ที่มี 5 รูปแล้วบอกแค่ว่า "รูปหนึ่งผิด" ยังต้องมานั่งไล่หาอยู่ดี

---

## ทดสอบ

```go
m := core.NewMemoryLLM(`{"vendor":"ACME","total":1250.5}`)
app, _ := core.NewApp(env, core.WithLLM(m))
ctx := app.NewContext(context.Background(), core.ModeTest)

r, err := llm.New[Receipt](ctx).
    Messages(core.LLMUser("อ่านนี่").With(core.LLMImage(fixture, "image/png"))).
    Generate()
require.NoError(t, err)

// ยืนยันว่ารูปถูกแนบไปจริง
parts := core.LLMCalls(m)[0].Messages[0].Parts
require.Len(t, parts, 1)
assert.Equal(t, "image/png", parts[0].MediaType)
assert.Equal(t, fixture, parts[0].Data)
```

memory model บันทึก part ไว้เหมือน field อื่น ๆ — ไม่ต้องมีรูปจริง ไม่ต้องมี key

---

ต่อไป: [Typed Values](./ai-typed.md) · [Providers & Tuning](./ai-providers.md)
