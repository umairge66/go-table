// Package db implements database access for every supported engine behind
// one Conn type: introspection, paged table browsing with filters, query
// execution and transactional row editing.
package db

import "time"

// ObjectKind is the kind of a schema object listed in the sidebar.
type ObjectKind string

const (
	KindTable ObjectKind = "table"
	KindView  ObjectKind = "view"
)

// Object is a table or view inside a schema.
type Object struct {
	Schema string
	Name   string
	Kind   ObjectKind
}

// TableRef addresses a table.
type TableRef struct {
	Schema string
	Name   string
}

// Column describes a table column.
type Column struct {
	Name       string
	Type       string
	Nullable   bool
	Default    *string
	PrimaryKey bool
	Comment    string
	// References is the foreign key the column belongs to (the
	// single-column one when it is part of several).
	References *ForeignRef
}

// ForeignRef is a foreign key: From columns of this table match Columns
// (same order) of Schema.Table.
type ForeignRef struct {
	Schema  string
	Table   string
	From    []string
	Columns []string
}

// Index describes a table index.
type Index struct {
	Name    string
	Columns []string
	Unique  bool
	Primary bool
}

// Value is a single cell. Null distinguishes SQL NULL from empty text.
// Hex marks binary data that isn't valid UTF-8; Text then holds 0x… hex.
type Value struct {
	Null bool
	Text string
	Hex  bool
}

// NullValue is SQL NULL.
var NullValue = Value{Null: true}

// Text returns a non-NULL value.
func Text(s string) Value { return Value{Text: s} }

// String renders the value for display.
func (v Value) String() string {
	if v.Null {
		return "NULL"
	}
	return v.Text
}

// ResultSet is the tabular output of a statement.
type ResultSet struct {
	Columns []string
	Types   []string
	Rows    [][]Value
}

// Result is the outcome of one statement in a script.
type Result struct {
	Statement    string
	Set          *ResultSet // nil for statements without rows
	RowsAffected int64
	Duration     time.Duration
	Truncated    bool // row cap reached
	Err          error
}

// SortDir is ascending or descending.
type SortDir string

const (
	Asc  SortDir = "ASC"
	Desc SortDir = "DESC"
)

// Sort orders by a column.
type Sort struct {
	Column string
	Dir    SortDir
}

// Page selects a slice of a table.
type Page struct {
	Filters []Filter
	// RawWhere is an optional SQL expression ANDed with Filters.
	RawWhere string
	Sort     []Sort
	Limit    int
	Offset   int
}
