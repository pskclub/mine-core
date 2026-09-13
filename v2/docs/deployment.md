# Deployment

หน้านี้คือสิ่งที่ต้องตั้งให้ถูกตอนเอา service ที่เขียนด้วย mine-core v2 ขึ้น
production: image, config, probe, การ scale แต่ละ [role](./lifecycle.md)
และการ shutdown ที่ไม่ทิ้ง request

## Image

สอง stage: build ด้วย golang image แล้ว copy binary
ลง alpine เปล่า

```dockerfile
# mine-core เป็น public module — ไม่ต้องตั้ง GOPRIVATE หรือ credential ใดๆ
FROM golang:1.27-alpine
WORKDIR /app

ENV GOTOOLCHAIN=auto

ADD go.mod go.sum /app/
RUN go mod download
ADD . /app/

RUN go build -o main

FROM alpine:3.22
# TLS ขาออก (Sentry, S3, FCM, HTTPS ทุกตัว) ต้องใช้ root store — alpine เปล่าไม่มี
RUN apk add --no-cache ca-certificates tzdata
COPY --from=0 /app/main /main
CMD ["/main"]
```

`ca-certificates` ลืมไม่ได้: ถ้าไม่มี ทุก HTTPS call จะพังด้วย
`x509: certificate signed by unknown authority` — และเป็นตอน runtime ไม่ใช่ตอน build

`ADD go.mod go.sum` ก่อน `ADD .` เพื่อให้ layer ของ `go mod download` ถูก cache
ไว้ข้ามการแก้โค้ด

## Config: environment เท่านั้น

`.env` มีไว้สำหรับเครื่อง dev — production ตั้งผ่าน OS environment ซึ่ง**ชนะไฟล์เสมอ**
และใช้ prefix `APP_`:

| ใน `.env` | ใน environment |
|---|---|
| `DB_CONNECTION_STRING=...` | `APP_DB_CONNECTION_STRING=...` |
| `ROLE=worker` | `APP_ROLE=worker` |

⚠️ prefix `APP_` ใช้กับ OS environment **เท่านั้น** — เขียน `APP_ROLE` ไว้ข้างใน
`.env` จะ bind เป็น key `app_role` และถูกเมินเงียบๆ ดู [Configuration](./env.md)

key ที่ควรตั้งต่างจาก dev เสมอ:

| Key | Production |
|---|---|
| `APP_ENV` | ไม่ใช่ `dev` — ไม่งั้นไม่มี graceful shutdown และข้อความ error ดิบหลุดออก response |
| `APP_SERVICE` | ชื่อ service — ติดไปกับทุก log line และทุก event ใน Sentry |
| `APP_LOG_SIMPLE` | ไม่ตั้ง (JSON) — text handler มีไว้ให้คนอ่านตอน dev |
| `APP_LOG_LEVEL` | `info` — `debug` log ทุก SQL statement |
| `APP_SENTRY_DSN` | ตั้ง ไม่งั้น error tracking เป็น no-op |
| `APP_HOST` | `0.0.0.0:<port>` — `localhost:` ใน container จะรับ traffic จากข้างนอกไม่ได้ |

> `APP_ENV=dev` ใน production คือความผิดพลาดที่แพงที่สุดในตารางนี้: dev mode ใส่
> ข้อความ error ตัวจริงลงไปใน response (ดู [Error handling](./error-handling.md))
> และ `StartHTTPServer` จะไม่ drain ตอนถูกสั่งหยุด

## Role ต่อ deployment

image เดียว หลาย deployment ต่างกันแค่ `APP_ROLE`:

```yaml
# api — scale ได้ตามใจ
env:
  - name: APP_ROLE
    value: "api"
replicas: 3
---
# worker — หนึ่งตัวเท่านั้น
env:
  - name: APP_ROLE
    value: "worker"
replicas: 1
```

⚠️ **`worker` และ `all` ต้องมี replica เดียว** — scheduler queue default อยู่ใน
memory ของแต่ละ process ทุก replica จึงยิงทุก cron tick ถ้าต้องการ worker
หลายตัว ให้ย้าย queue ไปไว้ที่ database ([Jobs](./jobs.md)) หรือกันด้วย Redis lock
(`SetNX`) ในตัว job เอง

