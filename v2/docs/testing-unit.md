# Testing — Unit tests

Unit test คือเทสที่ไม่แตะอะไรนอกตัวมันเอง ไม่มี database ไม่มีเครือข่าย ไม่มีไฟล์
รันเป็นมิลลิวินาที และเวลาพังมันชี้ไปที่ฟังก์ชันเดียว

ไม่ต้องใช้ `coretest` เลย — ใช้ `testing` กับ testify ก็พอ

## อะไรควรเป็น unit test

อะไรก็ตามที่เป็น **ตรรกะล้วน**: การคำนวณ, การแปลงข้อมูล, การตัดสินใจ, การประกอบ
query condition

```
✓ helper ใน utils/           ✗ service ที่เรียก repository
✓ การ map DTO                ✗ handler ที่ bind request
✓ scope / query builder      ✗ job ที่เขียน database
✓ กติกาทางธุรกิจที่ไม่แตะ IO
```

ถ้าฟังก์ชันต้องมี `IContext` เพื่อทำงาน มันไม่ใช่ unit test — ดู
[Integration tests](./testing-integration.md)

## ตัวอย่าง: ตัวช่วยเรื่อง pagination

ฟังก์ชันเติมค่า order เริ่มต้นเมื่อ request ไม่ได้ระบุมา:

```go
func DefaultOrder(opts *core.PageOptions, order ...string) *core.PageOptions {
	if opts == nil {
		return &core.PageOptions{OrderBy: order}
	}
	if len(opts.OrderBy) > 0 {
		return opts
	}

	cp := *opts
	cp.OrderBy = order
	return &cp
}
```

เทส ใช้ stdlib ล้วน ไม่ต้องเพิ่ม dependency:

```go
func TestDefaultOrder(t *testing.T) {
	t.Run("fills in the default when the request asked for no order", func(t *testing.T) {
		got := DefaultOrder(&core.PageOptions{Limit: 10}, "created_at DESC")

		if len(got.OrderBy) != 1 || got.OrderBy[0] != "created_at DESC" {
			t.Fatalf("OrderBy = %v, want [created_at DESC]", got.OrderBy)
		}
		if got.Limit != 10 {
			t.Errorf("Limit = %d, want the original 10", got.Limit)
		}
	})

	t.Run("keeps the request's own order", func(t *testing.T) {
		got := DefaultOrder(&core.PageOptions{OrderBy: []string{"email asc"}}, "created_at DESC")

		if got.OrderBy[0] != "email asc" {
			t.Fatalf("OrderBy = %v, want the caller's", got.OrderBy)
		}
	})

	t.Run("does not mutate the options it was given", func(t *testing.T) {
		opts := &core.PageOptions{Limit: 10}
		DefaultOrder(opts, "created_at DESC")

		if len(opts.OrderBy) != 0 {
			t.Errorf("input was mutated: %v", opts.OrderBy)
		}
	})

	t.Run("nil is usable", func(t *testing.T) {
		if got := DefaultOrder(nil, "created_at DESC"); got == nil {
			t.Fatal("returned nil; callers dereference this")
		}
	})
}
```

สังเกตเทสตัวที่สามกับสี่ — ตรวจ**สัญญา**ของฟังก์ชัน ไม่ใช่แค่ happy path: ห้าม
แก้ค่าที่รับมา และรับ nil ได้ ทั้งสองอย่างนี้เคยเป็นบั๊กจริงมาแล้ว

## ตัวอย่าง: table-driven

เมื่อกติกาเดียวมีหลายเคส เขียนเป็นตารางอ่านง่ายกว่าและเพิ่มเคสใหม่ได้ในบรรทัดเดียว

```go
func TestNormalizeFieldPath(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"items.0.name", "items.name"},
		{"items.17.name", "items.name"},
		{"email", "email"},
		{"a.b.c", "a.b.c"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizeFieldPath(tc.in))
		})
	}
}
```

