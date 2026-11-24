package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"
)

func TestBigQuerySplit(t *testing.T) {
	x := &bigquery{}
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"SELECT 'a;\\'b'; SELECT \"c;\" # x; y\n; -- z;\nSELECT `a;b`", []string{`SELECT 'a;\'b'`, "SELECT \"c;\" # x; y", "-- z;\nSELECT `a;b`"}},
		{"SELECT '''a;'\n;'''; SELECT r'\\'; SELECT 2", []string{"SELECT '''a;'\n;'''", `SELECT r'\'; SELECT 2`}},
		{"SELECT 1 /* ; */; ;", []string{"SELECT 1 /* ; */"}},
		{"DECLARE x INT64; SELECT x;", []string{"DECLARE x INT64; SELECT x"}},
		{"SELECT 1; BEGIN TRANSACTION; COMMIT", []string{"SELECT 1; BEGIN TRANSACTION; COMMIT"}},
		{"CREATE TEMP TABLE a AS SELECT 1; SELECT * FROM a", []string{"CREATE TEMP TABLE a AS SELECT 1; SELECT * FROM a"}},
		{"CREATE TABLE a (x INT64); SELECT * FROM a", []string{"CREATE TABLE a (x INT64)", "SELECT * FROM a"}},
		{"BEGIN SELECT 1; END", []string{"BEGIN SELECT 1; END"}},
	} {
		got := []string{}
		for _, s := range x.split(c.in) {
			got = append(got, s.SQL)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: %q", c.in, got)
		}
	}
}

func TestBigQueryValues(t *testing.T) {
	st := bqField{Name: "s", Type: "RECORD", Mode: "REPEATED", Fields: []bqField{{Name: "k", Type: "INTEGER"}, {Name: "a b", Type: "FLOAT", Mode: "REPEATED"}, {Name: "n", Type: "NUMERIC"}, {Name: "j", Type: "JSON"}}}
	raw := json.RawMessage(`[{"v":{"f":[{"v":"9223372036854775807"},{"v":[{"v":"0.33333333333333331"},{"v":"NaN"}]},{"v":"1.50"},{"v":"{\"x\":12345678901234567890}"}]}}]`)
	if got := bqValue(st, raw).Text; got != `[{"k":9223372036854775807,"a b":[0.3333333333333333,"NaN"],"n":"1.50","j":{"x":12345678901234567890}}]` {
		t.Error(got)
	}
	if got := st.sqlType(); got != "ARRAY<STRUCT<k INT64, `a b` ARRAY<FLOAT64>, n NUMERIC, j JSON>>" {
		t.Error(got)
	}
	d := DialectFor(model.DriverBigQuery)
	for _, c := range []struct {
		f         bqField
		raw, text string
		lit       string
	}{
		{bqField{Type: "TIMESTAMP"}, `"1709168523123456"`, "2024-02-29 01:02:03.123456+00", "TIMESTAMP '2024-02-29 01:02:03.123456+00'"},
		{bqField{Type: "TIMESTAMP"}, `"-62135596800000000"`, "0001-01-01 00:00:00+00", "TIMESTAMP '0001-01-01 00:00:00+00'"},
		{bqField{Type: "DATETIME"}, `"2024-02-29T01:02:03"`, "2024-02-29 01:02:03", "DATETIME '2024-02-29 01:02:03'"},
		{bqField{Type: "BYTES"}, `"AP8="`, "0x00FF", "FROM_HEX('00ff')"},
		{bqField{Type: "STRING"}, `"it's \\ a\nb"`, "it's \\ a\nb", `'it\'s \\ a\nb'`},
		{bqField{Type: "STRING"}, `null`, "", "NULL"},
		{bqField{Type: "FLOAT"}, `"-Infinity"`, "-Infinity", "CAST('-Infinity' AS FLOAT64)"},
		{bqField{Type: "BIGNUMERIC"}, `"1.5"`, "1.5", "BIGNUMERIC '1.5'"},
	} {
		v := bqValue(c.f, json.RawMessage(c.raw))
		if v.Text != c.text || v.Null != (c.raw == "null") || bqLiteral(d, c.f, json.RawMessage(c.raw)) != c.lit {
			t.Errorf("%s %s: %+v %s", c.f.Type, c.raw, v, bqLiteral(d, c.f, json.RawMessage(c.raw)))
		}
	}
	for typ, ok := range map[string]bool{"STRING(10)": true, "ARRAY<STRUCT<a INT64, `b c` NUMERIC(10, 2)>>": true, "RANGE<DATE>": true, "INT64); DROP TABLE x": false, "A<B": false, "A'": false, "": false} {
		if bqTypeOK(typ) != ok {
			t.Errorf("type %q", typ)
		}
	}
}

