package db

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"
)

// runAlterSuite changes a table's structure every way the Structure tab
// can, against a live database.
func runAlterSuite(t *testing.T, c *Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	kind := c.Config.Driver
	schema := c.Schema()
	parent := TableRef{Schema: schema, Name: "gt_alter"}
	child := TableRef{Schema: schema, Name: "gt_alter_child"}
	exec := func(sql string) Result {
		t.Helper()
		res, err := c.Execute(ctx, sql, 0)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return res[len(res)-1]
	}
	for _, s := range []string{
		"DROP TABLE IF EXISTS gt_alter_child", "DROP TABLE IF EXISTS gt_alter", "DROP TABLE IF EXISTS gt_alter2",
		"CREATE TABLE gt_alter (id INT PRIMARY KEY, name VARCHAR(50), qty INT)",
		"INSERT INTO gt_alter VALUES (1, 'a', 5), (2, 'b', NULL)",
		"CREATE TABLE gt_alter_child (id INT PRIMARY KEY, parent_id INT)",
		"INSERT INTO gt_alter_child VALUES (1, 1), (2, 2)",
	} {
		if r := exec(s); r.Err != nil {
			t.Fatalf("%s: %v", s, r.Err)
		}
	}
	apply := func(tbl TableRef, ops ...AlterOp) []string {
		t.Helper()
		stmts, err := c.Alter(ctx, tbl, ops, false)
		if err != nil {
			t.Fatalf("%v: %v", ops[0].Kind, err)
		}
		return stmts
	}
	state := func(tbl TableRef) TableState {
		t.Helper()
		if err := c.lock(ctx); err != nil {
			t.Fatal(err)
		}
		defer c.mu.Unlock()
		st, err := c.loadTableState(ctx, tbl)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	col := func(tbl TableRef, name string) Column {
		t.Helper()
		cl, ok := findColumn(state(tbl), name)
		if !ok {
			t.Fatalf("no column %s", name)
		}
		return cl
	}
	value := func(sql string) string {
		t.Helper()
		r := exec(sql)
		if r.Err != nil || r.Set == nil || len(r.Set.Rows) == 0 {
			t.Fatalf("%s: %v", sql, r.Err)
		}
		return r.Set.Rows[0][0].String()
	}
	intType := "INT"

	t.Run("columns", func(t *testing.T) {
		apply(parent, AlterOp{Kind: AlterAddColumn, Def: &ColumnDef{Name: "note", Type: "VARCHAR(20)", Nullable: true, DefaultKind: DefaultValue, Default: "it's"}})
		if n := col(parent, "note"); n.DefaultKind != DefaultValue || n.DefaultText != "it's" {
			t.Errorf("note default %q %q", n.DefaultKind, n.DefaultText)
		}
		// Rename, keep the type, add a default.
		apply(parent, AlterOp{Kind: AlterModifyColumn, Column: "qty", Def: &ColumnDef{Name: "quantity", Type: intType, Nullable: true, DefaultKind: DefaultValue, Default: "0"}})
		if q := col(parent, "quantity"); q.DefaultText != "0" {
			t.Errorf("quantity default %q %q", q.DefaultKind, q.DefaultText)
		}
		if v := value("SELECT quantity FROM gt_alter WHERE id = 1"); v != "5" {
			t.Errorf("data after rename: %q", v)
		}
		// Wider type, NOT NULL.
		apply(parent, AlterOp{Kind: AlterModifyColumn, Column: "name", Def: &ColumnDef{Name: "name", Type: "VARCHAR(100)", Nullable: false}})
		if n := col(parent, "name"); n.Nullable || !strings.Contains(strings.ToLower(n.Type), "100") {
			t.Errorf("name after modify: %+v", n)
		}
		if v := value("SELECT name FROM gt_alter WHERE id = 2"); v != "b" {
			t.Errorf("data after modify: %q", v)
		}
		if kind != model.DriverSQLite {
			apply(parent, AlterOp{Kind: AlterAddColumn, Def: &ColumnDef{Name: "made", Type: "TIMESTAMP", Nullable: true, DefaultKind: DefaultExpression, Default: "CURRENT_TIMESTAMP"}})
			if m := col(parent, "made"); m.DefaultKind != DefaultExpression {
				t.Errorf("made default %q %q", m.DefaultKind, m.DefaultText)
			}
			apply(parent, AlterOp{Kind: AlterDropColumn, Column: "made"})
		}
	})

	t.Run("indexes", func(t *testing.T) {
		apply(parent, AlterOp{Kind: AlterAddIndex, Index: &IndexDef{Name: "gt_alter_name", Columns: []string{"name"}, Unique: true}})
		apply(parent, AlterOp{Kind: AlterRenameIndex, Name: "gt_alter_name", NewName: "gt_alter_name_uq"})
		x, ok := findIndex(state(parent), "gt_alter_name_uq")
		if !ok || !x.Unique || !slices.Equal(x.Columns, []string{"name"}) {
			t.Fatalf("renamed index %+v %v", x, ok)
		}
		apply(parent, AlterOp{Kind: AlterDropIndex, Name: "gt_alter_name_uq"})
		if _, ok := findIndex(state(parent), "gt_alter_name_uq"); ok {
			t.Error("index still there")
		}
		// Composite primary key and back.
		apply(parent, AlterOp{Kind: AlterSetPrimaryKey, Columns: []string{"id", "name"}})
		if pk, _ := primaryIndex(state(parent)); !slices.Equal(pk.Columns, []string{"id", "name"}) {
			t.Errorf("primary key %v", pk.Columns)
		}
		apply(parent, AlterOp{Kind: AlterSetPrimaryKey, Columns: []string{"id"}})
		if pk, _ := primaryIndex(state(parent)); !slices.Equal(pk.Columns, []string{"id"}) {
			t.Errorf("primary key %v", pk.Columns)
		}
	})

	t.Run("foreign keys", func(t *testing.T) {
		apply(child, AlterOp{Kind: AlterAddForeignKey, ForeignKey: &ForeignKeyDef{Name: "gt_alter_child_parent", Columns: []string{"parent_id"},
			RefTable: "gt_alter", RefColumns: []string{"id"}, OnDelete: "CASCADE"}})
		fks := state(child).ForeignKeys
		if len(fks) != 1 || fks[0].Table != "gt_alter" || fks[0].OnDelete != "CASCADE" {
			t.Fatalf("foreign keys %+v", fks)
		}
		if r := exec("DELETE FROM gt_alter WHERE id = 1"); r.Err != nil {
			t.Fatal(r.Err)
		}
		if v := value("SELECT COUNT(*) FROM gt_alter_child"); v != "1" {
			t.Errorf("cascade left %s child rows", v)
		}
		apply(child, AlterOp{Kind: AlterDropForeignKey, Name: fks[0].Name})
		if fks := state(child).ForeignKeys; len(fks) != 0 {
			t.Errorf("foreign key still there: %+v", fks)
		}
	})

	if kind != model.DriverSQLite {
		t.Run("checks and comment", func(t *testing.T) {
			apply(parent, AlterOp{Kind: AlterAddCheck, Check: &CheckDef{Name: "gt_alter_quantity_ck", Expression: "quantity >= 0"}})
			if ck := state(parent).Info.Checks; len(ck) != 1 || ck[0].Name != "gt_alter_quantity_ck" {
				t.Fatalf("checks %+v", ck)
			}
			if r := exec("INSERT INTO gt_alter (id, name, quantity) VALUES (9, 'neg', -1)"); r.Err == nil {
				t.Error("the CHECK constraint let -1 in")
			}
			apply(parent, AlterOp{Kind: AlterDropCheck, Name: "gt_alter_quantity_ck"})
			apply(parent, AlterOp{Kind: AlterTableComment, Comment: "Parents"})
			if cm := state(parent).Info.Comment; cm != "Parents" {
				t.Errorf("comment %q", cm)
			}
		})
	}

	if kind == model.DriverMySQL || kind == model.DriverMariaDB {
		t.Run("table options", func(t *testing.T) {
			apply(parent, AlterOp{Kind: AlterTableOptions, Options: &TableOptions{Collation: "utf8mb4_bin", Convert: true}})
			st := state(parent)
			if st.Info.Collation != "utf8mb4_bin" {
				t.Errorf("collation %q", st.Info.Collation)
			}
			if n, _ := findColumn(st, "name"); n.Collation != "utf8mb4_bin" {
				t.Errorf("column collation %q", n.Collation)
			}
			// Restating a column keeps AUTO_INCREMENT when asked to.
			exec("DROP TABLE IF EXISTS gt_alter_ai")
			exec("CREATE TABLE gt_alter_ai (id INT AUTO_INCREMENT PRIMARY KEY, v INT)")
			ai := TableRef{Schema: schema, Name: "gt_alter_ai"}
			apply(ai, AlterOp{Kind: AlterModifyColumn, Column: "id", AutoIncrement: true, Def: &ColumnDef{Name: "id", Type: "BIGINT", Comment: "key"}})
			if id := col(ai, "id"); !id.AutoIncrement || id.Comment != "key" {
				t.Errorf("id after modify %+v", id)
			}
			// MySQL-only: move a column.
			apply(ai, AlterOp{Kind: AlterModifyColumn, Column: "v", Def: &ColumnDef{Name: "v", Type: "INT", Nullable: true, Position: "first"}})
			if st := state(ai); st.Columns[0].Name != "v" {
				t.Errorf("column order %v", st.Columns)
			}
			exec("DROP TABLE gt_alter_ai")
		})
	}

	t.Run("rename, drop and empty", func(t *testing.T) {
		apply(parent, AlterOp{Kind: AlterRenameTable, NewName: "gt_alter2"})
		if v := value("SELECT COUNT(*) FROM gt_alter2"); v != "1" {
			t.Errorf("renamed table has %s rows", v)
		}
		apply(TableRef{Schema: schema, Name: "gt_alter2"}, AlterOp{Kind: AlterRenameTable, NewName: "gt_alter"})
		// A column in an index (SQLite rebuilds the table for this).
		apply(parent, AlterOp{Kind: AlterAddIndex, Index: &IndexDef{Columns: []string{"note"}}})
		apply(parent, AlterOp{Kind: AlterDropColumn, Column: "note"})
		if _, ok := findColumn(state(parent), "note"); ok {
			t.Error("note still there")
		}
		apply(child, AlterOp{Kind: AlterTruncate})
		if v := value("SELECT COUNT(*) FROM gt_alter_child"); v != "0" {
			t.Errorf("child has %s rows after emptying", v)
		}
	})

	t.Run("dry run changes nothing", func(t *testing.T) {
		stmts, err := c.Alter(ctx, parent, []AlterOp{{Kind: AlterDropColumn, Column: "quantity"}}, true)
		if err != nil || len(stmts) == 0 {
			t.Fatalf("%v %v", stmts, err)
		}
		if _, ok := findColumn(state(parent), "quantity"); !ok {
			t.Error("dry run dropped the column")
		}
	})
	exec("DROP TABLE IF EXISTS gt_alter_child")
	exec("DROP TABLE IF EXISTS gt_alter")
}

func TestBuildAlterStatements(t *testing.T) {
	st := TableState{
		Columns: []Column{
			{Name: "id", Type: "integer", PrimaryKey: true},
			{Name: "name", Type: "text", Nullable: true},
		},
		Indexes: []Index{{Name: "t_pkey", Columns: []string{"id"}, Unique: true, Primary: true, Constraint: true}},
	}
	tbl := TableRef{Schema: "public", Name: "t"}
	plan, err := BuildAlter(pg, tbl, st, []AlterOp{{Kind: AlterModifyColumn, Column: "name",
		Def: &ColumnDef{Name: "title", Type: "varchar(80)", DefaultKind: DefaultValue, Default: "x", Comment: "Title"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`ALTER TABLE "public"."t" RENAME COLUMN "name" TO "title";`,
		`ALTER TABLE "public"."t" ALTER COLUMN "title" TYPE varchar(80) USING "title"::varchar(80);`,
		`ALTER TABLE "public"."t" ALTER COLUMN "title" SET NOT NULL;`,
		`ALTER TABLE "public"."t" ALTER COLUMN "title" SET DEFAULT 'x';`,
		`COMMENT ON COLUMN "public"."t"."title" IS 'Title';`,
	}
	if !slices.Equal(plan.Statements, want) {
		t.Errorf("modify:\n%s", strings.Join(plan.Statements, "\n"))
	}
	plan, err = BuildAlter(pg, tbl, st, []AlterOp{{Kind: AlterSetPrimaryKey, Columns: []string{"id", "name"}}})
	if err != nil || plan.Statements[0] != `ALTER TABLE "public"."t" DROP CONSTRAINT "t_pkey", ADD PRIMARY KEY ("id", "name");` {
		t.Errorf("primary key: %v %v", plan.Statements, err)
	}
	for _, bad := range []AlterOp{
		{Kind: AlterDropColumn, Column: "nope"},
		{Kind: AlterAddIndex, Index: &IndexDef{Columns: []string{"name"}, Kind: "fulltext"}},
		{Kind: AlterAddIndex, Index: &IndexDef{Columns: []string{"name"}, Method: "bogus"}},
		{Kind: AlterAddForeignKey, ForeignKey: &ForeignKeyDef{Columns: []string{"id"}, RefTable: "u", RefColumns: []string{"a", "b"}}},
		{Kind: AlterAddCheck, Check: &CheckDef{Expression: "1; DROP TABLE t"}},
		{Kind: AlterTableOptions, Options: &TableOptions{Engine: "InnoDB"}},
	} {
		if _, err := BuildAlter(pg, tbl, st, []AlterOp{bad}); err == nil {
			t.Errorf("%+v: expected an error", bad)
		}
	}
	my := DialectFor(model.DriverMySQL)
	plan, err = BuildAlter(my, TableRef{Schema: "shop", Name: "t"}, st, []AlterOp{{Kind: AlterAddIndex, Index: &IndexDef{Columns: []string{"name"}, Kind: "fulltext"}}})
	if err != nil || plan.Statements[0] != "ALTER TABLE `shop`.`t` ADD FULLTEXT INDEX `t_name_idx` (`name`);" {
		t.Errorf("fulltext: %v %v", plan.Statements, err)
	}
}
