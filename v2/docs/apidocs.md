# API Reference (Scalar)

`apidocs` เสิร์ฟ **เอกสาร API ของ service เอง** เป็นหน้าเว็บที่กดยิง request ได้
ทันที ที่ `/_docs` — ใช้ [Scalar](https://scalar.com) เป็นตัว render

มันมีเพราะ collection ที่ [postmangen](./postman.md) สร้างให้จะมีประโยชน์ก็ต่อ
เมื่อมีคน import: ต้อง regenerate → หาไฟล์ → โหลด → import เข้า client — และคน
ที่ต้องใช้มากที่สุด (SA ที่กำลังเช็คว่า staging ทำตาม ticket ไหม) คือคนที่มีโอกาส
ทำครบสี่ขั้นน้อยที่สุด หน้าเว็บที่ URL เดียวไม่ต้องทำอะไรเลยสักขั้น และเป็น API
ของ process ที่รันอยู่จริงเสมอ

```
GET /_docs           → หน้า API reference
GET /_docs/openapi   → ตัว OpenAPI document ดิบ ๆ (ให้ tool อื่นดูด)
```

## เริ่มใช้

สองขั้น: generate spec แล้ว mount

```sh
go tool postmangen        # เขียน data/openapi.generated.json
```

```go
package main

import (
	_ "embed"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/apidocs"
)

//go:embed data/openapi.generated.json
var apiSpec []byte

func main() {
	app, _ := core.NewApp(env)
	srv := core.NewHTTPServer(app, nil)

	if err := apidocs.Mount(srv, apidocs.Options{Spec: apiSpec}); err != nil {
		panic(err)
	}
	// ... route ของ service
}
```

`embed` เป็นทางที่ควรใช้ — binary พก reference ของโค้ดที่มันถูก build มาพอดี
ไม่มีไฟล์ให้ลืม deploy ถ้าอยากวางไฟล์ไว้ข้าง binary ใช้ `Options.SpecFile` แทน
(อ่านตอน mount ครั้งเดียว ไฟล์หายจะพังตอน boot ไม่ใช่ตอนมีคนเปิดหน้าเว็บ)

::: tip
ไฟล์ที่ `embed` ต้องมีอยู่ตอน compile — commit `data/openapi.generated.json` ลง
repo แล้วให้ CI ตรวจว่ามันตรงกับโค้ด (generate ซ้ำแล้ว `git diff --exit-code`)
:::

## ทำไมมันยิงถูก host

`servers` ตัวแรกของ spec ที่ postmangen สร้างคือ `/` — request จากหน้าเว็บจึงวิ่ง
กลับไปหา host ที่เสิร์ฟหน้านั้นเอง เปิดจาก `localhost:3000` ก็ยิง localhost เปิด
จาก staging ก็ยิง staging เปิดผ่าน `kubectl port-forward` ก็ยิงผ่าน tunnel นั้น
โดยไม่ต้องตั้งอะไร

ถ้า spec มาจากที่อื่นและระบุ host ไว้ผิด ให้ทับด้วย `Options.Servers`:

```go
apidocs.Mount(srv, apidocs.Options{
	Spec: apiSpec,
	Servers: []apidocs.Server{
		{URL: "/", Description: "เครื่องนี้"},
		{URL: "https://api-staging.example.com", Description: "Staging"},
	},
})
```

## หน้าตาเทียบกับ Postman

ค่าเริ่มต้นของหน้านี้จัด sidebar ให้อ่านเหมือน **รายการ request** ไม่ใช่สารบัญ:

- `defaultOpenAllTags` — ทุก endpoint โผล่ครบตั้งแต่แรก ไม่ต้องกดเปิด group ก่อน
- `hideModels` — ไม่เอา schema มาปนใน sidebar เหลือแต่สิ่งที่กดยิงได้

ถ้าหน้านี้ทำไว้ให้ **คนอ่าน** มากกว่าคนยิง (เช่นส่งให้ทีมที่มาเชื่อมต่อ) ให้เปิด
`Reference: true` กลับไปเป็น layout เอกสารมาตรฐาน — tag ปิด, models ขึ้นครบ

```go
apidocs.Mount(srv, apidocs.Options{Spec: apiSpec, Reference: true})
```

### folder / sub-group

sidebar จัดกลุ่มจาก path เดียวกับที่ Postman ใช้จัด folder ผ่าน `x-tagGroups`
— endpoint ที่ลึกกว่าหนึ่งชั้นใต้ resource จะแยกออกไปเป็นกลุ่มของตัวเอง:

```
[+] user
   [+] sessions / active        ← GET /users/:id/sessions/active
   [+] user                     ← endpoint ที่อยู่ตรง ๆ ใต้ /users
```

::: warning ลึกได้ 2 ชั้น ไม่เท่า Postman
`x-tagGroups` ให้ **group → tag** เท่านั้น ซ้อนต่อไม่ได้ Postman ที่ nest ได้
เรื่อย ๆ อย่าง `user → sessions → active` ในหน้านี้จึงยุบเป็น tag เดียวชื่อ
`sessions / active` ใต้ group `user`

(`tags.parent` ของ OpenAPI 3.2 จะ nest จริงได้ แต่ยังไม่มี renderer ตัวไหน
implement รวมถึง Scalar — [feature request](https://github.com/scalar/scalar/discussions/6866)
ยังเปิดอยู่)
:::

endpoint ที่อยู่ใต้ resource ตรง ๆ ได้ tag ชื่อเดียวกับ group เพราะ group ใน
`x-tagGroups` บรรจุได้แค่ tag ไม่ใช่ operation — และ **tag ที่ไม่อยู่ใน group ไหน
เลยจะไม่ถูกแสดง** ทั้งหมด ทุก tag จึงต้องมี group เสมอ

ถ้าไม่มี route ไหนลึกพอ (service ที่ path ตื้นทั้งหมด) จะไม่มี `x-tagGroups`
ออกมาเลย — ไม่ต้องมาเสียชั้นซ้อนฟรี ๆ โดยไม่ได้อะไร

::: warning ยังไม่ใช่ Postman เต็มตัว
Scalar มีสองตัว: **API Reference** (ตัวที่ฝังอยู่นี้ — เอกสารเป็นหลัก กด "Test
Request" แล้วค่อยยิง) กับ **API Client** ซึ่งเป็น UI แบบ Postman จริง ๆ

ตัวหลังฝังไม่ได้ — `@scalar/api-client` เป็น ESM-only ไม่มี browser global
(`package.json` ไม่มี `main`/`browser`/`unpkg`) จะใช้ต้องมี import map + Vue +
CSS แยก ซึ่งคือ node build step ที่ package นี้ตั้งใจเลี่ยงตั้งแต่ต้น และ API
ของมันยังขยับอยู่ (v3 ย้าย config ไป `createWorkspaceStore` คนละ package)

ถ้าต้องการ workflow แบบ Postman จริง ๆ ไฟล์ที่ต้องใช้มีอยู่แล้ว:
`data/postman_collection.generated.json` จาก [postmangen](./postman.md) — ตัวนี้
generate มาจาก parse รอบเดียวกัน อธิบาย API ตัวเดียวกัน
:::

## ตั้งค่า Scalar ที่ไม่มีใน Options

`scalar-go` ตั้งชื่อ option ไว้ทีละตัว และ Scalar ออก setting ใหม่เร็วกว่าที่
wrapper จะตามทัน — `ScalarConfig` เขียนลง config map ตรง ๆ ด้วยชื่อ key ของ
Scalar เอง

```go
import scalargo "github.com/bdpiprava/scalar-go"

apidocs.Mount(srv, apidocs.Options{
	Spec: apiSpec,
	Scalar: []scalargo.Option{
		apidocs.ScalarConfig("hideModels", false),   // เอา models กลับมา
		apidocs.ScalarConfig("hideSearch", true),
		scalargo.WithTheme(scalargo.ThemeKepler),
	},
})
```

`Options.Scalar` ถูก apply หลังสุด จึงทับค่าที่ `Mount` ตั้งไว้ได้ทุกตัว — key
เป็นของ Scalar ไม่ใช่ของ package นี้ สะกดผิดหน้าเว็บจะเมินเฉย ๆ ไม่มี error

## ความปลอดภัย

หน้านี้บอก **ทุก endpoint ทุก field และทุก rule** ของ service — เป็นแผนที่ของ
attack surface ที่คนสร้างระบบเขียนเอง `Mount` จึง**ปฏิเสธ**ถ้า `APP_ENV` ไม่ใช่
`dev` และไม่มี guard:

```
apidocs: refusing to mount at /_docs with APP_ENV=production and no guard —
set APP_APIDOCS_PASSWORD, or pass Options.BasicAuth or Options.Auth
```

มันล้มตอน boot ไม่ใช่ตอนมี request เพราะ failure mode ของเรื่องนี้เงียบ: ไม่มี
อะไรพัง แค่มีคนอ่านได้เฉย ๆ

### Basic auth ด้วย env ตัวเดียว

ทางที่สั้นที่สุดสำหรับ staging — ไม่ต้องแก้โค้ด:

```sh
APP_APIDOCS_PASSWORD=s3cret   # user เริ่มต้นคือ apidocs
APP_APIDOCS_USER=sa           # จะเปลี่ยนก็ได้
```

หรือเขียนในโค้ด:

```go
apidocs.Mount(srv, apidocs.Options{
	Spec:      apiSpec,
	BasicAuth: &apidocs.BasicAuth{User: "sa", Password: os.Getenv("DOCS_PASSWORD")},
})
```

::: warning
`BasicAuth` ที่มี `User` แต่ไม่มี `Password` คือกุญแจที่ล็อกทุกคนรวมถึงคนตั้งเอง
`Mount` จึงปฏิเสธด้วย `APIDOCS_NO_PASSWORD` แทนที่จะแกล้งทำเป็นล็อก
:::

### Middleware ของ service เอง

`Options.Auth` รับ echo middleware ธรรมดา — guard ที่ใช้กับ admin area อยู่แล้ว
ใช้ที่นี่ได้เลย และประกอบกับ `BasicAuth` ได้ (basic ทำงานก่อน เพื่อไม่ให้รหัสผิด
วิ่งไปถึง guard ที่ต้อง query database)

```go
apidocs.Mount(srv, apidocs.Options{
	Spec: apiSpec,
	Auth: []echo.MiddlewareFunc{middlewares.AdminOnly(app)},
})
```

รหัสของ `apidocs` แยกจากของ [devtools](/v2/devtools) โดยตั้งใจ — คนที่ควรอ่าน
เอกสาร API กับคนที่ควรอ่าน config ของ process เป็นคนละกลุ่ม รหัสเดียวกันจะทำให้
เป็นกลุ่มเดียวกัน

## Options

| field | default | ทำอะไร |
|---|---|---|
| `Prefix` | `/_docs` | path ที่ mount |
| `Spec` | — | ตัว document (JSON หรือ YAML) — ต้องมีอย่างใดอย่างหนึ่งกับ `SpecFile` |
| `SpecFile` | — | path ที่อ่านตอน mount ครั้งเดียว |
| `Title` | `info.title` ของ spec | ชื่อบน tab และหัวหน้า |
| `Servers` | ตาม spec | ทับ `servers` ของ document |
| `BasicAuth` | env `APP_APIDOCS_PASSWORD` | username/password ที่เบราว์เซอร์ถาม |
| `Auth` | — | echo middleware ที่คุมทุก route ของ mount นี้ |
| `CDN` | jsdelivr | ที่โหลด Scalar bundle |
| `DarkMode` | `false` | เริ่มที่โหมดมืด (ผู้อ่านสลับเองได้) |
| `Reference` | `false` | เปลี่ยนเป็น layout เอกสาร แทน layout รายการ request |
| `Scalar` | — | option ของ `scalar-go` ตรง ๆ — theme, layout, ซ่อน client, เติม token ไว้ล่วงหน้า |

## เรื่อง CDN

ตัว page โหลด Scalar bundle จาก CDN — ต่างจาก [devtools](/v2/devtools) ที่ฝัง UI
ไว้ในไฟล์เดียวทั้งหมด เพราะ bundle ของ Scalar เป็น JS หลายเมกะไบต์ที่ build ด้วย
node ซึ่งเอาเข้ามาใน Go library ไม่ได้โดยไม่ทำให้ทุกคนต้องมี toolchain ของ node

บนเครือข่ายที่ไม่มี egress หน้าเว็บจะโหลดไม่ขึ้น — มันจะขึ้นข้อความบอกว่าเกิด
อะไรขึ้น พร้อมลิงก์ไป `/_docs/openapi` ซึ่ง process นี้เสิร์ฟเอง เข้าถึงได้เสมอ
ถ้าต้องใช้จริงจังในเครือข่ายแบบนั้น ให้ host bundle เองแล้วชี้:

```go
apidocs.Mount(srv, apidocs.Options{
	Spec: apiSpec,
	CDN:  "https://assets.internal/scalar/api-reference.js",
})
```

## เอา document ไปใช้ที่อื่น

`GET /_docs/openapi` เสิร์ฟไฟล์ดิบตามที่ใส่เข้าไป ไม่แก้อะไร — เอาไป generate
client, import เข้า editor ของใครสักคน หรือ diff กับสิ่งที่ deploy ก่อนหน้า ได้
ตรง ๆ (route นี้อยู่หลัง guard เดียวกับหน้าเว็บ เพราะเป็นความลับก้อนเดียวกัน)

```sh
curl -u apidocs:s3cret https://api-staging.example.com/_docs/openapi > openapi.json
```

## ใช้คู่กับ devtools

สองอย่างนี้ตอบคนละคำถาม และ mount แยกกันได้:

| | ตอบคำถาม | ใครใช้ |
|---|---|---|
| [devtools](/v2/devtools) `/_dev` | process นี้ต่ออะไรอยู่ / route ไหนมีใครถือ / job รันไปถึงไหน | dev, on-call |
| `apidocs` `/_docs` | API รับอะไร ตอบอะไร และยิงดูเลยได้ไหม | dev, SA, ทีมที่มาเชื่อมต่อ |

```go
devtools.Mount(srv, devtools.Options{Runner: runner})
apidocs.Mount(srv, apidocs.Options{Spec: apiSpec})
```

## เมื่อหน้าเว็บว่างเปล่า

1. **ขึ้นข้อความว่าโหลด bundle ไม่ได้** — เครือข่ายไม่ถึง CDN ดูหัวข้อ
   [เรื่อง CDN](#เรื่อง-cdn)
2. **`Mount` คืน `APIDOCS_RENDER`** — document parse ไม่ผ่าน มันล้มตอน boot
   เพราะหน้าที่ render ว่างอ่านได้ว่า "service นี้ไม่มี endpoint เลย"
3. **หน้าขึ้นแต่ endpoint ไม่ครบ** — เป็นเรื่องของ generator ไม่ใช่ของหน้านี้ ดู
   [เมื่อ route หายไปจาก collection](./postman.md#เมื่อ-route-หายไปจาก-collection)
4. **endpoint เก่า** — ไฟล์ที่ `embed` ยังไม่ได้ generate ใหม่
5. **example ของ 200 เป็น object เปล่า** — generator ตาม type ที่ handler ตอบ
   ไม่เจอ ดู [เมื่อ example response เป็น `{}`](./postman.md#เมื่อ-example-response-เป็น)
6. **หน้าอืดมาก / เบราว์เซอร์ค้าง** — ไฟล์ spec ใหญ่เกินไป ลด
   [`max_depth`](./postman.md#ขนาดไฟล์-กับ-max-depth) ลงหนึ่งชั้นแล้ว generate ใหม่
   ขนาดจะลดลงราวครึ่งหนึ่ง
