package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"

	"gotable/internal/model"
)

// The row limit of the query editor. Reading fewer rows than a query returns
// isn't enough: closing the result makes the PostgreSQL, MySQL and ClickHouse
// drivers read (and discard) all the rest. Cancelling would stop it, but at
// the price of the session (temporary tables, settings) or of the open
// transaction. So the server is told how many rows are wanted, for this
// statement alone and in a way that leaves the session as it was:
//
//   - PostgreSQL: the protocol's own row limit (Execute's max_rows).
//   - MariaDB: SET STATEMENT sql_select_limit=n FOR …
//   - MySQL 8: the SET_VAR(sql_select_limit=n) hint.
//   - ClickHouse: the "limit" setting, sent with the query.
//
// Only plain reads are limited so; anything else runs to its end. The other
// drivers stop by themselves when a result is closed.

// cappable says whether a statement only reads, so running it to its end
// isn't part of what it does.
func cappable(d model.Driver, stmt string) bool {
	words := bareWords(d, stmt)
	if len(words) == 0 {
		return false
	}
	switch words[0].text {
	case "SELECT", "WITH", "VALUES", "TABLE":
	default:
		return false
	}
	for _, w := range words {
		switch w.text {
		case "INTO", "INSERT", "UPDATE", "DELETE", "MERGE", "SHARE", "LOCK", // FOR UPDATE: every row is to be locked
			"SQL_CALC_FOUND_ROWS", "SQL_SELECT_LIMIT":
			return false
		}
	}
	return true
}

// bareWord is an upper-cased word of a statement, outside quotes and comments.
type bareWord struct {
	text       string
	start, end int
	depth      int // of parentheses
}

func bareWords(d model.Driver, s string) []bareWord {
	f := d.Family()
	mysql := f == model.DriverMySQL
	backslash := mysql || f == model.DriverClickHouse
	var out []bareWord
	depth := 0
	for i := 0; i < len(s); {
		ch := s[i]
		switch {
		case ch == '\'' || ch == '"' || ch == '`':
			i = skipQuoted(s, i, ch, backslash)
		case ch == '-' && strings.HasPrefix(s[i:], "--"), ch == '#' && backslash:
			if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
				i += j + 1
			} else {
				i = len(s)
			}
		case ch == '/' && strings.HasPrefix(s[i:], "/*"):
			if j := strings.Index(s[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(s)
			}
		case ch == '$' && f == model.DriverPostgres:
			tag, ok := dollarTag(s[i:])
			if !ok {
				i++
			} else if j := strings.Index(s[i+len(tag):], tag); j >= 0 {
				i += j + 2*len(tag)
			} else {
				i = len(s)
			}
		case ch == '(':
			depth++
			i++
		case ch == ')':
			depth--
			i++
		case isBareByte(ch):
			j := i
			for j < len(s) && isBareByte(s[j]) {
				j++
			}
			out = append(out, bareWord{strings.ToUpper(s[i:j]), i, j, depth})
			i = j
		default:
			i++
		}
	}
	return out
}

func isBareByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// readStmt runs a statement that returns rows: begin gets the columns, then
// row each row, up to rowCap (0: all). An error from either stops it.
func (c *Conn) readStmt(ctx context.Context, stmt string, rowCap int, begin func(cols, types []string) error, row func([]Value) error) (truncated bool, err error) {
	if rowCap > 0 && !c.noCap && cappable(c.Config.Driver, stmt) {
		// One more than wanted tells whether there were more.
		switch c.Dialect.family {
		case model.DriverPostgres:
			return c.pgReadCapped(ctx, stmt, rowCap, begin, row)
		case model.DriverMySQL:
			stmt = c.mysqlCapped(ctx, stmt, rowCap+1)
		case model.DriverClickHouse:
			rows, err := c.conn.QueryContext(clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"limit": rowCap + 1})), stmt)
			var ex *clickhouse.Exception
			if errors.As(err, &ex) && ex.Code == 164 { // READONLY: this user may not change settings
				c.noCap = true
				break
			}
			if err != nil {
				return false, err
			}
			return readRows(rows, rowCap, begin, row)
		}
	}
	rows, err := c.conn.QueryContext(ctx, stmt)
	if err != nil {
		return false, err
	}
	return readRows(rows, rowCap, begin, row)
}