ใช้ `t.Run` ด้วยชื่อเคสเสมอ เวลา fail จะได้รู้ทันทีว่าแถวไหน

## ตัวอย่าง: เทสสิ่งที่ต้องไม่เกิดขึ้น

scope ที่รับคำค้นว่าง ต้องคืน query เดิมโดยไม่เติมเงื่อนไข:

```go
func UserSearch(q string) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if strings.TrimSpace(q) == "" {
			return db
		}
		...
	}
}
```

เทสได้โดยไม่ต้องมี database — เทียบ pointer ว่าเป็นตัวเดิม:

```go
func TestUserSearch_blankQueryIsANoop(t *testing.T) {
	for _, q := range []string{"", "   ", "\t\n"} {
		db := &gorm.DB{}

		if got := UserSearch(q)(db); got != db {
			t.Errorf("UserSearch(%q) altered the query; a blank q must add no condition", q)
		}
	}
}
```

`&gorm.DB{}` เปล่าๆ ใช้ได้เพราะ guard คืนก่อนจะแตะอะไรใน db เลย — ไม่ต้องมี driver

## เทสที่ผ่านโดยไม่ได้พิสูจน์อะไร

อันตรายที่สุดของ unit test คือเทสที่ผ่านเสมอ ไม่ว่าโค้ดจะถูกหรือผิด

วิธีตรวจ: **ลบตรรกะที่กำลังเทสออกชั่วคราว แล้วรันดู** ถ้ายังผ่าน แปลว่าเทสนั้น
ยังพิสูจน์อะไรไม่ได้

ตัวอย่างจริง — เทส rollback ตัวหนึ่งดูดีมาก:

```go
_, err := svc.CreateMany(payloads)   // batch ที่ชนกลางทาง

require.NotNil(t, err)
assert.Equal(t, int64(0), countInserted(t, ctx), "a partial batch must not survive")
```

แต่พอเอา `Transaction` ออกจาก `CreateMany` เทสก็**ยังผ่าน** เพราะ GORM ห่อ
`CreateInBatches` ด้วย transaction ของมันเองอยู่แล้ว เทสจึงแยกไม่ออกว่าการรับประกัน
มาจากโค้ดเราหรือจาก GORM

ทางแก้: ปิด transaction อัตโนมัติ แล้วเทสจะมีความหมาย

```go
ctx := coretest.NewContext(t,
    coretest.WithAutoMigrate(&models.User{}),
    coretest.WithoutDefaultTransaction(),
)
```

อีกกรณีหนึ่ง: เทสค้นหาที่ seed ข้อมูลด้วย `FullName = "Seed " + email` ทำให้ทุกแถวมี
อีเมลอยู่ในชื่อ เทสที่ค้นด้วยอีเมลจึงผ่านแม้โค้ดจะเลิกค้นอีเมลไปแล้ว — seed ต้องมี
อย่างน้อยหนึ่งแถวที่ชื่อกับอีเมลไม่เกี่ยวกันเลย

## แนวทาง

- **ชื่อเทสบอกกติกา ไม่ใช่บอกชื่อฟังก์ชัน** — `TestDefaultOrder_doesNotMutateInput`
  ดีกว่า `TestDefaultOrder2`
- **หนึ่งเทสหนึ่งเรื่อง** ใช้ `t.Run` แยกเคส เวลา fail จะรู้ทันทีว่าเรื่องไหน
- **`require` เมื่อไปต่อไม่ได้ / `assert` เมื่อยังตรวจต่อได้** เทสหนึ่งตัวจึงรายงาน
  ปัญหาได้หลายอย่างในรอบเดียว
- **`t.Helper()` ในทุก helper** ไม่งั้น failure จะชี้มาที่ helper แทนที่จะชี้ที่เทส
- **อย่า mock สิ่งที่ไม่ต้อง mock** ฟังก์ชันที่ไม่แตะ IO ก็เรียกมันตรงๆ
