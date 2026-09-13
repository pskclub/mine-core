# Testing — Database backends

ชุดเทสเดียวรันได้สอง backend สลับด้วย environment variable ตัวเดียว

```sh
go test ./...                                    # sqlite in memory
TEST_DATABASE_URL=postgres://... go test ./...   # postgres ของจริง
```

## เทียบกัน

| | sqlite (ค่าเริ่มต้น) | postgres |
|---|---|---|
| ต้องติดตั้งอะไร | ไม่ต้อง | database ที่รันอยู่ |
| ความเร็ว (ชุดเทส template) | ~0.2s | ~1.5s |
| การแยกกัน | database ใหม่ต่อเทส | schema ใหม่ต่อเทส |
| schema มาจาก | `AutoMigrate` (Go struct) | migration จริง |
| partial index | ไม่มี | มี |
| ชนิด UUID | ไม่มี (เก็บเป็น text) | มี |
| `ILIKE` | ไม่มี | มี |
| ตรวจชนิดข้อมูล | หลวม | เข้ม |

## ทำไมต้องมีสองอัน

sqlite เร็วพอจะรันทุกครั้งที่กด save — ไม่ต้องเปิด docker ไม่ต้องรอ ทำให้เทสเป็น
เครื่องมือระหว่างเขียนโค้ด ไม่ใช่พิธีกรรมตอนท้าย

แต่มันคนละ database กับที่ deploy จริง และ `AutoMigrate` สร้าง schema จาก Go
struct ซึ่ง**ไม่รู้จัก**อะไรที่ประกาศไว้ในไฟล์ migration เลย

ตัวอย่างจริงที่เคยเจอ: schema จริงมี

```sql
CREATE UNIQUE INDEX "users_email_key" ON "users"("email");   -- ไม่ partial
```

แต่โค้ด validate ว่าอีเมลของคนที่ถูก soft delete แล้วเอากลับมาใช้ได้
(`Unique(..., valid.Cond("deleted_at IS NULL"))`) สองอย่างนี้ขัดกัน — validation
ผ่าน แล้ว insert ชน index กลายเป็น **500** ใส่หน้าผู้ใช้

- บน sqlite: ผ่าน (`AutoMigrate` ไม่ได้สร้าง unique index นั้นเลย)
- บน postgres + migration จริง: fail ทันที

นี่คือเหตุผลทั้งหมดที่ backend postgres มีอยู่

**รูปแบบที่แนะนำ:** `go test ./...` ระหว่างเขียน และรัน postgres ก่อน push (CI ควรรัน
ทั้งสอง)

## ใช้ migration จริง

```go
ctx := coretest.NewContext(t,
    coretest.WithAutoMigrate(&models.User{}),
    coretest.WithMigrations(filepath.Join("..", "prisma", "schema", "migrations")),
)
```

`WithMigrations` อ่านทุก `<dir>/*/migration.sql` เรียงตามชื่อไดเรกทอรี ทั้ง prisma
และ golang-migrate ขึ้นต้นชื่อด้วย timestamp ลำดับตัวอักษรจึงเป็นลำดับ apply

ใส่ทั้งสอง option ได้และควรใส่ — postgres ใช้ migration, sqlite ใช้ `AutoMigrate`
(sqlite รับ SQL ของ postgres ไม่ได้) เทสชุดเดียวจึงรันได้ทั้งสอง backend

โครงสร้างที่รองรับ:

```
prisma/schema/migrations/
├── 20260726145146_init/
│   └── migration.sql
├── 20260801103000_add_orders/
│   └── migration.sql
└── migration_lock.toml        ← ไม่ใช่ไดเรกทอรี ถูกข้าม
```

## postgres สร้าง/ลบอะไร ที่ไหน

**ไม่ได้สร้าง database ใหม่ — สร้าง schema ใน database เดิม**

ทุกครั้งที่เรียก `NewContext`:

**1. ตั้งชื่อ schema**

```go
schema := "test_" + strings.ReplaceAll(utils.NewUUID(), "-", "")
// test_e9007d62e0ef4b5590b1bd191c39803d
```

ตัด `-` ออกเพราะเป็น identifier ของ postgres ที่ต้อง quote และ UUID ทำให้เทสที่รัน
ขนานกัน (Go รัน package ต่างๆ พร้อมกันโดย default) ไม่ชนกัน

**2. เปิด connection แรก (admin)** ด้วย DSN ที่ให้มา แล้วสั่ง

```sql
CREATE SCHEMA "test_e9007d62..."
```

**3. เปิด connection ที่สอง (scoped)** — DSN เดิม + `?search_path=test_e9007d62...`

