package db

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gotable/internal/model"
)

// engine implements the per-database introspection queries.
type engine interface {
	serverVersion(ctx context.Context, c *Conn) (string, error)
	databases(ctx context.Context, c *Conn) ([]string, error)
	schemas(ctx context.Context, c *Conn) ([]string, error)
	currentSchema(ctx context.Context, c *Conn) (string, error)
	useSchema(ctx context.Context, c *Conn, schema string) error
	objects(ctx context.Context, c *Conn, schema string) ([]Object, error)
	columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error)
	indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error)
	// foreignKeys lists the table's foreign keys.
	foreignKeys(ctx context.Context, c *Conn, t TableRef) ([]ForeignRef, error)
	definition(ctx context.Context, c *Conn, o Object) (string, error)
}

// Conn is an open database session. All operations run on one pinned
// connection so session state (search_path, USE, transactions started in the
// editor) behaves as the user expects. Operations are serialized.
type Conn struct {
	Config  *model.Connection
	Dialect Dialect

	mu     sync.Mutex
	db     *sql.DB
	conn   *sql.Conn
	eng    engine
	tunnel io.Closer
	schema string
}

// ErrClosed is returned by operations on a closed Conn.
var ErrClosed = errors.New("connection is closed")

func newConn(ctx context.Context, cfg *model.Connection, sqlDB *sql.DB, eng engine, tunnel io.Closer) (*Conn, error) {
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)
	sc, err := sqlDB.Conn(ctx)
	if err == nil {
		err = sc.PingContext(ctx)
	}
	if err != nil {
		sqlDB.Close()
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, err
	}
	c := &Conn{Config: cfg, Dialect: DialectFor(cfg.Driver), db: sqlDB, conn: sc, eng: eng, tunnel: tunnel}
	if s, err := eng.currentSchema(ctx, c); err == nil {
		c.schema = s
	}
	if cfg.SelectedSchema != "" && cfg.SelectedSchema != c.schema {
		if err := eng.useSchema(ctx, c, cfg.SelectedSchema); err == nil {
			c.schema = cfg.SelectedSchema
		}
	}
	return c, nil
}

// Close releases the session and SSH tunnel.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	cerr := c.conn.Close()
	if errors.Is(cerr, sql.ErrConnDone) {
		cerr = nil
	}
	errs := []error{cerr, c.db.Close()}
	if c.tunnel != nil {
		errs = append(errs, c.tunnel.Close())
	}
	c.conn = nil
	return errors.Join(errs...)
}

// lock acquires the session. If the pinned connection was closed by the
// driver (for example after a cancelled query), a new one is opened and the
// active schema restored; other session state is lost with the old one.
func (c *Conn) lock(ctx context.Context) error {
	c.mu.Lock()
	if c.conn == nil {
		c.mu.Unlock()
		return ErrClosed
	}
	if err := c.conn.Raw(func(any) error { return nil }); errors.Is(err, sql.ErrConnDone) {
		if err := c.reconnect(ctx); err != nil {
			c.mu.Unlock()
			return fmt.Errorf("reconnect: %w", err)
		}
	}
	return nil
}

func (c *Conn) reconnect(ctx context.Context) error {
	sc, err := c.db.Conn(ctx)
	if err != nil {
		return err
	}
	c.conn = sc
	if c.schema == "" {
		return nil
	}
	cur, err := c.eng.currentSchema(ctx, c)
	if err == nil && cur != c.schema {
		err = c.eng.useSchema(ctx, c, c.schema)
	}
	return err
}

// Ping checks the connection is alive.
func (c *Conn) Ping(ctx context.Context) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	return c.conn.PingContext(ctx)
}

// ServerVersion returns the server version string.
func (c *Conn) ServerVersion(ctx context.Context) (string, error) {
	if err := c.lock(ctx); err != nil {
		return "", err
	}
	defer c.mu.Unlock()
	return c.eng.serverVersion(ctx, c)
}

// Databases lists databases on the server (the file for SQLite).
func (c *Conn) Databases(ctx context.Context) ([]string, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	return c.eng.databases(ctx, c)
}

// Schemas lists schemas (PostgreSQL), databases (MySQL) or attached
// databases (SQLite).
func (c *Conn) Schemas(ctx context.Context) ([]string, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	return c.eng.schemas(ctx, c)
}

// Schema returns the active schema.
func (c *Conn) Schema() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.schema
}

// UseSchema switches the active schema of the session.
func (c *Conn) UseSchema(ctx context.Context, schema string) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	if err := c.eng.useSchema(ctx, c, schema); err != nil {
		return err
	}
	c.schema = schema
	return nil
}

// Objects lists tables and views of a schema.
func (c *Conn) Objects(ctx context.Context, schema string) ([]Object, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	return c.eng.objects(ctx, c, schema)
}

