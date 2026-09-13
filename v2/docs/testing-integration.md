# Testing — Integration tests

Integration test คือเทสที่ให้โค้ดคุยกับ**ของจริง** — database จริง, schema จริง,
SQL จริง — แต่ยังอยู่ในกระบวนการเดียวกับเทส ไม่ต้องมี service รันอยู่

นี่คือระดับที่เทสส่วนใหญ่ของ service ควรอยู่ เพราะสิ่งที่พังจริงในงานหลังบ้านคือ
SQL ไม่ใช่ตรรกะใน Go

## ทำไมไม่ mock database

mock repository เทสได้แค่ว่า "เราเรียก method ถูกไหม" แต่สิ่งที่พังจริงคือ

- soft-delete filter ที่ลืมใส่
- `ORDER BY` ที่ถูกเติมสองครั้ง
- transaction ที่ไม่ rollback จริง
- unique index ที่ขัดกับกติกา validation
- ชนิดข้อมูลที่ไม่ตรงกับ schema

mock จับไม่ได้สักอย่าง เพราะ mock ไม่มี SQL engine อยู่ข้างใน — มันคืนสิ่งที่เราสั่ง
ให้มันคืน ซึ่งก็คือสิ่งที่เราเดาว่า database จะทำ ถ้าเราเดาผิด mock ก็ผิดตาม

`coretest` จึงให้ database จริงที่เร็วพอจะใช้ตลอดเวลาแทน — ดู
[Database backends](./testing-database.md)

## รูปแบบพื้นฐาน

```go
func TestUserService_Create(t *testing.T) {
	ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&models.User{}))

	user, err := services.NewUserService(ctx).Create(&services.UserCreatePayload{
		Email:    "alice@example.com",
		FullName: "Alice",
	})

	coretest.RequireNoError(t, err)
	assert.Equal(t, "alice@example.com", user.Email)
	assert.NotEmpty(t, user.ID, "the service assigns the id")
}
```

service รับ `core.IContext` อยู่แล้ว จึงเรียกได้ตรงๆ **ไม่ต้องมี interface พิเศษ
สำหรับเทส** โค้ดที่รันในเทสคือโค้ดชุดเดียวกับที่รันใน handler

## เทสเส้นทางที่ไม่สำเร็จ

เส้นทางที่ผิดพลาดสำคัญกว่า happy path เพราะมันคือสิ่งที่ผู้ใช้เจอจริง

```go
t.Run("missing", func(t *testing.T) {
	_, err := svc.Find("00000000-0000-0000-0000-000000000000")

	coretest.RequireStatus(t, err, http.StatusNotFound)
	coretest.RequireCode(t, err, "NOT_FOUND")
})
```

จุดที่ต้องยืนยันคือ **404 ไม่ใช่ 500** — "ไม่เจอ" เป็นคำตอบ ไม่ใช่ความล้มเหลว
ถ้าวันหนึ่งมีคนไปห่อ error ผิดจนกลายเป็น 500 เทสนี้จะจับได้ และ client ที่แยก
"ไม่มีข้อมูล" ออกจาก "ระบบพัง" ไม่ต้องมาเดา

## เทสสิ่งที่ SQL ทำจริง

soft delete เป็นตัวอย่างที่ชัด: จากมุมแอปคือ "หายไป" แต่จากมุม database คือ
"ยังอยู่แต่มี `deleted_at`" เทสควรยืนยันทั้งสองมุม

```go
func TestUserService_Delete(t *testing.T) {
	ctx := newCtx(t)
	seeded := seedUsers(t, ctx, "dave@example.com")
	svc := services.NewUserService(ctx)

	coretest.RequireNoError(t, svc.Delete(seeded[0].ID))

	// มุมแอป: อ่านไม่เจอแล้ว
	_, err := svc.Find(seeded[0].ID)
	coretest.RequireStatus(t, err, http.StatusNotFound)

	// มุม database: แถวยังอยู่ กู้คืนได้ และยังจองอีเมลนั้นไว้
	var count int64
	require.NoError(t, ctx.DB().Unscoped().Model(&models.User{}).
		Where("id = ?", seeded[0].ID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "delete is soft: the row survives")
}
```

`ctx.DB()` ให้ `*gorm.DB` ตรงๆ สำหรับตรวจสิ่งที่ service ไม่เปิดเผย

## เทส pagination

