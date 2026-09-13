# Lifecycle & Roles

`App` ถูกสร้าง **ครั้งเดียว** ตอน start และถือ resource ที่อายุยืนทั้งหมด — connection
pool, logger, Sentry client ส่วน context ถูกสร้างใหม่ต่อ request/ต่อ job run
จาก `App` ตัวนั้น หน้านี้คือวงจรชีวิตทั้งเส้น: **bootstrap → เลือก role → serve →
ปิดตามลำดับ**

## Bootstrap: สร้าง App ครั้งเดียว

เปิดเฉพาะ connection ที่ config มีจริง — service ที่ใช้แค่ SQL ก็รันได้ โดย
`ctx.Cache()` คืน cache ที่ปิดอยู่ (อ่าน miss / เขียนทิ้ง) ส่วน `ctx.MQ()` กับ
`ctx.Storage()` คืน handle ที่ทุกคำสั่ง fail พร้อมบอกว่า config ตัวไหนหาย — ไม่มีตัวไหน
คืน `nil` และไม่มีตัวไหน panic ตอน boot

```go
// cmd/bootstrap.go
func Bootstrap() (*core.App, core.IError) {
    env, err := core.NewEnv()
    if err != nil {
        return nil, err
    }
    cfg := env.Config()

    opts := make([]core.Option, 0, 3)

    if cfg.DBConnectionString != "" || cfg.DBHost != "" {
        db, err := core.NewDatabase(env,
            core.WithMaxOpenConns(20),
            core.WithMaxIdleConns(5),
            core.WithConnMaxLifetime(time.Hour),
        )
        if err != nil {
            return nil, err
        }
        opts = append(opts, core.WithSQL("default", db))
    }

    if cfg.CacheConnectionString != "" || cfg.CacheHost != "" {
        cache, err := core.NewCache(env)
        if err != nil {
            return nil, err
        }
        opts = append(opts, core.WithCache("default", cache))
    }

    return core.NewApp(env, opts...)
}
```

⚠️ **pool อยู่ตลอดอายุ process ไม่ใช่ต่อ request** — นี่คือบั๊กของ v1 ที่ v2 แก้:
v1 `IContext.Close()` ปิด pool ที่ใช้ร่วมกันทิ้งทุก request ใน v2 มีแต่
`app.Shutdown()` ตอน process จบเท่านั้นที่ปิด

## Role: binary เดียว หนึ่ง role ต่อ deployment

```go
// main.go
func main() {
    app, err := cmd.Bootstrap()
    if err != nil {
        log.Fatalf("bootstrap: %v", err)
    }

    switch strings.ToLower(strings.TrimSpace(app.ENV().String("role"))) {
    case consts.RoleWorker:
        cmd.WorkerRun(app)   // scheduler อย่างเดียว
    case consts.RoleAll:
        cmd.AllRun(app)      // ทั้งคู่ใน process เดียว
    default:
        cmd.APIRun(app)      // HTTP อย่างเดียว
    }
}
```

| `APP_ROLE` | รันอะไร | Replica |
|---|---|---|
| *(ไม่ตั้ง)* / `api` | HTTP server | หลายตัวได้ |
| `worker` | scheduler + jobs | **ตัวเดียว** |
| `all` | ทั้งคู่ ใน process เดียว | **ตัวเดียวเท่านั้น** |

> อ่าน role จาก **config** ไม่ใช่ `os.Getenv` ตรงๆ — `ROLE=all` ใน `.env` และ
> `APP_ROLE=all` ใน environment จึงใช้ได้ทั้งคู่ (compose ตั้งแบบหลัง, dev เครื่อง
> ตัวเองตั้งแบบแรก) ดู [Configuration](./env.md)
>
> ⚠️ prefix `APP_` ใช้กับ **OS environment variable เท่านั้น** — เขียน `APP_ROLE=all`
> *ข้างใน* `.env` จะ bind เป็น key `app_role` และถูกเมินเงียบๆ เหลือ role `api` ตามเดิม

⚠️ **ห้าม scale `all` เกิน 1 replica** — scheduler queue default อยู่ใน memory
และเป็นของแต่ละ process ทุก replica จึงยิงทุก cron tick: 3 replica = รายงานกลางคืน
รัน 3 รอบ ถ้าจะ scale ให้รัน `api` หลายตัวคู่กับ `worker` ตัวเดียว
(หรือใช้ queue ที่เป็น database — ดู [Jobs](./jobs.md))

`all` เหมาะกับ service เล็กและ dev เครื่องตัวเอง: HTTP กับ job ใช้ `App` เดียวกัน
จึงมี pool ชุดเดียวไม่ใช่สองชุด และ shutdown เรียงลำดับข้ามทั้งคู่ให้ด้วย

