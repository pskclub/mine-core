# Schema & Migrations

**mine-core ไม่ migrate database ให้** — และไม่ควรทำ หน้านี้คือเหตุผล และแบบแผน
ที่ใช้กันอยู่: schema มี source of truth เดียวอยู่นอก Go, GORM model *อธิบาย*
ตารางที่ migration สร้างไว้แล้ว

## ทำไมไม่ใช่ `AutoMigrate` {#why-not-automigrate}

`AutoMigrate` เดา schema จาก struct — สะดวกมากตอนเริ่ม และเป็นหนี้ที่ทบทุกเดือน:

| ปัญหา | ผลที่เกิด |
|---|---|
| ไม่ลบ column / ไม่เปลี่ยน type / ไม่ rename | schema จริงกับที่คิดว่ามีค่อยๆ ห่างกัน |
| ไม่มี partial index, check constraint, extension, trigger | index ที่ตั้งใจไว้ไม่เคยถูกสร้าง |
| ไม่มีลำดับและไม่มี rollback | deploy สองตัวพร้อมกัน = แข่งกันแก้ schema |
| ไม่มี data migration | backfill ต้องทำมือ นอกสายตาของ history |
| รันตอน process start | pod ที่ scale ขึ้นพร้อมกันแก้ schema พร้อมกัน |

สิ่งที่แย่ที่สุดคือมัน "ผ่าน" ตลอด: struct เปลี่ยนไปเรื่อยๆ database ตามไม่ทัน
แล้ววันหนึ่ง query ก็พังใน production ด้วยสาเหตุที่ไม่มีอยู่ใน git history

## แบบแผนที่ใช้: schema เป็นเจ้าของโดย migration tool

```
prisma/schema/*.prisma          ← source of truth ของ schema
prisma/schema/migrations/       ← SQL ที่ commit ไว้ รันจริงทั้ง staging และ prod
models/*.go                     ← struct + tag ที่ "อธิบาย" ตารางที่มีอยู่แล้ว
```

