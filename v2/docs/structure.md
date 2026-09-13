# Project Structure

mine-core ไม่ได้บังคับโครงโปรเจกต์ — `IContext` ใช้ได้จากทุกที่ แต่โครงที่ทีมใช้จริง
และผ่านการใช้งานมาแล้วคือแบบนี้ หน้านี้อธิบายว่ามันเป็นยังไงและ**ทำไม** เพื่อให้
service ใหม่ไม่ต้องออกแบบเองตั้งแต่ต้น

ตัวเป็นๆ อยู่ที่ golang-template (internal) —
clone แล้วเริ่มจากตรงนั้นได้เลย

## Group by feature, not by layer

```
project
    .env                    config (ไม่ใส่ APP_ ที่นี่ — OS env ใช้ APP_ และชนะเสมอ)
    main.go                 เลือก role
    cmd/                    ประกอบและ start service
        bootstrap.go        สร้าง App (config + connections)
        modules.go          Modules — ที่เดียวที่รู้จัก module ทั้งหมด
        api.go              NewAPI — server + health + mods.MountHTTP
        worker.go           newScheduler
        run.go              signal handling + ordered shutdown
    modules/                ← โค้ดของ feature อยู่ที่นี่ทั้งหมด
        note/
            note.module.go        struct + New + Name + interface ที่ตัวเองต้องการ
            note.http.go          func (m *Module) Routes — และไม่มีอย่างอื่น
            note.jobs.go          Jobs / Cron — มีเมื่อ module นี้มีจริง
            note.mq.go            Consumers
            note.api.go           สิ่งที่ module อื่นเรียกได้ (forward ไป service)
            handler/              note.handler.go, note.request.go, note.job.go
            service/              note.service.go, note.dto.go
            client/               เรียก API ของคนอื่น — มีเมื่อ upstream หลาย endpoint
            store/                note.store.go — query ตารางของ module นี้
        user/ auth/               โครงเดียวกัน — ไฟล์ที่ไม่มีของจริงก็ไม่ต้องมี
    models/                 struct + column tags, ใช้ร่วมกันทุกที่
    middlewares/            สิ่งที่ route หยิบใช้ตามชื่อ: AuthRequire(e)
    emsgs/                  error + validation message ของ service นี้ (ต่อ module)
    repo/                   query helper ที่ไม่ได้เป็นของตารางไหนโดยเฉพาะ
    consts/ helpers/
    testkit/                test setup ที่ทุก package ใช้ร่วมกัน
    arch/                   กฎการ import — เขียนเป็น test
```

จัดกลุ่มตาม **feature ไม่ใช่ตาม layer**: ทุกอย่างที่ feature หนึ่งทำ — route,
handler, validation, business rule, query, test — อยู่ใน directory เดียว
การแก้ feature หนึ่งจึงเป็นการแก้ directory เดียว และมอบความเป็นเจ้าของให้ทีมได้

เทียบกับการแยกเป็น `handlers/` `services/` `repositories/` ที่ระดับบนสุด: การเพิ่ม
field หนึ่งช่องต้องเปิดสี่ directory และไม่มีใครตอบได้ว่า "ใครใช้ตารางนี้บ้าง"

ไฟล์ `<name>.*.go` ที่ root ของ module คือ **จุดต่อทั้งหมด**ที่ module มีกับ service —
`.module.go` ประกาศตัว module ที่เหลือแยกตามชนิดของสิ่งที่ต่อ กฎคือ*ชื่อไฟล์ต้องบอกความจริง*
ไม่ใช่ว่าต้องมีให้ครบ module ที่มีแต่ route เขียน `Routes` ไว้ใน `.module.go` เลยก็ได้

สิ่งที่การันตีจึงไม่ใช่ "ไฟล์เดียว" แต่คือ **package เดียว**: ไม่มีจุดต่อไหนของ note
อยู่นอก `modules/note/` ดู [Modules](./modules.md)

## Layer ภายใน module

```
handler → service → store
```