// Columns describes the columns of a table.
func (c *Conn) Columns(ctx context.Context, t TableRef) ([]Column, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	cols, err := c.eng.columns(ctx, c, t)
	if err != nil {
		return nil, err
	}
	// Foreign keys are a convenience (navigation); a failing lookup, e.g.
	// missing privileges, must not hide the table.
	if fks, err := c.eng.foreignKeys(ctx, c, t); err == nil {
		// Narrower keys win when a column is part of several.
		sort.SliceStable(fks, func(i, j int) bool { return len(fks[i].From) < len(fks[j].From) })
		for k := range fks {
			for i := range cols {
				if cols[i].References == nil && slices.Contains(fks[k].From, cols[i].Name) {
					cols[i].References = &fks[k]
				}
			}
		}
	}
	return cols, nil
}

// scanForeignKeys groups (constraint, column, ref schema, ref table, ref
// column) rows, ordered by constraint and position, into foreign keys.
func scanForeignKeys(rows *sql.Rows, err error) ([]ForeignRef, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ForeignRef
	last := ""
	for rows.Next() {
		var name, from, schema, table, to string
		if err := rows.Scan(&name, &from, &schema, &table, &to); err != nil {
			return nil, err
		}
		if len(out) == 0 || name != last {
			out = append(out, ForeignRef{Schema: schema, Table: table})
			last = name
		}
		fk := &out[len(out)-1]
		fk.From = append(fk.From, from)
		fk.Columns = append(fk.Columns, to)
	}
	return out, rows.Err()
}

// Indexes lists the indexes of a table.
func (c *Conn) Indexes(ctx context.Context, t TableRef) ([]Index, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	return c.eng.indexes(ctx, c, t)
}

// Definition returns the CREATE statement of a table or view.
func (c *Conn) Definition(ctx context.Context, o Object) (string, error) {
	if err := c.lock(ctx); err != nil {
		return "", err
	}
	defer c.mu.Unlock()
	return c.eng.definition(ctx, c, o)
}

// FetchRows returns one page of a table.
func (c *Conn) FetchRows(ctx context.Context, t TableRef, p Page, columns []string) (*ResultSet, error) {
	q, args, err := BuildSelect(c.Dialect, t, p, columns)
	if err != nil {
		return nil, err
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	rows, err := c.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	rs, _, err := scanRows(rows, 0)
	return rs, err
}

// CountRows counts the rows matching the page filters.
func (c *Conn) CountRows(ctx context.Context, t TableRef, p Page, columns []string) (int64, error) {
	q, args, err := BuildCount(c.Dialect, t, p, columns)
	if err != nil {
		return 0, err
	}
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	var n int64
	err = c.conn.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// ApplyChanges commits pending edits in one transaction. On error nothing is
// committed and the failing statement is reported.
func (c *Conn) ApplyChanges(ctx context.Context, t TableRef, changes []Change, hasPK bool) error {
	stmts := make([]Statement, 0, len(changes))
	for _, ch := range changes {
		s, err := BuildChange(c.Dialect, t, ch, hasPK)
		if err != nil {
			return err
		}
		stmts = append(stmts, s)
	}
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	tx, err := c.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, s := range stmts {
		res, err := tx.ExecContext(ctx, s.SQL, s.Args...)
		if err == nil && s.Args != nil && !strings.HasPrefix(s.SQL, "INSERT") {
			if n, rerr := res.RowsAffected(); rerr == nil && n == 0 {
				err = fmt.Errorf("row no longer exists or was changed by someone else")
			}
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", Preview(c.Dialect, s), err)
		}
	}
	return tx.Commit()
}

// Execute runs every statement of a script and returns one Result per
// statement. Execution stops at the first error. rowCap limits the rows
// kept per result set (0 = unlimited).
func (c *Conn) Execute(ctx context.Context, script string, rowCap int) ([]Result, error) {
	return c.ExecuteIn(ctx, "", script, rowCap)
}

// ExecuteIn is Execute in a given schema: switching to it and running the
// script happen under one lock, so concurrent queries for different schemas
// of the same session can't interleave.
func (c *Conn) ExecuteIn(ctx context.Context, schema, script string, rowCap int) ([]Result, error) {
	spans := SplitStatements(c.Config.Driver, script)
	if len(spans) == 0 {
		return nil, fmt.Errorf("nothing to execute")
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	if schema != "" && schema != c.schema {
		if err := c.eng.useSchema(ctx, c, schema); err != nil {
			return nil, err
		}
		c.schema = schema
	}
	var results []Result
	for _, sp := range spans {
		r := c.execOne(ctx, sp.SQL, rowCap)
		results = append(results, r)
		if r.Err != nil {
			break
		}
	}
	// A USE / SET search_path in the script changes the active schema.
	if s, err := c.eng.currentSchema(ctx, c); err == nil {
		c.schema = s
	}
	return results, nil
}

func (c *Conn) execOne(ctx context.Context, stmt string, rowCap int) Result {
	start := time.Now()
	r := Result{Statement: stmt}
	if returnsRows(stmt) {
		rows, err := c.conn.QueryContext(ctx, stmt)
		if err != nil {
			r.Err = err
		} else {
			r.Set, r.Truncated, r.Err = scanRows(rows, rowCap)
		}
	} else {
		res, err := c.conn.ExecContext(ctx, stmt)
		if err != nil {
			r.Err = err
		} else if n, err := res.RowsAffected(); err == nil {
			r.RowsAffected = n
		}
	}
	r.Duration = time.Since(start)
	return r
}

var rowKeywords = []string{"SELECT", "WITH", "SHOW", "EXPLAIN", "DESCRIBE", "DESC", "PRAGMA",
	"VALUES", "TABLE", "CALL", "ANALYZE", "CHECK", "CHECKSUM", "OPTIMIZE", "REPAIR", "FETCH"}

// returnsRows guesses whether a statement produces a result set.
func returnsRows(stmt string) bool {
	s := strings.TrimLeft(stripLeadingComments(stmt), "( \t\r\n")
	word := s
	if i := strings.IndexFunc(s, func(r rune) bool { return !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') }); i >= 0 {
		word = s[:i]
	}
	for _, k := range rowKeywords {
		if strings.EqualFold(word, k) {
			return true
		}
	}
	up := strings.ToUpper(s)
	return strings.Contains(up, " RETURNING ") || strings.Contains(up, "\nRETURNING ")
}

func stripLeadingComments(s string) string {
	for {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "--") || strings.HasPrefix(s, "#"):
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				return ""
			}
			s = s[i+1:]
		case strings.HasPrefix(s, "/*"):
			i := strings.Index(s, "*/")
			if i < 0 {
				return ""
			}
			s = s[i+2:]
		default:
			return s
		}
	}
}

// scanRows reads all rows (up to rowCap when > 0) as display values.
func scanRows(rows *sql.Rows, rowCap int) (*ResultSet, bool, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, false, err
	}
	rs := &ResultSet{Columns: cols, Types: make([]string, len(cols)), Rows: [][]Value{}}
	if cts, err := rows.ColumnTypes(); err == nil {
		for i, ct := range cts {
			rs.Types[i] = ct.DatabaseTypeName()
		}
	}
	raw := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	truncated := false
	for rows.Next() {
		if rowCap > 0 && len(rs.Rows) >= rowCap {
			truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, false, err
		}
		row := make([]Value, len(cols))
		for i, v := range raw {
			row[i] = toValue(v, rs.Types[i])
		}
		rs.Rows = append(rs.Rows, row)
	}
	return rs, truncated, rows.Err()
}

