package db

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// SQLite can't change a column's type, NULL or default, its primary key or
// foreign keys in place. Such changes rebuild the table as the SQLite
// documentation describes: create the new table, copy the rows, drop the
// old one, rename the new one and recreate its indexes and triggers.

// SQLiteTable is what a SQLite table rebuild needs besides the columns.
type SQLiteTable struct {
	SQL      string            // CREATE TABLE
	IndexSQL map[string]string // CREATE INDEX of explicit indexes, by name
	Triggers []string
	// AutoIncrement: the key uses AUTOINCREMENT (never reuses rowids).
	AutoIncrement bool
	// Options: " WITHOUT ROWID", " STRICT" or both.
	Options string
	// Hidden: the table has generated columns.
	Hidden bool
}

var (
	sqliteAutoinc  = regexp.MustCompile(`(?i)\bAUTOINCREMENT\b`)
	sqliteOptions  = regexp.MustCompile(`(?is)\)\s*((?:WITHOUT\s+ROWID|STRICT)(?:\s*,\s*(?:WITHOUT\s+ROWID|STRICT))*)\s*;?\s*$`)
	sqliteNoCopy   = regexp.MustCompile(`(?i)\bCHECK\s*\(|\bCOLLATE\b|\bGENERATED\b|\bAS\s*\(`)
	sqliteBareExpr = regexp.MustCompile(`(?i)^([+-]?\d+(\.\d+)?|'([^']|'')*'|null|true|false|current_(timestamp|date|time)|\(.*\))$`)
)

