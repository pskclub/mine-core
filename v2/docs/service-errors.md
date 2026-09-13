# Service Errors

[Error handling](./error-handling.md) อธิบายว่า `core.IError` ทำงานยังไง หน้านี้
อธิบายว่า error **ของ service คุณเอง** ควรถูกประกาศไว้ที่ไหนและอย่างไร — เพราะ
`code` คือสิ่งที่ client เอาไปเขียน `if` และมันจึงเป็น API พอๆ กับ JSON schema

## หนึ่ง package หนึ่งไฟล์ต่อ module

```
emsgs/
    emsgs.go          package doc + กติกา
    auth.emsgs.go     error ที่ auth ยก
    user.emsgs.go     validation code ของ user
    note.emsgs.go     error ที่ note ยก
```

ประกาศครั้งเดียวแล้วใช้ซ้ำ — `code` คือสิ่งที่ client แตกกิ่ง การพิมพ์ string
ซ้ำที่ call site จึงทำให้ typo กลายเป็นการพังแบบเงียบๆ ที่ไม่มีอะไรจับได้

::: code-group

```go [emsgs/auth.emsgs.go]
package emsgs

import (
    "net/http"

    core "github.com/pskclub/mine-core/v2"
)

// Errors raised by the auth module: registering, signing in, and every request
// that carries a token.
var (
    // InvalidCredentials คือคำตอบเดียวของ sign-in ที่ล้มเหลว
    //
    // ตั้งใจให้ "ไม่มี account นี้" กับ "รหัสผ่านผิด" เป็น error ตัวเดียวกัน:
    // การแยกสองอย่างนี้ทำให้ใครก็ตามไล่ได้ว่าอีเมลไหนสมัครไว้แล้ว และคนเรียก
    // เอาความต่างนั้นไปทำอะไรที่มีประโยชน์ไม่ได้อยู่ดี
    InvalidCredentials = core.New(
        http.StatusUnauthorized,
        "INVALID_CREDENTIALS",
        "email or password is incorrect",
    )

    // TokenExpired แยกจาก 401 ทั่วไปเพราะคนเรียก "ทำอะไรต่อได้": token เป็นของจริง
    // แค่หมดอายุ ทางแก้คือ sign in ใหม่ ไม่ใช่ไปไล่หาบั๊ก — และมันเป็น token
    // ของเจ้าตัวเอง จึงบอกได้ไม่เป็นการรั่วข้อมูล
    TokenExpired = core.New(
        http.StatusUnauthorized,
        "TOKEN_EXPIRED",
        "the session has expired, please sign in again",
    )
)
```

```go [modules/auth/service/auth.service.go]
// call site: ไม่มี string ซ้ำ ไม่มี status ที่ต้องจำ
return emsgs.InvalidCredentials      // 401 INVALID_CREDENTIALS
```

:::

> error ของ framework (`NotFound`, `BadRequest`, `Unauthorized`, `DBError` …) มาจาก
> [`errmsgs`](./error-handling.md) ของ
> mine-core — ใช้ตัวนั้นตรงๆ `emsgs` มีไว้สำหรับสิ่งที่ **service นี้** นิยามเองเท่านั้น

## กติกา

**status มาจาก `net/http` ไม่ใช่ตัวเลขดิบ** — `http.StatusUnauthorized` บอกว่า
401 แปลว่าอะไรโดยที่คนอ่านไม่ต้องนึกเอง

**error เป็น sentinel** เทียบด้วย `errors.Is` ซึ่ง match ด้วย code ทะลุการห่อทุกชั้น
builder ทุกตัว (`WithMessage`, `WithFields`, …) คืน copy ใหม่ คนเรียกจึงเติมข้อมูล
ได้โดยไม่แตะค่าที่ใช้ร่วมกัน:

```go
if errors.Is(err, emsgs.TokenExpired) { ... }

return emsgs.NoteLimitReached.WithFields(map[string]any{"limit": maxNotesPerUser})
```

**code ตั้งชื่อจากสิ่งที่เกิดขึ้น ไม่ใช่จากที่ที่มันเกิด** — `NOTE_LIMIT_REACHED`
ไม่ใช่ `NOTE_SERVICE_ERROR_3` client อ่านมันเพื่อจะตัดสินใจ ไม่ได้อ่านเพื่อจะรู้ว่า
โค้ดของเราจัดไฟล์ยังไง

