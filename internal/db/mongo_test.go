package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"gotable/internal/model"
)

func TestParseShell(t *testing.T) {
	cmd, err := parseShell(`// the oldest
	db.people.find({ age: { $gt: 30 }, 'name': "A\"b", when: ISODate("2024-03-15"), big: NumberLong("5"),
		id: ObjectId("507f1f77bcf86cd799439011"), re: /^a\/b[/]c/i, list: [1, 2.5, -3, true, null,], }, { name: 1 })
		.sort({ age: -1 })
		.limit(10);`)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.collection != "people" || len(cmd.calls) != 3 || cmd.calls[0].name != "find" || cmd.calls[2].name != "limit" {
		t.Fatalf("%+v", cmd)
	}
	f := cmd.calls[0].args[0].(bson.D)
	id, _ := bson.ObjectIDFromHex("507f1f77bcf86cd799439011")
	want := bson.D{
		{Key: "age", Value: bson.D{{Key: "$gt", Value: int32(30)}}},
		{Key: "name", Value: `A"b`},
		{Key: "when", Value: bson.DateTime(time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC).UnixMilli())},
		{Key: "big", Value: int64(5)},
		{Key: "id", Value: id},
		{Key: "re", Value: bson.Regex{Pattern: `^a\/b[/]c`, Options: "i"}},
		{Key: "list", Value: bson.A{int32(1), 2.5, int32(-3), true, nil}},
	}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("filter\n got %#v\nwant %#v", f, want)
	}

	for stmt, coll := range map[string]string{
		`db.getCollection("odd-name").find()`: "odd-name",
		`db["odd-name"].find()`:               "odd-name",
		`db.system.users.find()`:              "system.users",
	} {
		c, err := parseShell(stmt)
		if err != nil || c.collection != coll {
			t.Errorf("%s: %+v %v", stmt, c, err)
		}
	}
	if c, err := parseShell(`db.getSiblingDB("other").x.drop()`); err != nil || c.database != "other" || c.collection != "x" {
		t.Errorf("sibling: %+v %v", c, err)
	}
	if c, err := parseShell("use shop;"); err != nil || c.use != "shop" {
		t.Errorf("use: %+v %v", c, err)
	}
	if c, err := parseShell("show collections"); err != nil || c.show != "collections" {
		t.Errorf("show: %+v %v", c, err)
	}
	// Nothing but literals: this must never run as code.
	for _, bad := range []string{
		"SELECT * FROM people", "db.people", "db.people.find({ a: function () { return 1 } })",
		"db.people.find({ a: process.exit() })", "db.a.find() garbage", `db.a.find({ "x" 1 })`, "db.a.find({", "db.a.find().b.c()",
	} {
		if _, err := parseShell(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestSplitMongo(t *testing.T) {
	spans := splitMongo(`// a comment; with a semicolon
use shop
db.a.find({ x: "a;b", y: /;\/"/ })
  .sort({ x: 1 })

db.b.insertOne({
  note: 'it\'s; fine',
}); db.c.drop()
/* block ; */
db.d.countDocuments() // trailing
`)
	got := make([]string, len(spans))
	for i, s := range spans {
		got[i] = strings.Join(strings.Fields(s.SQL), " ")
	}
	want := []string{
		"use shop",
		`db.a.find({ x: "a;b", y: /;\/"/ }) .sort({ x: 1 })`,
		`db.b.insertOne({ note: 'it\'s; fine', })`,
		"db.c.drop()",
		"db.d.countDocuments() // trailing",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	for _, s := range spans {
		if _, err := parseShell(s.SQL); err != nil {
			t.Errorf("%s: %v", s.SQL, err)
		}
	}
}

func TestMongoValues(t *testing.T) {
	id := bson.NewObjectID()
	when := bson.DateTime(time.Date(2024, 3, 15, 10, 20, 30, 0, time.UTC).UnixMilli())
	dec, _ := bson.ParseDecimal128("1.50")
	uuid := bson.Binary{Subtype: bson.TypeBinaryUUID, Data: bytes.Repeat([]byte{0xab}, 16)}
	bin := bson.Binary{Subtype: 0x80, Data: []byte{1, 2, 0xff}}
	// A cell's text reads back as the same value and type.
	for _, v := range []any{"text", int32(5), int64(1 << 40), int64(5), 2.5, dec, true, when, id, uuid, bin,
		bson.D{{Key: "a", Value: int32(1)}, {Key: "b", Value: bson.A{"x"}}}, bson.A{int32(1), "y"}} {
		cell := mongoValue(v)
		back, err := coerceMongoValue(cell.Text, v, bsonType(v))
		if err != nil {
			t.Errorf("%v (%s): %v", v, cell.Text, err)
		} else if !reflect.DeepEqual(back, v) {
			t.Errorf("%T %q reads back as %#v", v, cell.Text, back)
		}
	}
	// A mistyped value is refused rather than changing the field's type…
	for _, tc := range [][2]string{{"12a", "int"}, {"x", "double"}, {"maybe", "bool"}, {"soon", "date"}, {"abc", "objectId"}, {"{a:", "object"}, {"{}", "array"}} {
		if _, err := coerceMongoValue(tc[0], nil, tc[1]); err == nil {
			t.Errorf("%s as %s: expected an error", tc[0], tc[1])
		}
	}
	// … unless the text spells out another type. Strings take any text.
	if v, err := coerceMongoValue(`NumberLong("7")`, nil, "int"); err != nil || v != int64(7) {
		t.Errorf("spelled out: %v %v", v, err)
	}
	if v, _ := coerceMongoValue(`NumberLong("7")`, nil, "string"); v != `NumberLong("7")` {
		t.Errorf("string: %v", v)
	}
	// A new field goes by what the text looks like; a zip code in a string
	// column stays a string as the column's type decides.
	for text, want := range map[string]any{"5": int32(5), "2.5": 2.5, "true": true, "hello": "hello", "12 Main St": "12 Main St"} {
		if v, _ := coerceMongoValue(text, nil, ""); v != want {
			t.Errorf("guess %q: %#v", text, v)
		}
	}
	if v, _ := coerceMongoValue("01234", nil, "string"); v != "01234" {
		t.Errorf("zip: %v", v)
	}
	if v, _ := coerceMongoValue("7", nil, "long"); v != int64(7) {
		t.Errorf("long stays long: %#v", v)
	}
}

func TestMongoFilter(t *testing.T) {
	f, err := mongoFilter(Page{Filters: []Filter{{Column: "age", Op: OpGt, Value: "30"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Both readings: MongoDB compares each with values of its own type.
	want := bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "age", Value: bson.D{{Key: "$gt", Value: "30"}}}},
		bson.D{{Key: "age", Value: bson.D{{Key: "$gt", Value: int32(30)}}}},
	}}}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("%#v", f)
	}
	if _, err := mongoFilter(Page{RawWhere: "{ age: "}, nil); err == nil {
		t.Fatal("expected an error for a broken raw filter")
	}
	if _, err := mongoFilter(Page{RawWhere: "5"}, nil); err == nil {
		t.Fatal("expected an error for a raw filter that isn't a document")
	}
	if likeToRegex("a%b_c.") != `^a.*b.c\.$` {
		t.Fatal(likeToRegex("a%b_c."))
	}
}

func TestMongoURI(t *testing.T) {
	c := model.NewConnection(model.DriverMongoDB)
	c.Host, c.User, c.Options = "db.example", "root", "replicaSet=rs0"
	u, err := mongoURI(c, c.Address(), "p@ss/w", false)
	if err != nil || u != "mongodb://root:p%40ss%2Fw@db.example:27017/?authSource=admin&replicaSet=rs0" {
		t.Fatal(u, err)
	}
	c = model.NewConnection(model.DriverMongoDB)
	c.Host, c.SRV, c.SSLMode = "cluster.example", true, "require"
	if u, _ := mongoURI(c, c.Address(), "", false); u != "mongodb+srv://cluster.example/" {
		t.Fatal(u)
	}
	// SRV: authSource is left to the TXT record, the database goes in the
	// path as the shell's fallback, and "disable" overrides the TLS default.
	c.User, c.DBName, c.SSLMode = "u", "shop", "disable"
	if u, _ := mongoURI(c, c.Address(), "pw", false); u != "mongodb+srv://u:pw@cluster.example/shop?tls=false" {
		t.Fatal(u)
	}
	c = model.NewConnection(model.DriverMongoDB)
	c.Host, c.Port = "a:1,b:2", 0
	if u, _ := mongoURI(c, "ignored", "", false); u != "mongodb://a:1,b:2/" {
		t.Fatal(u)
	}
	c = model.NewConnection(model.DriverMongoDB)
	c.Host = "behind"
	if u, _ := mongoURI(c, "127.0.0.1:9", "", true); u != "mongodb://127.0.0.1:9/?directConnection=true" {
		t.Fatal(u)
	}
}

// TestMongoDB covers GoTable on a real server: a standalone one, a replica
// set and a sharded cluster, whichever scripts/test-dbs.sh started, and a
// hosted one (Atlas) given as GOTABLE_TEST_MONGOATLAS_URL.
func TestMongoDB(t *testing.T) {
	ran := false
	for _, name := range []string{"MONGODB", "MONGORS", "MONGOSHARD", "MONGOATLAS", "FERRETDB"} {
		if os.Getenv("GOTABLE_TEST_"+name+"_HOST") == "" && os.Getenv("GOTABLE_TEST_"+name+"_URL") == "" {
			continue
		}
		ran = true
		t.Run(strings.ToLower(name), func(t *testing.T) { testMongoServer(t, name) })
	}
	if !ran {
		t.Skip("GOTABLE_TEST_MONGODB_HOST not set")
	}
}

func testMongoServer(t *testing.T, name string) {
	var cfg *model.Connection
	var pw string
	if u := os.Getenv("GOTABLE_TEST_" + name + "_URL"); u != "" {
		// A URL as the service gives it, read the way a pasted one is.
		var err error
		if cfg, pw, err = model.ParseURL(u); err != nil {
			t.Fatal(err)
		}
	} else {
		d := model.DriverMongoDB
		if name == "FERRETDB" {
			d = model.DriverFerretDB
		}
		cfg, pw = serverConfig(t, name, d)
		cfg.Options = os.Getenv("GOTABLE_TEST_" + name + "_OPTIONS") // directConnection=true for a replica set in a container
	}
	cfg.DBName = "gt_test"
	c := openTest(t, cfg, pw)
	// FerretDB (on PostgreSQL) emulates MongoDB without views, hidden
	// indexes, most $jsonSchema keywords or $regexMatch on converted values.
	ferret := name == "FERRETDB"
	// Transactions where the server has them: a replica set or mongos.
	if want := name != "MONGODB"; name != "FERRETDB" && c.mongoTxn != want {
		t.Fatalf("%s: transactions %v, want %v", name, c.mongoTxn, want)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	must := func(q string) Result {
		t.Helper()
		res, err := c.Execute(ctx, q, 0)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		last := res[len(res)-1]
		if last.Err != nil {
			t.Fatalf("%s: %v", last.Statement, last.Err)
		}
		return last
	}
	must("db.dropDatabase()")
	// Its own connection: the test closes c before cleanups run.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if c, err := Open(ctx, cfg, Credentials{Password: pw}); err == nil {
			c.Execute(ctx, `db.getSiblingDB("gt_test").dropDatabase(); db.getSiblingDB("gt_copy").dropDatabase()`, 0)
			c.Close()
		}
	})

	// A wrong password is told apart from an unreachable server, and quickly.
	if cfg.User != "" {
		wrong := *cfg
		start := time.Now()
		_, err := Open(ctx, &wrong, Credentials{Password: "wrong"})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "authentication failed") || time.Since(start) > 10*time.Second { // Atlas: "bad auth : authentication failed"
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	}
	if v, err := c.ServerVersion(ctx); err != nil || !strings.HasPrefix(v, map[bool]string{false: "MongoDB ", true: "FerretDB "}[ferret]) {
		t.Fatal(v, err)
	}
	if c.Schema() != "gt_test" {
		t.Fatal(c.Schema())
	}
	// A database that has no collection yet can still be open.
	if s, err := c.Schemas(ctx); err != nil || !slices.Contains(s, "gt_test") {
		t.Fatal(s, err)
	}

	must(`db.createCollection("people")`)
	tbl := TableRef{Schema: "gt_test", Name: "people"}
	// An empty collection still has its key, so the grid can add rows.
	cols, err := c.Columns(ctx, tbl)
	if err != nil || len(cols) != 1 || cols[0].Name != "_id" || !cols[0].PrimaryKey {
		t.Fatal(cols, err)
	}
	r := must(`db.people.insertMany([
		{ _id: 1, name: "Alice", age: 41, city: "Berlin", seen: ISODate("2024-03-15T10:20:30Z"), address: { zip: "01234" }, tags: ["a", "b"], big: NumberLong("9000000000") },
		{ _id: 2, name: "Bob", age: 29, city: "Paris", active: true },
		{ _id: 3, name: "Carol", age: "unknown", city: null },
		{ _id: "four", name: "Dave_x", age: 60, score: 2.5 },
	])`)
	if r.RowsAffected != 4 {
		t.Fatal(r.RowsAffected)
	}
	must(`db.people.createIndex({ name: 1 }, { unique: true })`)
	if !ferret { // FerretDB makes an empty collection of it
		must(`db.createView("adults", "people", [{ $match: { age: { $gte: 30 } } }, { $project: { name: 1 } }])`)
	}

	t.Run("introspection", func(t *testing.T) {
		objs, err := c.Objects(ctx, "gt_test")
		if ferret {
			if err != nil || len(objs) != 1 || objs[0].Name != "people" {
				t.Fatal(objs, err)
			}
		} else if err != nil || len(objs) != 2 || objs[0].Name != "adults" || objs[0].Kind != KindView || objs[1].Kind != KindTable {
			t.Fatal(objs, err)
		}
		cols, err := c.Columns(ctx, tbl)
		if err != nil {
			t.Fatal(err)
		}
		types := map[string]Column{}
		for _, col := range cols {
			types[col.Name] = col
		}
		if cols[0].Name != "_id" || !cols[0].PrimaryKey || cols[0].Nullable {
			t.Fatalf("%+v", cols[0])
		}
		for name, want := range map[string]string{"_id": "int|string", "name": "string", "age": "int|string", "seen": "date",
			"address": "object", "tags": "array", "big": "long", "active": "bool", "score": "double", "city": "string"} {
			if types[name].Type != want {
				t.Errorf("%s is %q, want %q", name, types[name].Type, want)
			}
		}
		if types["score"].Comment != "in 1 of 4 sampled documents" {
			t.Errorf("%q", types["score"].Comment)
		}
		idx, err := c.Indexes(ctx, tbl)
		if err != nil || len(idx) != 2 || !idx[0].Primary || idx[1].Name != "name_1" || !idx[1].Unique {
			t.Fatal(idx, err)
		}
		if names, err := c.ColumnNames(ctx, tbl); err != nil || names[0] != "_id" || !slices.Contains(names, "score") {
			t.Fatal(names, err)
		}
		if idx, err := c.Indexes(ctx, TableRef{Schema: "gt_test", Name: "adults"}); !ferret && (err != nil || len(idx) != 0) {
			t.Fatal(idx, err)
		}
		def, err := c.Definition(ctx, Object{Schema: "gt_test", Name: "people", Kind: KindTable})
		if err != nil || !strings.Contains(def, `db.createCollection("people")`) || !strings.Contains(def, `db.people.createIndex({"name":1}`) {
			t.Fatal(def, err)
		}
		def, err = c.Definition(ctx, Object{Schema: "gt_test", Name: "adults", Kind: KindView})
		if !ferret && (err != nil || !strings.Contains(def, `"viewOn":"people"`)) {
			t.Fatal(def, err)
		}
	})

	t.Run("browse", func(t *testing.T) {
		rs, err := c.FetchRows(ctx, tbl, Page{Sort: []Sort{{Column: "_id", Dir: Asc}}, Limit: 2}, []string{"_id", "name"})
		if err != nil {
			t.Fatal(err)
		}
		if rs.Columns[0] != "_id" || rs.Columns[1] != "name" || len(rs.Rows) != 2 {
			t.Fatalf("%+v", rs)
		}
		at := func(name string) Value { return rs.Rows[0][slices.Index(rs.Columns, name)] }
		if at("seen").Text != "2024-03-15T10:20:30.000Z" || at("big").Text != "9000000000" || at("tags").Text != `["a","b"]` || !rs.Rows[1][slices.Index(rs.Columns, "seen")].Null {
			t.Fatalf("%+v", rs.Rows[0])
		}
		if n, err := c.CountRows(ctx, tbl, Page{}, nil); err != nil || n != 4 {
			t.Fatal(n, err)
		}
		// Views count too, although they have no metadata to count from.
		if n, err := c.CountRows(ctx, TableRef{Schema: "gt_test", Name: "adults"}, Page{}, nil); !ferret && (err != nil || n != 2) {
			t.Fatal(n, err)
		}
	})

	t.Run("filters", func(t *testing.T) {
		names := func(p Page) []string {
			t.Helper()
			p.Sort = []Sort{{Column: "name", Dir: Asc}}
			rs, err := c.FetchRows(ctx, tbl, p, []string{"_id", "name", "age", "city", "seen", "address"})
			if err != nil {
				t.Fatal(err)
			}
			out := []string{}
			for _, row := range rs.Rows {
				out = append(out, row[slices.Index(rs.Columns, "name")].Text)
			}
			if n, err := c.CountRows(ctx, tbl, p, []string{"_id", "name", "age", "city", "seen", "address"}); err != nil || int(n) != len(out) {
				t.Fatalf("count %d %v, rows %v", n, err, out)
			}
			return out
		}
		for _, tc := range []struct {
			f    Filter
			want []string
		}{
			{Filter{Column: "age", Op: OpEq, Value: "29"}, []string{"Bob"}},
			{Filter{Column: "age", Op: OpEq, Value: "unknown"}, []string{"Carol"}},
			{Filter{Column: "age", Op: OpNe, Value: "29"}, []string{"Alice", "Carol", "Dave_x"}},
			{Filter{Column: "age", Op: OpGt, Value: "40"}, []string{"Alice", "Carol", "Dave_x"}}, // "unknown" > "40" as text
			{Filter{Column: "age", Op: OpBetween, Value: "30", Value2: "50"}, []string{"Alice"}},
			{Filter{Column: "age", Op: OpIn, Value: "29, 41"}, []string{"Alice", "Bob"}},
			{Filter{Column: "age", Op: OpNotIn, Value: "29,41"}, []string{"Carol", "Dave_x"}},
			{Filter{Column: "city", Op: OpIsNull}, []string{"Carol", "Dave_x"}}, // null or missing
			{Filter{Column: "city", Op: OpIsNotNull}, []string{"Alice", "Bob"}},
			{Filter{Column: "name", Op: OpContains, Value: "AL"}, []string{"Alice"}},
			{Filter{Column: "name", Op: OpContains, Value: "e_x"}, []string{"Dave_x"}},
			{Filter{Column: "name", Op: OpNotContain, Value: "a"}, []string{"Bob"}},
			{Filter{Column: "name", Op: OpBeginsWith, Value: "b"}, []string{"Bob"}},
			{Filter{Column: "name", Op: OpEndsWith, Value: "OL"}, []string{"Carol"}},
			{Filter{Column: "name", Op: OpLike, Value: "c%l"}, []string{"Carol"}},
			{Filter{Column: "name", Op: OpNotLike, Value: "%a%"}, []string{"Bob"}},
			{Filter{Column: "seen", Op: OpGt, Value: "2024-01-01"}, []string{"Alice"}},
			{Filter{Column: "age", Op: OpContains, Value: "29"}, []string{"Bob"}}, // finds numbers, as text
			{Filter{Column: AnyColumn, Op: OpContains, Value: "paris"}, []string{"Bob"}},
			{Filter{Column: AnyColumn, Op: OpContains, Value: "2024"}, []string{"Alice"}},
		} {
			// FerretDB: text searches match strings only.
			if ferret && (tc.f.Value == "29" && tc.f.Op == OpContains || tc.f.Value == "2024") {
				tc.want = nil
			}
			if got := names(Page{Filters: []Filter{tc.f}}); !slices.Equal(got, tc.want) {
				t.Errorf("%+v: got %v, want %v", tc.f, got, tc.want)
			}
		}
		if got := names(Page{RawWhere: `{ age: { $gt: 30 }, name: { $regex: "^a", $options: "i" } }`}); !slices.Equal(got, []string{"Alice"}) {
			t.Errorf("raw: %v", got)
		}
		if got := names(Page{RawWhere: `{ city: "Paris" }`, Filters: []Filter{{Column: "name", Op: OpEq, Value: "Alice"}}}); len(got) != 0 {
			t.Errorf("raw and filter: %v", got)
		}
	})

	t.Run("explain", func(t *testing.T) {
		plan := func(q string) *QueryPlan {
			t.Helper()
			r := must(q)
			if r.Plan == nil {
				t.Fatalf("%s: no plan", q)
			}
			return r.Plan
		}
		show := func(p *QueryPlan) string { b, _ := json.MarshalIndent(p, "", " "); return string(b) }
		is := func(n *int64, want int64) bool { return n != nil && *n == want }

		if ferret { // PostgreSQL's plan: its nodes, read as stages
			p := plan(`db.people.find({ city: "Berlin" }).explain()`)
			if !strings.Contains(p.Root.Stage, "Scan") {
				t.Fatal(show(p))
			}
			return
		}
		// An index lookup, executed: the counts are there.
		p := plan(`db.people.find({ name: "Alice" }).explain()`)
		if p.Verbosity != "executionStats" || !slices.Contains(p.Indexes, "name_1") || p.CollScan ||
			!is(p.NReturned, 1) || !is(p.DocsExamined, 1) || !is(p.KeysExamined, 1) {
			t.Fatal(show(p))
		}
		// The other form, planning only: no counts, and a collection scan.
		p = plan(`db.people.explain("queryPlanner").find({ city: "Berlin" })`)
		if !p.CollScan || len(p.Indexes) != 0 || p.DocsExamined != nil || p.NReturned != nil {
			t.Fatal(show(p))
		}
		// Sorting on a field without an index happens in memory.
		if p = plan(`db.people.find().sort({ age: -1 }).limit(2).explain("executionStats")`); !p.InMemorySort || !p.CollScan {
			t.Fatal(show(p))
		}
		// An aggregation finds its index through $match.
		p = plan(`db.people.aggregate([{ $match: { name: "Bob" } }, { $group: { _id: "$city", n: { $sum: 1 } } }]).explain()`)
		if !slices.Contains(p.Indexes, "name_1") || p.CollScan {
			t.Fatal(show(p))
		}
		// count and distinct, and explain(true).
		if p = plan(`db.people.explain().count({ name: "Bob" })`); !slices.Contains(p.Indexes, "name_1") {
			t.Fatal(show(p))
		}
		if p = plan(`db.people.explain(true).distinct("name")`); !slices.Contains(p.Indexes, "name_1") || p.Verbosity != "allPlansExecution" {
			t.Fatal(show(p))
		}
		// _id lookups are an index too, whatever the server calls the stage.
		if p = plan(`db.people.findOne({ _id: 2 }).explain()`); !slices.Contains(p.Indexes, "_id_") {
			t.Fatal(show(p))
		}
		// The raw document is shown as well.
		if r := must(`db.people.find().explain()`); r.Set == nil || !slices.Contains(r.Set.Columns, "queryPlanner") {
			t.Fatal(r.Set)
		}
		for _, bad := range []string{`db.people.find().explain("loud")`, `db.people.insertOne({}).explain()`, `db.people.explain()`, `db.people.find().explain().limit(1)`} {
			res, _ := c.Execute(ctx, bad, 0)
			if len(res) == 0 || res[0].Err == nil {
				t.Fatalf("%s: expected an error", bad)
			}
		}
	})

	t.Run("editing keeps types", func(t *testing.T) {
		edit := []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("1")}, Values: map[string]Value{
				"age": Text("42"), "name": Text("Alicia"), "seen": Text("2025-01-02 03:04:05"), "big": Text("7"),
				"tags": Text(`["c"]`), "address": Text(`{ zip: "99" }`), "city": NullValue,
			}},
			{Kind: ChangeDelete, Key: map[string]Value{"_id": Text("four")}},
			{Kind: ChangeInsert, Values: map[string]Value{"name": Text("Eve"), "age": Text("33"), "city": NullValue}},
		}
		preview, err := c.PreviewChanges(ctx, tbl, edit, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(preview[0], `db.people.updateOne({"_id":1}, {"$set":`) || preview[1] != `db.people.deleteOne({"_id":"four"});` || !strings.HasPrefix(preview[2], `db.people.insertOne(`) {
			t.Fatalf("%q", preview)
		}
		// What the review dialog shows parses as a command.
		for _, p := range preview {
			if _, err := parseShell(p); err != nil {
				t.Errorf("%s: %v", p, err)
			}
		}
		if err := c.ApplyChanges(ctx, tbl, edit, true); err != nil {
			t.Fatal(err)
		}
		r := must(`db.people.aggregate([{ $match: { _id: 1 } }, { $project: {
			age: { $type: "$age" }, seen: { $type: "$seen" }, big: { $type: "$big" }, tags: { $type: "$tags" }, address: { $type: "$address.zip" }, city: { $type: "$city" } } }])`)
		row := map[string]string{}
		for i, col := range r.Set.Columns {
			row[col] = r.Set.Rows[0][i].Text
		}
		for k, want := range map[string]string{"age": "int", "seen": "date", "big": "long", "tags": "array", "address": "string", "city": "null"} {
			if row[k] != want {
				t.Errorf("%s stored as %s, want %s", k, row[k], want)
			}
		}
		// The inserted row: a generated _id, numbers as numbers, nulls left out.
		r = must(`db.people.findOne({ name: "Eve" })`)
		if r.Set.Types[0] != "objectId" || r.Set.Rows[0][slices.Index(r.Set.Columns, "age")].Text != "33" || slices.Contains(r.Set.Columns, "city") {
			t.Fatalf("%+v", r.Set)
		}
		id := r.Set.Rows[0][0].Text
		if err := c.ApplyChanges(ctx, tbl, []Change{{Kind: ChangeDelete, Key: map[string]Value{"_id": Text(id)}}}, true); err != nil {
			t.Fatal(err) // by ObjectId, from the cell's text
		}
		// Bad text, a vanished row, and a duplicate are all reported.
		bad := []Change{{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("2")}, Values: map[string]Value{"active": Text("maybe")}}}
		if err := c.ApplyChanges(ctx, tbl, bad, true); err == nil || !strings.Contains(err.Error(), "true or false") {
			t.Fatal(err)
		}
		gone := []Change{{Kind: ChangeDelete, Key: map[string]Value{"_id": Text("999")}}}
		if err := c.ApplyChanges(ctx, tbl, gone, true); err == nil || !strings.Contains(err.Error(), "no longer exists") {
			t.Fatal(err)
		}
		dup := []Change{{Kind: ChangeInsert, Values: map[string]Value{"name": Text("Bob")}}}
		if err := c.ApplyChanges(ctx, tbl, dup, true); err == nil || !mongo.IsDuplicateKeyError(err) {
			t.Fatal(err)
		}
		// _id "1" and 1 at once: refuse to guess.
		must(`db.people.insertOne({ _id: "1", name: "text id" })`)
		if err := c.ApplyChanges(ctx, tbl, []Change{{Kind: ChangeDelete, Key: map[string]Value{"_id": Text("1")}}}, true); err == nil || !strings.Contains(err.Error(), "different types") {
			t.Fatal(err)
		}
		must(`db.people.deleteOne({ _id: "1" })`)
	})

	t.Run("query editor", func(t *testing.T) {
		res, err := c.Execute(ctx, `db.q.insertOne({ _id: 1, a: 1 }); db.q.insertOne({ _id: 2, a: 2 });
			db.q.find().sort({ a: -1 }).limit(1)
			db.q.countDocuments({ a: { $gt: 0 } })
			db.q.distinct("a")
			db.q.updateMany({}, { $inc: { a: 10 } })
			db.q.findOneAndUpdate({ _id: 1 }, { $set: { b: 1 } }, { returnDocument: "after" })
			db.q.aggregate([{ $group: { _id: null, total: { $sum: "$a" } } }])
			db.q.getIndexes()
			show collections
			db.q.deleteMany({})`, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if r.Err != nil {
				t.Fatalf("%s: %v", r.Statement, r.Err)
			}
		}
		if res[2].Set.Rows[0][1].Text != "2" || res[3].Set.Rows[0][0].Text != "2" || len(res[4].Set.Rows) != 2 || res[5].RowsAffected != 2 {
			t.Fatalf("%+v", res[2:6])
		}
		if res[6].Set.Rows[0][slices.Index(res[6].Set.Columns, "b")].Text != "1" || res[7].Set.Rows[0][1].Text != "23" || res[8].Set == nil {
			t.Fatalf("%+v", res[6:9])
		}
		if res[10].RowsAffected != 2 {
			t.Fatal(res[10].RowsAffected)
		}
		// The row cap, and stopping at the first error.
		must(`db.cap.insertMany([{ a: 1 }, { a: 2 }, { a: 3 }])`)
		res, _ = c.Execute(ctx, "db.cap.find()", 2)
		if len(res[0].Set.Rows) != 2 || !res[0].Truncated {
			t.Fatalf("%+v", res[0])
		}
		res, _ = c.Execute(ctx, "db.cap.nonsense(); db.cap.drop()", 0)
		if len(res) != 1 || res[0].Err == nil {
			t.Fatalf("%+v", res)
		}
		// use switches the database for the statements after it, and the session.
		res, err = c.Execute(ctx, `use gt_copy; db.x.insertOne({ a: 1 }); db.x.countDocuments()`, 0)
		if err != nil || res[2].Err != nil || c.Schema() != "gt_copy" {
			t.Fatal(res, err, c.Schema())
		}
		must(`db.dropDatabase(); use gt_test`)
	})

	t.Run("structure", func(t *testing.T) {
		must(`db.s.insertMany([{ _id: 1, a: 1, b: 2 }, { _id: 2, a: 3 }])`)
		st := TableRef{Schema: "gt_test", Name: "s"}
		ops := []AlterOp{{Kind: AlterAddIndex, Index: &IndexDef{Columns: []string{"a"}}}}
		// A dry run shows the command and changes nothing.
		stmts, err := c.Alter(ctx, st, ops, true)
		if err != nil || len(stmts) != 1 || stmts[0] != `db.s.createIndex({"a":1});` {
			t.Fatal(stmts, err)
		}
		if idx, _ := c.Indexes(ctx, st); len(idx) != 1 {
			t.Fatal(idx)
		}
		if _, err := c.Alter(ctx, st, ops, false); err != nil {
			t.Fatal(err)
		}
		if idx, _ := c.Indexes(ctx, st); len(idx) != 2 {
			t.Fatal(idx)
		}
		if _, err := c.Alter(ctx, st, []AlterOp{{Kind: AlterDropIndex, Name: "a_1"}, {Kind: AlterDropColumn, Column: "b"},
			{Kind: AlterModifyColumn, Column: "a", Def: &ColumnDef{Name: "c"}}}, false); err != nil {
			t.Fatal(err)
		}
		cols, _ := c.Columns(ctx, st)
		if len(cols) != 2 || cols[1].Name != "c" {
			t.Fatalf("%+v", cols)
		}
		for _, op := range []AlterOp{{Kind: AlterDropColumn, Column: "_id"}, {Kind: AlterDropIndex, Name: "_id_"}, {Kind: AlterAddColumn}, {Kind: AlterAddCheck}} {
			if _, err := c.Alter(ctx, st, []AlterOp{op}, true); err == nil {
				t.Errorf("%s: expected an error", op.Kind)
			}
		}
		if _, err := c.Alter(ctx, st, []AlterOp{{Kind: AlterRenameTable, NewName: "s2"}}, false); err != nil {
			t.Fatal(err)
		}
		st.Name = "s2"
		if _, err := c.Alter(ctx, st, []AlterOp{{Kind: AlterTruncate}}, false); err != nil {
			t.Fatal(err)
		}
		if n, _ := c.CountRows(ctx, st, Page{}, nil); n != 0 {
			t.Fatal(n)
		}
		if _, err := c.CreateTable(ctx, "gt_test", NewTable{Name: "made"}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Drop(ctx, []Object{{Schema: "gt_test", Name: "made", Kind: KindTable}, {Schema: "gt_test", Name: "s2", Kind: KindTable}}, false); err != nil {
			t.Fatal(err)
		}
		objs, _ := c.Objects(ctx, "gt_test")
		for _, o := range objs {
			if o.Name == "made" || o.Name == "s2" {
				t.Fatalf("%s wasn't dropped", o.Name)
			}
		}
	})

	t.Run("index options", func(t *testing.T) {
		if ferret {
			t.Skip("FerretDB has no hidden indexes")
		}
		must(`db.ix.insertMany([{ _id: 1, at: ISODate("2024-01-01T00:00:00Z"), age: 30, bio: "likes go", loc: { type: "Point", coordinates: [13.4, 52.5] }, x: { y: 1 } }, { _id: 2, age: 10 }])`)
		ref := TableRef{Schema: "gt_test", Name: "ix"}
		ttl := int64(3600)
		add := func(x IndexDef) []string {
			t.Helper()
			stmts, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterAddIndex, Index: &x}}, false)
			if err != nil {
				t.Fatal(err)
			}
			return stmts
		}
		if st := add(IndexDef{Columns: []string{"at"}, Mongo: &MongoIndexOptions{ExpireAfterSeconds: &ttl}}); st[0] != `db.ix.createIndex({"at":1}, {"expireAfterSeconds":3600});` {
			t.Fatal(st)
		}
		add(IndexDef{Name: "adults", Columns: []string{"age", "_id"}, Orders: []string{"-1", "1"}, Mongo: &MongoIndexOptions{Partial: "{ age: { $gte: 18 } }", Sparse: false}})
		add(IndexDef{Columns: []string{"bio"}, Orders: []string{"text"}})
		add(IndexDef{Columns: []string{"loc"}, Orders: []string{"2dsphere"}, Mongo: &MongoIndexOptions{Sparse: true}})
		add(IndexDef{Columns: []string{"_id"}, Orders: []string{"hashed"}, Name: "h"})
		add(IndexDef{Columns: []string{"x.$**"}, Mongo: &MongoIndexOptions{Hidden: true}})

		idx, err := c.Indexes(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		by := map[string]Index{}
		for _, x := range idx {
			by[x.Name] = x
		}
		want := map[string][2]string{ // orders, options
			"at_1":         {"1", "TTL 1 h"},
			"adults":       {"-1,1", `partial {"age":{"$gte":18}}`},
			"bio_text":     {"text,1", ""},
			"loc_2dsphere": {"2dsphere", "sparse"},
			"h":            {"hashed", ""},
			"x.$**_1":      {"1", "hidden"},
		}
		for name, w := range want {
			x, ok := by[name]
			if !ok {
				t.Fatalf("no index %s in %+v", name, idx)
			}
			// A text index lists its weights field internally on some versions.
			orders := strings.Join(x.Orders, ",")
			if name == "bio_text" {
				orders = strings.Join(x.Orders[:min(2, len(x.Orders))], ",")
				if !strings.HasPrefix(orders, "text") {
					t.Fatalf("%s: %+v", name, x)
				}
			} else if orders != w[0] {
				t.Fatalf("%s: orders %s", name, orders)
			}
			if !strings.Contains(x.Options, w[1]) {
				t.Fatalf("%s: options %q", name, x.Options)
			}
		}
		if !by["x.$**_1"].Hidden {
			t.Fatal("not hidden")
		}
		// The partial index is used for adults only; hiding one keeps the planner off it.
		if p := must(`db.ix.find({ age: { $gte: 20 } }).sort({ age: -1 }).explain()`).Plan; !slices.Contains(p.Indexes, "adults") {
			t.Fatal(p.Indexes)
		}
		if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterHideIndex, Name: "adults"}}, false); err != nil {
			t.Fatal(err)
		}
		if p := must(`db.ix.find({ age: { $gte: 20 } }).sort({ age: -1 }).explain()`).Plan; slices.Contains(p.Indexes, "adults") {
			t.Fatal("a hidden index was used")
		}
		must(`db.ix.unhideIndex("adults")`)
		if x, _ := c.Indexes(ctx, ref); slices.ContainsFunc(x, func(i Index) bool { return i.Name == "adults" && i.Hidden }) {
			t.Fatal("still hidden")
		}
		// The definition makes the same indexes again.
		def, err := c.Definition(ctx, Object{Schema: "gt_test", Name: "ix", Kind: "table"})
		if err != nil || !strings.Contains(def, `"expireAfterSeconds"`) || !strings.Contains(def, `"partialFilterExpression"`) {
			t.Fatal(def, err)
		}
		for _, bad := range []IndexDef{
			{Columns: []string{"a"}, Orders: []string{"up"}},
			{Columns: []string{"a", "b"}, Mongo: &MongoIndexOptions{ExpireAfterSeconds: &ttl}},
			{Columns: []string{"a"}, Mongo: &MongoIndexOptions{Partial: "[1]"}},
			{Columns: []string{"$**"}, Orders: []string{"hashed"}},
		} {
			if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterAddIndex, Index: &bad}}, true); err == nil {
				t.Errorf("%+v: expected an error", bad)
			}
		}
		if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterHideIndex, Name: "_id_"}}, true); err == nil {
			t.Error("hid _id_")
		}
	})

	t.Run("validation", func(t *testing.T) {
		if ferret {
			t.Skip("FerretDB's $jsonSchema lacks required, so no rules to test")
		}
		must(`db.createCollection("v")`)
		ref := TableRef{Schema: "gt_test", Name: "v"}
		if info, err := c.TableInfo(ctx, ref); err != nil || info.Validator != "" {
			t.Fatal(info, err)
		}
		rules := `{ $jsonSchema: { bsonType: "object", required: ["name"], properties: { name: { bsonType: "string" }, born: { bsonType: "date" } } } }`
		stmts, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterSetValidation, Validation: &ValidationDef{Validator: rules}}}, false)
		if err != nil || !strings.HasPrefix(stmts[0], `db.runCommand({"collMod":"v","validator":{"$jsonSchema"`) {
			t.Fatal(stmts, err)
		}
		// The rules are kept, and enforced.
		info, err := c.TableInfo(ctx, ref)
		if err != nil || !strings.Contains(info.Validator, `"required"`) || info.ValidationLevel != "strict" || info.ValidationAction != "error" {
			t.Fatalf("%+v %v", info, err)
		}
		if res, _ := c.Execute(ctx, `db.v.insertOne({ age: 3 })`, 0); res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "validation") {
			t.Fatal("a document without a name was let in", res[0].Err)
		}
		must(`db.v.insertOne({ name: "ok", born: ISODate("2000-01-01T00:00:00Z") })`)
		// What is shown parses back: saving it unchanged, as a warning, lets the document in.
		if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterSetValidation, Validation: &ValidationDef{Validator: info.Validator, Level: "moderate", Action: "warn"}}}, false); err != nil {
			t.Fatal(err)
		}
		must(`db.v.insertOne({ age: 3 })`)
		if info, _ := c.TableInfo(ctx, ref); info.ValidationLevel != "moderate" || info.ValidationAction != "warn" || !strings.Contains(info.Validator, `"date"`) {
			t.Fatalf("%+v", info)
		}
		// Empty rules remove them.
		if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterSetValidation, Validation: &ValidationDef{}}}, false); err != nil {
			t.Fatal(err)
		}
		if info, _ := c.TableInfo(ctx, ref); info.Validator != "" {
			t.Fatal(info.Validator)
		}
		for _, bad := range []ValidationDef{{Validator: "[1]"}, {Validator: "{ a: "}, {Level: "loose"}, {Action: "shout"}} {
			if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterSetValidation, Validation: &bad}}, true); err == nil {
				t.Errorf("%+v: expected an error", bad)
			}
		}
		// Collection options read as words.
		must(`db.createCollection("capped", { capped: true, size: 1048576, max: 100 })`)
		if info, _ := c.TableInfo(ctx, TableRef{Schema: "gt_test", Name: "capped"}); info.Options != "capped at 1 MB, 100 documents" {
			t.Fatalf("%q", info.Options)
		}
	})

	t.Run("collection options", func(t *testing.T) {
		v, _ := c.ServerVersion(ctx) // "MongoDB 7.0.x"
		var major, minor int
		fmt.Sscanf(strings.TrimPrefix(v, "MongoDB "), "%d.%d", &major, &minor)
		day := int64(86400)
		create := func(name string, o NewCollectionOptions) []string {
			t.Helper()
			stmts, err := c.CreateTable(ctx, "gt_test", NewTable{Name: name, Mongo: &o})
			if err != nil {
				t.Fatal(name, err)
			}
			return stmts
		}
		info := func(name string) string {
			t.Helper()
			i, err := c.TableInfo(ctx, TableRef{Schema: "gt_test", Name: name})
			if err != nil {
				t.Fatal(err)
			}
			return i.Options
		}
		if st := create("capd", NewCollectionOptions{Capped: true, SizeBytes: 4096, MaxDocs: 2}); st[0] != `db.createCollection("capd", {"capped":true,"size":4096,"max":2})` {
			t.Fatal(st)
		}
		// A capped collection keeps only the newest documents (FerretDB
		// accepts the options but keeps them all).
		if !ferret {
			// A capped collection keeps only the newest documents.
			must(`db.capd.insertMany([{ i: 1 }, { i: 2 }, { i: 3 }])`)
			if r := must(`db.capd.find({}, { _id: 0 })`); len(r.Set.Rows) != 2 || r.Set.Rows[0][0].Text != "2" {
				t.Fatal(r.Set.Rows)
			}
			if o := info("capd"); o != "capped at 4 KB, 2 documents" {
				t.Fatal(o)
			}
		}
		if major >= 5 {
			create("ts", NewCollectionOptions{TimeField: "at", MetaField: "sensor", Granularity: "minutes", ExpireAfterSeconds: &day})
			must(`db.ts.insertOne({ at: ISODate("2024-01-01T00:00:00Z"), sensor: "a", v: 1 })`)
			if o := info("ts"); o != "time series on at, by sensor, minutes · documents expire after 1 d" {
				t.Fatal(o)
			}
		}
		if major > 5 || (major == 5 && minor >= 3) {
			create("cl", NewCollectionOptions{Clustered: true})
			if o := info("cl"); o != "clustered by _id" {
				t.Fatal(o)
			}
		}
		for _, bad := range []NewCollectionOptions{
			{Capped: true},
			{Capped: true, SizeBytes: 1, TimeField: "at"},
			{TimeField: "at", Clustered: true},
			{MetaField: "m"},
			{ExpireAfterSeconds: &day},
			{TimeField: "at", Granularity: "days"},
		} {
			if _, err := BuildCreateTable(c.Dialect, "gt_test", NewTable{Name: "x", Mongo: &bad}); err == nil {
				t.Errorf("%+v: expected an error", bad)
			}
		}
	})

	t.Run("export and import", func(t *testing.T) {
		must(`db.dump.insertMany([
			{ _id: ObjectId("507f1f77bcf86cd799439011"), s: "a;b\n'c'", n: NumberLong("5"), i: 7, d: 1.5, dec: NumberDecimal("1.50"), t: true, nul: null,
			  at: ISODate("2024-03-15T10:20:30.123Z"), u: UUID("0123456789abcdef0123456789abcdef"), bin: BinData(128, "AQID"), re: /a\/b/i,
			  nest: { x: [1, { y: NumberLong("2") }] } },
			{ _id: 2, extra: "only here" },
		])`)
		must(`db.dump.createIndex({ s: 1 }, { unique: true, sparse: true })`)
		objs := []Object{{Schema: "gt_test", Name: "dump", Kind: KindTable}}
		if !ferret {
			must(`db.createView("dumpview", "dump", [{ $match: { i: 7 } }])`)
			objs = append(objs, Object{Schema: "gt_test", Name: "dumpview", Kind: KindView})
		}
		var buf bytes.Buffer
		var last DumpProgress
		if err := c.Dump(ctx, &buf, DumpOptions{Objects: objs, Structure: true, Data: true, DropFirst: true, BatchSize: 1}, func(p DumpProgress) { last = p }); err != nil {
			t.Fatal(err)
		}
		if last.Rows != 2 {
			t.Fatalf("%+v", last)
		}
		before := must(`db.dump.aggregate([{ $match: { _id: { $type: "objectId" } } }, { $project: { types: { $map: { input: { $objectToArray: "$$ROOT" }, in: { k: "$$this.k", t: { $type: "$$this.v" } } } } } }])`)
		// Into another database, in full.
		must("use gt_copy")
		res, err := c.ImportSQL(ctx, bytes.NewReader(buf.Bytes()), ImportOptions{}, nil)
		if err != nil {
			t.Fatalf("%v\n%s", err, buf.String())
		}
		if res.Rows != 2 || res.Failed != 0 {
			t.Fatalf("%+v", res)
		}
		after := must(`db.dump.aggregate([{ $match: { _id: { $type: "objectId" } } }, { $project: { types: { $map: { input: { $objectToArray: "$$ROOT" }, in: { k: "$$this.k", t: { $type: "$$this.v" } } } } } }])`)
		if !reflect.DeepEqual(before.Set.Rows, after.Set.Rows) {
			t.Fatalf("types differ\n%v\n%v", before.Set.Rows, after.Set.Rows)
		}
		same := func(q string) {
			t.Helper()
			a := must(q)
			b := must("use gt_test; " + q)
			must("use gt_copy")
			if !reflect.DeepEqual(a.Set, b.Set) {
				t.Errorf("%s:\n%+v\n%+v", q, a.Set, b.Set)
			}
		}
		same("db.dump.find().sort({ _id: 1 })")
		same("db.dump.getIndexes()")
		objects, _ := c.Objects(ctx, "gt_copy")
		if ferret {
			if len(objects) != 1 || objects[0].Name != "dump" {
				t.Fatalf("%+v", objects)
			}
		} else if same("db.dumpview.find()"); len(objects) != 2 || objects[0].Name != "dump" || objects[1].Kind != KindView {
			t.Fatalf("%+v", objects)
		}
		// An import reports a failing statement, or skips it when told to.
		bad := "db.dump.drop()\ndb.dump.nonsense()\ndb.dump.insertOne({ a: 1 })\n"
		if _, err := c.ImportSQL(ctx, strings.NewReader(bad), ImportOptions{}, nil); err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Fatal(err)
		}
		r2, err := c.ImportSQL(ctx, strings.NewReader(bad), ImportOptions{ContinueOnError: true}, nil)
		if err != nil || r2.Failed != 1 || r2.Rows != 1 || r2.Errors[0].Line != 2 {
			t.Fatalf("%+v %v", r2, err)
		}
		must("db.dropDatabase(); use gt_test")

		// Table exports find fields the column sample doesn't list.
		var cols []string
		rows := 0
		err = c.StreamRows(ctx, TableRef{Schema: "gt_test", Name: "dump"}, Page{}, []string{"_id", "s"}, func(cs, _ []string, row []Value) error {
			cols = cs
			rows++
			return nil
		})
		if err != nil || rows != 2 || !slices.Contains(cols, "extra") || !slices.Contains(cols, "nest") {
			t.Fatal(cols, rows, err)
		}
	})

	t.Run("json import", func(t *testing.T) {
		ref := TableRef{Schema: "gt_test", Name: "ji"}
		imp := func(text string, o JSONImportOptions) (JSONImportResult, error) {
			return c.ImportJSON(ctx, strings.NewReader(text), ref, o, nil)
		}
		// Compass's export: an array of relaxed or canonical Extended JSON.
		res, err := imp(`[
			{"_id": {"$oid": "65f000000000000000000001"}, "at": {"$date": "2024-01-01T00:00:00Z"}, "n": {"$numberLong": "5"}, "f": 1.5, "i": 7, "tags": ["a"], "sub": {"x": null}},
			{"name": "no id"}
		]`, JSONImportOptions{})
		if err != nil || res.Inserted != 2 {
			t.Fatal(res, err)
		}
		types := must(`db.ji.aggregate([{ $match: { at: { $exists: true } } }, { $project: { _id: { $type: "$_id" }, at: { $type: "$at" }, n: { $type: "$n" }, f: { $type: "$f" }, i: { $type: "$i" }, tags: { $type: "$tags" }, sub: { $type: "$sub" } } }])`)
		var got []string
		for i, col := range types.Set.Columns {
			got = append(got, col+"="+types.Set.Rows[0][i].Text)
		}
		if strings.Join(got, " ") != "_id=objectId at=date n=long f=double i=int tags=array sub=object" {
			t.Fatal(got)
		}
		// mongoexport's: one a line, with a byte order mark and blank lines; truncating first.
		res, err = imp("\uFEFF{\"_id\": 1, \"v\": \"a\"}\n\n{\"_id\": 2, \"v\": \"b\"}\n", JSONImportOptions{Truncate: true})
		if err != nil || res.Inserted != 2 {
			t.Fatal(res, err)
		}
		if n, _ := c.CountRows(ctx, ref, Page{}, nil); n != 2 {
			t.Fatal(n)
		}
		// A duplicate _id stops the import there, saying where…
		res, err = imp(`[{"_id": 3}, {"_id": 1}, {"_id": 4}]`, JSONImportOptions{})
		if err == nil || !strings.Contains(err.Error(), "documents 1–3") || !strings.Contains(strings.ToLower(err.Error()), "duplicate key") || res.Inserted != 1 {
			t.Fatal(res, err)
		}
		// … or is skipped and reported.
		res, err = imp(`[{"_id": 5}, {"_id": 2}, {"_id": 6}]`, JSONImportOptions{ContinueOnError: true})
		if err != nil || res.Inserted != 2 || res.Failed != 1 || len(res.Errors) != 1 || res.Errors[0].Statement != "document 2" {
			t.Fatal(res, err)
		}
		// Many documents go in batches.
		var b strings.Builder
		for i := range 2500 {
			fmt.Fprintf(&b, "{\"k\": %d}\n", i)
		}
		var calls int
		res, err = c.ImportJSON(ctx, strings.NewReader(b.String()), TableRef{Schema: "gt_test", Name: "jbig"}, JSONImportOptions{}, func(JSONImportResult) { calls++ })
		if err != nil || res.Inserted != 2500 || calls != 3 {
			t.Fatal(res, err, calls)
		}
		// Export and import again: canonical keeps every type; relaxed keeps
		// what JSON can say (int and long both read back as numbers).
		must(`db.rt.insertOne({ _id: ObjectId("65f000000000000000000009"), at: ISODate("2024-01-01T00:00:00Z"), i: NumberInt(1), l: NumberLong("2"), d: 1.5, dec: NumberDecimal("1.10"), bin: BinData(0, "AQI="), re: /a+/i, sub: { x: [1, "b", null] } })`)
		rtTypes := func(coll string) string {
			r := must(`db.` + coll + `.aggregate([{ $project: { _id: 0, t: { $map: { input: { $objectToArray: "$$ROOT" }, in: { $concat: ["$$this.k", "=", { $type: "$$this.v" }] } } } } }])`)
			return r.Set.Rows[0][0].Text
		}
		want := rtTypes("rt")
		for _, canonical := range []bool{true, false} {
			var buf bytes.Buffer
			if err := c.ExportJSON(ctx, &buf, TableRef{Schema: "gt_test", Name: "rt"}, Page{}, nil, canonical, nil); err != nil {
				t.Fatal(err)
			}
			to := fmt.Sprintf("rt_%v", canonical)
			if _, err := c.ImportJSON(ctx, &buf, TableRef{Schema: "gt_test", Name: to}, JSONImportOptions{}, nil); err != nil {
				t.Fatal(err)
			}
			got := rtTypes(to)
			if canonical && got != want {
				t.Fatalf("canonical:\n%s\n%s", got, want)
			}
			// Relaxed writes a small long as a plain number, which reads back as int.
			if !canonical && got != strings.Replace(want, "l=long", "l=int", 1) {
				t.Fatalf("relaxed:\n%s\n%s", got, want)
			}
		}
		// A filtered page, and chosen fields.
		var buf bytes.Buffer
		p := Page{Filters: []Filter{{Column: "v", Op: OpEq, Value: "b"}}}
		if err := c.ExportJSON(ctx, &buf, ref, p, []string{"v"}, false, nil); err != nil || buf.String() != "[\n  {\"v\":\"b\"}\n]\n" {
			t.Fatalf("%q %v", buf.String(), err)
		}
		buf.Reset()
		if err := c.ExportJSON(ctx, &buf, TableRef{Schema: "gt_test", Name: "none"}, Page{}, nil, false, nil); err != nil || buf.String() != "[]\n" {
			t.Fatalf("%q %v", buf.String(), err)
		}
		for text, want := range map[string]string{
			``:                    "empty",
			`5`:                   "starts with [ or {",
			`[{"a": 1}, 5]`:       "document 2",
			`{"a": 1} {"b": `:     "document 2",
			`[{"a": 1}`:           "after document 1",
			`[{"d": {"$date": 1}`: "document 1",
			`[]`:                  "no documents",
		} {
			if _, err := imp(text, JSONImportOptions{}); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%q: %v, want %q", text, err, want)
			}
		}
	})

	t.Run("schema analysis", func(t *testing.T) {
		must(`db.sa.insertMany([
			{ _id: 1, name: "a", age: 30, city: "Berlin", at: ISODate("2024-01-01T00:00:00Z"), addr: { zip: "1", geo: { lat: 1 } }, tags: ["x", "y"], items: [{ sku: "p", n: 1 }] },
			{ _id: 2, name: "b", age: "unknown", city: "Berlin", at: ISODate("2024-06-01T00:00:00Z"), addr: { zip: "2" }, tags: [] },
			{ _id: 3, name: "c", age: 45.5, city: "Paris", addr: null, tags: ["x"], items: [{ sku: "q" }, { sku: "p", n: 2 }] },
			{ _id: 4, name: "d", city: null },
		])`)
		rep, err := c.AnalyzeSchema(ctx, TableRef{Schema: "gt_test", Name: "sa"}, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Sampled != 4 || rep.Total != 4 {
			t.Fatal(rep.Sampled, rep.Total)
		}
		by := map[string]FieldReport{}
		var paths []string
		for _, f := range rep.Fields {
			by[f.Path] = f
			paths = append(paths, f.Path)
		}
		show := func(f FieldReport) string { b, _ := json.Marshal(f); return string(b) }
		// Children follow their parent, in the order fields first appear.
		if got := strings.Join(paths, " "); got != "_id name age city at addr addr.zip addr.geo addr.geo.lat tags tags[] items items[] items[].sku items[].n" {
			t.Fatal(got)
		}
		types := func(f FieldReport) string {
			var s []string
			for _, x := range f.Types {
				s = append(s, fmt.Sprintf("%s:%d", x.Type, x.Count))
			}
			return strings.Join(s, ",")
		}
		check := func(path string, ok bool) {
			t.Helper()
			if !ok {
				t.Fatalf("%s: %s", path, show(by[path]))
			}
		}
		f := by["age"]
		check("age", f.Count == 3 && f.Of == 4 && types(f) == "double:1,int:1,string:1" && f.Min == "30" && f.Max == "45.5")
		f = by["city"]
		check("city", types(f) == "string:3,null:1" && len(f.Top) == 3 && f.Top[0] == ValueCount{"Berlin", 2} && !f.Unique)
		f = by["name"]
		check("name", f.Unique && f.Top == nil && f.Distinct == 4)
		f = by["at"]
		check("at", f.Count == 2 && f.Min == "2024-01-01T00:00:00Z" && f.Max == "2024-06-01T00:00:00Z")
		// Nested: out of the documents where the parent is a document.
		check("addr.zip", by["addr.zip"].Count == 2 && by["addr.zip"].Of == 2)
		check("addr.geo.lat", by["addr.geo.lat"].Of == 1)
		check("addr", types(by["addr"]) == "object:2,null:1")
		// Arrays: elements out of all elements; an empty array adds none.
		check("tags[]", by["tags[]"].Count == 3 && by["tags[]"].Of == 3 && by["tags[]"].Top[0] == ValueCount{"x", 2})
		check("items[].sku", by["items[].sku"].Count == 3 && by["items[].sku"].Of == 3)
		check("items[].n", by["items[].n"].Count == 2 && by["items[].n"].Of == 3)
		// A sample smaller than the collection.
		if rep, err := c.AnalyzeSchema(ctx, TableRef{Schema: "gt_test", Name: "sa"}, 2); err != nil || rep.Sampled != 2 {
			t.Fatal(rep, err)
		}
		// A view works too, and an empty collection says so.
		if !ferret {
			must(`db.createView("sav", "sa", [{ $match: { city: "Berlin" } }])`)
			if rep, err := c.AnalyzeSchema(ctx, TableRef{Schema: "gt_test", Name: "sav"}, 1000); err != nil || rep.Sampled != 2 {
				t.Fatal(rep, err)
			}
		}
		must(`db.createCollection("saempty")`)
		if rep, err := c.AnalyzeSchema(ctx, TableRef{Schema: "gt_test", Name: "saempty"}, 1000); err != nil || rep.Sampled != 0 || len(rep.Fields) != 0 {
			t.Fatal(rep, err)
		}
	})

	t.Run("nested edits keep types", func(t *testing.T) {
		must(`db.nest.insertOne({ _id: 1, sub: { big: NumberLong("5"), d: 2.0, i: NumberInt(1), at: ISODate("2024-01-01T00:00:00Z"), dec: NumberDecimal("1.10"), arr: [NumberLong("7"), 1.5] }, list: [NumberLong("9"), 3.0] })`)
		ref := TableRef{Schema: "gt_test", Name: "nest"}
		cells := func() (sub, list string) {
			t.Helper()
			rs, err := c.FetchRows(ctx, ref, Page{Limit: 10}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i, cn := range rs.Columns {
				switch cn {
				case "sub":
					sub = rs.Rows[0][i].Text
				case "list":
					list = rs.Rows[0][i].Text
				}
			}
			return
		}
		types := func() string {
			return must(`db.nest.aggregate([{ $project: { _id: 0, t: [{ $type: "$sub.big" }, { $type: "$sub.d" }, { $type: "$sub.i" }, { $type: "$sub.at" }, { $type: "$sub.dec" }, { $type: { $arrayElemAt: ["$sub.arr", 0] } }, { $type: { $arrayElemAt: ["$list", 0] } }, { $type: { $arrayElemAt: ["$list", 1] } }] } }])`).Set.Rows[0][0].Text
		}
		save := func(vals map[string]Value) {
			t.Helper()
			if err := c.ApplyChanges(ctx, ref, []Change{{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("1")}, Values: vals}}, true); err != nil {
				t.Fatal(err)
			}
		}
		want := `["long","double","int","date","decimal","long","long","double"]`
		if got := types(); got != want {
			t.Fatal(got)
		}
		// Saved back unchanged, as an edit of a sibling field does.
		sub, list := cells()
		save(map[string]Value{"sub": Text(sub), "list": Text(list)})
		if got := types(); got != want {
			t.Fatalf("unchanged save: %s", got)
		}
		// One nested value edited, one added: the rest keep their types, and
		// an edited long stays a long.
		sub, _ = cells()
		save(map[string]Value{"sub": Text(strings.Replace(strings.Replace(sub, `"big":5`, `"big":6`, 1), `{`, `{"new":1,`, 1))})
		if got := types(); got != want {
			t.Fatalf("edit: %s", got)
		}
		if r := must(`db.nest.findOne({ "sub.big": NumberLong("6"), "sub.new": NumberInt(1) })`); len(r.Set.Rows) != 1 {
			t.Fatal("the edit wasn't saved as written")
		}
		// Naming a type on purpose still changes it.
		// (FerretDB doesn't store an array whose numbers are equal to the old
		// ones, 9 for NumberLong 9: the long stays.)
		save(map[string]Value{"list": Text(`[NumberInt(9), 3.0]`)})
		if r := must(`db.nest.aggregate([{ $project: { _id: 0, t: { $type: { $arrayElemAt: ["$list", 0] } } } }])`); !ferret && r.Set.Rows[0][0].Text != "int" {
			t.Fatal(r.Set.Rows)
		}
	})

	t.Run("csv import", func(t *testing.T) {
		must(`db.csv.insertOne({ _id: 0, zip: "01000", qty: 1, on: ISODate("2020-01-01"), note: "x" })`)
		ref := TableRef{Schema: "gt_test", Name: "csv"}
		data := "zip,qty,on,note,new\n01234,5,2024-03-15,,3\n,6,2024-03-16,NULL,abc\n"
		n, err := c.ImportCSV(ctx, strings.NewReader(data), ref, CSVImportOptions{Header: true, EmptyAsNull: true, NullText: "NULL",
			Columns: []string{"zip", "qty", "on", "note", "new"}, Mapping: []int{0, 1, 2, 3, 4}}, nil)
		if err != nil || n != 2 {
			t.Fatal(n, err)
		}
		r := must(`db.csv.find({ qty: 5 }, { _id: 0 })`)
		row := map[string]Value{}
		for i, col := range r.Set.Columns {
			row[col] = r.Set.Rows[0][i]
		}
		ty := map[string]string{}
		for i, col := range r.Set.Columns {
			ty[col] = r.Set.Types[i]
		}
		if row["zip"].Text != "01234" || ty["zip"] != "string" || ty["qty"] != "int" || ty["on"] != "date" || ty["new"] != "int" || slices.Contains(r.Set.Columns, "note") {
			t.Fatalf("%+v %+v", row, ty)
		}
		if rows := must(`db.csv.find({ new: "abc" })`).Set.Columns; slices.Contains(rows, "note") || slices.Contains(rows, "zip") {
			t.Fatalf("empty and NULL fields should be left out: %v", rows)
		}
		if _, err := c.ImportCSV(ctx, strings.NewReader("qty\nnope\n"), ref, CSVImportOptions{Header: true, Columns: []string{"qty"}, Mapping: []int{0}}, nil); err == nil {
			t.Fatal("expected an error for text in a number field")
		}
		if n, err := c.ImportCSV(ctx, strings.NewReader("qty\n1\n"), ref, CSVImportOptions{Header: true, Truncate: true, Columns: []string{"qty"}, Mapping: []int{0}}, nil); err != nil || n != 1 {
			t.Fatal(n, err)
		}
		if n, _ := c.CountRows(ctx, ref, Page{}, nil); n != 1 {
			t.Fatal(n)
		}
	})

	t.Run("commit", func(t *testing.T) {
		must(`db.tx.insertMany([{ _id: 1, a: 1 }, { _id: 2, a: 2 }])`)
		must(`db.tx.createIndex({ a: 1 }, { unique: true })`)
		ref := TableRef{Schema: "gt_test", Name: "tx"}
		// The second change collides with a unique index.
		err := c.ApplyChanges(ctx, ref, []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("1")}, Values: map[string]Value{"a": Text("10")}},
			{Kind: ChangeInsert, Values: map[string]Value{"a": Text("2")}},
		}, true)
		if err == nil {
			t.Fatal("expected a duplicate key")
		}
		first := must(`db.tx.findOne({ _id: 1 })`).Set.Rows[0][1].Text
		if c.mongoTxn {
			if first != "1" {
				t.Fatalf("a replica set should have rolled back, a = %s", first)
			}
		} else if first != "10" || !strings.Contains(err.Error(), "before it were applied") {
			t.Fatalf("a standalone server keeps and reports the first change: a = %s, %v", first, err)
		}
	})

	// Through mongos, in a collection spread over shards by a hashed key.
	t.Run("sharded collection", func(t *testing.T) {
		if name != "MONGOSHARD" {
			t.Skip("needs a sharded cluster")
		}
		must(`db.adminCommand({ enableSharding: "gt_test" })`)
		must(`db.adminCommand({ shardCollection: "gt_test.sh", key: { k: "hashed" } })`)
		must(`db.sh.insertMany([{ _id: 1, k: "a", v: 1 }, { _id: 2, k: "b", v: 2 }])`)
		ref := TableRef{Schema: "gt_test", Name: "sh"}
		if obj, err := c.Objects(ctx, "gt_test"); err != nil || !slices.ContainsFunc(obj, func(o Object) bool { return o.Name == "sh" }) {
			t.Fatal(obj, err)
		}
		// A failing second change rolls back the first, across mongos.
		err := c.ApplyChanges(ctx, ref, []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("1")}, Values: map[string]Value{"v": Text("10")}},
			{Kind: ChangeInsert, Values: map[string]Value{"_id": Text("2"), "k": Text("b")}},
		}, true)
		if err == nil || !strings.Contains(err.Error(), "duplicate key") {
			t.Fatal(err)
		}
		if v := must(`db.sh.findOne({ _id: 1 })`).Set.Rows[0][2].Text; v != "1" {
			t.Fatalf("not rolled back: v = %s", v)
		}
		// Edits find documents by _id alone, without the shard key.
		if err := c.ApplyChanges(ctx, ref, []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"_id": Text("1")}, Values: map[string]Value{"v": Text("10")}},
			{Kind: ChangeInsert, Values: map[string]Value{"_id": Text("3"), "k": Text("c"), "v": Text("3")}},
			{Kind: ChangeDelete, Key: map[string]Value{"_id": Text("2")}},
		}, true); err != nil {
			t.Fatal(err)
		}
		r := must(`db.sh.find({}, { _id: 1, v: 1 }).sort({ _id: 1 })`)
		var got []string
		for _, row := range r.Set.Rows {
			got = append(got, row[0].Text+"="+row[1].Text)
		}
		if strings.Join(got, ",") != "1=10,3=3" {
			t.Fatal(got)
		}
		if n, err := c.CountRows(ctx, ref, Page{}, nil); err != nil || n != 2 {
			t.Fatal(n, err)
		}
		// The plan shows each shard, and the totals add theirs up.
		p := must(`db.sh.find().explain()`).Plan
		var shards []string
		var walk func(s PlanStage)
		walk = func(s PlanStage) {
			if s.Stage == "SHARD" {
				shards = append(shards, s.Shard)
			}
			for _, ch := range s.Children {
				walk(ch)
			}
		}
		walk(p.Root)
		if len(shards) != 2 || p.DocsExamined == nil || *p.DocsExamined != 2 || !p.CollScan {
			b, _ := json.Marshal(p)
			t.Fatal(shards, string(b))
		}
	})

	t.Run("cancel", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		res, err := c.ExecuteIn(cctx, "", "db.people.find()", 0)
		if err == nil && res[0].Err == nil {
			t.Fatal("expected a cancelled query to fail")
		}
		if err := c.Ping(ctx); err != nil {
			t.Fatal(err)
		}
	})

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Objects(ctx, "gt_test"); err != ErrClosed {
		t.Fatal(err)
	}
}

