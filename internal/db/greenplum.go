package db

// Greenplum is PostgreSQL spread over segments: each table's rows are
// distributed by a key (a unique index must contain it), and foreign keys
// are kept but not enforced. Version 6 is PostgreSQL 9.4, without identity
// columns and the pg_sequences view; 7 is PostgreSQL 12. The PostgreSQL
// engine does the rest.

import (
	"context"
	"regexp"
	"strings"

	"gotable/internal/model"
)

type greenplumEngine struct{ postgresEngine }

var greenplumVersion = regexp.MustCompile(`^PostgreSQL (\S+) \(Greenplum Database (\d[\d.]*)`)

func (greenplumEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var v string
	err := c.conn.QueryRowContext(ctx, "SELECT version()").Scan(&v) // PostgreSQL 12.12 (Greenplum Database 7.0.0 build …) on …
	if m := greenplumVersion.FindStringSubmatch(v); m != nil {
		return "Greenplum " + strings.TrimRight(m[2], ".") + " · PostgreSQL " + m[1], err
	}
	return v, err
}

// schemas leaves out Greenplum's own ones too.
func (e greenplumEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	all, err := e.postgresEngine.schemas(ctx, c)
	out := []string{}
	for _, s := range all {
		switch s {
		case "gp_toolkit", "pg_aoseg", "pg_bitmapindex":
		default:
			out = append(out, s)
		}
	}
	return out, err
}

func (greenplumEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	identity := "a.attidentity <> ''"
	if greenplumOld(ctx, c) {
		identity = "false"
	}
	return pgColumns(ctx, c, t, identity)
}

func (e greenplumEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	return pgDefinition(ctx, c, o, e.columns)
}

// tableInfo adds how the rows are spread.
func (e greenplumEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	ti, err := e.postgresEngine.tableInfo(ctx, c, t)
	ti.Options = strings.ToLower(strings.TrimSpace(greenplumDistribution(ctx, c, t)))
	return ti, err
}

// greenplumDistribution is a table's DISTRIBUTED clause, with a newline in
// front; "" elsewhere. Callers hold c.mu.
func greenplumDistribution(ctx context.Context, c *Conn, t TableRef) string {
	if c.Config.Driver != model.DriverGreenplum {
		return ""
	}
	var s string
	c.conn.QueryRowContext(ctx, `SELECT pg_get_table_distributedby(cl.oid) FROM pg_class cl
		JOIN pg_namespace n ON n.oid = cl.relnamespace WHERE n.nspname = $1 AND cl.relname = $2`, t.Schema, t.Name).Scan(&s)
	if s == "" { // a partition, an external table
		return ""
	}
	return "\n" + s
}

// greenplumOld: Greenplum 6, on PostgreSQL 9.4. Callers hold c.mu.
func greenplumOld(ctx context.Context, c *Conn) bool {
	if c.Config.Driver != model.DriverGreenplum {
		return false
	}
	var n int
	err := c.conn.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int").Scan(&n)
	return err == nil && n < 100000
}

// greenplumSequences: pg_sequences for Greenplum 6.
func greenplumSequences(ctx context.Context, c *Conn, schema string) ([]pgSequence, error) {
	names, err := c.queryStrings(ctx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind = 'S' ORDER BY 1`, schema)
	if err != nil {
		return nil, err
	}
	out := []pgSequence{}
	for _, n := range names {
		s := pgSequence{name: n, guard: true}
		var last int64
		var called bool
		err := c.conn.QueryRowContext(ctx, "SELECT last_value, is_called FROM "+c.Dialect.QualifiedName(TableRef{Schema: schema, Name: n})).Scan(&last, &called)
		if err != nil {
			return nil, err
		}
		if called {
			s.last = &last
		}
		out = append(out, s)
	}
	return out, nil
}

// greenplumSerial: the serial type of an integer type.
func greenplumSerial(typ string) string {
	switch strings.ToLower(typ) {
	case "smallint", "int2":
		return "smallserial"
	case "bigint", "int8":
		return "bigserial"
	}
	return "serial"
}
