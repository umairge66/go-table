package db

// InfluxDB 3 (Core and Enterprise): time series over HTTP, read with SQL.
//
//   - A database is a schema. A table's key is its tags and its time.
//   - There is no INSERT, UPDATE or DELETE: data is written as line protocol.
//     Writing a point again with the same key replaces the fields it names,
//     so a cell can be edited; a field can't be set back to NULL, and rows
//     can't be deleted.
//   - The editor runs SQL queries, and GoTable's own statements for what SQL
//     can't do there: WRITE (line protocol), CREATE and DROP TABLE, and
//     CREATE and DROP DATABASE.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gotable/internal/model"
)

const influxBatch = 5000 // lines per write

type influx struct {
	base    string
	token   string
	http    *http.Client
	version string
}

var (
	errInfluxDelete = errors.New("InfluxDB can't delete rows: delete the table, or let a retention period expire them")
	errInfluxNull   = errors.New("InfluxDB can't set a field back to NULL: a write only adds or replaces values")
)

func openInflux(ctx context.Context, cfg *model.Connection, addr, password string, tunnel io.Closer) (*Conn, error) {
	fail := func(err error) (*Conn, error) {
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, err
	}
	x := &influx{token: strings.TrimSpace(password)}
	var err error
	if x.base, x.http, err = remoteBase(cfg, addr, tunnel != nil); err != nil {
		return fail(err)
	}
	res, err := x.send(ctx, http.MethodGet, "/ping", nil, "")
	var refused *influxError
	if errors.As(err, &refused) && refused.Version != "" {
		return fail(influxOld(refused.Version))
	}
	if err != nil {
		return fail(err)
	}
	res.Body.Close()
	x.version = res.Header.Get("X-Influxdb-Version")
	switch {
	case x.version == "":
		return fail(errors.New("the server's answer isn't InfluxDB's"))
	case !strings.HasPrefix(strings.TrimPrefix(x.version, "v"), "3."):
		return fail(influxOld(x.version))
	}
	if b := res.Header.Get("X-Influxdb-Build"); b != "" {
		x.version += " " + b
	}
	c := &Conn{Config: cfg, Dialect: DialectFor(cfg.Driver), eng: x, remote: x, tunnel: tunnel, schema: cfg.DBName}
	if c.schema == "" {
		dbs, err := x.schemas(ctx, c)
		if err != nil {
			return fail(err)
		}
		if len(dbs) > 0 {
			c.schema = dbs[0]
		}
	}
	// Wrong tokens are found out now, not at the first table.
	if c.schema != "" {
		if err := x.query(ctx, c.schema, "SELECT 1", func(map[string]json.RawMessage, []byte) error { return nil }); err != nil && !influxNoDB(err) {
			return fail(err)
		}
	}
	return c, nil
}

func influxOld(v string) error {
	return fmt.Errorf("the server is InfluxDB %s: GoTable works with InfluxDB 3 (Core or Enterprise)", strings.TrimPrefix(v, "v"))
}

// ---------------------------------------------------------------- requests

type influxError struct {
	Status  int
	Message string
	Version string
}

func (e *influxError) Error() string { return e.Message }

func influxNoDB(err error) bool {
	var e *influxError
	return errors.As(err, &e) && strings.Contains(e.Message, "database not found")
}

func (x *influx) send(ctx context.Context, method, path string, body []byte, contentType string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, x.base+path, rd)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if x.token != "" {
		req.Header.Set("Authorization", "Bearer "+x.token)
	}
	res, err := x.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if res.StatusCode < 300 {
		return res, nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	return nil, influxReadError(res, b)
}

