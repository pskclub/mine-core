# Postman Collection & OpenAPI

`postmangen` อ่าน route ที่ service ลงทะเบียนไว้จาก **source code** แล้วสร้าง
Postman collection กับ OpenAPI document ออกมา — พร้อม request body ตัวอย่าง,
query param, path variable, header auth และ example response ทั้งฝั่งสำเร็จและ
ฝั่ง error

ไม่ต้อง annotate อะไรเพิ่มในโค้ด และไม่ต้องรัน server: มันอ่าน AST ตรง ๆ จาก
convention ที่ framework บังคับอยู่แล้ว (`WithHTTPContext`, `BindWithValidate`,
`GetPageOptions`, rule chain ของ `valid`) collection จึงไม่มีวันเป็นของเก่า
ตราบใดที่ยัง regenerate

## ติดตั้ง

Go 1.24 ขึ้นไปใช้ `tool` directive ได้ — เครื่องมือถูกปักเวอร์ชันไว้ใน `go.mod`
เหมือน dependency ตัวอื่น ไม่ต้องให้ทุกคนในทีมไป `go install` เอง

```sh
go get -tool github.com/pskclub/mine-core/v2/cmd/postmangen
```

แล้วรัน:

```sh
go tool postmangen              # layout แบบ grouped (ค่าเริ่มต้น)
go tool postmangen -layout flat # หนึ่งโฟลเดอร์ต่อ resource ไม่ซ้อนชั้น
```

ค่าเริ่มต้นเขียนออกมาสองไฟล์จากการ parse ครั้งเดียว:

| ไฟล์ | เอาไปทำอะไร |
|---|---|
| `data/postman_collection.generated.json` | import เข้า Postman — `_postman_id` คงที่ การ import ทับจึงเป็นการ**อัปเดต** collection เดิม ไม่ใช่สร้างใบใหม่ทุกครั้ง |
| `data/openapi.generated.json` | OpenAPI 3.1 — เอาไป `embed` แล้ว mount เป็นหน้า [API Reference](/v2/apidocs) ให้ dev/SA กดยิงได้จากเบราว์เซอร์โดยไม่ต้อง import อะไรเลย |

เลือกเขียนอย่างเดียวได้ด้วย `-format`:

```sh
go tool postmangen -format openapi   # เขียนแค่ OpenAPI
go tool postmangen -format postman   # เขียนแค่ collection
go tool postmangen -format both      # ค่าเริ่มต้น
```

ทั้งสองไฟล์มาจาก parse รอบเดียวกันและใช้ helper ชุดเดียวกัน — field, rule, ค่า
ตัวอย่างและ example response จึงพูดตรงกันเสมอ ไม่มีทางที่ collection จะบอกอย่าง
แล้ว spec บอกอีกอย่าง

::: tip
ทุกค่าที่มันสุ่มไม่ได้ มันไม่สุ่ม — generate ซ้ำโดยที่ route ไม่เปลี่ยน ไฟล์ต้อง
ไม่ขยับแม้แต่ byte เดียว จะ commit ไฟล์นี้ไว้ใน repo แล้วให้ CI ตรวจว่ามันตรง
กับโค้ดก็ได้
:::

## มันอ่านอะไรบ้าง

