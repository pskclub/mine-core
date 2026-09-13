# Best Practices

หน้าอื่นในหมวดนี้บอกว่า repository ทำอะไรได้ หน้านี้คือสิ่งที่ควรทำ (และไม่ควรทำ)
เมื่อ service โตขึ้นและตารางไม่ได้มีแค่พันแถวอีกต่อไป

## กฎสิบข้อ

| # | กฎ | ทำไม |
|---|---|---|
| 1 | ทุก query ที่ไม่ใช่การอ่านด้วย primary key ต้องมี **index รองรับ** | ตารางที่ยังเล็กปิดบังปัญหานี้ได้นานหลายเดือน |
| 2 | **อย่าอ่านทั้งตาราง** — `Limit`, `Pagination` หรือ `FindInBatches` เสมอ | `FindAll()` คือ OOM ที่รอวันโต |
| 3 | ระวัง **N+1** ทุกที่ที่มี loop รอบ query | ปัญหา performance อันดับหนึ่งของทุก service ที่ใช้ ORM |
| 4 | transaction สั้นที่สุดเท่าที่ถูกต้อง | lock ที่ถือระหว่างรอ API ภายนอกคือ deadlock ที่รอเกิด |
| 5 | ใน transaction ต้องใช้ `repository.NewWithDB[M](ctx, tx)` **ทุก query** | ไม่งั้นมันไม่ได้อยู่ใน transaction นั้นจริง |
| 6 | schema เป็นของ **migration tool** ไม่ใช่ของ `AutoMigrate` | production ที่ schema เปลี่ยนเองตอน deploy คือของที่ย้อนไม่ได้ |
| 7 | เขียนเงื่อนไขความเป็นเจ้าของ **ใน query** ไม่ใช่ตรวจทีหลัง | IDOR เกิดจากการตรวจที่ลืมได้ |
| 8 | `Select` เฉพาะคอลัมน์ที่ใช้ เมื่อแถวมีคอลัมน์ใหญ่ | ประหยัดทั้ง I/O และ memory |
| 9 | อย่าใช้ `OFFSET` ลึกๆ กับตารางใหญ่ | หน้า 5,000 คือการอ่านทิ้ง 5,000 หน้าแรกทุกครั้ง |
| 10 | เขียนอะไรที่ **รันซ้ำได้** เมื่องานนั้นอาจถูกรันซ้ำ (job, consumer) | at-least-once เป็นค่าตั้งต้นของทั้งสองระบบ |

## Query

```go
// ❌ N+1 — หนึ่ง query ต่อหนึ่งแถว
orders, _ := repository.New[Order](ctx).FindAll()
for _, o := range orders {
    items, _ := repository.New[Item](ctx).Where("order_id = ?", o.ID).FindAll()
}

// ✅ อ่านมาพร้อมกัน
orders, _ := repository.New[Order](ctx).Preload("Items").Limit(100).FindAll()
```

