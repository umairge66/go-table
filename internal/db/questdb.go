package db

// QuestDB, a time-series database, speaks the PostgreSQL protocol with its
// own SQL: one schema, tables with a designated timestamp and partitions,
// SYMBOL columns that can be indexed, no primary keys (a WAL table's
// deduplication keys are the closest thing), no DELETE, and catalog
// functions (tables(), table_columns(), views()) instead of pg_catalog.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gotable/internal/model"
)

type questEngine struct{ postgresEngine }

var errQuestDelete = errors.New("QuestDB can't delete rows: drop a partition (ALTER TABLE … DROP PARTITION) or the table instead")

func (questEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var b string
	err := c.conn.QueryRowContext(ctx, "SELECT build()").Scan(&b) // Build Information: QuestDB 10.2.0, JDK …
	if _, rest, ok := strings.Cut(b, "QuestDB "); ok {
		v, _, _ := strings.Cut(rest, ",")
		return "QuestDB " + v, err
	}
	return b, err
}

func (questEngine) databases(context.Context, *Conn) ([]string, error) { return []string{}, nil }

func (questEngine) schemas(context.Context, *Conn) ([]string, error) { return []string{"public"}, nil }

func (questEngine) currentSchema(context.Context, *Conn) (string, error) { return "public", nil }

func (questEngine) useSchema(_ context.Context, _ *Conn, schema string) error {
	if schema != "public" {
		return fmt.Errorf("QuestDB has one schema, public")
	}
	return nil
}

func (questEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	views, err := c.queryStrings(ctx, "SELECT view_name FROM views() ORDER BY 1")
	if err != nil {
		views = nil // before views (QuestDB 9)
	}
	isView := map[string]bool{}
	for _, v := range views {
		isView[v] = true
	}
	tables, err := c.queryStrings(ctx, "SELECT table_name FROM tables() ORDER BY 1")
	if err != nil {
		return nil, err
	}
	out := []Object{}
	for _, t := range tables {
		if !isView[t] && !strings.HasPrefix(t, "sys.") && !strings.HasPrefix(t, "_") {
			out = append(out, Object{Schema: schema, Name: t, Kind: KindTable})
		}
	}
	for _, v := range views {
		out = append(out, Object{Schema: schema, Name: v, Kind: KindView})
	}
	return out, nil
}

func (questEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT "column", "type", upsertKey FROM table_columns(`+questLiteral(t.Name)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		if err := rows.Scan(&col.Name, &col.Type, &col.PrimaryKey); err != nil {
			return nil, err
		}
		col.Nullable = true // every QuestDB column takes NULL
		out = append(out, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("table %s not found", t.Name)
	}
	return out, nil
}

// indexes: indexed SYMBOL columns.
func (questEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	names, err := c.queryStrings(ctx, `SELECT "column" FROM table_columns(`+questLiteral(t.Name)+`) WHERE indexed`)
	out := []Index{}
	for _, n := range names {
		out = append(out, Index{Name: n, Columns: []string{n}, Type: "symbol"})
	}
	return out, err
}

func (questEngine) foreignKeys(context.Context, *Conn, TableRef) ([]ForeignRef, error) {
	return nil, nil
}

func (questEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	if o.Kind == KindView {
		var def string
		err := c.conn.QueryRowContext(ctx, "SELECT view_sql FROM views() WHERE view_name = $1", o.Name).Scan(&def)
		return "CREATE VIEW " + c.Dialect.QuoteIdent(o.Name) + " AS\n" + def + ";", err
	}
	var ddl string
	err := c.conn.QueryRowContext(ctx, "SHOW CREATE TABLE "+c.Dialect.QuoteIdent(o.Name)).Scan(&ddl)
	return ddl, err
}

// tableInfo: the designated timestamp, partitioning and write-ahead log.
func (questEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	var ts, part string
	var wal bool
	err := c.conn.QueryRowContext(ctx, `SELECT coalesce(designatedTimestamp, ''), partitionBy, walEnabled FROM tables() WHERE table_name = $1`, t.Name).Scan(&ts, &part, &wal)
	if err != nil {
		return TableInfo{}, nil // a view
	}
	var parts []string
	if ts != "" {
		parts = append(parts, "timestamp "+ts)
	}
	if part != "" && part != "NONE" {
		parts = append(parts, "partitioned by "+strings.ToLower(part))
	}
	if wal {
		parts = append(parts, "WAL")
	}
	return TableInfo{Options: strings.Join(parts, " · ")}, nil
}

// questNoDelete refuses deletes before anything runs.
func questNoDelete(c *Conn, changes []Change) error {
	if c.Config.Driver != model.DriverQuestDB {
		return nil
	}
	for _, ch := range changes {
		if ch.Kind == ChangeDelete {
			return errQuestDelete
		}
	}
	return nil
}

// questLiteral: QuestDB's catalog functions take a string, not a parameter.
func questLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