service เล็กที่ยังไม่ต้อง scale: ใช้ `APP_ROLE=all` deployment เดียว replica เดียว
— HTTP กับ job ใช้ pool ชุดเดียวกัน

## Probes

```yaml
livenessProbe:
  httpGet: {path: /healthz, port: 3000}
  periodSeconds: 10
readinessProbe:
  httpGet: {path: /healthz, port: 3000}
  periodSeconds: 5
```

`/healthz` **ห้ามแตะ database**: liveness ที่ query DB จะทำให้ database กระตุก
ครั้งเดียวกลายเป็นการรีสตาร์ตทุก pod ที่ยังดีอยู่ — เปลี่ยนเหตุขัดข้องชั่วคราว
ให้เป็นเหตุขัดข้องเต็มรูปแบบ ถ้าอยากเช็ค dependency จริงให้ทำเป็น `/readyz`
แยกต่างหาก และผูกกับ readiness probe เท่านั้น

`worker` ไม่มี HTTP port จึงไม่มี probe — ให้ดูจาก heartbeat job แทน
([Logging practices](./logging-practices.md#what-is-worth-a-line)) และตั้ง alert
ว่าถ้าไม่เห็นบรรทัดนั้นเกิน N นาทีคือมีปัญหา

## Shutdown ต้องพอดีกับ grace period

`StartHTTPServer` ดัก `SIGTERM` และให้เวลา drain ตาม `core.DefaultGracefulTimeout`
(10 วินาที) — ตัวเลขนี้ต้อง**น้อยกว่า** `terminationGracePeriodSeconds` ของ
orchestrator ไม่งั้น process จะถูก `SIGKILL` ระหว่างที่ยัง drain ไม่จบ

```yaml
terminationGracePeriodSeconds: 30   # > graceful timeout ของแอป
```

process ที่รัน job ด้วยคุม shutdown เอง ให้เทียบกับ `shutdownTimeout` ในนั้นแทน
— ดู [Lifecycle](./lifecycle.md)

## Migration ตอน deploy

schema ไม่ได้ถูกแก้โดยตัว service ([Migrations](./migrations.md)) — รันเป็น
step แยกก่อน rollout ด้วย image ของตัวเอง:

```dockerfile
FROM oven/bun:1.3.4-alpine
WORKDIR /app
COPY ./package.json ./bun.lock /app/
RUN bun install --frozen-lockfile
COPY ./prisma.config.ts /app
COPY ./prisma /app/prisma
CMD ["bun", "run", "migrate:deploy"]
```

`migrate deploy` ไม่ใช่ `migrate dev`: มัน apply เฉพาะ migration ที่ commit ไว้
แล้วเท่านั้น ส่วน `dev` เป็นตัวเขียน migration ใหม่ — มันขอ shadow database, ถาม
คำถาม และ reset database ที่ใช้ร่วมกันได้

## Log และ error tracking

เขียน JSON ลง stdout ให้ platform เก็บเอง ไม่ต้องเขียนไฟล์ ไม่ต้อง rotate

`APP_SENTRY_DSN` ทำให้ทุก error ที่ status ≥ 500 ถูกส่งพร้อม request, user และ
breadcrumb ของ unit นั้น — ดู [Sentry](./sentry.md) สำหรับ sampling rate,
environment และ release ซึ่งควรตั้งจาก CI (`APP_SENTRY_RELEASE=$CI_COMMIT_SHA`)

## Checklist

- [ ] `APP_ENV` ไม่ใช่ `dev`
- [ ] `APP_HOST` bind `0.0.0.0`
- [ ] `ca-certificates` อยู่ใน runtime image
- [ ] `terminationGracePeriodSeconds` > graceful timeout ของแอป
- [ ] `worker` / `all` = 1 replica
- [ ] `/healthz` ไม่แตะ database
- [ ] migration รันเป็น step แยกก่อน rollout
- [ ] `APP_SENTRY_DSN` + release ตั้งจาก CI
- [ ] secret มาจาก secret store ไม่ใช่ `.env` ที่ commit ไว้
