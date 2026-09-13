# Testing — Mocks และของปลอม

`coretest` ตั้งใจให้ใช้ของจริงเป็นค่าเริ่มต้น — database จริง, HTTP stack จริง,
job runner จริง เพราะของปลอมพิสูจน์ได้แค่ว่าเราเรียกมันถูก ไม่ได้พิสูจน์ว่ามัน
ทำงานถูก

แต่บางอย่างปลอมดีกว่า หน้านี้ว่าด้วยว่าอันไหน และปลอมอย่างไร

## ปลอมเมื่อไหร่

| ปลอมเมื่อ | เหตุผล |
|---|---|
| ของจริงอยู่นอกองค์กร | payment gateway, SMS, FCM — ยิงจริงไม่ได้ในเทส |
| ของจริงคิดเงิน | ทุก request มีค่าใช้จ่าย |
| ของจริงช้าหรือไม่เสถียร | เทสที่ fail สุ่มๆ แย่กว่าไม่มีเทส |
| ต้องบังคับให้เกิดความล้มเหลว | timeout, 500, การตอบผิดรูปแบบ — สั่งของจริงให้พังยาก |
| ผลลัพธ์ไม่คงที่ | เวลา, ตัวเลขสุ่ม, UUID |

**อย่าปลอม** database, repository ของตัวเอง, หรือ service ในโปรเจกต์เดียวกัน —
พวกนั้นเร็วพออยู่แล้วและของจริงจับบั๊กได้มากกว่า
(ดู [Integration tests](./testing-integration.md#ทําไมไม่-mock-database))

## ปลอม capability ผ่าน `WithAppOptions`

ทุก capability ใน `App` เป็น interface จึงสลับตัวจริงเป็นตัวปลอมได้ตอนสร้าง

```go
ctx := coretest.NewContext(t,
    coretest.WithAutoMigrate(&models.User{}),
    coretest.WithAppOptions(core.WithCache("default", fakeCache)),
)
```

capability ที่สลับได้: `WithCache`, `WithMQ`, `WithRequester`, `WithMongo`,
`WithLogger`, `WithSentry`

**capability ที่ไม่ได้ตั้ง จะคืน `nil` ไม่ panic** โค้ดที่เขียนแบบนี้จึงเทสได้ทั้ง
สองทางโดยไม่ต้องปลอมอะไรเลย:

```go
if mq := ctx.MQ(); mq != nil {
    ...
}
```

ยกเว้น cache: `ctx.Cache()` ไม่เคยเป็น `nil` — ถ้าไม่ได้ตั้งจะได้ handle ที่อ่าน miss
และเขียนทิ้ง ทำให้ `core.Remember` ทำงานได้ทั้งสองทางโดยไม่ต้องเช็ค ถ้าอยากได้ cache
จริงในเทสให้ใส่ `core.NewMemoryCache()` ซึ่งเป็นของจริงทั้ง TTL, counter, lock และ
pub/sub แต่อยู่ในโปรเซสเดียว:

```go
ctx := coretest.NewContext(t,
    coretest.WithAppOptions(core.WithCache("default", core.NewMemoryCache())),
)
```

## ปลอม HTTP client ขาออก

service ที่เรียก API ภายนอกใช้ `core.Requester(ctx)` — ปลอมได้ด้วย `httptest.Server`
ซึ่งเป็นของจริงทั้งสองฝั่ง แค่ปลายทางเป็นของเรา

```go
func TestPaymentService_charge(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/charges", r.URL.Path)   // ยืนยันสิ่งที่เราส่งไป

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"ch_123","status":"succeeded"}`))
	}))
	defer upstream.Close()

	ctx := coretest.NewContext(t, coretest.WithEnv(map[string]string{
		"payment_base_url": upstream.URL,      // ชี้ service ไปที่ตัวปลอม
	}))

	res, err := services.NewPaymentService(ctx).Charge(payload)

	coretest.RequireNoError(t, err)
	assert.Equal(t, "ch_123", res.ID)
}
```

วิธีนี้ดีกว่าการ mock ตัว `IRequester` เพราะได้ทดสอบการ encode request, header,
และการ decode response ของจริงด้วย — ซึ่งเป็นจุดที่พลาดกันบ่อยกว่าตรรกะ

### บังคับให้ upstream พัง

```go
t.Run("upstream returns 500", func(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	_, err := chargeVia(t, upstream.URL)

	coretest.RequireStatus(t, err, http.StatusBadGateway)   // แปลงเป็น error ของเราแล้ว
})