// readRows reads a result (up to rowCap rows when > 0) as display values.
func readRows(rows *sql.Rows, rowCap int, begin func(cols, types []string) error, row func([]Value) error) (truncated bool, err error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return false, err
	}
	types := make([]string, len(cols))
	if cts, err := rows.ColumnTypes(); err == nil {
		for i, ct := range cts {
			types[i] = ct.DatabaseTypeName()
		}
	}
	if err := begin(cols, types); err != nil {
		return false, err
	}
	raw := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	for n := 0; rows.Next(); n++ {
		if rowCap > 0 && n >= rowCap {
			return true, nil
		}
		if err := rows.Scan(ptrs...); err != nil {
			return false, err
		}
		vals := make([]Value, len(cols))
		for i, v := range raw {
			vals[i] = toValue(v, types[i])
		}
		if err := row(vals); err != nil {
			return false, err
		}
	}
	return false, rows.Err()
}

// ---------------------------------------------------------------- MySQL

// mysqlLooksBack: the script asks about its statement before, whose answer a
// limit would change.
var mysqlLooksBack = regexp.MustCompile(`FOUND_ROWS|ROW_COUNT|WARNING_COUNT|ERROR_COUNT`)

// mysqlCapped adds the limit to a SELECT, in the way its server has.
// An explicit LIMIT in the query wins over it.
func (c *Conn) mysqlCapped(ctx context.Context, stmt string, n int) string {
	if c.mysqlCap == "" {
		c.mysqlCap = c.mysqlCapKind(ctx)
	}
	limit := "sql_select_limit=" + strconv.Itoa(n)
	switch c.mysqlCap {
	case "statement":
		return "SET STATEMENT " + limit + " FOR " + stmt
	case "hint":
		return mysqlHint(c.Config.Driver, stmt, "SET_VAR("+limit+")")
	}
	return stmt
}

// mysqlCapKind asks the server once, before the first query.
func (c *Conn) mysqlCapKind(ctx context.Context) string {
	// A limit of the user's (or the administrator's) own stays in charge.
	var own string
	if c.conn.QueryRowContext(ctx, "SELECT @@sql_select_limit").Scan(&own) != nil || own != "18446744073709551615" {
		return "none"
	}
	v, err := c.eng.serverVersion(ctx, c)
	if err != nil {
		return "none"
	}
	if strings.HasPrefix(v, "MariaDB ") {
		return "statement"
	}
	for _, p := range []string{"MySQL ", "Percona Server "} {
		if rest, ok := strings.CutPrefix(v, p); ok {
			major, _, _ := strings.Cut(rest, ".")
			if n, err := strconv.Atoi(major); err == nil && n >= 8 {
				return "hint"
			}
		}
	}
	return "none"
}

// mysqlHint adds an optimizer hint to the statement's main SELECT (a hint in
// a subquery or a CTE would not count).
func mysqlHint(d model.Driver, stmt, hint string) string {
	for _, w := range bareWords(d, stmt) {
		if w.depth != 0 || w.text != "SELECT" {
			continue
		}
		rest := stmt[w.end:]
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		if own, ok := strings.CutPrefix(trimmed, "/*+"); ok { // hints of its own
			return stmt[:w.end] + rest[:len(rest)-len(trimmed)] + "/*+ " + hint + " " + own
		}
		return stmt[:w.end] + " /*+ " + hint + " */" + rest
	}
	return stmt
}

// ---------------------------------------------------------------- PostgreSQL