| สิ่งที่เห็นในโค้ด                                             | สิ่งที่ได้ใน collection                                                                                                       |
| ---------------------------------------------------------------| -------------------------------------------------------------------------------------------------------------------------------|
| `e.GET("/notes", c.List, middlewares.AuthRequire(e))`         | request `GET /notes` พร้อม header `Authorization: Bearer {{authToken}}`                                                       |
| `g := e.Group("/users", middlewares.AuthRequire(e))`          | ทุก route ใต้ group ได้ prefix และ auth ต่อกันเป็นทอด ๆ รวมถึง group ซ้อน group                                               |
| `core.RegisterHealthRoutes(e)`                                | `GET /healthz` และ `GET /readyz` พร้อม example body ที่ core ตอบเอง — รวม `503` ของ readiness ตอน dependency ที่ critical ล่ม |
| `e.GET("/live", core.LiveHandler())`                          | probe ที่ mount เองที่ path ของตัวเอง ได้ example ชุดเดียวกัน                                                                 |
| `ctx.BindWithValidate(&req)`                                  | body/query/path ของ request สร้างจาก struct นั้น                                                                              |
| `c.QueryParam("q")` / `c.QueryParamOr("limit", "20")`         | query `q` และ `limit` — ค่า default ที่ handler เขียนไว้กลายเป็นค่าตัวอย่าง                                                   |
| `c.FormFile("file")`                                          | body เป็น `multipart/form-data` พร้อมช่องให้เลือกไฟล์                                                                         |
| `c.FormValue("is_public")`                                    | field ของฟอร์ม — มีไฟล์ด้วยเป็น multipart ไม่มีเป็น `x-www-form-urlencoded`                                                   |
| `c.Request().Header.Get("X-Api-Key")`                         | header ติดไปกับ request                                                                                                       |
| `c.Cookie("session_id")`                                      | header `Cookie: session_id=...`                                                                                               |
| `c.Param("id")`                                               | path variable `:id`                                                                                                           |
| `v.Str("email", r.Email).Required().Email()`                  | ค่าตัวอย่างเป็น `user@example.com` — มาจาก **rule** ไม่ใช่การเดา                                                              |
| `v.Str("role", r.Role).In("ADMIN", "MEMBER")`                 | ค่าตัวอย่างเป็น `ADMIN` — option แรกคือค่าที่ server รับแน่                                                                   |
| `v.Str("role", r.Role).In(consts.RoleAdmin, roleMember)`      | เหมือนกันทุกประการ — argument ที่เป็น constant/var ถูกตามไปหาค่าจริง ([ดูด้านล่าง](#rule-ท-เข-ยนด-วย-constant))                |
| `v.Str("password", r.Password).Length(minPasswordLength, 72)` | ยืดค่าให้ยาวพอตาม rule แม้ค่าต่ำสุดจะเป็น named constant                                                                      |
| `ctx.GetPageOptions()`                                        | query `page` และ `limit` ติดมาให้เอง                                                                                          |
| `return c.JSON(http.StatusOK, user)`                          | example response ที่ตาม type จริงของ `user` กลับไปถึง service ที่ผลิตมัน                                                      |
| field ที่ `Required()`                                        | example `400 Invalid parameters` ที่หน้าตาเหมือน error จริงของ framework                                                      |
| route ที่ต้อง auth                                            | example `401 UNAUTHORIZED`                                                                                                    |

เรื่อง generic ก็ตามได้: `Page[models.User]` จะถูก render เป็นหน้า pagination
ที่ `items` เป็น user จริง ๆ ไม่ใช่ `{}`

route ที่ `/auth/login` จะได้ test script ติดไปด้วย — เรียกครั้งเดียว token ถูก
เก็บลง collection variable แล้ว request ที่เหลือยิงต่อได้ทันที

## handler ที่อ่านค่าจาก context ตรง ๆ

ไม่ใช่ทุก endpoint จะ bind struct — upload, endpoint ที่รับ query ตัวเดียว หรือ
handler ที่อ่าน header เองไม่ต้องมี request struct ก็ทำงานได้ generator อ่าน
call พวกนี้ออกด้วย ไม่งั้น endpoint จะโผล่ใน collection แบบ "ไม่รับอะไรเลย"

```go
func (h NoteHandler) Upload(c core.IHTTPContext) error {
	file, err := c.FormFile("file")           // → multipart/form-data + ช่องเลือกไฟล์
	if err != nil {
		return err
	}
	_ = c.Param("id")                          // → path variable :id
	_ = c.FormValue("is_public")               // → field "is_public"
	_ = c.FormValueOr("caption", "untitled")   // → field "caption" ค่าตัวอย่าง "untitled"
	_ = c.Request().Header.Get("X-Api-Key")    // → header X-Api-Key
	...
}
```

รูปร่างของ body ตามสิ่งที่ handler ทำ:

| handler ทำอะไร | body ที่ได้ |
|---|---|
| `FormFile` / `MultipartForm` | `multipart/form-data` (Content-Type ปล่อยให้ Postman เขียนเอง เพราะ boundary มีแต่มันที่รู้) |
| `FormValue` / `FormValues` อย่างเดียว | `application/x-www-form-urlencoded` |
| bind struct เฉย ๆ | raw JSON เหมือนเดิม |

handler ที่ทั้ง bind struct **และ** รับไฟล์ — upload ที่มี metadata ติดมาด้วย —
ได้ body เดียวที่มีทั้ง field ของ struct และไฟล์ ตรงกับ request ที่ server รับ
จริง และ field จะใช้ชื่อจาก tag `form:` ก่อน `json:` เพราะฟอร์มส่งชื่อนั้น

::: warning ข้อจำกัด

- นับเฉพาะ call ที่ยิงบน**พารามิเตอร์ตัวแรกของ handler** เท่านั้น —
  `svc.Param("id")` ของ service จึงไม่ถูกนับเป็น request parameter และ
  `c.Response().Header().Get(...)` ก็ไม่นับ เพราะเป็น header ขาส่งออก
- ชื่อ parameter ต้องเป็น **string literal** — `c.QueryParam(keyVar)` อ่านไม่ได้
- `c.Get("user")` คือ store ต่อ request ของ echo ไม่ใช่ค่าจาก request จึงไม่ถูกนับ
- ค่าที่ handler เขียนไว้ใน `...Or` ถูกใช้เป็นค่าตัวอย่าง เพราะเป็นค่าที่ server
  รับแน่ ๆ อยู่แล้ว
:::

ถ้าทั้ง struct tag และ `c.QueryParam` พูดถึง parameter ตัวเดียวกัน tag ชนะ —
มันรู้ทั้ง type และ rule ของ field ส่วน call รู้แค่ชื่อ

## type alias ก็ตามได้

request ที่ bind ผ่านชื่อที่เป็น alias ของ type อื่นถูกตามไปถึงตัวจริง ทั้งสอง
รูปแบบนี้ให้ผลเหมือนกัน:

```go
type ProjectFilter = requests.ProjectFilter // alias
type ProjectFilter requests.ProjectFilter   // defined type
```

field, rule และ query parameter มาจากตัวที่ประกาศจริง — module ที่ re-export
request ของ package อื่นภายใต้ชื่อของตัวเองจึงไม่ต้อง bind ข้าม package เพื่อให้
เอกสารออกมาครบ

## Convention ที่มันยึด

ตัว generator หาสิ่งเหล่านี้:

- **route ประกาศในไฟล์ที่ลงท้าย `.http.go` หรือ `.module.go`** — ไฟล์อื่นไม่ถูกสแกนหา route
  เพื่อไม่ให้ `client.Get(...)` ของ HTTP client กลายเป็น route ไปด้วย
  ข้อยกเว้นเดียวคือ `RegisterHealthRoutes` ซึ่งอ่านจากทุกไฟล์ เพราะ probe ถูก
  ลงทะเบียนตอนประกอบ server (`cmd/api.go`) ไม่ใช่ในไฟล์ route ของ module — และ
  ชื่อนี้ไม่ใช่ method ของอย่างอื่นเหมือน `GET`
- **type ที่ถือ handler ลงท้ายด้วย `Handler` หรือ `Controller`** — ทั้งแบบ
  literal (`c := &handler.NoteHandler{}`) และแบบ constructor
  (`c := handler.NewNoteHandler(app)`)
- **middleware ที่บังคับ token ชื่อ `AuthRequire` / `AuthRole`** — เทียบชื่อแบบ
  ตรงตัว middleware ที่ปล่อย anonymous ผ่าน (`AuthOptional`) จึงไม่นับ

service ที่วางโครงตาม [Project Structure](./structure.md) ได้ครบทั้งสามข้อโดย
ไม่ต้องทำอะไร ถ้าโครงต่างจากนี้ให้ตั้งค่าเพิ่มในหัวข้อถัดไป

## ตั้งค่า

วาง `postmangen.json` ที่ root ของ repo ทุก key เป็น optional — ที่ไม่ได้เขียน
จะใช้ค่า default:

```json
{
  "name": "Wallet Service API",
  "id": "wallet-service-generated",
  "base_url": "https://api-dev.example.com",
  "output": "data/postman_collection.generated.json",
  "layout": "grouped",

  "format": "both",
  "openapi_output": "data/openapi.generated.json",
  "api_version": "1.4.0",
  "max_depth": 3,

  "route_file_suffixes": [".http.go", ".module.go"],
  "handler_suffixes": ["Handler", "Controller"],
  "auth_middlewares": ["AuthRequire", "AuthRole"],

  "auth_variable": "authToken",
  "token_captures": {
    "/auth/login": "authToken",
    "/admin/auth/login": "adminToken"
  },

  "skip_dirs": [".git", "node_modules", "vendor", "testdata"]
}
```

| key | default | ทำอะไร |
|---|---|---|
| `name` | `API Collection (Generated)` | ชื่อที่ Postman แสดง |
| `id` | ตั้งจากชื่อ module | `_postman_id` — ตัวระบุตัวตนของ collection ตอน import ทับ |
| `description` | ข้อความสั้น ๆ + layout | คำอธิบายบน collection |
| `base_url` | `http://localhost:3000` | ค่าเริ่มต้นของตัวแปร `{{baseUrl}}` |
| `output` | `data/postman_collection.generated.json` | ที่เขียนไฟล์ (relative กับ root เท่านั้น) |
| `layout` | `grouped` | `grouped` หรือ `flat` |
| `format` | `both` | เขียนอะไรบ้าง — `postman`, `openapi` หรือ `both` |
| `openapi_output` | `data/openapi.generated.json` | ที่เขียน OpenAPI document (relative กับ root เท่านั้น) |
| `api_version` | `0.0.0` | `info.version` ของ OpenAPI — เป็นเวอร์ชันของ **API** ไม่ใช่ของเครื่องมือ |
| `max_depth` | `3` | body ที่ render ออกมาซ้อน object ได้ลึกกี่ชั้น (ดู [ขนาดไฟล์](#ขนาดไฟล์-กับ-max-depth)) |
| `route_file_suffixes` | `[".http.go", ".module.go"]` | ไฟล์ที่ถูกสแกนหา route (ตัวหลังคือเมธอด `Routes` ของ [module](./modules.md)) |
| `route_file_suffix` | — | รูปแบบเดิมที่ระบุได้ค่าเดียว ตั้งแล้วจะ**แทนที่** list ข้างบนทั้งหมด |
| `handler_suffixes` | `["Handler", "Controller"]` | ชื่อ type ที่ถือ handler |
| `auth_middlewares` | `AuthRequire`, `AuthRole` + ชื่อเก่าอีก 4 ตัว | middleware ที่แปลว่า "route นี้ต้องมี token" |
| `auth_variable` | `authToken` | ตัวแปรที่ถูกส่งเป็น bearer token |
| `token_captures` | `{"/auth/login": "authToken"}` | route ที่ล็อกอินแล้วเก็บ token ลงตัวแปรไหน |
| `skip_dirs` | `.git`, `.agent(s)`, `node_modules`, `vendor`, `testdata` | ไดเรกทอรีที่ไม่เดินเข้าไป (ไดเรกทอรีขึ้นต้นด้วย `.` ถูกข้ามอยู่แล้ว) |

flag ชนะไฟล์ ไฟล์ชนะ default — งานครั้งเดียวจึงไม่ต้องไปแก้ไฟล์:

```sh
go tool postmangen -config config/postmangen.json
go tool postmangen -layout flat -out data/postman_flat.json
go tool postmangen -name "Wallet API (staging)" -base-url https://api-staging.example.com
go tool postmangen -format openapi -openapi-out api/openapi.json -api-version 1.4.0
```

## OpenAPI ได้อะไรเพิ่มจาก collection

collection เก็บ "ค่าตัวอย่างหนึ่งค่า" ส่วน spec เก็บ **rule** ด้วย — rule chain
ของ `valid` ที่เขียนไว้ใน Go จึงเดินทางไปถึง schema:

| เขียนใน `Valid(ctx)` | ได้ใน schema |
|---|---|
| `.Required()` | ชื่อ field อยู่ใน `required` |
| `.Email()` / `.UUID()` / `.URL()` / `.ISO8601()` | `format: email` / `uuid` / `uri` / `date-time` |
| `.Length(8, 72)` บน `Str` | `minLength: 8`, `maxLength: 72` |
| `.Min(1).Max(99)` บน `Int` | `minimum: 1`, `maximum: 99` |
| `.Min(1)` บน `Arr` | `minItems: 1` |
| `.In("ACTIVE", "INACTIVE")` | `enum` — และเป็น enum ชนิดเดียวกับ field เสมอ |
| route ที่ผ่าน `AuthRequire` | `security: [{bearerAuth: []}]` + response `401` |

### rule ที่เขียนด้วย constant

argument ของ rule ไม่จำเป็นต้องเป็น literal — generator ตามไปหาค่าจริงให้:

```go
// consts/role.go
type Role string

const (
    RoleAdmin  = "ADMIN"
    RoleMember = Role("MEMBER")     // เขียนเป็น conversion ก็ได้
)

// modules/user/handler/user.request.go
v.Str("role", r.Role).Required().In(consts.RoleAdmin, string(consts.RoleMember))
v.Int("priority", r.Priority).In(consts.PriorityLow, consts.PriorityHigh)
```

ได้ `enum: ["ADMIN", "MEMBER"]` และ `enum: [1, 9]` เท่ากับเขียน literal ทุกประการ
รวมถึง `Prefix` / `Suffix` / `Contains` และค่าตัวอย่างใน collection ด้วย

รองรับทั้ง **const และ package-level var**, ทั้งในแพ็กเกจเดียวกันและข้ามแพ็กเกจ
(`consts.RoleAdmin`) — เพราะการประกาศค่าที่อนุญาตไว้ที่เดียวแล้วอ้างถึงคือวิธีที่
เขียนกันจริง: handler ก็ `switch` กับ constant ชุดเดียวกัน และ literal ที่พิมพ์ซ้ำ
ใน validator คือ literal ที่จะเพี้ยนจากอันที่ handler เช็ค

::: warning ค่าที่อ่านตอน compile ไม่ได้ จะถูกข้าม
argument ที่เป็นผลของ**ฟังก์ชัน** (`In(allowedRoles()...)`) อ่านไม่ได้จาก source
จึงถูกตัดออกจาก enum ไม่ใช่ใส่เป็นค่าว่าง — option ที่เหลืออ่านได้ยังออกมาครบ
เพราะ "รู้บางค่า" ดีกว่า "ไม่รู้เลย" แต่ enum ที่ได้จะไม่ครบ ให้ประกาศเป็น const
ถ้าอยากให้ขึ้นครบ
:::

การจัดกลุ่มก็มาจากที่เดียวกับ folder ของ collection: tag หนึ่งตัวต่อกลุ่ม และ
`x-tagGroups` เป็นชั้นบนสุด ให้ sidebar ของ
[API Reference](/v2/apidocs#folder-sub-group) มี folder เหมือนกัน — ต่างกันตรง
Postman ซ้อน folder ได้ไม่จำกัด ส่วน `x-tagGroups` ได้แค่สองชั้น path ที่ลึกกว่า
นั้นจึงถูกยุบเป็นชื่อ tag เดียว (`sessions / active`)

`servers` มีสองรายการ โดยรายการแรกเป็น `/` เสมอ — ตัว API Reference ที่ mount
อยู่ในตัว service จะยิง request กลับไปที่ host ที่เสิร์ฟหน้านั้นเอง ไม่ว่าจะเป็น
laptop, staging หรือ port-forward ส่วนรายการที่สองคือ `base_url` สำหรับคนที่
export ไฟล์ออกไปใช้ที่อื่น

## สิ่งที่ตั้งค่าไม่ได้ — และทำไม

`WithHTTPContext`, `Bind` / `BindWithValidate`, `GetPageOptions`,
`RegisterHealthRoutes` (รวมถึง path ทั้งสองที่มันตั้งให้), rule chain ของ
package `valid` และรูปร่างของ error body (`{code, message, fields}`) เป็น
contract ของ framework เอง ไม่ใช่ของ project — เปลี่ยนได้เมื่อไหร่ collection ก็
ไม่ได้อธิบาย server ตัวจริงอีกต่อไป ทั้งหมดนี้จึงตายตัว และเป็นเหตุผลที่
เครื่องมือนี้อยู่ใน core: เมื่อ core ขยับ contract ตัวมันขยับตาม แล้วทุก service
ได้ของที่แก้แล้วจากการ bump version ครั้งเดียว

## ต่อกับ Makefile

```makefile
postman:
	go tool postmangen

postman-flat:
	go tool postmangen -layout flat
```

ถ้า service mount [API Reference](/v2/apidocs) ไว้ ให้ generate ก่อน build เสมอ
— หน้าเว็บอ่านจากไฟล์ที่ `embed` เข้าไป ไฟล์เก่าแปลว่าหน้าเว็บอธิบาย API ของ
เมื่อเดือนที่แล้วโดยไม่มีอะไรบอก

## เมื่อ route หายไปจาก collection

ไล่ตามลำดับนี้:

1. **ไฟล์ลงท้าย `.http.go` หรือ `.module.go` หรือยัง** — ถ้าโครงเรียกอย่างอื่น ตั้ง
   `route_file_suffixes`
2. **path เป็น string literal หรือเปล่า** — path ที่ประกอบจากตัวแปรอ่านไม่ได้
   ตอน compile จึงอ่านไม่ได้ตอนนี้เช่นกัน
3. **handler ชื่อลงท้ายถูกไหม** — `NoteAPI` ไม่เข้าเกณฑ์ ใส่เพิ่มใน
   `handler_suffixes`
4. **ไฟล์ parse ผ่านไหม** — ไฟล์ที่ parse ไม่ผ่านจะถูกข้ามพร้อม warning ที่
   stderr เสีย route ของตัวเองอย่างเดียว ไม่ล้มทั้ง repo

## relation ที่ไม่ได้ preload จะไม่โผล่ใน example

field ที่ tag ว่า `omitempty` และเป็น **slice / map / pointer ไปหา struct** ถูก
ตัดออกจาก example เพราะ `encoding/json` ไม่ส่ง key นั้นเลยเมื่อมันว่าง — endpoint
ที่ไม่ได้ `Preload` มาจึงไม่มี field นั้นใน response จริง ๆ

```go
type DataUser struct {
	BaseModelHardDelete
	Username string `json:"username"`
	Role     string `json:"role"`

	// Relations
	UserTokens []DataUserToken  `json:"user_tokens,omitempty"`
	APILogs    []DataUserAPILog `json:"api_logs,omitempty"`
}
```

`POST /data-auth/login` ที่ตอบ struct ซึ่ง embed `*DataUser` ตัวนี้ จะได้ example
ที่มีแค่ `username`, `role`, `token` ฯลฯ — ไม่มี `user_tokens` และ `api_logs`
ตรงกับที่ server ส่งจริง ก่อนหน้านี้มันโชว์ array ที่มีข้อมูลเต็ม ซึ่งเป็น
response ที่ไม่มีวันเกิดขึ้น

กฎนี้ตามพฤติกรรมของ `encoding/json` เป๊ะ ๆ ไม่ได้เดา:

| field | `omitempty` ทำอะไร | ใน example |
|---|---|---|
| `[]T` / `map[K]V` / `*Struct` | หายไปทั้ง key เมื่อว่าง | **ตัดออก** |
| `*string` / `*int` | หายเมื่อ nil — แต่มักเป็นค่าที่ handler ตั้งจริง | เก็บไว้ |
| `string` / `int` / `bool` | หายเมื่อเป็น zero — แต่มักมีค่า | เก็บไว้ |
| `Struct` (ไม่ใช่ pointer) | **ไม่ทำอะไรเลย** — encoding/json ไม่ตัด struct | เก็บไว้ |

::: tip ผลข้างเคียง: ไฟล์เล็กลงมาก
relation คือทั้งตัวที่ทำให้ example ผิด **และ**ตัวที่ทำให้ไฟล์ระเบิด — service
จริงตัวหนึ่งลดจาก 6.7 MB เหลือ 856 KB (จาก 425 MB เดิมคือ 497 เท่า) โดย
**แม่นยำขึ้น** ไม่ใช่แลกมาด้วยการตัดข้อมูลทิ้ง
:::

ถ้าอยากให้ relation ไหนโผล่ใน example ให้เอา `omitempty` ออกจาก tag ของมัน —
ซึ่งก็ถูกต้องอยู่แล้ว เพราะ endpoint ที่ preload มาจริงย่อมส่ง key นั้นเสมอ

## ขนาดไฟล์ กับ max_depth

`max_depth` คุมว่า body ที่ render ออกมาซ้อน object ได้ลึกกี่ชั้น ค่าเริ่มต้นคือ
`3` และมันเป็นค่าที่ตัดสินว่าไฟล์ที่ได้ใช้งานได้หรือไม่

การไม่กางซ้ำ type ที่อยู่บน path เดิมกัน**วนไม่สิ้นสุด**ได้ แต่ไม่กัน**การโต**:
model ที่ชี้หากันเป็นวง — province ถือ districts, district ถือ province ของมัน,
address ถือทั้งคู่ — ไม่มี path ไหนซ้ำ type เลยสักเส้น แต่จำนวน path ที่ไม่ซ้ำนั้น
โตตาม graph ไม่ใช่ตามความลึก และทุกเส้นถูกเขียนออกมาหมด

ผลจาก service จริงตัวหนึ่ง (184 endpoint, model แบบ location เต็มไปหมด):

| `max_depth` | OpenAPI | บรรทัด |
|---|---|---|
| ไม่จำกัด (ก่อนแก้) | 425 MB | 5,503,556 |
| 5 | 27.9 MB | 534,565 |
| 4 | 14.2 MB | 295,779 |
| **3** (ค่าเริ่มต้น) | 6.7 MB | 153,199 |
| 2 | 2.9 MB | 76,249 |

**ขนาดโตประมาณเท่าตัวต่อหนึ่งชั้น** — ถ้าไฟล์ใหญ่จนเบราว์เซอร์อืด ลด 1 ชั้นได้ผล
มากกว่าทำอย่างอื่นทั้งหมดรวมกัน กลับกันถ้า service มี DTO แบน ๆ ไม่ชี้หากัน จะเพิ่ม
เป็น 5–6 ก็ได้ ไม่มีอะไรระเบิด

```sh
go tool postmangen -max-depth 2
```

::: tip เทียบจำนวนบรรทัดระหว่างสองไฟล์ไม่ได้
collection ของ Postman เก็บ body เป็น **string** — ทั้ง body ที่มี newline อยู่
ข้างในถูก escape เป็น `\n` กลายเป็นบรรทัดเดียวในไฟล์ ส่วน OpenAPI เก็บเป็น JSON
จริงซึ่งกางออกบรรทัดละ key

body ก้อนเดียวกันจึงนับได้ 1 บรรทัดในฝั่ง Postman และ 10,000 บรรทัดในฝั่ง OpenAPI
ทั้งที่ข้อมูลเท่ากัน — ถ้าจะเทียบ ให้ดู**ขนาดไฟล์** ไม่ใช่จำนวนบรรทัด
:::

## เมื่อ example response เป็น `{}`

`{}` แปลว่ามันตาม type ปลายทางไม่เจอ ไม่ได้แปลว่า endpoint ตอบ object เปล่า —
และในหน้า [API Reference](/v2/apidocs) มันจะโผล่เป็น schema `object` ที่ไม่มี
field เลย ซึ่งอ่านผิดได้ง่ายกว่าใน Postman มาก

รูปแบบที่ตามได้แล้ว:

```go
// เรียกต่อกันในบรรทัดเดียว
res, err := services.NewUserService(c).Pagination(opts)

// เก็บใส่ตัวแปรก่อน — รูปแบบที่ handler ส่วนใหญ่เขียนกันจริง
svc := services.NewUserService(c)
res, err := svc.Pagination(opts)

// service เป็น field ของ controller (DI) — ตามผ่าน type ของ field
// ซึ่งเป็น interface ก็ได้
func (h ProfileController) Me(c core.IHTTPContext) error {
	user, err := h.users.Find(id)
	...
}
```

ที่ยังตามไม่ได้ และเป็นเหตุผลที่ยังเจอ `{}` อยู่:

- response ที่ประกาศ type ปลายทางเป็น `any` / `interface{}` — ไม่มีอะไรให้อ่าน
- ค่าที่มาจาก parameter ของ handler หรือจากตัวแปรที่ประกาศไว้นอก function
- chain ที่ลึกเกิน 8 ชั้น (กันไม่ให้ type ที่อ้างวนกันวนไม่รู้จบ)

ส่วน `{}` ที่โผล่**ข้างใน** body ที่ซ้อนกันหลายชั้น เป็นคนละเรื่อง — นั่นคือ
[`max_depth`](#ขนาดไฟล์-กับ-max-depth) ตัดให้ ไม่ใช่ตามไม่เจอ และ field ที่
**หายไปทั้ง key** ก็คนละเรื่องอีก — ดู
[relation ที่ไม่ได้ preload](#relation-ที่ไม่ได้-preload-จะไม่โผล่ใน-example)

ทางแก้ที่ตรงที่สุดคือให้ method ที่ handler เรียกประกาศ return type ที่เป็น
struct จริง ๆ แทน `any` — ได้ทั้ง documentation และ type safety ในที่เดียว