| Layer | รู้เรื่อง | ไม่รู้เรื่อง |
|---|---|---|
| `note.module.go` | module นี้ต่ออะไรบ้าง + ต้องการอะไรจากข้างนอก | process นี้มี HTTP/scheduler ไหม |
| `note.http.go` `.jobs.go` `.mq.go` | path, method, middleware / ชื่อ job / ชื่อ queue | ทุกอย่างที่เหลือ |
| `handler/` | request, status code, การ render — รวม job payload | ตารางและ business rule |
| `service/` | business rule, ลำดับการทำงาน, transaction | HTTP, status code, `ICronjobContext` |
| `store/` | query ตารางของ module นี้ | ว่าใครเรียกมัน |
| `client/` *(ถ้ามี)* | เรียก API ของคนอื่น — base URL, auth, การแปลง error | ว่าใครเรียกมัน |

`client/` เป็นคู่สมมาตรของ `store/` สำหรับ module ที่ข้อมูลอยู่ที่ผู้ให้บริการรายอื่น
มีเมื่อ upstream นั้นมีหลาย endpoint — ดู [External services](./external-services.md)

Handler ทำ 4 อย่างเท่านั้น: **bind → convert → call → render** — บรรทัดไหนที่
*ตัดสินใจ* อะไร บรรทัดนั้นควรอยู่ใน service เพราะ job และ test เข้าถึงได้ด้วย

::: code-group

```go [modules/note/handler/note.handler.go]
func (m NoteHandler) Create(c core.IHTTPContext) error {
    ownerID, err := callerID(c)
    if err != nil {
        return err
    }

    input := &CreateRequest{}
    if bErr := c.BindWithValidate(input); bErr != nil {
        return bErr
    }

    payload, _ := utils.Copy[service.CreatePayload](input)

    note, sErr := service.NewNoteService(c).Create(ownerID, &payload)
    if sErr != nil {
        return sErr
    }

    return c.JSON(http.StatusCreated, note)
}
```

**job entrypoint ก็เป็น handler** ไม่ใช่ service — มันทำ 4 อย่างเดียวกันเป๊ะ เปลี่ยนแค่
ที่มาของ input (`c.Params` แทน `c.BindWithValidate`) และปลายทางของผล (`c.SetResult`
แทน `c.JSON`) วางไว้ที่นี่แล้ว `service/` จะไม่รู้จัก `ICronjobContext` เลย ซึ่งแปลว่า
test ของ service ไม่ต้องประกอบ context ชนิดที่สอง:

```go [modules/note/handler/note.job.go]
func Reindex(c core.ICronjobContext) error {
    params := &ReindexParams{}
    if err := c.Params(params); err != nil {
        return err
    }

    n, sErr := service.NewNoteService(c).Reindex(params.Since)
    if sErr != nil {
        return sErr
    }

    c.SetResult(map[string]int{"reindexed": n})
    return nil
}
```

Service รับ `core.IContext` (ไม่ใช่ `IHTTPContext` และไม่ใช่ `ICronjobContext`) —
โค้ดชุดเดียวจึงรันได้ทั้งใน handler, ใน job และใน test โดยไม่ต้องมี interface สำหรับ
test โดยเฉพาะ:

```go [modules/note/service/note.service.go]
type INoteService interface {
    Create(ownerID string, input *CreatePayload) (*models.Note, core.IError)
    Find(ownerID, id string) (*models.Note, core.IError)
    // ...
}

func NewNoteService(ctx core.IContext) INoteService { return &noteService{ctx: ctx} }
```

> `NewNoteService` เก็บแค่ context ไม่เก็บ logger — `ctx.Log()` พก request id
> มาให้อยู่แล้ว ดู [Logging practices](./logging-practices.md)

Store เป็น package **เดียว**ที่แตะตารางของ module นี้ ทำให้ scope ที่ต้องมีทุกครั้ง
(เช่น "ต้องเป็นของ user คนนี้") ถูกลืมไม่ได้:

```go [modules/note/store/note.store.go]
func Note(ctx core.IContext) *repository.Repo[models.Note] {
    return repository.New[models.Note](ctx)
}

// ownership เป็น WHERE ไม่ใช่การเช็คหลังโหลด — "ไม่ใช่ของคุณ" กับ "ไม่มี" จึงตอบ
// เหมือนกัน (404) ถ้าโหลดมาแล้วค่อยเทียบ user_id คนเรียกจะแยกออกว่า id ไหนมีจริง
func OwnedBy(ownerID string) func(*gorm.DB) *gorm.DB {
    return func(db *gorm.DB) *gorm.DB { return db.Where("user_id = ?", ownerID) }
}
```

:::

## กฎ 4 ข้อที่ทำให้โครงนี้อยู่ได้