`search_path` ทำให้ชื่อ table ที่ไม่ระบุ schema (`users` เฉยๆ) ไปตกใน schema นั้น
แทน `public` ทั้ง migration และทุก query ของเทสจึงอยู่ในกล่องของตัวเอง
**โดยที่โค้ด service ไม่ต้องรู้เรื่องและไม่ต้องแก้อะไรเลย**

ที่ต้องมีสอง connection เพราะจะ `search_path` ไปยัง schema ที่ยังไม่มีตัวตนไม่ได้

**4. apply migration** ผ่าน connection ที่ scoped ไว้ ตารางจึงเกิดใน schema นั้น

### ตอนลบ

ลงทะเบียนไว้ตั้งแต่ตอนสร้าง Go เรียกให้เองเมื่อเทสจบ:

```go
t.Cleanup(func() {
    admin.Exec(`DROP SCHEMA "test_e9007d62..." CASCADE`)
    closeDB(scoped)
    closeDB(admin)
})
```

`CASCADE` ลบทุกอย่างข้างใน (table, index, constraint) ในคำสั่งเดียว จึงไม่ต้องมี
teardown ในเทสเลย และ `t.Cleanup` ทำงานแม้เทสจะ fail หรือ `t.Fatal` — ต่างจากการ
เขียน cleanup ไว้ท้ายฟังก์ชันซึ่งจะถูกข้ามเมื่อ fail

### ทำไม schema ไม่ใช่ database

`CREATE DATABASE` ใน postgres แพงกว่ามาก — คัดลอกจาก template database, สั่งใน
transaction ไม่ได้ และต้องต่อไป database อื่นเพื่อสั่ง ส่วน `CREATE SCHEMA` เป็น
คำสั่งเดียวบน connection ที่มีอยู่แล้ว

ผลที่ตามมาคือ **ชี้ไป development database ได้อย่างปลอดภัย** ข้อมูลใน `public`
ไม่ถูกแตะเลย จึงไม่ต้องสร้าง `my_database_test` แยก

## schema ที่ค้าง

`t.Cleanup` ไม่ทำงานถ้า process ตายกะทันหัน — `kill -9`, ไฟดับ, หรือ Ctrl-C
ระหว่างรัน (`go test` ไม่รัน cleanup เมื่อได้รับ SIGINT)

ตรวจของที่ค้าง:

```sh
docker exec db psql -U my_user -d my_database -tAc \
  "SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE 'test_%';"
```

เก็บกวาด:

```sh
docker exec db psql -U my_user -d my_database -tAc \
  "SELECT 'DROP SCHEMA \"'||schema_name||'\" CASCADE;' \
     FROM information_schema.schemata WHERE schema_name LIKE 'test_%';" \
  | docker exec -i db psql -U my_user -d my_database
```

schema ที่ค้างไม่ทำให้เทสรอบหน้าพัง (ชื่อไม่ซ้ำกันอยู่แล้ว) แค่รกและกินที่

## ตั้งตัวแปรที่ไหน

`TEST_DATABASE_URL` อ่านจาก process environment ตรงๆ ผ่าน `os.Getenv`

> ⚠️ **`.env` ใช้ไม่ได้** และไม่มี prefix `APP_` ใส่ใน `.env` แล้วจะไม่มี error
> ไม่มีคำเตือน — เทสจะรันบน sqlite ต่อไปเงียบๆ

| ที่ | วิธี |
|---|---|
| Makefile | `make test-integration` ตั้งให้แล้ว |
| shell ครั้งเดียว | `TEST_DATABASE_URL="postgres://…" go test ./...` |
| CI | job `test:integration` ใน `.gitlab-ci.yml` |
| VS Code | `go.testEnvVars` ใน `.vscode/settings.json` |
| GoLand | Run Configuration → Environment variables |

ตัวอย่าง CI (GitLab):

```yaml
test:integration:
  services:
    - name: postgres:17-alpine
      alias: db
  variables:
    POSTGRES_DB: my_database
    POSTGRES_USER: my_user
    POSTGRES_PASSWORD: my_password
    TEST_DATABASE_URL: "postgres://my_user:my_password@db:5432/my_database?sslmode=disable"
  script:
    - go test -count=1 ./...
```

## ตรวจว่าตัวแปรถึงจริงไหม

เวลาที่ใช้จะต่างกันชัดเจน เพราะ postgres ต้องสร้าง schema และ apply migration:

```
ไม่ตั้ง  → services 0.167s
ตั้ง     → services 0.617s
```

ถ้าตั้งแล้วเวลาไม่เปลี่ยน แปลว่าตัวแปรไปไม่ถึง หรือเช็คในโค้ดด้วย
`coretest.IsPostgres()`:

```go
func TestOnlyMakesSenseOnPostgres(t *testing.T) {
    if !coretest.IsPostgres() {
        t.Skipf("set %s", coretest.EnvDatabaseURL)
    }
    ...
}
```
