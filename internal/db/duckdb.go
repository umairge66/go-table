//go:build cgo

package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2" // registers the "duckdb" driver

	"gotable/internal/model"
)

// duckdbEngine is DuckDB, an embedded analytics database in one file. Its
// SQL follows PostgreSQL's; the catalog is read through duckdb_*() functions.
// A "schema" is a schema of the opened file (main, …).
type duckdbEngine struct{}

func init() {
	openers[model.DriverDuckDB] = func(cfg *model.Connection, _, _ string) (*sql.DB, engine, error) {
		db, err := sql.Open("duckdb", cfg.Path)
		return db, duckdbEngine{}, err
	}
}

func (duckdbEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var v string
	err := c.conn.QueryRowContext(ctx, "SELECT version()").Scan(&v)
	return "DuckDB " + v, err
}

// databases lists the attached database files (like SQLite's).
func (duckdbEngine) databases(ctx context.Context, c *Conn) ([]string, error) {
	return c.queryStrings(ctx, "SELECT database_name FROM duckdb_databases() WHERE NOT internal ORDER BY 1")
}

func (duckdbEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	return c.queryStrings(ctx, `SELECT DISTINCT schema_name FROM duckdb_schemas()
		WHERE database_name = current_database() AND schema_name NOT IN ('information_schema', 'pg_catalog')
		ORDER BY schema_name <> 'main', 1`)
}

func (duckdbEngine) currentSchema(ctx context.Context, c *Conn) (string, error) {
	var s string
	err := c.conn.QueryRowContext(ctx, "SELECT current_schema()").Scan(&s)
	return s, err
}

func (duckdbEngine) useSchema(ctx context.Context, c *Conn, schema string) error {
	_, err := c.conn.ExecContext(ctx, "USE "+c.Dialect.QuoteIdent(schema))
	return err
}

func (duckdbEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT table_name, 'table' FROM duckdb_tables()
			WHERE database_name = current_database() AND schema_name = ? AND NOT internal
		UNION ALL SELECT view_name, 'view' FROM duckdb_views()
			WHERE database_name = current_database() AND schema_name = ? AND NOT internal
		ORDER BY 1`, schema, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		o := Object{Schema: schema}
		if err := rows.Scan(&o.Name, &o.Kind); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// duckdbTable filters duckdb_*() rows to one table of the open database.
const duckdbTable = "database_name = current_database() AND schema_name = ? AND table_name = ?"

var enumValue = regexp.MustCompile(`'((?:[^']|'')*)'`)