### 1. module เป็นเจ้าของตารางของตัวเอง — รวมทั้ง route และ job ของมัน

repository ของ module ไม่ export ออกไป module อื่นถามผ่าน service interface
**shared repository package คือทางลัดที่ปีนึงให้หลัง ทุกอย่างจะ query ทุกอย่าง
และจะไม่มีคอลัมน์ไหนแก้ได้อีก**

job ก็เหมือนกัน: cron ที่ลบแถวใน `access_tokens` เป็นการตัดสินใจของ auth
(token เก็บไว้นานแค่ไหน) มันจึงอยู่ใน `modules/auth/` — ประกาศใน `auth.jobs.go`
ตัว handler อยู่ `handler/auth.job.go` — ไม่ใช่ `jobs/` กลาง

### 2. module ห้าม import module อื่น และเข้าได้ทาง top-level package เท่านั้น

module ประกาศสิ่งที่ตัวเองต้องการเป็น interface **ของตัวเอง** แล้วให้ `cmd.Modules`
ส่งของจริงเข้ามา:

::: code-group

```go [modules/auth/service/auth.deps.go]
// auth ต้องอ่าน account แต่ห้าม import user
type Users interface {
    Find(id string) (*models.User, core.IError)
    FindByEmail(email string) (*models.User, core.IError)
    CreateAccount(email, fullName, passwordHash string) (*models.User, core.IError)
}

type UsersFor func(core.IContext) Users
```

```go [modules/auth/auth.module.go]
// alias ให้ composition root เห็น ตัว interface ประกาศใน service/ ที่ใช้มันจริง
// — root import service อยู่แล้ว ถ้าประกาศกลับด้าน service จะ import root แล้ววนกลับ
type Users = service.Users
type UsersFor = service.UsersFor

type Module struct{ users UsersFor }

func New(users UsersFor) *Module { return &Module{users: users} }

func (*Module) Name() string { return "auth" }
```

```go [cmd/modules.go]
// ที่เดียวที่รู้จัก module ทั้งหมด
func Modules(app *core.App) (*core.ModuleSet, core.IError) {
    usersFor := func(ctx core.IContext) auth.Users { return user.NewUserService(ctx) }
    middlewares.RegisterAuth(app, auth.ResolveToken(app, usersFor))

    return core.NewModules(
        home.New(),
        auth.New(usersFor),
        user.New(),
        note.New(),
    )
}
```

:::

ทำไมต้องเป็น interface ไม่ import ตรงๆ: route ของ user อยู่หลัง guard ของ auth
อยู่แล้ว → user ขึ้นกับ auth การ import กลับจะปิดวงและ**คอมไพล์ไม่ผ่าน** การประกาศ
ที่ฝั่งผู้ใช้ (shape ที่ auth ต้องการ ไม่ใช่ shape ที่ user บังเอิญมี) ทำให้ลูกศร
ชี้ทางเดียว

`UsersFor` เป็น factory ไม่ใช่ value เพราะ service ทุกตัวผูกกับ context เดียว —
deadline, transaction, trace ของ request นั้น ไม่มี "user service ตัวยืน"

ส่วนที่ module อื่นเรียกได้ อยู่ใน `<name>.api.go` ซึ่งเป็น alias + forward สั้นๆ:

```go [modules/user/user.api.go]
type IUserService = service.IUserService

func NewUserService(ctx core.IContext) IUserService { return service.NewUserService(ctx) }
```

ที่ไม่อยู่ในไฟล์นี้ = module อื่นเรียกไม่ได้ การเพิ่มบรรทัดจึงเป็นการขยายสัญญา
อย่างตั้งใจ และเห็นชัดตอน review

### 3. `models/` และ `consts/` ไม่ import อะไรในโปรเจกต์เลย

มันคือ shared kernel — มีแต่ type ไม่มี behaviour และทุกคนขึ้นกับมัน
การ import ย้อนกลับคือสิ่งที่เปลี่ยน shared type ให้เป็น shared tangle

`middlewares/` ก็ห้าม import module: route ของทุก module import `middlewares`
ถ้ามัน import กลับ วงจะปิดทันที ([Middleware](./middleware.md) อธิบายทางออก)

### 4. route เขียนเต็ม middleware บรรทัดละตัว

