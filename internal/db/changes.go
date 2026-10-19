package db

import (
	"fmt"
	"sort"
	"strings"
)

// ChangeKind is the kind of a pending row edit.
type ChangeKind int

const (
	ChangeUpdate ChangeKind = iota
	ChangeInsert
	ChangeDelete
)

// Change is a pending edit in the data view, committed in one transaction.
type Change struct {
	Kind ChangeKind
	// Key identifies the row for updates and deletes: the primary key
	// columns, or every original column when the table has no primary key.
	Key map[string]Value
	// Values are the new column values for inserts and updates.
	Values map[string]Value
}

// Statement is SQL plus bind arguments.
type Statement struct {
	SQL  string
	Args []any
}

func sortedKeys(m map[string]Value) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func bindValue(v Value) any {
	if v.Null {
		return nil
	}
	return v.Text
}

// keyCondition builds "a = ? AND b IS NULL" for a row key.
func keyCondition(d Dialect, key map[string]Value, args *[]any) string {
	parts := make([]string, 0, len(key))
	for _, k := range sortedKeys(key) {
		v := key[k]
		if v.Null {
			parts = append(parts, d.QuoteIdent(k)+" IS NULL")
			continue
		}
		*args = append(*args, v.Text)
		parts = append(parts, d.QuoteIdent(k)+" = "+d.Placeholder(len(*args)))
	}
	return strings.Join(parts, " AND ")
}

// BuildChange renders a change as a parameterized statement. Updates and
// deletes without a primary key are restricted to one row where the engine
// supports it.
func BuildChange(d Dialect, t TableRef, c Change, hasPK bool) (Statement, error) {
	name := d.QualifiedName(t)
	var args []any
	switch c.Kind {
	case ChangeInsert:
		if len(c.Values) == 0 {
			return Statement{SQL: insertDefaults(d, name)}, nil
		}
		cols := sortedKeys(c.Values)
		qcols := make([]string, len(cols))
		ph := make([]string, len(cols))
		for i, k := range cols {
			qcols[i] = d.QuoteIdent(k)
			args = append(args, bindValue(c.Values[k]))
			ph[i] = d.Placeholder(len(args))
		}
		return Statement{SQL: fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", name,
			strings.Join(qcols, ", "), strings.Join(ph, ", ")), Args: args}, nil
	case ChangeUpdate:
		if len(c.Values) == 0 {
			return Statement{}, fmt.Errorf("update without values")
		}
		if len(c.Key) == 0 {
			return Statement{}, fmt.Errorf("update without row key")
		}
		sets := make([]string, 0, len(c.Values))
		for _, k := range sortedKeys(c.Values) {
			args = append(args, bindValue(c.Values[k]))
			sets = append(sets, d.QuoteIdent(k)+" = "+d.Placeholder(len(args)))
		}
		where := keyCondition(d, c.Key, &args)
		q := fmt.Sprintf("UPDATE %s SET %s WHERE %s", name, strings.Join(sets, ", "), where)
		return Statement{SQL: q + singleRowSuffix(d, hasPK), Args: args}, nil
	case ChangeDelete:
		if len(c.Key) == 0 {
			return Statement{}, fmt.Errorf("delete without row key")
		}
		where := keyCondition(d, c.Key, &args)
		q := fmt.Sprintf("DELETE FROM %s WHERE %s", name, where)
		return Statement{SQL: q + singleRowSuffix(d, hasPK), Args: args}, nil
	}
	return Statement{}, fmt.Errorf("unknown change kind %d", c.Kind)
}

func insertDefaults(d Dialect, name string) string {
	if d.mysqlLike() {
		return "INSERT INTO " + name + " () VALUES ()"
	}
	return "INSERT INTO " + name + " DEFAULT VALUES"
}

func singleRowSuffix(d Dialect, hasPK bool) string {
	if !hasPK && d.mysqlLike() {
		return " LIMIT 1"
	}
	return ""
}

// Preview renders a statement with literals inlined, for the "review
// changes" dialog. It is never executed.
func Preview(d Dialect, s Statement) string {
	var b strings.Builder
	argIdx := 0
	next := func() string {
		if argIdx >= len(s.Args) {
			return "?"
		}
		a := s.Args[argIdx]
		argIdx++
		if a == nil {
			return "NULL"
		}
		return d.QuoteLiteral(Text(fmt.Sprint(a)))
	}
	sql := s.SQL
	inQuote := byte(0)
	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		if inQuote != 0 {
			b.WriteByte(ch)
			if ch == inQuote {
				inQuote = 0
			}
			continue
		}
		switch {
		case ch == '\'' || ch == '"' || ch == '`':
			inQuote = ch
			b.WriteByte(ch)
		case ch == '?' && d.Kind() != "postgres":
			b.WriteString(next())
		case ch == '$' && d.Kind() == "postgres" && i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9':
			j := i + 1
			for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
				j++
			}
			b.WriteString(next())
			i = j - 1
		default:
			b.WriteByte(ch)
		}
	}
	return b.String() + ";"
}
