# Testing — End-to-end

`NewServer` เรียก handler ในหน่วยความจำ (`httptest`) ไม่มี socket ไม่มีการ
serialise จริง ซึ่งเร็วและพอสำหรับเกือบทุกอย่าง

แต่มันพิสูจน์ไม่ได้ว่า process จริงเปิด port ถูก, route ถูกลงทะเบียนใน `main`,
config โหลดผ่าน, หรือ migration รันแล้ว — เพราะไม่เคยมี process ไหนถูกรันเลย

มีสองระดับให้เลือก **ทั้งคู่ใช้ assertion ชุดเดิม** เปลี่ยนแค่ transport

## ระดับที่ 1 — `Serve`: socket จริง แต่ยังอยู่ใน `go test`

```go
func TestAPI_overARealSocket(t *testing.T) {
    app := coretest.NewApp(t, coretest.WithAutoMigrate(&models.User{}))

    e := core.NewHTTPServer(app, nil)

    mods, err := cmd.Modules(app)        // wiring จริงของ service
    require.Nil(t, err)
    mods.MountHTTP(e)

    c := coretest.Serve(t, e)

    c.Get("/healthz").RequireStatus(200)
}
```

`Serve` bind listener จริงที่ **port 0** ให้ OS เลือก port ว่างเอง เทสที่รันขนานกัน
จึงไม่ชนกัน แล้วคืน client ที่ชี้ไปที่นั้น server ถูก shutdown อัตโนมัติเมื่อเทสจบ

ก่อนคืน client มันเรียก `WaitReady` ให้แล้ว จึงมั่นใจได้ว่า request แรกไม่เจอ
connection refused

### ได้อะไรเพิ่มจาก `NewServer`

- **TCP จริง** — connection reuse, keep-alive, timeout ของ client
- **serialise จริง** — body ถูกเขียนลง socket แล้วอ่านกลับ ไม่ใช่ส่ง struct ต่อกันตรงๆ
- **routing จริง** — รวมถึงลำดับ route เช่น `/users/bulk` กับ `/users/:id`
- **การประกอบ wiring** — ประกอบ module set ชุดเดียวกับที่ `cmd/` ประกอบ

### ยังคุม database ได้

App อยู่ในมือ จึง seed และ assert ได้เหมือนเดิม:

```go
app := coretest.NewApp(t, coretest.WithAutoMigrate(&models.User{}))
e := core.NewHTTPServer(app, nil)
mods, _ := cmd.Modules(app)
mods.MountHTTP(e)
c := coretest.Serve(t, e)

ctx := app.NewContext(t.Context(), core.ModeTest)
repository.New[models.User](ctx).Create(&seed)     // seed ตรงๆ

c.Get("/users").RequireStatus(200)                 // เห็นแถวนั้น
```

นี่คือข้อได้เปรียบเหนือ e2e เต็มรูปแบบ: ยังจัดสถานะตั้งต้นได้อย่างแม่นยำ

## ระดับที่ 2 — `NewClientFromEnv`: ยิงไปที่ service ที่รันอยู่จริง

```go
func TestBulkCreate_e2e(t *testing.T) {
    c := coretest.NewClientFromEnv(t)          // skip ถ้าไม่ได้ตั้ง E2E_BASE_URL
    c.WaitReady("/healthz", 10*time.Second)

    body := c.Post("/users/bulk", payload).RequireStatus(400).Error()

    assert.Equal(t, "DUPLICATE_IN_REQUEST", body.Fields["users.1.email"].Code)
}
```

```sh
E2E_BASE_URL=http://localhost:3000 go test ./...
```

ตรงนี้คุยกับ process แยกจริงๆ ผ่านเครือข่าย — binary ที่ compile แล้ว, config ที่มัน
โหลดเอง, database ที่มัน migrate เอง

### ทำไม skip ไม่ใช่ fail

