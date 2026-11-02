package db

import (
	"strconv"
	"strings"

	"gotable/internal/model"
)

// Dialect captures SQL syntax differences between engines.
type Dialect struct {
	kind   model.Driver
	family model.Driver // kind.Family(): what the syntax follows
}

// DialectFor returns the dialect of a driver.
func DialectFor(d model.Driver) Dialect { return Dialect{kind: d, family: d.Family()} }

// Kind returns the driver of the dialect.
func (d Dialect) Kind() model.Driver { return d.kind }

func (d Dialect) mysqlLike() bool { return d.family == model.DriverMySQL }

// QuoteIdent quotes an identifier.
func (d Dialect) QuoteIdent(name string) string {
	switch d.family {
	case model.DriverMySQL:
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	case model.DriverClickHouse:
		return "`" + strings.NewReplacer(`\`, `\\`, "`", "\\`").Replace(name) + "`"
	case model.DriverMSSQL:
		return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QualifiedName returns schema.table quoted. An empty schema is omitted.
func (d Dialect) QualifiedName(t TableRef) string {
	if t.Schema == "" || d.family == model.DriverSQLite && t.Schema == "main" {
		return d.QuoteIdent(t.Name)
	}
	return d.QuoteIdent(t.Schema) + "." + d.QuoteIdent(t.Name)
}

// Placeholder returns the bind parameter for the n-th (1-based) argument.
func (d Dialect) Placeholder(n int) string {
	switch d.family {
	case model.DriverPostgres:
		return "$" + strconv.Itoa(n)
	case model.DriverMSSQL:
		return "@p" + strconv.Itoa(n)
	case model.DriverOracle:
		return ":" + strconv.Itoa(n)
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
		switch d.family {
		case model.DriverPostgres:
			return `'\x` + strings.ToLower(h) + `'`
		case model.DriverMSSQL:
			return "0x" + h
		case model.DriverClickHouse:
			return "unhex('" + h + "')"
		case model.DriverOracle:
			return "HEXTORAW('" + h + "')"
		case model.DriverDuckDB:
			var b strings.Builder
			for i := 0; i+1 < len(h); i += 2 {
				b.WriteString(`\x` + h[i:i+2])
			}
			return "'" + b.String() + "'::BLOB"
		}
		return "X'" + h + "'"
	}
	if d.family == model.DriverClickHouse { // backslash escapes, like MySQL's
		return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v.Text) + "'"
	}
	s := strings.ReplaceAll(v.Text, "'", "''")
	if d.mysqlLike() {
		s = strings.ReplaceAll(s, `\`, `\\`)
	}
	if d.family == model.DriverMSSQL {
		return "N'" + s + "'" // Unicode, whatever the column's collation
	}
	return "'" + s + "'"
}

// TextExpr casts a column to text so LIKE works on any column type.
func (d Dialect) TextExpr(col string) string {
	q := d.QuoteIdent(col)
	switch d.family {
	case model.DriverPostgres:
		return q + "::text"
	case model.DriverMySQL:
		return "CAST(" + q + " AS CHAR)"
	case model.DriverMSSQL:
		return "CAST(" + q + " AS NVARCHAR(MAX))"
	case model.DriverDuckDB:
		return "CAST(" + q + " AS VARCHAR)"
	case model.DriverClickHouse:
		return "toString(" + q + ")"
	case model.DriverOracle:
		return "TO_CHAR(" + q + ")"
	}
	return q
}

// LikeOp returns the case-insensitive LIKE operator.
func (d Dialect) LikeOp() string {
	switch d.family {
	case model.DriverPostgres, model.DriverDuckDB, model.DriverClickHouse:
		return "ILIKE"
	}
	return "LIKE" // MySQL, SQLite and SQL Server compare case-insensitively by default
}

// likeEscape declares backslash as the LIKE escape character. PostgreSQL and
// MySQL use it by default (and MySQL would parse '\' as an open literal).
func (d Dialect) likeEscape() string {
	switch d.family {
	case model.DriverSQLite, model.DriverMSSQL, model.DriverDuckDB, model.DriverOracle:
		return ` ESCAPE '\'`
	}
	return ""
}

// escapeLike makes s match literally in a LIKE pattern (with likeEscape).
// SQL Server also treats [ as the start of a character class.
func (d Dialect) escapeLike(s string) string {
	s = escapeLike(s)
	if d.family == model.DriverMSSQL {
		s = strings.ReplaceAll(s, "[", `\[`)
	}
	return s
}

// LimitOffset renders a LIMIT/OFFSET clause.
func (d Dialect) LimitOffset(limit, offset int) string {
	if d.family == model.DriverMSSQL || d.family == model.DriverOracle { // SQL Server also needs an ORDER BY (BuildSelect)
		if limit <= 0 && offset <= 0 {
			return ""
		}
		s := " OFFSET " + strconv.Itoa(offset) + " ROWS"
		if limit > 0 {
			s += " FETCH NEXT " + strconv.Itoa(limit) + " ROWS ONLY"
		}
		return s
	}
	if limit <= 0 {
		if offset > 0 {
			if d.mysqlLike() {
				return " LIMIT 18446744073709551615 OFFSET " + strconv.Itoa(offset)
			}
			if d.family == model.DriverSQLite {
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

// familyLabel names the database in messages: "SQL Server", "DuckDB", ….
func familyLabel(d Dialect) string { return d.kind.Label() }
