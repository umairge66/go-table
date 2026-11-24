package db

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gotable/internal/model"
)

func TestInfluxSplit(t *testing.T) {
	x := &influx{}
	script := "SELECT 1; -- c;\nWRITE\ncpu,host=it's\\ \"5\" v=1,s=\"a;\n\nb \\\" WRITE\n\" 1\n# note; x\ncpu v=2 2\n\nSELECT 'WRITE';\nwrite\nm v=1i 1"
	var got []string
	for _, sp := range x.split(script) {
		if !strings.Contains(script[sp.Start:sp.End], sp.SQL) {
			t.Errorf("%+v", sp)
		}
		got = append(got, sp.SQL)
	}
	want := []string{"SELECT 1", "WRITE\ncpu,host=it's\\ \"5\" v=1,s=\"a;\n\nb \\\" WRITE\n\" 1\n# note; x\ncpu v=2 2", "SELECT 'WRITE'", "write\nm v=1i 1"}
	// The comment after SELECT 1 may come as a statement of its own.
	got = slices.DeleteFunc(got, func(s string) bool { return strings.TrimSpace(stripLeadingComments(s)) == "" })
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestInfluxTime(t *testing.T) {
	for in, want := range map[string]int64{"2023-11-14T22:13:20.123456789": 1700000000123456789, "2023-11-14 22:13:20.123456789Z": 1700000000123456789, "2023-11-14T23:13:20+01:00": 1700000000000000000,
		"1700000000123456789": 1700000000123456789, "2023-11-14": 1699920000000000000, "1970-01-01T00:00:00.000000001": 1} {
		if got, err := influxTime(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "soon", "2024", "9999-01-01", "0001-01-01"} {
		if n, err := influxTime(in); err == nil {
			t.Errorf("%q → %d", in, n)
		}
	}
}

func TestInfluxOtherServers(t *testing.T) {
	version, status := "", 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if version != "" {
			w.Header().Set("X-Influxdb-Version", version)
		}
		if r.URL.Path != "/ping" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"the request was not authenticated"}`)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	cfg := model.NewConnection(model.DriverInfluxDB)
	cfg.Name, cfg.Host, cfg.DBName = "x", u.Hostname(), "db"
	cfg.Port, _ = strconv.Atoi(u.Port())
	for _, tc := range []struct {
		version string
		status  int
		want    string
	}{{"v2.7.10", 204, "InfluxDB 2.7.10"}, {"1.8.10", 204, "works with InfluxDB 3"}, {"1.8.10", 401, "works with InfluxDB 3"}, {"", 200, "isn't InfluxDB's"}, {"3.12.3", 200, "authentication failed"}} {
		version, status = tc.version, tc.status
		if _, err := Open(context.Background(), cfg, Credentials{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestInfluxDB(t *testing.T) {
	cfg, pw := serverConfig(t, "INFLUX", model.DriverInfluxDB)
	c := openTest(t, cfg, pw)
	ctx := context.Background()
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
	schema := c.Schema()
	for _, n := range []string{"gt_cpu", "gt_odd"} {
		c.Execute(ctx, "DROP TABLE IF EXISTS "+n, 0)
	}
	exec("WRITE\n" + `gt_cpu,host=a\ b,region=eu usage=1.5,n=9007199254740993i,u=18446744073709551615u,ok=true,note="hé \"q\" x" 1700000000123456789
gt_cpu,host=b usage=2 1700000000123456790
gt_cpu,host=c,region=us usage=3,n=7i 1700000000123456791
gt_cpu,host=d usage=4,note="" 1700000001000000000
`)
	ref := TableRef{Schema: schema, Name: "gt_cpu"}
	hosts := func(p Page) string {
		t.Helper()
		rs, err := c.FetchRows(ctx, ref, p, nil)
		if err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
		i := slices.Index(rs.Columns, "host")
		var out []string
		for _, r := range rs.Rows {
			out = append(out, r[i].Text)
		}
		return strings.Join(out, ",")
	}
	one := func(host, col string) Value {
		t.Helper()
		rs, err := c.FetchRows(ctx, ref, Page{Filters: []Filter{{Column: "host", Op: OpEq, Value: host}}}, nil)
		if err != nil || len(rs.Rows) != 1 {
			t.Fatalf("%s: %+v %v", host, rs, err)
		}
		return rs.Rows[0][slices.Index(rs.Columns, col)]
	}

	t.Run("introspection", func(t *testing.T) {
		if v, err := c.ServerVersion(ctx); err != nil || !strings.HasPrefix(v, "InfluxDB 3.") {
			t.Error(v, err)
		}
		if s, err := c.Schemas(ctx); err != nil || !slices.Contains(s, schema) || slices.Contains(s, "_internal") {
			t.Error(s, err)
		}
		objs, err := c.Objects(ctx, schema)
		if err != nil || !slices.Contains(objs, Object{Schema: schema, Name: "gt_cpu", Kind: KindTable}) {
			t.Error(objs, err)
		}
		cols, err := c.Columns(ctx, ref)
		var got []string
		for _, col := range cols {
			got = append(got, fmt.Sprint(col.Name, " ", col.Type, " ", col.PrimaryKey))
		}
		if want := []string{"time timestamp true", "host tag true", "region tag true", "n int64 false", "note utf8 false", "ok boolean false", "u uint64 false", "usage float64 false"}; err != nil || !slices.Equal(got, want) {
			t.Error(got, err)
		}
		if _, err := c.Columns(ctx, TableRef{Schema: schema, Name: "nope"}); err == nil {
			t.Error("no such table")
		}
		if def, err := c.Definition(ctx, Object{Schema: schema, Name: "gt_cpu", Kind: KindTable}); err != nil || !strings.Contains(def, `"host" TAG`) {
			t.Error(def, err)
		}
		if err := c.UseSchema(ctx, "gt_no_such_db"); err == nil {
			t.Error("no such database")
		}
	})

	t.Run("values", func(t *testing.T) {
		for col, want := range map[string]string{"time": "2023-11-14T22:13:20.123456789", "n": "9007199254740993", "u": "18446744073709551615", "ok": "true", "note": `hé "q" x`, "usage": "1.5", "region": "eu"} {
			if v := one("a b", col); v.Text != want || v.Null {
				t.Errorf("%s: %+v", col, v)
			}
		}
		if v := one("b", "n"); !v.Null {
			t.Error(v)
		}
		if v := one("d", "note"); v.Null || v.Text != "" {
			t.Error(v)
		}
	})

	t.Run("sorting, paging, filters", func(t *testing.T) {
		if got := hosts(Page{}); got != "d,c,b,a b" { // the newest first
			t.Error(got)
		}
		if got := hosts(Page{Sort: []Sort{{Column: "usage", Dir: Asc}}, Limit: 2, Offset: 1}); got != "b,c" {
			t.Error(got)
		}
		for _, tc := range []struct {
			f    Filter
			want string
		}{
			{Filter{Column: "usage", Op: OpGt, Value: "2"}, "c,d"},
			{Filter{Column: "usage", Op: OpBetween, Value: "2", Value2: "3"}, "b,c"},
			{Filter{Column: "usage", Op: OpIn, Value: "1.5, 4"}, "a b,d"},
			{Filter{Column: "usage", Op: OpNotIn, Value: "1.5, 4"}, "b,c"},
			{Filter{Column: "n", Op: OpEq, Value: "9007199254740993"}, "a b"},
			{Filter{Column: "n", Op: OpEq, Value: "9007199254740992"}, ""},
			{Filter{Column: "u", Op: OpEq, Value: "18446744073709551615"}, "a b"},
			{Filter{Column: "region", Op: OpIsNull}, "b,d"},
			{Filter{Column: "region", Op: OpNe, Value: "eu"}, "c"},
			{Filter{Column: "ok", Op: OpEq, Value: "true"}, "a b"},
			{Filter{Column: "host", Op: OpContains, Value: "A B"}, "a b"},
			{Filter{Column: "host", Op: OpNotContain, Value: "b"}, "c,d"},
			{Filter{Column: "host", Op: OpBeginsWith, Value: "A"}, "a b"},
			{Filter{Column: "note", Op: OpEndsWith, Value: "X"}, "a b"},
			{Filter{Column: "note", Op: OpContains, Value: `"q"`}, "a b"},
			{Filter{Column: "host", Op: OpLike, Value: "a_b"}, "a b"},
			{Filter{Column: "time", Op: OpEq, Value: "2023-11-14T22:13:20.123456790"}, "b"},
			{Filter{Column: "time", Op: OpGe, Value: "2023-11-14 22:13:21"}, "d"},
			{Filter{Column: "time", Op: OpLt, Value: "1700000000123456790"}, "a b"},
			{Filter{Column: AnyColumn, Op: OpContains, Value: "HÉ"}, "a b"},
			{Filter{Column: AnyColumn, Op: OpEq, Value: "7"}, "c"},
			{Filter{Column: AnyColumn, Op: OpEq, Value: "us"}, "c"},
			{Filter{Column: "host", Op: OpEq, Value: "it's"}, ""},
		} {
			if got := hosts(Page{Filters: []Filter{tc.f}, Sort: []Sort{{Column: "host", Dir: Asc}}}); got != tc.want {
				t.Errorf("%+v: %q, want %q", tc.f, got, tc.want)
			}
			n, err := c.CountRows(ctx, ref, Page{Filters: []Filter{tc.f}}, nil)
			if want := len(strings.FieldsFunc(tc.want, func(r rune) bool { return r == ',' })); err != nil || int(n) != want {
				t.Errorf("%+v: count %d %v", tc.f, n, err)
			}
		}
		if got := hosts(Page{RawWhere: "usage > 2 OR host = 'b' -- c", Filters: []Filter{{Column: "usage", Op: OpLt, Value: "4"}}, Sort: []Sort{{Column: "host", Dir: Asc}}}); got != "b,c" {
			t.Error(got)
		}
		for _, f := range []Filter{{Column: "usage", Op: OpEq, Value: "1; DROP"}, {Column: "ok", Op: OpEq, Value: "maybe"}, {Column: "time", Op: OpGt, Value: "later"}, {Column: "nope", Op: OpEq, Value: "1"}} {
			if _, err := c.FetchRows(ctx, ref, Page{Filters: []Filter{f}}, nil); err == nil {
				t.Errorf("%+v worked", f)
			}
		}
	})

	key := func(host, region, tm string) map[string]Value {
		r := Text(region)
		if region == "" {
			r = NullValue
		}
		return map[string]Value{"time": Text(tm), "host": Text(host), "region": r}
	}
	t.Run("editing", func(t *testing.T) {
		changes := []Change{
			{Kind: ChangeUpdate, Key: key("a b", "eu", "2023-11-14T22:13:20.123456789"), Values: map[string]Value{"usage": Text("9.25"), "note": Text("new \"one\" \\ back\nline2")}},
			{Kind: ChangeInsert, Values: map[string]Value{"host": Text("e,=x y"), "usage": Text("5"), "ok": Text("false")}},
			{Kind: ChangeInsert, Values: map[string]Value{"time": Text("2024-01-02T03:04:05.000000006Z"), "host": Text("f"), "n": Text("-3")}},
		}
		if p, err := c.PreviewChanges(ctx, ref, changes, true); err != nil || len(p) != 3 || !strings.HasPrefix(p[0], `WRITE gt_cpu,host=a\ b,region=eu `) {
			t.Fatal(p, err)
		}
		if err := c.ApplyChanges(ctx, ref, changes, true); err != nil {
			t.Fatal(err)
		}
		for col, want := range map[string]string{"usage": "9.25", "note": "new \"one\" \\ back\nline2", "n": "9007199254740993", "u": "18446744073709551615", "ok": "true", "time": "2023-11-14T22:13:20.123456789"} {
			if v := one("a b", col); v.Text != want {
				t.Errorf("%s: %q", col, v.Text)
			}
		}
		if v := one("e,=x y", "usage"); v.Text != "5.0" && v.Text != "5" {
			t.Error(v)
		}
		if v := one("f", "time"); v.Text != "2024-01-02T03:04:05.000000006" {
			t.Error(v)
		}
		if n, _ := c.CountRows(ctx, ref, Page{}, nil); n != 6 {
			t.Error(n)
		}
		for _, bad := range []struct {
			ch   Change
			want string
		}{
			{Change{Kind: ChangeDelete, Key: key("b", "", "2023-11-14T22:13:20.123456790")}, "can't delete"},
			{Change{Kind: ChangeUpdate, Key: key("b", "", "2023-11-14T22:13:20.123456790"), Values: map[string]Value{"usage": NullValue}}, "NULL"},
			{Change{Kind: ChangeUpdate, Key: key("b", "", "2023-11-14T22:13:20.123456790"), Values: map[string]Value{"host": Text("z")}}, "key"},
			{Change{Kind: ChangeUpdate, Key: key("b", "", "2023-11-14T22:13:20.123456790"), Values: map[string]Value{"n": Text("1.5")}}, "whole number"},
			{Change{Kind: ChangeUpdate, Key: key("b", "eu", "2023-11-14T22:13:20.123456790"), Values: map[string]Value{"n": Text("1")}}, errMongoRowGone.Error()},
			{Change{Kind: ChangeUpdate, Key: key("b", "", "2023-11-14T22:13:20.123456799"), Values: map[string]Value{"n": Text("1")}}, errMongoRowGone.Error()},
			{Change{Kind: ChangeInsert, Values: map[string]Value{"time": Text("2023-11-14T22:13:20.123456790"), "host": Text("b"), "usage": Text("0")}}, "would be replaced"},
			{Change{Kind: ChangeInsert, Values: map[string]Value{"host": Text("b")}}, "at least one field"},
			{Change{Kind: ChangeInsert, Values: map[string]Value{"host": Text("a\nb"), "usage": Text("1")}}, "line protocol"},
		} {
			if err := c.ApplyChanges(ctx, ref, []Change{bad.ch}, true); err == nil || !strings.Contains(err.Error(), bad.want) {
				t.Errorf("%+v: %v", bad.ch, err)
			}
		}
		if v := one("b", "usage"); v.Text != "2.0" && v.Text != "2" {
			t.Error(v)
		}
	})

	t.Run("editor", func(t *testing.T) {
		r := exec("SELECT host, region, CAST(NULL AS BIGINT) AS none FROM gt_cpu WHERE host = 'b'")
		if !slices.Equal(r.Set.Columns, []string{"host", "region", "none"}) || len(r.Set.Rows) != 1 || !r.Set.Rows[0][2].Null || r.Duration <= 0 {
			t.Errorf("%+v", r.Set)
		}
		if r := exec("SELECT host FROM gt_cpu WHERE false"); !slices.Equal(r.Set.Columns, []string{"host"}) || len(r.Set.Rows) != 0 {
			t.Errorf("%+v", r.Set)
		}
		if r := exec("-- c\nWITH x AS (SELECT 1 AS a) SELECT * FROM x;"); r.Set.Rows[0][0].Text != "1" {
			t.Errorf("%+v", r.Set)
		}
		if r := exec("SHOW TABLES"); len(r.Set.Rows) == 0 {
			t.Error("no tables")
		}
		if r := exec("EXPLAIN SELECT * FROM gt_cpu"); len(r.Set.Rows) == 0 {
			t.Error("no plan")
		}
		res, err := c.Execute(ctx, "SELECT * FROM gt_cpu", 2)
		if err != nil || len(res[0].Set.Rows) != 2 || !res[0].Truncated {
			t.Errorf("%+v %v", res, err)
		}
		if r := exec("WRITE\n# note\ngt_odd v=1 1\ngt_odd v=2 2\n\n"); r.RowsAffected != 2 {
			t.Errorf("%+v", r)
		}
		res, _ = c.Execute(ctx, "WRITE\ngt_odd v=3 3\ngt_odd v=\"x\" 4\n", 0)
		if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "line 2") {
			t.Errorf("%+v", res)
		}
		if r := exec("SELECT count(*) AS n FROM gt_odd"); r.Set.Rows[0][0].Text != "2" { // all or none
			t.Errorf("%+v", r.Set)
		}
		for _, q := range []string{"DELETE FROM gt_odd", "SELECT * FROM gt_nope", "SELEKT 1", "CREATE TABLE gt_bad (a NUMBER)", "DROP TABLE gt_nope"} {
			if res, err := c.Execute(ctx, q, 0); err == nil && res[0].Err == nil {
				t.Errorf("%s worked", q)
			}
		}
		res, _ = c.Execute(ctx, "SELECT 1;\nSELECT * FROM gt_nope;\nSELECT 2", 0)
		if len(res) != 2 || res[1].Err == nil {
			t.Errorf("an error should stop the script: %+v", res)
		}
		exec("DROP TABLE gt_odd")
		objs, _ := c.Objects(ctx, schema)
		if slices.ContainsFunc(objs, func(o Object) bool { return strings.HasPrefix(o.Name, "gt_odd") }) {
			t.Error("a deleted table is listed:", objs)
		}
		exec(`CREATE TABLE "gt_odd" ("a, b" TAG, temp FLOAT64, n INT64, u UINT64, s UTF8, b BOOLEAN); CREATE TABLE IF NOT EXISTS gt_odd (x TAG, y INT64);`)
		cols, err := c.Columns(ctx, TableRef{Schema: schema, Name: "gt_odd"})
		if err != nil || len(cols) != 7 || cols[1].Name != "a, b" || cols[1].Type != influxTag {
			t.Error(cols, err)
		}
		exec("CREATE DATABASE IF NOT EXISTS gt_made_db; CREATE DATABASE IF NOT EXISTS gt_made_db")
		if s, _ := c.Schemas(ctx); !slices.Contains(s, "gt_made_db") {
			t.Error(s)
		}
		if objs, err := c.Objects(ctx, "gt_made_db"); err != nil || len(objs) != 0 {
			t.Error(objs, err)
		}
		exec("DROP DATABASE gt_made_db")
	})

	t.Run("csv import", func(t *testing.T) {
		n, err := c.ImportCSV(ctx, strings.NewReader("t,h,u\n2024-05-01T00:00:00Z,csv1,1\n2024-05-01T00:00:01Z,csv2,2\n"), ref,
			CSVImportOptions{Header: true, Columns: []string{"time", "host", "usage"}, Mapping: []int{0, 1, 2}}, nil)
		if err != nil || n != 2 {
			t.Fatal(n, err)
		}
		if got := hosts(Page{Filters: []Filter{{Column: "host", Op: OpBeginsWith, Value: "csv"}}}); got != "csv2,csv1" {
			t.Error(got)
		}
		for _, bad := range []struct {
			csv  string
			o    CSVImportOptions
			want string
		}{
			{"h,u\na,1\n", CSVImportOptions{Header: true, Columns: []string{"host", "usage"}, Mapping: []int{0, 1}}, "time"},
			{"t,u\n2024-01-01,1\nx,2\n", CSVImportOptions{Header: true, Columns: []string{"time", "usage"}, Mapping: []int{0, 1}}, "line 3"},
			{"t,u\n2024-01-01,lots\n", CSVImportOptions{Header: true, Columns: []string{"time", "usage"}, Mapping: []int{0, 1}}, "isn't a number"},
			{"t,u\n2024-01-01,1\n", CSVImportOptions{Header: true, Truncate: true, Columns: []string{"time", "usage"}, Mapping: []int{0, 1}}, "can't delete"},
		} {
			if _, err := c.ImportCSV(ctx, strings.NewReader(bad.csv), ref, bad.o, nil); err == nil || !strings.Contains(err.Error(), bad.want) {
				t.Errorf("%q: %v", bad.csv, err)
			}
		}
	})

	t.Run("export and import", func(t *testing.T) {
		all := func() (out []string) {
			if err := c.StreamRows(ctx, ref, Page{}, nil, func(cols, _ []string, row []Value) error { out = append(out, fmt.Sprintf("%q", row)); return nil }); err != nil {
				t.Fatal(err)
			}
			return
		}
		before := all()
		var buf bytes.Buffer
		if err := c.Dump(ctx, &buf, DumpOptions{Objects: []Object{{Schema: schema, Name: "gt_cpu", Kind: KindTable}}, Structure: true, Data: true, DropFirst: true}, nil); err != nil {
			t.Fatal(err)
		}
		res, err := c.ImportSQL(ctx, bytes.NewReader(buf.Bytes()), ImportOptions{}, nil)
		if err != nil || int(res.Rows) != len(before) {
			t.Fatalf("%+v %v\n%s", res, err, buf.String())
		}
		if after := all(); len(before) != 8 || !slices.Equal(before, after) {
			t.Errorf("%q\n%q\n%s", before, after, buf.String())
		}
	})

	t.Run("delete tables", func(t *testing.T) {
		if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterTruncate}}, false); err == nil {
			t.Error("emptied")
		}
		if _, err := c.Drop(ctx, []Object{{Schema: schema, Name: "gt_cpu", Kind: KindTable}, {Schema: schema, Name: "gt_odd", Kind: KindTable}}, false); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Columns(ctx, ref); err == nil {
			t.Error("gt_cpu is still there")
		}
	})
}
