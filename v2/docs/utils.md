# Utils (pointer & conversion helpers)

`github.com/pskclub/mine-core/v2/utils` — small, dependency-free generics for
the pointer/enum juggling that request→model mapping needs.

## Pointer basics

```go
p := utils.ToPointer("hello")          // string  → *string
v := utils.ToNonPointer(p)             // *string → string (zero value if nil)
v := utils.ToNonPointerOr(p, "def")    // *string → string, "def" if nil
```

## Converting enum pointers

A request DTO usually carries `*string`, while the model wants `*SomeEnum` (a
`type SomeEnum string`). Instead of the verbose deref → convert → re-pointer:

```go
// before — long and easy to get wrong
var projectType *models.PMOProjectType
if input.Type != nil {
    projectType = utils.ToPointer(models.PMOProjectType(utils.ToNonPointer(input.Type)))
}

// after — one nil-safe call
projectType := utils.PtrConvert[models.PMOProjectType](input.Type)
```

Inline in a struct literal it stays short:

```go
pmoProject := &models.PMOProject{
    BaseModel:   models.NewBaseModel(),
    Name:        input.Name,
    Status:      models.PMOProjectStatusIntro,
    Type:        utils.PtrConvert[models.PMOProjectType](input.Type), // *string → *PMOProjectType
    Code:        input.Code,
    CreatedByID: utils.ToPointer(s.ctx.GetUser().ID),
}
```

It works both ways (e.g. building an output DTO):

```go
resp.Type = utils.PtrConvert[string](pmoProject.Type) // *PMOProjectType → *string
```

`R` is given explicitly; the source type is inferred; `nil` in → `nil` out.

## Numeric enums

For `type Priority int` and friends use `PtrConvertNum`:

```go
priority := utils.PtrConvertNum[models.Priority](input.Priority) // *int → *models.Priority
```

## Anything else — MapPtr

For non-trivial conversions (structs, formatting), pass a function; still nil-safe:

```go
createdAt := utils.MapPtr(model.CreatedAt, func(t time.Time) string {
    return t.Format(time.RFC3339)
})
```

## API

```go
func ToPointer[T any](v T) *T
func ToNonPointer[T any](p *T) T
func ToNonPointerOr[T any](p *T, def T) T
func PtrConvert[R ~string, T ~string](p *T) *R    // string enums
func PtrConvertNum[R Numeric, T Numeric](p *T) *R // numeric enums
func MapPtr[T, R any](p *T, fn func(T) R) *R       // custom conversions
```

## Slices

```go
utils.DerefSlice([]*string{p1, nil, p3})      // []*T → []T (zero for nil)
utils.PtrSlice([]int{1, 2})                   // []T  → []*T
utils.Map([]int{1, 2}, func(n int) int { return n*2 })   // → [2 4]
utils.Filter(xs, func(x T) bool { return ... })
utils.Contains([]string{"a", "b"}, "b")       // true
utils.Unique([]int{1, 2, 2, 3})               // [1 2 3]
```

## JSON & structs

Every conversion helper **returns the value** — no `&dst` out-parameter. You name
the result type, the compiler checks it:

```go
user, err := utils.JSONParse[User](body)      // unmarshal, friendly type-error message
ids,  err := utils.JSONParse[[]string](body)  // works for any T, not just structs
dto,  err := utils.MapTo[UserDTO](userModel)  // struct/map → struct via JSON
m,    err := utils.StructToMap(userModel)     // → map[string]any
payload, err := utils.Copy[CreatePayload](input) // deep copy matching fields (jinzhu/copier)
list, err := utils.Copy[[]NoteDTO](notes)        // slices too

utils.JSONToString(v)   // compact JSON string ("" on error)
utils.StructToString(v) // pretty (indented) JSON string
utils.IsEmpty(v)        // nil or zero value (any type, via reflect)
utils.IsZero(v)         // zero value (comparable — no reflection)
```

`JSONParse` reports a mismatched field in a message you can hand to the caller
(`the age field must be int type`), which is why it exists next to plain
`json.Unmarshal`.

`IsEmpty` treats a nil slice/map as empty but an allocated-yet-empty one as not.
For a bare `nil` the type parameter can't be inferred — write `utils.IsEmpty[any](nil)`.

## UUID / time

```go
utils.NewUUID()        // random UUID string
utils.IsUUID(s)        // validate (fixed vs v1)
utils.ParseUUID(s)     // (uuid.UUID, error)
utils.NowPtr()         // *time.Time = now
```

## Crypto / encoding

```go
utils.SHA256(s) / SHA384(s) / SHA512(s)   // hex digests
utils.MD5(s)                              // checksums/etags only (not security)
utils.Base64Encode(s) / Base64Decode(s)
utils.HexEncode(b) / HexDecode(s)
utils.HashPassword(pw)                    // bcrypt → (string, error)
utils.ComparePassword(hash, pw)           // bool
```

## Changes from v1

| v1 | v2 |
|---|---|
| `GetString/GetInt/GetBool/…` | `ToNonPointer[T]` (generic) + `ToNonPointerOr` |
| `GetArrayString` | `DerefSlice[T]` (generic) |
| `GetUUID` | `NewUUID` |
| `GetCurrentDateTime` | `NowPtr` |
| `NewSha256/384/512` | `SHA256/384/512` |
| `GetMD5Hash` | `MD5` |
| `HashPassword` returns `*string` | returns `string` |
| `IsUUID` (broken `FromBytes`) | fixed (`uuid.Parse`) |
| DID / ECDSA-RSA keypairs / sign-verify / `GetBigNumber` (0x/0b) / `MockExplorer` | **removed** — domain-specific, belongs in the consuming service |
| `utils.IsStrIn` / `IsExists` (govalidator+gorm) | **removed** — use the [`valid`](./validation.md) package |
| `JSONParse(body, &dst)` | `JSONParse[T](body) (T, error)` |
| `MapToStruct(src, &dst)` | `MapTo[T](src) (T, error)` |
| `Copy(&dst, src)` | `Copy[T](src) (T, error)` |
| `StructToStringNoPretty` | `JSONToString` |
