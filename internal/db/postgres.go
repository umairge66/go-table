package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/stdlib"

	"gotable/internal/model"
)

type postgresEngine struct{}

// postgresDSN builds the pgx URL. The simple protocol is used so values
// typed as text in the grid are coerced by the server to the column type,
// and so multi-statement session commands behave like psql.
func postgresDSN(cfg *model.Connection, addr, password string) string {
	dbname := cfg.DBName
	if dbname == "" {
		dbname = "postgres"
	}
	u := url.URL{Scheme: "postgres", Host: addr, Path: "/" + dbname}
	u.User = url.UserPassword(cfg.User, password)
	q := url.Values{}
	if cfg.SSLMode != "" {
		q.Set("sslmode", cfg.SSLMode)
	}
	if cfg.SSLCAPath != "" {
		q.Set("sslrootcert", cfg.SSLCAPath)
	}
	if cfg.SSLCertPath != "" {
		q.Set("sslcert", cfg.SSLCertPath)
	}
	if cfg.SSLKeyPath != "" {
		q.Set("sslkey", cfg.SSLKeyPath)
	}
	q.Set("default_query_exec_mode", "simple_protocol")
	q.Set("application_name", "GoTable")
	q.Set("connect_timeout", "15")
	u.RawQuery = q.Encode()
	return u.String()
}

func openPostgres(cfg *model.Connection, addr, password string) (*sql.DB, error) {
	return sql.Open("pgx", postgresDSN(cfg, addr, password))
}

var _ = stdlib.GetDefaultDriver // register the "pgx" driver

func (postgresEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var v string
	err := c.conn.QueryRowContext(ctx, "SHOW server_version").Scan(&v)
	return "PostgreSQL " + v, err
}

func (postgresEngine) databases(ctx context.Context, c *Conn) ([]string, error) {
	return c.queryStrings(ctx, "SELECT datname FROM pg_database WHERE NOT datistemplate ORDER BY datname")
}

func (postgresEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	return c.queryStrings(ctx, `SELECT nspname FROM pg_namespace
		WHERE nspname NOT IN ('pg_catalog', 'information_schema')
		  AND nspname NOT LIKE 'pg\_toast%' AND nspname NOT LIKE 'pg\_temp%'
		ORDER BY nspname`)
}

func (postgresEngine) currentSchema(ctx context.Context, c *Conn) (string, error) {
	var s sql.NullString
	err := c.conn.QueryRowContext(ctx, "SELECT current_schema()").Scan(&s)
	if err == nil && !s.Valid {
		return "public", nil
	}
	return s.String, err
}

func (postgresEngine) useSchema(ctx context.Context, c *Conn, schema string) error {
	_, err := c.conn.ExecContext(ctx, "SET search_path TO "+c.Dialect.QuoteIdent(schema))
	return err
}

func (postgresEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT c.relname, c.relkind::text FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		ORDER BY c.relname`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, err
		}
		k := KindTable
		if kind == "v" || kind == "m" {
			k = KindView
		}
		out = append(out, Object{Schema: schema, Name: name, Kind: k})
	}
	return out, rows.Err()
}

func (postgresEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT a.attname, format_type(a.atttypid, a.atttypmod),
			NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid), a.attidentity <> '',
			COALESCE((SELECT co.collname FROM pg_collation co JOIN pg_type ty ON ty.oid = a.atttypid
				WHERE co.oid = a.attcollation AND a.attcollation <> ty.typcollation), ''),
			EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = a.attrelid AND i.indisprimary AND a.attnum = ANY(i.indkey)),
			COALESCE(col_description(a.attrelid, a.attnum), ''),
			array_to_json(ARRAY(SELECT e.enumlabel FROM pg_enum e WHERE e.enumtypid = a.atttypid ORDER BY e.enumsortorder))::text
		FROM pg_attribute a
		JOIN pg_class cl ON cl.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = cl.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE n.nspname = $1 AND cl.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		var def sql.NullString
		var values string
		var identity bool
		if err := rows.Scan(&col.Name, &col.Type, &col.Nullable, &def, &identity, &col.Collation, &col.PrimaryKey, &col.Comment, &values); err != nil {
			return nil, err
		}
		if def.Valid {
			col.Default = &def.String
			col.DefaultKind, col.DefaultText = DefaultExpression, def.String
			if m := pgLiteral.FindStringSubmatch(def.String); m != nil {
				col.DefaultKind, col.DefaultText = DefaultValue, strings.ReplaceAll(m[1], "''", "'")
			}
		}
		col.AutoIncrement = identity || strings.HasPrefix(def.String, "nextval(")
		json.Unmarshal([]byte(values), &col.Values) // enum labels; [] for other types
		if len(col.Values) == 0 {
			col.Values = nil
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

func (postgresEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT i.relname, ix.indisunique, ix.indisprimary,
			array_to_string(ARRAY(SELECT pg_get_indexdef(ix.indexrelid, k + 1, true)
				FROM generate_subscripts(ix.indkey, 1) k ORDER BY k), ','),
			am.amname,
			EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conrelid = ix.indrelid AND con.conindid = ix.indexrelid AND con.contype IN ('p', 'u', 'x'))
		FROM pg_index ix
		JOIN pg_class tb ON tb.oid = ix.indrelid
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_am am ON am.oid = i.relam
		JOIN pg_namespace n ON n.oid = tb.relnamespace
		WHERE n.nspname = $1 AND tb.relname = $2
		ORDER BY ix.indisprimary DESC, i.relname`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Index
	for rows.Next() {
		var ix Index
		var cols string
		if err := rows.Scan(&ix.Name, &ix.Unique, &ix.Primary, &cols, &ix.Type, &ix.Constraint); err != nil {
			return nil, err
		}
		ix.Columns = strings.Split(cols, ",")
		out = append(out, ix)
	}
	return out, rows.Err()
}

