package core

import (
	"encoding"
	"encoding/csv"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// utf8BOM is the byte-order mark Excel looks for to decide a CSV is UTF-8.
// Without it Excel reads the file in the system code page, which turns every
// non-ASCII character — every Thai one — into mojibake. Nothing else needs it,
// which is why it is an option rather than the default.
const utf8BOM = "\ufeff"

// DefaultCSVTimeLayout is how time values are written and read.
const DefaultCSVTimeLayout = time.RFC3339

// CSVOptions tunes marshalling and unmarshalling. The zero value is the default
// behaviour: comma-separated, RFC 3339 times, no BOM.
type CSVOptions struct {
	// BOM writes a UTF-8 byte-order mark before the header. Turn it on for a
	// file a person will open in Excel; leave it off for one another program
	// will parse.
	BOM bool
	// Comma is the field separator (default ',').
	Comma rune
	// TimeLayout is the layout time values are formatted and parsed with
	// (default DefaultCSVTimeLayout). On reading it is tried first, then RFC
	// 3339 and the plain date, so a file exported by a spreadsheet still loads.
	TimeLayout string
	// NoHeader writes no header row, and on reading treats the first row as
	// data mapped to the struct's fields in order.
	NoHeader bool
}

func (o CSVOptions) comma() rune {
	if o.Comma == 0 {
		return ','
	}
	return o.Comma
}

func (o CSVOptions) timeLayout() string {
	if o.TimeLayout == "" {
		return DefaultCSVTimeLayout
	}
	return o.TimeLayout
}

func csvOptions(opts []CSVOptions) CSVOptions {
	if len(opts) > 0 {
		return opts[0]
	}
	return CSVOptions{}
}

// CSVMarshal writes rows as CSV to w. The header is taken from each field's
// `csv:"name"` tag (falling back to the field name); fields tagged `csv:"-"` are
// skipped.
//
// Supported field types: string, bool, the integer and float kinds, time.Time,
// anything implementing encoding.TextMarshaler, and pointers to those (a nil
// pointer writes an empty cell). A field of any other type is an error rather
// than an empty column — a report missing a value is worse than one that failed
// to build.
func CSVMarshal[T any](w io.Writer, rows []T, opts ...CSVOptions) IError {
	cw, err := NewCSVWriter[T](w, opts...)
	if err != nil {
		return err
	}
	if werr := cw.Write(rows...); werr != nil {
		return werr
	}
	return cw.Flush()
}

// CSVWriter streams rows to a CSV file without holding them all in memory — for
// an export whose size is the database's business rather than the process's.
//
//	cw, err := core.NewCSVWriter[Row](w, core.CSVOptions{BOM: true})
//	for _, batch := range batches {
//	    if err := cw.Write(batch...); err != nil { return err }
//	}
//	return cw.Flush()
type CSVWriter[T any] struct {
	cw     *csv.Writer
	cols   []csvColumn
	layout string
	rec    []string
}

// NewCSVWriter starts a CSV file: it writes the BOM and the header immediately,
// so a caller that writes no rows still produces a valid file.
func NewCSVWriter[T any](w io.Writer, opts ...CSVOptions) (*CSVWriter[T], IError) {
	o := csvOptions(opts)

	cols, err := csvColumns(reflect.TypeOf(*new(T)))
	if err != nil {
		return nil, err
	}

	if o.BOM {
		if _, werr := io.WriteString(w, utf8BOM); werr != nil {
			return nil, Wrap(werr, "csv: bom")
		}
	}

	cw := csv.NewWriter(w)
	cw.Comma = o.comma()

	if !o.NoHeader {
		header := make([]string, len(cols))
		for i, c := range cols {
			header[i] = c.name
		}
		if werr := cw.Write(header); werr != nil {
			return nil, Wrap(werr, "csv: header")
		}
	}

	return &CSVWriter[T]{cw: cw, cols: cols, layout: o.timeLayout(), rec: make([]string, len(cols))}, nil
}

// Write appends rows. Nothing is guaranteed to have reached w until Flush.
func (c *CSVWriter[T]) Write(rows ...T) IError {
	for _, row := range rows {
		// an addressable copy, so a MarshalText declared on the pointer receiver
		// is still found — most types declare it that way
		rv := reflect.New(reflect.TypeOf(row)).Elem()
		rv.Set(reflect.ValueOf(row))

		for i, col := range c.cols {
			// a nil embedded pointer is a group of empty cells, not a failure —
			// nothing is allocated on the way out
			field, ok := col.value(rv, false)
			if !ok {
				c.rec[i] = ""
				continue
			}
			cell, err := csvCell(field, col.timeLayout(c.layout))
			if err != nil {
				return Wrapf(err, "csv: field %s", col.name)
			}
			c.rec[i] = cell
		}
		if err := c.cw.Write(c.rec); err != nil {
			return Wrap(err, "csv: write")
		}
	}
	return nil
}

// Flush writes anything buffered and reports the first error of the whole file.
func (c *CSVWriter[T]) Flush() IError {
	c.cw.Flush()
	if err := c.cw.Error(); err != nil {
		return Wrap(err, "csv: flush")
	}
	return nil
}

// CSVUnmarshal reads CSV from r into a slice of T, matching columns by header
// against the `csv` tag / field name. A UTF-8 BOM, if present, is ignored.
//
// It holds the whole result in memory; use CSVEach for a file whose size you do
// not control.
func CSVUnmarshal[T any](r io.Reader, opts ...CSVOptions) ([]T, IError) {
	out := make([]T, 0)
	if err := CSVEach(r, func(row T) error {
		out = append(out, row)
		return nil
	}, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

// CSVEach reads CSV from r and calls fn once per row, holding one row in memory
// at a time. An error from fn stops the read and is returned.
func CSVEach[T any](r io.Reader, fn func(T) error, opts ...CSVOptions) IError {
	o := csvOptions(opts)

	cols, err := csvColumns(reflect.TypeOf(*new(T)))
	if err != nil {
		return err
	}
	byName := make(map[string]csvColumn, len(cols))
	for _, c := range cols {
		byName[c.name] = c
	}

	cr := csv.NewReader(skipBOM(r))
	cr.Comma = o.comma()
	// rows are matched by header, so a ragged file is the caller's business
	cr.FieldsPerRecord = -1

	layout := o.timeLayout()
	// order is the column each position maps to: from the header row, or the
	// struct's own field order when the file carries no header
	order := cols
	first := true

	for {
		rec, rerr := cr.Read()
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return Wrap(rerr, "csv: read")
		}

		if first {
			first = false
			if !o.NoHeader {
				order = headerOrder(rec, byName)
				continue
			}
		}

		var row T
		rv := reflect.ValueOf(&row).Elem()
		for i, col := range order {
			if i >= len(rec) || col.missing() {
				continue
			}
			// allocating on the way in: a value for a field inside an embedded
			// pointer is what makes that pointer needed
			field, _ := col.value(rv, true)
			if serr := setCSVField(field, rec[i], col.timeLayout(layout)); serr != nil {
				return Wrapf(serr, "csv: field %s", col.name)
			}
		}
		if ferr := fn(row); ferr != nil {
			return Wrap(ferr, "csv: row")
		}
	}
}

// headerOrder maps each column of the file to a field of the struct. A heading
// the struct does not have becomes a placeholder, so the columns after it still
// line up.
func headerOrder(header []string, byName map[string]csvColumn) []csvColumn {
	order := make([]csvColumn, len(header))
	for i, name := range header {
		if c, ok := byName[strings.TrimSpace(name)]; ok {
			order[i] = c
			continue
		}
		order[i] = csvColumn{name: name} // no path: a heading nothing maps to
	}
	return order
}

// skipBOM drops a leading UTF-8 byte-order mark, which is what a file exported
// from Excel begins with.
func skipBOM(r io.Reader) io.Reader {
	br := &bomReader{src: r}
	return br
}

type bomReader struct {
	src     io.Reader
	checked bool
	buf     []byte
}

func (b *bomReader) Read(p []byte) (int, error) {
	if !b.checked {
		b.checked = true
		head := make([]byte, 3)
		n, err := io.ReadFull(b.src, head)
		head = head[:n]
		if string(head) != utf8BOM {
			b.buf = head
		}
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return 0, err
		}
	}
	if len(b.buf) > 0 {
		n := copy(p, b.buf)
		b.buf = b.buf[n:]
		return n, nil
	}
	return b.src.Read(p)
}

type csvColumn struct {
	name string
	// path is the field's position, through any embedded structs. Nil for a
	// column of the file that the struct has no field for.
	path []int
	// layout overrides the time format for this column, from `csv:"x,format:…"`.
	layout string
}

// missing reports whether this is a heading the struct has no field for.
func (c csvColumn) missing() bool { return len(c.path) == 0 }

// timeLayout is this column's own format, falling back to the file's.
func (c csvColumn) timeLayout(fallback string) string {
	if c.layout != "" {
		return c.layout
	}
	return fallback
}

// value walks to the field this column names, allocating any nil pointer on the
// way — an embedded *Timestamps is a group of columns, not a reason to fail.
func (c csvColumn) value(root reflect.Value, alloc bool) (reflect.Value, bool) {
	v := root
	for i, idx := range c.path {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !alloc {
					return reflect.Value{}, false
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(idx)
	}
	return v, true
}

// csvColumns describes the columns of T, refusing a type that has none to
// describe — a mistake that used to be a panic inside reflect.
//
// An embedded struct contributes its own fields rather than a column of its
// own, so the `Model`/`Timestamps` a repository model embeds appear as the
// columns they are. The embedded type must be exported: reflection cannot read
// through an unexported one without tripping over its own safety rules, and
// silently exporting less than was asked for is worse than not flattening.
// Give the embedded field a `csv:"name"` tag to keep it as one column instead.
func csvColumns(t reflect.Type) ([]csvColumn, IError) {
	if t == nil || t.Kind() != reflect.Struct {
		return nil, Newf(500, "CSV_INVALID_TYPE", "csv: rows must be a struct, got %s", csvTypeName(t))
	}
	return collectCSVColumns(t, nil, map[reflect.Type]bool{}), nil
}

func collectCSVColumns(t reflect.Type, prefix []int, seen map[reflect.Type]bool) []csvColumn {
	// a struct that embeds its own type (directly or through a pointer) would
	// otherwise describe an infinite set of columns
	if seen[t] {
		return nil
	}
	seen[t] = true
	defer delete(seen, t)

	cols := make([]csvColumn, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("csv")
		if tag == "-" {
			continue
		}

		path := append(append([]int(nil), prefix...), i)

		// an embedded struct with no name of its own is flattened; one that was
		// given a `csv:"…"` name is a single column, rendered by its own
		// MarshalText if it has one
		if f.Anonymous && tag == "" {
			if ft := derefType(f.Type); ft.Kind() == reflect.Struct && ft != timeType {
				cols = append(cols, collectCSVColumns(ft, path, seen)...)
				continue
			}
		}

		name, layout := parseCSVTag(tag)
		if name == "" {
			name = f.Name
		}
		cols = append(cols, csvColumn{name: name, path: path, layout: layout})
	}
	return cols
}

// parseCSVTag reads `csv:"name,format:2006-01-02"`.
func parseCSVTag(tag string) (name, layout string) {
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, opt := range parts[1:] {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(opt), "format:"); ok {
			layout = rest
		}
	}
	return name, layout
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func csvTypeName(t reflect.Type) string {
	if t == nil {
		return "nil"
	}
	return t.String()
}

var (
	timeType            = reflect.TypeOf(time.Time{})
	textMarshalerType   = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

// csvCell renders one field. A nil pointer and a zero time are empty cells; a
// type with no textual form is an error rather than a silently empty column.
func csvCell(v reflect.Value, layout string) (string, error) {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "", nil
		}
		v = v.Elem()
	}

	if v.Type() == timeType {
		t := v.Interface().(time.Time)
		if t.IsZero() {
			return "", nil
		}
		return t.Format(layout), nil
	}

	// a type that knows how to write itself (uuid, decimal, an enum) is asked
	// before the kind switch, which would otherwise render its underlying type
	if m, ok := textMarshaler(v); ok {
		b, err := m.MarshalText()
		if err != nil {
			return "", err
		}
		return string(b), nil
	}

	switch v.Kind() {
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64), nil
	default:
		// silence here is what used to lose every time.Time in an export
		return "", Newf(500, "CSV_INVALID_TYPE", "csv: cannot write a %s to a cell", v.Type())
	}
}