## Composition root: ที่เดียวที่รู้จัก module ทั้งหมด

```go
// cmd/modules.go — รายชื่อ module ชุดเดียว ใช้ได้ทุก role
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

```go
// cmd/api.go — เสียบ set นั้นเข้ากับ server
func NewAPI(app *core.App, mods *core.ModuleSet, opts *core.HTTPOptions) *core.Server {
    e := core.NewHTTPServer(app, opts)

    core.RegisterHealthRoutes(e, core.HealthOptions{Checks: mods.HealthChecks()})
    mods.MountHTTP(e)

    return e
}
```

```go
// cmd/worker.go — job ของ module ถูกเสียบโดย Runner ไม่ใช่ที่นี่
func newScheduler(app *core.App) (*core.Scheduler, core.IError) {
    sc, err := core.NewScheduler(app)
    if err != nil {
        return nil, err
    }

    return sc, registerInfrastructureJobs(sc)   // เหลือแต่ job ที่ไม่เป็นของ module ไหน
}
```

`NewAPI` **export** เพราะ test ประกอบ service จริงผ่านมัน (`testkit.Serve` เรียก
ฟังก์ชันนี้) — module ที่ลงทะเบียนโดยมี dependency ขาด จึงพังใน test ไม่ใช่ใน production

การเพิ่ม route หรือ job ให้ module ที่มีอยู่แล้วแตะแค่ module นั้น มีแค่ module **ตัวใหม่**
ที่แตะ `Modules` — และเมื่อมันเป็น list ชุดเดียว `api` กับ `worker` จึงเลื่อนออกจากกันไม่ได้
ดู [Modules](./modules.md)

## Shutdown: ลำดับสำคัญ

```
1. HTTP    หยุดรับ connection ใหม่, ปล่อยงานที่ค้างอยู่ให้จบ
2. Jobs    หยุด schedule, รอ run ที่กำลังทำอยู่
3. Pools   ปิด DB / cache / MQ / Sentry เป็นอันสุดท้าย
```

pool ที่ถูกปิดตอน request ยังถืออยู่ เปลี่ยน shutdown ที่สะอาดให้กลายเป็น 500
รัวๆ — ลำดับนี้จึงกลับด้านไม่ได้

### HTTP อย่างเดียว: `StartHTTPServer` ทำให้หมดแล้ว

```go
e := core.NewHTTPServer(app, &core.HTTPOptions{AllowOrigins: []string{"*"}})
e.GET("/healthz", healthz)

core.StartHTTPServer(e, env)   // block; drain แล้วปิด pool ให้เอง
```

| นอก dev | ใน dev (`APP_ENV=dev`) |
|---|---|
| ดัก `SIGINT` + `SIGTERM` | ไม่ดัก — Ctrl-C ตายทันที |
| drain `core.DefaultGracefulTimeout` (10s) | ไม่ drain |
| `app.Shutdown()` ปิด pool หลัง drain จบ | ไม่ปิด (process กำลังจะตายอยู่แล้ว) |

> **SIGTERM คือตัวที่สำคัญใน production** — `docker stop`, Kubernetes และ systemd
> ส่งตัวนี้ process ที่ไม่ดักจะถูกฆ่าทิ้งหลังหมด grace period พร้อม request ที่ค้างอยู่
> ทั้งหมด (ส่วน SIGINT คือ Ctrl-C ซึ่งเป็นเหตุการณ์เดียวกันจาก terminal)
>
> สัญญาณตัวที่สองระหว่าง drain จะ **ฆ่า process** ไม่ถูกกลืน — server ที่ค้างนาน
> เกินไปต้อง Ctrl-C ซ้ำแล้วจบได้

`app.Shutdown()` เรียกซ้ำได้ปลอดภัย (`sync.Once` ข้างใน) — `defer app.Shutdown(ctx)`
ไว้บนสุดของ `main` คู่กับ `StartHTTPServer` จึงไม่มีปัญหา

### HTTP + jobs: process เป็นเจ้าของ shutdown เอง

เพราะ scheduler ต้องหยุดคั่นกลางระหว่างสองขั้นนั้น:

```go
// cmd/run.go
const shutdownTimeout = 15 * time.Second   // ตั้งให้ยาวกว่างานที่นานที่สุดที่คาดไว้