func (e postgresEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	t := TableRef{Schema: o.Schema, Name: o.Name}
	name := c.Dialect.QualifiedName(t)
	if o.Kind == KindView {
		var kind, def string
		err := c.conn.QueryRowContext(ctx, `SELECT c.relkind::text, pg_get_viewdef(c.oid, true) FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2`,
			o.Schema, o.Name).Scan(&kind, &def)
		if err != nil {
			return "", err
		}
		if kind == "m" {
			return "CREATE MATERIALIZED VIEW " + name + " AS\n" + def, nil
		}
		return "CREATE OR REPLACE VIEW " + name + " AS\n" + def, nil
	}
	cols, err := e.columns(ctx, c, t)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(cols)+4)
	for _, col := range cols {
		l := "  " + c.Dialect.QuoteIdent(col.Name) + " " + col.Type
		if !col.Nullable {
			l += " NOT NULL"
		}
		if col.Default != nil {
			l += " DEFAULT " + *col.Default
		}
		lines = append(lines, l)
	}
	rows, err := c.conn.QueryContext(ctx, `SELECT conname, pg_get_constraintdef(co.oid, true) FROM pg_constraint co
		JOIN pg_class cl ON cl.oid = co.conrelid JOIN pg_namespace n ON n.oid = cl.relnamespace
		WHERE n.nspname = $1 AND cl.relname = $2
		ORDER BY CASE co.contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'f' THEN 2 ELSE 3 END, conname`, o.Schema, o.Name)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var cn, def string
		if err := rows.Scan(&cn, &def); err != nil {
			rows.Close()
			return "", err
		}
		lines = append(lines, "  CONSTRAINT "+c.Dialect.QuoteIdent(cn)+" "+def)
	}
	rows.Close()
	ddl := "CREATE TABLE " + name + " (\n" + strings.Join(lines, ",\n") + "\n);"
	idx, err := c.queryStrings(ctx, `SELECT pg_get_indexdef(ix.indexrelid) FROM pg_index ix
		JOIN pg_class tb ON tb.oid = ix.indrelid JOIN pg_namespace n ON n.oid = tb.relnamespace
		WHERE n.nspname = $1 AND tb.relname = $2
		  AND NOT EXISTS (SELECT 1 FROM pg_constraint co WHERE co.conindid = ix.indexrelid)
		ORDER BY 1`, o.Schema, o.Name)
	if err != nil {
		return "", err
	}
	for _, d := range idx {
		ddl += "\n" + d + ";"
	}
	return ddl, nil
}

func (postgresEngine) foreignKeys(ctx context.Context, c *Conn, t TableRef) ([]ForeignRef, error) {
	return scanForeignKeys(c.conn.QueryContext(ctx, `SELECT con.conname, a.attname, nr.nspname, cr.relname, af.attname,
			`+pgAction("con.confdeltype")+`, `+pgAction("con.confupdtype")+`
		FROM pg_constraint con
		JOIN pg_class cl ON cl.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = cl.relnamespace
		JOIN pg_class cr ON cr.oid = con.confrelid
		JOIN pg_namespace nr ON nr.oid = cr.relnamespace
		CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS k(col, fcol, ord)
		JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.col
		JOIN pg_attribute af ON af.attrelid = con.confrelid AND af.attnum = k.fcol
		WHERE con.contype = 'f' AND n.nspname = $1 AND cl.relname = $2
		ORDER BY con.conname, k.ord`, t.Schema, t.Name))
}

// pgLiteral matches a default that is a plain literal: 'text'::type.
var pgLiteral = regexp.MustCompile(`^'((?:[^']|'')*)'::[\w\s".\[\]()]+$`)

// pgAction spells a pg_constraint referential action code.
func pgAction(col string) string {
	return "CASE " + col + " WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' ELSE 'NO ACTION' END"
}

func (postgresEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	var ti TableInfo
	err := c.conn.QueryRowContext(ctx, `SELECT COALESCE(obj_description(cl.oid, 'pg_class'), '')
		FROM pg_class cl JOIN pg_namespace n ON n.oid = cl.relnamespace
		WHERE n.nspname = $1 AND cl.relname = $2`, t.Schema, t.Name).Scan(&ti.Comment)
	if err != nil {
		return ti, err
	}
	rows, err := c.conn.QueryContext(ctx, `SELECT con.conname, pg_get_constraintdef(con.oid, true)
		FROM pg_constraint con
		JOIN pg_class cl ON cl.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = cl.relnamespace
		WHERE con.contype = 'c' AND n.nspname = $1 AND cl.relname = $2
		ORDER BY con.conname`, t.Schema, t.Name)
	if err != nil {
		return ti, err
	}
	defer rows.Close()
	for rows.Next() {
		var ck Check
		if err := rows.Scan(&ck.Name, &ck.Expression); err != nil {
			return ti, err
		}
		// "CHECK ((price > 0))" → "price > 0"
		e := strings.TrimSpace(strings.TrimPrefix(ck.Expression, "CHECK "))
		for strings.HasPrefix(e, "(") && strings.HasSuffix(e, ")") && balanced(e[1:len(e)-1]) {
			e = e[1 : len(e)-1]
		}
		ck.Expression = e
		ti.Checks = append(ti.Checks, ck)
	}
	return ti, rows.Err()
}

// balanced reports whether brackets in s pair up (outside quotes).
func balanced(s string) bool {
	depth := 0
	quote := false
	for _, r := range s {
		switch {
		case r == '\'':
			quote = !quote
		case quote:
		case r == '(':
			depth++
		case r == ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}
