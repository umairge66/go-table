package db

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"
)

// suiteDDL returns engine specific DDL for the conformance suite tables.
func suiteDDL(d model.Driver) []string {
	id := "id INTEGER PRIMARY KEY"
	switch d {
	case model.DriverPostgres:
		id = "id SERIAL PRIMARY KEY"
	case model.DriverMySQL, model.DriverMariaDB:
		id = "id INT AUTO_INCREMENT PRIMARY KEY"
	}
	return []string{
		"DROP VIEW IF EXISTS gt_people_view",
		"DROP TABLE IF EXISTS gt_people",
		"DROP TABLE IF EXISTS gt_nopk",
		"CREATE TABLE gt_people (" + id + ", name VARCHAR(100) NOT NULL, city VARCHAR(100), score INT DEFAULT 0)",
		"CREATE INDEX gt_people_city_idx ON gt_people (city)",
		"CREATE TABLE gt_nopk (a INT, b VARCHAR(10))",
		"CREATE VIEW gt_people_view AS SELECT name, score FROM gt_people",
		`INSERT INTO gt_people (name, city, score) VALUES
			('Alice', 'Berlin', 90), ('Bob', 'Paris', 70), ('Carol', NULL, 85),
			('Dave', 'Berlin', 60), ('Eve_x', 'Rome', 99)`,
		"INSERT INTO gt_nopk (a, b) VALUES (1, 'x'), (1, 'x'), (2, 'y')",
	}
}

func names(rs *ResultSet, col int) []string {
	out := make([]string, len(rs.Rows))
	for i, r := range rs.Rows {
		out[i] = r[col].String()
	}
	return out
}

