package db

// Cloudflare D1: SQLite's SQL over Cloudflare's web API, through a
// database/sql driver of its own.
//
//   - A request is a session: there is no BEGIN. A transaction here collects
//     its statements and sends them as one batch on COMMIT, which D1 runs all
//     or not at all. Rows changed aren't known before that, so an edit is
//     followed by a statement that fails the batch if it matched no row.
//   - Foreign keys are always enforced (PRAGMA foreign_keys does nothing;
//     in a batch they are checked at its end). Dropping a table therefore
//     deletes the rows that reference it ON DELETE CASCADE, so GoTable
//     refuses to rebuild a table that others reference, or to drop one
//     whose references would change other tables' rows.
//   - Answers are JavaScript's: whole numbers past 2^53 are rounded and
//     blobs are lists of bytes. Tables are read with those numbers as text;
//     in the query editor a number that may be rounded is marked with ≈.

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gotable/internal/model"
)

// d1API is Cloudflare's API (a variable for tests).
var d1API = "https://api.cloudflare.com/client/v4"

const (
	d1Exact     = 1 << 53 // JavaScript's whole numbers are exact below this
	d1Page      = 2000    // rows per request of an export
	d1Statement = 90_000  // bytes of a statement (D1 takes 100,000)
	d1Params    = 100     // bound values of a statement
)

type d1Connector struct {
	base  string // …/accounts/<account>/d1/database
	db    string // the database's id
	token string
	http  *http.Client
	name  string
	place string // where it runs, e.g. WEUR
}

type d1Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *d1Error) Error() string { return e.Message }

// api sends a request and reads the answer's result.
func (c *d1Connector) api(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	defer res.Body.Close()
	var ans struct {
		Success bool            `json:"success"`
		Errors  []d1Error       `json:"errors"`
		Result  json.RawMessage `json:"result"`
	}
	dec := json.NewDecoder(res.Body)
	dec.UseNumber()
	if err := dec.Decode(&ans); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if res.StatusCode >= 300 {
			return &d1Error{Message: "Cloudflare answered " + res.Status}
		}
		return fmt.Errorf("Cloudflare's answer couldn't be read: %w", err)
	}
	if !ans.Success {
		e := &d1Error{Message: "Cloudflare answered " + res.Status}
		if len(ans.Errors) > 0 {
			e = &ans.Errors[0]
		}
		switch {
		case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden || e.Code == 10000:
			e.Message = "authentication failed: the API token is wrong, or has no D1 right on this account (" + e.Message + ")"
		case res.StatusCode == http.StatusTooManyRequests:
			e.Message = "Cloudflare's rate limit was reached: wait a little (" + e.Message + ")"
		}
		return e
	}
	if out == nil {
		return nil
	}
	dec = json.NewDecoder(bytes.NewReader(ans.Result))
	dec.UseNumber()
	return dec.Decode(out)
}

type d1Query struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params,omitempty"`
}

type d1Result struct {
	Results struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	} `json:"results"`
	Meta struct {
		Changes json.Number `json:"changes"`
		LastID  json.Number `json:"last_row_id"`
	} `json:"meta"`
}

func (c *d1Connector) raw(ctx context.Context, in any) ([]d1Result, error) {
	var out []d1Result
	err := c.api(ctx, http.MethodPost, "/"+c.db+"/raw", in, &out)
	return out, err
}

