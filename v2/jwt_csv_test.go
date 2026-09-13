package core

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type myClaims struct {
	UserID string `json:"user_id"`
	jwt.RegisteredClaims
}

func TestJWT_signVerifyRoundTrip(t *testing.T) {
	secret := "s3cr3t"
	tokenStr, err := JWTSign(&myClaims{
		UserID:           "u-1",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}, secret)
	require.NoError(t, err)

	var got myClaims
	require.NoError(t, JWTVerify(tokenStr, secret, &got))
	assert.Equal(t, "u-1", got.UserID)
}

func TestJWT_rejectsWrongSecretAndExpiry(t *testing.T) {
	tokenStr, _ := JWTSign(&myClaims{UserID: "u-1",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}, "right")

	var got myClaims
	assert.Error(t, JWTVerify(tokenStr, "wrong", &got), "wrong secret must fail")

	expired, _ := JWTSign(&myClaims{UserID: "u-1",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))}}, "right")
	assert.Error(t, JWTVerify(expired, "right", &myClaims{}), "expired token must fail")
}

type csvRow struct {
	Name   string `csv:"name"`
	Age    int    `csv:"age"`
	Active bool   `csv:"active"`
	Secret string `csv:"-"`
}

func TestCSV_roundTrip(t *testing.T) {
	rows := []csvRow{
		{Name: "alice", Age: 30, Active: true, Secret: "x"},
		{Name: "bob", Age: 25, Active: false, Secret: "y"},
	}
	var buf bytes.Buffer
	require.NoError(t, CSVMarshal(&buf, rows))
	assert.NotContains(t, buf.String(), "Secret", `csv:"-" field must be skipped`)
	assert.NotContains(t, buf.String(), ",x", `csv:"-" value must be skipped`)

	got, err := CSVUnmarshal[csvRow](&buf)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "alice", got[0].Name)
	assert.Equal(t, 30, got[0].Age)
	assert.True(t, got[0].Active)
}

type csvTimeRow struct {
	Name      string     `csv:"name"`
	CreatedAt time.Time  `csv:"created_at"`
	DeletedAt *time.Time `csv:"deleted_at"`
	Score     *int       `csv:"score"`
}

// A time field used to render as an empty column: reflect.Kind sees a struct and
// the switch fell through to "". Every date in every export was lost, silently.
func TestCSV_timeAndPointerFields(t *testing.T) {
	created := time.Date(2026, 7, 30, 9, 30, 0, 0, time.UTC)
	deleted := created.Add(48 * time.Hour)
	score := 42

	rows := []csvTimeRow{
		{Name: "alice", CreatedAt: created, DeletedAt: &deleted, Score: &score},
		{Name: "bob", CreatedAt: created}, // nil pointers, no deletion
	}

	var buf bytes.Buffer
	require.NoError(t, CSVMarshal(&buf, rows))
	assert.Contains(t, buf.String(), created.Format(time.RFC3339), "the date must reach the file")

	got, err := CSVUnmarshal[csvTimeRow](&buf)
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.True(t, created.Equal(got[0].CreatedAt))
	require.NotNil(t, got[0].DeletedAt)
	assert.True(t, deleted.Equal(*got[0].DeletedAt))
	require.NotNil(t, got[0].Score)
	assert.Equal(t, 42, *got[0].Score)

	assert.Nil(t, got[1].DeletedAt, "an empty cell reads back as nil, not as an error")
	assert.Nil(t, got[1].Score, "an empty cell in a number column is not a parse failure")
}

type csvUnsupportedRow struct {
	Name string   `csv:"name"`
	Tags []string `csv:"tags"`
}

func TestCSV_unsupportedFieldIsAnError(t *testing.T) {
	var buf bytes.Buffer
	err := CSVMarshal(&buf, []csvUnsupportedRow{{Name: "a", Tags: []string{"x"}}})
	require.Error(t, err, "a column that cannot be rendered must fail, not come out empty")
	assert.Contains(t, err.Error(), "tags")
}

func TestCSV_nonStructTypeIsAnErrorNotAPanic(t *testing.T) {
	var buf bytes.Buffer
	err := CSVMarshal(&buf, []string{"a", "b"})
	require.Error(t, err)
	assert.Equal(t, "CSV_INVALID_TYPE", err.GetCode())

	_, rerr := CSVUnmarshal[int](strings.NewReader("1\n2\n"))
	require.Error(t, rerr)
	assert.Equal(t, "CSV_INVALID_TYPE", rerr.GetCode())
}

// Excel decides a CSV's encoding from the byte-order mark. Without one it reads
// the system code page, and every Thai character becomes mojibake.
func TestCSV_bomIsWrittenAndReadBack(t *testing.T) {
	rows := []csvRow{{Name: "สมชาย", Age: 30, Active: true}}

	var buf bytes.Buffer
	require.NoError(t, CSVMarshal(&buf, rows, CSVOptions{BOM: true}))
	assert.True(t, strings.HasPrefix(buf.String(), "\ufeff"), "Excel needs the BOM")
	assert.Contains(t, buf.String(), "สมชาย")

	// and reading it back must not leave the mark glued to the first heading
	got, err := CSVUnmarshal[csvRow](bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "สมชาย", got[0].Name)
	assert.Equal(t, 30, got[0].Age, "a BOM must not stop the first column from matching")
}

