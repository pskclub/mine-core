# Defining Jobs & Parameters

job หนึ่งตัวคือ **นิยาม (`JobDef`) + handler** ลงทะเบียนไว้กับ registry ตอน boot

```go
reg := core.NewJobRegistry()

// ไม่มี parameter
_ = reg.Register(core.JobDef{Name: "reindex"}, func(c core.ICronjobContext) error {
    return search.Reindex(c)
})

// มี parameter แบบ typed
_ = core.RegisterJob(reg, core.JobDef{Name: "report"},
    func(c core.ICronjobContext, p *ReportParams) error {
        return build(c, p)
    })
```

`Register` กับ `RegisterJob` ต่างกันแค่ parameter — `RegisterJob` เป็น generic ที่ decode
JSON ของ run ใส่ `P`, validate ให้ และอ่าน schema ของ parameter ออกมาให้ admin UI

ลงทะเบียนชื่อซ้ำได้ `409 DUPLICATE_JOB` ไม่ใช่การเขียนทับเงียบๆ — สอง module ที่บังเอิญ
ตั้งชื่อ job ชนกันควรรู้ตั้งแต่ boot

## `JobDef` ทุก field

```go
core.JobDef{
    Name:        "settlement",
    Description: "ตัดยอดรายวัน",
    Schedule:    core.Cron("0 2 * * *"),
    Queue:       "heavy",
    Timeout:     10 * time.Minute,
    StopGrace:   time.Minute,
    MaxAttempts: 3,
    Backoff:     core.ExponentialBackoff(time.Second, 2*time.Minute),
    MaxConcurrent: 1,
    Concurrency:   core.ConcurrencySkip,
    Replayable:    core.BoolPtr(false),
    Logs:          core.LogPolicyPtr(core.LogAlways),
    RetainRuns:    365 * 24 * time.Hour,
    RetainLogs:    30 * 24 * time.Hour,
}
```