// toValue converts a driver value to its display/edit text.
func toValue(v any, dbType string) Value {
	switch x := v.(type) {
	case nil:
		return NullValue
	case []byte:
		if utf8.Valid(x) {
			return Text(string(x))
		}
		return Value{Text: "0x" + strings.ToUpper(hex.EncodeToString(x)), Hex: true}
	case string:
		return Text(x)
	case time.Time:
		return Text(formatTime(x, strings.ToUpper(dbType)))
	case float64:
		return Text(strconv.FormatFloat(x, 'f', -1, 64))
	case float32:
		return Text(strconv.FormatFloat(float64(x), 'f', -1, 32))
	case bool:
		return Text(strconv.FormatBool(x))
	}
	return Text(fmt.Sprint(v))
}

func formatTime(t time.Time, dbType string) string {
	switch {
	case dbType == "DATE":
		return t.Format("2006-01-02")
	case dbType == "TIME":
		return t.Format("15:04:05.999999")
	case strings.Contains(dbType, "TZ"):
		return t.Format("2006-01-02 15:04:05.999999Z07:00")
	}
	return t.Format("2006-01-02 15:04:05.999999")
}

// queryStrings runs a query on the pinned connection and returns the first
// column of every row. Callers hold c.mu.
func (c *Conn) queryStrings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := c.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s.String)
	}
	return out, rows.Err()
}

// CurrentDatabase returns the database the session is connected to: the
// PostgreSQL database, the MySQL default database, or the SQLite file name.
func (c *Conn) CurrentDatabase(ctx context.Context) string {
	switch c.Config.Driver {
	case model.DriverPostgres:
		if err := c.lock(ctx); err != nil {
			return c.Config.DBName
		}
		defer c.mu.Unlock()
		var name string
		if err := c.conn.QueryRowContext(ctx, "SELECT current_database()").Scan(&name); err == nil {
			return name
		}
		return c.Config.DBName
	case model.DriverSQLite:
		return c.Config.Path[strings.LastIndexAny(c.Config.Path, `/\`)+1:]
	}
	return c.Schema()
}