func TestBigQuerySettings(t *testing.T) {
	for in, want := range map[string]int64{"": 10 << 30, "limit=0": 0, "limit=0.5": 1 << 29, "limit=x": -1, "limit=-1": -1} {
		n, err := bqLimit(in)
		if want < 0 && err == nil || want >= 0 && n != want {
			t.Errorf("%s: %d %v", in, n, err)
		}
	}
	for n, want := range map[int64]string{10: "10 bytes", 1536: "1.5 KB", 10 << 20: "10 MB", 10 << 30: "10 GB", 150 << 30: "150 GB"} {
		if got := bqSize(n); got != want {
			t.Errorf("%d: %s", n, got)
		}
	}
	x := &bigquery{limit: 1 << 30}
	err := x.explain(&bqError{Message: "Query exceeded limit for bytes billed: 1073741824. 5368709120 or higher required."})
	if !strings.Contains(err.Error(), "read 5 GB") || !strings.Contains(err.Error(), "limit of 1 GB") {
		t.Error(err)
	}
	dir := t.TempDir()
	for body, want := range map[string]string{"x": "JSON file", `{"type":"external_account"}`: "aren't supported", `{"type":"authorized_user","refresh_token":"r","quota_project_id":"p"}`: ""} {
		p := filepath.Join(dir, "k.json")
		os.WriteFile(p, []byte(body), 0o600)
		_, proj, err := bqCredentials(p)
		if want == "" && (err != nil || proj != "p") || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %s %v", body, proj, err)
		}
	}
}