// openD1: the account is the user, the token the password, and the database
// a name or an id.
func openD1(ctx context.Context, cfg *model.Connection, token string) (*sql.DB, error) {
	c := &d1Connector{base: d1API + "/accounts/" + url.PathEscape(cfg.User) + "/d1/database", token: strings.TrimSpace(token), http: &http.Client{Timeout: 10 * time.Minute}}
	type info struct {
		ID     string `json:"uuid"`
		Name   string `json:"name"`
		Region string `json:"running_in_region"`
	}
	var x info
	if d1ID.MatchString(cfg.DBName) {
		if err := c.api(ctx, http.MethodGet, "/"+cfg.DBName, nil, &x); err != nil {
			return nil, err
		}
	} else {
		var list []info
		if err := c.api(ctx, http.MethodGet, "?per_page=100&name="+url.QueryEscape(cfg.DBName), nil, &list); err != nil {
			return nil, err
		}
		var names []string
		for _, i := range list { // the API finds parts of names too
			names = append(names, i.Name)
			if i.Name == cfg.DBName {
				x = i
			}
		}
		if x.ID == "" {
			if len(names) > 0 {
				return nil, fmt.Errorf("the account has no D1 database %q (it has %s)", cfg.DBName, strings.Join(names, ", "))
			}
			return nil, fmt.Errorf("the account has no D1 database %q", cfg.DBName)
		}
		c.api(ctx, http.MethodGet, "/"+x.ID, nil, &x) // where it runs
	}
	c.db, c.name, c.place = x.ID, x.Name, x.Region
	return sql.OpenDB(c), nil
}

var d1ID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (c *d1Connector) Connect(context.Context) (driver.Conn, error) { return &d1Conn{c: c}, nil }
func (c *d1Connector) Driver() driver.Driver                        { return d1Driver{c} }

type d1Driver struct{ c *d1Connector }

func (d1Driver) Open(string) (driver.Conn, error) {
	return nil, errors.New("D1 connections are opened with a connector")
}

type d1Conn struct {
	c    *d1Connector
	inTx bool
	tx   []d1Query // waiting for COMMIT
}

func (d *d1Conn) Close() error { d.inTx, d.tx = false, nil; return nil }
func (d *d1Conn) Ping(ctx context.Context) error {
	_, err := d.c.raw(ctx, d1Query{SQL: "SELECT 1"})
	return err
}

func d1Args(args []driver.NamedValue) ([]any, error) {
	out := make([]any, len(args))
	for i, a := range args {
		switch x := a.Value.(type) {
		case nil:
			out[i] = nil
		case int64: // numbers arrive as REAL; text takes its column's type, exactly
			out[i] = strconv.FormatInt(x, 10)
		case bool:
			out[i] = "0"
			if x {
				out[i] = "1"
			}
		case float64:
			out[i] = x
		case string:
			out[i] = x
		case []byte:
			l := make([]int, len(x))
			for j, b := range x {
				l[j] = int(b)
			}
			out[i] = l
		case time.Time:
			out[i] = x.Format("2006-01-02 15:04:05.999999999-07:00")
		default:
			return nil, fmt.Errorf("value of type %T", a.Value)
		}
	}
	return out, nil
}

// d1Done is a statement's outcome; inside a transaction it isn't known yet.
type d1Done struct {
	changes int64
	id      int64
	pending bool
}

func (d d1Done) RowsAffected() (int64, error) {
	if d.pending {
		return 0, errors.New("not known before COMMIT")
	}
	return d.changes, nil
}
func (d d1Done) LastInsertId() (int64, error) { return d.id, nil }

func (d *d1Conn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	vals, err := d1Args(args)
	if err != nil {
		return nil, err
	}
	switch strings.ToUpper(strings.TrimRight(strings.TrimSpace(q), "; \t\r\n")) {
	case "BEGIN":
		d.inTx, d.tx = true, nil
		return d1Done{}, nil
	case "COMMIT":
		n, err := d.commit(ctx)
		return d1Done{changes: n}, err
	case "ROLLBACK":
		d.inTx, d.tx = false, nil
		return d1Done{}, nil
	}
	if d.inTx {
		d.tx = append(d.tx, d1Query{q, vals})
		return d1Done{pending: true}, nil
	}
	res, err := d.c.raw(ctx, d1Query{q, vals})
	if err != nil {
		return nil, err
	}
	var done d1Done
	for _, r := range res {
		n, _ := r.Meta.Changes.Int64()
		done.changes += n
		done.id, _ = r.Meta.LastID.Int64()
	}
	if !d1Writes.MatchString(stripLeadingComments(q)) { // DDL reports the rows it moved about
		done.changes = 0
	}
	return done, nil
}

