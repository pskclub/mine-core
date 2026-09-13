# Testing — Fixtures

หน้านี้ว่าด้วยตัวสร้าง fixture: อะไรถูกสร้างขึ้นมาให้ ปรับแต่งได้แค่ไหน และแต่ละเทส
ถูกแยกออกจากกันอย่างไร

## Entry points

| ฟังก์ชัน | คืนอะไร | ใช้เมื่อ |
|---|---|---|
| `NewContext(t, ...)` | `core.IContext` | เทส service / repository — ใช้บ่อยที่สุด |
| `NewApp(t, ...)` | `*core.App` | ต้องการหลาย context บน pool เดียวกัน |
| `NewDB(t, ...)` | `*gorm.DB` | seed หรือ assert ตรงๆ โดยไม่ผ่าน context |
| `NewServer(t, ...)` | `*coretest.Server` | เทส handler ([HTTP](./testing-http.md)) |
| `NewJob(t, ...)` | `*coretest.Job` | เทส job ([Jobs](./testing-jobs.md)) |

ทุกตัวรับ `Option` ชุดเดียวกัน

## `NewContext` สร้างอะไรให้บ้าง

เรียกครั้งเดียวได้ครบ:

1. **database เปล่าเฉพาะเทสนี้** — sqlite `:memory:` หรือ schema ชั่วคราวบน postgres
2. **schema** — จาก `WithAutoMigrate` หรือ `WithMigrations`
3. **`IENV`** — อ่านจาก environment ที่ scope ไว้ด้วย `t.Setenv` (ค่าเริ่มต้น `ENV=test`)
4. **`App`** — ผูก database เข้าเป็น connection ชื่อ `default`
5. **`IContext`** — จาก `app.NewContext(t.Context(), ModeTest)`

ผลคือ `ctx.DB()`, `ctx.ENV()`, `ctx.Log()` ใช้ได้ทันที และ `repository.New[T](ctx)`
ทำงานได้เลย

```go
ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&models.User{}))

repository.New[models.User](ctx).Create(&user)   // ใช้ได้เลย
ctx.DB().Exec("CREATE INDEX ...")                // เข้าถึง gorm ตรงๆ ก็ได้
```

## Options ทั้งหมด

### `WithAutoMigrate(models ...any)`

สร้าง table จาก Go struct ผ่าน `db.AutoMigrate`

```go
coretest.NewContext(t, coretest.WithAutoMigrate(&models.User{}, &models.Order{}))
```

สะดวก แต่เป็น**ทางลัด**: มันสร้าง schema ตามที่ struct บอก ไม่ใช่ตามที่ migration
สร้าง ถ้าสองอย่างเริ่มไม่ตรงกัน path นี้ไม่มีทางบอกคุณได้ — ใช้ `WithMigrations`
สำหรับสิ่งที่ deploy จริง

### `WithMigrations(dir string)`

apply ไฟล์ `<dir>/*/migration.sql` เรียงตามชื่อ

```go
coretest.NewContext(t,
    coretest.WithAutoMigrate(&models.User{}),                        // สำหรับ sqlite
    coretest.WithMigrations(filepath.Join("..", "prisma", "schema", "migrations")),
)
```