// TestMongoDBTLS connects to a server that requires TLS, with the test CA
// scripts/test-dbs.sh made: each SSL mode, and X.509 login by certificate.
func TestMongoDBTLS(t *testing.T) {
	base, pw := serverConfig(t, "MONGOTLS", model.DriverMongoDB)
	ca, client := os.Getenv("GOTABLE_TEST_MONGOTLS_CA"), os.Getenv("GOTABLE_TEST_MONGOTLS_CLIENT")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	open := func(edit func(c *model.Connection), pw string) (*Conn, error) {
		cfg := *base
		cfg.Options = "serverSelectionTimeoutMS=3000"
		edit(&cfg)
		return Open(ctx, &cfg, Credentials{Password: pw})
	}
	ok := func(c *Conn, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		res, err := c.Execute(ctx, "db.runCommand({ connectionStatus: 1 })", 0)
		if err != nil || res[0].Err != nil {
			t.Fatal(err, res)
		}
	}
	fails := func(want string, c *Conn, err error) {
		t.Helper()
		if err == nil {
			c.Close()
			t.Fatal("connected")
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q in: %v", want, err)
		}
	}

	// The server refuses a plain connection, as soon as the timeout in
	// Options says: it overrides GoTable's default of 15 seconds.
	start := time.Now()
	_, err := open(func(c *model.Connection) { c.SSLMode = "disable" }, pw)
	fails("", nil, err)
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("serverSelectionTimeoutMS ignored: gave up after %v", d)
	}
	// require checks the certificate: against the system's CAs it fails…
	c, err := open(func(c *model.Connection) { c.SSLMode = "require" }, pw)
	fails("certificate", c, err)
	// … against the given CA it passes, by host name or by address.
	ok(open(func(c *model.Connection) { c.SSLMode, c.SSLCAPath = "require", ca }, pw))
	ok(open(func(c *model.Connection) { c.SSLMode, c.SSLCAPath, c.Host = "require", ca, "localhost" }, pw))
	// skip-verify takes any certificate.
	ok(open(func(c *model.Connection) { c.SSLMode = "skip-verify" }, pw))
	// A CA file without certificates is reported as such.
	c, err = open(func(c *model.Connection) { c.SSLMode, c.SSLCAPath = "require", client+".missing" }, pw)
	fails("no such file", c, err)

	// X.509: the client certificate is the login, with no user or password.
	x509 := func(c *model.Connection) {
		c.SSLMode, c.SSLCAPath, c.SSLCertPath, c.User = "require", ca, client, ""
		c.Options = "authMechanism=MONGODB-X509&serverSelectionTimeoutMS=3000"
	}
	c, err = open(x509, "")
	ok(c, err)
	c, _ = open(x509, "")
	defer c.Close()
	res, err := c.Execute(ctx, "db.runCommand({ connectionStatus: 1 })", 0)
	if err != nil || res[0].Err != nil {
		t.Fatal(err, res)
	}
	if got := fmt.Sprint(res[0].Set.Rows); !strings.Contains(got, "CN=gotable-client") {
		t.Fatalf("not logged in by certificate: %s", got)
	}
}