t.Run("upstream times out", func(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer upstream.Close()
	...
})
```

นี่คือเหตุผลหลักที่ต้องปลอม: สั่ง upstream จริงให้ตอบ 500 หรือค้างไม่ได้

## ปลอมด้วย interface ของตัวเอง

ถ้า service พึ่งพาอะไรที่ไม่ใช่ capability ของ framework ให้ประกาศ interface
**ฝั่งผู้ใช้** แล้วรับเข้ามา

```go
// ประกาศไว้ที่ฝั่งที่ใช้ ไม่ใช่ฝั่งที่ implement
type Notifier interface {
    Notify(ctx core.IContext, userID, message string) core.IError
}

type orderService struct {
	ctx      core.IContext
	notifier Notifier
}
```

ตัวปลอมที่บันทึกสิ่งที่ถูกเรียก:

```go
type fakeNotifier struct {
	sent []string
	err  core.IError        // ตั้งเพื่อบังคับให้ล้มเหลว
}

func (f *fakeNotifier) Notify(_ core.IContext, userID, message string) core.IError {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, userID+": "+message)
	return nil
}
```

```go
func TestOrderService_notifiesOnPlacement(t *testing.T) {
	ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&models.Order{}))
	notifier := &fakeNotifier{}

	svc := services.NewOrderService(ctx, notifier)
	_, err := svc.Place(payload)

	coretest.RequireNoError(t, err)
	require.Len(t, notifier.sent, 1)
	assert.Contains(t, notifier.sent[0], "order placed")
}

// สิ่งที่ปลอมแล้วเทสได้ง่ายที่สุด: เมื่อของข้างนอกพัง เราทำตัวยังไง
func TestOrderService_notificationFailureDoesNotLoseTheOrder(t *testing.T) {
	ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&models.Order{}))
	notifier := &fakeNotifier{err: errmsgs.InternalServerError}

	order, err := services.NewOrderService(ctx, notifier).Place(payload)

	coretest.RequireNoError(t, err, "การแจ้งเตือนล้มเหลวต้องไม่ทำให้คำสั่งซื้อหาย")
	assert.NotEmpty(t, order.ID)
})
```

เทสตัวหลังคือคุณค่าที่แท้จริงของการปลอม — กติกาที่ว่า "แจ้งเตือนพังแต่ออเดอร์ต้องอยู่"
พิสูจน์ด้วยของจริงไม่ได้เลย

> ประกาศ interface ที่ฝั่งผู้ใช้ ไม่ใช่ฝั่ง implement — ผู้ใช้รู้ว่าตัวเองต้องการ
> อะไร ส่วนผู้ให้บริการมักมี method เยอะกว่าที่จำเป็น interface เล็กๆ ทำให้ตัวปลอม
> สั้นและเปลี่ยนตามน้อยลง

## เวลาและค่าสุ่ม

อย่าเรียก `time.Now()` ตรงๆ ในตรรกะที่ต้องเทส — รับเข้ามาแทน

```go
type reportService struct {
	ctx core.IContext
	now func() time.Time      // เทสสั่งได้
}

func NewReportService(ctx core.IContext) *reportService {
	return &reportService{ctx: ctx, now: time.Now}
}
```

```go
svc := services.NewReportService(ctx)
svc.SetClock(func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) })
```

ถ้าเปลี่ยนโครงสร้างไม่ได้ ก็ยอมให้ผลลัพธ์ยืดหยุ่นแทนการ freeze เวลา:

```go
assert.WithinDuration(t, time.Now(), *user.CreatedAt, 5*time.Second)
```

## ไม่ต้องปลอม Sentry

ถ้าไม่ได้ตั้ง `SENTRY_DSN` tracker เป็น no-op อยู่แล้ว เทสจึงไม่ยิงออกเน็ตโดยไม่
ต้องทำอะไร

ถ้าอยากยืนยันว่ามีการรายงานจริง ให้ใส่ตัวบันทึกผ่าน `WithAppOptions(core.WithSentry(...))`
แล้วตรวจ event ที่เก็บไว้

## สรุปการตัดสินใจ

```
เป็นตรรกะล้วน?              → เรียกตรงๆ ไม่ต้องปลอม   (unit)
เป็น database / repository? → ใช้ของจริง             (integration)
เป็น API ภายนอก?            → httptest.Server
เป็นบริการภายนอกอื่น?        → interface + ตัวปลอม
เป็นเวลา / ค่าสุ่ม?          → รับเข้ามาเป็น dependency
```

ทุกครั้งที่จะปลอมอะไร ถามก่อนว่า **"ถ้าของจริงพฤติกรรมไม่ตรงกับที่ปลอมไว้ เทสนี้จะรู้ไหม"**
ถ้าคำตอบคือไม่รู้ ให้พิจารณาใช้ของจริงแทน
