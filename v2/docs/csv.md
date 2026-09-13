# CSV

Generic CSV marshal/unmarshal over the standard library's `encoding/csv`.
Columns come from the `csv:"name"` struct tag (falling back to the field name);
`csv:"-"` skips a field.

## Marshal

```go
type Row struct {
    Name      string    `csv:"name"`
    Age       int64       `csv:"age"`
    Active    bool      `csv:"active"`
    CreatedAt time.Time `csv:"created_at"`
    Secret    string    `csv:"-"` // omitted
}

rows := []Row{{Name: "alice", Age: 30, Active: true, CreatedAt: time.Now()}}

var buf bytes.Buffer
if err := core.CSVMarshal(&buf, rows); err != nil {
    return err
}
// buf:
// name,age,active,created_at
// alice,30,true,2026-07-30T09:30:00Z
```

Writing to an HTTP response:

```go
func Export(c core.IHTTPContext) error {
    c.Response().Header().Set("Content-Type", "text/csv")   // http.ResponseWriter
    return core.CSVMarshal(c.Response(), rows)
}
```

## ไฟล์ที่คนเปิดด้วย Excel

Excel ตัดสินใจว่าไฟล์เป็น encoding อะไรจาก **byte-order mark** ไม่มี BOM มันจะอ่าน
ด้วย code page ของเครื่อง แล้วภาษาไทยทุกตัวกลายเป็นขยะ:

```go
core.CSVMarshal(w, rows, core.CSVOptions{BOM: true})
```

default คือ **ไม่ใส่** — ไฟล์ที่โปรแกรมอื่น parse ไม่ควรขึ้นต้นด้วย mark ที่มันไม่รู้จัก
ใส่เมื่อปลายทางคือคนกับ Excel เท่านั้น

ฝั่งอ่านไม่ต้องสั่งอะไร: `CSVUnmarshal`/`CSVEach` ข้าม BOM ให้เองอยู่แล้ว ไฟล์ที่
export ออกจาก Excel จึงโหลดกลับได้ตรงๆ

## Unmarshal

Columns are matched by header name against the tags. หัวคอลัมน์ที่ struct ไม่รู้จักจะ
ถูกข้าม โดยคอลัมน์ที่เหลือยังเรียงตรงเหมือนเดิม:

```go
rows, err := core.CSVUnmarshal[Row](reader)
```

## ไฟล์ที่ใหญ่เกินจะถือทั้งก้อน

`CSVMarshal`/`CSVUnmarshal` ถือทั้งไฟล์ไว้ใน memory — พอสำหรับรายงานปกติ แต่ไม่ใช่
สำหรับ export ที่ขนาดขึ้นกับจำนวนแถวใน database คู่ที่ stream ทีละแถว:

```go
// เขียน: header ออกทันทีตอนสร้าง แล้วป้อนทีละ batch
cw, err := core.NewCSVWriter[Row](w, core.CSVOptions{BOM: true})
if err != nil {
    return err
}
for {
    batch, err := next()
    if err != nil || len(batch) == 0 {
        break
    }
    if err := cw.Write(batch...); err != nil {
        return err
    }
}
return cw.Flush()
```

```go
// อ่าน: หนึ่งแถวใน memory ต่อครั้ง — error จาก callback หยุดการอ่านทันที
err := core.CSVEach(r, func(row Row) error {
    return importOne(ctx, row)
})
```

## Options

```go
type CSVOptions struct {
    BOM        bool   // ใส่ UTF-8 BOM ให้ Excel (default: ไม่ใส่)
    Comma      rune   // ตัวคั่น (default ',')
    TimeLayout string // layout ของ time (default RFC 3339)
    NoHeader   bool   // ไม่มีแถว header — map ตามลำดับ field
}
```

`TimeLayout` ใช้ตอนเขียน ส่วนตอนอ่านจะลอง layout นี้ก่อน แล้วตกไป RFC 3339,
`2006-01-02 15:04:05` และ `2006-01-02` ตามลำดับ ไฟล์ที่ spreadsheet เขียนจึงโหลดได้

## API

```go
func CSVMarshal[T any](w io.Writer, rows []T, opts ...CSVOptions) IError
func CSVUnmarshal[T any](r io.Reader, opts ...CSVOptions) ([]T, IError)

func NewCSVWriter[T any](w io.Writer, opts ...CSVOptions) (*CSVWriter[T], IError)
func (c *CSVWriter[T]) Write(rows ...T) IError
func (c *CSVWriter[T]) Flush() IError

func CSVEach[T any](r io.Reader, fn func(T) error, opts ...CSVOptions) IError
```

## Embedded struct

struct ที่ embed มาแบบไม่มีชื่อ จะกลายเป็น**คอลัมน์ของมัน** ไม่ใช่คอลัมน์เดียวที่ชื่อตาม
type — model ที่ embed `Timestamps` จึง export คอลัมน์ที่มันมีจริง:

```go
type Timestamps struct {
    CreatedAt time.Time  `csv:"created_at"`
    UpdatedAt *time.Time `csv:"updated_at"`
}

type User struct {
    Timestamps
    Name string `csv:"name"`
}
// → created_at,updated_at,name
```

type ที่ embed ต้อง **exported** — reflection อ่านทะลุ type ที่ไม่ exported ไม่ได้โดยไม่
ชนกฎความปลอดภัยของตัวเอง และการ export ออกมาน้อยกว่าที่สั่งแบบเงียบๆ แย่กว่าการ
ไม่ flatten เลย ถ้าอยากให้เป็นคอลัมน์เดียว ใส่ tag `csv:"name"` ให้ field ที่ embed

embedded pointer ที่เป็น nil = กลุ่มของ cell ว่าง ไม่ใช่ error และตอนอ่านกลับจะ allocate
ให้ต่อเมื่อมีค่าจะใส่จริง

## Format ต่อ field

```go
type Report struct {
    Day       time.Time `csv:"day,format:2006-01-02"`
    CreatedAt time.Time `csv:"created_at"`     // ใช้ TimeLayout ของไฟล์
}
```

layout เดียวกันนี้ถูกใช้ตอนอ่านกลับด้วย

## ชนิดของ field ที่รองรับ

`string`, `bool`, int/uint ทุกขนาด, float, `time.Time`, อะไรก็ตามที่ implement
`encoding.TextMarshaler`/`TextUnmarshaler` (เช่น `uuid.UUID`) และ **pointer ของทุกตัว
ข้างต้น**

- pointer ที่เป็น nil และ `time.Time` ที่เป็น zero → cell ว่าง
- cell ว่างตอนอ่าน → zero value (nil สำหรับ pointer) ไม่ใช่ error — เพราะนั่นคือสิ่งที่
  spreadsheet เขียนเวลาไม่มีค่า
- field ที่เป็นชนิดอื่น (slice, map, struct ที่ไม่ใช่ `time.Time`) → **error** ไม่ใช่
  คอลัมน์ว่าง รายงานที่ค่าหายเงียบๆ แย่กว่ารายงานที่สร้างไม่สำเร็จ
- `T` ที่ไม่ใช่ struct → `CSV_INVALID_TYPE` (เดิม panic อยู่ใน reflect)
