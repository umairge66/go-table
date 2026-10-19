package db

import (
	"fmt"
	"strings"
)

// Op is a filter operator offered in the data view filter bar.
type Op string

const (
	OpEq         Op = "="
	OpNe         Op = "<>"
	OpLt         Op = "<"
	OpGt         Op = ">"
	OpLe         Op = "<="
	OpGe         Op = ">="
	OpIn         Op = "IN"
	OpNotIn      Op = "NOT IN"
	OpIsNull     Op = "IS NULL"
	OpIsNotNull  Op = "IS NOT NULL"
	OpBetween    Op = "BETWEEN"
	OpLike       Op = "LIKE"
	OpNotLike    Op = "NOT LIKE"
	OpContains   Op = "Contains"
	OpNotContain Op = "Not contains"
	OpBeginsWith Op = "Begins with"
	OpEndsWith   Op = "Ends with"
)

// Ops lists every operator in display order.
var Ops = []Op{OpEq, OpNe, OpLt, OpGt, OpLe, OpGe, OpIn, OpNotIn, OpIsNull, OpIsNotNull,
	OpBetween, OpLike, OpNotLike, OpContains, OpNotContain, OpBeginsWith, OpEndsWith}

// NeedsValue reports whether the operator takes a value.
func (o Op) NeedsValue() bool { return o != OpIsNull && o != OpIsNotNull }

// AnyColumn as Filter.Column matches the value against every column.
const AnyColumn = "*"

// Filter is one condition of the filter bar. For IN / NOT IN, Value is a
// comma separated list; for BETWEEN, Value and Value2 are the bounds.
type Filter struct {
	Column string
	Op     Op
	Value  string
	Value2 string
}

// whereBuilder accumulates a WHERE clause with bind arguments.
type whereBuilder struct {
	d    Dialect
	args []any
}

func (w *whereBuilder) bind(v any) string {
	w.args = append(w.args, v)
	return w.d.Placeholder(len(w.args))
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (w *whereBuilder) condition(f Filter, columns []string) (string, error) {
	if f.Column == AnyColumn {
		if len(columns) == 0 {
			return "", fmt.Errorf("no columns to search")
		}
		parts := make([]string, 0, len(columns))
		for _, c := range columns {
			p, err := w.condition(Filter{Column: c, Op: f.Op, Value: f.Value, Value2: f.Value2}, nil)
			if err != nil {
				return "", err
			}
			parts = append(parts, p)
		}
		joiner := " OR "
		if f.Op == OpNe || f.Op == OpNotIn || f.Op == OpNotLike || f.Op == OpNotContain || f.Op == OpIsNotNull {
			joiner = " AND "
		}
		return "(" + strings.Join(parts, joiner) + ")", nil
	}
	if f.Column == "" {
		return "", fmt.Errorf("filter column is empty")
	}
	col := w.d.QuoteIdent(f.Column)
	like := func(not bool, pattern string) string {
		op := w.d.LikeOp()
		if not {
			op = "NOT " + op
		}
		return w.d.TextExpr(f.Column) + " " + op + " " + w.bind(pattern) + w.d.likeEscape()
	}
	switch f.Op {
	case OpEq, OpNe, OpLt, OpGt, OpLe, OpGe:
		return col + " " + string(f.Op) + " " + w.bind(f.Value), nil
	case OpIsNull, OpIsNotNull:
		return col + " " + string(f.Op), nil
	case OpIn, OpNotIn:
		items := splitList(f.Value)
		if len(items) == 0 {
			return "", fmt.Errorf("%s needs at least one value", f.Op)
		}
		ph := make([]string, len(items))
		for i, it := range items {
			ph[i] = w.bind(it)
		}
		return col + " " + string(f.Op) + " (" + strings.Join(ph, ", ") + ")", nil
	case OpBetween:
		return col + " BETWEEN " + w.bind(f.Value) + " AND " + w.bind(f.Value2), nil
	case OpLike:
		return w.d.TextExpr(f.Column) + " LIKE " + w.bind(f.Value), nil
	case OpNotLike:
		return w.d.TextExpr(f.Column) + " NOT LIKE " + w.bind(f.Value), nil
	case OpContains:
		return like(false, "%"+escapeLike(f.Value)+"%"), nil
	case OpNotContain:
		return like(true, "%"+escapeLike(f.Value)+"%"), nil
	case OpBeginsWith:
		return like(false, escapeLike(f.Value)+"%"), nil
	case OpEndsWith:
		return like(false, "%"+escapeLike(f.Value)), nil
	}
	return "", fmt.Errorf("unknown operator %q", f.Op)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// buildWhere returns " WHERE ..." (or "") and its arguments. columns is used
// to expand AnyColumn filters.
func buildWhere(d Dialect, p Page, columns []string) (string, []any, error) {
	w := &whereBuilder{d: d}
	var parts []string
	for _, f := range p.Filters {
		c, err := w.condition(f, columns)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, c)
	}
	if raw := strings.TrimSpace(p.RawWhere); raw != "" {
		parts = append(parts, "("+raw+")")
	}
	if len(parts) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(parts, " AND "), w.args, nil
}

// BuildSelect returns the paged SELECT for a table and its arguments.
func BuildSelect(d Dialect, t TableRef, p Page, columns []string) (string, []any, error) {
	where, args, err := buildWhere(d, p, columns)
	if err != nil {
		return "", nil, err
	}
	q := "SELECT * FROM " + d.QualifiedName(t) + where
	if len(p.Sort) > 0 {
		parts := make([]string, len(p.Sort))
		for i, s := range p.Sort {
			dir := s.Dir
			if dir != Desc {
				dir = Asc
			}
			parts[i] = d.QuoteIdent(s.Column) + " " + string(dir)
		}
		q += " ORDER BY " + strings.Join(parts, ", ")
	}
	return q + d.LimitOffset(p.Limit, p.Offset), args, nil
}

// BuildCount returns the COUNT(*) query matching the page filters.
func BuildCount(d Dialect, t TableRef, p Page, columns []string) (string, []any, error) {
	where, args, err := buildWhere(d, p, columns)
	if err != nil {
		return "", nil, err
	}
	return "SELECT COUNT(*) FROM " + d.QualifiedName(t) + where, args, nil
}
