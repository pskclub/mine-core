package utils_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pskclub/mine-core/v2/utils"
)

func TestSliceHelpers(t *testing.T) {
	assert.Equal(t, []string{"a", "", "c"},
		utils.DerefSlice([]*string{utils.ToPointer("a"), nil, utils.ToPointer("c")}))

	ptrs := utils.PtrSlice([]int{1, 2})
	require.Len(t, ptrs, 2)
	assert.Equal(t, 1, *ptrs[0])

	assert.Equal(t, []int{2, 4}, utils.Map([]int{1, 2}, func(n int) int { return n * 2 }))
	assert.Equal(t, []int{2, 4}, utils.Filter([]int{1, 2, 3, 4}, func(n int) bool { return n%2 == 0 }))
	assert.True(t, utils.Contains([]string{"a", "b"}, "b"))
	assert.False(t, utils.Contains([]string{"a", "b"}, "z"))
	assert.Equal(t, []int{1, 2, 3}, utils.Unique([]int{1, 2, 2, 3, 1}))
}

func TestJSONHelpers(t *testing.T) {
	type person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	src := person{Name: "alice", Age: 30}

	assert.JSONEq(t, `{"name":"alice","age":30}`, utils.JSONToString(src))
	assert.Contains(t, utils.StructToString(src), "\n") // pretty

	m, err := utils.StructToMap(src)
	require.NoError(t, err)
	assert.Equal(t, "alice", m["name"])

	got, err := utils.MapTo[person](map[string]any{"name": "carol", "age": 40})
	require.NoError(t, err)
	assert.Equal(t, "carol", got.Name)

	parsed, err := utils.JSONParse[person]([]byte(`{"name":"dave","age":50}`))
	require.NoError(t, err)
	assert.Equal(t, person{Name: "dave", Age: 50}, parsed)

	ids, err := utils.JSONParse[[]string]([]byte(`["a","b"]`))
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, ids)

	_, err = utils.JSONParse[person]([]byte(`{"age":"not-int"}`))
	require.EqualError(t, err, "the age field must be int type")

	_, err = utils.JSONParse[person]([]byte(`not json`))
	require.EqualError(t, err, "must be json format")
}

func TestIsEmptyIsZero(t *testing.T) {
	assert.True(t, utils.IsEmpty[any](nil))
	assert.True(t, utils.IsEmpty(""))
	assert.True(t, utils.IsEmpty(0))
	assert.False(t, utils.IsEmpty("x"))
	assert.True(t, utils.IsEmpty([]string(nil)), "nil slice is empty")
	assert.False(t, utils.IsEmpty([]string{}), "empty-but-allocated slice is not")
	assert.True(t, utils.IsEmpty((*int)(nil)))
	assert.True(t, utils.IsZero(0))
	assert.False(t, utils.IsZero(3))
}

func TestUUID(t *testing.T) {
	id := utils.NewUUID()
	assert.True(t, utils.IsUUID(id), "generated UUID must validate")
	assert.False(t, utils.IsUUID("not-a-uuid"))
	_, err := utils.ParseUUID(id)
	assert.NoError(t, err)
}

func TestNowPtr(t *testing.T) {
	assert.NotNil(t, utils.NowPtr())
}

func TestCopy(t *testing.T) {
	type from struct{ Name string }
	type to struct{ Name string }

	dst, err := utils.Copy[to](from{Name: "alice"})
	require.NoError(t, err)
	assert.Equal(t, "alice", dst.Name)

	list, err := utils.Copy[[]to]([]from{{Name: "a"}, {Name: "b"}})
	require.NoError(t, err)
	assert.Equal(t, []to{{Name: "a"}, {Name: "b"}}, list)
}

func TestCrypto_hashesAndEncoding(t *testing.T) {
	assert.Len(t, utils.SHA256("x"), 64)
	assert.Len(t, utils.SHA384("x"), 96)
	assert.Len(t, utils.SHA512("x"), 128)
	assert.Len(t, utils.MD5("x"), 32)
	assert.Equal(t, utils.SHA256("x"), utils.SHA256("x"), "deterministic")

	enc := utils.Base64Encode("hello")
	dec, err := utils.Base64Decode(enc)
	require.NoError(t, err)
	assert.Equal(t, "hello", dec)

	h := utils.HexEncode([]byte{0xde, 0xad})
	assert.Equal(t, "dead", h)
	b, err := utils.HexDecode(h)
	require.NoError(t, err)
	assert.Equal(t, []byte{0xde, 0xad}, b)
}

func TestCrypto_password(t *testing.T) {
	hash, err := utils.HashPassword("s3cret")
	require.NoError(t, err)
	assert.True(t, utils.ComparePassword(hash, "s3cret"))
	assert.False(t, utils.ComparePassword(hash, "wrong"))
}
