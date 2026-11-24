package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gotable/internal/model"
)

func TestElasticSplit(t *testing.T) {
	e := &elastic{}
	script := `# a comment
GET /_cat/indices?v

PUT /shop
{
  "mappings": { "properties": { "q": { "type": "text" } } }
}
// another
POST shop/_bulk
{"index":{"_id":"1"}}
{"q":"GET /not/a/request; } \" {"}
{"delete":{"_id":"2"}}

POST /shop/_search
{ "query": { "script": { "source": """
GET /inside
if (true) { return 1 }
""" } } }
SELECT a,
  b FROM shop;
-- sql comment
SELECT 2; 
FROM shop
| WHERE a > 1
| LIMIT 5;
DELETE /shop`
	var got []string
	for _, sp := range e.split(script) {
		if script[sp.Start:sp.End] == "" || strings.TrimSpace(script[sp.Start:sp.End]) != sp.SQL {
			t.Errorf("span %+v", sp)
		}
		f := strings.Fields(esStrip(sp.SQL))
		got = append(got, f[0]+" "+f[1])
	}
	want := []string{"GET /_cat/indices?v", "PUT /shop", "POST shop/_bulk", "POST /shop/_search", "SELECT a,", "SELECT 2;", "FROM shop", "DELETE /shop"}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	spans := e.split(script)
	if n := strings.Count(strings.TrimSpace(spans[2].SQL), "\n"); n != 3 {
		t.Errorf("bulk has %d lines: %s", n+1, spans[2].SQL)
	}
	if !strings.Contains(spans[3].SQL, "GET /inside") || !strings.Contains(spans[6].SQL, "LIMIT 5") {
		t.Error(spans)
	}
	if len(e.split("  \n# only\n// comments\n")) != 0 {
		t.Error("comments are no statements")
	}
}

func TestElasticCoerce(t *testing.T) {
	for _, tc := range []struct{ typ, now, in, want string }{
		{"long", "", "9007199254740993", "9007199254740993"},
		{"long", "", "007", "7"},
		{"long", "", "1.5", ""},
		{"long", "", "[1, 2]", "[1, 2]"},
		{"long", "[1]", "2", ""},
		{"unsigned_long", "", "18446744073709551615", "18446744073709551615"},
		{"double", "", "1.50", "1.50"},
		{"double", "", "NaN", ""},
		{"double", "", "abc", ""},
		{"boolean", "", "TRUE", "true"},
		{"boolean", "", "maybe", ""},
		{"keyword", "", "12", `"12"`},
		{"keyword", "", `["a"]`, `"[\"a\"]"`}, // a keyword's text, not a list
		{"keyword", `["a"]`, `["a","b"]`, `["a","b"]`},
		{"keyword", `["a"]`, "b", ""},
		{"text", `"x"`, `{"a":1}`, `"{\"a\":1}"`},
		{"object", "", `{"a":1}`, `{"a":1}`},
		{"object", "", "a", ""},
		{"date", `"2024-01-01"`, "2024-02-02", `"2024-02-02"`},
		{"date", "1700000000000", "1700000000001", "1700000000001"},
		{"geo_point", "", "41.12,-71.34", `"41.12,-71.34"`},
		{"geo_point", "", `{"lat":1,"lon":2}`, `{"lat":1,"lon":2}`},
		{"", "5", "6", "6"},
		{"", `"5"`, "6", `"6"`},
	} {
		got, err := esCoerce(tc.typ, json.RawMessage(tc.now), Text(tc.in))
		if (err != nil) != (tc.want == "") || err == nil && string(got) != tc.want {
			t.Errorf("%s (now %s): %q → %s %v, want %s", tc.typ, tc.now, tc.in, got, err, tc.want)
		}
	}
	if got, _ := esCoerce("long", nil, NullValue); string(got) != "null" {
		t.Error(string(got))
	}
}

