package db

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"
)

// TestClickHouse covers what GoTable does with a ClickHouse server. It has
// its own test because ClickHouse differs too much for the shared suite: no
// transactions, foreign keys or auto-increment, and rows change through
// mutations.
func TestClickHouse(t *testing.T) {
	cfg, pw := serverConfig(t, "CH", model.DriverClickHouse)
	c := openTest(t, cfg, pw)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	exec := func(q string) Result {
		t.Helper()
		res, err := c.Execute(ctx, q, 0)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return res[len(res)-1]
	}
	must := func(q string) {
		t.Helper()
		if r := exec(q); r.Err != nil {
			t.Fatalf("%s: %v", q, r.Err)
		}
	}
	schema := c.Schema()
	tbl := TableRef{Schema: schema, Name: "gt_ch"}
	must("DROP TABLE IF EXISTS gt_ch_view")
	must("DROP TABLE IF EXISTS gt_ch")
	must(`CREATE TABLE gt_ch (id UInt32, name String, city Nullable(String), score Int32 DEFAULT 0 COMMENT 'points',
		mood Enum8('sad' = 1, 'it''s ok' = 2)) ENGINE = MergeTree ORDER BY id COMMENT 'people'`)
	must(`INSERT INTO gt_ch (id, name, city, score, mood) VALUES (1, 'Alice', 'Berlin', 90, 'sad'), (2, 'Bob', 'Paris', 70, 'sad'),
		(3, 'Carol', NULL, 85, 'sad'), (4, 'Dave', 'Berlin', 60, 'sad'), (5, 'Eve_x', 'Rome', 99, 'sad')`)
	must("CREATE VIEW gt_ch_view AS SELECT name, score FROM gt_ch")
	defer c.Execute(context.Background(), "DROP TABLE IF EXISTS gt_ch_view; DROP TABLE IF EXISTS gt_ch", 0)

	t.Run("catalog", func(t *testing.T) {
		if v, err := c.ServerVersion(ctx); err != nil || !strings.HasPrefix(v, "ClickHouse ") {
			t.Errorf("version %q %v", v, err)
		}
		if s, _ := c.Schemas(ctx); !slices.Contains(s, schema) || slices.Contains(s, "system") {
			t.Errorf("schemas %v", s)
		}
		objs, err := c.Objects(ctx, schema)
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]ObjectKind{}
		for _, o := range objs {
			kinds[o.Name] = o.Kind
		}
		if kinds["gt_ch"] != KindTable || kinds["gt_ch_view"] != KindView {
			t.Errorf("objects %v", kinds)
		}
		cols, err := c.Columns(ctx, tbl)
		if err != nil || len(cols) != 5 {
			t.Fatalf("columns %+v %v", cols, err)
		}
		if !cols[0].PrimaryKey || cols[0].Nullable || cols[2].Type != "String" || !cols[2].Nullable {
			t.Errorf("columns %+v", cols[:3])
		}
		if cols[3].DefaultKind != DefaultValue || cols[3].DefaultText != "0" || cols[3].Comment != "points" {
			t.Errorf("score %+v", cols[3])
		}
		if !slices.Equal(cols[4].Values, []string{"sad", "it's ok"}) {
			t.Errorf("enum values %q", cols[4].Values)
		}
		idx, err := c.Indexes(ctx, tbl)
		if err != nil || len(idx) != 1 || !idx[0].Primary || !slices.Equal(idx[0].Columns, []string{"id"}) {
			t.Errorf("indexes %+v %v", idx, err)
		}
		info, err := c.TableInfo(ctx, tbl)
		if err != nil || info.Comment != "people" || info.Engine != "MergeTree" {
			t.Errorf("info %+v %v", info, err)
		}
		if ddl, err := c.Definition(ctx, Object{Schema: schema, Name: "gt_ch", Kind: KindTable}); err != nil || !strings.Contains(ddl, "MergeTree") {
			t.Errorf("definition %q %v", ddl, err)
		}
	})

	cols := []string{"id", "name", "city", "score", "mood"}
	t.Run("browse and filter", func(t *testing.T) {
		rs, err := c.FetchRows(ctx, tbl, Page{Sort: []Sort{{Column: "score", Dir: Desc}}, Limit: 2, Offset: 1}, cols)
		if err != nil || !slices.Equal(names(rs, 1), []string{"Alice", "Carol"}) {
			t.Fatalf("page %v %v", rs, err)
		}
		for _, tc := range []struct {
			f    Filter
			want []string
		}{
			{Filter{Column: "city", Op: OpEq, Value: "Berlin"}, []string{"Alice", "Dave"}},
			{Filter{Column: "score", Op: OpGt, Value: "85"}, []string{"Alice", "Eve_x"}},
			{Filter{Column: "city", Op: OpIn, Value: "Paris, Rome"}, []string{"Bob", "Eve_x"}},
			{Filter{Column: "city", Op: OpIsNull}, []string{"Carol"}},
			{Filter{Column: "score", Op: OpBetween, Value: "60", Value2: "85"}, []string{"Bob", "Carol", "Dave"}},
			{Filter{Column: "name", Op: OpContains, Value: "AR"}, []string{"Carol"}},
			{Filter{Column: "name", Op: OpContains, Value: "_"}, []string{"Eve_x"}},
			{Filter{Column: AnyColumn, Op: OpContains, Value: "rom"}, []string{"Eve_x"}},
		} {
			p := Page{Filters: []Filter{tc.f}, Sort: []Sort{{Column: "name"}}}
			rs, err := c.FetchRows(ctx, tbl, p, cols)
			if err != nil || !slices.Equal(names(rs, 1), tc.want) {
				t.Errorf("%+v: %v %v", tc.f, names(rs, 1), err)
			}
			if n, err := c.CountRows(ctx, tbl, p, cols); err != nil || n != int64(len(tc.want)) {
				t.Errorf("%+v: count %d %v", tc.f, n, err)
			}
		}
		if rs, _ := c.FetchRows(ctx, tbl, Page{Filters: []Filter{{Column: "name", Op: OpEq, Value: "Carol"}}}, cols); len(rs.Rows) != 1 || !rs.Rows[0][2].Null {
			t.Errorf("NULL city: %+v", rs.Rows)
		}
	})

	t.Run("edit rows", func(t *testing.T) {
		err := c.ApplyChanges(ctx, tbl, []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"id": Text("1")}, Values: map[string]Value{"city": Text("Munich"), "score": Text("91")}},
			{Kind: ChangeUpdate, Key: map[string]Value{"id": Text("2")}, Values: map[string]Value{"city": NullValue}},
			{Kind: ChangeInsert, Values: map[string]Value{"id": Text("6"), "name": Text(`Frank 'O\`), "mood": Text("it's ok")}},
			{Kind: ChangeDelete, Key: map[string]Value{"id": Text("4")}},
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		rs, _ := c.FetchRows(ctx, tbl, Page{Sort: []Sort{{Column: "id"}}}, cols)
		got := map[string][]Value{}
		for _, r := range rs.Rows {
			got[r[1].Text] = r
		}
		if a := got["Alice"]; a[2].Text != "Munich" || a[3].Text != "91" {
			t.Errorf("Alice %+v", a)
		}
		if !got["Bob"][2].Null {
			t.Errorf("Bob %+v", got["Bob"])
		}
		if f, ok := got[`Frank 'O\`]; !ok || f[3].Text != "0" || f[4].Text != "it's ok" {
			t.Errorf("Frank %+v", f)
		}
		if _, ok := got["Dave"]; ok {
			t.Error("Dave not deleted")
		}
		if err := c.ApplyChanges(ctx, tbl, []Change{{Kind: ChangeInsert}}, true); err == nil {
			t.Error("an empty insert should be refused")
		}
	})

	t.Run("sql editor", func(t *testing.T) {
		res, err := c.Execute(ctx, "-- two;\nSELECT count() AS n FROM gt_ch; SELECT name FROM gt_ch WHERE name = 'it\\'s' OR id = 1", 0)
		if err != nil || len(res) != 2 || res[0].Err != nil || res[1].Err != nil {
			t.Fatalf("%v %+v", err, res)
		}
		if res[0].Set.Rows[0][0].Text != "5" || res[1].Set.Rows[0][0].Text != "Alice" {
			t.Errorf("results %+v %+v", res[0].Set, res[1].Set)
		}
		if res, _ := c.Execute(ctx, "SELECT * FROM gt_ch", 2); !res[0].Truncated {
			t.Error("row cap")
		}
		if res, _ := c.Execute(ctx, "SELECT [1, 2] AS l, map('a', 1) AS m, (1, 'x') AS t", 0); res[0].Err != nil ||
			res[0].Set.Rows[0][0].Text != "[1,2]" || res[0].Set.Rows[0][1].Text != `{"a":1}` {
			t.Errorf("composite values %+v %v", res[0].Set, res[0].Err)
		}
	})

	t.Run("structure", func(t *testing.T) {
		apply := func(ops ...AlterOp) {
			t.Helper()
			if stmts, err := c.Alter(ctx, tbl, ops, false); err != nil {
				t.Fatalf("%v: %v", stmts, err)
			}
		}
		col := func(n string) Column {
			t.Helper()
			cols, _ := c.Columns(ctx, tbl)
			for _, cl := range cols {
				if cl.Name == n {
					return cl
				}
			}
			t.Fatalf("no column %s", n)
			return Column{}
		}
		apply(AlterOp{Kind: AlterAddColumn, Def: &ColumnDef{Name: "note", Type: "String", Nullable: true, DefaultKind: DefaultValue, Default: "it's", Comment: "free", Position: "name"}})
		if n := col("note"); !n.Nullable || n.DefaultText != "it's" || n.Comment != "free" {
			t.Errorf("note %+v", n)
		}
		apply(AlterOp{Kind: AlterModifyColumn, Column: "score", Def: &ColumnDef{Name: "points", Type: "Int64", DefaultKind: DefaultValue, Default: "1", Comment: "pts"}})
		if p := col("points"); p.Type != "Int64" || p.Nullable || p.DefaultText != "1" || p.Comment != "pts" {
			t.Errorf("points %+v", p)
		}
		apply(AlterOp{Kind: AlterModifyColumn, Column: "points", Def: &ColumnDef{Name: "points", Type: "Int64", Nullable: true, Comment: "pts"}})
		if p := col("points"); !p.Nullable || p.Default != nil {
			t.Errorf("points nullable, no default %+v", p)
		}
		apply(AlterOp{Kind: AlterAddIndex, Index: &IndexDef{Name: "gt_ch_name", Columns: []string{"name"}, Method: "bloom_filter"}})
		if idx, _ := c.Indexes(ctx, tbl); len(idx) != 2 || idx[1].Name != "gt_ch_name" || idx[1].Type != "bloom_filter" {
			t.Errorf("indexes %+v", idx)
		}
		apply(AlterOp{Kind: AlterDropIndex, Name: "gt_ch_name"})
		apply(AlterOp{Kind: AlterAddCheck, Check: &CheckDef{Name: "gt_ch_id", Expression: "id < 1000"}})
		if info, _ := c.TableInfo(ctx, tbl); len(info.Checks) != 1 || info.Checks[0].Name != "gt_ch_id" {
			t.Errorf("checks %+v", info.Checks)
		}
		if r := exec("INSERT INTO gt_ch (id, name) VALUES (5000, 'x')"); r.Err == nil {
			t.Error("CHECK let 5000 in")
		}
		apply(AlterOp{Kind: AlterDropCheck, Name: "gt_ch_id"})
		apply(AlterOp{Kind: AlterTableComment, Comment: "folks"})
		apply(AlterOp{Kind: AlterDropColumn, Column: "note"})
		for _, bad := range []AlterOp{{Kind: AlterSetPrimaryKey, Columns: []string{"name"}},
			{Kind: AlterAddForeignKey, ForeignKey: &ForeignKeyDef{Columns: []string{"id"}, RefTable: "x", RefColumns: []string{"id"}}},
			{Kind: AlterRenameIndex, Name: "PRIMARY", NewName: "x"}} {
			if _, err := c.Alter(ctx, tbl, []AlterOp{bad}, false); err == nil {
				t.Errorf("%s should be refused", bad.Kind)
			}
		}
		apply(AlterOp{Kind: AlterRenameTable, NewName: "gt_ch2"})
		renamed := TableRef{Schema: schema, Name: "gt_ch2"}
		if _, err := c.Alter(ctx, renamed, []AlterOp{{Kind: AlterRenameTable, NewName: "gt_ch"}}, false); err != nil {
			t.Fatal(err)
		}
		if info, _ := c.TableInfo(ctx, tbl); info.Comment != "folks" {
			t.Errorf("comment %q", info.Comment)
		}
	})

	t.Run("create and dump", func(t *testing.T) {
		c.Execute(ctx, "DROP TABLE IF EXISTS gt_ch_made", 0)
		nt := NewTable{Name: "gt_ch_made", Comment: "made", Columns: []NewColumn{
			{ColumnDef: ColumnDef{Name: "id", Type: "UInt64"}, PrimaryKey: true},
			{ColumnDef: ColumnDef{Name: "label", Type: "String", Nullable: true, Comment: "l"}, Index: "index"},
		}}
		if _, err := c.CreateTable(ctx, schema, nt); err != nil {
			t.Fatal(err)
		}
		defer c.Execute(context.Background(), "DROP TABLE IF EXISTS gt_ch_made", 0)
		cols, _ := c.Columns(ctx, TableRef{Schema: schema, Name: "gt_ch_made"})
		if len(cols) != 2 || !cols[0].PrimaryKey || !cols[1].Nullable || cols[1].Comment != "l" {
			t.Errorf("created %+v", cols)
		}
		nt.Columns[0].AutoIncrement = true
		if _, err := BuildCreateTable(c.Dialect, schema, nt); err == nil {
			t.Error("auto-increment should be refused")
		}
		info, err := c.TypeInfo(ctx, schema)
		if err != nil || !slices.Contains(info.Engines, "MergeTree") {
			t.Errorf("type info %+v %v", info, err)
		}
		c.Execute(ctx, "DROP DATABASE IF EXISTS gt_ch_db", 0)
		if _, err := c.CreateDatabase(ctx, NewDatabase{Name: "gt_ch_db"}); err != nil {
			t.Fatal(err)
		}
		c.Execute(ctx, "DROP DATABASE gt_ch_db", 0)

		var buf bytes.Buffer
		err = c.Dump(ctx, &buf, DumpOptions{Objects: []Object{{Schema: schema, Name: "gt_ch", Kind: KindTable}}, Structure: true, Data: true, DropFirst: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		dump := buf.String()
		for _, want := range []string{"DROP TABLE IF EXISTS `gt_ch`", "CREATE TABLE gt_ch", "MergeTree", "INSERT INTO `gt_ch`", `'Frank \'O\\'`} {
			if !strings.Contains(dump, want) {
				t.Errorf("dump lacks %q:\n%s", want, dump)
			}
		}
		// The dump loads back.
		must("DROP TABLE gt_ch_view")
		if _, err := c.ImportSQL(ctx, strings.NewReader(dump), ImportOptions{Transaction: true}, nil); err != nil {
			t.Fatal(err)
		}
		if n, err := c.CountRows(ctx, tbl, Page{}, nil); err != nil || n != 5 {
			t.Errorf("after import: %d rows %v", n, err)
		}
		must("CREATE VIEW gt_ch_view AS SELECT name FROM gt_ch")
	})

	t.Run("drop", func(t *testing.T) {
		if _, err := c.Drop(ctx, []Object{{Schema: schema, Name: "gt_ch_view", Kind: KindView}}, true); err == nil {
			t.Error("CASCADE should be refused")
		}
		if _, err := c.Drop(ctx, []Object{{Schema: schema, Name: "gt_ch_view", Kind: KindView}, {Schema: schema, Name: "gt_ch", Kind: KindTable}}, false); err != nil {
			t.Fatal(err)
		}
		if objs, _ := c.Objects(ctx, schema); slices.ContainsFunc(objs, func(o Object) bool { return strings.HasPrefix(o.Name, "gt_ch") && o.Name != "gt_ch_made" }) {
			t.Errorf("left over: %v", objs)
		}
	})
}