```go
func TestUserService_Pagination(t *testing.T) {
	ctx := newCtx(t)
	seedUsers(t, ctx, "a@example.com", "b@example.com", "c@example.com")
	svc := services.NewUserService(ctx)

	t.Run("limits and counts", func(t *testing.T) {
		page, err := svc.Pagination(&core.PageOptions{Limit: 2, Page: 1})

		coretest.RequireNoError(t, err)
		assert.Len(t, page.Items, 2, "limit applies to the page")
		assert.Equal(t, int64(3), page.Total, "total counts every match, not just this page")
	})

	t.Run("second page", func(t *testing.T) {
		page, err := svc.Pagination(&core.PageOptions{Limit: 2, Page: 2})

		coretest.RequireNoError(t, err)
		assert.Len(t, page.Items, 1)
	})
}
```

สองเทสนี้จับคนละเรื่อง: อันแรกจับกรณีที่ `Total` ถูกคำนวณจาก `len(Items)` (ทำให้
จำนวนหน้าผิด) อันที่สองจับ `OFFSET` ที่คลาดไปหนึ่ง

`subtest` แชร์ `ctx` และข้อมูล seed ชุดเดียวกัน ปลอดภัยเพราะทุกอันเป็น read-only
ถ้าจะมี subtest ที่เขียนข้อมูล ให้เรียก `newCtx` แยกของตัวเอง

## เทส transaction

```go
func TestUserService_CreateMany_rollsBackOnFailure(t *testing.T) {
	ctx := coretest.NewContext(t,
		coretest.WithAutoMigrate(&models.User{}),
		coretest.WithoutDefaultTransaction(),   // ไม่งั้นเทสนี้ไม่พิสูจน์อะไร
	)
	require.NoError(t, ctx.DB().Exec(
		`CREATE UNIQUE INDEX idx_users_email ON users(email) WHERE deleted_at IS NULL`).Error)

	svc := services.NewUserService(ctx)
	seedUsers(t, ctx, "taken@example.com")

	// ให้ยาวเกินหนึ่ง batch แล้ววางตัวที่ชนไว้ใน batch หลัง
	payloads := makePayloads(60)
	payloads[55].Email = "taken@example.com"

	_, err := svc.CreateMany(payloads)

	require.NotNil(t, err)

	var count int64
	require.NoError(t, ctx.DB().Model(&models.User{}).
		Where("email LIKE ?", "bulk-%").Count(&count).Error)
	assert.Equal(t, int64(0), count, "a partial batch must not survive")
}
```

สองรายละเอียดที่ทำให้เทสนี้มีความหมาย:

1. **payload ยาวเกินหนึ่ง batch** และตัวที่ชนอยู่ใน batch หลัง — ถ้าทุกอย่างอยู่ใน
   INSERT เดียว statement นั้นก็ล้มตัวเองอยู่แล้ว ไม่ได้พิสูจน์เรื่อง transaction
2. **`WithoutDefaultTransaction`** — GORM ห่อ write ให้เองอยู่แล้ว ถ้าไม่ปิด
   เทสจะผ่านแม้โค้ดไม่ได้เปิด transaction เลย

## เทส validation ที่แตะ database

กติกาอย่าง `Unique` / `Exists` ยิง query จริง จึงต้องมี database

```go
func TestUserCreate_rejectsExistingEmail(t *testing.T) {
	ctx := newCtx(t)
	require.NoError(t, ctx.DB().Create(&models.User{
		BaseModel: models.NewBaseModel(),
		Email:     "taken@example.com",
		FullName:  "Taken",
	}).Error)

	r := &requests.UserCreate{
		Email:    utils.ToPointer("taken@example.com"),
		FullName: utils.ToPointer("Someone"),
	}

	assert.Contains(t, coretest.FieldCodes(t, r.Valid(ctx)), "email")
}
```

และกติกาที่ database บอกไม่ได้ — เช่นอีเมลซ้ำกัน**ภายในคำขอเดียว** — ต้องเทสแยก
เพราะ `Unique` ถามแต่ database ซึ่งไม่รู้เรื่อง payload ที่เหลือ

## รันบนของจริงก่อน push

`make test` ใช้ sqlite ซึ่งเร็วแต่ไม่ใช่ database ที่ deploy จริง

```sh
make test               # sqlite
make test-integration   # postgres + migration จริง
```

ตัวอย่างที่ต่างกันจริง (เจอมาแล้วทั้งสองทาง):

- `ILIKE` ทำงานบน postgres แต่ **sqlite ไม่มี** → `make test` พัง
- unique index แบบไม่ partial มีใน migration จริง แต่ `AutoMigrate` ไม่ได้สร้าง →
  โค้ดที่อนุญาตให้ใช้อีเมลของคนที่ถูกลบซ้ำได้ ผ่านบน sqlite แต่ **500 บน postgres**

รายละเอียดของสอง backend อยู่ที่ [Database backends](./testing-database.md)