[mine-core-template](https://github.com/pskclub/mine-core-template) ใช้ **Prisma**
(`prisma migrate`) แต่หลักการเดียวกันใช้ได้กับ [golang-migrate](https://github.com/golang-migrate/migrate),
[goose](https://github.com/pressly/goose), [atlas](https://atlasgo.io) หรือ
[dbmate](https://github.com/amacneil/dbmate) — สิ่งที่ framework สนใจมีข้อเดียว:
**ไฟล์ SQL ที่ commit ไว้ และรันแยกจาก process ของ service**

Prisma ได้เปรียบตรง `schema.prisma` อ่านง่ายกว่า SQL ดิบ, generate migration ให้
จาก diff, และ seed script เขียนเป็น TypeScript ได้ ถ้าทีมไม่มี Node.js อยู่แล้ว
goose หรือ golang-migrate ตรงไปตรงมากว่า

ขั้นตอนการเพิ่มตาราง:

```sh
# 1. แก้ prisma/schema/*.prisma
make migrate-dev        # เขียน migration จาก schema (บนเครื่อง, ถามชื่อ)
# 2. เพิ่ม struct ใน models/ ให้ตรงกับตารางที่เพิ่งสร้าง
make migrate            # apply migration ที่ commit แล้ว (ใน container — step เดียวกับ CI)
```

`migrate dev` กับ `migrate deploy` ต่างกันมาก: `dev` **เขียน** migration ใหม่ —
ขอ shadow database, ถามคำถาม และ reset database ที่ใช้ร่วมกันได้ ส่วน `deploy`
apply เฉพาะที่ commit ไว้แล้ว จึงชี้ไปที่ database ที่ใช้ร่วมกันได้อย่างปลอดภัย
ดู [Deployment](./deployment.md)

## Model คืออะไรในโครงนี้

struct ใน `models/` ไม่ได้สร้างตาราง มันบอกว่า **column ที่มีอยู่ map กับ Go
ยังไง**:

```go
type Note struct {
    models.BaseModel
    UserID string `json:"user_id" gorm:"column:user_id"`
    Title  string `json:"title"   gorm:"column:title"`
    Pinned bool   `json:"pinned"  gorm:"column:pinned"`
}

func (Note) TableName() string { return "notes" }
```

`models/` เป็น shared kernel — ทุก module ใช้ร่วมกันและมันไม่ import อะไรใน
โปรเจกต์เลย (type ไม่ใช่การตัดสินใจ behaviour ต่างหากที่ใช่ ดู
[Project Structure](./structure.md))

## Test: สอง backend หนึ่งชุดเทสต์

```go
func options(extra ...coretest.Option) []coretest.Option {
    return append([]coretest.Option{
        coretest.WithAutoMigrate(allModels()...),      // sqlite: เร็ว ไม่ต้องพึ่งอะไร
        coretest.WithMigrations(migrationsDir()),      // postgres: schema จริง
    }, extra...)
}
```

| คำสั่ง | Backend | Schema มาจาก | ใช้ตอน |
|---|---|---|---|
| `make test` | sqlite `:memory:` | `WithAutoMigrate` | รันตลอดเวลาระหว่างเขียนโค้ด (~0.5s) |
| `make test-integration` | postgres จริง (schema ชั่วคราว) | `WithMigrations` | **ต้องผ่านก่อน push** |

`WithMigrations` คือสิ่งที่ทำให้รอบ postgres มีค่า: มัน apply SQL ชุดเดียวกับที่
production รัน — constraint ที่โค้ดขัดแย้งด้วยจึงพังที่นี่ ไม่ใช่ที่ staging
(บน sqlite มันถูกข้ามไป เพราะ dialect ไม่รับ)

⚠️ sqlite ไม่มี partial index, ไม่มี UUID type, ไม่มี `ILIKE` และ `AutoMigrate`
สร้าง schema จาก struct ไม่ใช่จาก migration — **ชุดเทสต์ที่เห็นแต่ sqlite จะผ่าน
ฉลุยบน constraint ที่ postgres ปฏิเสธ** จึงต้องรันทั้งสองใน CI: `test` เพื่อ
feedback เร็ว, `test:integration` เพื่อความจริง

`TEST_DATABASE_URL` อ่านจาก process environment ตรงๆ — **ไม่ใช่จาก `.env`** และ
ไม่มี prefix `APP_` ใส่ไว้ใน `.env` แล้วจะไม่มีอะไรเกิดขึ้น ชุดเทสต์จะเงียบๆ
อยู่บน sqlite ต่อไป ดู [Testing: Database](./testing-database.md)

## ข้อยกเว้น: ตารางของ job runner

[Jobs](./jobs.md) ที่เก็บสถานะลง database ต้องมี `job_runs` และ `job_run_logs`
ซึ่งเป็นตารางของ framework ไม่ใช่ของ service:

```go
// service เล็ก: สร้างตอน boot ไปเลย (db ตัวเดียวกับที่ส่งเข้า core.WithSQL)
if err := jobstore.Migrate(db); err != nil {
    return err
}
```

service ที่โตแล้วควรรัน `jobstore.Migrate` ใส่ database เปล่าหนึ่งครั้ง แล้วเอา
DDL ที่ได้ไปใส่ migration tool ของตัวเอง — เพื่อให้ทุกตารางใน production มาจาก
แหล่งเดียวกันหมด

## สรุป

- schema เปลี่ยนได้ที่เดียว: ไฟล์ migration ที่ commit ไว้
- migration รันเป็น **step ต่างหาก** ก่อน rollout ไม่ใช่ตอน process start
- `AutoMigrate` ใช้ได้ในเทสต์ที่รันบน sqlite เท่านั้น
- integration test ต้องอ่าน migration จริง ไม่งั้นมันรับรอง schema ที่ไม่มีใครใช้