// textMarshaler finds a MarshalText declared on either the value or the pointer
// receiver — rows are copied into an addressable value so both are reachable.
func textMarshaler(v reflect.Value) (encoding.TextMarshaler, bool) {
	if v.Type().Implements(textMarshalerType) {
		m, ok := v.Interface().(encoding.TextMarshaler)
		return m, ok
	}
	if v.CanAddr() && reflect.PointerTo(v.Type()).Implements(textMarshalerType) {
		m, ok := v.Addr().Interface().(encoding.TextMarshaler)
		return m, ok
	}
	return nil, false
}

// setCSVField parses one cell into a field. An empty cell leaves the field at
// its zero value (nil for a pointer) rather than failing, because that is what a
// spreadsheet writes for "no value".
func setCSVField(v reflect.Value, s string, layout string) error {
	if v.Kind() == reflect.Pointer {
		if s == "" {
			v.Set(reflect.Zero(v.Type()))
			return nil
		}
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		v = v.Elem()
	}

	if v.Type() == timeType {
		if s == "" {
			v.Set(reflect.Zero(timeType))
			return nil
		}
		t, err := parseCSVTime(s, layout)
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(t))
		return nil
	}

	if v.CanAddr() && reflect.PointerTo(v.Type()).Implements(textUnmarshalerType) {
		if s == "" {
			return nil
		}
		return v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(s))
	}

	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		if s == "" {
			return nil
		}
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if s == "" {
			return nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if s == "" {
			return nil
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		if s == "" {
			return nil
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		v.SetFloat(n)
	default:
		return Newf(500, "CSV_INVALID_TYPE", "csv: cannot read a %s from a cell", v.Type())
	}
	return nil
}

// parseCSVTime tries the configured layout, then the two forms a spreadsheet or
// an API is most likely to have written.
func parseCSVTime(s, layout string) (time.Time, error) {
	layouts := []string{layout, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"}
	var err error
	for _, l := range layouts {
		var t time.Time
		if t, err = time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, err
}
