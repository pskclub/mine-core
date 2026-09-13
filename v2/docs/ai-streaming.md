# AI · Streaming

```go
s, err := core.LLM(ctx).Stream(core.LLMRequest{
    System:   systemPrompt,
    Messages: []core.LLMMessage{core.LLMUser(question)},
})
if err != nil {
    return err
}
defer s.Close()

for s.Next() {
    fmt.Print(s.Text())      // delta ไม่ใช่ข้อความสะสม
}
if err := s.Err(); err != nil {
    return err
}

usage := s.Response().Usage  // ครบเมื่อ stream จบแล้วเท่านั้น
```

`Response()` เรียกกลางสตรีมได้ ไม่ค้าง แต่ค่าที่รู้ได้ตอนจบ (`Usage`, `Sources`) จะยังไม่ครบ — [grounding ที่ stream](./ai-tools.md#tool-ที่-provider-รันเอง) ก็อ่าน `Response().Sources` หลังลูปจบเช่นกัน

## ทำไมเป็น iterator ไม่ใช่ channel

goai (engine ข้างใน) คืน `<-chan StreamChunk` ซึ่งย้ายภาระไปให้ caller: **ทิ้ง channel กลางคันแล้ว producer จะค้างหรือ leak**

iterator ที่มี `Close()` ย้ายภาระนั้นกลับมาเป็นของ driver และทำให้ terminal error มีที่อยู่ที่เดียว — เป็นทรงเดียวกับ `bufio.Scanner` และ `sql.Rows` ที่ทุกคนใน Go รู้จักอยู่แล้ว

| | channel | iterator + Close |
|---|---|---|
| เลิกอ่านกลางคัน | producer ค้าง / goroutine leak | `Close()` ปล่อยให้ |
| error ที่ทำให้จบ | ต้องส่งเป็น chunk พิเศษ | `Err()` ที่เดียว |
| ผลลัพธ์รวม | ต้องสะสมเอง | `Response()` |

## สัญญาของ `Close`

- **เรียกซ้ำได้** — `defer s.Close()` คู่กับ `s.Close()` ตรง ๆ เป็นเรื่องปกติ
- **เรียกก่อนอ่านจบได้** — กรณีผู้ใช้ปิดหน้าจอไปกลางคัน
- **ปลดล็อกได้จริง** — cancel context ข้างใน ทำให้ reader ที่ค้างอยู่หลุด ไม่ทิ้ง goroutine

เคสสุดท้ายคือเคสที่รั่วง่ายสุดในโค้ดจริง และมีเทสต์คุมไว้ทั้งใน unit suite และ integration suite (`StreamCloseBeforeDrain`)

## `Response()` จะครบเมื่อไหร่

```go
for s.Next() { ... }
final := s.Response()

final.Text          // ข้อความเต็ม = ผลรวมของทุก delta
final.FinishReason  // ครบเมื่อจบแล้ว
final.Usage         // มากับ chunk สุดท้าย
final.Steps
```

`Text` ที่สะสมได้จาก delta **ต้องเท่ากับ** `Response().Text` เสมอ — ไม่งั้น UI ที่ render delta จะแสดงคนละอย่างกับโค้ดที่อ่านผลลัพธ์ (เป็นข้อหนึ่งใน conformance suite)

---

## ส่งออกเป็น SSE จาก HTTP handler

```go
func StreamAnswer(c core.IHTTPContext) error {
    s, err := core.LLM(c).Stream(core.LLMRequest{
        System:   systemPrompt,
        Messages: []core.LLMMessage{core.LLMUser(c.QueryParam("q"))},
    })
    if err != nil {
        return err
    }
    defer s.Close()

    w := c.Response()
    // Response() คือ http.ResponseWriter ธรรมดาของ echo v5 — Flush ต้อง assert เอง
    flush := func() {}
    if f, ok := w.(http.Flusher); ok {
        flush = f.Flush
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("X-Accel-Buffering", "no")   // กัน nginx บัฟเฟอร์จนไม่เห็นอะไรเลย
    w.WriteHeader(http.StatusOK)

    for s.Next() {
        if _, werr := fmt.Fprintf(w, "data: %s\n\n", s.Text()); werr != nil {
            return nil     // client หลุดไปแล้ว — defer s.Close() เก็บกวาดให้
        }
        flush()
    }
    if err := s.Err(); err != nil {
        return err
    }

    fmt.Fprint(w, "event: done\ndata: {}\n\n")
    flush()

    c.Log().Info("answered",
        "tokens", s.Response().Usage.OutputTokens,
        "finish", s.Response().FinishReason)
    return nil
}
```

⚠️ delta อาจมีขึ้นบรรทัดใหม่อยู่ข้างใน ซึ่งจะทำให้ SSE frame พัง — ถ้าเนื้อหาอาจมี `\n` ให้ห่อเป็น JSON:

```go
line, _ := json.Marshal(map[string]string{"t": s.Text()})
fmt.Fprintf(w, "data: %s\n\n", line)
```

## เขียนลง WebSocket

```go
for s.Next() {
    if err := conn.WriteJSON(map[string]string{"delta": s.Text()}); err != nil {
        return nil       // อีกฝั่งปิดไปแล้ว
    }
}
```

## นับ token ระหว่างทาง

usage มากับ chunk สุดท้าย ระหว่างสตรีมจึงยังไม่มี — ถ้าต้องการตัวเลขคร่าว ๆ ระหว่างทางให้นับ delta ที่ส่งออกไปเอง แล้วใช้ค่าจริงจาก `Response()` ตอนจบทับ

---

## ปิด stream ตอนผู้ใช้ยกเลิก

ไม่ต้องทำอะไรพิเศษ — `core.LLM(ctx)` ผูกกับ deadline ของ request อยู่แล้ว พอ client ตัดการเชื่อมต่อ echo จะ cancel context และ stream จะจบเอง ส่วน `defer s.Close()` เก็บกวาดที่เหลือ

ถ้าอยากให้ generation รอดต่อแม้ request จบ (เช่นเขียนผลลง DB ให้เสร็จ) ต้องตั้งใจแยก context เอง:

```go
bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
defer cancel()

s, err := core.LLM(ctx).WithContext(bg).Stream(req)
```

`WithoutCancel` เก็บค่าที่ context พกไว้ (รวมถึง App) แต่ตัดสายการยกเลิกออก

---

ต่อไป: [Typed Values](./ai-typed.md) · [Tool Use](./ai-tools.md)