func loadSQLiteTable(ctx context.Context, c *Conn, t TableRef) (*SQLiteTable, error) {
	sq := &SQLiteTable{IndexSQL: map[string]string{}}
	rows, err := c.conn.QueryContext(ctx, "SELECT type, name, sql FROM "+sqliteEngine{}.master(c, t.Schema)+
		" WHERE tbl_name = ? AND sql IS NOT NULL ORDER BY rowid", t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name, sql string
		if err := rows.Scan(&typ, &name, &sql); err != nil {
			return nil, err
		}
		switch typ {
		case "table":
			sq.SQL = sql
		case "index":
			sq.IndexSQL[name] = sql
		case "trigger":
			sq.Triggers = append(sq.Triggers, sql)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sq.AutoIncrement = sqliteAutoinc.MatchString(sq.SQL)
	if m := sqliteOptions.FindStringSubmatch(sq.SQL); m != nil {
		sq.Options = " " + strings.Join(strings.Fields(strings.ToUpper(m[1])), " ")
	}
	var hidden int
	if err := c.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_xinfo(?, ?) WHERE hidden <> 0", t.Name, schemaOrMain(t.Schema)).Scan(&hidden); err == nil {
		sq.Hidden = hidden > 0
	}
	return sq, nil
}

// sqliteNeedsRebuild reports whether op can't run as ALTER TABLE on SQLite.
func sqliteNeedsRebuild(st TableState, op AlterOp) bool {
	switch op.Kind {
	case AlterModifyColumn:
		old, ok := findColumn(st, op.Column)
		if !ok || op.Def == nil {
			return false // reported by buildOp
		}
		def := op.Def
		return !sameType(def.Type, old.Type) || def.Nullable != old.Nullable || def.DefaultKind != old.DefaultKind ||
			(def.DefaultKind != DefaultNone && def.Default != old.DefaultText)
	case AlterDropColumn:
		// DROP COLUMN refuses columns in keys and indexes.
		for _, x := range st.Indexes {
			if slices.Contains(x.Columns, op.Column) {
				return true
			}
		}
		for _, fk := range st.ForeignKeys {
			if slices.Contains(fk.From, op.Column) {
				return true
			}
		}
	case AlterSetPrimaryKey, AlterAddForeignKey, AlterDropForeignKey:
		return true
	case AlterDropIndex:
		x, ok := findIndex(st, op.Name)
		return ok && x.Constraint
	}
	return false
}

type rebuildColumn struct {
	from    string // the column it is copied from; "" for a new one
	name    string
	typ     string
	notNull bool
	def     string // DEFAULT as SQL; "" for none
}

// defaultSQL renders a ColumnDef default for SQLite.
func defaultSQL(d Dialect, c ColumnDef) string {
	switch c.DefaultKind {
	case DefaultValue:
		return d.QuoteLiteral(Text(c.Default))
	case DefaultExpression:
		e := strings.TrimSpace(c.Default)
		if !sqliteBareExpr.MatchString(e) {
			e = "(" + e + ")"
		}
		return e
	case DefaultNull:
		return "NULL"
	}
	return ""
}

func buildSQLiteRebuild(d Dialect, t TableRef, st TableState, ops []AlterOp) (AlterPlan, error) {
	sq := st.SQLite
	if sq == nil || sq.SQL == "" {
		return AlterPlan{}, fmt.Errorf("can't read the definition of %s", t.Name)
	}
	if sq.Hidden || sqliteNoCopy.MatchString(sq.SQL) {
		return AlterPlan{}, errors.New("this change rebuilds the SQLite table, but its definition has CHECK constraints, collations or generated columns that GoTable can't carry over; make the change in SQL instead")
	}
	var cols []rebuildColumn
	for _, c := range st.Columns {
		rc := rebuildColumn{from: c.Name, name: c.Name, typ: c.Type, notNull: !c.Nullable}
		if c.Default != nil && c.DefaultKind != DefaultNone {
			rc.def = *c.Default
		}
		cols = append(cols, rc)
	}
	var pk []string
	if x, ok := primaryIndex(st); ok {
		pk = slices.Clone(x.Columns)
	}
	autoinc := sq.AutoIncrement
	var uniques [][]string
	explicit := map[string][]string{} // index name → columns
	for _, x := range st.Indexes {
		switch {
		case x.Primary:
		case x.Constraint:
			uniques = append(uniques, slices.Clone(x.Columns))
		default:
			explicit[x.Name] = slices.Clone(x.Columns)
		}
	}
	fks := slices.Clone(st.ForeignKeys)

	find := func(name string) int {
		return slices.IndexFunc(cols, func(c rebuildColumn) bool { return c.name == name })
	}
	rename := func(list []string, from, to string) {
		for i := range list {
			if list[i] == from {
				list[i] = to
			}
		}
	}
	for _, op := range ops {
		switch op.Kind {
		case AlterModifyColumn:
			i := find(op.Column)
			if i < 0 || op.Def == nil {
				return AlterPlan{}, fmt.Errorf("there is no column %q", op.Column)
			}
			def := *op.Def
			def.Name = strings.TrimSpace(def.Name)
			if err := def.check(d, true); err != nil {
				return AlterPlan{}, err
			}
			if strings.TrimSpace(def.Comment) != "" {
				return AlterPlan{}, errors.New("SQLite doesn't support column comments")
			}
			if def.Collation != "" {
				return AlterPlan{}, errors.New("GoTable can't set a collation on an existing SQLite column")
			}
			if def.Name != op.Column {
				if find(def.Name) >= 0 {
					return AlterPlan{}, fmt.Errorf("column %q already exists", def.Name)
				}
				rename(pk, op.Column, def.Name)
				for _, u := range uniques {
					rename(u, op.Column, def.Name)
				}
				for k := range explicit {
					rename(explicit[k], op.Column, def.Name)
				}
				for k := range fks {
					fks[k].From = slices.Clone(fks[k].From)
					rename(fks[k].From, op.Column, def.Name)
				}
			}
			cols[i] = rebuildColumn{from: cols[i].from, name: def.Name, typ: strings.TrimSpace(def.Type), notNull: !def.Nullable, def: defaultSQL(d, def)}
		case AlterAddColumn:
			if op.Def == nil {
				return AlterPlan{}, errors.New("missing column definition")
			}
			def := *op.Def
			def.Name = strings.TrimSpace(def.Name)
			if err := def.check(d, true); err != nil {
				return AlterPlan{}, err
			}
			if find(def.Name) >= 0 {
				return AlterPlan{}, fmt.Errorf("column %q already exists", def.Name)
			}
			cols = append(cols, rebuildColumn{name: def.Name, typ: strings.TrimSpace(def.Type), notNull: !def.Nullable, def: defaultSQL(d, def)})
		case AlterDropColumn:
			i := find(op.Column)
			if i < 0 {
				return AlterPlan{}, fmt.Errorf("there is no column %q", op.Column)
			}
			cols = slices.Delete(cols, i, i+1)
			pk = slices.DeleteFunc(pk, func(c string) bool { return c == op.Column })
			uniques = slices.DeleteFunc(uniques, func(u []string) bool { return slices.Contains(u, op.Column) })
			for k, v := range explicit {
				if slices.Contains(v, op.Column) {
					delete(explicit, k)
				}
			}
			fks = slices.DeleteFunc(fks, func(f ForeignRef) bool { return slices.Contains(f.From, op.Column) })
		case AlterSetPrimaryKey:
			for _, c := range op.Columns {
				if find(c) < 0 {
					return AlterPlan{}, fmt.Errorf("there is no column %q", c)
				}
			}
			autoinc = autoinc && slices.Equal(pk, op.Columns)
			pk = slices.Clone(op.Columns)
		case AlterAddForeignKey:
			fk := op.ForeignKey
			if fk == nil {
				return AlterPlan{}, errors.New("missing foreign key")
			}
			for _, c := range fk.Columns {
				if find(c) < 0 {
					return AlterPlan{}, fmt.Errorf("there is no column %q", c)
				}
			}
			if len(fk.Columns) == 0 {
				return AlterPlan{}, errors.New("choose the foreign key columns")
			}
			if _, err := foreignKeyClause(d, t, *fk); err != nil {
				return AlterPlan{}, err
			}
			fks = append(fks, ForeignRef{Name: "new", Table: fk.RefTable, From: fk.Columns, Columns: fk.RefColumns,
				OnDelete: strings.ToUpper(fk.OnDelete), OnUpdate: strings.ToUpper(fk.OnUpdate)})
		case AlterDropForeignKey:
			n := len(fks)
			fks = slices.DeleteFunc(fks, func(f ForeignRef) bool { return f.Name == op.Name })
			if len(fks) == n {
				return AlterPlan{}, fmt.Errorf("there is no foreign key %q", op.Name)
			}
		case AlterDropIndex:
			x, ok := findIndex(st, op.Name)
			switch {
			case !ok:
				return AlterPlan{}, fmt.Errorf("there is no index %q", op.Name)
			case x.Primary:
				pk, autoinc = nil, false
			case x.Constraint:
				uniques = slices.DeleteFunc(uniques, func(u []string) bool { return slices.Equal(u, x.Columns) })
			default:
				delete(explicit, x.Name)
			}
		default:
			return AlterPlan{}, errors.New("on SQLite, make this change on its own")
		}
	}
	if len(cols) == 0 {
		return AlterPlan{}, errors.New("a table needs at least one column; drop the table instead")
	}
	if len(pk) != 1 {
		autoinc = false
	}

	var defs []string
	for _, c := range cols {
		s := d.QuoteIdent(c.name)
		if c.typ != "" {
			s += " " + c.typ
		}
		if autoinc && pk[0] == c.name {
			s += " PRIMARY KEY AUTOINCREMENT" // only allowed inline
		}
		if c.notNull {
			s += " NOT NULL"
		}
		if c.def != "" {
			s += " DEFAULT " + c.def
		}
		defs = append(defs, s)
	}
	if len(pk) > 0 && !autoinc {
		defs = append(defs, "PRIMARY KEY ("+quoteList(d, pk)+")")
	}
	for _, u := range uniques {
		defs = append(defs, "UNIQUE ("+quoteList(d, u)+")")
	}
	for _, fk := range fks {
		s := "FOREIGN KEY (" + quoteList(d, fk.From) + ") REFERENCES " + d.QuoteIdent(fk.Table) + " (" + quoteList(d, fk.Columns) + ")"
		if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
			s += " ON DELETE " + fk.OnDelete
		}
		if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
			s += " ON UPDATE " + fk.OnUpdate
		}
		defs = append(defs, s)
	}

	tmp := TableRef{Schema: t.Schema, Name: t.Name + "_gotable_new"}
	out := []string{"CREATE TABLE " + d.QualifiedName(tmp) + " (\n  " + strings.Join(defs, ",\n  ") + "\n)" + sq.Options + ";"}
	var to, from []string
	for _, c := range cols {
		if c.from != "" {
			to = append(to, c.name)
			from = append(from, c.from)
		}
	}
	if len(to) > 0 {
		out = append(out, "INSERT INTO "+d.QualifiedName(tmp)+" ("+quoteList(d, to)+") SELECT "+quoteList(d, from)+" FROM "+d.QualifiedName(t)+";")
	}
	out = append(out, "DROP TABLE "+d.QualifiedName(t)+";", "ALTER TABLE "+d.QualifiedName(tmp)+" RENAME TO "+d.QuoteIdent(t.Name)+";")
	for _, x := range st.Indexes {
		colsNow, kept := explicit[x.Name]
		if !kept {
			continue
		}
		def := sq.IndexSQL[x.Name]
		if def == "" || !slices.Equal(colsNow, x.Columns) {
			// A renamed column: write the index anew.
			kw := "INDEX "
			if x.Unique {
				kw = "UNIQUE INDEX "
			}
			def = "CREATE " + kw + d.QualifiedName(TableRef{Schema: t.Schema, Name: x.Name}) + " ON " + d.QuoteIdent(t.Name) + " (" + quoteList(d, colsNow) + ")"
		}
		out = append(out, def+";")
	}
	for _, tr := range sq.Triggers {
		out = append(out, tr+";")
	}
	return AlterPlan{Statements: out, Rebuild: true, CheckTable: t.Name}, nil
}

// runRebuild runs a rebuild with foreign key enforcement off (rows of
// other tables keep pointing at the table while it is swapped), then
// checks the keys before committing. Callers hold c.mu.
func (c *Conn) runRebuild(ctx context.Context, t TableRef, plan AlterPlan) error {
	var on int
	if err := c.conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on); err != nil {
		return err
	}
	if on == 1 {
		if _, err := c.conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return err
		}
		defer c.conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON")
	}
	tx, err := c.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, s := range plan.Statements {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s %w", s, err)
		}
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA %s.foreign_key_check(%s)", c.Dialect.QuoteIdent(schemaOrMain(t.Schema)), c.Dialect.QuoteIdent(plan.CheckTable)))
	if err != nil {
		tx.Rollback()
		return err
	}
	bad := 0
	for rows.Next() {
		bad++
	}
	rows.Close()
	if bad > 0 {
		tx.Rollback()
		return fmt.Errorf("the change would leave %d row(s) with a foreign key that points nowhere; nothing was changed", bad)
	}
	return tx.Commit()
}