// Credentials reach the server, and refusals are told plainly.
func TestElasticAuth(t *testing.T) {
	var last string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = r.Header.Get("Authorization")
		if strings.Contains(last, "bad") {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"root_cause":[{"type":"security_exception","reason":"unable to authenticate"}],"type":"security_exception","reason":"unable to authenticate"},"status":401}`)
			return
		}
		io.WriteString(w, `{"cluster_name":"c","version":{"number":"9.0.0"}}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	cfg := model.NewConnection(model.DriverElasticsearch)
	cfg.Name, cfg.Host = "x", u.Hostname()
	cfg.Port, _ = strconv.Atoi(u.Port())
	for _, tc := range []struct{ user, pw, want string }{{"", "", ""}, {"elastic", "pw", "Basic ZWxhc3RpYzpwdw=="}, {"", "key==", "ApiKey key=="}} {
		cfg.User = tc.user
		c, err := Open(context.Background(), cfg, Credentials{Password: tc.pw})
		if err != nil || last != tc.want {
			t.Errorf("%+v: sent %q, %v", tc, last, err)
			continue
		}
		c.Close()
		if err := c.Ping(context.Background()); err != ErrClosed {
			t.Error(err)
		}
	}
	cfg.User = ""
	if _, err := Open(context.Background(), cfg, Credentials{Password: "bad"}); err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Error(err)
	}
}

func TestElasticsearch(t *testing.T) {
	ran := false
	for env, d := range map[string]model.Driver{"ELASTIC": model.DriverElasticsearch, "OPENSEARCH": model.DriverOpenSearch} {
		if os.Getenv("GOTABLE_TEST_"+env+"_HOST") == "" {
			continue
		}
		ran = true
		t.Run(d.Label(), func(t *testing.T) { testElastic(t, env, d) })
	}
	if !ran {
		t.Skip("GOTABLE_TEST_ELASTIC_HOST not set")
	}
}