func (duckdbEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	pk, _, err := duckdbConstraintColumns(ctx, c, t, "PRIMARY KEY")
	if err != nil {
		return nil, err
	}
	rows, err := c.conn.QueryContext(ctx, `SELECT column_name, data_type, is_nullable, column_default, COALESCE(comment, '')
		FROM duckdb_columns() WHERE `+duckdbTable+` ORDER BY column_index`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		var def sql.NullString
		if err := rows.Scan(&col.Name, &col.Type, &col.Nullable, &def, &col.Comment); err != nil {
			return nil, err
		}
		col.PrimaryKey = len(pk) > 0 && containsName(pk[0], col.Name)
		if def.Valid {
			col.Default = &def.String
			col.DefaultKind, col.DefaultText = mssqlDefault(def.String) // 'text', 12, (expr)
			col.AutoIncrement = strings.HasPrefix(def.String, "nextval(")
		}
		if strings.HasPrefix(col.Type, "ENUM(") {
			for _, m := range enumValue.FindAllStringSubmatch(col.Type, -1) {
				col.Values = append(col.Values, strings.ReplaceAll(m[1], "''", "'"))
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

func containsName(list []string, n string) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

// duckdbConstraintColumns returns the column lists of a table's PRIMARY
// KEY or UNIQUE constraints, with their names.
func duckdbConstraintColumns(ctx context.Context, c *Conn, t TableRef, kind string) ([][]string, []string, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT constraint_index, constraint_name, unnest(constraint_column_names)
		FROM duckdb_constraints() WHERE `+duckdbTable+` AND constraint_type = ? ORDER BY constraint_index`, t.Schema, t.Name, kind)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out [][]string
	var names []string
	last := int64(-1)
	for rows.Next() {
		var i int64
		var name, col string
		if err := rows.Scan(&i, &name, &col); err != nil {
			return nil, nil, err
		}
		if i != last {
			out = append(out, nil)
			names = append(names, name)
			last = i
		}
		out[len(out)-1] = append(out[len(out)-1], col)
	}
	return out, names, rows.Err()
}

func (duckdbEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	var out []Index
	for _, kind := range []string{"PRIMARY KEY", "UNIQUE"} {
		lists, names, err := duckdbConstraintColumns(ctx, c, t, kind)
		if err != nil {
			return nil, err
		}
		for i, cols := range lists {
			out = append(out, Index{Name: names[i], Columns: cols, Unique: true, Primary: kind == "PRIMARY KEY", Type: "art", Constraint: true})
		}
	}
	// CREATE INDEX: the columns are a list of expressions, e.g. ['"name"'].
	rows, err := c.conn.QueryContext(ctx, `SELECT index_name, is_unique, CAST(expressions AS VARCHAR)
		FROM duckdb_indexes() WHERE `+duckdbTable+` ORDER BY index_name`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		x := Index{Type: "art"}
		var exprs string
		if err := rows.Scan(&x.Name, &x.Unique, &exprs); err != nil {
			return nil, err
		}
		x.Columns = duckdbExpressions(exprs)
		out = append(out, x)
	}
	return out, rows.Err()
}

// duckdbExpressions reads ["name", "lower(a)"] or ['"a b"'] into names,
// unquoting plain column references.
func duckdbExpressions(s string) []string {
	s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "["), "]")
	var out []string
	for _, p := range splitTopLevel(s) {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && p[0] == '\'' && p[len(p)-1] == '\'' {
			p = strings.ReplaceAll(p[1:len(p)-1], "''", "'")
		}
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			p = strings.ReplaceAll(p[1:len(p)-1], `""`, `"`)
		}
		out = append(out, p)
	}
	return out
}

func (duckdbEngine) foreignKeys(ctx context.Context, c *Conn, t TableRef) ([]ForeignRef, error) {
	// DuckDB only enforces NO ACTION; the referenced table is in the same schema.
	return scanForeignKeys(c.conn.QueryContext(ctx, `SELECT COALESCE(constraint_name, 'fk_' || constraint_index), unnest(constraint_column_names),
			schema_name, referenced_table, unnest(referenced_column_names), 'NO ACTION', 'NO ACTION'
		FROM duckdb_constraints() WHERE `+duckdbTable+` AND constraint_type = 'FOREIGN KEY'
		ORDER BY constraint_index`, t.Schema, t.Name))
}

func (duckdbEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	var ti TableInfo
	err := c.conn.QueryRowContext(ctx, "SELECT COALESCE(comment, '') FROM duckdb_tables() WHERE "+duckdbTable, t.Schema, t.Name).Scan(&ti.Comment)
	if err != nil && err != sql.ErrNoRows {
		return ti, err
	}
	rows, err := c.conn.QueryContext(ctx, `SELECT COALESCE(constraint_name, 'check_' || constraint_index), expression
		FROM duckdb_constraints() WHERE `+duckdbTable+` AND constraint_type = 'CHECK' ORDER BY constraint_index`, t.Schema, t.Name)
	if err != nil {
		return ti, err
	}
	defer rows.Close()
	for rows.Next() {
		var ck Check
		if err := rows.Scan(&ck.Name, &ck.Expression); err != nil {
			return ti, err
		}
		ti.Checks = append(ti.Checks, ck)
	}
	return ti, rows.Err()
}

func (duckdbEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	q := "SELECT sql FROM duckdb_tables() WHERE " + duckdbTable +
		" UNION ALL SELECT sql FROM duckdb_indexes() WHERE " + duckdbTable + " AND sql IS NOT NULL"
	args := []any{o.Schema, o.Name, o.Schema, o.Name}
	if o.Kind == KindView {
		q = "SELECT sql FROM duckdb_views() WHERE database_name = current_database() AND schema_name = ? AND view_name = ?"
		args = args[:2]
	}
	defs, err := c.queryStrings(ctx, q, args...)
	if err != nil {
		return "", err
	}
	if len(defs) == 0 {
		return "", fmt.Errorf("no definition for %s", o.Name)
	}
	for i := range defs {
		defs[i] = strings.TrimRight(strings.TrimSpace(defs[i]), ";")
	}
	return strings.Join(defs, ";\n\n") + ";", nil
}
