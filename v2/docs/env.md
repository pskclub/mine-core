# Configuration (ENV)

mine-core v2 อ่าน config ผ่าน `core.IENV` — ชื่อและ method เหมือน v1 แต่ข้างในเป็น
[koanf](https://github.com/knadh/koanf) แบบ **instance** (ไม่มี global viper) และ
**ไม่ต้อง maintain รายการ key ซ้ำ** อีกต่อไป (v1 ต้องแก้ทั้ง `ENVConfig` และ `envKeys`)

## โหลด config

```go
import core "github.com/pskclub/mine-core/v2"

env, err := core.NewEnv()          // อ่านจาก ./.env (หรือ ./test.env เมื่อ APP_ENV=test)
if err != nil {
    log.Fatal(err)
}

env, err := core.NewEnvPath("./config")   // ระบุ directory เอง
```

ทั้งสองฟังก์ชันคืน `core.IError` และ **fail fast** — config ผิดรู้ตั้งแต่ boot
ไม่ใช่ตอน request แรก

### ลำดับความสำคัญ

ทีหลังทับก่อนหน้า:

1. ไฟล์ `.env` (หรือ `test.env`) — **ไม่บังคับ** ไม่มีไฟล์ก็ทำงานได้
2. environment variables ที่ขึ้นต้นด้วย `APP_`

### กติกาการตั้งชื่อ key (จุดที่พลาดกันบ่อย)

| ที่ตั้ง | เขียนยังไง | กลายเป็น key |
|---|---|---|
| OS environment | `APP_DB_HOST=localhost` | `db_host` |
| ไฟล์ `.env` | `DB_HOST=localhost` | `db_host` |

- **OS env ต้องมี prefix `APP_`** — ตัวที่ไม่มี prefix จะถูกมองข้ามทั้งหมด
  (กัน env var ของระบบอย่าง `HOST`, `PATH` มาชนกับ config ของเรา)
- **ในไฟล์ `.env` ต้อง _ไม่_ มี prefix** — ถ้าเขียน `APP_DB_HOST=...` ในไฟล์
  จะได้ key `app_db_host` ซึ่งไม่ผูกกับ field ไหนเลย และ **ไม่มี error แจ้ง**
- key ถูกแปลงเป็นตัวพิมพ์เล็กเสมอ — `DB_HOST`, `db_host`, `Db_Host` ค่าเท่ากัน

### รูปแบบไฟล์ `.env`

รองรับไวยากรณ์ dotenv มาตรฐาน (ผ่าน `godotenv`):

```sh
# คอมเมนต์ได้
ENV=dev
SERVICE=payment-api

DB_PASSWORD="ค่าที่มี ช่องว่าง ใส่ quote"
DB_CONNECTION_STRING='postgres://u:p@localhost:5432/app?sslmode=disable'

export CACHE_HOST=localhost      # ใส่ export นำหน้าได้ (ถูกตัดทิ้ง)

FIREBASE_CREDENTIAL="{
  \"type\": \"service_account\"
}"                                # multi-line ต้องอยู่ใน double quote
```

### `APP_ENV` เลือกไฟล์ที่จะอ่าน

`APP_ENV=test` → อ่าน `test.env` แทน `.env`

> ⚠️ ค่านี้ถูกอ่านจาก **OS environment เท่านั้น** — ใส่ `ENV=test` ในไฟล์ `.env`
> ไม่ทำให้สลับไปอ่าน `test.env` (ตอนนั้นยังไม่ได้อ่านไฟล์เลย) ตอนรัน test ให้ใช้
> `APP_ENV=test go test ./...` หรือ `t.Setenv("APP_ENV", "test")`

ค่าที่ยอมรับคือ `dev` / `test` / `mock` / `prod` หรือปล่อยว่าง —
นอกจากนี้ `NewEnv` คืน error `INVALID_CONFIG` ทันที

---

# อ้างอิง key ทั้งหมด

ตารางข้างล่างใช้ชื่อแบบที่เขียนใน `.env` (เติม `APP_` นำหน้าเมื่อตั้งผ่าน OS env)
คอลัมน์ default คือค่าที่ framework ใช้เมื่อ **ไม่ได้ตั้ง**

## Application

| Key | ชนิด | Default | คำอธิบาย |
|---|---|---|---|
| `ENV` | string | *(ว่าง)* | `dev` / `test` / `mock` / `prod` มีผลต่อพฤติกรรมหลายอย่าง (ดูตารางถัดไป) ค่าอื่นทำให้ boot ไม่ผ่าน |
| `SERVICE` | string | *(ว่าง)* | ชื่อ service ใช้เป็น tag `service` ในทุก Sentry event ควรตั้งเสมอเมื่อมีหลาย service ส่งเข้า Sentry เดียวกัน |
| `HOST` | string | `:8080` | address ที่ `StartHTTPServer` bind — รูปแบบ `host:port` เช่น `:3000` หรือ `127.0.0.1:3000` |

### `ENV` เปลี่ยนอะไรบ้าง

| พฤติกรรม | `dev` | อื่นๆ |
|---|---|---|
| `message` ใน error response | เปิดเผยข้อความจริงจาก **root cause** (ข้อความของ driver/library ตัวจริง ไม่ใช่ป้ายของ wrapper) รวมถึงข้อความ panic | ใช้ message ของ error type เท่านั้น (ไม่รั่วรายละเอียดภายใน) |
| `StartHTTPServer` | block ตรงๆ ปิดทันทีเมื่อ Ctrl-C | รอ signal แล้ว graceful shutdown 10 วินาที |
| ไฟล์ config | `.env` | `test.env` เมื่อ `APP_ENV=test` |
| Sentry environment | `dev` | ใช้ค่า `ENV` (ถ้าไม่ได้ตั้ง `SENTRY_ENVIRONMENT`) |

## Logging

| Key | ชนิด | Default | คำอธิบาย |
|---|---|---|---|
| `LOG_LEVEL` | string | `info` | `debug` / `info` / `warn` / `error` — ค่าที่ไม่รู้จักถือเป็น `info` **`debug` เปิด log ของ SQL ทุก statement ด้วย** — แยกออกจากกันได้ด้วย `DB_LOG_LEVEL` (ดู [Query logging](./database-logging.md)) |
| `LOG_REQUEST` | bool | `true` | เขียนบรรทัด access log ต่อ request (`GET /users/42 200 12ms`) ตั้ง `false` เมื่อมี gateway/ingress log ให้อยู่แล้ว หรือ traffic เป็น health check ล้วน — ไม่กระทบ breadcrumb / Sentry Logs / error reporting ซึ่งเป็นสวิตช์ของตัวเอง ปรับต่อ server ได้ที่ `HTTPOptions.DisableRequestLog` (ชนะค่านี้) |
| `LOG_SIMPLE` | bool | `false` | `true` = text handler อ่านง่ายสำหรับ dev, `false` = JSON สำหรับ production log pipeline |
| `LOG_SOURCE` | bool | `true` | แนบ `source` = ไฟล์:บรรทัดที่เขียน log บรรทัดนั้น (`services/payment.go:88`) ตั้ง `false` เมื่อ log ถี่จนไม่อยากจ่ายค่า `runtime.Callers` ต่อบรรทัด (ดู [Logger](./logger.md)) |

> `LOG_HOST` / `LOG_PORT` ยังมีอยู่ใน `ENVConfig` เพื่อความเข้ากันได้กับ v1 (graylog)
> แต่ **v2 ยังไม่ได้ใช้** — ตั้งไว้ก็ไม่มีผล

## Sentry

ตั้งแค่ `SENTRY_DSN` ตัวเดียวก็ครบทุกอย่าง ที่เหลือคือการปรับจูน —
รายละเอียดพฤติกรรมอยู่ที่ [Sentry](./sentry.md)

| Key | ชนิด | Default | คำอธิบาย |
|---|---|---|---|
| `SENTRY_DSN` | string | *(ว่าง)* | **ว่าง = ปิดทั้งระบบ** (ทุก method เป็น no-op) DSN ผิดรูปแบบ = boot ไม่ผ่าน |
| `SENTRY_ENVIRONMENT` | string | ค่า `ENV` | ใช้แยก issue ระหว่าง staging/production ใน Sentry |
| `SENTRY_RELEASE` | string | *(ว่าง)* | version หรือ commit sha — ทำให้ Sentry บอกได้ว่า bug นี้เกิดใหม่ใน release ไหน (regression tracking) |
| `SENTRY_SERVER_NAME` | string | hostname | ชื่อเครื่อง/pod ที่ส่ง event |
| `SENTRY_DEBUG` | bool | `false` | ให้ SDK พิมพ์การทำงานของตัวเองออก stderr ใช้ตอน "ทำไมไม่เห็น event" |
| `SENTRY_SAMPLE_RATE` | float | `1.0` | สัดส่วน **error** ที่ส่งจริง (`0.25` = 25%) ลดเมื่อ error เยอะจนกินโควตา |
| `SENTRY_TRACES_SAMPLE_RATE` | float | `0` | สัดส่วน **transaction** ที่ส่ง — ตั้ง > 0 ถือว่าเปิด tracing โดยปริยาย |
| `SENTRY_ENABLE_TRACING` | bool | `false` | เปิด transaction ของ request/job/HTTP call ขาออก ถ้าเปิดโดยไม่ตั้ง sample rate จะใช้ `1.0` |
| `SENTRY_ENABLE_CRONS` | bool | `false` | ส่ง check-in ของ job ที่มี `Schedule` ทำให้ Sentry เตือนเมื่อ run ที่ควรเกิดแต่ไม่เกิด ⚠️ เปิดแล้ว Sentry จะสร้าง monitor อัตโนมัติ (มีโควตาแยก) |
| `SENTRY_ENABLE_LOGS` | bool | `false` | ส่งทุกบรรทัด `ctx.Log()` เข้า **Sentry Logs** (ค้นหาได้เองโดยไม่ต้องมี error, ผูก trace เดียวกับ request) ⚠️ มีโควตาแยกจาก event |
| `SENTRY_LOG_LEVEL` | string | ตาม `LOG_LEVEL` | ระดับต่ำสุดที่ส่งเข้า Sentry Logs — `debug` / `info` / `warn` / `error` ไม่ตั้ง = ส่งทุกบรรทัดที่ `LOG_LEVEL` ปล่อยผ่าน ตั้งเมื่ออยากส่ง Sentry น้อยกว่าที่พิมพ์ออก stdout |
| `SENTRY_ENABLE_METRICS` | bool | `false` | เปิด `ctx.Meter()` — counter / gauge / distribution ที่ผูกกับ trace ของ request นั้น ⚠️ มีโควตาแยก |
| `SENTRY_MIN_STATUS` | int | `500` | **เส้นแบ่งเดียวของทั้งระบบ**: error ที่ status ต่ำกว่านี้ไม่ถือเป็น incident ตั้ง `400` ถ้าอยากเห็น 4xx ด้วย — error ที่**ไม่มี** status (plain error) ถูกส่งเสมอ ไม่ผ่านเส้นนี้ (ดู [Sentry](./sentry.md)) |
| `SENTRY_CAPTURE_RETRIES` | bool | `false` | `false` = job รายงานเฉพาะตอน attempt หมดแล้ว, `true` = รายงานทุก attempt ที่ fail |
| `SENTRY_BREADCRUMB_LEVEL` | string | `info` | ระดับ log ต่ำสุดที่กลายเป็น breadcrumb — `debug` / `info` / `warn` / `error` / `off` |
| `SENTRY_MAX_BREADCRUMBS` | int | `50` | จำนวน breadcrumb สูงสุดต่อ event (อันเก่าสุดถูกดันออก) |
| `SENTRY_ATTACH_STACKTRACE` | bool | `true` | แนบ stack แม้กับ event ที่ไม่ได้มาจาก error |
| `SENTRY_SEND_DEFAULT_PII` | bool | `false` | ให้ SDK ส่ง IP/cookie ตาม default ของมัน (framework redact header ที่อ่อนไหวให้อยู่แล้ว) |
| `SENTRY_CAPTURE_BODY` | bool | `true` | แนบ request body ของ request ที่ fail (scrub แล้ว, ข้าม multipart/binary) |
| `SENTRY_MAX_BODY_BYTES` | int | `16384` | เพดาน body ที่เก็บ (16 KiB) — เกินจากนี้ถูกตัด |
| `SENTRY_SEND_ENV` | bool | `true` | แนบ config ทั้งชุด (scrub แล้ว) เป็น context `config` ของทุก event |
| `SENTRY_IGNORE_ERRORS` | string | *(ว่าง)* | regex คั่นด้วย `,` — event ที่ message ตรงจะถูกทิ้ง เช่น `context canceled,broken pipe` |
| `SENTRY_IGNORE_TRANSACTIONS` | string | *(ว่าง)* | regex คั่นด้วย `,` สำหรับ transaction เช่น `GET /health` |
| `SENTRY_FLUSH_TIMEOUT` | int (วินาที) | `5` | เวลารอส่ง event ที่ค้างตอน `app.Shutdown` |

## Database (SQL)

| Key | ชนิด | Default | คำอธิบาย |
|---|---|---|---|
| `DB_CONNECTION_STRING` | string | *(ว่าง)* | URI เต็ม — **ถ้าตั้งไว้ ฟิลด์ `DB_*` ที่เหลือถูกมองข้ามทั้งหมด** |
| `DB_DRIVER` | string | *(ว่าง)* | `postgres` หรือ `mysql` — **บังคับ** เมื่อไม่ได้ใช้ connection string (ถ้าใช้ URI จะเดาจาก scheme ให้) |
| `DB_HOST` | string | *(ว่าง)* | hostname ของ DB |
| `DB_PORT` | string | *(ว่าง)* | port (เป็น string เพราะต่อเข้า DSN ตรงๆ) |
| `DB_NAME` | string | *(ว่าง)* | ชื่อ database |
| `DB_USER` | string | *(ว่าง)* | user |
| `DB_PASSWORD` | string | *(ว่าง)* | password — ถูก mask ในทุก Sentry event เสมอ |
| `DB_LOG_LEVEL` | string | ตาม `LOG_LEVEL` | คุมปริมาณ log ของ SQL แยกจาก `LOG_LEVEL` — `silent` (ปิดสนิท, `off`/`false` ก็ได้) / `error` / `warn` (เฉพาะ slow + fail) / `info` (ทุก statement) ดู [Query logging](./database-logging.md) |
| `DB_SSLMODE` | string | `disable` | เฉพาะ postgres: `disable` / `require` / `verify-full` … |

- scheme ที่เดา driver ได้: `postgres://`, `postgresql://`, `mysql://`
- MySQL: ใส่เป็น URI ได้เลย framework แปลงเป็น Go DSN (`u:p@tcp(host:port)/db`)
  พร้อมเติม `parseTime=True&charset=utf8mb4&loc=UTC` ให้เมื่อไม่ได้ระบุ
- ขนาด connection pool **ไม่ได้อยู่ใน env** — ตั้งผ่าน option ตอนสร้าง:
  ```go
  db, _ := core.NewDatabase(env,
      core.WithMaxOpenConns(50),   // default 20
      core.WithMaxIdleConns(10),   // default 5
      core.WithConnMaxLifetime(30*time.Minute), // default 1h
  )
  ```
- `DB_SID` มีอยู่ใน struct (Oracle ของ v1) แต่ **v2 ไม่ได้ใช้**

## MongoDB

| Key | ชนิด | Default | คำอธิบาย |
|---|---|---|---|
| `DB_MONGO_CONNECTION_STRING` | string | *(ว่าง)* | URI เต็ม ใช้ก่อนฟิลด์แยกเสมอ — จำเป็นถ้าต้องการ options ที่ฟิลด์แยกไม่ครอบคลุม |
| `DB_MONGO_HOST` | string | `127.0.0.1` | ใส่หลายตัวคั่น comma = replica set (`a,b,c` หรือ `a:27018,b`) |
| `DB_MONGO_PORT` | string | `27017` | |
| `DB_MONGO_USERNAME` | string | *(ว่าง)* | ไม่ตั้ง = ต่อแบบไม่มี auth (mongo local) |
| `DB_MONGO_PASSWORD` | string | *(ว่าง)* | ถูก mask ใน Sentry — ถูก percent-encode ให้แล้ว ใส่ `@` `/` `:` ได้ |
| `DB_MONGO_MAX_POOL_SIZE` | int | *(driver)* | |
| `DB_MONGO_MIN_POOL_SIZE` | int | *(driver)* | |
| `DB_MONGO_TIMEOUT` | int | *(ไม่จำกัด)* | วินาที — deadline ต่อ operation กันไม่ให้ query จาก context ที่ไม่มี deadline (cron/consumer) ค้างตลอดกาล |
| `DB_MONGO_NAME` | string | *(ว่าง)* | ชื่อ database ที่ `ctx.DBMongo()` ใช้ (ไม่ได้อยู่ใน URI) — **ต้องตั้งเสมอ** แม้ใช้ connection string |

| `DB_MONGO_REPLICA_NAME` | string | *(ว่าง)* | ชื่อ replica set — ต้องมีถ้าจะใช้ `Transaction` |
| `DB_MONGO_TLS` | bool | `false` | |
| `DB_MONGO_LOG_LEVEL` | string | ตาม `LOG_LEVEL` | `silent` / `error` / `warn` (คำสั่งช้า + ที่ fail — default) / `info`\|`debug` (ทุกคำสั่ง) — คู่ขนานกับ `DB_LOG_LEVEL` ของ SQL |

> ไม่ตั้ง `DB_MONGO_*` เลยก็รันได้ แต่ `ctx.DBMongo()` ทุก call จะ error `MONGO_DISABLED`
> (ไม่ nil ไม่ panic) — เหมือน storage คือ**ไม่** degrade เงียบ เพราะ read ที่คืนว่าง
> เงียบๆ หรือ write ที่หายไปเฉยๆ แย่กว่า error ที่บอกตรงๆ

## Cache (Redis)

| Key | ชนิด | Default | คำอธิบาย |
|---|---|---|---|
| `CACHE_CONNECTION_STRING` | string | *(ว่าง)* | `redis://:pass@host:6379/0` หรือ `rediss://` สำหรับ TLS — ใช้ก่อนฟิลด์แยก |
| `CACHE_HOST` | string | `127.0.0.1` | |
| `CACHE_PORT` | string | `6379` | |
| `CACHE_USERNAME` | string | *(ว่าง)* | redis 6+ ACL |
| `CACHE_PASSWORD` | string | *(ว่าง)* | ถูก mask ใน Sentry |
| `CACHE_DB` | int | `0` | หมายเลข database ของ redis |
| `CACHE_PREFIX` | string | *(ว่าง)* | namespace ของทุก key **และทุก channel** — ตั้งต่อ service/environment เมื่อใช้ redis ร่วมกัน |
| `CACHE_ADDRS` | string | *(ว่าง)* | `host:port` คั่นด้วย comma — หลายตัว = cluster, หรือรายชื่อ sentinel เมื่อใช้คู่กับ `CACHE_MASTER_NAME` |
| `CACHE_MASTER_NAME` | string | *(ว่าง)* | ชื่อ master ของ sentinel (เปลี่ยน address list ให้กลายเป็น sentinel list) |
| `CACHE_SENTINEL_PASSWORD` | string | *(ว่าง)* | ถูก mask ใน Sentry |
| `CACHE_TLS` | bool | `false` | เปิด TLS เมื่อใช้ฟิลด์แยก (URI ใช้ `rediss://` แทน) |
| `CACHE_TLS_SKIP_VERIFY` | bool | `false` | สำหรับ self-signed cert เท่านั้น |
| `CACHE_POOL_SIZE` | int | *(driver)* | จำนวน connection สูงสุดต่อ node |
| `CACHE_MIN_IDLE_CONNS` | int | *(driver)* | |
| `CACHE_DIAL_TIMEOUT` | int | `5` | วินาที — ใช้กับ PING ตอน boot ด้วย |
| `CACHE_READ_TIMEOUT` | int | `3` | วินาที — ตัวกันไม่ให้ redis ที่ค้างลากทั้ง request ไปด้วย |
| `CACHE_WRITE_TIMEOUT` | int | `3` | วินาที |
| `CACHE_MAX_RETRIES` | int | `3` | `-1` = ไม่ retry |

> ไม่ตั้ง `CACHE_*` เลยก็รันได้: `ctx.Cache()` จะคืน cache ที่ปิดอยู่ — อ่านแล้ว miss
> เขียนแล้วทิ้ง — ดังนั้น `core.Remember` ยังทำงาน (เรียก loader ทุกครั้ง) แทนที่จะ panic
> ส่วน `Subscribe` จะ error ชัดเจน เพราะ subscriber ที่เงียบคือ service ที่ดูปกติแต่ไม่ทำงาน

## Message Queue (RabbitMQ)

| Key | ชนิด | Default | คำอธิบาย |
|---|---|---|---|
| `MQ_CONNECTION_STRING` | string | *(ว่าง)* | `amqp://u:p@host:5672/vhost` (`amqps://` = TLS) — ใช้ก่อนฟิลด์แยก จำเป็นถ้าต้องระบุ vhost |
| `MQ_HOST` | string | *(ว่าง)* | ฟิลด์แยกจะต่อ URL เป็น `amqp://user:pass@host:port/` (vhost = `/`) |
| `MQ_PORT` | string | *(ว่าง)* | |
| `MQ_USER` | string | *(ว่าง)* | |
| `MQ_PASSWORD` | string | *(ว่าง)* | ถูก mask ใน Sentry |

## Storage (S3 / MinIO)

| Key | ชนิด | Default | คำอธิบาย |
|---|---|---|---|
| `S3_BUCKET` | string | *(ว่าง)* | **จำเป็น** — bucket ที่ `IStorage` ทุก method ทำงานด้วย ไม่ตั้ง = `NewStorage` error ตั้งแต่ boot |
| `S3_ENDPOINT` | string | *(ว่าง)* | ปล่อยว่าง = AWS S3 จริง ตั้งเมื่อใช้ MinIO/Ceph/R2 เช่น `minio:9000` หรือ `https://s3.example.com` |
| `S3_HTTPS` | bool | `false` | scheme ที่จะเติมให้ `S3_ENDPOINT` เมื่อเขียนมาแบบไม่มี scheme (`minio:9000` → `http://minio:9000`) |
| `S3_REGION` | string | `ap-southeast-1` | S3-compatible ไม่สนใจค่านี้แต่ signer ต้องมี — ตั้งเมื่อ bucket อยู่ region อื่น |
| `S3_ACCESS_KEY` | string | *(ว่าง)* | ถูก mask ใน Sentry — **ไม่ตั้งทั้งคู่** = ใช้ AWS default chain (instance role / IRSA / `~/.aws` / `AWS_*`) |
| `S3_SECRET_KEY` | string | *(ว่าง)* | ถูก mask ใน Sentry |
| `S3_FORCE_PATH_STYLE` | bool | `false` | `true` สำหรับ MinIO และ endpoint ที่ไม่รองรับ virtual-host style |
| `S3_PREFIX` | string | *(ว่าง)* | namespace ของทุก key — ตั้งต่อ service/environment เมื่อใช้ bucket ร่วมกัน |
| `S3_PUBLIC_URL` | string | *(ว่าง)* | base URL ที่ `PublicURL()` ใช้ (CDN หน้า bucket) ไม่ตั้ง = ประกอบจาก endpoint/bucket เอง |

> ไม่ตั้ง `S3_*` เลยก็ยังรันได้ แต่ `ctx.Storage()` ทุก call จะ error `STORAGE_DISABLED`
> ต่างจาก cache ตรงที่ storage **ไม่** degrade เงียบๆ — cache miss ยัง recompute ได้
> แต่ไฟล์ที่ upload แล้วถูกทิ้งเงียบคือไฟล์ที่ไม่มีใครเอาคืนได้

## Mailer (SMTP)

| Key | ชนิด | Default | คำอธิบาย |
|---|---|---|---|
| `EMAIL_SERVER` | string | *(ว่าง)* | SMTP host — คีย์เดียวที่จำเป็น |
| `EMAIL_PORT` | int | `0` | เช่น `587` (STARTTLS) หรือ `465` (implicit TLS) |
| `EMAIL_USERNAME` | string | *(ว่าง)* | ว่าง = ไม่ authenticate เลย |
| `EMAIL_PASSWORD` | string | *(ว่าง)* | ถูก mask ใน Sentry |
| `EMAIL_SENDER` | string | *(ว่าง)* | ที่อยู่ผู้ส่ง (`From`) ของทุกฉบับ |
| `EMAIL_SENDER_NAME` | string | *(ว่าง)* | ชื่อที่แสดงคู่กับที่อยู่ผู้ส่ง |
| `EMAIL_TLS_POLICY` | string | `mandatory` | `mandatory` \| `opportunistic` \| `none` — credentials วิ่งบน connection นี้ |
| `EMAIL_SSL` | bool | `false` | implicit TLS ตั้งแต่ byte แรก (port 465) |
| `EMAIL_TLS_SKIP_VERIFY` | bool | `false` | รับ certificate ที่ verify ไม่ผ่าน — เฉพาะ relay self-signed ในเน็ตส่วนตัว |
| `EMAIL_AUTH` | string | `auto` | `plain` \| `login` \| `cram-md5` \| `xoauth2` \| `scram-sha-1` \| `scram-sha-256` \| `none` \| `auto` |
| `EMAIL_TIMEOUT` | int | `30` | วินาที ครอบทั้งการส่งหนึ่งฉบับ |

## Push (FCM)

| Key | ชนิด | Default | คำอธิบาย |
|---|---|---|---|
| `FIREBASE_CREDENTIAL` | string | *(ว่าง)* | เนื้อ service-account JSON ทั้งก้อน (ไม่ใช่ path) ส่งเข้า `core.NewPusherFromEnv(env)` ปล่อยว่างเพื่อใช้ credentials ของเครื่อง (workload identity) |

## JWT

| Key | ชนิด | Default | คำอธิบาย |
|---|---|---|---|
| `JWT_SECRET` | string | *(ว่าง)* | framework **ไม่ได้อ่านเอง** — ส่งให้ชัดเจนตอนสร้าง auth: `core.NewJWTAuth(env.Config().JWTSecret)` ค่านี้ถูก mask ในทุก Sentry event |

---

## Connection string หรือ discrete fields

ทุก connection รองรับสองแบบ **ถ้ามี connection string จะใช้ตัวนั้นและมองข้ามฟิลด์แยกทั้งหมด**

| บริการ | URI (แนะนำ) | discrete fields |
|---|---|---|
| SQL | `DB_CONNECTION_STRING=postgres://u:p@host:5432/db?sslmode=disable` | `DB_DRIVER`, `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `DB_SSLMODE` |
| Cache | `CACHE_CONNECTION_STRING=redis://:pass@host:6379/0` | `CACHE_HOST`, `CACHE_PORT`, `CACHE_USERNAME`, `CACHE_PASSWORD`, `CACHE_DB` (cluster/sentinel ใช้ `CACHE_ADDRS` + `CACHE_MASTER_NAME`) |
| Mongo | `DB_MONGO_CONNECTION_STRING=mongodb://u:p@host:27017/db` | `DB_MONGO_HOST`, `DB_MONGO_PORT`, `DB_MONGO_USERNAME`, `DB_MONGO_PASSWORD` |
| MQ | `MQ_CONNECTION_STRING=amqp://u:p@host:5672/vhost` | `MQ_HOST`, `MQ_PORT`, `MQ_USER`, `MQ_PASSWORD` |

```go
db, _    := core.NewDatabase(env)   // อ่าน DB_CONNECTION_STRING หรือ DB_* ให้เอง
redis, _ := core.NewCache(env)
mongo, _ := core.NewMongoDB(env)    // ยังต้องมี DB_MONGO_NAME เสมอ
mq, _    := core.NewMQ(env)
```

แนะนำ URI ในทุก environment ที่ไม่ใช่ dev เพราะ option ปลีกย่อย (replica set, TLS,
vhost, pool params) ใส่ได้ครบในสายเดียว และมักเป็นรูปแบบที่ managed service ให้มาอยู่แล้ว

## อ่านค่าแบบ typed

```go
cfg := env.Config()          // *core.ENVConfig
cfg.Service
cfg.DBHost
cfg.EmailPort               // int
cfg.SentryTracesSampleRate  // float64
```

## Environment gates

```go
env.IsDev()   // ENV=dev
env.IsTest()  // ENV=test
env.IsMock()  // ENV=mock
env.IsProd()  // ENV=prod
```

ถ้าไม่ได้ตั้ง `ENV` เลย ทั้งสี่ตัวคืน `false` — โค้ดที่เขียนแบบ `if !env.IsProd()`
จะทำงานเหมือนอยู่ใน dev โดยไม่ตั้งใจ **จึงควรตั้ง `ENV` เสมอ**

## อ่าน key ที่ไม่ได้อยู่ใน struct

```go
env.String("some_key")     // APP_SOME_KEY หรือ SOME_KEY ในไฟล์
env.Int("some_count")      // แปลงจาก string ให้
env.Bool("some_flag")      // "true"/"1" → true
env.Float64("some_rate")
env.All()                  // map[string]string ของทุก key ที่โหลดมา
```

- key ที่ไม่มีอยู่จริงคืน zero value (`""`, `0`, `false`) ไม่ error —
  ถ้าต้องแยก "ไม่ได้ตั้ง" กับ "ตั้งเป็น 0/false" ให้เช็ค `env.String(key) != ""` ก่อน
- `env.All()` คือสิ่งที่ถูกแนบไปกับ Sentry event (หลัง scrub) — key ที่ชื่อเข้าข่าย
  ความลับถูก mask ให้อัตโนมัติ

> แนะนำให้เพิ่ม field ใน `ENVConfig` เป็นหลัก accessor พวกนี้เป็น escape hatch

## เพิ่ม config key ใหม่

เพิ่ม **field เดียว** ใน `ENVConfig` พร้อม tag `koanf:"..."` — loader bind ให้เอง
(ไม่มี list ที่สองให้ลืมแก้เหมือน v1):

```go
// ใน ENVConfig
NewFeatureURL string `koanf:"new_feature_url"`
// ตั้งค่า: APP_NEW_FEATURE_URL=... หรือใน .env: NEW_FEATURE_URL=...
```

ข้อควรรู้: bool ที่อยากให้ default เป็น `true` ใส่เป็น field ตรงๆ ไม่ได้ เพราะ
"ไม่ได้ตั้ง" กับ "ตั้งเป็น false" หน้าตาเหมือนกัน — ให้เช็คการมีอยู่ของค่าแทน
(แบบเดียวกับที่ `SENTRY_CAPTURE_BODY` ทำ):

```go
enabled := true
if env.String("my_flag") != "" {
    enabled = env.Bool("my_flag")
}
```

## ตัวอย่างไฟล์เต็ม

`.env` สำหรับ dev:

```sh
ENV=dev
SERVICE=payment-api
HOST=:3000
LOG_LEVEL=debug
LOG_SIMPLE=true

DB_CONNECTION_STRING=postgres://postgres:postgres@localhost:5432/payment?sslmode=disable
CACHE_CONNECTION_STRING=redis://localhost:6379/0
```

production (ตั้งผ่าน OS env / secret manager — สังเกต prefix `APP_`):

```sh
APP_ENV=prod
APP_SERVICE=payment-api
APP_HOST=:8080
APP_LOG_LEVEL=info

APP_DB_CONNECTION_STRING=postgres://app:***@db.internal:5432/payment?sslmode=require
APP_CACHE_CONNECTION_STRING=rediss://:***@cache.internal:6379/0
APP_MQ_CONNECTION_STRING=amqps://app:***@mq.internal:5671/payment

APP_SENTRY_DSN=https://***@o1.ingest.sentry.io/123
APP_SENTRY_RELEASE=payment-api@1.4.2
APP_SENTRY_ENABLE_CRONS=true
APP_SENTRY_TRACES_SAMPLE_RATE=0.1
APP_SENTRY_IGNORE_TRANSACTIONS=GET /health
```

`test.env` (ใช้เมื่อ `APP_ENV=test`):

```sh
ENV=test
SERVICE=payment-api-test
DB_CONNECTION_STRING=postgres://postgres:postgres@localhost:5432/payment_test?sslmode=disable
# ไม่ตั้ง SENTRY_DSN → tracker เป็น no-op, test ไม่ยิงออกเน็ต
```

## ต่างจาก v1

| v1 | v2 |
|---|---|
| global `viper.GetString(...)` | koanf instance (ไม่มี global, test แยกกันได้, ปลอด race) |
| เพิ่ม key = แก้ `ENVConfig` **และ** `envKeys` list | เพิ่ม field เดียว (bind อัตโนมัติ) |
| config ผิดรู้ตอน runtime | validate `APP_ENV` + DSN ของ Sentry ตอนโหลด |
| `NewEnv()` ไม่คืน error — อ่านไฟล์พลาดก็เงียบ | คืน `(IENV, IError)` ให้ caller ตัดสินใจ |
| ตั้ง key ผิด/ลืมใส่ใน `envKeys` → ค่าว่างแบบเงียบ | ยัง bind อัตโนมัติ แต่ key ที่ผูกไม่ติดสังเกตได้จาก `env.All()` |