คนที่รัน `go test ./...` เฉยๆ โดยไม่ได้เปิด service ควรเห็นสีเขียว ไม่ใช่เจอ error
กองหนึ่งที่ไม่เกี่ยวกับสิ่งที่เขาแก้ ส่วน CI เป็นที่ที่ตั้งตัวแปรให้ครอบคลุมจริง

ข้อความ skip บอกด้วยว่าต้องตั้งอะไร:

```
--- SKIP: TestBulkCreate_e2e (0.00s)
    set E2E_BASE_URL to run this against a running service
```

ข้อแลกเปลี่ยน: ถ้า CI ลืมตั้งตัวแปร เทสจะถูกข้ามเงียบๆ ให้ตรวจในผล CI ว่าไม่มี
SKIP ที่ไม่ควรมี หรือใส่ guard ไว้:

```go
if os.Getenv("CI") != "" && os.Getenv(coretest.EnvBaseURL) == "" {
    t.Fatal("CI ต้องตั้ง E2E_BASE_URL")
}
```

### `WaitReady`

จำเป็นเมื่อ service ขึ้นพร้อมกับเทส — container ที่ **รันแล้ว** ไม่ได้แปลว่า
**listen แล้ว** ถ้าไม่รอ request แรกจะเจอ connection refused แล้วอ่านเหมือน
service พัง ทั้งที่แค่ยังไม่พร้อม

```go
c.WaitReady("/healthz", 30*time.Second)
```

poll ทุก 100ms จนกว่าจะตอบ หรือ fail พร้อมบอก error สุดท้ายที่เจอ

### header ถาวร

```go
token := login(t)
c := coretest.NewClientFromEnv(t, coretest.WithHeader("Authorization", "Bearer "+token))

c.Get("/me").RequireStatus(200)     // ทุก request มี header นี้
```

เปลี่ยน transport ทั้งก้อนก็ได้ (timeout ยาวขึ้น, cookie jar, proxy):

```go
c := coretest.NewClientFromEnv(t, coretest.WithHTTPClient(&http.Client{
    Timeout: 2 * time.Minute,
}))
```

## เลือกระดับไหน

| | `NewServer` | `Serve` | `NewClientFromEnv` |
|---|---|---|---|
| socket จริง | ไม่ | ใช่ | ใช่ |
| process แยก | ไม่ | ไม่ | ใช่ |
| ต้องมี service รันอยู่ | ไม่ | ไม่ | ใช่ |
| seed database ได้ | ใช่ | ใช่ | ไม่ (ต้องผ่าน API) |
| ความเร็ว | เร็วที่สุด | เร็ว | ช้าสุด |
| จับอะไรได้เพิ่ม | handler | serialise, routing, wiring | `main`, config, migration, deploy |

**เขียนส่วนใหญ่ที่ `NewServer`** ใช้ `Serve` เมื่ออยากพิสูจน์ว่าทุกชิ้นประกอบติดกัน
และเก็บ `NewClientFromEnv` ไว้เป็น smoke test จำนวนน้อยๆ

เหตุผลที่ไม่ควรมี e2e เยอะ: มันช้า, ต้องพึ่งของภายนอก, และเวลา fail มันบอกแค่ว่า
"พัง" ไม่ได้บอกว่าพังตรงไหน ส่วนเทสระดับ service ที่ fail ชี้ไปที่ฟังก์ชันเดียวได้เลย

e2e ที่คุ้มค่าคือพวกที่ตอบคำถามว่า "ของที่ deploy ไปแล้วยังใช้ได้ไหม" — health check,
เส้นทางหลักหนึ่งเส้น, การ login สักครั้ง ไม่ใช่การไล่เทส validation ทุกกรณีซ้ำอีกรอบ

## รันคู่กับ docker compose

```sh
make start                                   # service + database ขึ้น
E2E_BASE_URL=http://localhost:3000 go test ./...
```

ใน CI ให้ service เป็น job ที่รันคู่กัน แล้วชี้ `E2E_BASE_URL` ไปที่ alias ของมัน
`WaitReady` จัดการเรื่องจังหวะให้เอง