func testElastic(t *testing.T, env string, d model.Driver) {
	cfg, pw := serverConfig(t, env, d)
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
	fails := func(q string) error {
		t.Helper()
		res, err := c.Execute(ctx, q, 0)
		if err == nil {
			err = res[len(res)-1].Err
		}
		if err == nil {
			t.Errorf("%s worked", q)
			return fmt.Errorf("none")
		}
		return err
	}
	clean := func() {
		for _, n := range []string{"gt_shop", "gt_copy", "gt_many", "gt_bare", "gt_routed"} {
			c.Execute(ctx, "DELETE /"+n+"?ignore_unavailable=true", 0)
		}
		c.Execute(ctx, "DELETE /_data_stream/gt-logs", 0)
		c.Execute(ctx, "DELETE /_index_template/gt-logs", 0)
	}
	clean()
	defer clean()
	exec(`PUT /gt_shop
{ "settings": { "number_of_replicas": 0 },
  "aliases": { "gt_alias": { "filter": { "term": { "city": "Berlin" } } } },
  "mappings": { "properties": {
    "name": { "type": "text", "fields": { "keyword": { "type": "keyword", "ignore_above": 256 } } },
    "city": { "type": "keyword" }, "score": { "type": "integer" }, "big": { "type": "long" },
    "price": { "type": "double" }, "active": { "type": "boolean" }, "made": { "type": "date" },
    "bio": { "type": "text" }, "tags": { "type": "keyword" }, "address": { "properties": { "zip": { "type": "keyword" }, "floor": { "type": "integer" } } },
    "lines": { "type": "nested", "properties": { "sku": { "type": "keyword" } } } } } }
POST /gt_shop/_bulk?refresh=true
{"index":{"_id":"1"}}
{"name":"Alice","city":"Berlin","score":90,"big":9007199254740993,"price":1.50,"active":true,"made":"2024-03-15T10:20:30Z","bio":"likes quiet mountains","tags":["a","b"],"address":{"zip":"10115","floor":3},"lines":[{"sku":"x"}]}
{"index":{"_id":"2"}}
{"name":"Bob","city":"Paris","score":70,"active":false}
{"index":{"_id":"3"}}
{"name":"Carol","city":null,"score":85}
{"index":{"_id":"4"}}
{"name":"Dave","city":"Berlin","score":60}
{"index":{"_id":"5/odd id?"}}
{"name":"Eve*x","city":"Rome","score":99}`)
	schema := c.Schema()
	ref := TableRef{Schema: schema, Name: "gt_shop"}
	names := func(p Page) []string {
		t.Helper()
		if p.Sort == nil {
			p.Sort = []Sort{{Column: "name", Dir: Asc}}
		}
		rs, err := c.FetchRows(ctx, ref, p, nil)
		if err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
		i := slices.Index(rs.Columns, "name")
		out := []string{}
		for _, r := range rs.Rows {
			out = append(out, r[i].Text)
		}
		return out
	}
	value := func(id, col string) Value {
		t.Helper()
		rs, err := c.FetchRows(ctx, ref, Page{Filters: []Filter{{Column: "_id", Op: OpEq, Value: id}}, Limit: 5}, nil)
		if err != nil || len(rs.Rows) != 1 {
			t.Fatalf("%s: %v %+v", id, err, rs)
		}
		return rs.Rows[0][slices.Index(rs.Columns, col)]
	}

	t.Run("introspection", func(t *testing.T) {
		if v, err := c.ServerVersion(ctx); err != nil || !strings.HasPrefix(v, d.Label()+" ") {
			t.Fatal(v, err)
		}
		objs, err := c.Objects(ctx, schema)
		kinds := map[string]ObjectKind{}
		for _, o := range objs {
			kinds[o.Name] = o.Kind
		}
		if err != nil || kinds["gt_shop"] != KindTable || kinds["gt_alias"] != KindView {
			t.Fatal(objs, err)
		}
		for n := range kinds {
			if n[0] == '.' {
				t.Error("hidden index", n)
			}
		}
		cols, err := c.Columns(ctx, ref)
		types := map[string]string{}
		for _, col := range cols {
			types[col.Name] = col.Type
		}
		if err != nil || !cols[0].PrimaryKey || cols[0].Name != "_id" || types["name"] != "text" || types["address"] != "object" || types["lines"] != "nested" || types["big"] != "long" {
			t.Fatal(cols, err)
		}
		if _, err := c.Columns(ctx, TableRef{Schema: schema, Name: "gt_none"}); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Error(err)
		}
		ti, err := c.TableInfo(ctx, ref)
		if err != nil || !strings.Contains(ti.Options, "1 primary shard(s), 0 replica(s)") {
			t.Error(ti, err)
		}
		def, err := c.Definition(ctx, Object{Schema: schema, Name: "gt_shop", Kind: KindTable})
		if err != nil || !strings.HasPrefix(def, "PUT /gt_shop\n") || !strings.Contains(def, "ignore_above") || strings.Contains(def, "uuid") {
			t.Error(def, err)
		}
		def, err = c.Definition(ctx, Object{Schema: schema, Name: "gt_alias", Kind: KindView})
		if err != nil || !strings.HasPrefix(def, "POST /_aliases") || !strings.Contains(def, "gt_shop") {
			t.Error(def, err)
		}
		if _, err := c.Indexes(ctx, ref); err != nil {
			t.Error(err)
		}
		if err := c.UseSchema(ctx, "elsewhere"); err == nil {
			t.Error("another cluster")
		}
	})

	t.Run("values", func(t *testing.T) {
		for col, want := range map[string]string{"big": "9007199254740993", "price": "1.50", "active": "true", "made": "2024-03-15T10:20:30Z", "tags": `["a","b"]`,
			"address": `{"zip":"10115","floor":3}`, "lines": `[{"sku":"x"}]`} {
			if v := value("1", col); v.Text != want || v.Null {
				t.Errorf("%s: %+v, want %s", col, v, want)
			}
		}
		if v := value("3", "city"); !v.Null {
			t.Error(v)
		}
		if v := value("2", "price"); !v.Null {
			t.Error(v)
		}
	})

	t.Run("sorting, paging, filters", func(t *testing.T) {
		if got := names(Page{Sort: []Sort{{Column: "score", Dir: Desc}}, Limit: 2, Offset: 1}); !slices.Equal(got, []string{"Alice", "Carol"}) {
			t.Error(got)
		}
		for _, s := range []string{"bio", "_id", "address", "lines"} {
			if _, err := c.FetchRows(ctx, ref, Page{Sort: []Sort{{Column: s, Dir: Asc}}, Limit: 5}, nil); err == nil || strings.Contains(err.Error(), "illegal_argument") {
				t.Errorf("sort by %s: %v", s, err)
			}
		}
		for _, tc := range []struct {
			f    Filter
			want string
		}{
			{Filter{Column: "city", Op: OpEq, Value: "Berlin"}, "Alice Dave"},
			{Filter{Column: "city", Op: OpNe, Value: "Berlin"}, "Bob Eve*x"},
			{Filter{Column: "score", Op: OpGt, Value: "80"}, "Alice Carol Eve*x"},
			{Filter{Column: "score", Op: OpLe, Value: "70"}, "Bob Dave"},
			{Filter{Column: "score", Op: OpBetween, Value: "60", Value2: "80"}, "Bob Dave"},
			{Filter{Column: "score", Op: OpIn, Value: "70, 99"}, "Bob Eve*x"},
			{Filter{Column: "score", Op: OpNotIn, Value: "70, 99"}, "Alice Carol Dave"},
			{Filter{Column: "city", Op: OpIsNull}, "Carol"},
			{Filter{Column: "city", Op: OpIsNotNull}, "Alice Bob Dave Eve*x"},
			{Filter{Column: "address", Op: OpIsNotNull}, "Alice"},
			{Filter{Column: "lines", Op: OpIsNotNull}, "Alice"},
			{Filter{Column: "lines", Op: OpIsNull}, "Bob Carol Dave Eve*x"},
			{Filter{Column: "name", Op: OpEq, Value: "Alice"}, "Alice"},
			{Filter{Column: "name", Op: OpEq, Value: "alice"}, ""},
			{Filter{Column: "name", Op: OpContains, Value: "AL"}, "Alice"},
			{Filter{Column: "name", Op: OpContains, Value: "e*x"}, "Eve*x"},
			{Filter{Column: "name", Op: OpContains, Value: "e?x"}, ""},
			{Filter{Column: "name", Op: OpNotContain, Value: "a"}, "Bob Eve*x"},
			{Filter{Column: "name", Op: OpBeginsWith, Value: "b"}, "Bob"},
			{Filter{Column: "name", Op: OpEndsWith, Value: "OL"}, "Carol"},
			{Filter{Column: "name", Op: OpLike, Value: "_o%"}, "Bob"},
			{Filter{Column: "name", Op: OpNotLike, Value: "_o%"}, "Alice Carol Dave Eve*x"},
			{Filter{Column: "bio", Op: OpContains, Value: "quiet mount"}, "Alice"},
			{Filter{Column: "tags", Op: OpEq, Value: "b"}, "Alice"},
			{Filter{Column: "active", Op: OpEq, Value: "true"}, "Alice"},
			{Filter{Column: "made", Op: OpGt, Value: "2024-01-01"}, "Alice"},
			{Filter{Column: "big", Op: OpEq, Value: "9007199254740993"}, "Alice"},
			{Filter{Column: "big", Op: OpEq, Value: "9007199254740992"}, ""},
			{Filter{Column: "_id", Op: OpIn, Value: "1, 3"}, "Alice Carol"},
			{Filter{Column: "_id", Op: OpNe, Value: "1"}, "Bob Carol Dave Eve*x"},
			{Filter{Column: AnyColumn, Op: OpContains, Value: "pari"}, "Bob"},
			{Filter{Column: AnyColumn, Op: OpContains, Value: "85"}, "Carol"},
			{Filter{Column: AnyColumn, Op: OpContains, Value: "mountains"}, "Alice"},
			{Filter{Column: AnyColumn, Op: OpNotContain, Value: "berlin"}, "Bob Carol Eve*x"},
		} {
			if got := strings.Join(names(Page{Filters: []Filter{tc.f}}), " "); got != tc.want {
				t.Errorf("%+v: got %q, want %q", tc.f, got, tc.want)
			}
			if n, err := c.CountRows(ctx, ref, Page{Filters: []Filter{tc.f}}, nil); err != nil || int(n) != len(strings.Fields(tc.want)) {
				t.Errorf("%+v: count %d %v", tc.f, n, err)
			}
		}
		for raw, want := range map[string]string{`{"term":{"address.zip":"10115"}}`: "Alice", `{"query":{"range":{"score":{"gte":90}}}}`: "Alice Eve*x", "score:>80 AND NOT city:Rome": "Alice Carol", "address.floor:3": "Alice"} {
			if got := strings.Join(names(Page{RawWhere: raw}), " "); got != want {
				t.Errorf("%s: %q, want %q", raw, got, want)
			}
		}
		if got := strings.Join(names(Page{RawWhere: "score:>80", Filters: []Filter{{Column: "city", Op: OpEq, Value: "Berlin"}}}), " "); got != "Alice" {
			t.Error(got)
		}
		for _, p := range []Page{{RawWhere: "{nope"}, {RawWhere: "score:("}, {Filters: []Filter{{Column: "score", Op: OpEq, Value: "many"}}}, {Filters: []Filter{{Column: "address", Op: OpEq, Value: "x"}}},
			{Filters: []Filter{{Column: "nope", Op: OpEq, Value: "x"}}}, {Filters: []Filter{{Column: "_id", Op: OpContains, Value: "x"}}}, {Filters: []Filter{{Column: "bio", Op: OpGt, Value: "x"}}}} {
			if _, err := c.FetchRows(ctx, ref, p, nil); err == nil {
				t.Errorf("%+v worked", p)
			}
		}
		rs, err := c.FetchRows(ctx, TableRef{Schema: schema, Name: "gt_alias"}, Page{Limit: 10}, nil)
		if err != nil || len(rs.Rows) != 2 {
			t.Error("alias", rs, err)
		}
	})

	t.Run("editing", func(t *testing.T) {
		changes := []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("1")}, Values: map[string]Value{"name": Text("Alicia"), "city": NullValue, "address": Text(`{"zip":"20095"}`), "tags": Text(`["c"]`), "price": Text("2.25")}},
			{Kind: ChangeInsert, Values: map[string]Value{"_id": Text("6"), "name": Text("Frank"), "score": Text("50")}},
			{Kind: ChangeInsert, Values: map[string]Value{"name": Text("Gina"), "active": Text("true")}},
			{Kind: ChangeDelete, Key: map[string]Value{"_id": Text("5/odd id?")}},
		}
		if p, err := c.PreviewChanges(ctx, ref, changes, true); err != nil || len(p) != 4 || !strings.Contains(p[0], "Alicia") || p[3] != "DELETE /gt_shop/_doc/5/odd id?" {
			t.Fatal(p, err)
		}
		if err := c.ApplyChanges(ctx, ref, changes, true); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(names(Page{}), " "); got != "Alicia Bob Carol Dave Frank Gina" {
			t.Fatal(got)
		}
		// An object is replaced, not merged; what wasn't edited is as it was.
		for col, want := range map[string]string{"address": `{"zip":"20095"}`, "tags": `["c"]`, "big": "9007199254740993", "lines": `[{"sku":"x"}]`, "price": "2.25"} {
			if v := value("1", col); v.Text != want {
				t.Errorf("%s: %q, want %q", col, v.Text, want)
			}
		}
		if v := value("1", "city"); !v.Null {
			t.Error(v)
		}
		for _, bad := range []struct {
			ch   Change
			want string
		}{
			{Change{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("nope")}, Values: map[string]Value{"name": Text("x")}}, errMongoRowGone.Error()},
			{Change{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("2")}, Values: map[string]Value{"_id": Text("9")}}, "can't change"},
			{Change{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("2")}, Values: map[string]Value{"score": Text("lots")}}, "whole number"},
			{Change{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("2")}, Values: map[string]Value{"made": Text("someday")}}, "made"},
			{Change{Kind: ChangeInsert, Values: map[string]Value{"_id": Text("2"), "name": Text("x")}}, "already exists"},
			{Change{Kind: ChangeDelete, Key: map[string]Value{"_id": Text("nope")}}, errMongoRowGone.Error()},
		} {
			if err := c.ApplyChanges(ctx, ref, []Change{bad.ch}, true); err == nil || !strings.Contains(err.Error(), bad.want) {
				t.Errorf("%+v: %v", bad.ch, err)
			}
		}
		// Two edits of which the second fails: the answer says what was saved.
		err := c.ApplyChanges(ctx, ref, []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("2")}, Values: map[string]Value{"score": Text("71")}},
			{Kind: ChangeInsert, Values: map[string]Value{"_id": Text("3"), "name": Text("x")}}}, true)
		if err == nil || !strings.Contains(err.Error(), "1 of 2 changes were saved") {
			t.Error(err)
		}
		// A document written between the plan and the save.
		e := c.remote.(*elastic)
		ops, err := e.plan(ctx, ref, []Change{{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("2")}, Values: map[string]Value{"score": Text("1")}}})
		if err != nil {
			t.Fatal(err)
		}
		exec(`POST /gt_shop/_update/2?refresh=true
{"doc":{"score":77}}`)
		w := esBulk{e: e, index: "gt_shop"}
		w.add(ops[0].lines...)
		if _, _, err := w.send(ctx, "true"); err == nil || !strings.Contains(err.Error(), "changed by someone else") {
			t.Error(err)
		}
		if v := value("2", "score"); v.Text != "77" {
			t.Error(v)
		}
	})

	t.Run("routing", func(t *testing.T) {
		exec(`PUT /gt_routed
{"settings":{"number_of_shards":3,"number_of_replicas":0},"mappings":{"_routing":{"required":true},"properties":{"a":{"type":"keyword"}}}}
POST /gt_routed/_bulk?refresh=true
{"index":{"_id":"1","routing":"x"}}
{"a":"one"}
{"index":{"_id":"2","routing":"y"}}
{"a":"two"}`)
		r := TableRef{Schema: schema, Name: "gt_routed"}
		if err := c.ApplyChanges(ctx, r, []Change{{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("1")}, Values: map[string]Value{"a": Text("ONE")}},
			{Kind: ChangeDelete, Key: map[string]Value{"_id": Text("2")}}}, true); err != nil {
			t.Fatal(err)
		}
		rs, err := c.FetchRows(ctx, r, Page{Limit: 9}, nil)
		if err != nil || len(rs.Rows) != 1 || rs.Rows[0][1].Text != "ONE" {
			t.Fatal(rs, err)
		}
	})

	t.Run("editor", func(t *testing.T) {
		r := exec("GET /gt_shop/_search\n{\"size\": 2, \"sort\": [{\"score\": \"desc\"}], \"query\": {\"exists\": {\"field\": \"score\"}}}")
		if r.Set == nil || len(r.Set.Rows) != 2 || r.Set.Columns[0] != "_id" || r.Duration <= 0 {
			t.Fatalf("%+v", r.Set)
		}
		r = exec("GET _cat/indices/gt_shop?h=index,pri")
		if len(r.Set.Rows) != 1 || !slices.Equal(r.Set.Columns, []string{"index", "pri"}) || r.Set.Rows[0][0].Text != "gt_shop" {
			t.Errorf("%+v", r.Set)
		}
		r = exec(`POST /gt_shop/_search
{"size":0,"aggs":{"by_city":{"terms":{"field":"city"},"aggs":{"best":{"max":{"field":"score"}}}}}}`)
		if !slices.Equal(r.Set.Columns, []string{"by_city", "doc_count", "best"}) || len(r.Set.Rows) != 2 {
			t.Errorf("%+v", r.Set)
		}
		r = exec(`POST /gt_shop/_search
{"size":0,"aggs":{"n":{"value_count":{"field":"score"}},"m":{"min":{"field":"score"}}}}`)
		if !slices.Equal(r.Set.Columns, []string{"m", "n"}) || len(r.Set.Rows) != 1 {
			t.Errorf("%+v", r.Set)
		}
		if r = exec("GET /gt_shop/_doc/1"); r.Set.Rows[0][0].Text != "1" || !slices.Contains(r.Set.Columns, "name") {
			t.Errorf("%+v", r.Set)
		}
		if r = exec("GET /gt_shop/_mapping"); r.Set.Columns[0] != "response" || !strings.Contains(r.Set.Rows[0][0].Text, "ignore_above") {
			t.Errorf("%+v", r.Set)
		}
		if r = exec("HEAD /gt_shop"); r.Set.Rows[0][0].Text != "200 OK" {
			t.Errorf("%+v", r.Set)
		}
		if r = exec("PUT /gt_shop/_doc/x1?refresh=true\n{\"name\": \"\"\"tri\"ple\n\"\"\"}"); r.RowsAffected != 1 || value("x1", "name").Text != "tri\"ple\n" {
			t.Errorf("%+v", r)
		}
		if r = exec(`POST /gt_shop/_delete_by_query?refresh=true
{"query":{"ids":{"values":["x1"]}}}`); r.RowsAffected != 1 {
			t.Errorf("%+v", r)
		}
		r = exec("SELECT name, score FROM gt_shop WHERE score > 80 ORDER BY score DESC")
		if len(r.Set.Rows) != 2 || r.Set.Rows[0][0].Text != "Alicia" || r.Set.Rows[0][1].Text != "90" || r.Set.Columns[0] != "name" {
			t.Errorf("%+v", r.Set)
		}
		res, err := c.Execute(ctx, "SELECT name FROM gt_shop", 2)
		if err != nil || res[0].Err != nil || len(res[0].Set.Rows) != 2 || !res[0].Truncated {
			t.Errorf("%+v %v", res, err)
		}
		if d == model.DriverElasticsearch {
			r = exec("# ES|QL\nFROM gt_shop\n| WHERE score > 80\n| STATS n = COUNT(*)")
			if r.Set.Rows[0][0].Text != "2" || r.Set.Columns[0] != "n" {
				t.Errorf("%+v", r.Set)
			}
		} else if err := fails("FROM gt_shop | LIMIT 1"); !strings.Contains(err.Error(), "no ES|QL") {
			t.Error(err)
		}
		if err := fails("GET /gt_none/_search"); !strings.Contains(err.Error(), "no such index") {
			t.Error(err)
		}
		if err := fails("POST /gt_shop/_search\n{\"query\": {\"nope\": {}}}"); !strings.Contains(err.Error(), "nope") {
			t.Error(err)
		}
		fails("POST /gt_shop/_bulk\n{\"create\":{\"_id\":\"2\"}}\n{\"name\":\"dup\"}")
		fails("SELECT nope FROM gt_none")
		fails("INSERT INTO gt_shop VALUES (1)")
		rs, _ := c.Execute(ctx, "GET /\nGET /gt_none\nGET /", 0)
		if len(rs) != 2 || rs[1].Err == nil {
			t.Errorf("an error should stop the script: %+v", rs)
		}
	})

	t.Run("csv import", func(t *testing.T) {
		n, err := c.ImportCSV(ctx, strings.NewReader("id,name,score,active\n20,Imp,1,true\n,Ort,2,\n"), ref,
			CSVImportOptions{Header: true, Columns: []string{"_id", "name", "score", "active"}, Mapping: []int{0, 1, 2, 3}, EmptyAsNull: true}, nil)
		if err != nil || n != 2 {
			t.Fatal(n, err)
		}
		if got := names(Page{Filters: []Filter{{Column: "score", Op: OpLt, Value: "10"}}}); !slices.Equal(got, []string{"Imp", "Ort"}) {
			t.Error(got)
		}
		_, err = c.ImportCSV(ctx, strings.NewReader("name,made\nok,2024-01-01\nbad,someday\n"), ref,
			CSVImportOptions{Header: true, Columns: []string{"name", "made"}, Mapping: []int{0, 1}}, nil)
		if err == nil || !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), "1 rows were imported") {
			t.Error(err)
		}
	})

	t.Run("export and import", func(t *testing.T) {
		var buf bytes.Buffer
		if err := c.Dump(ctx, &buf, DumpOptions{Objects: []Object{{Schema: schema, Name: "gt_shop", Kind: KindTable}, {Schema: schema, Name: "gt_alias", Kind: KindView}}, Structure: true, Data: true, DropFirst: true}, nil); err != nil {
			t.Fatal(err)
		}
		before := names(Page{})
		dump := strings.ReplaceAll(strings.ReplaceAll(buf.String(), "gt_shop", "gt_copy"), "gt_alias", "gt_alias2")
		res, err := c.ImportSQL(ctx, strings.NewReader(dump), ImportOptions{}, nil)
		if err != nil || int(res.Rows) != len(before) {
			t.Fatalf("%+v %v\n%s", res, err, dump)
		}
		all := func(n string) (out []string) {
			c.StreamRows(ctx, TableRef{Schema: schema, Name: n}, Page{}, nil, func(_, _ []string, row []Value) error { out = append(out, fmt.Sprint(row)); return nil })
			slices.Sort(out)
			return
		}
		if a, b := all("gt_shop"), all("gt_copy"); len(a) != len(before) || !slices.Equal(a, b) {
			t.Errorf("%q\n%q", a, b)
		}
		ca, _ := c.Columns(ctx, ref)
		cb, _ := c.Columns(ctx, TableRef{Schema: schema, Name: "gt_copy"})
		if fmt.Sprint(ca) != fmt.Sprint(cb) {
			t.Error(ca, cb)
		}
		if _, err := c.ImportSQL(ctx, strings.NewReader("GET /\n\nGET /gt_none\n"), ImportOptions{}, nil); err == nil || !strings.Contains(err.Error(), "line 3") {
			t.Error(err)
		}
	})

	t.Run("many documents", func(t *testing.T) {
		exec("PUT /gt_many\n{\"settings\":{\"number_of_shards\":2,\"number_of_replicas\":0},\"mappings\":{\"properties\":{\"n\":{\"type\":\"integer\"}}}}")
		var b strings.Builder
		for i := range 10_500 {
			if i%3500 == 0 {
				b.WriteString("POST /gt_many/_bulk\n")
			}
			fmt.Fprintf(&b, "{\"index\":{\"_id\":\"%d\"}}\n{\"n\":%d}\n", i, i)
		}
		b.WriteString("POST /gt_many/_refresh\n")
		exec(b.String())
		m := TableRef{Schema: schema, Name: "gt_many"}
		seen := map[string]bool{}
		if err := c.StreamRows(ctx, m, Page{}, nil, func(_, _ []string, row []Value) error { seen[row[0].Text] = true; return nil }); err != nil || len(seen) != 10_500 {
			t.Error(len(seen), err)
		}
		if n, err := c.CountRows(ctx, m, Page{}, nil); err != nil || n != 10_500 {
			t.Error(n, err)
		}
		// Past the 10,000 a search reaches.
		rs, err := c.FetchRows(ctx, m, Page{Sort: []Sort{{Column: "n", Dir: Asc}}, Limit: 300, Offset: 10_200}, nil)
		if err != nil || len(rs.Rows) != 300 || rs.Rows[0][1].Text != "10200" || rs.Rows[299][1].Text != "10499" {
			t.Error(len(rs.Rows), err)
		}
		if _, err := c.Alter(ctx, m, []AlterOp{{Kind: AlterTruncate}}, false); err != nil {
			t.Error(err)
		}
		if n, err := c.CountRows(ctx, m, Page{}, nil); err != nil || n != 0 {
			t.Error(n, err)
		}
		if _, err := c.Alter(ctx, m, []AlterOp{{Kind: AlterDropColumn, Column: "n"}}, true); err == nil {
			t.Error("a field was dropped")
		}
	})

	t.Run("data streams and unmapped fields", func(t *testing.T) {
		exec(`PUT /_index_template/gt-logs
{"index_patterns":["gt-logs"],"data_stream":{},"priority":500,"template":{"settings":{"number_of_replicas":0}}}
POST /gt-logs/_bulk?refresh=true
{"create":{}}
{"@timestamp":"2024-01-01T00:00:00Z","msg":"hello"}`)
		objs, _ := c.Objects(ctx, schema)
		if !slices.ContainsFunc(objs, func(o Object) bool { return o.Name == "gt-logs" && o.Kind == KindView }) {
			t.Error(objs)
		}
		rs, err := c.FetchRows(ctx, TableRef{Schema: schema, Name: "gt-logs"}, Page{Limit: 5, Sort: []Sort{{Column: "@timestamp", Dir: Desc}}}, nil)
		if err != nil || len(rs.Rows) != 1 || !slices.Contains(rs.Columns, "msg") {
			t.Error(rs, err)
		}
		if def, err := c.Definition(ctx, Object{Schema: schema, Name: "gt-logs", Kind: KindView}); err != nil || !strings.Contains(def, "_data_stream") {
			t.Error(def, err)
		}
		if _, err := c.Drop(ctx, []Object{{Schema: schema, Name: "gt-logs", Kind: KindView}}, false); err == nil {
			t.Error("a data stream was dropped as an index")
		}
		exec("PUT /gt_bare\n{\"mappings\":{\"dynamic\":false,\"properties\":{\"a\":{\"type\":\"keyword\"}}}}\nPUT /gt_bare/_doc/1?refresh=true\n{\"a\":\"x\",\"extra\":{\"k\":[1,2]}}")
		rs, err = c.FetchRows(ctx, TableRef{Schema: schema, Name: "gt_bare"}, Page{Limit: 5}, nil)
		if err != nil || !slices.Equal(rs.Columns, []string{"_id", "a", "extra"}) || rs.Rows[0][2].Text != `{"k":[1,2]}` {
			t.Error(rs, err)
		}
	})

	t.Run("delete indices", func(t *testing.T) {
		if _, err := c.Drop(ctx, []Object{{Schema: schema, Name: "gt_shop", Kind: KindTable}, {Schema: schema, Name: "gt_copy", Kind: KindTable}}, false); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Columns(ctx, ref); err == nil {
			t.Error("gt_shop is still there")
		}
	})
}