| field | default | ความหมาย |
|---|---|---|
| `Name` | – (บังคับ) | ตัวระบุ job ใช้ตอน trigger, pause, ดู log |
| `Description` | – | ข้อความที่หน้า admin แสดง |
| `Schedule` | `nil` = manual-only | `core.Cron(...)`, `core.CronIn(loc, ...)`, `core.CronWithSeconds(...)`, `core.Every(d)` |
| `Queue` | `"default"` | ป้ายบอกว่า worker กลุ่มไหนหยิบ — [ดู queue](./jobs-running.md#queue-แยกงานหนักออกจากงานเบา) |
| `Timeout` | 5 นาที | context ของ run ถูก cancel เมื่อครบ |
| `StopGrace` | 30 วินาที | รอ handler คืนค่าหลังถูก cancel ก่อนจะทิ้งมันไป |
| `MaxAttempts` | 1 (ไม่ retry) | จำนวนครั้งรวมครั้งแรก |
| `Backoff` | exponential 1s→5m | ระยะห่างก่อน retry แต่ละครั้ง |
| `MaxConcurrent` | 0 = ไม่จำกัด | กี่ run ของ job นี้ที่ทำงานพร้อมกันได้ (1 = singleton) |
| `Concurrency` | `ConcurrencyEnqueue` | ทำยังไงเมื่อชนเพดาน — [สามนโยบาย](./jobs-running.md#เมื่อชนเพดาน-สามนโยบาย) |
| `Replayable` | `true` | `false` = operator กด replay ไม่ได้ (`403`) |
| `Params` | อ่านจาก type ให้ | schema ที่ admin UI ใช้สร้างฟอร์ม |
| `Logs` | ตาม runner (`LogOff`) | นโยบาย log ต่อ job |
| `RetainRuns` / `RetainLogs` | 30 วัน / 7 วัน | TTL ที่ `Purge` ใช้ |

ค่าที่ปล่อยว่างไว้จะถูก resolve เป็น default **ตอน log ครั้งแรก** ด้วย — boot log พิมพ์
`timeout=5m0s` ไม่ใช่ `0s` เพราะ `0s` อธิบาย struct ไม่ได้อธิบายพฤติกรรม

## Parameters

`RegisterJob` decodes the run's JSON into your struct and validates it before the
handler is called — the same contract as an HTTP payload, and the **same
[validation builder](./validation.md)**, because a run context *is* a
`core.IContext`:

```go
type ReportParams struct {
    Date  *string `json:"date"`
    Email *string `json:"email"`
    Limit *int64  `json:"limit"`
}

func (p *ReportParams) Valid(ctx core.IContext) core.IError {
    v := valid.New(ctx)
    v.Str("date", p.Date).Date("2006-01-02")
    v.Str("email", p.Email).Email().Exists("users", "email")   // DB rules work too
    v.Int("limit", p.Limit).Between(1, 1000)
    return v.Error()
}

core.RegisterJob(reg, core.JobDef{Name: "report"},
    func(c core.ICronjobContext, p *ReportParams) error {
        date := utils.ToNonPointerOr(p.Date, yesterday())
        return build(c, date)
    })

runner.Trigger(ctx, "report", &ReportParams{Date: utils.ToPointer("2026-07-01")})
```

Everything in [validation.md](./validation.md) applies unchanged — field builders,
`Each`/`Nested`, `Unique`/`Exists` with scopes, `Check`/`CheckDB`, custom
messages. DB-backed rules query through the run's own connections, so they are
cancelled with the run like any other query.

Violations fail the run **before the handler is called**, and land on the run
itself in the same shape the HTTP layer returns:

```json
{
  "status": "failed",
  "error": {
    "code": "INVALID_PARAMS",
    "status": 400,
    "fields": {
      "date":  { "code": "INVALID_DATE",  "message": "The date field must be a valid date" },
      "email": { "code": "NOT_EXISTS",    "message": "The email field does not exist" }
    }
  }
}
```

> ⚠️ A **scheduled** run passes no parameters, so the struct arrives with its
> zero values. If a field is `Required()`, every scheduled run will fail
> validation. For a job that is both scheduled *and* parameterised, validate the
> shape (`Date()`, `Between()`, …) and default the value in the handler, or keep
> the job manual-only.

`IValidate` (`Valid() core.IError`, no context) is supported too, for parameters
with no DB rules. Both work whether the parameter type is a struct or a pointer
to one.

อ่าน parameter เองก็ได้ ถ้า job ลงทะเบียนด้วย `Register` ธรรมดา:

```go
var p ReportParams
if err := c.Params(&p); err != nil {   // decode + validate ในขั้นเดียว
    return err
}
```

## Publishing the parameter schema

A job also advertises **what** it accepts, so an admin API can list the jobs and
a UI can build the "run now" form on its own. Declare it on the `JobDef`, next to
everything else about the job — the parameter struct stays a plain data type:

```go
core.RegisterJob(reg, core.JobDef{
    Name: "sales-report",
    Params: core.Params(
        core.DateParam("date").Required().
            Desc("Day to report on").Example("2026-07-01"),
        core.EnumParam("status", "draft", "sent", "paid").Default("sent"),
        core.IntParam("limit").Desc("Maximum rows"),
        core.BoolParam("force"),
        core.ArrayParam("emails", core.StringParam("")),
        core.ObjectParam("address",
            core.StringParam("street").Required(),
            core.StringParam("zip"),
        ),
    ),
}, salesReport)
```

**Constructors**: `StringParam` · `IntParam` · `NumberParam` · `BoolParam` ·
`DateParam` · `TimeParam` · `DateTimeParam` · `EnumParam(name, values…)` ·
`ArrayParam(name, item)` · `ObjectParam(name, fields…)` · `AnyParam` ·
`CustomParam(name, kind)`.

**Chained on any of them**:

| | |
|---|---|
| `.Required()` `.Desc(…)` `.Default(…)` `.Example(…)` `.Label(…)` | what a form shows |
| `.Enum(values…)` `.Options(Opt(v, label)…)` | a fixed set, with display text when it differs from the value |
| `.Min(n)` `.Max(n)` `.Between(a, b)` `.Pattern(re)` `.Multiline()` | constraints the form can enforce before submitting |
| `.Kind(…)` `.Meta(key, value)` `.With(fn)` | anything this package does not model |

### Customising further

`CustomParam` takes any kind you like, and `Meta` carries whatever your UI needs
— a widget, a group, an ordering hint. Both are passed through untouched, so a
consumer that does not recognise them can fall back to a text input:

```go
core.CustomParam("amount", "currency").
    Meta("currency", "THB").
    Meta("widget", "money-input").
    Between(1, 1_000_000)
```

`With` is the escape hatch for house conventions — wrap them once and reuse:

```go
func tenantParam() *core.ParamSpec {
    return core.StringParam("tenant").Required().
        With(func(f *core.ParamField) { f.Pattern = "^t_[a-z0-9]+$" })
}
```

### Letting the type describe itself

A parameter type may implement `core.IParamsDescriber` instead. It is the only
way to build a schema from **runtime state** — allowed values loaded from the
database, a feature flag — and it keeps the description next to the struct:

```go
func (PayoutParams) ParamSchema() []core.ParamField {
    return core.Params(
        core.StringParam("tenant").Required(),
        core.EnumParam("currency", loadCurrencies()...),
    )
}
```

`JobDef.Params` wins when both are given. Whichever way the schema arrives, the
name check below applies.

```go
runner.Registry().Info()            // every job + its schedule, limits and params
runner.Registry().Params("report")  // just one job's parameters
```

```json
{
  "name": "report",
  "schedule": "cron(0 2 * * *)",
  "queue": "heavy",
  "max_attempts": 3,
  "concurrency": "skip",
  "replayable": false,
  "paused": false,
  "params": [
    {"name": "date",   "kind": "date", "required": true,
     "description": "Day to report on", "example": "2026-07-01"},
    {"name": "status", "kind": "enum", "enum": ["draft","sent","paid"], "default": "sent"},
    {"name": "limit",  "kind": "int",  "description": "Maximum rows"},
    {"name": "force",  "kind": "bool"},
    {"name": "emails", "kind": "array", "items": {"kind": "string"}}
  ]
}
```

**Kinds**: `string`, `int`, `number`, `bool`, `date`, `time`, `datetime`, `enum`,
`array` (with `items`), `object` (with `fields`), `any`.

A declared name that the parameter struct does not have is **rejected at
registration**:

```
job sales-report declares parameters that its type does not have: statuss
```

That is the trade for keeping the schema out of the struct: it can drift, so the
drift is caught at startup rather than becoming a form field the job silently
ignores. (Free-form parameter types — a `map[string]any` — have nothing to check
against, so their declared schema is taken as written.)

Declare nothing and the job still lists its parameters, read off the type:

```go
core.RegisterJob(reg, core.JobDef{Name: "quick-report"}, salesReport)
// params: [{name: "date", kind: "string"}, {name: "limit", kind: "int"}, …]
```

That is enough for a rough form, but only a declared schema knows that a string
is a date, which values an enum allows, or what a field means. Derived
automatically: `time.Time` → `datetime`, slices → `array`, nested structs →
`object`, embedded structs flattened, `json:"-"` and unexported fields left out.

> `Required()` is documentation for the caller — it does not validate anything;
> `Valid` is still what enforces the rules. Keeping them separate is deliberate:
> a job that is also scheduled must tolerate empty parameters (see the warning
> above) even while the form marks them required for a human.

## สิ่งที่ handler ได้

`ICronjobContext` คือ `IContext` เต็มๆ บวกของเฉพาะ run:

| | |
|---|---|
| `c.JobName()` / `c.RunID()` / `c.Attempt()` / `c.Trigger()` | ตัวตนของ run นี้ — `Attempt()` เป็น 1-based, 2 คือ retry ครั้งแรก |
| `c.Params(&dest)` | decode + validate parameter |
| `c.Progress(50, "…")` / `c.SetResult(v)` | ความคืบหน้าและผลลัพธ์ที่คนดูหน้า admin เห็น |
| `c.Stopping()` / `c.IsStopping()` | ถูกสั่งหยุดหรือยัง — **loop ยาวต้องเช็ค** |
| `c.Log()` | logger ที่ติด `job` / `run_id` / `attempt` ให้ทุกบรรทัด |
| `c.DB()`, `c.Cache()`, `c.MQ()`, … | capability ปกติ ผูก context ของ run แล้ว |

```go
func handler(c core.ICronjobContext) error {
    for i, item := range items {
        if c.IsStopping() {
            return c.Err()          // ถูก cancel / timeout / กำลัง shutdown
        }
        c.Progress(i*100/len(items), item.Name)
        // …
    }
    c.SetResult(map[string]any{"processed": len(items)})
    return nil
}
```

`Trigger()` มีประโยชน์กว่าที่คิด: job ที่ทำงานหนักตอนถูกยิงตามเวลา แต่ควรทำแบบเบาๆ
ตอนคนกดเอง แยกได้ที่นี่โดยไม่ต้องมีสอง job

```go
if c.Trigger() == core.TriggerSchedule {
    // full rebuild
}
```