ใส่ทั้งสองตัวได้ — postgres จะใช้ migration ส่วน sqlite ใช้ `AutoMigrate`
(เพราะรับ dialect ของ postgres ไม่ได้) รายละเอียดอยู่ที่
[Database backends](./testing-database.md#ใช้-migration-จริง)

path เป็น relative จาก**โฟลเดอร์ของ package ที่เทสอยู่** ไม่ใช่ repo root

### `WithDatabaseURL(dsn string)`

บังคับใช้ postgres ที่ DSN นี้ โดยไม่สนใจ `TEST_DATABASE_URL` — ใช้เมื่อบางเทส
ต้องรันบน postgres เสมอ ไม่ว่าคนรันจะตั้งตัวแปรหรือไม่

### `WithEnv(map[string]string)`

ตั้ง config key เฉพาะเทสนี้ ใช้ชื่อแบบเดียวกับใน `ENVConfig`

```go
ctx := coretest.NewContext(t, coretest.WithEnv(map[string]string{
    "jwt_secret": "s3cret",
    "log_level":  "debug",
}))
```

ข้างในใช้ `t.Setenv` ซึ่ง Go จะคืนค่าเดิมให้เมื่อเทสจบ ไม่รั่วไปหาเทสอื่น
(และทำให้เทสนั้นรัน `t.Parallel()` ไม่ได้ ซึ่งเป็นข้อจำกัดของ Go เอง)

### `WithMode(core.Mode)`

เปลี่ยน mode ของ context ค่าเริ่มต้นคือ `ModeTest` ใช้ `ModeCron` เมื่ออยากให้โค้ด
ที่เช็ค `ctx.Mode()` เดินเส้นทางของ job

### `WithAppOptions(opts ...core.Option)`

ส่ง option เพิ่มให้ `core.NewApp` — สำหรับต่อ cache, MQ หรือ requester ปลอม

```go
ctx := coretest.NewContext(t,
    coretest.WithAppOptions(core.WithCache("default", fakeCache)),
)
```

ไม่ได้ตั้ง = capability นั้นคืน `nil` (ไม่ panic) โค้ดที่เช็ค `if ctx.MQ() != nil`
จึงเทสได้ทั้งสองทาง ยกเว้น cache ที่ไม่เคยเป็น `nil` — ถ้าไม่ได้ตั้งจะได้ handle ที่อ่าน
miss/เขียนทิ้ง ถ้าอยากได้ของจริงในเทสใช้ `core.NewMemoryCache()`

### `WithoutDefaultTransaction()`

ปิด transaction อัตโนมัติของ GORM

มีไว้เพื่อกรณีเฉพาะแต่สำคัญ: GORM ห่อ write ด้วย transaction ของมันเองอยู่แล้ว
แปลว่าเทส rollback ธรรมดา**แยกไม่ออก**ว่า transaction ที่ทำงานมาจากโค้ดคุณหรือจาก
GORM ปิดมันเพื่อให้ assertion มีความหมาย:

```go
ctx := coretest.NewContext(t,
    coretest.WithAutoMigrate(&models.User{}),
    coretest.WithoutDefaultTransaction(),
)
// ถ้า service ไม่ได้เปิด transaction เอง เทสนี้จะ fail
```

วิธีตรวจว่าเทสของคุณมีความหมายจริง: ลบ transaction ออกจากโค้ดชั่วคราวแล้วรันดู
ถ้ายังผ่าน แปลว่าเทสนั้นยังพิสูจน์อะไรไม่ได้

## การแยกเทสออกจากกัน

**หนึ่งครั้งที่เรียก = หนึ่ง database** เรียก `NewContext` สองครั้งในเทสเดียวได้สอง
database คนละอัน:

```go
first := coretest.NewContext(t, coretest.WithAutoMigrate(&user{}))
repository.New[user](first).Create(&user{Email: "only@here.co"})

second := coretest.NewContext(t, coretest.WithAutoMigrate(&user{}))
n, _ := repository.New[user](second).Count()   // 0 — คนละที่กัน
```

ถ้าอยากให้ subtest แชร์ข้อมูลกัน ให้เรียก `NewContext` **นอก** `t.Run` แล้วส่งต่อ:

```go
ctx := coretest.NewContext(t, ...)      // ครั้งเดียว
seedUsers(t, ctx, "a@x.co", "b@x.co")   // seed ครั้งเดียว

t.Run("first page", func(t *testing.T) { ... })   // เห็นข้อมูลเดียวกัน
t.Run("second page", func(t *testing.T) { ... })
```

ปลอดภัยเมื่อ subtest เป็น read-only ทั้งหมด ถ้ามีอันไหนเขียนข้อมูล มันจะรั่วไปหา
อันถัดไปทันที — กรณีนั้นให้แต่ละ subtest เรียก `NewContext` ของตัวเอง

## seed ข้อมูล

seed ด้วย `ctx.DB()` ตรงๆ **ไม่ใช่ผ่าน service**:

```go
func seedUsers(t *testing.T, ctx core.IContext, emails ...string) []models.User {
    t.Helper()

    out := make([]models.User, 0, len(emails))
    for _, email := range emails {
        u := models.User{BaseModel: models.NewBaseModel(), Email: email, FullName: "Seed " + email}
        require.NoError(t, ctx.DB().Create(&u).Error)
        out = append(out, u)
    }
    return out
}
```

เหตุผล: ถ้า seed ด้วย `svc.Create()` แล้ววันหนึ่ง `Create` พัง เทสของ `Pagination`
จะพังตามไปด้วยทั้งที่ pagination ไม่ได้ผิด — อ่าน failure แล้วหลงทาง

> ⚠️ ระวังจุดบอดจาก seed: ถ้า seed ตั้ง `FullName = "Seed " + email` ทุกแถวจะมี
> อีเมลอยู่ในชื่อเสมอ เทสที่ค้นด้วยอีเมลจะผ่านแม้โค้ดจะเลิกค้นอีเมลไปแล้ว
> ให้ seed อย่างน้อยหนึ่งแถวที่ **ชื่อกับอีเมลไม่เกี่ยวกันเลย**