## Validation error vs business rule

เส้นแบ่งที่ควรรักษาไว้:

| | ตรวจที่ | ตอบด้วย |
|---|---|---|
| ตอบได้จาก payload อย่างเดียว | `Valid()` ของ request | 400 `INVALID_PARAMS` + `fields` |
| ต้องดูแถวใน database | service | error ของตัวเอง (409, 403, …) |

```go
// handler/note.request.go — ตอบได้จาก payload
func (r *CreateRequest) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Str("title", r.Title).Required().Length(1, maxTitleLength)
    v.Str("body", r.Body).Required().Length(1, maxBodyLength)

    return v.Error()
}
```

```go
// service/note.service.go — ต้องนับแถวก่อนถึงจะรู้
if count >= maxNotesPerUser {
    return nil, emsgs.NoteLimitReached      // 409: ไม่มีอะไรผิดกับสิ่งที่ส่งมา
}
```

`Valid` ถามว่า "จำนวน note ของ user คนนี้เกินหรือยัง" ไม่ได้ เพราะมันขึ้นกับแถวที่
เปลี่ยนได้ระหว่างการเช็คกับการ insert ส่วน 409 ไม่ใช่ 400 ก็สื่อความเดียวกัน:
สิ่งที่ส่งมาไม่มีอะไรผิด

## Validation code ที่เขียนเอง

`v.Must` ยกโค้ดที่ไม่มีข้อความของตัวเอง — จึง register ข้อความไว้ใน `init` ของไฟล์
เดียวกัน:

```go
package emsgs

import "github.com/pskclub/mine-core/v2/valid"

const CodeDuplicateInRequest = "DUPLICATE_IN_REQUEST"

func init() {
    valid.SetMessage(CodeDuplicateInRequest, "The {field} field is repeated in this request")
}
```

```go
v.Must("email", emsgs.CodeDuplicateInRequest, !seen[email])
```

การเก็บ code ไว้คู่กับข้อความของมันมีผลอย่างหนึ่งที่สำคัญ: การจะใช้ code ต้อง
import `emsgs` ซึ่งแปลว่า `init` ของมันทำงานแน่นอน — **ยก code โดยที่ข้อความยังไม่มี
จึงเป็นไปไม่ได้** ถ้าปล่อยให้ข้อความไปอยู่ใน request ตัวแรกที่บังเอิญต้องใช้ อีก
สามเดือนจะไม่มีใครหาเจอ

ดู [Validation](./validation.md) สำหรับ rule ทั้งชุด

## อย่าให้ layer ล่างรู้จัก HTTP

store คืน error ของ repository, service ห่อด้วย `ctx.NewError` ที่จุดที่ context
ยังรู้จัก user และ request อยู่:

```go
note, err := store.Note(s.ctx).Scopes(store.OwnedBy(ownerID)).FindOne("id = ?", id)
if err != nil {
    return nil, s.ctx.NewError(err, err)   // คง status เดิม, รายงาน 5xx เข้า Sentry
}
```

`ctx.NewError(err, err)` คงสถานะเดิมของ error จาก repository ไว้ (404 ยังเป็น 404)
พร้อมรายงานตัวที่เป็น 5xx เข้า Sentry ที่จุดนั้น handler จึงแค่ `return err`
โดยไม่ต้องแปลอะไรเลย

⚠️ **404 ไม่ใช่ 403 สำหรับแถวของคนอื่น** — 403 คือการยืนยันว่า id นั้นมีอยู่จริง
ซึ่งเป็นข้อเท็จจริงที่คนเรียกไม่มีสิทธิ์รู้ ทำ ownership เป็น `WHERE` แล้ว "ไม่ใช่
ของคุณ" กับ "ไม่มี" จะตอบเหมือนกันโดยอัตโนมัติ

## อ่านต่อ

- [Error handling](./error-handling.md) — `IError`, `Wrap`, stack, dev message
- [Validation](./validation.md) — `valid` package ทั้งชุด
- [Logging practices](./logging-practices.md) — error ที่ return แล้ว **ไม่ต้อง** log ซ้ำ
