package db

// CrateDB speaks the PostgreSQL protocol with its own SQL and catalog: no
// pg_collation or pg_index, no indexes to manage (every column is indexed),
// no foreign keys or transactions, and writes that become visible on the
// next refresh (every second by default). The PostgreSQL engine does the
// rest.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"gotable/internal/model"
)

type crateEngine struct{ postgresEngine }

var crateSystemSchemas = map[string]bool{"blob": true, "information_schema": true, "pg_catalog": true, "sys": true}

func (crateEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var v string
	err := c.conn.QueryRowContext(ctx, "SELECT version()").Scan(&v) // CrateDB 6.5.1 (built …)
	if f := strings.Fields(v); len(f) >= 2 && f[0] == "CrateDB" {
		return "CrateDB " + f[1], err
	}
	return v, err
}

func (crateEngine) databases(context.Context, *Conn) ([]string, error) { return []string{}, nil }

func (crateEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	all, err := c.queryStrings(ctx, "SELECT schema_name FROM information_schema.schemata ORDER BY schema_name")
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, s := range all {
		if !crateSystemSchemas[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (crateEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT table_name, table_type FROM information_schema.tables
		WHERE table_schema = $1 AND table_type IN ('BASE TABLE', 'VIEW') ORDER BY table_name`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Object{}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, err
		}
		k := KindTable
		if kind == "VIEW" {
			k = KindView
		}
		out = append(out, Object{Schema: schema, Name: name, Kind: k})
	}
	return out, rows.Err()
}

func (crateEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	pk, err := cratePrimaryKey(ctx, c, t)
	if err != nil {
		return nil, err
	}
	// Top-level columns only: an object's sub-columns (o['x']) are part of it.
	rows, err := c.conn.QueryContext(ctx, `SELECT column_name, data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name NOT LIKE '%[%'
		ORDER BY ordinal_position`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		var nullable string
		var def sql.NullString
		if err := rows.Scan(&col.Name, &col.Type, &nullable, &def); err != nil {
			return nil, err
		}
		col.Nullable = nullable == "YES"
		col.PrimaryKey = pk[col.Name]
		if def.Valid {
			col.Default = &def.String
			col.DefaultKind, col.DefaultText = DefaultExpression, def.String
			if m := pgLiteral.FindStringSubmatch(def.String); m != nil {
				col.DefaultKind, col.DefaultText = DefaultValue, strings.ReplaceAll(m[1], "''", "'")
			} else if !strings.ContainsAny(def.String, "()") {
				col.DefaultKind, col.DefaultText = DefaultValue, def.String // 0, true
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

func cratePrimaryKey(ctx context.Context, c *Conn, t TableRef) (map[string]bool, error) {
	names, err := c.queryStrings(ctx, `SELECT kcu.column_name FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu ON kcu.constraint_name = tc.constraint_name
		  AND kcu.table_schema = tc.table_schema AND kcu.table_name = tc.table_name
		WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = $1 AND tc.table_name = $2
		ORDER BY kcu.ordinal_position`, t.Schema, t.Name)
	pk := map[string]bool{}
	for _, n := range names {
		pk[n] = true
	}
	return pk, err
}

// indexes: CrateDB indexes every column itself; only the primary key shows.
func (crateEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	names, err := c.queryStrings(ctx, `SELECT kcu.column_name FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu ON kcu.constraint_name = tc.constraint_name
		  AND kcu.table_schema = tc.table_schema AND kcu.table_name = tc.table_name
		WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = $1 AND tc.table_name = $2
		ORDER BY kcu.ordinal_position`, t.Schema, t.Name)
	if err != nil || len(names) == 0 {
		return []Index{}, err
	}
	return []Index{{Name: t.Name + "_pkey", Columns: names, Unique: true, Primary: true, Constraint: true}}, nil
}

func (crateEngine) foreignKeys(context.Context, *Conn, TableRef) ([]ForeignRef, error) {
	return nil, nil
}

// definition: CrateDB writes its own DDL, with shards and table settings.
func (crateEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	name := c.Dialect.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name})
	if o.Kind == KindView {
		var def string
		err := c.conn.QueryRowContext(ctx, `SELECT view_definition FROM information_schema.views
			WHERE table_schema = $1 AND table_name = $2`, o.Schema, o.Name).Scan(&def)
		return "CREATE OR REPLACE VIEW " + name + " AS\n" + def, err
	}
	var ddl string
	err := c.conn.QueryRowContext(ctx, "SHOW CREATE TABLE "+name).Scan(&ddl)
	return ddl + ";", err
}

func (crateEngine) tableInfo(context.Context, *Conn, TableRef) (TableInfo, error) {
	return TableInfo{}, nil
}

// crateRefresh makes a table's new writes visible now, not at the next
// refresh, so the grid reloads what was just saved.
func (c *Conn) crateRefresh(ctx context.Context, t TableRef) {
	if c.Config.Driver == model.DriverCrateDB {
		c.conn.ExecContext(ctx, "REFRESH TABLE "+c.Dialect.QualifiedName(t))
	}
}
