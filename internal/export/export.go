// Package export writes rows as CSV, SQL INSERT statements or JSON. The
// Writer types stream row by row; CSV, SQL and JSON convert a ResultSet.
package export

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gotable/internal/db"
)

// QuoteMode selects when CSV fields are quoted.
type QuoteMode int

const (
	QuoteAuto   QuoteMode = iota // only when needed
	QuoteAlways                  // every non-NULL field
)

// CSVOptions configures CSV output.
type CSVOptions struct {
	Header    bool
	Delimiter rune
	Quote     QuoteMode
	LineBreak string // "\n", "\r\n" or "\r"
	// NullAsEmpty writes NULL as an empty field instead of the text NULL.
	NullAsEmpty bool
	// DecimalComma rewrites the decimal point of numeric columns to ','.
	DecimalComma bool
}

// DefaultCSVOptions are the options used when the user changes nothing.
func DefaultCSVOptions() CSVOptions {
	return CSVOptions{Header: true, Delimiter: ',', Quote: QuoteAuto, LineBreak: "\n"}
}

// Writer streams rows in one format.
type Writer interface {
	Begin(columns []string) error
	Row(values []db.Value) error
	End() error
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// CSVWriter streams CSV.
type CSVWriter struct {
	w *bufio.Writer
	o CSVOptions
}

// NewCSVWriter returns a streaming CSV writer.
func NewCSVWriter(w io.Writer, o CSVOptions) *CSVWriter {
	if o.Delimiter == 0 {
		o.Delimiter = ','
	}
	if o.LineBreak == "" {
		o.LineBreak = "\n"
	}
	return &CSVWriter{w: bufio.NewWriterSize(w, 1<<16), o: o}
}

func (c *CSVWriter) field(v db.Value) string {
	if v.Null {
		if c.o.NullAsEmpty {
			return ""
		}
		return "NULL"
	}
	s := v.Text
	if c.o.DecimalComma && isNumeric(s) {
		s = strings.Replace(s, ".", ",", 1)
	}
	delim := string(c.o.Delimiter)
	if c.o.Quote == QuoteAlways || strings.ContainsAny(s, delim+"\"\r\n") || s != strings.TrimSpace(s) {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func (c *CSVWriter) write(vals []db.Value) error {
	for i, v := range vals {
		if i > 0 {
			c.w.WriteRune(c.o.Delimiter)
		}
		c.w.WriteString(c.field(v))
	}
	_, err := c.w.WriteString(c.o.LineBreak)
	return err
}

// Begin writes the header row if enabled.
func (c *CSVWriter) Begin(columns []string) error {
	if !c.o.Header {
		return nil
	}
	hdr := make([]db.Value, len(columns))
	for i, col := range columns {
		hdr[i] = db.Text(col)
	}
	return c.write(hdr)
}

// Row writes one record.
func (c *CSVWriter) Row(values []db.Value) error { return c.write(values) }

// End flushes the output.
func (c *CSVWriter) End() error { return c.w.Flush() }

// JSONWriter streams an array of objects with keys in column order.
type JSONWriter struct {
	w    *bufio.Writer
	keys [][]byte
	n    int
}

// NewJSONWriter returns a streaming JSON writer.
func NewJSONWriter(w io.Writer) *JSONWriter { return &JSONWriter{w: bufio.NewWriterSize(w, 1<<16)} }

// Begin starts the array.
func (j *JSONWriter) Begin(columns []string) error {
	j.keys = make([][]byte, len(columns))
	for i, c := range columns {
		j.keys[i], _ = json.Marshal(c)
	}
	_, err := j.w.WriteString("[")
	return err
}

// Row writes one object. NULL becomes null.
func (j *JSONWriter) Row(values []db.Value) error {
	if j.n > 0 {
		j.w.WriteString(",")
	}
	j.n++
	j.w.WriteString("\n  {")
	for i, v := range values {
		if i > 0 {
			j.w.WriteString(", ")
		}
		j.w.Write(j.keys[i])
		j.w.WriteString(": ")
		if v.Null {
			j.w.WriteString("null")
		} else {
			b, _ := json.Marshal(v.Text)
			j.w.Write(b)
		}
	}
	_, err := j.w.WriteString("}")
	return err
}

// End closes the array and flushes.
func (j *JSONWriter) End() error {
	if j.n > 0 {
		j.w.WriteString("\n")
	}
	j.w.WriteString("]\n")
	return j.w.Flush()
}

// SQLOptions configures INSERT export.
type SQLOptions struct {
	Table db.TableRef
	// BatchSize rows per INSERT statement (<=1 writes one per row).
	BatchSize int
	// CreateDDL is written before the data when set.
	CreateDDL string
}

// SQLWriter streams INSERT statements.
type SQLWriter struct {
	w      *bufio.Writer
	d      db.Dialect
	o      SQLOptions
	prefix string
	batch  []string
}

// NewSQLWriter returns a streaming INSERT writer.
func NewSQLWriter(w io.Writer, d db.Dialect, o SQLOptions) *SQLWriter {
	if o.BatchSize < 1 {
		o.BatchSize = 1
	}
	return &SQLWriter{w: bufio.NewWriterSize(w, 1<<16), d: d, o: o}
}

// Begin writes the optional DDL and prepares the INSERT prefix.
func (s *SQLWriter) Begin(columns []string) error {
	if s.o.CreateDDL != "" {
		fmt.Fprintf(s.w, "%s\n\n", strings.TrimRight(s.o.CreateDDL, "\n"))
	}
	cols := make([]string, len(columns))
	for i, c := range columns {
		cols[i] = s.d.QuoteIdent(c)
	}
	s.prefix = "INSERT INTO " + s.d.QualifiedName(s.o.Table) + " (" + strings.Join(cols, ", ") + ") VALUES"
	return nil
}

func (s *SQLWriter) flush() error {
	if len(s.batch) == 0 {
		return nil
	}
	s.w.WriteString(s.prefix)
	for i, v := range s.batch {
		if i > 0 {
			s.w.WriteString(",")
		}
		if s.o.BatchSize > 1 {
			s.w.WriteString("\n  ")
		} else {
			s.w.WriteString(" ")
		}
		s.w.WriteString(v)
	}
	_, err := s.w.WriteString(";\n")
	s.batch = s.batch[:0]
	return err
}

// Row adds one row, writing a statement when the batch is full.
func (s *SQLWriter) Row(values []db.Value) error {
	vals := make([]string, len(values))
	for i, v := range values {
		vals[i] = s.d.QuoteLiteral(v)
	}
	s.batch = append(s.batch, "("+strings.Join(vals, ", ")+")")
	if len(s.batch) >= s.o.BatchSize {
		return s.flush()
	}
	return nil
}

// End writes the last batch and flushes.
func (s *SQLWriter) End() error {
	if err := s.flush(); err != nil {
		return err
	}
	return s.w.Flush()
}

func writeAll(w Writer, rs *db.ResultSet) error {
	if err := w.Begin(rs.Columns); err != nil {
		return err
	}
	for _, r := range rs.Rows {
		if err := w.Row(r); err != nil {
			return err
		}
	}
	return w.End()
}

// CSV writes rs as CSV.
func CSV(w io.Writer, rs *db.ResultSet, o CSVOptions) error { return writeAll(NewCSVWriter(w, o), rs) }

// SQL writes rs as INSERT statements for the given dialect.
func SQL(w io.Writer, d db.Dialect, rs *db.ResultSet, o SQLOptions) error {
	return writeAll(NewSQLWriter(w, d, o), rs)
}

// JSON writes rs as an array of objects with keys in column order.
func JSON(w io.Writer, rs *db.ResultSet) error { return writeAll(NewJSONWriter(w), rs) }
