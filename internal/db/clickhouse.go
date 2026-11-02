package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	_ "github.com/ClickHouse/clickhouse-go/v2" // registers the "clickhouse" driver

	"gotable/internal/model"
)

// clickhouseEngine is ClickHouse over its native protocol. Databases play
// the role of schemas (as on MySQL). A table's primary key is its sort key,
// not a uniqueness constraint; there are no foreign keys or transactions.
type clickhouseEngine struct{}

func init() {
	openers[model.DriverClickHouse] = func(cfg *model.Connection, addr, password string) (*sql.DB, engine, error) {
		db, err := sql.Open("clickhouse", clickhouseDSN(cfg, addr, password))
		return db, clickhouseEngine{}, err
	}
}

// clickhouseDSN builds the clickhouse-go URL. mutations_sync makes
// ALTER TABLE … UPDATE/DELETE (how rows are edited) wait until applied.
func clickhouseDSN(cfg *model.Connection, addr, password string) string {
	u := url.URL{Scheme: "clickhouse", Host: addr, User: url.UserPassword(cfg.User, password), Path: "/" + cfg.DBName}
	q := url.Values{}
	switch cfg.SSLMode {
	case "require":
		q.Set("secure", "true")
	case "skip-verify":
		q.Set("secure", "true")
		q.Set("skip_verify", "true")
	}
	q.Set("dial_timeout", "15s")
	q.Set("mutations_sync", "2")
	q.Set("client_info_product", "GoTable/1")
	u.RawQuery = q.Encode()
	return u.String()
}

func (clickhouseEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var v string
	err := c.conn.QueryRowContext(ctx, "SELECT version()").Scan(&v)
	return "ClickHouse " + v, err
}

func (clickhouseEngine) databases(ctx context.Context, c *Conn) ([]string, error) {
	return c.queryStrings(ctx, `SELECT name FROM system.databases
		WHERE name NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema') ORDER BY name`)
}

func (e clickhouseEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	return e.databases(ctx, c)
}

func (clickhouseEngine) currentSchema(ctx context.Context, c *Conn) (string, error) {
	var s string
	err := c.conn.QueryRowContext(ctx, "SELECT currentDatabase()").Scan(&s)
	return s, err
}

func (clickhouseEngine) useSchema(ctx context.Context, c *Conn, schema string) error {
	_, err := c.conn.ExecContext(ctx, "USE "+c.Dialect.QuoteIdent(schema))
	return err
}

func (clickhouseEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT name, engine FROM system.tables
		WHERE database = ? AND NOT is_temporary ORDER BY name`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		o := Object{Schema: schema, Kind: KindTable}
		var engine string
		if err := rows.Scan(&o.Name, &engine); err != nil {
			return nil, err
		}
		if engine == "View" || engine == "MaterializedView" || engine == "LiveView" {
			o.Kind = KindView
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

var (
	chNullable  = regexp.MustCompile(`^Nullable\((.*)\)$`)
	chLiteral   = regexp.MustCompile(`^'(?:[^'\\]|\\.|'')*'$`)
	chEnumValue = regexp.MustCompile(`'((?:[^'\\]|\\.|'')*)'`)
)

// chNullableType wraps a type so it allows NULL: Nullable(String), or
// LowCardinality(Nullable(String)) (Nullable can't wrap LowCardinality).
func chNullableType(t string) string {
	t = strings.TrimSpace(t)
	switch {
	case strings.Contains(t, "Nullable("):
		return t
	case strings.HasPrefix(t, "LowCardinality(") && strings.HasSuffix(t, ")"):
		return "LowCardinality(Nullable(" + t[len("LowCardinality("):len(t)-1] + "))"
	}
	return "Nullable(" + t + ")"
}