```go [modules/note/note.http.go]
func (m *Module) Routes(e *core.Server) {
    c := &handler.NoteHandler{}

    e.GET("/notes", c.Pagination, middlewares.AuthRequire(e))
    e.GET("/notes/:id", c.Find, middlewares.AuthRequire(e))
    e.POST("/notes", c.Create, middlewares.AuthRequire(e))
}
```

path เต็ม (ไม่ใช่ group prefix) → grep เจอจาก log ได้ตรงๆ และ "route นี้ป้องกันไว้ไหม"
ตอบได้จากบรรทัดนั้นเองโดยไม่ต้องเลื่อนขึ้นไปหา group

ราคาที่จ่ายคือ route ใหม่ที่ลืมใส่ = route ที่เปิดโล่ง จึงต้องมี test ที่ไล่ชื่อทุก
route แล้ว assert ว่า anonymous เรียกไม่ได้ — ดู [Middleware](./middleware.md#testing-protected-routes)

## บังคับกฎด้วย test ไม่ใช่ด้วยความจำ

Go ห้าม import cycle ให้แล้ว ซึ่งครอบคลุมความผิดพลาดที่พังทันที แต่ไม่พูดถึงอันที่
ค่อยๆ เน่า: model เอื้อมกลับเข้า module, module แอบ import พี่น้อง, shared package
เริ่มรู้จัก feature — ทุกอันถูกกฎหมาย คอมไพล์ผ่าน และเป็นก้าวที่เปลี่ยนชุด module
กลับไปเป็นก้อนเดียวกัน

กฎพวกนี้จึงเขียนเป็น **test** รันพร้อม `make test` ไม่ต้องลงเครื่องมืออะไรเพิ่ม
และพังใน CI ที่ commit ที่ทำผิด แทนที่จะพังใน review ที่ไม่มีใครทันสังเกต:

```go
// arch/arch_test.go
cmd := exec.Command("go", "list", "-json", "./...")  // ถาม toolchain ว่าใคร import อะไร

for _, pkg := range listPackages(t) {
    for _, r := range rules {
        if !r.applies(pkg.ImportPath) { continue }
        for _, imported := range pkg.Imports {
            if r.forbids(pkg.ImportPath, imported) {
                t.Errorf("\n%s\n\n  %s\n  imports %s\n\n%s\n",
                    r.name, short(pkg.ImportPath), short(imported), indent(r.because))
            }
        }
    }
}
```

ข้อความ failure บอก **ทำไมกฎถึงมี** ไม่ใช่บอกว่ากฎว่าอะไร — คนที่เจอมันตอนตีสอง
ต้องตัดสินใจได้ว่าจะแก้ยังไง

`go list` เป็นผู้ตัดสิน ไม่ใช่การ parse source เอง เพราะมัน resolve build tag และ
generated file ให้ด้วย และดูเฉพาะ import ที่ไม่ใช่ test — test ของ module หนึ่ง
import พี่น้องได้ (มันประกอบของจริงเหมือน `cmd.Modules`) และนั่นไม่ใช่ dependency
ของ binary ที่ ship

ไฟล์เต็ม: `arch/arch_test.go` (golang-template, internal)

## เพิ่ม module ใหม่

`golang-template` มี generator ให้:

```sh
make new-module name=order
```

มันเขียนไฟล์ตามโครงข้างบนให้ แล้วพิมพ์สี่อย่างที่มันตัดสินใจแทนไม่ได้: schema,
model, error code และบรรทัดเดียวใน `cmd.Modules`

## อ่านต่อ

- [Modules](./modules.md) — ให้ module ประกาศ route/job/cron/health ของตัวเองไว้ที่เดียว แล้ว `cmd/` เสียบบรรทัดเดียว
- [Data shapes](./data-shapes.md) — request, payload, model, view, event อยู่ไฟล์ไหนและต่างกันยังไง
- [External services](./external-services.md) — module ที่ข้อมูลอยู่ที่ upstream และ module อื่นต้องใช้ด้วย
- [Lifecycle & roles](./lifecycle.md) — `cmd/` ประกอบและปิด service ยังไง
- [Middleware](./middleware.md) — ทำไม `middlewares` ถึงรับ dependency ตอน startup
- [Service errors](./service-errors.md) — `emsgs/` จัดยังไง
- [Logging practices](./logging-practices.md) — layer ไหน log อะไร
- [Testing](./testing.md) — `testkit/` และการทดสอบทั้ง service
