package db

import (
	"strconv"
	"strings"

	"gotable/internal/model"
)

// Dialect captures SQL syntax differences between engines.
type Dialect struct {
	kind model.Driver
}

// DialectFor returns the dialect of a driver.
func DialectFor(d model.Driver) Dialect { return Dialect{kind: d} }

// Kind returns the driver of the dialect.
func (d Dialect) Kind() model.Driver { return d.kind }

func (d Dialect) mysqlLike() bool {
	return d.kind == model.DriverMySQL || d.kind == model.DriverMariaDB
}

// QuoteIdent quotes an identifier.
func (d Dialect) QuoteIdent(name string) string {
	if d.mysqlLike() {
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QualifiedName returns schema.table quoted. An empty schema is omitted.
func (d Dialect) QualifiedName(t TableRef) string {
	if t.Schema == "" || d.kind == model.DriverSQLite && t.Schema == "main" {
		return d.QuoteIdent(t.Name)
	}
	return d.QuoteIdent(t.Schema) + "." + d.QuoteIdent(t.Name)
}

// Placeholder returns the bind parameter for the n-th (1-based) argument.
func (d Dialect) Placeholder(n int) string {
	if d.kind == model.DriverPostgres {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

// QuoteLiteral renders a value as an SQL literal for previews and exports.
func (d Dialect) QuoteLiteral(v Value) string {
	if v.Null {
		return "NULL"
	}
	if v.Hex {
		h := strings.TrimPrefix(v.Text, "0x")
		if d.kind == model.DriverPostgres {
			return `'\x` + strings.ToLower(h) + `'`
		}
		return "X'" + h + "'"
	}
	s := strings.ReplaceAll(v.Text, "'", "''")
	if d.mysqlLike() {
		s = strings.ReplaceAll(s, `\`, `\\`)
	}
	return "'" + s + "'"
}

// TextExpr casts a column to text so LIKE works on any column type.
func (d Dialect) TextExpr(col string) string {
	q := d.QuoteIdent(col)
	switch {
	case d.kind == model.DriverPostgres:
		return q + "::text"
	case d.mysqlLike():
		return "CAST(" + q + " AS CHAR)"
	}
	return q
}

// LikeOp returns the case-insensitive LIKE operator.
func (d Dialect) LikeOp() string {
	if d.kind == model.DriverPostgres {
		return "ILIKE"
	}
	return "LIKE"
}

// likeEscape declares backslash as the LIKE escape character. PostgreSQL and
// MySQL use it by default (and MySQL would parse '\' as an open literal).
func (d Dialect) likeEscape() string {
	if d.kind == model.DriverSQLite {
		return ` ESCAPE '\'`
	}
	return ""
}

// LimitOffset renders a LIMIT/OFFSET clause.
func (d Dialect) LimitOffset(limit, offset int) string {
	if limit <= 0 {
		if offset > 0 {
			if d.mysqlLike() {
				return " LIMIT 18446744073709551615 OFFSET " + strconv.Itoa(offset)
			}
			if d.kind == model.DriverSQLite {
				return " LIMIT -1 OFFSET " + strconv.Itoa(offset)
			}
			return " OFFSET " + strconv.Itoa(offset)
		}
		return ""
	}
	s := " LIMIT " + strconv.Itoa(limit)
	if offset > 0 {
		s += " OFFSET " + strconv.Itoa(offset)
	}
	return s
}