// pgReadCapped runs a query through the extended protocol, whose Execute
// takes a row limit: the server stops there by itself, inside a transaction
// or not. database/sql and pgx have no access to it, so the messages are
// sent here; values arrive as text, as with the simple protocol GoTable
// otherwise uses, and are read the way pgx reads them.
func (c *Conn) pgReadCapped(ctx context.Context, stmt string, rowCap int, begin func(cols, types []string) error, row func([]Value) error) (truncated bool, err error) {
	var fail error // begin's, row's or the server's: the connection stays usable
	rawErr := c.conn.Raw(func(dc any) error {
		sc, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected driver connection %T", dc)
		}
		pc := sc.Conn().PgConn()
		if pc.IsClosed() {
			return driver.ErrBadConn
		}
		if err := ctx.Err(); err != nil {
			fail = err
			return nil
		}
		m := sc.Conn().TypeMap()
		f := pc.Frontend()
		f.SendParse(&pgproto3.Parse{Query: stmt})
		f.SendBind(&pgproto3.Bind{})
		f.SendDescribe(&pgproto3.Describe{ObjectType: 'P'})
		f.SendExecute(&pgproto3.Execute{MaxRows: uint32(rowCap + 1)})
		f.SendClose(&pgproto3.Close{ObjectType: 'P'}) // an open portal would block DDL in the same transaction
		f.SendSync(&pgproto3.Sync{})
		if err := f.Flush(); err != nil {
			pgDrop(pc)
			return errors.Join(err, driver.ErrBadConn)
		}
		var (
			fields []pgproto3.FieldDescription
			types  []string
			n      int
		)
		for {
			msg, err := pc.ReceiveMessage(ctx)
			if err != nil {
				pgDrop(pc)
				if cerr := ctx.Err(); cerr != nil {
					err = cerr
				}
				return errors.Join(err, driver.ErrBadConn)
			}
			switch msg := msg.(type) {
			case *pgproto3.RowDescription:
				fields = append(fields, msg.Fields...)
				cols := make([]string, len(fields))
				types = make([]string, len(fields))
				for i, fd := range fields {
					cols[i] = string(fd.Name)
					if t, ok := m.TypeForOID(fd.DataTypeOID); ok {
						types[i] = strings.ToUpper(t.Name)
					} else {
						types[i] = strconv.FormatUint(uint64(fd.DataTypeOID), 10)
					}
				}
				if fail == nil {
					fail = begin(cols, types)
				}
			case *pgproto3.DataRow:
				if n++; n > rowCap {
					truncated = true
					continue
				}
				if fail != nil {
					continue
				}
				vals := make([]Value, len(msg.Values))
				for i, src := range msg.Values {
					if src == nil {
						vals[i] = toValue(nil, types[i])
						continue
					}
					v, err := pgValue(m, fields[i].DataTypeOID, fields[i].Format, src)
					if err != nil {
						fail = fmt.Errorf("convert field %d failed: %w", i, err)
						break
					}
					vals[i] = toValue(v, types[i])
				}
				if fail == nil {
					fail = row(vals)
				}
			case *pgproto3.ErrorResponse:
				fail = pgconn.ErrorResponseToPgError(msg)
				if msg.Severity == "FATAL" || msg.SeverityUnlocalized == "FATAL" {
					pgDrop(pc)
					return errors.Join(fail, driver.ErrBadConn)
				}
			case *pgproto3.ReadyForQuery:
				return nil
			}
		}
	})
	if rawErr != nil {
		return false, rawErr
	}
	return truncated && fail == nil, fail
}

// pgDrop closes a connection whose answer wasn't read to its end, telling
// the server to stop too.
func pgDrop(pc *pgconn.PgConn) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pc.CancelRequest(ctx)
		pc.Close(ctx)
	}()
}

// pgValue reads a value as pgx's database/sql driver does.
func pgValue(m *pgtype.Map, oid uint32, format int16, src []byte) (any, error) {
	switch oid {
	case pgtype.BoolOID:
		var d bool
		err := m.Scan(oid, format, src, &d)
		return d, err
	case pgtype.ByteaOID, pgtype.JSONOID, pgtype.JSONBOID, pgtype.XMLOID:
		var d []byte
		err := m.Scan(oid, format, src, &d)
		return d, err
	case pgtype.CIDOID, pgtype.OIDOID, pgtype.XIDOID:
		var d pgtype.Uint32
		if err := m.Scan(oid, format, src, &d); err != nil {
			return nil, err
		}
		return d.Value()
	case pgtype.DateOID:
		var d pgtype.Date
		if err := m.Scan(oid, format, src, &d); err != nil {
			return nil, err
		}
		return d.Value()
	case pgtype.TimestampOID:
		var d pgtype.Timestamp
		if err := m.Scan(oid, format, src, &d); err != nil {
			return nil, err
		}
		return d.Value()
	case pgtype.TimestamptzOID:
		var d pgtype.Timestamptz
		if err := m.Scan(oid, format, src, &d); err != nil {
			return nil, err
		}
		return d.Value()
	case pgtype.Float4OID:
		var d float32
		err := m.Scan(oid, format, src, &d)
		return float64(d), err
	case pgtype.Float8OID:
		var d float64
		err := m.Scan(oid, format, src, &d)
		return d, err
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.Int8OID:
		var d int64
		err := m.Scan(oid, format, src, &d)
		return d, err
	}
	var d string
	err := m.Scan(oid, format, src, &d)
	return d, err
}
