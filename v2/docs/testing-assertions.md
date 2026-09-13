# Testing — Assertions

`require.Error` เปล่าๆ บอกอะไรไม่ได้เลยกับ error ของ framework สิ่งที่สำคัญคือ
**status ที่ client ได้รับ** กับ **code ที่ client เอาไปแตกเงื่อนไข** — ไม่ใช่ข้อความ
ซึ่งเป็นภาษามนุษย์และเปลี่ยนได้ตลอด

`coretest` จึงมี assertion เฉพาะสำหรับ `core.IError`

## ชุดคำสั่ง

```go
coretest.RequireNoError(t, err)                     // fail พร้อมพิมพ์ status/code
coretest.RequireStatus(t, err, http.StatusNotFound) // ตรวจ HTTP status
coretest.RequireCode(t, err, "USER_NOT_FOUND")      // ตรวจ machine code
coretest.RequireIs(t, err, errmsgs.NotFound)        // เทียบ sentinel ทะลุ wrap
coretest.Fields(t, err)                             // map[string]FieldError
coretest.FieldCodes(t, err)                         // map[field]code
```

`RequireStatus` และ `RequireCode` คืน error กลับมาด้วย จึง chain ต่อได้

## `RequireNoError` ทำไมรับ `IError` ไม่ใช่ `error`

นี่คือกับดักคลาสสิกของ Go:

```go
var err *core.Error = nil
var e error = err
e != nil            // true! เพราะ interface เก็บ type ไว้ด้วย
```

`require.NoError(t, err)` ที่รับ `error` จึงพลาดได้ ถ้าโค้ดคืน `*core.Error` ที่เป็น
nil ออกมาเป็น `error` — เทสจะ fail ทั้งที่ไม่มีอะไรผิด หรือแย่กว่านั้นคือ assertion
ตรงข้ามผ่านทั้งที่ควรพัง

`RequireNoError` รับ `core.IError` ทำให้เขียนโค้ดที่ผิดแบบนั้นไม่ได้ตั้งแต่ตอน
compile และเมื่อ fail มันพิมพ์ status, code, message ออกมาให้ ไม่ใช่แค่ที่อยู่
หน่วยความจำ:

```
unexpected error: status=500 code=DATABASE_ERROR message=repository
```

## `RequireIs` กับ sentinel

`errmsgs` sentinel เทียบกันด้วย **code** จึง match ได้แม้ถูกห่อหลายชั้น:

```go
err := core.Wrap(errmsgs.NotFound, "loading user")

coretest.RequireIs(t, err, errmsgs.NotFound)   // ผ่าน
```

ใช้ตัวนี้แทนการเทียบ code เป็น string เมื่อมี sentinel อยู่แล้ว — ถ้าวันหนึ่ง code
ของ sentinel เปลี่ยน เทสจะยังถูกต้อง

## validation error

`Fields` คืนรายละเอียดรายฟิลด์ โดย key เป็น field path:

```go
fields := coretest.Fields(t, err)

assert.Equal(t, "REQUIRED", fields["email"].Code)
assert.Equal(t, "body", fields["email"].In)
assert.Contains(t, fields["full_name"].Message, "between 2 and 100")
```

`FieldError` มีสี่ค่า:

| field | คือ |
|---|---|
| `Code` | machine-readable เช่น `REQUIRED`, `INVALID_EMAIL` |
| `Message` | ข้อความสำหรับมนุษย์ |
| `In` | มาจากไหน — `path` / `query` / `header` / `body` |
| `Data` | ค่าประกอบของ rule เช่น `{"min":2,"max":100}` |

### เทียบทั้งชุด

```go
assert.Equal(t, map[string]string{
    "email":     "INVALID_EMAIL",
    "full_name": "REQUIRED",
}, coretest.FieldCodes(t, err))
```

แนะนำแบบนี้มากกว่า assert ทีละตัว เพราะจับได้ทั้งสองทิศ: field ที่**ควรพัง**แต่ไม่พัง
และ field ที่**ไม่ควรพัง**แต่ดันพัง การ assert ทีละตัวจับได้แค่อย่างแรก

### field ที่มี index

```go
fields := coretest.Fields(t, err)

assert.Equal(t, "INVALID_EMAIL", fields["users.1.email"].Code)
assert.NotContains(t, fields, "users.0.email")
```

## assert จาก wire shape

`Fields` และ `FieldCodes` **marshal error เป็น JSON ก่อน** แล้วค่อยอ่านกลับ ไม่ได้
อ่าน field ภายในของ struct

เพราะสิ่งที่สำคัญคือสิ่งที่ client ได้รับ ถ้าวันหนึ่ง `JSON()` เปลี่ยนรูปแบบ เทส
จะพัง — ซึ่งถูกแล้ว เพราะนั่นคือ breaking change ต่อทุกคนที่เรียก API

ผลข้างเคียงที่ดี: assertion บนผลจาก service (`Fields`) กับบนผลจาก HTTP
(`res.Error().Fields`) มีรูปแบบเหมือนกันเป๊ะ ย้ายเทสข้ามระดับได้โดยแทบไม่ต้องแก้

## ใช้ร่วมกับ testify

`coretest` ไม่ได้แทน testify แต่เสริมเฉพาะส่วนที่เป็น `IError`

```go
user, err := svc.Create(payload)

coretest.RequireNoError(t, err)              // ส่วนของ framework
require.NotNil(t, user)                      // testify ตามปกติ
assert.Equal(t, "a@b.co", user.Email)
```

ธรรมเนียมที่ใช้ในโมดูลนี้: `require` สำหรับเงื่อนไขที่ถ้าไม่ผ่านแล้วเทสไปต่อไม่ได้
(จะ stop ทันที) และ `assert` สำหรับตรวจค่า (เก็บ failure ไว้แล้วไปต่อ) — เทสหนึ่งตัว
จึงรายงานปัญหาได้หลายอย่างในรอบเดียว

## เขียน assertion ของตัวเอง

ถ้ามีรูปแบบที่ใช้ซ้ำใน service ให้ทำเป็น helper และอย่าลืม `t.Helper()`:

```go
func requireConflict(t *testing.T, err core.IError, wantCode string) {
    t.Helper()                                  // failure ชี้ไปที่ผู้เรียก

    coretest.RequireStatus(t, err, http.StatusConflict)
    coretest.RequireCode(t, err, wantCode)
}
```

ไม่มี `t.Helper()` เวลา fail มันจะชี้มาที่บรรทัดใน helper ซึ่งไม่ช่วยอะไรเลย