// TestMongoDBSRV connects by mongodb+srv:// URLs, as pasted from Atlas: the
// hosts, the replica set and authSource come from DNS. It needs the DNS
// server of scripts/test-mongo-srv.sh.
func TestMongoDBSRV(t *testing.T) {
	host := os.Getenv("GOTABLE_TEST_MONGOSRV_HOST")
	if host == "" {
		t.Skip("GOTABLE_TEST_MONGOSRV_HOST not set")
	}
	user, pw := os.Getenv("GOTABLE_TEST_MONGOSRV_USER"), os.Getenv("GOTABLE_TEST_MONGOSRV_PASSWORD")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, tc := range []struct{ path, schema string }{
		{"/?tls=false", "test"},
		// The database in the path is the one to open; the user is still
		// found in admin, as the TXT record says.
		{"/gt_srv?retryWrites=true&w=majority&tls=false", "gt_srv"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			cfg, urlPW, err := model.ParseURL("mongodb+srv://" + user + ":" + pw + "@" + host + tc.path)
			if err != nil || urlPW != pw || !cfg.SRV {
				t.Fatal(cfg, err)
			}
			c, err := Open(ctx, cfg, Credentials{Password: urlPW})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if c.Schema() != tc.schema {
				t.Fatalf("schema %q", c.Schema())
			}
			if !c.mongoTxn {
				t.Fatal("the replica set from TXT was not found")
			}
			res, err := c.Execute(ctx, `db.srv.insertOne({ a: 1 }); db.srv.countDocuments(); db.dropDatabase()`, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range res {
				if r.Err != nil {
					t.Fatal(r.Statement, r.Err)
				}
			}
		})
	}
	// A wrong password fails as such, not as a missing server.
	cfg, _, _ := model.ParseURL("mongodb+srv://" + user + ":wrong@" + host + "/?tls=false")
	if _, err := Open(ctx, cfg, Credentials{Password: "wrong"}); err == nil || !strings.Contains(err.Error(), "Authentication failed") {
		t.Fatal(err)
	}
}