func TestCSV_noBOMByDefault(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, CSVMarshal(&buf, []csvRow{{Name: "a"}}))
	assert.False(t, strings.HasPrefix(buf.String(), "\ufeff"),
		"a file another program parses should not start with a mark it does not expect")
}

func TestCSV_customSeparator(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, CSVMarshal(&buf, []csvRow{{Name: "a", Age: 1}}, CSVOptions{Comma: ';'}))
	assert.Contains(t, buf.String(), "name;age;active")

	got, err := CSVUnmarshal[csvRow](&buf, CSVOptions{Comma: ';'})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Name)
}

// A heading the struct does not know must not shift every column after it.
func TestCSV_unknownColumnIsIgnored(t *testing.T) {
	in := "name,surprise,age,active\nalice,ignored,30,true\n"

	got, err := CSVUnmarshal[csvRow](strings.NewReader(in))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "alice", got[0].Name)
	assert.Equal(t, 30, got[0].Age, "the columns after an unknown heading must still line up")
	assert.True(t, got[0].Active)
}

// The streaming pair: an export whose size is the database's business, and an
// import that never holds the whole file.
func TestCSV_streaming(t *testing.T) {
	var buf bytes.Buffer
	cw, err := NewCSVWriter[csvRow](&buf)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		require.NoError(t, cw.Write(csvRow{Name: "u" + strconv.Itoa(i), Age: i}))
	}
	require.NoError(t, cw.Flush())

	seen := make([]string, 0, 3)
	require.NoError(t, CSVEach(bytes.NewReader(buf.Bytes()), func(r csvRow) error {
		seen = append(seen, r.Name)
		return nil
	}))
	assert.Equal(t, []string{"u0", "u1", "u2"}, seen)
}

func TestCSV_eachStopsOnCallerError(t *testing.T) {
	in := "name,age,active\na,1,true\nb,2,true\nc,3,true\n"

	count := 0
	err := CSVEach(strings.NewReader(in), func(csvRow) error {
		count++
		if count == 2 {
			return errors.New("enough")
		}
		return nil
	})
	require.Error(t, err)
	assert.Equal(t, 2, count, "the read stops where the caller stopped it")
}

func TestCSV_emptyWriterStillProducesAHeader(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, CSVMarshal(&buf, []csvRow{}))
	assert.Equal(t, "name,age,active\n", buf.String())
}

type CSVAudit struct {
	CreatedAt time.Time  `csv:"created_at"`
	UpdatedAt *time.Time `csv:"updated_at"`
}

type csvEmbedded struct {
	CSVAudit
	Name string    `csv:"name"`
	Day  time.Time `csv:"day,format:2006-01-02"`
}

// A model that embeds a Timestamps struct should export the columns it has, not
// one column named after the embedded type.
func TestCSV_embeddedStructIsFlattened(t *testing.T) {
	created := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)
	day := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)

	var buf bytes.Buffer
	require.NoError(t, CSVMarshal(&buf, []csvEmbedded{
		{CSVAudit: CSVAudit{CreatedAt: created, UpdatedAt: &updated}, Name: "ann", Day: day},
	}))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	assert.Equal(t, "created_at,updated_at,name,day", strings.TrimSpace(lines[0]))

	got, err := CSVUnmarshal[csvEmbedded](bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, created.Equal(got[0].CreatedAt))
	require.NotNil(t, got[0].UpdatedAt)
	assert.True(t, updated.Equal(*got[0].UpdatedAt))
	assert.Equal(t, "ann", got[0].Name)
}

// A per-field format is what makes a date column readable in a report without
// changing every other time in the file.
func TestCSV_perFieldTimeFormat(t *testing.T) {
	day := time.Date(2026, 7, 30, 13, 45, 0, 0, time.UTC)

	var buf bytes.Buffer
	require.NoError(t, CSVMarshal(&buf, []csvEmbedded{
		{Day: day, CSVAudit: CSVAudit{CreatedAt: day}},
	}))

	body := buf.String()
	assert.Contains(t, body, ",2026-07-30\n", "the tagged column uses its own layout")
	assert.Contains(t, body, day.Format(time.RFC3339), "the others keep the file's")

	got, err := CSVUnmarshal[csvEmbedded](bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "2026-07-30", got[0].Day.Format("2006-01-02"),
		"and the same layout reads it back")
}

type csvEmbeddedPtr struct {
	*CSVAudit
	Name string `csv:"name"`
}

// A nil embedded pointer is a group of empty cells, not a crash — and reading
// one back allocates it only when there is something to put in it.
func TestCSV_nilEmbeddedPointer(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, CSVMarshal(&buf, []csvEmbeddedPtr{{Name: "ann"}}))
	assert.Contains(t, buf.String(), ",,ann")

	got, err := CSVUnmarshal[csvEmbeddedPtr](bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ann", got[0].Name)
}
