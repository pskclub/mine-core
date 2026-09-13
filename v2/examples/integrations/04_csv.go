package main

import (
	"bytes"
	"io"
	"net/http"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 4: CSV ---------------------------------------------------------
//
// Generic marshal/unmarshal over encoding/csv. Columns come from the `csv:"name"`
// tag, falling back to the field name; `csv:"-"` drops a field.
//
// A field of a type with no textual form (a slice, a map, a struct that is not
// time.Time) is an *error* rather than an empty column — a report that quietly
// lost a value is worse than one that failed to build.

// Timestamps is embedded without a name, so it contributes its own columns
// rather than one column named after the type. The embedded type must be
// exported: reflection cannot read through an unexported one without tripping
// over its own safety rules. Give the field a `csv:"name"` tag to keep it as a
// single column instead.
type Timestamps struct {
	CreatedAt time.Time  `csv:"created_at"`
	UpdatedAt *time.Time `csv:"updated_at"` // nil writes an empty cell
}

// SalesRow is one line of the export. `format:` overrides the file's TimeLayout
// for one column, and is used again when the file is read back.
type SalesRow struct {
	Timestamps
	Day    time.Time `csv:"day,format:2006-01-02"`
	Name   string    `csv:"name"`
	Amount float64   `csv:"amount"`
	Paid   bool      `csv:"paid"`
	Note   *string   `csv:"note"` // nil → empty cell
	Secret string    `csv:"-"`    // never leaves the process
}

// exportSmall builds a whole file in memory. Fine for a report whose size you
// control; see exportStreaming for one whose size is the database's business.
func exportSmall(rows []SalesRow) ([]byte, core.IError) {
	var buf bytes.Buffer
	// BOM: Excel decides a file's encoding from the byte-order mark. Without it,
	// it reads in the machine's code page and every Thai character becomes
	// mojibake. It is off by default because a file another *program* parses
	// should not begin with a mark it does not recognise — turn it on only when
	// the reader is a person with Excel.
	if err := core.CSVMarshal(&buf, rows, core.CSVOptions{BOM: true}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// exportHandler writes straight to the response. Nothing is buffered here
// either, so the client starts receiving while the rows are still being read.
func exportHandler(c core.IHTTPContext, next func() ([]SalesRow, core.IError)) error {
	c.Response().Header().Set("Content-Type", "text/csv; charset=utf-8")
	c.Response().Header().Set("Content-Disposition", `attachment; filename="sales.csv"`)
	return exportStreaming(c.Response(), next)
}

// exportStreaming holds one batch in memory at a time.
//
// NewCSVWriter writes the BOM and the header immediately, so an export that
// turns out to have no rows still produces a valid file with its columns — an
// empty CSV and a broken CSV look identical to whoever opens it otherwise.
func exportStreaming(w io.Writer, next func() ([]SalesRow, core.IError)) core.IError {
	cw, err := core.NewCSVWriter[SalesRow](w, core.CSVOptions{BOM: true})
	if err != nil {
		return err
	}

	for {
		batch, bErr := next()
		if bErr != nil {
			return bErr
		}
		if len(batch) == 0 {
			break
		}
		if wErr := cw.Write(batch...); wErr != nil {
			return wErr
		}
	}

	// nothing is guaranteed to have reached w until Flush, and Flush reports the
	// first error of the whole file — so it is never safe to skip
	return cw.Flush()
}

// importAll reads the whole file into a slice. Columns are matched by header
// against the tags, a heading the struct does not know is skipped (and the
// columns after it still line up), and a leading BOM is ignored — so a file
// exported from Excel loads straight back.
func importAll(r io.Reader) ([]SalesRow, core.IError) {
	return core.CSVUnmarshal[SalesRow](r)
}

// importStreaming holds one row at a time, which is what an upload whose size
// you do not control requires. An error from the callback stops the read
// immediately and comes back out.
//
// An empty cell leaves the field at its zero value rather than failing, because
// that is exactly what a spreadsheet writes for "no value".
func importStreaming(ctx core.IContext, r io.Reader) core.IError {
	var imported int
	err := core.CSVEach(r, func(row SalesRow) error {
		if row.Name == "" {
			// returning here abandons the rest of the file — for an import where
			// partial success is acceptable, collect the bad rows instead and
			// report them all at once
			return core.New(http.StatusBadRequest, "INVALID_ROW", "name is required")
		}
		imported++
		return nil
	})
	if err != nil {
		return err
	}
	ctx.Log().Info("imported rows", "count", imported)
	return nil
}

// pagedRows is the shape a real export feeds exportStreaming with: a closure
// over a repository query that returns the next page and finally nothing.
// Keeping it a callback is what lets the writer stay ignorant of where rows come
// from — a table, a Mongo cursor, an upstream API.
func pagedRows(all []SalesRow, size int) func() ([]SalesRow, core.IError) {
	offset := 0
	return func() ([]SalesRow, core.IError) {
		if offset >= len(all) {
			return nil, nil
		}
		end := min(offset+size, len(all))
		batch := all[offset:end]
		offset = end
		return batch, nil
	}
}