func (clickhouseEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT name, type, default_kind, default_expression, comment, is_in_primary_key
		FROM system.columns WHERE database = ? AND table = ? ORDER BY position`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		var kind, def string
		var pk uint8
		if err := rows.Scan(&col.Name, &col.Type, &kind, &def, &col.Comment, &pk); err != nil {
			return nil, err
		}
		col.PrimaryKey = pk == 1
		// Nullable(T) is how a column allows NULL; show T and the flag.
		if m := chNullable.FindStringSubmatch(col.Type); m != nil {
			col.Type, col.Nullable = m[1], true
		} else {
			col.Nullable = strings.Contains(col.Type, "Nullable(") // LowCardinality(Nullable(String))
		}
		if kind == "DEFAULT" && def != "" {
			col.Default = &def
			col.DefaultKind, col.DefaultText = mssqlDefault(def)
			if chLiteral.MatchString(def) { // 'it\'s'
				col.DefaultKind, col.DefaultText = DefaultValue, chUnquote(def)
			}
		}
		if strings.HasPrefix(col.Type, "Enum") {
			for _, m := range chEnumValue.FindAllStringSubmatch(col.Type, -1) {
				col.Values = append(col.Values, chUnquote("'"+m[1]+"'"))
			}
		}
		out = append(out, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("table %s not found", c.Dialect.QualifiedName(t))
	}
	return out, nil
}

// chUnquote reads a ClickHouse string literal: backslash escapes, ” too.
func chUnquote(s string) string {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return s
	}
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
		case s[i] == '\'' && i+1 < len(s) && s[i+1] == '\'':
			b.WriteByte('\'')
			i++
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func (clickhouseEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	var out []Index
	var pk string
	if err := c.conn.QueryRowContext(ctx, "SELECT primary_key FROM system.tables WHERE database = ? AND name = ?", t.Schema, t.Name).Scan(&pk); err != nil {
		return nil, err
	}
	if pk != "" {
		// The sort key: it orders and indexes the data, but isn't unique.
		out = append(out, Index{Name: "PRIMARY", Columns: chExprList(pk), Primary: true, Type: "primary", Constraint: true})
	}
	rows, err := c.conn.QueryContext(ctx, `SELECT name, type_full, expr FROM system.data_skipping_indices
		WHERE database = ? AND table = ? ORDER BY name`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x Index
		var expr string
		if err := rows.Scan(&x.Name, &x.Type, &expr); err != nil {
			return nil, err
		}
		x.Columns = chExprList(expr)
		out = append(out, x)
	}
	return out, rows.Err()
}

// chExprList splits "a, b" or "(a, b)" into names, unquoting `quoted` ones.
func chExprList(s string) []string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") && balanced(s[1:len(s)-1]) {
		s = s[1 : len(s)-1]
	}
	var out []string
	for _, p := range splitTopLevel(s) {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && p[0] == '`' && p[len(p)-1] == '`' {
			p = p[1 : len(p)-1]
		}
		out = append(out, p)
	}
	return out
}

// ClickHouse has no foreign keys.
func (clickhouseEngine) foreignKeys(context.Context, *Conn, TableRef) ([]ForeignRef, error) {
	return nil, nil
}

var chConstraint = regexp.MustCompile("(?m)^\\s*CONSTRAINT\\s+(`[^`]+`|\\S+)\\s+CHECK\\s+(.+?),?$")

func (clickhouseEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	var ti TableInfo
	var create string
	err := c.conn.QueryRowContext(ctx, "SELECT comment, engine, create_table_query FROM system.tables WHERE database = ? AND name = ?",
		t.Schema, t.Name).Scan(&ti.Comment, &ti.Engine, &create)
	if err != nil {
		return ti, err
	}
	// CHECK constraints live only in the table's definition.
	if def, err := (clickhouseEngine{}).definition(ctx, c, Object{Schema: t.Schema, Name: t.Name, Kind: KindTable}); err == nil {
		create = def
	}
	for _, m := range chConstraint.FindAllStringSubmatch(create, -1) {
		ti.Checks = append(ti.Checks, Check{Name: strings.Trim(m[1], "`"), Expression: strings.TrimSpace(m[2])})
	}
	return ti, nil
}

func (clickhouseEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	var def string
	err := c.conn.QueryRowContext(ctx, "SHOW CREATE TABLE "+c.Dialect.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name})).Scan(&def)
	return strings.TrimSpace(def), err
}

// splitTopLevel splits on commas outside brackets and quotes.
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '\'' || ch == '"':
			quote = ch
		case ch == '(' || ch == '[':
			depth++
		case ch == ')' || ch == ']':
			depth--
		case ch == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if strings.TrimSpace(s[start:]) != "" {
		out = append(out, s[start:])
	}
	return out
}