วิธี*เห็น* N+1 ก่อนที่ user จะเห็น: เปิด [query log](./database-logging.md) ใน dev แล้ว
ดูว่า endpoint เดียวยิงกี่ query — ตัวเลขที่โตตามจำนวนแถวคือคำตอบ
([รายละเอียด](./repository-relations.md#n-1-how-to-see-it))

**กรองด้วยเงื่อนไขที่ index ครอบ** ไม่ใช่กรองใน Go:

```go
// ❌ อ่านมาหมดแล้วค่อยคัด
all, _ := repository.New[Order](ctx).FindAll()
for _, o := range all { if o.Status == "paid" { … } }

// ✅ ให้ database ทำงานที่มันเก่ง
paid, _ := repository.New[Order](ctx).Where("status = ?", "paid").FindAll()
```

**ตารางที่มีเวลาเข้ามาเกี่ยว ให้ index เป็นคู่** — `(user_id, created_at)` ตอบทั้ง
"ของ user นี้" และ "เรียงตามเวลา" ในการอ่านครั้งเดียว ส่วน index เดี่ยวสองอันไม่ได้
ช่วยเท่าที่คิด

## Transaction

```go
// ✅ transaction ครอบเฉพาะการเขียนที่ต้องไปด้วยกัน
if err := repo.Transaction(func(tx *gorm.DB) error {
    if err := repository.NewWithDB[Order](ctx, tx).Create(&order); err != nil {
        return err
    }
    return repository.NewWithDB[Stock](ctx, tx).
        Where("sku = ? AND qty >= ?", sku, n).
        Update("qty", gorm.Expr("qty - ?", n))
}); err != nil {
    return err
}

// สิ่งที่อยู่ *นอก* transaction
ctx.MQ().Publish("orders", "order.created", order)     // ดู mq: publish หลัง commit
```

สิ่งที่**ไม่ควร**อยู่ในนั้น: HTTP call ออกไปข้างนอก, การส่งเมล, การรอ lock ของ redis,
งานคำนวณหนัก — ทุกอย่างที่ทำให้ transaction ยาวขึ้นคือการถือ lock ไว้นานขึ้นเท่านั้น
([What belongs inside](./database-transactions.md#what-belongs-inside))

**การเขียนที่แข่งกันควรให้ database ตัดสิน** ไม่ใช่ read-modify-write ใน Go:

```go
// ❌ สอง request อ่านค่าเดียวกัน แล้วเขียนทับกัน
stock.Qty -= n
repo.Save(&stock)

// ✅ atomic ในหนึ่ง statement
repository.New[Stock](ctx).
    Where("sku = ? AND qty >= ?", sku, n).
    Update("qty", gorm.Expr("qty - ?", n))
```

## Schema & migration

- **`AutoMigrate` ใช้ได้ใน dev และเทส ไม่ใช่ production** — [ทำไม](./migrations.md#why-not-automigrate)
- **migration ต้องย้อนได้หรือปลอดภัยพอที่จะไม่ต้องย้อน** — เพิ่มคอลัมน์ที่ nullable
  ก่อน แล้วค่อย backfill ด้วย job แล้วค่อยบังคับ not-null คือลำดับที่ deploy ได้โดยไม่มี
  downtime
- **อย่าลบคอลัมน์พร้อมกับ deploy โค้ดที่เลิกใช้มัน** — ปล่อยให้ผ่านไปหนึ่งรอบ deploy
  เพื่อให้ rollback ยังทำงานได้
- **index ที่เพิ่มบนตารางใหญ่ต้องสร้างแบบ concurrent** (postgres: `CREATE INDEX
  CONCURRENTLY`) ไม่งั้นตารางถูก lock ระหว่างสร้าง
- ตารางของ job runner เป็นข้อยกเว้นที่ core มี `jobstore.Migrate` ให้ —
  [เหตุผล](./migrations.md#ข้อยกเว้น-ตารางของ-job-runner)

## Model

- **`TableName()` เขียนไว้เสมอ** — ชื่อที่ ORM เดาให้เปลี่ยนได้เมื่อ struct ถูก rename
- **เวลาใช้ `*time.Time` เป็นค่าตั้งต้น** — คอลัมน์ที่ยัง null ต้องอ่านได้ว่า "ยังไม่มีค่า"
  ไม่ใช่ `0001-01-01` ที่แยกจากค่าจริงไม่ออก (`CreatedAt` ที่ GORM เติมให้ก็ยังเป็น
  pointer ได้ — ราคาที่จ่ายคือ deref ตอนอ่าน ซึ่ง `utils.ToNonPointer` จัดการให้)
- **ตัวเลขใช้ชนิด 64 บิต** (`int64`/`float64`) — id ที่โตข้าม 32 บิตและยอดที่ overflow
  คือบั๊กที่ค้นพบตอนสายเสมอ ส่วนเงินยังคงเป็น integer ของหน่วยย่อย ไม่ใช่ float
- **เวลาใช้ UTC ในฐานข้อมูล** แปลงเป็น timezone ของผู้ใช้ที่ชั้นบนสุดเท่านั้น
- **เงินอย่าเก็บเป็น float** — integer หน่วยย่อย (สตางค์) หรือ `decimal` ตามที่ database
  รองรับ
- **soft delete ต้องตั้งใจ** — `gorm.DeletedAt` ทำให้ทุก query กรองแถวที่ลบออกให้เอง
  ซึ่งดีจนถึงวันที่ unique index ไม่ยอมให้สร้างซ้ำเพราะแถวเก่ายังอยู่
  ([Soft deletes](./repository-queries.md#soft-deletes-and-unscoped))
- **enum เก็บเป็น string ที่มี constant กำกับ** ไม่ใช่ int ที่ความหมายอยู่ในหัวคนเขียน

## Pagination

```go
page, err := repository.New[Order](ctx).
    Where("user_id = ?", uid).
    Order("created_at desc").
    Pagination(c.GetPageOptionsWithAllowed("id", "created_at"))
```

- **`GetPageOptionsWithAllowed` เสมอเมื่อคอลัมน์ที่เรียงมาจาก client** — allow-list คือ
  สิ่งที่กัน SQL injection ผ่าน `order_by`
- **`OFFSET` ลึกๆ แพงขึ้นเรื่อยๆ** — API ที่ต้องไล่ข้อมูลทั้งชุด ควรใช้ keyset
  (`WHERE id > ?`) แทน page number ([The cost of OFFSET](./database-pagination.md#the-cost-of-offset))
- **จำกัด `Limit` สูงสุด** — default คือ 30 และเพดาน 10,000 ซึ่งสูงเกินไปสำหรับ endpoint
  ส่วนใหญ่ ตั้งเพดานของตัวเองที่ชั้น validation

## หลาย connection

```go
core.NewApp(env,
    core.WithSQL("default", primary),
    core.WithSQL("readonly", replica),
)

repository.NewWithDB[Report](ctx, ctx.DBS("readonly")).FindAll()
```

- อ่านรายงานหนักๆ จาก replica ได้ **แต่อย่าอ่านสิ่งที่เพิ่งเขียน** — replication lag
  ไม่ใช่บั๊ก
- แต่ละ connection มี pool ของตัวเอง — นับรวมกันเวลาเทียบกับ `max_connections`
  ([Performance](./performance.md#sql))

## Testing

```go
ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&models.Order{}))
```

- เทส repository **ด้วย database จริง** ไม่ใช่ mock — [เหตุผล](./repository-testing.md)
- `coretest` ใช้ sqlite โดย default ซึ่งเร็วและพอสำหรับ logic ส่วนใหญ่ แต่
  **sqlite ยอมรับ SQL ที่ postgres ปฏิเสธ** — ตั้ง `TEST_DATABASE_URL` ให้ CI รันกับ
  postgres จริงอย่างน้อยหนึ่งรอบ
- เคสที่คุ้มที่สุด: **unique constraint ทำงานจริง**, **transaction rollback แล้วไม่เหลือ
  ร่องรอย** และ **query ที่มี soft delete คืนสิ่งที่ควรคืน**

## Checklist ก่อนขึ้น production

- [ ] ทุก query ใน path ที่ร้อนมี index รองรับ และตรวจด้วย `EXPLAIN` แล้ว
- [ ] ไม่มี `FindAll()` ที่ไม่มีเงื่อนไขบนตารางที่โตได้
- [ ] endpoint ที่มี list ทั้งหมดใช้ pagination + allow-list ของ `order_by`
- [ ] transaction ไม่มี I/O ภายนอกอยู่ข้างใน
- [ ] migration รันด้วยเครื่องมือ ไม่ใช่ `AutoMigrate` ตอน boot
- [ ] pool ขนาดสอดคล้องกับ `max_connections` และจำนวน replica
- [ ] query log เปิดในระดับที่เห็น query ช้าได้ ([Query logging](./database-logging.md))
- [ ] มีแผนสำหรับตารางที่โตไม่หยุด (retention / archive)
