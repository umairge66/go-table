package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

type sqliteEngine struct{}

func openSQLite(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	return sql.Open("sqlite", "file:"+path+"?"+q.Encode())
}

func (sqliteEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var v string
	err := c.conn.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&v)
	return "SQLite " + v, err
}

func (sqliteEngine) databases(ctx context.Context, c *Conn) ([]string, error) {
	return []string{c.Config.Path}, nil
}

func (sqliteEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	return c.queryStrings(ctx, "SELECT name FROM pragma_database_list ORDER BY seq")
}

// currentSchema: SQLite has no session schema; GoTable tracks the selected
// attached database itself.
func (sqliteEngine) currentSchema(_ context.Context, c *Conn) (string, error) {
	if c.schema != "" {
		return c.schema, nil
	}
	return "main", nil
}

func (sqliteEngine) useSchema(ctx context.Context, c *Conn, schema string) error {
	var n int
	err := c.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_database_list WHERE name = ?", schema).Scan(&n)
	if err == nil && n == 0 {
		err = fmt.Errorf("no attached database %q", schema)
	}
	return err
}

func (sqliteEngine) master(c *Conn, schema string) string {
	if schema == "temp" {
		return "sqlite_temp_master"
	}
	return c.Dialect.QuoteIdent(schemaOrMain(schema)) + ".sqlite_master"
}

func schemaOrMain(s string) string {
	if s == "" {
		return "main"
	}
	return s
}

func (e sqliteEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	rows, err := c.conn.QueryContext(ctx, "SELECT name, type FROM "+e.master(c, schema)+
		" WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		k := KindTable
		if typ == "view" {
			k = KindView
		}
		out = append(out, Object{Schema: schemaOrMain(schema), Name: name, Kind: k})
	}
	return out, rows.Err()
}

func (sqliteEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT name, type, "notnull", dflt_value, pk
		FROM pragma_table_info(?, ?) ORDER BY cid`, t.Name, schemaOrMain(t.Schema))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		var notNull, pk int
		var def sql.NullString
		if err := rows.Scan(&col.Name, &col.Type, &notNull, &def, &pk); err != nil {
			return nil, err
		}
		col.Nullable = notNull == 0
		col.PrimaryKey = pk > 0
		if def.Valid {
			col.Default = &def.String
			col.DefaultKind, col.DefaultText = sqliteDefault(def.String)
		}
		out = append(out, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("table %s not found", c.Dialect.QualifiedName(t))
	}
	// A lone INTEGER PRIMARY KEY is the rowid: it numbers new rows.
	pks := 0
	for _, col := range out {
		if col.PrimaryKey {
			pks++
		}
	}
	for i := range out {
		out[i].AutoIncrement = pks == 1 && out[i].PrimaryKey && strings.EqualFold(out[i].Type, "INTEGER")
	}
	return out, nil
}

// sqliteDefault reads dflt_value, which is SQL: 'text', 12, NULL,
// CURRENT_TIMESTAMP or (expression).
func sqliteDefault(def string) (DefaultKind, string) {
	switch {
	case strings.EqualFold(def, "NULL"):
		return DefaultNone, ""
	case len(def) >= 2 && def[0] == '\'' && def[len(def)-1] == '\'':
		return DefaultValue, unquoteSQL(def, false)
	}
	return DefaultExpression, def
}

func (sqliteEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	schema := schemaOrMain(t.Schema)
	rows, err := c.conn.QueryContext(ctx, `SELECT name, "unique", origin FROM pragma_index_list(?, ?) ORDER BY origin = 'pk' DESC, name`, t.Name, schema)
	if err != nil {
		return nil, err
	}
	var out []Index
	for rows.Next() {
		var ix Index
		var unique int
		var origin string
		if err := rows.Scan(&ix.Name, &unique, &origin); err != nil {
			rows.Close()
			return nil, err
		}
		ix.Unique = unique == 1
		ix.Primary = origin == "pk"
		ix.Constraint = origin != "c" // made by PRIMARY KEY or UNIQUE in the table
		ix.Type = "btree"
		out = append(out, ix)
	}
	rows.Close()
	for i := range out {
		cols, err := c.queryStrings(ctx, "SELECT name FROM pragma_index_info(?, ?) ORDER BY seqno", out[i].Name, schema)
		if err != nil {
			return nil, err
		}
		out[i].Columns = cols
	}
	// INTEGER PRIMARY KEY (rowid alias) has no index entry; report it.
	hasPK := false
	for _, ix := range out {
		hasPK = hasPK || ix.Primary
	}
	if !hasPK {
		cols, err := c.queryStrings(ctx, "SELECT name FROM pragma_table_info(?, ?) WHERE pk > 0 ORDER BY pk", t.Name, schema)
		if err != nil {
			return nil, err
		}
		if len(cols) > 0 {
			out = append([]Index{{Name: "PRIMARY", Columns: cols, Unique: true, Primary: true, Type: "btree", Constraint: true}}, out...)
		}
	}
	return out, nil
}

func (e sqliteEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	defs, err := c.queryStrings(ctx, "SELECT sql FROM "+e.master(c, o.Schema)+
		" WHERE tbl_name = ? AND sql IS NOT NULL ORDER BY type = 'table' DESC, type = 'view' DESC, name", o.Name)
	if err != nil {
		return "", err
	}
	if len(defs) == 0 {
		return "", fmt.Errorf("no definition for %s", o.Name)
	}
	return strings.Join(defs, ";\n\n") + ";", nil
}

func (sqliteEngine) foreignKeys(ctx context.Context, c *Conn, t TableRef) ([]ForeignRef, error) {
	schema := schemaOrMain(t.Schema)
	// A NULL "to" means the parent's primary key, column by column.
	return scanForeignKeys(c.conn.QueryContext(ctx, `SELECT CAST(f.id AS TEXT), f."from", ?, f."table",
			COALESCE(f."to", (SELECT p.name FROM pragma_table_info(f."table", ?) p WHERE p.pk = f.seq + 1), ''),
			f.on_delete, f.on_update
		FROM pragma_foreign_key_list(?, ?) f
		ORDER BY f.id, f.seq`, schema, schema, t.Name, schema))
}

// SQLite has no table comments or options, and keeps CHECK constraints
// only in the CREATE TABLE text.
func (sqliteEngine) tableInfo(context.Context, *Conn, TableRef) (TableInfo, error) {
	return TableInfo{}, nil
}
