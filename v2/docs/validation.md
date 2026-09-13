# Validation

A typed, fluent builder. Build a `valid.Validator` from the context, chain
type-specific rules per field, and return `v.Error()`.

> Not just for HTTP: **job parameters validate the same way** — a job's run
> context is a `core.IContext`, so everything on this page (including the DB
> rules) works inside a job. See [jobs-defining.md](./jobs-defining.md#parameters). Machine codes are separate
from human messages, and DB rules never swallow errors.

## Basic usage

```go
import "github.com/pskclub/mine-core/v2/valid"

func (r *CreateUserRequest) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Str("email", r.Email).Required().Email().Unique("users", "email")
    v.Str("username", r.Username).Required().Length(3, 20)
    v.Int("age", r.Age).Required().Between(18, 150)
    v.Arr("tags", r.Tags).Max(5)
    return v.Error()
}
```

Fields are pointers (from JSON binding). Empty/nil values are **skipped by every
rule except `Required`**, so optional fields validate only when present.

### ไม่ได้ส่ง, ส่งค่าว่าง, และ required

`""` ถูกอ่านว่า **"ไม่ได้ส่ง"** ทุกที่ในแพ็กเกจนี้ — `Required` จึงตกด้วยโค้ด
`REQUIRED` และ rule อื่นทุกตัวข้ามไป เป็นคอนเวนชันเดียวกับ v1 และเป็นเหตุผลที่
client ซึ่งส่ง field มาครบทุกตัวโดยปล่อยว่างอันที่ไม่ได้กรอกไม่พังทั้ง API

ส่วน field ที่ **optional แต่ค่าว่างคือ bug ของ client** (ส่ง `change_at: ""` มา
แล้วถูกเก็บเป็น null เงียบ ๆ) ใช้ `NotBlank` ซึ่งมองเฉพาะค่าที่ส่งมาจริง:

```go
v.Str("change_at", r.ChangeAt).NotBlank().Date()
```

| ค่าที่ส่ง | `Date()` | `NotBlank().Date()` | `Required().Date()` |
|---|---|---|---|
| ไม่ส่ง (nil) | ผ่าน | ผ่าน | `REQUIRED` |
| `""` | ผ่าน | `BLANK` | `REQUIRED` |
| `"   "` | `INVALID_DATE` | `BLANK` | `INVALID_DATE` |
| `31-12-2025` | `INVALID_DATE` | `INVALID_DATE` | `INVALID_DATE` |
| `2026-07-15` | ผ่าน | ผ่าน | ผ่าน |

`Required` อ่าน `""` ว่าไม่ได้ส่ง แต่ **`"   "` ผ่าน `Required`** เพราะช่องว่างก็คือค่า
— field ที่บังคับกรอกและต้องมีเนื้อจริงจึงเขียน `Required().NotBlank()` คู่กัน

## Rules

**String** (`v.Str`): `Trim`, `Lower`, `Apply(fn)`, `Required`, `NotBlank`, `Email`, `URL`, `Length(min,max)`, `Min`, `Max`,
`In(...)`, `Lowercase`, `Uppercase`, `Contains`, `NotContains`, `Prefix`, `Suffix`,
`Match(re)`, `Numeric`, `UUID`, `IP`, `Base64`, `JSON`, `Custom(code, ok)`, the
date/time rules below, plus the DB rules further down.

**Number** (`v.Int`, `v.Float`): `Required`, `Min`, `Max`, `Between`, `In(...)`,
`Positive`.

**Bool** (`v.Bool`): `Required`.

**Time** (`v.Time`, for `*time.Time`): `Required`, `Before(t)`, `After(t)`,
`Min(t)`, `Max(t)` (inclusive), `Between(start, end)`, `Past`, `Future`,
`Weekday(days...)`, `Custom(code, ok)`, `Value()`.

**Array** (`v.Arr`): `Required`, `Size(n)`, `Min(n)`, `Max(n)`.

Rules stop on the first failure **per field** (no piled-up messages).

## Normalizers: `Trim`, `Lower`, `Apply`

สามตัวนี้ไม่ตัดสินค่า แต่**เขียนทับค่า** ผ่าน pointer ที่ binding เติมไว้ — handler
ที่อ่าน payload ต่อจึงได้ค่าที่สะอาดแล้วด้วย ไม่ใช่แค่ rule ที่เห็น

```go
v.Str("email", &r.Email).Trim().Lower().Required().Email().Unique("users", "email")
v.Str("meeting_at", &r.MeetingAt).Trim().Required().DateTime()
```

วางไว้**หน้าสุดของ chain** เพราะ rule ทำงานตามลำดับที่เรียก normalizer ช่วยได้เฉพาะ
rule ที่อยู่หลังมัน

| chain | ส่ง `" 2026-07-15 10:00:00 "` | ส่ง `"   "` |
|---|---|---|
| `.DateTime()` | `INVALID_DATETIME` | `INVALID_DATETIME` |
| `.Trim().DateTime()` | ผ่าน, handler ได้ `2026-07-15 10:00:00` | ผ่าน, กลายเป็น `""` = ไม่ได้ส่ง |
| `.Trim().Required()` | ผ่าน | `REQUIRED` |

แถวสุดท้าย: `Trim` ยุบ `"   "` เป็น `""` แล้ว `Required` จับต่อได้เอง เป็นอีกทางหนึ่ง
นอกจาก `NotBlank` — ต่างกันตรงที่ `Trim` ทำให้ค่าที่เหลือกลายเป็น "ไม่ได้ส่ง" จริง ๆ
rule ที่ตามมาจึงข้ามไป ส่วน `NotBlank` ฟ้องแล้วหยุดทันที

`Apply(fn)` คือรูปทั่วไปของสองตัวแรก ไว้เสียบ normalizer ประจำบ้านโดยไม่ต้องรอ core
เพิ่ม method ให้:

```go
func toE164(s string) string { return "+66" + strings.TrimPrefix(s, "0") }

v.Str("phone", &r.Phone).Trim().Apply(toE164).Required().Match(phoneRe)
// " 0812345678 " → "+66812345678" ทั้งใน rule ที่ตามมาและใน payload
```

`fn` ต้องรับได้ทุก input ที่ client ส่งมา (ยังไม่ผ่าน rule ใด ๆ ตอนถูกเรียก) ถ้าส่ง
`fn` เป็น nil หรือ field ไม่ได้ถูกส่งมา จะไม่ทำอะไรเลย

`Lower` ต่างจาก `Lowercase`: `Lower` แก้ค่าให้, `Lowercase` เป็น rule ที่ฟ้อง 400
ใช้ `Lowercase` เมื่อ client ส่ง `"ADMIN"` มาแล้วถือเป็นความผิด ใช้ `Lower` เมื่อมันแค่
เรื่องที่ควรเก็บกวาดให้

สองข้อควรระวัง:

- **ต้องเป็น pointer ที่ชี้เข้า payload** ถ้าส่งตัวชั่วคราวอย่าง `v.Str("x", utils.Ptr(s))`
  ค่าที่ normalize จะตกอยู่กับตัวแปรลอย ๆ payload ไม่เปลี่ยน และไม่มี error เตือน
- **อย่าใช้เหมารวมทุก field** password ที่ลงท้ายด้วยช่องว่างคือรหัสที่ถูกต้อง และ
  free text บางอันต้องการ indent เดิม จึงจงใจให้เป็น opt-in รายฟิลด์ ไม่มีสวิตช์
  trim ทั้งระบบที่ชั้น binding

## Dates and times

Strict by default, permissive when you say so. Each rule takes the layouts it
accepts; with none it keeps its historical single format, and the `Any*` variants
accept every format the package knows:

```go
v.Str("dob", r.DOB).Date()                            // 2026-07-15 only
v.Str("dob", r.DOB).Date("2006-01-02", "02/01/2006")  // either of two
v.Str("dob", r.DOB).AnyDate()                         // any known date format

v.Str("opens_at", r.OpensAt).Time()        // 08:30:00 only
v.Str("opens_at", r.OpensAt).AnyTime()     // 08:30, 8:30 AM, 083000, …

v.Str("at", r.At).DateTime()               // 2026-07-15 08:30:00 only
v.Str("at", r.At).AnyDateTime()            // RFC 3339, slashed, RFC 1123, …
v.Str("at", r.At).ISO8601()                // RFC 3339 exactly

v.Str("when", r.When).AnyTemporal()        // a date, a time, or both
```

What each set contains is in `valid.DateLayouts`, `valid.TimeLayouts` and
`valid.DateTimeLayouts`. Append at init time to teach the validator a house
format:

```go
func init() { valid.DateLayouts = append(valid.DateLayouts, "02 Jan 06") }
```

Three things worth knowing:

- **ค่าต้องตรง layout เป๊ะ ๆ** ช่องว่างหน้า/หลังทำให้ตก ไม่ได้ถูกตัดทิ้ง —
  `" 2026-07-15 "` คือ `INVALID_DATE` เพราะ service ได้ string ดิบไปใช้ต่อ ถ้า
  validator ปล่อยผ่าน `time.Parse` ฝั่งนั้นจะพัง แล้ว field กลายเป็น null เงียบ ๆ
  ทั้งที่ผ่าน 400 มาแล้ว ถ้ารู้ว่า client ส่งค่ามีช่องว่างติดมา ให้ trim ตอน bind
- **Kinds don't blur.** `AnyDate` rejects a value carrying a clock,
  `AnyDateTime` rejects a bare date. A field whose precision the client picks
  wants `AnyTemporal`.
- **Fractional seconds** are always accepted after a seconds field, so
  `DateTime()` also matches `2026-07-15 08:30:00.123` — that is `time.Parse`'s
  own rule.
- **`02/01/2006` is day-first.** `03/04/2026` is 3 April. A month-first API must
  ask for it: `Date("01/02/2006")` — no auto-detecting rule can tell them apart.

### From a string to the temporal rules

`AsTime` parses the string and continues as a `*time.Time` chain, so range rules
apply to what the client actually sent:

```go
v.Str("starts_at", r.StartsAt).Required().AsTime().Future()
v.Str("dob", r.DOB).AnyDate().AsTime().Past()
```

`AsTimeIn(loc, layouts...)` reads a zone-less value in the location given —
`2026-07-15` as midnight in Bangkok rather than in UTC. A value that carries its
own offset keeps it.

A field that already failed a string rule, or that is absent, carries that state
over: no second violation for the same field, and the rules after it skip.

`Value()` exposes the parsed time for cross-field checks:

```go
start := v.Str("start", r.Start).Required().AsTime()
end := v.Str("end", r.End).Required().AsTime()
if s, e := start.Value(), end.Value(); s != nil && e != nil {
    v.Must("end", "INVALID_DATE_RANGE", e.After(*s), map[string]any{"other": "start"})
}
```

Parsing is available on its own too: `valid.ParseDate`, `valid.ParseClock`,
`valid.ParseDateTime`, and `valid.ParseTime(s, layouts...)` (no layouts = any
known format), each returning `(time.Time, bool)`.

## Adding rules to the same field on separate lines

`v.Str(...)` (and `v.Int`/`v.Bool`/`v.Arr`) return a **field builder** (a pointer).
Keep it in a variable to continue its rules on later lines — the same builder is
reused, so **stop-on-first still applies** (one violation per field):

```go
email := v.Str("email", r.Email)
email.Required()
email.Email()
email.Min(3)
// empty email → only REQUIRED (later rules skip, exactly like one chain)
```

This is the clean way to split a long chain, or to add a rule conditionally:

```go
name := v.Str("name", r.Name)
name.Required().Length(2, 50)

if isCreate {
    name.Unique("users", "name") // continues the SAME builder
}
```

> ⚠️ Don't call `v.Str("email", …)` **twice** for the same field — each call makes
> an *independent* builder (stop-on-first won't carry over, and the JSON output
> keeps only the last violation for that field). Store the builder in a variable
> instead, or use one chain.

For conditional blocks across *several* fields, prefer [`When`](#conditional-cross-field-validation).

## Nested / arrays of objects

```go
v.Each("items", len(r.Items), func(iv *valid.Validator, i int) {
    iv.Str("name", r.Items[i].Name).Required()
    iv.Int("qty", r.Items[i].Qty).Required().Min(1)
})
// failed fields are prefixed: items.0.name, items.1.qty, ...
```

## DB rules

`Unique`/`Exists` run a query. If the query itself fails (bad column, connection
lost) the error is **surfaced as HTTP 500**, not silently passed — v1 swallowed
these, letting duplicates through.

```go
v.Str("email", r.Email).Unique("users", "email")
v.Str("code",  r.Code).Exists("coupons", "code")
```

### Complex conditions (Scopes)

`Unique`/`Exists` take any number of **Scopes** — `func(*gorm.DB) *gorm.DB` — so
you can express tenant scoping, soft-delete filters, or exclude the current row:

```go
// unique email WITHIN a tenant, excluding the record being updated
v.Str("email", r.Email).Unique("users", "email",
    valid.Cond("tenant_id = ?", tenantID),
    valid.Except("id", userID),          // ignore self on update
)

// exists only among active (non-deleted) rows
v.Str("code", r.Code).Exists("coupons", "code",
    valid.Cond("deleted_at IS NULL"),
    valid.Cond("expires_at > ?", time.Now()),
)

// or an inline scope for anything
v.Str("email", r.Email).Unique("users", "email", func(db *gorm.DB) *gorm.DB {
    return db.Where("status <> ?", "banned")
})
```

Helpers: `valid.Except(column, value)` (adds `column != value`) and
`valid.Cond(query, args...)` (adds a `WHERE`). Compose as many as you need.

### Mongo

```go
v.Str("email", r.Email).MongoUnique("users", bson.M{"email": *r.Email})
v.Str("ref",   r.Ref).MongoExists("orders", bson.M{"ref": *r.Ref})
```

### Fully custom (`Check` / `CheckDB`)

For logic the built-ins don't cover, `Check` runs a predicate with the context
(DB, cache, repositories). A returned error is treated as infra failure (500);
`ok=false` records a violation with your code:

```go
v.Str("coupon", r.Coupon).Check("COUPON_INVALID", func(ctx core.IContext, code string) (bool, error) {
    n, err := repository.New[Coupon](ctx).Where("code = ? AND used = false", code).Count()
    return n > 0, err
})
```

For a check not tied to a single string value, use the validator-level
`CheckDB(field, code, fn)`:

```go
v.CheckDB("slug", "SLUG_TAKEN", func(ctx core.IContext) (bool, error) {
    n, err := repository.New[Page](ctx).Where("slug = ? AND site_id = ?", slug, siteID).Count()
    return n == 0, err
})
```

## Error shape

`v.Error()` is a `core.IError` rendering:

```json
{
  "code": "INVALID_PARAMS",
  "message": "Invalid parameters",
  "fields": {
    "email": { "code": "INVALID_EMAIL", "message": "The email field must be a valid email address" }
  }
}
```

Codes are stable and machine-readable; messages are generated from a **catalog**
of templates (see below).

## Custom messages

Every rule sets a `Code`; the human `Message` is filled in automatically from a
template. There are three ways to control it:

**1. Default catalog** (`valid/messages.go`) — templates keyed by code, with
`{field}` and rule params (`{min}`, `{max}`, `{sub}`, …) interpolated:

```go
"REQUIRED":               "The {field} field is required",
"INVALID_NUMBER_BETWEEN": "The {field} field must be between {min} and {max}",
```

So `v.Str("email", r.Email).Required()` → `"The email field is required"` with no
extra work.

**2. Override a code globally** (at init — e.g. to localise or reword). Affects
every field using that code:

```go
func init() {
    valid.SetMessage("REQUIRED", "{field} จำเป็นต้องกรอก")
    valid.SetMessage("INVALID_EMAIL", "รูปแบบอีเมลไม่ถูกต้อง")
}
```

**3. Per-rule inline** with `.Message(...)` — override the message of the rule
**immediately before it**, applied only when that rule is the one that failed.
So you can give a different message to each rule on the same field:

```go
v.Str("password", r.Password).
    Required().Message("Password is required").
    Min(8).Message("Password must be at least 8 characters")
// empty password → "Password is required"
// "abc"          → "Password must be at least 8 characters"
```

```go
v.Int("age", r.Age).
    Required().Message("อายุจำเป็นต้องกรอก").
    Between(18, 150).Message("อายุต้องอยู่ระหว่าง 18–150 ปี")
```

`.Message()` binds to the preceding rule only:
- it's a **no-op** when that rule passed or was skipped, and when the field is valid;
- it overrides the human message while keeping the machine `Code`;
- stop-on-first still means one violation per field (the first failing rule's,
  with the message you attached to it).

> For `Must(field, code, ok)` (cross-field), the message comes from the catalog
> for `code` — register it with `SetMessage`, or reuse an existing code.

> **i18n:** call `SetMessage` per locale at startup (or swap the catalog). Codes
> stay constant so clients can also translate on their side by `code`.

## Conditional & cross-field validation

For rules that depend on other fields, use `When` (conditional blocks) and `Must`
(arbitrary predicate → violation on a field).

**Conditional required** — validate a field only when another says so:

```go
func (r *NotifyRequest) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Bool("notify", r.Notify).Required()

    // notify_email is required (and must be an email) only when notify == true
    v.When(r.Notify != nil && *r.Notify, func(v *valid.Validator) {
        v.Str("notify_email", r.NotifyEmail).Required().Email()
    })
    return v.Error()
}
```

**Discriminated union** — different rules per `type`:

```go
v.Str("type", r.Type).Required().In("person", "company")
if r.Type != nil {
    switch *r.Type {
    case "person":
        v.Str("first_name", r.FirstName).Required()
        v.Str("last_name", r.LastName).Required()
    case "company":
        v.Str("company_name", r.CompanyName).Required()
        v.Str("tax_id", r.TaxID).Required().Length(13, 13)
    }
}
```

**Cross-field** — compare two fields with `Must(field, code, ok, data...)`:

```go
// end must be after start
v.Must("end_at", "INVALID_DATE_RANGE",
    r.StartAt == nil || r.EndAt == nil || r.EndAt.After(*r.StartAt),
    map[string]any{"other": "start_at"})

// password confirmation
v.Must("password_confirm", "MISMATCH",
    r.Password != nil && r.PasswordConfirm != nil && *r.Password == *r.PasswordConfirm)
```

**Exactly one of** — mutually exclusive fields:

```go
hasEmail := r.Email != nil && *r.Email != ""
hasPhone := r.Phone != nil && *r.Phone != ""
v.Must("contact", "REQUIRED_ONE_OF", hasEmail != hasPhone) // XOR: exactly one
```

Custom codes get a generic message by default; register your own at init time:

```go
func init() {
    valid.SetMessage("INVALID_DATE_RANGE", "The {field} field must be after {other}")
    valid.SetMessage("MISMATCH", "The {field} field does not match")
}
```

## Reusable nested validators

Define a request piece once as an `IValidatable` (a `Validate(v *valid.Validator)`
method) and reuse it — standalone, nested under a field, or over a slice.

```go
type AddressRequest struct {
    Street *string `json:"street"`
    Zip    *string `json:"zip"`
}

// rules live here, once
func (r *AddressRequest) Validate(v *valid.Validator) {
    v.Str("street", r.Street).Required()
    v.Str("zip", r.Zip).Required().Length(5, 5)
}

// standalone request: Valid delegates to the shared rules
func (r *AddressRequest) Valid(ctx core.IContext) core.IError {
    return valid.Run(ctx, r)
}
```

**Nest under a field** — `Nested` prefixes every violation with `name.`:

```go
type CheckoutRequest struct {
    BillingAddress  *AddressRequest `json:"billing_address"`
    ShippingAddress *AddressRequest `json:"shipping_address"`
}

func (r *CheckoutRequest) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Nested("billing_address", r.BillingAddress)   // → billing_address.zip, ...
    v.Nested("shipping_address", r.ShippingAddress) // → shipping_address.street, ...
    return v.Error()
}
```

**Slice of nested objects** — `EachNested` prefixes with `name.<index>.`:

```go
type OrderRequest struct {
    Items []*ItemRequest `json:"items"`
}

func (r *OrderRequest) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Int("total", r.Total).Required().Min(0)
    valid.EachNested(v, "items", r.Items)  // → items.0.name, items.1.qty, ...
    return v.Error()
}
```

The same `AddressRequest`/`ItemRequest` type is now reused everywhere with one
source of truth for its rules. Inside `Validate`, `v.Ctx()` gives access to the
context for DB rules (`Unique`/`Exists`).

## Best practices

- **validate ที่ขอบเดียว** — request และ job parameter ตรวจตรงจุดเข้า ส่วน service ชั้นใน
  รับค่าที่ผ่านการตรวจแล้ว การตรวจซ้ำหลายชั้นทำให้กฎสองชุดค่อยๆ ไม่ตรงกัน
- **normalizer มาก่อน rule** — `.Trim().Lower()` แล้วค่อย `.Required().Email()` เพราะ
  rule ทำงานตามลำดับที่เรียก
- **`Required()` กับ `NotBlank()` ตอบคนละคำถาม** — บังคับกรอกและต้องมีเนื้อจริงเขียนคู่กัน
- **enum ใช้ `In(...)` ไม่ใช่ตรวจเองใน service** — และคอลัมน์ที่ client เลือกได้
  (`order_by`, `filter`) ต้องมี allow-list เสมอ
- **กฎที่แตะ database ใช้ `Unique`/`Exists`** แทน query เอง มันผูก context ของ request
  อยู่แล้ว จึงถูกยกเลิกพร้อมกันเมื่อ client ตัดสาย
- **code สำหรับเครื่อง message สำหรับคน** — frontend ควร branch จาก `code` ไม่ใช่จาก
  ข้อความ เพราะข้อความเปลี่ยนได้เมื่อไหร่ก็ได้
- **payload ของ job ใช้ builder ตัวเดียวกัน** — แต่ระวังว่า scheduled run ไม่มี parameter
  จึงห้ามใส่ `Required()` ในสิ่งที่ตั้งเวลาไว้ ([Jobs](./jobs-defining.md#parameters))
- **อย่าเขียนกฎธุรกิจที่ต้องอ่านหลายตารางลงใน `Valid`** — ตรวจรูปแบบที่นี่ ตรวจเงื่อนไข
  ที่ service ซึ่งเป็นที่ที่ transaction อยู่