var d1Writes = regexp.MustCompile(`(?i)^(INSERT|UPDATE|DELETE|REPLACE|WITH)\b`)

// commit sends the transaction as one batch, foreign keys checked at its end.
func (d *d1Conn) commit(ctx context.Context) (int64, error) {
	stmts := d.tx
	d.inTx, d.tx = false, nil
	if len(stmts) == 0 {
		return 0, nil
	}
	batch := append([]d1Query{{SQL: "PRAGMA defer_foreign_keys = on"}}, stmts...)
	res, err := d.c.raw(ctx, map[string]any{"batch": batch})
	if err != nil {
		var answer *d1Error
		if !errors.As(err, &answer) {
			return 0, fmt.Errorf("Cloudflare's answer to the changes was lost, so they may or may not have been saved: %w", err)
		}
		if m := d1Gone.FindStringSubmatch(answer.Message); m != nil {
			n, _ := strconv.Atoi(m[1])
			return 0, d1RowGone(n)
		}
		answer.Message = strings.TrimPrefix(answer.Message, "D1 DB was reset and rolled back to its last known good state because the application left the database in a state where constraints were violated: ")
		return 0, answer
	}
	var total int64
	for i, r := range res {
		if i > 0 && i <= len(stmts) && d1Writes.MatchString(stripLeadingComments(stmts[i-1].SQL)) {
			n, _ := r.Meta.Changes.Int64()
			total += n
		}
	}
	return total, nil
}

// d1Guard fails a batch if the statement before it, the n-th edit, changed
// no row. The error names the JSON path, and so the edit.
func d1Guard(n int) string {
	return fmt.Sprintf("SELECT CASE WHEN changes() = 0 THEN json_extract('{}', 'gotable edit %d') END", n)
}

var d1Gone = regexp.MustCompile(`gotable edit (\d+)`)

// d1RowGone is the edit that d1Guard stopped at.
type d1RowGone int

func (d1RowGone) Error() string {
	return "row no longer exists or was changed by someone else"
}

func (d *d1Conn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	vals, err := d1Args(args)
	if err != nil {
		return nil, err
	}
	res, err := d.c.raw(ctx, d1Query{q, vals})
	if err != nil {
		return nil, err
	}
	r := &d1Rows{}
	if len(res) > 0 { // of several statements, the last
		last := res[len(res)-1].Results
		r.cols, r.rows = last.Columns, last.Rows
	}
	return r, nil
}

type d1Rows struct {
	cols []string
	rows [][]any
	at   int
}

func (r *d1Rows) Columns() []string { return r.cols }
func (r *d1Rows) Close() error      { return nil }
func (r *d1Rows) Next(dest []driver.Value) error {
	if r.at >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.at]
	r.at++
	for i := range dest {
		if i >= len(row) {
			dest[i] = nil
			continue
		}
		v, err := d1Value(row[i])
		if err != nil {
			return err
		}
		dest[i] = v
	}
	return nil
}

func d1Value(v any) (driver.Value, error) {
	switch x := v.(type) {
	case nil, string, bool:
		return x, nil
	case json.Number:
		s := x.String()
		if !strings.ContainsAny(s, ".eE") {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n >= d1Exact || n <= -d1Exact {
				return "≈" + s, nil // rounded, perhaps
			}
			return n, nil
		}
		return x.Float64()
	case []any: // a blob
		b := make([]byte, len(x))
		for i, e := range x {
			n, ok := e.(json.Number)
			if !ok {
				return nil, fmt.Errorf("unexpected value %v", v)
			}
			k, err := strconv.ParseUint(n.String(), 10, 8)
			if err != nil {
				return nil, err
			}
			b[i] = byte(k)
		}
		return b, nil
	}
	return nil, fmt.Errorf("unexpected value %v", v)
}

func (d *d1Conn) Prepare(q string) (driver.Stmt, error) { return d1Prepared{d, q}, nil }

type d1Prepared struct {
	d *d1Conn
	q string
}

