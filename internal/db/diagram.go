package db

import (
	"context"

	"gotable/internal/model"
)

// DiagramTable is a table as the ER diagram draws it.
type DiagramTable struct {
	Name        string
	Columns     []Column // name, type, nullable and primary key only
	ForeignKeys []ForeignRef
}

// Diagram returns a schema's tables with their columns and foreign keys.
// PostgreSQL and MySQL answer for all tables at once; other engines are
// asked table by table.
func (c *Conn) Diagram(ctx context.Context, schema string) ([]DiagramTable, error) {
	if c.mongo != nil || c.redis != nil || c.cass != nil || c.dyn != nil || c.remote != nil {
		return nil, errNoDiagram
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	var cols, fks string
	switch c.Dialect.family {
	case model.DriverPostgres:
		cols, fks = pgDiagramColumns, pgDiagramKeys
	case model.DriverMySQL:
		cols, fks = mysqlDiagramColumns, mysqlDiagramKeys
	}
	if cols != "" {
		if out, err := c.diagramBulk(ctx, schema, cols, fks); err == nil {
			return out, nil
		} else if ctx.Err() != nil {
			return nil, err
		} // a server without some of it (CockroachDB, CrateDB…): table by table
	}
	objs, err := c.eng.objects(ctx, c, schema)
	if err != nil {
		return nil, err
	}
	out := []DiagramTable{}
	for _, o := range objs {
		if o.Kind != KindTable {
			continue
		}
		t := TableRef{Schema: o.Schema, Name: o.Name}
		d := DiagramTable{Name: o.Name}
		if d.Columns, err = c.eng.columns(ctx, c, t); err != nil {
			return nil, err
		}
		d.ForeignKeys, _ = c.eng.foreignKeys(ctx, c, t) // an extra: missing privileges don't hide the table
		out = append(out, d)
	}
	return out, nil
}

func (c *Conn) diagramBulk(ctx context.Context, schema, colsSQL, fksSQL string) ([]DiagramTable, error) {
	out := []DiagramTable{}
	at := map[string]int{}
	rows, err := c.conn.QueryContext(ctx, colsSQL, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var col Column
		if err := rows.Scan(&table, &col.Name, &col.Type, &col.Nullable, &col.PrimaryKey); err != nil {
			return nil, err
		}
		i, ok := at[table]
		if !ok {
			i = len(out)
			at[table] = i
			out = append(out, DiagramTable{Name: table})
		}
		out[i].Columns = append(out[i].Columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if rows, err = c.conn.QueryContext(ctx, fksSQL, schema); err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var table, from, to string
		var f ForeignRef
		if err := rows.Scan(&table, &f.Name, &from, &f.Schema, &f.Table, &to, &f.OnDelete, &f.OnUpdate); err != nil {
			return nil, err
		}
		i, ok := at[table]
		if !ok {
			continue
		}
		fks := out[i].ForeignKeys
		if n := len(fks); n > 0 && fks[n-1].Name == f.Name {
			fks[n-1].From = append(fks[n-1].From, from)
			fks[n-1].Columns = append(fks[n-1].Columns, to)
			continue
		}
		f.From, f.Columns = []string{from}, []string{to}
		out[i].ForeignKeys = append(fks, f)
	}
	return out, rows.Err()
}

const pgDiagramColumns = `SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
		EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.oid AND i.indisprimary AND a.attnum = ANY (i.indkey))
	FROM pg_class c
	JOIN pg_namespace n ON n.oid = c.relnamespace
	JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
	WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'f') AND NOT coalesce(c.relispartition, false)
	ORDER BY c.relname, a.attnum`

var pgDiagramKeys = `SELECT cl.relname, con.conname, a.attname, nr.nspname, cr.relname, af.attname,
		` + pgAction("con.confdeltype") + `, ` + pgAction("con.confupdtype") + `
	FROM pg_constraint con
	JOIN pg_class cl ON cl.oid = con.conrelid
	JOIN pg_namespace n ON n.oid = cl.relnamespace
	JOIN pg_class cr ON cr.oid = con.confrelid
	JOIN pg_namespace nr ON nr.oid = cr.relnamespace
	CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS k(col, fcol, ord)
	JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.col
	JOIN pg_attribute af ON af.attrelid = con.confrelid AND af.attnum = k.fcol
	WHERE con.contype = 'f' AND n.nspname = $1 AND coalesce(con.conparentid, 0) = 0
	ORDER BY cl.relname, con.conname, k.ord`

const mysqlDiagramColumns = `SELECT c.TABLE_NAME, c.COLUMN_NAME, c.COLUMN_TYPE, c.IS_NULLABLE = 'YES', c.COLUMN_KEY = 'PRI'
	FROM information_schema.COLUMNS c
	JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME AND t.TABLE_TYPE = 'BASE TABLE'
	WHERE c.TABLE_SCHEMA = ? ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION`

const mysqlDiagramKeys = `SELECT k.TABLE_NAME, k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
		r.DELETE_RULE, r.UPDATE_RULE
	FROM information_schema.KEY_COLUMN_USAGE k
	JOIN information_schema.REFERENTIAL_CONSTRAINTS r
		ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.TABLE_NAME = k.TABLE_NAME
	WHERE k.TABLE_SCHEMA = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
	ORDER BY k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`
