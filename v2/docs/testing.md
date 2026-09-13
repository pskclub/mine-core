# Testing

แพ็กเกจ `coretest` ประกอบ fixture ที่เทสของ service ต้องใช้ — App, `IContext` ที่ต่อ
database จริง, HTTP client ที่เข้าใจรูปแบบ error ของ framework และตัวรัน job

มีไว้เพื่อไม่ให้ทุก service ต้องเขียน wiring ซ้ำกันคนละ 40 บรรทัด

```go
import "github.com/pskclub/mine-core/v2/coretest"
```

import จากไฟล์ `_test.go` เท่านั้น (มันขึ้นกับ `testing`) และทุก helper รับ
`*testing.T` เพื่อให้ failure ชี้กลับมาที่บรรทัดของผู้เรียก ไม่ใช่ในตัว helper

## เริ่มเร็วที่สุด

```go
func TestCreateUser(t *testing.T) {
    ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&models.User{}))

    user, err := services.NewUserService(ctx).Create(payload)

    coretest.RequireNoError(t, err)
    assert.Equal(t, "a@b.co", user.Email)
}
```

`NewContext` คืน `core.IContext` ที่ผูกกับ database แยกของเทสนั้น service รับ
`IContext` อยู่แล้ว จึงเทสได้ตรงๆ **ไม่ต้องมี interface พิเศษ ไม่ต้อง mock**

นี่คือผลจากการที่ทุกอย่างใน framework รับ `IContext` ตัวเดียวกัน — โค้ดที่รันใน
handler, ใน job และในเทส เป็นโค้ดชุดเดียวกันจริงๆ ไม่มีทางแยกสำหรับเทส

## หน้าต่างๆ ในหมวดนี้

**เขียนเทสแต่ละแบบ**

| หน้า | เนื้อหา |
|---|---|
| [Unit tests](./testing-unit.md) | ตรรกะล้วน ไม่แตะ database — table-driven, เทสที่ผ่านโดยไม่พิสูจน์อะไร |
| [Integration tests](./testing-integration.md) | service / repository บน database จริง — soft delete, pagination, transaction |
| [End-to-end tests](./testing-e2e.md) | `Serve` กับ `NewClientFromEnv` — socket จริงและ process จริง |
| [Mocks และของปลอม](./testing-mock.md) | ปลอมอะไรควร ปลอมอะไรไม่ควร และปลอมอย่างไร |

**อ้างอิง API**

| หน้า | เนื้อหา |
|---|---|
| [Fixtures](./testing-fixtures.md) | `NewContext` / `NewApp` / `NewDB`, options ทั้งหมด, การแยกเทส |
| [Database backends](./testing-database.md) | sqlite กับ postgres, migration จริง, กลไก schema ชั่วคราว |
| [HTTP](./testing-http.md) | `NewServer`, ยิง request, ตรวจ error body และ field sources |
| [Jobs](./testing-jobs.md) | `NewJob`, params, retry, การตรวจ job ที่ fail |
| [Assertions](./testing-assertions.md) | assert กับ `core.IError` และ validation error |

## รันยังไง

```sh
go test ./...                                    # sqlite in memory
TEST_DATABASE_URL=postgres://... go test ./...   # postgres ของจริง
E2E_BASE_URL=http://localhost:3000 go test ./... # เพิ่ม e2e เข้าไปด้วย
```

ตัวแปรทั้งสองอ่านจาก **process environment ตรงๆ** ผ่าน `os.Getenv` — ไม่ผ่าน
`.env` และไม่มี prefix `APP_` ใส่ใน `.env` แล้วจะไม่มี error ไม่มีคำเตือน
เทสจะรันแบบเดิมเงียบๆ (ดู [Database backends](./testing-database.md#ตั้งตัวแปรที่ไหน))

## หลักการที่ใช้ออกแบบ

**ไม่ mock database** — mock repository จะเทสแค่ว่า "เราเรียก method ถูกไหม" แต่สิ่งที่
พังจริงคือ SQL: soft-delete filter, `ORDER BY` ที่ซ้ำ, transaction ที่ไม่ rollback,
unique index ที่ขัดกับ validation พวกนี้ mock จับไม่ได้เลยสักอย่าง

**assert จาก wire shape** — helper ทุกตัวที่อ่าน error จะ marshal เป็น JSON ก่อน แล้ว
ค่อยอ่านกลับ สิ่งที่เทสยืนยันจึงเป็นสิ่งที่ client ได้รับจริง ไม่ใช่ field ภายใน
ถ้า response shape เปลี่ยน เทสจะพัง — ซึ่งถูกแล้ว เพราะนั่นคือ breaking change

**แต่ละเทสมี database ของตัวเอง** ไม่ต้องมี teardown ไม่ต้องกลัวลำดับการรัน และ
รันขนานกันได้ ([รายละเอียด](./testing-database.md))

**config มาจาก environment ล้วนๆ** fixture ชี้ `NewEnvPath` ไปโฟลเดอร์ว่าง ไฟล์
`.env` หรือ `test.env` ในเครื่องใครจึงไม่มีผลต่อผลเทส

**ไม่มี Sentry** ถ้าไม่ได้ตั้ง DSN tracker เป็น no-op เทสไม่ยิงออกเน็ต

## Best practices

- **เทสพฤติกรรม ไม่ใช่การเรียกเมธอด** — assert ผลลัพธ์ที่เห็นจากภายนอก (แถวใน database,
  status ของ response, message ที่ถูก publish) ไม่ใช่ว่าเมธอดไหนถูกเรียกกี่ครั้ง
- **ใช้ memory implementation แทน mock** — `NewMemoryCache`, `NewMemoryStorage`,
  `NewMemoryMailer`, `NewMemoryPusher`, `NewRecordingSentry` ทำงานจริงและ compile คู่ไป
  กับ interface จึงพังทันทีที่ interface เปลี่ยน แทนที่จะเงียบจนกว่าจะมีใคร regenerate
- **ข้อความ assert บอกว่าทำไมค่าต้องเป็นแบบนั้น** ไม่ใช่บอกว่ามันต่างกัน —
  `"order ที่จ่ายแล้วต้องยกเลิกไม่ได้"` ดีกว่า `"expected false"`
- **เทส error path ให้พอๆ กับ happy path** — 4xx ที่ควรเกิด, retry ที่ควรทำงาน,
  งานที่ควร dead-letter
- **table-driven สำหรับกฎที่มีหลายกรณี** (validation, การคำนวณ) — เพิ่มเคสได้ด้วยหนึ่ง
  บรรทัด และชื่อเคสกลายเป็นเอกสาร
- **อย่าพึ่งเวลาจริงและลำดับจริง** — ไม่มี `time.Sleep` เพื่อ "รอให้เสร็จ",
  ไม่มีเทสที่ผ่านเฉพาะตอนรันเรียงกัน
- **`go test -race ./...` ต้องสะอาด** — v2 ไม่มี global state จึงไม่มีข้ออ้าง
- **integration test แยกด้วย build tag** — โค้ดที่ครอบคลุมด้วย integration test อย่างเดียว
  คือโค้ดที่ CI ไม่เคยรัน