func (d1Prepared) Close() error  { return nil }
func (d1Prepared) NumInput() int { return -1 }
func (p d1Prepared) Exec(args []driver.Value) (driver.Result, error) {
	return p.d.ExecContext(context.Background(), p.q, hranaNamed(args))
}
func (p d1Prepared) Query(args []driver.Value) (driver.Rows, error) {
	return p.d.QueryContext(context.Background(), p.q, hranaNamed(args))
}

func (d *d1Conn) Begin() (driver.Tx, error) {
	return d.BeginTx(context.Background(), driver.TxOptions{})
}
func (d *d1Conn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	d.inTx, d.tx = true, nil
	return d1Tx{d}, nil
}

type d1Tx struct{ d *d1Conn }

func (t d1Tx) Commit() error {
	_, err := t.d.commit(context.Background())
	return err
}
func (t d1Tx) Rollback() error { t.d.inTx, t.d.tx = false, nil; return nil }

// ---------------------------------------------------------------- engine

type d1Engine struct{ sqliteEngine }

func d1Of(c *Conn) *d1Connector {
	d, _ := c.db.Driver().(d1Driver)
	return d.c
}

func (d1Engine) serverVersion(_ context.Context, c *Conn) (string, error) {
	x := d1Of(c)
	if x.place != "" {
		return "Cloudflare D1 · " + x.name + " (" + x.place + ")", nil
	}
	return "Cloudflare D1 · " + x.name, nil
}

func (d1Engine) databases(_ context.Context, c *Conn) ([]string, error) {
	return []string{d1Of(c).name}, nil
}

func (d1Engine) schemas(context.Context, *Conn) ([]string, error) { return []string{"main"}, nil }

func (d1Engine) useSchema(_ context.Context, _ *Conn, schema string) error {
	if schema != "main" {
		return errors.New("a D1 database has one schema, main")
	}
	return nil
}

// objects leaves out Cloudflare's own tables.
func (e d1Engine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	all, err := e.sqliteEngine.objects(ctx, c, schema)
	out := []Object{}
	for _, o := range all {
		if !strings.HasPrefix(o.Name, "_cf_") {
			out = append(out, o)
		}
	}
	return out, err
}

// d1References: the tables whose foreign keys point at t, with what they do
// when its rows go. Callers hold c.mu.
func d1References(ctx context.Context, c *Conn, t string) (map[string]string, error) {
	names, err := c.queryStrings(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE '\\_cf\\_%' ESCAPE '\\' AND lower(sql) LIKE '%references%'")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, n := range names {
		acts, err := c.queryStrings(ctx, "SELECT on_delete FROM pragma_foreign_key_list(?) WHERE lower(\"table\") = lower(?)", n, t)
		if err != nil {
			return nil, err
		}
		for _, a := range acts {
			if out[n] == "" || a != "NO ACTION" && a != "RESTRICT" {
				out[n] = a
			}
		}
	}
	return out, nil
}

// d1MayRebuild refuses to rebuild a table that others (or itself) reference:
// the old table is dropped on the way, and D1 would delete or change the rows
// that point at it. Callers hold c.mu.
func d1MayRebuild(ctx context.Context, c *Conn, t TableRef) error {
	if c.Config.Driver != model.DriverD1 {
		return nil
	}
	refs, err := d1References(ctx, c, t.Name)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return fmt.Errorf("D1 can't change that on %s while %s references it: the table has to be rebuilt, and D1 would delete or change the rows that point at it", t.Name, strings.Join(slices.Sorted(maps.Keys(refs)), ", "))
	}
	return nil
}

// d1MayDrop refuses drops that would delete or change rows of tables that
// stay. Callers hold c.mu.
func d1MayDrop(ctx context.Context, c *Conn, objs []Object) error {
	if c.Config.Driver != model.DriverD1 {
		return nil
	}
	going := map[string]bool{}
	for _, o := range objs {
		going[strings.ToLower(o.Name)] = true
	}
	for _, o := range objs {
		if o.Kind == KindView {
			continue
		}
		refs, err := d1References(ctx, c, o.Name)
		if err != nil {
			return err
		}
		for _, n := range slices.Sorted(maps.Keys(refs)) {
			if !going[strings.ToLower(n)] {
				return fmt.Errorf("%s is referenced by %s (ON DELETE %s), and D1 can't leave foreign keys unchecked: delete %s too, or remove its foreign key first", o.Name, n, refs[n], n)
			}
		}
	}
	return nil
}