func serve(app *core.App, e *core.Server, sc *core.Scheduler) {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    if sc != nil {
        if err := sc.Start(); err != nil {          // non-blocking
            app.Log().Error("scheduler start", "err", err)
            return
        }
        app.Log().Info("scheduler started")
    }

    if e != nil {
        addr := app.Config().Host
        if addr == "" {
            addr = ":8080"
        }

        // bind ก่อนค่อยประกาศ: ถ้า start ใน goroutine แล้ว log "started" คู่กันไป
        // เลย พอ port ชนจะได้ "started" ตามด้วย bind error ทันที ซึ่งอ่านเหมือน
        // crash มากกว่าเหมือน start ไม่ขึ้น
        ln, err := net.Listen("tcp", addr)
        if err != nil {
            app.Log().Error("http server cannot bind", "addr", addr, "err", err)
            shutdown(app, nil, sc)
            return
        }

        go func() {
            cfg := echo.StartConfig{Listener: ln, HideBanner: true, HidePort: true}
            if err := e.Serve(context.Background(), cfg); err != nil && !errors.Is(err, http.ErrServerClosed) {
                app.Log().Error("http server stopped", "err", err)
                stop()   // listener ตายต้องพา process ลงไปด้วย ไม่ใช่ค้าง
            }
        }()
        app.Log().Info("http server listening", "addr", ln.Addr().String())
    }

    <-ctx.Done()
    app.Log().Info("shutting down")
    shutdown(app, e, sc)
}

func shutdown(app *core.App, e *core.Server, sc *core.Scheduler) {
    shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
    defer cancel()

    // 1. หยุด "ผลิต" งานใหม่ก่อน — tick ที่เกิดระหว่าง drain คือ run ที่ถูก
    //    queue ไว้แล้วโดนยกเลิกอีกไม่กี่วินาทีต่อมา (หรือแย่กว่านั้นถ้ามันไม่ idempotent)
    if sc != nil {
        if err := sc.Stop(); err != nil {
            app.Log().Error("scheduler shutdown", "err", err)
        }
    }
    // 2. drain ของที่ค้างอยู่
    if e != nil {
        if err := e.Shutdown(shutdownCtx); err != nil {
            app.Log().Error("http shutdown", "err", err)
        }
    }
    // 3. ค่อยปิด pool — App.Shutdown หยุด subscriber/consumer ให้ก่อนปิด connection
    if err := app.Shutdown(shutdownCtx); err != nil {
        app.Log().Error("app shutdown", "err", err)
    }
}
```

ลำดับนี้ตรงกับที่ [`Runner`](./runner.md) ทำให้ — และเป็นเหตุผลที่ควรใช้ `Runner` แทน
การเขียนเอง: มันจัดลำดับ hook → scheduler → jobs → HTTP → pool ให้ครบ พร้อม
drain/close timeout แยกกัน โค้ดข้างบนคือรูปที่เล็กที่สุดที่ยังถูกเมื่อมีแค่ HTTP + scheduler

ทุก argument เป็น optional — startup ที่ล้มกลางคันจึงยังคืน resource ส่วนที่ขึ้นมา
แล้วได้ และ `serve` ตัวเดียวใช้ได้ทั้งสาม role: `APIRun` ส่ง scheduler เป็น nil,
`WorkerRun` ส่ง server เป็น nil, `AllRun` ส่งครบ

> `e.Serve` ใน goroutine รับ `context.Background()` ไม่ใช่ `ctx` — การ drain
> ทำที่ `e.Shutdown(shutdownCtx)` ในลำดับที่ถูกต้อง ถ้าส่ง `ctx` เข้าไปด้วย
> HTTP จะเริ่ม drain เองตั้งแต่วินาทีที่ได้สัญญาณ ก่อนที่ `shutdown` จะได้จัดลำดับอะไรเลย

## Health check

ไม่ต้องเขียนเอง — บรรทัดเดียวได้ทั้ง liveness และ readiness ที่แยกหน้าที่กันถูกแล้ว:

```go
core.RegisterHealthRoutes(e)   // GET /healthz + GET /readyz
```

`/healthz` **ไม่แตะ dependency เลยโดยตั้งใจ** — liveness ที่ ping database คือ probe ที่
รีสตาร์ตทุก instance พร้อมกันตอน database กระตุกครั้งเดียว ส่วน `/readyz` เช็ค
dependency จริงและแยก `up` / `degraded` / `down` ให้

รายละเอียดและการเพิ่ม check ของตัวเองอยู่ที่ [Health & Readiness](./health.md) ·
การตั้ง probe ที่ [Deployment](./deployment.md#probes)

## อ่านต่อ

- [Deployment](./deployment.md) — image, probe, การ scale แต่ละ role
- [API + Cron together](./api-with-cron.md) — ตัวอย่างเต็มของ process แบบรวม
- [Jobs](./jobs.md) / [Scheduler](./scheduler.md) — job runner และ queue
- [Project Structure](./structure.md) — `cmd/` วางตัวยังไงเทียบกับ `modules/`