func influxReadError(res *http.Response, body []byte) error {
	e := &influxError{Status: res.StatusCode, Version: res.Header.Get("X-Influxdb-Version")}
	var out struct {
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	type lineError struct {
		Message string `json:"error_message"`
		Line    int    `json:"line_number"`
	}
	if json.Unmarshal(body, &out) == nil && out.Error != "" {
		e.Message = strings.TrimPrefix(out.Error, "query error: ")
		var list []lineError
		var one lineError
		if json.Unmarshal(out.Data, &list) == nil && len(list) > 0 {
			one = list[0]
		} else {
			json.Unmarshal(out.Data, &one)
		}
		if one.Message != "" {
			e.Message = one.Message
			if one.Line > 0 {
				e.Message = fmt.Sprintf("line %d: %s", one.Line, one.Message)
			}
		}
	} else if e.Message = strings.TrimSpace(string(body)); e.Message == "" || strings.HasPrefix(e.Message, "<") {
		e.Message = "the server answered " + res.Status
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		e.Message = "authentication failed: the token is missing, wrong, or has no right to do this (" + e.Message + ")"
	}
	return e
}

// query runs SQL and calls fn for every row, as its values and its line.
func (x *influx) query(ctx context.Context, db, sql string, fn func(map[string]json.RawMessage, []byte) error) error {
	b, err := json.Marshal(map[string]string{"db": db, "q": sql, "format": "jsonl"})
	if err != nil {
		return err
	}
	res, err := x.send(ctx, http.MethodPost, "/api/v3/query_sql", b, "application/json")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	rd := bufio.NewReaderSize(res.Body, 64<<10)
	for {
		line, err := rd.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var row map[string]json.RawMessage
			if jerr := json.Unmarshal(line, &row); jerr != nil {
				if err != nil && err != io.EOF {
					return err
				}
				return fmt.Errorf("the server broke off: %s", snippet(string(line)))
			}
			if ferr := fn(row, line); ferr != nil {
				return ferr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}

// strings is the first column of a query named col.
func (x *influx) strings(ctx context.Context, db, sql, col string) ([]string, error) {
	out := []string{}
	err := x.query(ctx, db, sql, func(row map[string]json.RawMessage, _ []byte) error {
		out = append(out, esValue(row[col]).Text)
		return nil
	})
	return out, err
}

// write sends line protocol, all of it or none.
func (x *influx) write(ctx context.Context, db string, lines []byte) error {
	res, err := x.send(ctx, http.MethodPost, "/api/v3/write_lp?precision=nanosecond&accept_partial=false&db="+url.QueryEscape(db), lines, "text/plain; charset=utf-8")
	if err == nil {
		res.Body.Close()
	}
	return err
}

func (x *influx) ping(ctx context.Context, _ *Conn) error {
	res, err := x.send(ctx, http.MethodGet, "/ping", nil, "")
	if err == nil {
		res.Body.Close()
	}
	return err
}

// ---------------------------------------------------------------- engine

func (x *influx) serverVersion(context.Context, *Conn) (string, error) {
	return "InfluxDB " + x.version, nil
}

func (x *influx) databases(context.Context, *Conn) ([]string, error) { return []string{}, nil }

func (x *influx) schemas(ctx context.Context, c *Conn) ([]string, error) {
	res, err := x.send(ctx, http.MethodGet, "/api/v3/configure/database?format=json", nil, "")
	if err != nil {
		var e *influxError // a token of one database can't list them
		if errors.As(err, &e) && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden) && c.Config.DBName != "" {
			return []string{c.Config.DBName}, nil
		}
		return nil, err
	}
	defer res.Body.Close()
	var list []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		return nil, err
	}
	out := []string{}
	for _, m := range list {
		if n, _ := m["iox::database"].(string); n != "" && n != "_internal" {
			out = append(out, n)
		}
	}
	if d := c.Config.DBName; d != "" && !slices.Contains(out, d) {
		out = append(out, d) // made by the first write
	}
	slices.Sort(out)
	return out, nil
}

func (x *influx) currentSchema(_ context.Context, c *Conn) (string, error) { return c.schema, nil }

func (x *influx) useSchema(ctx context.Context, c *Conn, schema string) error {
	dbs, err := x.schemas(ctx, c)
	if err == nil && !slices.Contains(dbs, schema) {
		err = fmt.Errorf("no database %q", schema)
	}
	return err
}

// influxDeleted: a deleted table stays listed for a while as name-20260102T030405.
var influxDeleted = regexp.MustCompile(`-\d{8}T\d{6}$`)

func (x *influx) objects(ctx context.Context, _ *Conn, schema string) ([]Object, error) {
	names, err := x.strings(ctx, schema, "SELECT table_name FROM information_schema.tables WHERE table_schema = 'iox' ORDER BY table_name", "table_name")
	if err != nil {
		if influxNoDB(err) || schema == "" {
			return []Object{}, nil
		}
		return nil, err
	}
	out := []Object{}
	for _, n := range names {
		if !influxDeleted.MatchString(n) {
			out = append(out, Object{Schema: schema, Name: n, Kind: KindTable})
		}
	}
	return out, nil
}

type influxCol struct{ name, typ string }

const influxTag = "tag"

// cols are a table's columns in the server's order: tag, int64, uint64,
// float64, utf8, boolean or timestamp.
func (x *influx) cols(ctx context.Context, t TableRef) ([]influxCol, error) {
	var out []influxCol
	err := x.query(ctx, t.Schema, "SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'iox' AND table_name = "+influxText(t.Name)+" ORDER BY ordinal_position",
		func(row map[string]json.RawMessage, _ []byte) error {
			out = append(out, influxCol{esValue(row["column_name"]).Text, influxType(esValue(row["data_type"]).Text)})
			return nil
		})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("table %s not found", t.Name)
	}
	// The time first, then the tags, as points are thought of.
	rank := func(c influxCol) int {
		switch {
		case c.name == "time":
			return 0
		case c.typ == influxTag:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(out, func(a, b influxCol) int { return rank(a) - rank(b) })
	return out, nil
}

func influxType(arrow string) string {
	l := strings.ToLower(arrow)
	switch {
	case strings.HasPrefix(l, "dictionary("):
		return influxTag
	case strings.HasPrefix(l, "timestamp"):
		return "timestamp"
	}
	return l
}

func (x *influx) columns(ctx context.Context, _ *Conn, t TableRef) ([]Column, error) {
	cols, err := x.cols(ctx, t)
	if err != nil {
		return nil, err
	}
	out := make([]Column, len(cols))
	for i, c := range cols {
		out[i] = Column{Name: c.name, Type: c.typ, Nullable: c.name != "time", PrimaryKey: c.name == "time" || c.typ == influxTag}
		if c.name == "time" {
			out[i].AutoIncrement = true // the moment it is added, if left out
		}
	}
	return out, nil
}

func (x *influx) indexes(context.Context, *Conn, TableRef) ([]Index, error) { return []Index{}, nil }

func (x *influx) foreignKeys(context.Context, *Conn, TableRef) ([]ForeignRef, error) { return nil, nil }

func (x *influx) tableInfo(context.Context, *Conn, TableRef) (TableInfo, error) {
	return TableInfo{}, nil
}

func (x *influx) definition(ctx context.Context, _ *Conn, o Object) (string, error) {
	cols, err := x.cols(ctx, TableRef{Schema: o.Schema, Name: o.Name})
	if err != nil {
		return "", err
	}
	lines := []string{}
	for _, c := range cols {
		if c.name != "time" { // every table has it
			lines = append(lines, "  "+influxIdent(c.name)+" "+strings.ToUpper(c.typ))
		}
	}
	return "CREATE TABLE " + influxIdent(o.Name) + " (\n" + strings.Join(lines, ",\n") + "\n);", nil
}

// ---------------------------------------------------------------- reading

func influxIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func influxText(s string) string  { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// where is a page's WHERE clause, "" for none.
func influxWhere(p Page, cols []influxCol) (string, error) {
	var parts []string
	for _, f := range p.Filters {
		s, err := influxCondition(f, cols)
		if err != nil {
			return "", err
		}
		parts = append(parts, s)
	}
	if raw := strings.TrimSpace(p.RawWhere); raw != "" {
		parts = append(parts, "("+raw+"\n)") // a comment at its end stays in it
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(parts, " AND "), nil
}

func influxCondition(f Filter, cols []influxCol) (string, error) {
	if f.Column == AnyColumn {
		var parts []string
		for _, c := range cols {
			g := f
			g.Column = c.name
			if s, err := influxCondition(g, cols); err == nil { // a number can't be 'abc'
				parts = append(parts, s)
			}
		}
		if len(parts) == 0 {
			return "", errors.New("no column can be searched for that")
		}
		join := " OR "
		if f.Op == OpNe || f.Op == OpNotIn || f.Op == OpNotLike || f.Op == OpNotContain || f.Op == OpIsNotNull {
			join = " AND "
		}
		return "(" + strings.Join(parts, join) + ")", nil
	}
	i := slices.IndexFunc(cols, func(c influxCol) bool { return c.name == f.Column })
	if i < 0 {
		return "", fmt.Errorf("no column %q", f.Column)
	}
	c := cols[i]
	col := influxIdent(c.name)
	text := "lower(CAST(" + col + " AS VARCHAR))"
	low := influxText(strings.ToLower(f.Value))
	switch f.Op {
	case OpIsNull, OpIsNotNull:
		return col + " " + string(f.Op), nil
	case OpContains:
		return "strpos(" + text + ", " + low + ") > 0", nil
	case OpNotContain:
		return "strpos(" + text + ", " + low + ") = 0", nil
	case OpBeginsWith:
		return "starts_with(" + text + ", " + low + ")", nil
	case OpEndsWith:
		return "ends_with(" + text + ", " + low + ")", nil
	case OpLike, OpNotLike:
		return "CAST(" + col + " AS VARCHAR) " + string(f.Op) + " " + influxText(f.Value), nil
	case OpIn, OpNotIn:
		var vals []string
		for _, it := range splitList(f.Value) {
			v, err := influxLiteral(c, it)
			if err != nil {
				return "", err
			}
			vals = append(vals, v)
		}
		if len(vals) == 0 {
			return "", fmt.Errorf("%s needs at least one value", f.Op)
		}
		return col + " " + string(f.Op) + " (" + strings.Join(vals, ", ") + ")", nil
	}
	v, err := influxLiteral(c, f.Value)
	if err != nil {
		return "", err
	}
	if f.Op == OpBetween {
		v2, err := influxLiteral(c, f.Value2)
		if err != nil {
			return "", err
		}
		return col + " BETWEEN " + v + " AND " + v2, nil
	}
	return col + " " + string(f.Op) + " " + v, nil
}

// influxLiteral writes a filter's value as the column's type.
func influxLiteral(c influxCol, s string) (string, error) {
	s = strings.TrimSpace(s)
	switch c.typ {
	case "int64", "uint64", "float64":
		if !esNumber.MatchString(s) {
			return "", fmt.Errorf("%s: %q isn't a number", c.name, s)
		}
		return s, nil
	case "boolean":
		if l := strings.ToLower(s); l == "true" || l == "false" {
			return l, nil
		}
		return "", fmt.Errorf("%s: %q isn't true or false", c.name, s)
	case "timestamp":
		ns, err := influxTime(s)
		if err != nil {
			return "", fmt.Errorf("%s: %w", c.name, err)
		}
		return "TIMESTAMP " + influxText(time.Unix(0, ns).UTC().Format("2006-01-02T15:04:05.000000000Z")), nil
	}
	return influxText(s), nil
}

// influxTime reads a moment (UTC unless it says otherwise) as nanoseconds.
func influxTime(s string) (int64, error) {
	s = strings.TrimSpace(s)
	for _, l := range []string{"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			if y := t.Year(); y < 1678 || y > 2261 {
				return 0, fmt.Errorf("%q is out of range", s)
			}
			return t.UnixNano(), nil
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && len(strings.TrimPrefix(s, "-")) > 8 { // nanoseconds
		return n, nil
	}
	return 0, fmt.Errorf("%q isn't a time, such as 2024-03-15T10:20:30.5Z", s)
}

func influxOrder(p Page, cols []influxCol, dir string) string {
	var parts []string
	if len(p.Sort) > 0 {
		for _, s := range p.Sort {
			d := "ASC"
			if s.Dir == Desc {
				d = "DESC"
			}
			parts = append(parts, influxIdent(s.Column)+" "+d+" NULLS LAST")
		}
	} else { // the newest first; the tags make pages repeatable
		parts = append(parts, `"time" `+dir)
		for _, c := range cols {
			if c.typ == influxTag {
				parts = append(parts, influxIdent(c.name))
			}
		}
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}

func influxSelect(t TableRef, p Page, cols []influxCol, dir string) (string, error) {
	w, err := influxWhere(p, cols)
	if err != nil {
		return "", err
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = influxIdent(c.name)
	}
	q := "SELECT " + strings.Join(names, ", ") + " FROM " + influxIdent(t.Name) + w + influxOrder(p, cols, dir)
	if p.Limit > 0 {
		q += " LIMIT " + strconv.Itoa(p.Limit)
	}
	if p.Offset > 0 {
		q += " OFFSET " + strconv.Itoa(p.Offset)
	}
	return q, nil
}

func (x *influx) rows(ctx context.Context, t TableRef, p Page, dir string, fn func(cols, types []string, row []Value) error) ([]string, []string, error) {
	cols, err := x.cols(ctx, t)
	if err != nil {
		return nil, nil, err
	}
	q, err := influxSelect(t, p, cols, dir)
	if err != nil {
		return nil, nil, err
	}
	names, types := make([]string, len(cols)), make([]string, len(cols))
	for i, c := range cols {
		names[i], types[i] = c.name, c.typ
	}
	return names, types, x.query(ctx, t.Schema, q, func(m map[string]json.RawMessage, _ []byte) error {
		row := make([]Value, len(cols))
		for i, c := range cols {
			row[i] = esValue(m[c.name])
		}
		return fn(names, types, row)
	})
}

func (x *influx) fetchRows(ctx context.Context, _ *Conn, t TableRef, p Page) (*ResultSet, error) {
	rs := &ResultSet{Rows: [][]Value{}}
	var err error
	rs.Columns, rs.Types, err = x.rows(ctx, t, p, "DESC", func(_, _ []string, row []Value) error { rs.Rows = append(rs.Rows, row); return nil })
	if err != nil {
		return nil, err
	}
	return rs, nil
}

func (x *influx) streamRows(ctx context.Context, _ *Conn, t TableRef, p Page, fn func(cols, types []string, row []Value) error) error {
	_, _, err := x.rows(ctx, t, p, "ASC", fn)
	return err
}

func (x *influx) countRows(ctx context.Context, _ *Conn, t TableRef, p Page) (int64, error) {
	cols, err := x.cols(ctx, t)
	if err != nil {
		return 0, err
	}
	w, err := influxWhere(p, cols)
	if err != nil {
		return 0, err
	}
	n, err := x.strings(ctx, t.Schema, "SELECT count(*) AS n FROM "+influxIdent(t.Name)+w, "n")
	if err != nil || len(n) == 0 {
		return 0, err
	}
	return strconv.ParseInt(n[0], 10, 64)
}

// ---------------------------------------------------------------- line protocol

var (
	lpMeasurement = strings.NewReplacer(`,`, `\,`, ` `, `\ `)
	lpKey         = strings.NewReplacer(`,`, `\,`, `=`, `\=`, ` `, `\ `)
	lpString      = strings.NewReplacer(`\`, `\\`, `"`, `\"`)
)

func lpName(s string, r *strings.Replacer) (string, error) {
	if s == "" || strings.ContainsAny(s, "\n\r") || strings.HasSuffix(s, `\`) {
		return "", fmt.Errorf("%q can't be written as line protocol", s)
	}
	return r.Replace(s), nil
}

// lpField writes a field's value as its type.
func lpField(typ string, s string) (string, error) {
	t := strings.TrimSpace(s)
	switch typ {
	case "int64":
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return "", fmt.Errorf("%q isn't a whole number", s)
		}
		return strconv.FormatInt(n, 10) + "i", nil
	case "uint64":
		n, err := strconv.ParseUint(t, 10, 64)
		if err != nil {
			return "", fmt.Errorf("%q isn't a whole number without a sign", s)
		}
		return strconv.FormatUint(n, 10) + "u", nil
	case "float64":
		n, err := strconv.ParseFloat(t, 64)
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return "", fmt.Errorf("%q isn't a number", s)
		}
		return strconv.FormatFloat(n, 'g', -1, 64), nil
	case "boolean":
		switch strings.ToLower(t) {
		case "true", "t", "1":
			return "true", nil
		case "false", "f", "0":
			return "false", nil
		}
		return "", fmt.Errorf("%q isn't true or false", s)
	case "utf8":
		return `"` + lpString.Replace(s) + `"`, nil
	}
	return "", fmt.Errorf("a %s can't be written", typ)
}

// lpLine writes a point. Tags and fields left out (or NULL) aren't written.
func lpLine(table string, cols []influxCol, vals map[string]Value, ns int64) (string, error) {
	b, err := lpName(table, lpMeasurement)
	if err != nil {
		return "", err
	}
	var fields []string
	for _, c := range cols {
		v, ok := vals[c.name]
		if !ok || v.Null || c.name == "time" {
			continue
		}
		k, err := lpName(c.name, lpKey)
		if err != nil {
			return "", err
		}
		if c.typ == influxTag {
			if v.Text == "" { // the same as no tag
				continue
			}
			s, err := lpName(v.Text, lpKey)
			if err != nil {
				return "", fmt.Errorf("%s: %w", c.name, err)
			}
			b += "," + k + "=" + s
			continue
		}
		s, err := lpField(c.typ, v.Text)
		if err != nil {
			return "", fmt.Errorf("%s: %w", c.name, err)
		}
		fields = append(fields, k+"="+s)
	}
	if len(fields) == 0 {
		return "", errors.New("a point needs a value in at least one field (a column that isn't a tag or the time)")
	}
	return b + " " + strings.Join(fields, ",") + " " + strconv.FormatInt(ns, 10), nil
}

// lpEnd is where the line of line protocol that starts at i ends (after its
// newline): strings in fields may hold newlines, tags hold bare quotes.
func lpEnd(s string, i int) int {
	if i < len(s) && s[i] == '#' { // a comment
		if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
			return i + j + 1
		}
		return len(s)
	}
	fields, str := false, false
	for ; i < len(s); i++ {
		switch ch := s[i]; {
		case ch == '\\' && i+1 < len(s) && s[i+1] != '\n':
			i++
		case str:
			str = ch != '"'
		case ch == '\n':
			return i + 1
		case ch == ' ':
			fields = true
		case fields && ch == '"' && s[i-1] == '=':
			str = true
		}
	}
	return len(s)
}

// ---------------------------------------------------------------- editing

func (x *influx) plan(ctx context.Context, t TableRef, changes []Change) ([]string, error) {
	cols, err := x.cols(ctx, t)
	if err != nil {
		return nil, err
	}
	typ := map[string]string{}
	for _, c := range cols {
		typ[c.name] = c.typ
	}
	now := time.Now().UnixNano()
	var out []string
	for i, ch := range changes {
		switch ch.Kind {
		case ChangeDelete:
			return nil, errInfluxDelete
		case ChangeInsert:
			ns := now + int64(i) // points of the same moment would replace each other
			key := map[string]Value{}
			if v, ok := ch.Values["time"]; ok && !v.Null && v.Text != "" {
				if ns, err = influxTime(v.Text); err != nil {
					return nil, fmt.Errorf("time: %w", err)
				}
			}
			for _, c := range cols {
				if c.typ == influxTag {
					key[c.name] = ch.Values[c.name]
					if v, ok := ch.Values[c.name]; !ok || v.Text == "" {
						key[c.name] = NullValue
					}
				}
			}
			if n, err := x.points(ctx, t, cols, key, ns); err != nil {
				return nil, err
			} else if n > 0 {
				return nil, errors.New("a point with these tags and this time exists: it would be replaced")
			}
			line, err := lpLine(t.Name, cols, ch.Values, ns)
			if err != nil {
				return nil, err
			}
			out = append(out, line)
		case ChangeUpdate:
			v, ok := ch.Key["time"]
			if !ok {
				return nil, errors.New("the point's time is missing")
			}
			ns, err := influxTime(v.Text)
			if err != nil {
				return nil, err
			}
			vals := map[string]Value{}
			for k, v := range ch.Key {
				if typ[k] == influxTag {
					vals[k] = v
				}
			}
			for _, k := range sortedKeys(ch.Values) {
				v := ch.Values[k]
				switch {
				case k == "time" || typ[k] == influxTag:
					return nil, fmt.Errorf("%s is part of the point's key, which can't change: add a new point", k)
				case v.Null:
					return nil, fmt.Errorf("%s: %w", k, errInfluxNull)
				}
				vals[k] = v
			}
			n, err := x.points(ctx, t, cols, vals, ns)
			if err != nil {
				return nil, err
			}
			if n == 0 {
				return nil, errMongoRowGone
			}
			line, err := lpLine(t.Name, cols, vals, ns)
			if err != nil {
				return nil, err
			}
			out = append(out, line)
		}
	}
	return out, nil
}

// points counts the points with these tags and this time (0 or 1).
func (x *influx) points(ctx context.Context, t TableRef, cols []influxCol, tags map[string]Value, ns int64) (int64, error) {
	q := "SELECT count(*) AS n FROM " + influxIdent(t.Name) + ` WHERE "time" = TIMESTAMP ` + influxText(time.Unix(0, ns).UTC().Format("2006-01-02T15:04:05.000000000Z"))
	for _, c := range cols {
		if c.typ != influxTag {
			continue
		}
		if v, ok := tags[c.name]; !ok || v.Null {
			q += " AND " + influxIdent(c.name) + " IS NULL"
		} else {
			q += " AND " + influxIdent(c.name) + " = " + influxText(v.Text)
		}
	}
	n, err := x.strings(ctx, t.Schema, q, "n")
	if err != nil || len(n) == 0 {
		return 0, err
	}
	return strconv.ParseInt(n[0], 10, 64)
}

func (x *influx) previewChanges(ctx context.Context, _ *Conn, t TableRef, changes []Change) ([]string, error) {
	lines, err := x.plan(ctx, t, changes)
	for i, l := range lines {
		lines[i] = "WRITE " + l
	}
	return lines, err
}

func (x *influx) applyChanges(ctx context.Context, _ *Conn, t TableRef, changes []Change) error {
	lines, err := x.plan(ctx, t, changes)
	if err != nil || len(lines) == 0 {
		return err
	}
	return x.write(ctx, t.Schema, []byte(strings.Join(lines, "\n")+"\n"))
}

// ---------------------------------------------------------------- the query editor

var (
	influxWriteRe  = regexp.MustCompile(`(?im)^[ \t]*WRITE[ \t]*\r?\n`)
	influxCreateRe = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+(IF\s+NOT\s+EXISTS\s+)?("(?:[^"]|"")+"|[\w.\-]+)\s*\((.*)\)\s*;?$`)
	influxColRe    = regexp.MustCompile(`(?is)^\s*("(?:[^"]|"")+"|[\w.\-]+)\s+([a-z0-9_ ]+?)\s*$`)
	influxDropRe   = regexp.MustCompile(`(?is)^DROP\s+(TABLE|DATABASE)\s+(IF\s+EXISTS\s+)?("(?:[^"]|"")+"|[\w.\-]+)\s*;?$`)
	influxMakeRe   = regexp.MustCompile(`(?is)^CREATE\s+DATABASE\s+(IF\s+NOT\s+EXISTS\s+)?("(?:[^"]|"")+"|[\w.\-]+)\s*;?$`)
)

// split: a line with WRITE alone starts a block of line protocol, to the
// next empty line; the rest is SQL.
func (x *influx) split(script string) []Span {
	var out []Span
	sql := func(from, to int) {
		for _, sp := range SplitStatements(model.DriverInfluxDB, script[from:to]) {
			out = append(out, Span{SQL: sp.SQL, Start: sp.Start + from, End: sp.End + from})
		}
	}
	at := 0
	for {
		m := influxWriteRe.FindStringIndex(script[at:])
		if m == nil {
			break
		}
		sql(at, at+m[0])
		start := at + m[0]
		i := at + m[1]
		for i < len(script) {
			end := lpEnd(script, i)
			if strings.TrimSpace(script[i:end]) == "" {
				i = end
				break
			}
			i = end
		}
		out = append(out, Span{SQL: strings.TrimSpace(script[start:i]), Start: start, End: i})
		at = i
	}
	sql(at, len(script))
	return out
}

func (x *influx) run(ctx context.Context, c *Conn, stmt string, rowCap int) (r Result) {
	r = Result{Statement: stmt}
	start := time.Now()
	defer func() { r.Duration = time.Since(start) }()
	s := strings.TrimSpace(dynLeading.ReplaceAllString(stmt, ""))
	if m := influxWriteRe.FindStringIndex(s); m != nil && m[0] == 0 {
		lines := s[m[1]:]
		for i := 0; i < len(lines); {
			end := lpEnd(lines, i)
			if l := strings.TrimSpace(lines[i:end]); l != "" && l[0] != '#' {
				r.RowsAffected++
			}
			i = end
		}
		if r.Err = x.write(ctx, c.schema, []byte(lines+"\n")); r.Err != nil {
			r.RowsAffected = 0
		}
		return r
	}
	if m := influxCreateRe.FindStringSubmatch(s); m != nil {
		r.Err = x.create(ctx, c.schema, dynUnquote(m[2]), m[3])
		if m[1] != "" && influxConflict(r.Err) {
			r.Err = nil
		}
		return r
	}
	if m := influxMakeRe.FindStringSubmatch(s); m != nil {
		b, _ := json.Marshal(map[string]string{"db": dynUnquote(m[2])})
		r.Err = x.configure(ctx, http.MethodPost, "/api/v3/configure/database", b)
		if m[1] != "" && influxConflict(r.Err) {
			r.Err = nil
		}
		return r
	}
	if m := influxDropRe.FindStringSubmatch(s); m != nil {
		name := dynUnquote(m[3])
		if strings.EqualFold(m[1], "DATABASE") {
			r.Err = x.configure(ctx, http.MethodDelete, "/api/v3/configure/database?db="+url.QueryEscape(name), nil)
		} else {
			r.Err = x.dropTable(ctx, c.schema, name)
		}
		var e *influxError
		if m[2] != "" && errors.As(r.Err, &e) && e.Status == http.StatusNotFound {
			r.Err = nil
		}
		return r
	}
	s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	rs := &ResultSet{Columns: []string{}, Types: []string{}, Rows: [][]Value{}}
	at := map[string]int{}
	add := func(name, typ string) {
		if _, ok := at[name]; !ok {
			at[name] = len(rs.Columns)
			rs.Columns, rs.Types = append(rs.Columns, name), append(rs.Types, typ)
		}
	}
	// The rows leave NULLs out, so the columns are asked for.
	if f := strings.Fields(s); len(f) > 0 && (strings.EqualFold(f[0], "SELECT") || strings.EqualFold(f[0], "WITH")) {
		x.query(ctx, c.schema, "DESCRIBE "+s, func(row map[string]json.RawMessage, _ []byte) error {
			add(esValue(row["column_name"]).Text, influxType(esValue(row["data_type"]).Text))
			return nil
		})
	}
	var rows []map[string]json.RawMessage
	err := x.query(ctx, c.schema, s, func(row map[string]json.RawMessage, line []byte) error {
		if rowCap > 0 && len(rows) >= rowCap {
			r.Truncated = true
			return errInfluxEnough
		}
		for _, k := range jsonKeys(line) {
			add(k, "")
		}
		rows = append(rows, row)
		return nil
	})
	if err != nil && !errors.Is(err, errInfluxEnough) {
		r.Err = err
		return r
	}
	for _, m := range rows {
		row := make([]Value, len(rs.Columns))
		for i, n := range rs.Columns {
			row[i] = esValue(m[n])
		}
		rs.Rows = append(rs.Rows, row)
	}
	r.Set = rs
	return r
}

var errInfluxEnough = errors.New("enough rows")

func influxConflict(err error) bool {
	var e *influxError
	return errors.As(err, &e) && (e.Status == http.StatusConflict || strings.Contains(e.Message, "already exists"))
}

func (x *influx) configure(ctx context.Context, method, path string, body []byte) error {
	ct := ""
	if body != nil {
		ct = "application/json"
	}
	res, err := x.send(ctx, method, path, body, ct)
	if err == nil {
		res.Body.Close()
	}
	return err
}

// create: CREATE TABLE "t" ("site" TAG, "temp" FLOAT64, …); the time column
// is always there.
func (x *influx) create(ctx context.Context, db, name, defs string) error {
	type field struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	in := struct {
		DB     string   `json:"db"`
		Table  string   `json:"table"`
		Tags   []string `json:"tags"`
		Fields []field  `json:"fields"`
	}{db, name, []string{}, []field{}}
	for _, def := range influxDefs(defs) {
		m := influxColRe.FindStringSubmatch(def)
		if m == nil {
			return fmt.Errorf(`%q: a column is "name" TAG, or "name" and a type (FLOAT64, INT64, UINT64, UTF8, BOOLEAN)`, strings.TrimSpace(def))
		}
		n := dynUnquote(m[1])
		switch strings.ToLower(m[2]) {
		case "tag":
			in.Tags = append(in.Tags, n)
		case "float64", "double", "float", "real", "double precision":
			in.Fields = append(in.Fields, field{n, "float64"})
		case "int64", "bigint", "int", "integer":
			in.Fields = append(in.Fields, field{n, "int64"})
		case "uint64", "bigint unsigned":
			in.Fields = append(in.Fields, field{n, "uint64"})
		case "utf8", "text", "string", "varchar":
			in.Fields = append(in.Fields, field{n, "utf8"})
		case "boolean", "bool":
			in.Fields = append(in.Fields, field{n, "bool"})
		case "timestamp":
			if n != "time" {
				return fmt.Errorf("%s: only the column time holds times", n)
			}
		default:
			return fmt.Errorf("%s: the types are TAG, FLOAT64, INT64, UINT64, UTF8 and BOOLEAN, not %s", n, m[2])
		}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return x.configure(ctx, http.MethodPost, "/api/v3/configure/table", b)
}

// influxDefs cuts column definitions at the commas outside quoted names.
func influxDefs(s string) []string {
	var out []string
	start, quoted := 0, false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			quoted = !quoted
		case s[i] == ',' && !quoted:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func (x *influx) dropTable(ctx context.Context, db, name string) error {
	return x.configure(ctx, http.MethodDelete, "/api/v3/configure/table?db="+url.QueryEscape(db)+"&table="+url.QueryEscape(name), nil)
}

// ---------------------------------------------------------------- structure, export and import

func (x *influx) createTable(context.Context, *Conn, string, NewTable) ([]string, error) {
	return nil, errors.New(`a table is made in the query editor: CREATE TABLE "name" ("site" TAG, "temp" FLOAT64), or by writing to it`)
}

func (x *influx) alter(_ context.Context, _ *Conn, _ TableRef, ops []AlterOp, _ bool) ([]string, error) {
	for _, op := range ops {
		if op.Kind == AlterTruncate {
			return nil, errInfluxDelete
		}
		return nil, fmt.Errorf("InfluxDB can't %s: a new field is added by writing one", strings.ReplaceAll(string(op.Kind), "_", " "))
	}
	return nil, nil
}

func buildInfluxDrop(objs []Object) []string {
	out := make([]string, len(objs))
	for i, o := range objs {
		out[i] = "DROP TABLE " + influxIdent(o.Name) + ";"
	}
	return out
}

func (x *influx) drop(ctx context.Context, _ *Conn, objs []Object) ([]string, error) {
	for i, o := range objs {
		if err := x.dropTable(ctx, o.Schema, o.Name); err != nil {
			return nil, fmt.Errorf("%s: %w (%d table(s) before it were deleted)", o.Name, err, i)
		}
	}
	return buildInfluxDrop(objs), nil
}

func (x *influx) dump(ctx context.Context, c *Conn, w io.Writer, o DumpOptions, progress func(DumpProgress)) error {
	p := DumpProgress{Tables: len(o.Objects)}
	out := &sqlWriter{w: w}
	out.printf("-- GoTable dump of InfluxDB %s, %s\n-- CREATE TABLE, DROP TABLE and WRITE (line protocol) are read by GoTable's Import.\n\n", x.version, time.Now().UTC().Format(time.RFC3339))
	for i, ob := range o.Objects {
		p.Table, p.TableIndex = ob.Name, i
		if progress != nil {
			progress(p)
		}
		ref := TableRef{Schema: ob.Schema, Name: ob.Name}
		if o.DropFirst {
			out.printf("DROP TABLE IF EXISTS %s;\n", influxIdent(ob.Name))
		}
		if o.Structure {
			def, err := x.definition(ctx, c, ob)
			if err != nil {
				return err
			}
			out.printf("%s\n\n", strings.Replace(def, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ", 1))
		}
		if !o.Data {
			continue
		}
		cols, err := x.cols(ctx, ref)
		if err != nil {
			return err
		}
		n := 0
		_, _, err = x.rows(ctx, ref, Page{}, "ASC", func(names, _ []string, row []Value) error {
			vals := map[string]Value{}
			for i, name := range names {
				vals[name] = row[i]
			}
			ns, err := influxTime(vals["time"].Text)
			if err != nil {
				return err
			}
			line, err := lpLine(ob.Name, cols, vals, ns)
			if err != nil {
				return err
			}
			if n%influxBatch == 0 {
				if n > 0 {
					out.printf("\n")
				}
				out.printf("WRITE\n")
			}
			n++
			out.printf("%s\n", line)
			p.Rows++
			if progress != nil && p.Rows%1000 == 0 {
				progress(p)
			}
			return out.err
		})
		if err != nil {
			return fmt.Errorf("%s: %w", ob.Name, err)
		}
		out.printf("\n")
	}
	if progress != nil {
		progress(p)
	}
	return out.err
}

func (x *influx) importCSV(ctx context.Context, _ *Conn, r io.Reader, t TableRef, o CSVImportOptions, progress func(int64)) (int64, error) {
	if o.Truncate {
		return 0, errInfluxDelete
	}
	cols, err := x.cols(ctx, t)
	if err != nil {
		return 0, err
	}
	ti := slices.Index(o.Columns, "time")
	if ti < 0 || o.Mapping[ti] < 0 {
		return 0, errors.New("choose the file's column for time: points without one would replace each other")
	}
	var done int64
	var buf bytes.Buffer
	n, first := int64(0), 0
	flush := func() error {
		if n == 0 {
			return nil
		}
		if err := x.write(ctx, t.Schema, buf.Bytes()); err != nil {
			return fmt.Errorf("rows from line %d: %w (%d rows were imported)", first, err, done)
		}
		done += n
		n = 0
		buf.Reset()
		if progress != nil {
			progress(done)
		}
		return nil
	}
	err = readCSV(r, o, func(line int, rec []string) error {
		vals := map[string]Value{}
		for i, col := range o.Columns {
			if o.Mapping[i] < 0 {
				continue
			}
			f := rec[o.Mapping[i]]
			if o.EmptyAsNull && f == "" || o.NullText != "" && f == o.NullText {
				continue
			}
			vals[col] = Text(f)
		}
		ns, err := influxTime(vals["time"].Text)
		if err == nil {
			var l string
			if l, err = lpLine(t.Name, cols, vals, ns); err == nil {
				if n == 0 {
					first = line
				}
				buf.WriteString(l)
				buf.WriteByte('\n')
				n++
			}
		}
		if err != nil {
			return fmt.Errorf("line %d: %w (%d rows were imported)", line, err, done)
		}
		if n >= influxBatch {
			return flush()
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	return done, err
}