// TestBigQuery runs on the project of the key file in
// GOTABLE_TEST_BIGQUERY_KEY, in datasets of its own (gt_suite…). The free
// tier is enough: rows come from CREATE TABLE AS and load jobs.
func TestBigQuery(t *testing.T) {
	key := os.Getenv("GOTABLE_TEST_BIGQUERY_KEY")
	if key == "" {
		t.Skip("GOTABLE_TEST_BIGQUERY_KEY not set")
	}
	cfg := model.NewConnection(model.DriverBigQuery)
	cfg.Name, cfg.KeyPath, cfg.Options = "bq", key, "limit=0.1"
	c := openTest(t, cfg, "")
	x := c.remote.(*bigquery)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	run := func(t *testing.T, script string) []Result {
		t.Helper()
		res, err := c.Execute(ctx, script, 0)
		if err != nil {
			t.Fatalf("%s: %v", script, err)
		}
		for _, r := range res {
			if r.Err != nil {
				t.Fatalf("%s: %v", r.Statement, r.Err)
			}
		}
		return res
	}
	const ds = "gt_suite"
	c.Execute(ctx, "DROP SCHEMA IF EXISTS gt_suite CASCADE", 0) // denied where it never was
	run(t, "CREATE SCHEMA gt_suite")
	defer c.Execute(context.Background(), "DROP SCHEMA IF EXISTS gt_suite CASCADE", 0)
	if err := c.UseSchema(ctx, ds); err != nil {
		t.Fatal(err)
	}
	ref := func(n string) TableRef { return TableRef{Schema: ds, Name: n} }

	t.Run("names", func(t *testing.T) {
		dbs, err := c.Databases(ctx)
		if err != nil || !slices.Contains(dbs, x.project) || c.CurrentDatabase(ctx) != x.project {
			t.Error(dbs, err)
		}
		sets, err := c.Schemas(ctx)
		if err != nil || !slices.Contains(sets, ds) {
			t.Error(sets, err)
		}
		if err := c.UseSchema(ctx, "gt_nope"); err == nil {
			t.Error("used a dataset that isn't there")
		}
		if _, err := c.Columns(ctx, ref("nope")); err == nil {
			t.Error("columns of a missing table")
		}
	})

	run(t, `CREATE TABLE allt AS SELECT 1 AS id, NUMERIC '1234567890123456789012345678.123456789' n, BIGNUMERIC '12345678901234567890123456789012345678.12345678901234567890123456789012345678' bn,
		1/3 f, TRUE b, 'it\'s \\ a\nb ✓ %_' s, b'\x00\xff' byt, DATE '2024-02-29' d, TIME '23:59:59.123456' t, DATETIME '2024-02-29 01:02:03.123456' dt,
		TIMESTAMP '2024-02-29 01:02:03.123456+00' ts, JSON '{"a":[1,null],"b":12345678901234567}' j, ST_GEOGPOINT(1,2) g, [1,2,3] arr, STRUCT(1 AS x, 'y' AS y) st,
		[STRUCT(1 AS k, [1.5] AS v)] ast, INTERVAL 3 DAY iv, RANGE(DATE '2024-01-01', DATE '2024-02-01') r, 9223372036854775807 big
		UNION ALL SELECT 2, NULL, NULL, CAST('nan' AS FLOAT64), NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, [], NULL, [], NULL, NULL, NULL`)

	t.Run("every type", func(t *testing.T) {
		rs, err := c.FetchRows(ctx, ref("allt"), Page{Limit: 10}, nil)
		if err != nil || len(rs.Rows) != 2 {
			t.Fatal(rs, err)
		}
		want := `[1 1234567890123456789012345678.123456789 12345678901234567890123456789012345678.12345678901234567890123456789012345678 0.3333333333333333 true it's \ a
b ✓ %_ 0x00FF 2024-02-29 23:59:59.123456 2024-02-29 01:02:03.123456 2024-02-29 01:02:03.123456+00 {"a":[1,null],"b":12345678901234567} POINT(1 2) [1,2,3] {"x":1,"y":"y"} [{"k":1,"v":[1.5]}] 0-0 3 0:0:0 [2024-01-01, 2024-02-01) 9223372036854775807]`
		find := func(rows [][]Value, id string) []Value {
			for _, r := range rows {
				if r[0].Text == id {
					return r
				}
			}
			return nil
		}
		if got := fmt.Sprint(find(rs.Rows, "1")); got != want {
			t.Errorf("\n%s\n%s", got, want)
		}
		// A query gives the same.
		q := run(t, "SELECT * FROM allt ORDER BY id")[0].Set
		if fmt.Sprint(q.Rows) != fmt.Sprint([][]Value{find(rs.Rows, "1"), find(rs.Rows, "2")}) || !slices.Equal(q.Types, rs.Types) {
			t.Errorf("\n%v\n%v", q.Rows, rs.Rows)
		}
		if r := find(rs.Rows, "2"); r[3].Text != "NaN" || !r[5].Null || r[13].Text != "[]" {
			t.Error(r)
		}
		cols, err := c.Columns(ctx, ref("allt"))
		if err != nil || cols[15].Type != "ARRAY<STRUCT<k INT64, v ARRAY<FLOAT64>>>" || cols[17].Type != "RANGE<DATE>" {
			t.Errorf("%+v %v", cols, err)
		}
	})

	t.Run("filters", func(t *testing.T) {
		for _, f := range []struct {
			f    Filter
			want string
		}{
			{Filter{Column: "id", Op: OpEq, Value: "1"}, "1"}, {Filter{Column: "n", Op: OpGt, Value: "5"}, "1"}, {Filter{Column: "f", Op: OpEq, Value: "0.3333333333333333"}, "1"},
			{Filter{Column: "s", Op: OpContains, Value: "IT'S \\"}, "1"}, {Filter{Column: "s", Op: OpEndsWith, Value: "%_"}, "1"}, {Filter{Column: "s", Op: OpContains, Value: "%x"}, ""},
			{Filter{Column: "b", Op: OpEq, Value: "true"}, "1"}, {Filter{Column: "d", Op: OpBetween, Value: "2024-01-01", Value2: "2024-12-31"}, "1"},
			{Filter{Column: "ts", Op: OpEq, Value: "2024-02-29 01:02:03.123456+00"}, "1"}, {Filter{Column: "dt", Op: OpEq, Value: "2024-02-29 01:02:03.123456"}, "1"},
			{Filter{Column: "byt", Op: OpEq, Value: "0x00FF"}, "1"}, {Filter{Column: "g", Op: OpEq, Value: "POINT(1 2)"}, "1"}, {Filter{Column: "arr", Op: OpContains, Value: "2,3"}, "1"},
			{Filter{Column: "id", Op: OpIn, Value: "1, 2"}, "1,2"}, {Filter{Column: "s", Op: OpIsNull}, "2"}, {Filter{Column: AnyColumn, Op: OpContains, Value: "12345678901234567"}, "1"},
			{Filter{Column: "j", Op: OpContains, Value: "null"}, "1"},
		} {
			p := Page{Filters: []Filter{f.f}, Sort: []Sort{{Column: "id", Dir: Asc}}, Limit: 50}
			rs, err := c.FetchRows(ctx, ref("allt"), p, nil)
			if err != nil {
				t.Errorf("%v: %v", f.f, err)
				continue
			}
			if got := strings.Join(names(rs, 0), ","); got != f.want {
				t.Errorf("%v: %s", f.f, got)
			}
			if c.CountIsCostly(ctx, ref("allt"), p) {
				t.Error("the count was read with the rows")
			}
			if n, err := c.CountRows(ctx, ref("allt"), p, nil); err != nil || int(n) != len(rs.Rows) {
				t.Errorf("%v: count %d %v", f.f, n, err)
			}
		}
		rs, err := c.FetchRows(ctx, ref("allt"), Page{RawWhere: "id = 2 -- last", Limit: 5}, nil)
		if err != nil || len(rs.Rows) != 1 {
			t.Error(rs, err)
		}
	})

	t.Run("pages", func(t *testing.T) {
		run(t, "CREATE TABLE many AS SELECT n AS id, CONCAT('row ', CAST(n AS STRING)) AS s FROM UNNEST(GENERATE_ARRAY(1, 25000)) AS n; CREATE VIEW manyv AS SELECT * FROM gt_suite.many")
		if res, _ := c.Execute(ctx, "CREATE VIEW badv AS SELECT * FROM many", 0); res[0].Err == nil {
			t.Error("made a view that can't be read")
		}
		rs, err := c.FetchRows(ctx, ref("many"), Page{Limit: 300, Offset: 24900}, nil)
		n, _ := c.CountRows(ctx, ref("many"), Page{Limit: 300, Offset: 24900}, nil)
		if err != nil || len(rs.Rows) != 100 || n != 25000 {
			t.Error(len(rs.Rows), n, err)
		}
		for _, name := range []string{"many", "manyv"} {
			p := Page{Sort: []Sort{{Column: "id", Dir: Desc}}, Limit: 3, Offset: 10}
			rs, err = c.FetchRows(ctx, ref(name), p, nil)
			n, _ = c.CountRows(ctx, ref(name), p, nil)
			if err != nil || strings.Join(names(rs, 0), ",") != "24990,24989,24988" || n != 25000 {
				t.Error(name, rs, n, err)
			}
			got := 0
			sum := map[string]bool{}
			if err := c.StreamRows(ctx, ref(name), Page{}, nil, func(_, _ []string, r []Value) error { got++; sum[r[0].Text] = true; return nil }); err != nil || got != 25000 || len(sum) != 25000 {
				t.Error(name, got, len(sum), err)
			}
		}
		if c.CountIsCostly(ctx, ref("many"), Page{}) {
			t.Error("a table's count is in its metadata")
		}
		if !c.CountIsCostly(ctx, ref("manyv"), Page{Filters: []Filter{{Column: "id", Op: OpEq, Value: "77"}}}) {
			t.Error("a view's count is a query")
		}
	})

	t.Run("limit", func(t *testing.T) {
		old := x.limit
		x.limit = 10 << 20
		defer func() { x.limit = old }()
		res, _ := c.Execute(ctx, "SELECT repo_name FROM `bigquery-public-data.github_repos.sample_commits`", 1)
		if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "nothing is billed") {
			t.Error(res[0].Err)
		}
		t.Log(res[0].Err)
		n, err := c.Estimate(ctx, ds, "SELECT s FROM many")
		if err != nil || n < 100_000 || n > 1_000_000 {
			t.Error(n, err)
		}
		if _, err := c.Estimate(ctx, ds, "SELEC"); err == nil {
			t.Error("estimated nonsense")
		}
	})

	t.Run("editor", func(t *testing.T) {
		res := run(t, "DECLARE x INT64 DEFAULT 3; CREATE TEMP TABLE z AS SELECT x AS a; SELECT a FROM z; SELECT 'it\\'s; x' AS b, 2 AS b")
		var sets []string
		for _, r := range res {
			if r.Set != nil {
				sets = append(sets, fmt.Sprint(r.Set.Columns, r.Set.Rows))
			}
		}
		if !slices.Equal(sets, []string{"[a] [[3]]", "[b b_1] [[it's; x 2]]"}) {
			t.Error(sets, len(res))
		}
		res, _ = c.Execute(ctx, "SELECT 1; SELEC 2; SELECT 3", 0)
		if len(res) != 2 || res[0].Set == nil || res[1].Err == nil {
			t.Errorf("%+v", res)
		}
		res, _ = c.Execute(ctx, "DECLARE y INT64; SELECT 1/0", 0)
		if len(res) == 0 || res[len(res)-1].Err == nil {
			t.Errorf("%+v", res)
		}
		res, _ = c.Execute(ctx, "SELECT * FROM many", 100)
		if len(res[0].Set.Rows) != 100 || !res[0].Truncated {
			t.Error(len(res[0].Set.Rows), res[0].Truncated)
		}
		if res = run(t, "SELECT * FROM many"); len(res[0].Set.Rows) != 25000 {
			t.Error(len(res[0].Set.Rows))
		}
		res, _ = c.Execute(ctx, "DELETE FROM many WHERE id = 1", 0)
		t.Log("DML:", res[0].Err)
		if err := c.ApplyChanges(ctx, ref("many"), []Change{{Kind: ChangeDelete, Key: map[string]Value{"id": Text("1")}}}, true); err == nil {
			t.Error("edited")
		}
	})

	t.Run("cancel", func(t *testing.T) {
		cctx, stop := context.WithTimeout(ctx, 4*time.Second)
		defer stop()
		start := time.Now()
		res, err := c.Execute(cctx, "SELECT COUNT(*) FROM UNNEST(GENERATE_ARRAY(1, 1000000)) a, UNNEST(GENERATE_ARRAY(1, 1000000)) b WHERE MOD(a * b, 7) = 3", 0)
		if err == nil && res[0].Err == nil || time.Since(start) > 20*time.Second {
			t.Error(err, res, time.Since(start))
		}
		run(t, "SELECT 1")
		b := new(bytes.Buffer)
		for range 15 {
			time.Sleep(time.Second)
			q, _ := x.http.Get(x.url("/jobs", nil) + "?stateFilter=running&maxResults=5")
			b.Reset()
			b.ReadFrom(q.Body)
			if !strings.Contains(b.String(), "jobReference") {
				t.Logf("stopped %v after the start", time.Since(start).Round(time.Second))
				return
			}
		}
		t.Errorf("still running: %s", b)
	})

	t.Run("design", func(t *testing.T) {
		nt := NewTable{Name: "made", Comment: "it's \\ c", Columns: []NewColumn{
			{ColumnDef: ColumnDef{Name: "id", Type: "INT64"}, PrimaryKey: true},
			{ColumnDef: ColumnDef{Name: "s", Type: "STRING(20)", Nullable: true, DefaultKind: DefaultValue, Default: "x y", Comment: "c'm"}},
			{ColumnDef: ColumnDef{Name: "n", Type: "NUMERIC(10, 2)", Nullable: true, DefaultKind: DefaultValue, Default: "1.5"}},
			{ColumnDef: ColumnDef{Name: "at", Type: "TIMESTAMP", Nullable: true, DefaultKind: DefaultExpression, Default: "CURRENT_TIMESTAMP()"}},
			{ColumnDef: ColumnDef{Name: "ok", Type: "BOOL", Nullable: true, DefaultKind: DefaultValue, Default: "true"}},
			{ColumnDef: ColumnDef{Name: "d", Type: "DATE", Nullable: true, DefaultKind: DefaultValue, Default: "2024-01-02"}},
			{ColumnDef: ColumnDef{Name: "tags", Type: "ARRAY<STRING>"}},
			{ColumnDef: ColumnDef{Name: "st", Type: "STRUCT<a INT64, b STRING>", Nullable: true}}}}
		if _, err := c.CreateTable(ctx, ds, nt); err != nil {
			t.Fatal(err)
		}
		cols, err := c.Columns(ctx, ref("made"))
		if err != nil || !cols[0].PrimaryKey || cols[0].Nullable || cols[1].Type != "STRING(20)" || cols[1].DefaultText != "x y" || cols[1].Comment != "c'm" || cols[2].Type != "NUMERIC(10, 2)" || cols[2].DefaultText != "1.5" || cols[4].DefaultText != "true" {
			t.Errorf("%+v %v", cols, err)
		}
		for _, col := range cols { // saved unchanged, nothing changes
			def := ColumnDef{Name: col.Name, Type: col.Type, Nullable: col.Nullable, DefaultKind: col.DefaultKind, Default: col.DefaultText, Comment: col.Comment}
			if st, err := c.Alter(ctx, ref("made"), []AlterOp{{Kind: AlterModifyColumn, Column: col.Name, Def: &def}}, true); err == nil {
				t.Errorf("%s: %v", col.Name, st)
			}
		}
		run(t, "CREATE TABLE par (id INT64, PRIMARY KEY (id) NOT ENFORCED)")
		if st, err := c.Alter(ctx, ref("made"), []AlterOp{
			{Kind: AlterAddColumn, Def: &ColumnDef{Name: "extra", Type: "INT64", Nullable: true, DefaultKind: DefaultValue, Default: "7", Comment: "x"}},
			{Kind: AlterModifyColumn, Column: "n", Def: &ColumnDef{Name: "n2", Type: "NUMERIC(12, 2)", Nullable: true, DefaultKind: DefaultNone, Comment: "y"}},
			{Kind: AlterDropColumn, Column: "ok"},
			{Kind: AlterAddForeignKey, ForeignKey: &ForeignKeyDef{Name: "fk1", Columns: []string{"id"}, RefTable: "par", RefColumns: []string{"id"}}},
			{Kind: AlterTableComment, Comment: "n'ew"}}, false); err != nil {
			t.Fatal(st, err)
		}
		cols, _ = c.Columns(ctx, ref("made"))
		ti, _ := c.TableInfo(ctx, ref("made"))
		fks, _ := c.ForeignKeys(ctx, ref("made"))
		if len(cols) != 8 || cols[7].Name != "extra" && cols[6].Name != "extra" || ti.Comment != "n'ew" || len(fks) != 1 || fks[0].Table != "par" || fks[0].Name != "fk1" {
			t.Errorf("%+v %+v %+v", cols, ti, fks)
		}
		// par stays while made points at it.
		if _, err := c.Drop(ctx, []Object{{Schema: ds, Name: "par", Kind: KindTable}}, false); err == nil || !strings.Contains(err.Error(), "made has a foreign key") {
			t.Error(err)
		}
		if _, err := c.Alter(ctx, ref("made"), []AlterOp{{Kind: AlterDropForeignKey, Name: "fk1"}, {Kind: AlterSetPrimaryKey}, {Kind: AlterRenameTable, NewName: "made2"}}, false); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Alter(ctx, ref("made2"), []AlterOp{{Kind: AlterSetPrimaryKey, Columns: []string{"id"}}}, false); err != nil {
			t.Error(err)
		}
		if _, err := c.Drop(ctx, []Object{{Schema: ds, Name: "par", Kind: KindTable}}, false); err != nil {
			t.Error(err)
		}
		for _, op := range []AlterOp{{Kind: AlterTruncate}, {Kind: AlterAddCheck, Check: &CheckDef{Expression: "id > 0"}},
			{Kind: AlterAddColumn, Def: &ColumnDef{Name: "q", Type: "INT64"}}, {Kind: AlterAddIndex, Index: &IndexDef{Columns: []string{"id"}}},
			{Kind: AlterAddForeignKey, ForeignKey: &ForeignKeyDef{Columns: []string{"id"}, RefTable: "x", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}} {
			if _, err := c.Alter(ctx, ref("made2"), []AlterOp{op}, true); err == nil {
				t.Errorf("%s was accepted", op.Kind)
			}
		}
	})

	t.Run("csv", func(t *testing.T) {
		run(t, "CREATE TABLE csvt (id INT64 NOT NULL, d DATE, ts TIMESTAMP, j JSON, arr ARRAY<INT64>, b BYTES, s STRING, n NUMERIC DEFAULT 5)")
		var b strings.Builder
		b.WriteString("id,d,ts,j,arr,b,s\n1,2024-02-29,2024-02-29 01:02:03.5+00,\"{\"\"a\"\":1}\",\"[1,2]\",0x00FF,\"it's \\ x\"\n2,,,,,,\n")
		for i := 3; i <= 20000; i++ {
			fmt.Fprintf(&b, "%d,2024-01-01,2024-01-01 00:00:00,%d,[%d],0xAB,row %d\n", i, i, i, i)
		}
		o := CSVImportOptions{Delimiter: ',', Header: true, Columns: []string{"id", "d", "ts", "j", "arr", "b", "s"}, Mapping: []int{0, 1, 2, 3, 4, 5, 6}, EmptyAsNull: true}
		begin := time.Now()
		n, err := c.ImportCSV(ctx, strings.NewReader(b.String()), ref("csvt"), o, nil)
		if err != nil || n != 20000 {
			t.Fatal(n, err)
		}
		t.Logf("20,000 rows in %v", time.Since(begin).Round(100*time.Millisecond))
		rs, err := c.FetchRows(ctx, ref("csvt"), Page{Sort: []Sort{{Column: "id", Dir: Asc}}, Limit: 2}, nil)
		if err != nil || fmt.Sprint(rs.Rows[0]) != `[1 2024-02-29 2024-02-29 01:02:03.5+00 {"a":1} [1,2] 0x00FF it's \ x 5]` || !rs.Rows[1][6].Null || rs.Rows[1][4].Text != "[]" {
			t.Error(rs, err)
		}
		for _, bad := range []string{"id,d,ts,j,arr,b,s\n5,2024-01-01,,,,,\n6,nonsense,,,,,\n", "id,d,ts,j,arr,b,s\n,,,,,,x\n", "id,d,ts,j,arr,b,s\n7,,,{,,,x\n", "id,d,ts,j,arr,b,s\n7,,,,,0xZZ,x\n"} {
			for _, tr := range []bool{false, true} {
				o.Truncate = tr
				if _, err := c.ImportCSV(ctx, strings.NewReader(bad), ref("csvt"), o, nil); err == nil {
					t.Errorf("%q went in", bad)
				} else {
					t.Log(err)
				}
			}
		}
		if n, _ := c.CountRows(ctx, ref("csvt"), Page{}, nil); n != 20000 {
			t.Errorf("%d rows after failed imports", n)
		}
		o.Truncate = true
		if n, err := c.ImportCSV(ctx, strings.NewReader("id,d,ts,j,arr,b,s\n9,,,,,,only\n"), ref("csvt"), o, nil); err != nil || n != 1 {
			t.Error(n, err)
		}
		cols, _ := c.Columns(ctx, ref("csvt"))
		if n, _ := c.CountRows(ctx, ref("csvt"), Page{}, nil); n != 1 || cols[0].Nullable || cols[7].DefaultText != "5" {
			t.Errorf("%d rows, %+v", n, cols)
		}
	})

	t.Run("dump", func(t *testing.T) {
		run(t, "CREATE TABLE part (id INT64, d DATE, PRIMARY KEY (id) NOT ENFORCED) PARTITION BY DATE_TRUNC(d, MONTH) CLUSTER BY id OPTIONS(description=\"it's\")")
		run(t, "CREATE TABLE kid (id INT64, p INT64, CONSTRAINT kfk FOREIGN KEY (p) REFERENCES part (id) NOT ENFORCED)")
		run(t, "CREATE TABLE rng (id INT64) PARTITION BY RANGE_BUCKET(id, GENERATE_ARRAY(0, 100, 10))")
		objs, err := c.Objects(ctx, ds)
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]ObjectKind{}
		for _, o := range objs {
			kinds[o.Name] = o.Kind
		}
		if kinds["manyv"] != KindView || kinds["part"] != KindTable {
			t.Error(kinds)
		}
		objs = slices.DeleteFunc(objs, func(o Object) bool { return o.Name == "many" || o.Name == "manyv" })
		var buf bytes.Buffer
		if err := c.Dump(ctx, &buf, DumpOptions{Objects: objs, Structure: true, Data: true, DropFirst: true}, nil); err != nil {
			t.Fatal(err)
		}
		c.Execute(ctx, "DROP SCHEMA IF EXISTS gt_suite2 CASCADE", 0)
		run(t, "CREATE SCHEMA gt_suite2")
		defer c.Execute(context.Background(), "DROP SCHEMA IF EXISTS gt_suite2 CASCADE", 0)
		c.UseSchema(ctx, "gt_suite2")
		defer c.UseSchema(ctx, ds)
		// The free tier runs no INSERT: the structure is made, and each
		// INSERT's rows are read back as a query.
		inserts := 0
		for _, s := range bqSplit(buf.String()) {
			st := stripLeadingComments(s.SQL)
			if head, rows, ok := strings.Cut(st, " VALUES\n  "); ok && strings.HasPrefix(st, "INSERT") {
				inserts++
				if _, err := c.Estimate(ctx, "gt_suite2", st); err != nil && !strings.Contains(err.Error(), "billing") {
					t.Errorf("%s: %v", head, err)
				}
				for _, n := range []string{"allt", "csvt", "made2"} { // the rows read back are the same
					if !strings.Contains(head, "`"+n+"`") {
						continue
					}
					bt, _ := x.table(ctx, ref(n))
					st := bqField{Type: "RECORD", Fields: bt.Schema.Fields}
					got := run(t, "SELECT * FROM UNNEST(ARRAY<"+st.sqlType()+">["+rows+"]) ORDER BY id")[0].Set
					want := run(t, "SELECT * FROM gt_suite."+n+" ORDER BY id")[0].Set
					if fmt.Sprint(got) != fmt.Sprint(want) {
						t.Errorf("%s:\n%v\n%v", n, got, want)
					}
				}
				continue
			}
			run(t, st)
		}
		if inserts < 2 {
			t.Error(inserts, "inserts")
		}
		for _, n := range []string{"part", "kid", "rng", "made2", "allt"} {
			a, err1 := c.Definition(ctx, Object{Schema: ds, Name: n, Kind: KindTable})
			b, err2 := c.Definition(ctx, Object{Schema: "gt_suite2", Name: n, Kind: KindTable})
			if err1 != nil || err2 != nil || a != strings.ReplaceAll(b, "gt_suite2", ds) {
				t.Errorf("%s:\n%s\n%s\n%v %v", n, a, b, err1, err2)
			}
		}
		if def, _ := c.Definition(ctx, Object{Schema: ds, Name: "kid", Kind: KindTable}); !strings.Contains(def, "CONSTRAINT `kfk` FOREIGN KEY (`p`) REFERENCES `gt_suite`.`part` (`id`)") {
			t.Error(def)
		}
	})
}
