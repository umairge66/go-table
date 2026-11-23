package db

// The compatibility suite: what GoTable does with a table, on servers whose
// SQL differs too much from their protocol's engine for the shared suite
// (CrateDB, QuestDB, StarRocks, Doris). Each says what it can do.

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

type compatCaps struct {
	version  string   // the version string's start
	setup    []string // makes gt_c (id, name, city, score) and its rows
	after    string   // run after writes so they can be read (CrateDB's REFRESH)
	views    bool
	edits    bool   // rows can be changed from the grid
	pk       bool   // gt_c has a primary key
	noDelete bool   // rows can't be deleted (QuestDB)
	textType string // for the added column; VARCHAR(50) by default
	// nullNotEqual: NULL <> 'x' is true (QuestDB), not unknown as in SQL.
	nullNotEqual bool
	// editor: two statements that run, and a script whose second fails
	// (the defaults are SQL).
	editorOK, editorStop string
	addCol               bool
	csv                  bool
	dump                 bool // a SQL dump loads back
}

func runCompatSuite(t *testing.T, c *Conn, caps compatCaps) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	exec := func(q string) Result {
		t.Helper()
		res, err := c.Execute(ctx, q, 0)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		for _, r := range res {
			if r.Err != nil {
				t.Fatalf("%s: %v", r.Statement, r.Err)
			}
		}
		return res[len(res)-1]
	}
	settle := func() {
		if caps.after != "" {
			exec(caps.after)
		}
	}
	for _, q := range caps.setup {
		exec(q)
	}
	settle()
	schema := c.Schema()
	ref := TableRef{Schema: schema, Name: "gt_c"}
	t.Cleanup(func() {
		c.Execute(context.Background(), "DROP VIEW IF EXISTS gt_cv", 0)
		c.Execute(context.Background(), "DROP TABLE IF EXISTS gt_c", 0)
		c.Execute(context.Background(), "DROP TABLE IF EXISTS gt_copy", 0)
	})
	names := func(p Page) []string {
		t.Helper()
		if p.Sort == nil {
			p.Sort = []Sort{{Column: "id", Dir: Asc}}
		}
		rs, err := c.FetchRows(ctx, ref, p, nil)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.Index(rs.Columns, "name")
		out := []string{}
		for _, r := range rs.Rows {
			out = append(out, r[i].Text)
		}
		return out
	}

	t.Run("introspection", func(t *testing.T) {
		if v, err := c.ServerVersion(ctx); err != nil || !strings.HasPrefix(v, caps.version) {
			t.Fatalf("version %q %v", v, err)
		}
		if s, err := c.Schemas(ctx); err != nil || !slices.Contains(s, schema) {
			t.Fatal(s, schema, err)
		}
		if caps.views {
			exec("CREATE VIEW gt_cv AS SELECT name, score FROM gt_c")
		}
		objs, err := c.Objects(ctx, schema)
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]ObjectKind{}
		for _, o := range objs {
			kinds[o.Name] = o.Kind
		}
		if kinds["gt_c"] != KindTable || caps.views && kinds["gt_cv"] != KindView {
			t.Fatalf("%+v", objs)
		}
		cols, err := c.Columns(ctx, ref)
		if err != nil || len(cols) != 4 || cols[0].Name != "id" || !slices.ContainsFunc(cols, func(c Column) bool { return c.Name == "name" }) || caps.pk != cols[0].PrimaryKey {
			t.Fatalf("%+v %v", cols, err)
		}
		if _, err := c.Indexes(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if _, err := c.TableInfo(ctx, ref); err != nil {
			t.Fatal(err)
		}
		def, err := c.Definition(ctx, Object{Schema: schema, Name: "gt_c", Kind: KindTable})
		if err != nil || !strings.Contains(def, "gt_c") || !strings.Contains(def, "score") {
			t.Fatal(def, err)
		}
		if caps.views {
			def, err := c.Definition(ctx, Object{Schema: schema, Name: "gt_cv", Kind: KindView})
			if err != nil || !strings.Contains(strings.ToLower(def), "select") {
				t.Fatal(def, err)
			}
		}
	})

	t.Run("rows, sorting and paging", func(t *testing.T) {
		if got := names(Page{}); !slices.Equal(got, []string{"Alice", "Bob", "Carol", "Dave", "Eve_x"}) {
			t.Fatal(got)
		}
		if got := names(Page{Sort: []Sort{{Column: "score", Dir: Desc}}, Limit: 2, Offset: 1}); !slices.Equal(got, []string{"Alice", "Carol"}) {
			t.Fatal(got)
		}
		if n, err := c.CountRows(ctx, ref, Page{}, nil); err != nil || n != 5 {
			t.Fatal(n, err)
		}
	})

	t.Run("filters", func(t *testing.T) {
		for _, tc := range []struct {
			f    Filter
			want []string
		}{
			{Filter{Column: "city", Op: OpEq, Value: "Berlin"}, []string{"Alice", "Dave"}},
			{Filter{Column: "city", Op: OpNe, Value: "Berlin"}, map[bool][]string{false: {"Bob", "Eve_x"}, true: {"Bob", "Carol", "Eve_x"}}[caps.nullNotEqual]},
			{Filter{Column: "score", Op: OpGt, Value: "80"}, []string{"Alice", "Carol", "Eve_x"}},
			{Filter{Column: "score", Op: OpBetween, Value: "60", Value2: "80"}, []string{"Bob", "Dave"}},
			{Filter{Column: "score", Op: OpIn, Value: "70, 99"}, []string{"Bob", "Eve_x"}},
			{Filter{Column: "city", Op: OpIsNull}, []string{"Carol"}},
			{Filter{Column: "name", Op: OpContains, Value: "AL"}, []string{"Alice"}},
			{Filter{Column: "name", Op: OpContains, Value: "e_x"}, []string{"Eve_x"}},
			{Filter{Column: "name", Op: OpBeginsWith, Value: "b"}, []string{"Bob"}},
			{Filter{Column: "name", Op: OpEndsWith, Value: "OL"}, []string{"Carol"}},
		} {
			if got := names(Page{Filters: []Filter{tc.f}}); !slices.Equal(got, tc.want) {
				t.Errorf("%+v: got %v, want %v", tc.f, got, tc.want)
			}
		}
		rs, err := c.FetchRows(ctx, ref, Page{Filters: []Filter{{Column: AnyColumn, Op: OpContains, Value: "pari"}}}, []string{"id", "name", "city", "score"})
		if err != nil || len(rs.Rows) != 1 {
			t.Fatal(rs, err)
		}
		if n, err := c.CountRows(ctx, ref, Page{Filters: []Filter{{Column: "city", Op: OpEq, Value: "Berlin"}}}, nil); err != nil || n != 2 {
			t.Fatal(n, err)
		}
	})

	t.Run("editing", func(t *testing.T) {
		if !caps.edits {
			t.Skip("no row edits on this server")
		}
		del := Change{Kind: ChangeDelete, Key: map[string]Value{"id": Text("2")}}
		changes := []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"id": Text("1")}, Values: map[string]Value{"name": Text("Alicia"), "city": NullValue}},
			{Kind: ChangeInsert, Values: map[string]Value{"id": Text("6"), "name": Text("Frank"), "score": Text("50")}},
		}
		want := []string{"Alicia", "Bob", "Carol", "Dave", "Eve_x", "Frank"}
		if caps.noDelete {
			if err := c.ApplyChanges(ctx, ref, []Change{del}, caps.pk); err == nil || !strings.Contains(err.Error(), "can't delete") {
				t.Fatal("a delete should be refused before it runs:", err)
			}
		} else {
			changes = append(changes, del)
			want = slices.Delete(want, 1, 2)
		}
		if stmts, err := c.PreviewChanges(ctx, ref, changes, caps.pk); err != nil || len(stmts) != len(changes) {
			t.Fatal(stmts, err)
		}
		if err := c.ApplyChanges(ctx, ref, changes, caps.pk); err != nil {
			t.Fatal(err)
		}
		settle()
		if got := names(Page{}); !slices.Equal(got, want) {
			t.Fatal(got)
		}
		if got := names(Page{Filters: []Filter{{Column: "city", Op: OpIsNull}}}); !slices.Equal(got, []string{"Alicia", "Carol", "Frank"}) {
			t.Fatal(got)
		}
	})

	t.Run("query editor", func(t *testing.T) {
		ok, stop := caps.editorOK, caps.editorStop
		if ok == "" {
			ok = "SELECT name FROM gt_c WHERE score > 80 ORDER BY name;\nSELECT COUNT(*) AS n FROM gt_c"
			stop = "SELECT 1 AS a;\nSELECT * FROM gt_no_such_table;\nSELECT 2 AS b"
		}
		res, err := c.Execute(ctx, ok, 0)
		if err != nil || len(res) != 2 || res[0].Set == nil || res[1].Set.Rows[0][0].Text == "" {
			t.Fatal(res, err)
		}
		res, _ = c.Execute(ctx, stop, 0)
		if len(res) != 2 || res[1].Err == nil {
			t.Fatalf("an error should stop the script: %+v", res)
		}
		if res, err := c.Execute(ctx, "SELECT * FROM gt_c", 2); err != nil || len(res[0].Set.Rows) != 2 || !res[0].Truncated {
			t.Fatal(res, err)
		}
	})

	t.Run("add column", func(t *testing.T) {
		if !caps.addCol {
			t.Skip("no ADD COLUMN here")
		}
		typ := caps.textType
		if typ == "" {
			typ = "VARCHAR(50)"
		}
		if _, err := c.AddColumn(ctx, ref, ColumnDef{Name: "note", Type: typ, Nullable: true}); err != nil {
			t.Fatal(err)
		}
		cols, err := c.Columns(ctx, ref)
		if err != nil || !slices.ContainsFunc(cols, func(c Column) bool { return c.Name == "note" }) {
			t.Fatal(cols, err)
		}
	})

	t.Run("csv import", func(t *testing.T) {
		if !caps.csv {
			t.Skip("no CSV import here")
		}
		n, err := c.ImportCSV(ctx, strings.NewReader("id,name,score\n20,Imp,1\n21,Ort,2\n"), ref,
			CSVImportOptions{Header: true, Columns: []string{"id", "name", "score"}, Mapping: []int{0, 1, 2}}, nil)
		if err != nil || n != 2 {
			t.Fatal(n, err)
		}
		settle()
		if got := names(Page{Filters: []Filter{{Column: "id", Op: OpGe, Value: "20"}}}); !slices.Equal(got, []string{"Imp", "Ort"}) {
			t.Fatal(got)
		}
	})

	t.Run("export and import", func(t *testing.T) {
		var buf bytes.Buffer
		if err := c.Dump(ctx, &buf, DumpOptions{Objects: []Object{{Schema: schema, Name: "gt_c", Kind: KindTable}}, Structure: true, Data: true}, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "gt_c") || !strings.Contains(buf.String(), "INSERT") {
			t.Fatal(buf.String())
		}
		if !caps.dump {
			return
		}
		want := names(Page{})
		dump := strings.ReplaceAll(buf.String(), "gt_c", "gt_copy")
		if _, err := c.ImportSQL(ctx, strings.NewReader(dump), ImportOptions{}, nil); err != nil {
			t.Fatalf("%v\n%s", err, dump)
		}
		if caps.after != "" {
			exec(strings.ReplaceAll(caps.after, "gt_c", "gt_copy"))
		}
		copyRef := TableRef{Schema: schema, Name: "gt_copy"}
		rs, err := c.FetchRows(ctx, copyRef, Page{Sort: []Sort{{Column: "id", Dir: Asc}}}, nil)
		if err != nil || len(rs.Rows) != len(want) {
			t.Fatalf("%+v %v", rs, err)
		}
	})

	t.Run("delete tables", func(t *testing.T) {
		objs := []Object{{Schema: schema, Name: "gt_c", Kind: KindTable}}
		if caps.views {
			objs = append([]Object{{Schema: schema, Name: "gt_cv", Kind: KindView}}, objs...)
		}
		if _, err := c.Drop(ctx, objs, false); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Columns(ctx, ref); err == nil {
			t.Fatal("gt_c is still there")
		}
	})
}

// compatRows are the suite's rows, as INSERT values.
const compatRows = `(1, 'Alice', 'Berlin', 90), (2, 'Bob', 'Paris', 70), (3, 'Carol', NULL, 85), (4, 'Dave', 'Berlin', 60), (5, 'Eve_x', 'Rome', 99)`