// d1Columns is a SELECT list that reads a table exactly: numbers JavaScript
// would round come as text. Callers hold c.mu.
func d1Columns(ctx context.Context, c *Conn, t TableRef) (string, error) {
	names, err := c.queryStrings(ctx, "SELECT name FROM pragma_table_xinfo(?) WHERE hidden <> 1 ORDER BY cid", t.Name)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("table %s not found", t.Name)
	}
	parts := make([]string, len(names))
	for i, n := range names {
		q := c.Dialect.QuoteIdent(n)
		parts[i] = fmt.Sprintf("CASE WHEN typeof(%[1]s) = 'integer' AND (%[1]s > 9007199254740991 OR %[1]s < -9007199254740991) THEN CAST(%[1]s AS TEXT) "+
			"WHEN typeof(%[1]s) = 'real' AND abs(%[1]s) = 9e999 THEN iif(%[1]s > 0, '9e999', '-9e999') "+ // infinity, as SQLite reads it back
			"WHEN typeof(%[1]s) = 'real' AND (%[1]s > 9007199254740991 OR %[1]s < -9007199254740991) THEN printf('%%!.17g', %[1]s) ELSE %[1]s END AS %[1]s", q)
	}
	return strings.Join(parts, ", "), nil
}

// d1Rows makes BuildSelect's query read exactly. The query stays inside, so
// its ORDER BY means the columns and not the list's expressions.
func d1Select(ctx context.Context, c *Conn, t TableRef, q string) (string, error) {
	list, err := d1Columns(ctx, c, t)
	return "SELECT " + list + " FROM (" + q + ")", err
}

// d1Stream reads a table in pages by its rowid, so that an export needn't fit
// in one answer. A view, a table without rowid or a sorted read comes whole.
func (c *Conn) d1Stream(ctx context.Context, t TableRef, p Page, columns []string, fn func(cols, types []string, row []Value) error) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	list, err := d1Columns(ctx, c, t)
	if err != nil {
		return err
	}
	whole := len(p.Sort) > 0 || p.Limit > 0 || p.Offset > 0
	if !whole {
		rows, err := c.conn.QueryContext(ctx, "SELECT _rowid_ FROM "+c.Dialect.QuoteIdent(t.Name)+" LIMIT 0")
		if whole = err != nil; !whole {
			rows.Close()
		}
	}
	last := ""
	for {
		pg := p
		if last != "" {
			cond := "_rowid_ > " + last
			if strings.TrimSpace(p.RawWhere) != "" {
				cond += " AND (" + p.RawWhere + "\n)"
			}
			pg.RawWhere = cond
		}
		q, args, err := BuildSelect(c.Dialect, t, pg, columns)
		if err != nil {
			return err
		}
		if whole {
			q = "SELECT " + list + " FROM (" + q + ")"
		} else {
			q = "SELECT CAST(_rowid_ AS TEXT), " + list + strings.TrimPrefix(q, "SELECT *") + " ORDER BY _rowid_ LIMIT " + strconv.Itoa(d1Page)
		}
		rows, err := c.conn.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		rs, _, err := scanRows(rows, 0)
		if err != nil {
			return err
		}
		cols, types := rs.Columns, rs.Types
		if !whole {
			cols, types = cols[1:], types[1:]
		}
		for _, row := range rs.Rows {
			if !whole {
				last, row = row[0].Text, row[1:]
			}
			if err := fn(cols, types, row); err != nil {
				return err
			}
		}
		if whole || len(rs.Rows) < d1Page {
			return nil
		}
	}
}

var d1NoTx = regexp.MustCompile(`(?i)^(BEGIN|COMMIT|END|ROLLBACK|SAVEPOINT|RELEASE)\b`)