// runSuite exercises every Conn operation against a live database.
func runSuite(t *testing.T, c *Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	kind := c.Config.Driver
	for _, s := range suiteDDL(kind) {
		res, err := c.Execute(ctx, s, 0)
		if err != nil || res[len(res)-1].Err != nil {
			t.Fatalf("setup %q: %v %v", s, err, res)
		}
	}
	schema := c.Schema()
	tbl := TableRef{Schema: schema, Name: "gt_people"}
	nopk := TableRef{Schema: schema, Name: "gt_nopk"}

	t.Run("version", func(t *testing.T) {
		v, err := c.ServerVersion(ctx)
		if err != nil || v == "" {
			t.Fatalf("version %q %v", v, err)
		}
	})

	t.Run("schemas", func(t *testing.T) {
		s, err := c.Schemas(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(s, schema) {
			t.Fatalf("schemas %v missing %q", s, schema)
		}
	})

	t.Run("objects", func(t *testing.T) {
		objs, err := c.Objects(ctx, schema)
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]ObjectKind{}
		for _, o := range objs {
			kinds[o.Name] = o.Kind
		}
		if kinds["gt_people"] != KindTable || kinds["gt_nopk"] != KindTable || kinds["gt_people_view"] != KindView {
			t.Fatalf("objects %v", kinds)
		}
	})

	t.Run("columns", func(t *testing.T) {
		cols, err := c.Columns(ctx, tbl)
		if err != nil {
			t.Fatal(err)
		}
		if len(cols) != 4 {
			t.Fatalf("columns %+v", cols)
		}
		if cols[0].Name != "id" || !cols[0].PrimaryKey {
			t.Errorf("id column %+v", cols[0])
		}
		if cols[1].Name != "name" || cols[1].Nullable || !strings.Contains(strings.ToLower(cols[1].Type), "char") {
			t.Errorf("name column %+v", cols[1])
		}
		if !cols[2].Nullable || cols[2].PrimaryKey {
			t.Errorf("city column %+v", cols[2])
		}
		if cols[3].Default == nil || !strings.Contains(*cols[3].Default, "0") {
			t.Errorf("score default %+v", cols[3])
		}
		if _, err := c.Columns(ctx, TableRef{Schema: schema, Name: "gt_missing"}); err == nil {
			t.Error("expected error for missing table")
		}
	})

	t.Run("create table and database", func(t *testing.T) {
		c.Execute(ctx, "DROP TABLE IF EXISTS gt_created", 0)
		idType := "bigint"
		if kind == model.DriverSQLite {
			idType = "INTEGER"
		}
		nt := NewTable{Name: "gt_created", Columns: []NewColumn{
			{ColumnDef: ColumnDef{Name: "id", Type: idType}, PrimaryKey: true, AutoIncrement: true},
			{ColumnDef: ColumnDef{Name: "label", Type: "varchar(50)", DefaultKind: DefaultValue, Default: "none"}},
		}}
		if _, err := c.CreateTable(ctx, schema, nt); err != nil {
			t.Fatal(err)
		}
		defer c.Execute(ctx, "DROP TABLE gt_created", 0)
		if res, err := c.Execute(ctx, "INSERT INTO gt_created (label) VALUES ('a'); INSERT INTO gt_created (label) VALUES ('b')", 0); err != nil || res[0].Err != nil {
			t.Fatalf("insert: %v %v", err, res)
		}
		res, _ := c.Execute(ctx, "SELECT id, label FROM gt_created ORDER BY id", 0)
		if got := names(res[0].Set, 0); len(got) != 2 || got[0] != "1" || got[1] != "2" {
			t.Fatalf("auto-increment ids %v", got)
		}
		// Creating it again fails cleanly, and nothing half-made is left.
		if _, err := c.CreateTable(ctx, schema, nt); err == nil {
			t.Fatal("duplicate table should fail")
		}

		// Everything the designer offers, per database, run for real.
		info, err := c.TypeInfo(ctx, schema)
		if err != nil || len(info.Collations) == 0 {
			t.Fatalf("type info %+v %v", info, err)
		}
		c.Execute(ctx, "DROP TABLE IF EXISTS gt_designed", 0)
		cols := []NewColumn{
			{ColumnDef: ColumnDef{Name: "id", Type: idType}, PrimaryKey: true, AutoIncrement: true},
			{ColumnDef: ColumnDef{Name: "email", Type: "varchar(190)", Collation: info.Collations[0]}, Index: "unique"},
			{ColumnDef: ColumnDef{Name: "city", Type: "varchar(80)", Nullable: true}, Index: "index"},
		}
		nt2 := NewTable{Name: "gt_designed", Columns: cols}
		switch {
		case kind == model.DriverMySQL || kind == model.DriverMariaDB:
			if len(info.Engines) == 0 || info.DefaultEngine == "" || len(info.Charsets) == 0 {
				t.Fatalf("mysql type info %+v", info)
			}
			nt2.Engine, nt2.Charset, nt2.Collation = "InnoDB", "utf8mb4", "utf8mb4_unicode_ci"
			cols[1].Collation = "utf8mb4_bin"
			nt2.Columns = append(cols,
				NewColumn{ColumnDef: ColumnDef{Name: "bio", Type: "text", Nullable: true}, Index: "fulltext"},
				NewColumn{ColumnDef: ColumnDef{Name: "updated", Type: "timestamp", Nullable: true, DefaultKind: DefaultExpression, Default: "CURRENT_TIMESTAMP", OnUpdateNow: true}},
				NewColumn{ColumnDef: ColumnDef{Name: "qty", Type: "int unsigned", Nullable: true}})
		case kind == model.DriverPostgres:
			cols[1].Collation = "C"
		case kind == model.DriverSQLite:
			cols[1].Collation = "NOCASE"
			if info.Strict {
				nt2.Strict = true
				for i := range cols[1:] {
					cols[i+1].Type = "TEXT"
				}
			}
		}
		if _, err := c.CreateTable(ctx, schema, nt2); err != nil {
			t.Fatalf("designed table: %v", err)
		}
		defer c.Execute(ctx, "DROP TABLE gt_designed", 0)
		idx, err := c.Indexes(ctx, TableRef{Schema: schema, Name: "gt_designed"})
		if err != nil {
			t.Fatal(err)
		}
		var unique, plain bool
		for _, ix := range idx {
			if len(ix.Columns) == 1 && ix.Columns[0] == "email" && ix.Unique {
				unique = true
			}
			if len(ix.Columns) == 1 && ix.Columns[0] == "city" && !ix.Unique {
				plain = true
			}
		}
		if !unique || !plain {
			t.Fatalf("indexes %+v", idx)
		}

		if kind == model.DriverSQLite {
			if _, err := c.CreateDatabase(ctx, NewDatabase{Name: "x"}); err == nil {
				t.Fatal("SQLite has no CREATE DATABASE")
			}
			return
		}
		kinds := []string{"database"}
		if kind == model.DriverPostgres {
			kinds = append(kinds, "schema")
		}
		for _, k := range kinds {
			name := "gt_created_" + k
			drop := "DROP DATABASE IF EXISTS " + name
			if k == "schema" {
				drop = "DROP SCHEMA IF EXISTS " + name
			}
			c.Execute(ctx, drop, 0)
			if _, err := c.CreateDatabase(ctx, NewDatabase{Name: name, Kind: k}); err != nil {
				t.Fatalf("create %s: %v", k, err)
			}
			list, _ := c.Schemas(ctx)
			if k == "database" && kind == model.DriverPostgres {
				list, _ = c.Databases(ctx)
			}
			if !slices.Contains(list, name) {
				t.Errorf("%s %s not listed in %v", k, name, list)
			}
			c.Execute(ctx, drop, 0)
		}
	})

	t.Run("foreign keys", func(t *testing.T) {
		setup := []string{
			"DROP TABLE IF EXISTS gt_fk_child",
			"DROP TABLE IF EXISTS gt_fk_pair",
			"CREATE TABLE gt_fk_pair (a INT NOT NULL, b INT NOT NULL, PRIMARY KEY (a, b))",
			`CREATE TABLE gt_fk_child (id INT PRIMARY KEY, person INT, pa INT, pb INT,
				FOREIGN KEY (person) REFERENCES gt_people (id),
				FOREIGN KEY (pa, pb) REFERENCES gt_fk_pair (a, b))`,
		}
		for _, q := range setup {
			if res, err := c.Execute(ctx, q, 0); err != nil || res[0].Err != nil {
				t.Fatalf("%s: %v %v", q, err, res)
			}
		}
		defer c.Execute(ctx, "DROP TABLE gt_fk_child; DROP TABLE gt_fk_pair", 0)
		cols, err := c.Columns(ctx, TableRef{Schema: schema, Name: "gt_fk_child"})
		if err != nil {
			t.Fatal(err)
		}
		ref := cols[1].References
		if ref == nil || ref.Table != "gt_people" || !slices.Equal(ref.From, []string{"person"}) || !slices.Equal(ref.Columns, []string{"id"}) || ref.Schema != schema {
			t.Errorf("person reference %+v", ref)
		}
		if cols[0].References != nil {
			t.Errorf("id has no reference: %+v", cols[0].References)
		}
		for _, i := range []int{2, 3} {
			ref := cols[i].References
			if ref == nil || ref.Table != "gt_fk_pair" || !slices.Equal(ref.From, []string{"pa", "pb"}) || !slices.Equal(ref.Columns, []string{"a", "b"}) {
				t.Errorf("%s composite reference %+v", cols[i].Name, ref)
			}
		}
	})

	t.Run("indexes", func(t *testing.T) {
		idx, err := c.Indexes(ctx, tbl)
		if err != nil {
			t.Fatal(err)
		}
		var pk, city bool
		for _, ix := range idx {
			if ix.Primary && slices.Equal(ix.Columns, []string{"id"}) {
				pk = true
			}
			if ix.Name == "gt_people_city_idx" && !ix.Unique && slices.Equal(ix.Columns, []string{"city"}) {
				city = true
			}
		}
		if !pk || !city {
			t.Fatalf("indexes %+v", idx)
		}
	})

	t.Run("definition", func(t *testing.T) {
		ddl, err := c.Definition(ctx, Object{Schema: schema, Name: "gt_people", Kind: KindTable})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"CREATE TABLE", "gt_people", "name", "city"} {
			if !strings.Contains(ddl, want) {
				t.Errorf("table DDL missing %q:\n%s", want, ddl)
			}
		}
		vddl, err := c.Definition(ctx, Object{Schema: schema, Name: "gt_people_view", Kind: KindView})
		if err != nil || !strings.Contains(strings.ToUpper(vddl), "VIEW") {
			t.Fatalf("view DDL %q %v", vddl, err)
		}
	})

	cols := []string{"id", "name", "city", "score"}

	t.Run("fetch page and sort", func(t *testing.T) {
		rs, err := c.FetchRows(ctx, tbl, Page{Sort: []Sort{{Column: "score", Dir: Desc}}, Limit: 2, Offset: 1}, cols)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(rs, 1); !slices.Equal(got, []string{"Alice", "Carol"}) {
			t.Fatalf("page = %v", got)
		}
		if !slices.Equal(rs.Columns, cols) {
			t.Fatalf("columns %v", rs.Columns)
		}
	})

	t.Run("NULL is distinct from text", func(t *testing.T) {
		rs, err := c.FetchRows(ctx, tbl, Page{Filters: []Filter{{Column: "name", Op: OpEq, Value: "Carol"}}}, cols)
		if err != nil || len(rs.Rows) != 1 {
			t.Fatalf("%v %v", rs, err)
		}
		if !rs.Rows[0][2].Null {
			t.Fatalf("city should be NULL: %+v", rs.Rows[0][2])
		}
	})

	filterCases := []struct {
		name string
		f    []Filter
		raw  string
		want []string
	}{
		{"eq", []Filter{{Column: "city", Op: OpEq, Value: "Berlin"}}, "", []string{"Alice", "Dave"}},
		{"ne", []Filter{{Column: "city", Op: OpNe, Value: "Berlin"}}, "", []string{"Bob", "Eve_x"}},
		{"gt numeric", []Filter{{Column: "score", Op: OpGt, Value: "85"}}, "", []string{"Alice", "Eve_x"}},
		{"in", []Filter{{Column: "city", Op: OpIn, Value: "Paris, Rome"}}, "", []string{"Bob", "Eve_x"}},
		{"not in", []Filter{{Column: "city", Op: OpNotIn, Value: "Paris,Rome"}}, "", []string{"Alice", "Dave"}},
		{"is null", []Filter{{Column: "city", Op: OpIsNull}}, "", []string{"Carol"}},
		{"is not null", []Filter{{Column: "city", Op: OpIsNotNull}}, "", []string{"Alice", "Bob", "Dave", "Eve_x"}},
		{"between", []Filter{{Column: "score", Op: OpBetween, Value: "60", Value2: "85"}}, "", []string{"Bob", "Carol", "Dave"}},
		{"contains case-insensitive", []Filter{{Column: "name", Op: OpContains, Value: "AR"}}, "", []string{"Carol"}},
		{"contains escapes wildcard", []Filter{{Column: "name", Op: OpContains, Value: "_"}}, "", []string{"Eve_x"}},
		{"not contains", []Filter{{Column: "name", Op: OpNotContain, Value: "e"}}, "", []string{"Bob", "Carol"}},
		{"begins", []Filter{{Column: "name", Op: OpBeginsWith, Value: "da"}}, "", []string{"Dave"}},
		{"ends", []Filter{{Column: "name", Op: OpEndsWith, Value: "B"}}, "", []string{"Bob"}},
		{"like on int column", []Filter{{Column: "score", Op: OpLike, Value: "9%"}}, "", []string{"Alice", "Eve_x"}},
		{"any column", []Filter{{Column: AnyColumn, Op: OpContains, Value: "rom"}}, "", []string{"Eve_x"}},
		{"combined with raw", []Filter{{Column: "city", Op: OpEq, Value: "Berlin"}}, "score > 65", []string{"Alice"}},
	}
	for _, tc := range filterCases {
		t.Run("filter "+tc.name, func(t *testing.T) {
			p := Page{Filters: tc.f, RawWhere: tc.raw, Sort: []Sort{{Column: "name"}}}
			rs, err := c.FetchRows(ctx, tbl, p, cols)
			if err != nil {
				t.Fatal(err)
			}
			if got := names(rs, 1); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			n, err := c.CountRows(ctx, tbl, p, cols)
			if err != nil || n != int64(len(tc.want)) {
				t.Fatalf("count %d %v", n, err)
			}
		})
	}

	t.Run("apply changes", func(t *testing.T) {
		err := c.ApplyChanges(ctx, tbl, []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"id": Text("1")}, Values: map[string]Value{"city": Text("Munich"), "score": Text("91")}},
			{Kind: ChangeUpdate, Key: map[string]Value{"id": Text("2")}, Values: map[string]Value{"city": NullValue}},
			{Kind: ChangeInsert, Values: map[string]Value{"name": Text("Frank 'O"), "city": Text(`C:\x`)}},
			{Kind: ChangeDelete, Key: map[string]Value{"id": Text("4")}},
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		rs, err := c.FetchRows(ctx, tbl, Page{Sort: []Sort{{Column: "id"}}}, cols)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string][]Value{}
		for _, r := range rs.Rows {
			got[r[1].Text] = r
		}
		if a := got["Alice"]; a[2].Text != "Munich" || a[3].Text != "91" {
			t.Errorf("Alice %+v", a)
		}
		if !got["Bob"][2].Null {
			t.Errorf("Bob city should be NULL: %+v", got["Bob"])
		}
		if f, ok := got["Frank 'O"]; !ok || f[2].Text != `C:\x` || f[3].Text != "0" {
			t.Errorf("inserted row %+v (default score expected)", f)
		}
		if _, ok := got["Dave"]; ok {
			t.Error("Dave not deleted")
		}
	})

	t.Run("update unchanged value succeeds", func(t *testing.T) {
		err := c.ApplyChanges(ctx, tbl, []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"id": Text("1")}, Values: map[string]Value{"city": Text("Munich")}},
		}, true)
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("failed change rolls back all", func(t *testing.T) {
		err := c.ApplyChanges(ctx, tbl, []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"id": Text("1")}, Values: map[string]Value{"city": Text("Hamburg")}},
			{Kind: ChangeUpdate, Key: map[string]Value{"id": Text("999")}, Values: map[string]Value{"city": Text("x")}},
		}, true)
		if err == nil {
			t.Fatal("expected error for missing row")
		}
		rs, _ := c.FetchRows(ctx, tbl, Page{Filters: []Filter{{Column: "id", Op: OpEq, Value: "1"}}}, cols)
		if rs.Rows[0][2].Text != "Munich" {
			t.Fatalf("rollback failed: %+v", rs.Rows[0])
		}
		if !strings.Contains(err.Error(), "999") {
			t.Errorf("error should show failing statement: %v", err)
		}
	})

	t.Run("no primary key edits one row", func(t *testing.T) {
		key := map[string]Value{"a": Text("1"), "b": Text("x")}
		changes := []Change{{Kind: ChangeUpdate, Key: key, Values: map[string]Value{"b": Text("z")}}}
		if kind == model.DriverMySQL || kind == model.DriverMariaDB {
			if err := c.ApplyChanges(ctx, nopk, changes, false); err != nil {
				t.Fatal(err)
			}
			n, _ := c.CountRows(ctx, nopk, Page{Filters: []Filter{{Column: "b", Op: OpEq, Value: "z"}}}, nil)
			if n != 1 {
				t.Fatalf("updated %d rows, want 1", n)
			}
		}
		// Deleting with a key that matches nothing is reported.
		err := c.ApplyChanges(ctx, nopk, []Change{{Kind: ChangeDelete, Key: map[string]Value{"a": Text("7"), "b": NullValue}}}, false)
		if err == nil {
			t.Fatal("expected error deleting missing row")
		}
	})

	t.Run("execute script", func(t *testing.T) {
		script := `-- comment; with semicolon
			UPDATE gt_people SET score = score + 1 WHERE city IS NULL;
			SELECT name, score FROM gt_people WHERE name = 'it''s;fine' OR id = 1;
			/* block ; */ SELECT COUNT(*) AS n FROM gt_people`
		res, err := c.Execute(ctx, script, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 3 {
			t.Fatalf("results %d", len(res))
		}
		for _, r := range res {
			if r.Err != nil {
				t.Fatalf("%s: %v", r.Statement, r.Err)
			}
		}
		if res[0].Set != nil || res[0].RowsAffected != 2 {
			t.Errorf("update result %+v", res[0])
		}
		if res[1].Set == nil || len(res[1].Set.Rows) != 1 || res[1].Set.Rows[0][0].Text != "Alice" {
			t.Errorf("select result %+v", res[1].Set)
		}
		if res[2].Set.Columns[0] != "n" || res[2].Set.Rows[0][0].Text != "5" {
			t.Errorf("count result %+v", res[2].Set)
		}
	})

	t.Run("execute stops at error", func(t *testing.T) {
		res, err := c.Execute(ctx, "SELECT 1; SELECT * FROM gt_no_such_table; SELECT 2", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 2 || res[0].Err != nil || res[1].Err == nil {
			t.Fatalf("results %+v", res)
		}
	})

	t.Run("row cap truncates", func(t *testing.T) {
		res, err := c.Execute(ctx, "SELECT * FROM gt_people", 2)
		if err != nil || res[0].Err != nil {
			t.Fatal(err, res)
		}
		if len(res[0].Set.Rows) != 2 || !res[0].Truncated {
			t.Fatalf("rows %d truncated %v", len(res[0].Set.Rows), res[0].Truncated)
		}
	})

	t.Run("session state persists", func(t *testing.T) {
		if _, err := c.Execute(ctx, "BEGIN", 0); err != nil {
			t.Fatal(err)
		}
		if res, _ := c.Execute(ctx, "DELETE FROM gt_people", 0); res[0].Err != nil {
			t.Fatal(res[0].Err)
		}
		if res, _ := c.Execute(ctx, "ROLLBACK", 0); res[0].Err != nil {
			t.Fatal(res[0].Err)
		}
		n, err := c.CountRows(ctx, tbl, Page{}, nil)
		if err != nil || n != 5 {
			t.Fatalf("rollback via editor: %d %v", n, err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		cctx, ccancel := context.WithCancel(ctx)
		ccancel()
		if _, err := c.FetchRows(cctx, tbl, Page{}, cols); err == nil {
			t.Fatal("expected cancelled error")
		}
		// The session remains usable after a cancelled call.
		if err := c.Ping(ctx); err != nil {
			t.Fatalf("ping after cancel: %v", err)
		}
	})

	t.Run("add column", func(t *testing.T) {
		def := ColumnDef{Name: "note", Type: "varchar(50)", DefaultKind: DefaultValue, Default: "n/a"}
		if kind != model.DriverSQLite {
			def.Comment = "free text"
		}
		if kind == model.DriverMySQL || kind == model.DriverMariaDB {
			def.Position = "name"
		}
		stmts, err := c.AddColumn(ctx, tbl, def)
		if err != nil {
			t.Fatalf("%v (%q)", err, stmts)
		}
		cols, err := c.Columns(ctx, tbl)
		if err != nil {
			t.Fatal(err)
		}
		var got *Column
		for i := range cols {
			if cols[i].Name == "note" {
				got = &cols[i]
			}
		}
		if got == nil || got.Nullable || got.Default == nil || !strings.Contains(*got.Default, "n/a") {
			t.Fatalf("column %+v", got)
		}
		if kind != model.DriverSQLite && got.Comment != "free text" {
			t.Errorf("comment %q", got.Comment)
		}
		if (kind == model.DriverMySQL || kind == model.DriverMariaDB) && cols[2].Name != "note" {
			t.Errorf("position: %v", cols)
		}
		// Existing rows get the default.
		n, err := c.CountRows(ctx, tbl, Page{Filters: []Filter{{Column: "note", Op: OpEq, Value: "n/a"}}}, nil)
		if err != nil || n != 5 {
			t.Fatalf("rows with default: %d %v", n, err)
		}
		// A duplicate column is a database error, reported as such.
		if _, err := c.AddColumn(ctx, tbl, def); err == nil {
			t.Fatal("expected duplicate column error")
		}
	})

	for _, s := range []string{"DROP VIEW gt_people_view", "DROP TABLE gt_people", "DROP TABLE gt_nopk"} {
		c.Execute(ctx, s, 0)
	}
}
