package db

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

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
	c.Host, c.SRV = "cluster.example", true
	if u, _ := mongoURI(c, c.Address(), "", false); u != "mongodb+srv://cluster.example/" {
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

// TestMongoDB covers GoTable on a real server.
func TestMongoDB(t *testing.T) {
	cfg, pw := serverConfig(t, "MONGODB", model.DriverMongoDB)
	cfg.DBName = "gt_test"
	cfg.Options = os.Getenv("GOTABLE_TEST_MONGODB_OPTIONS") // directConnection=true for a replica set in a container
	c := openTest(t, cfg, pw)
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
	defer c.Execute(context.Background(), `db.getSiblingDB("gt_test").dropDatabase(); db.getSiblingDB("gt_copy").dropDatabase()`, 0)

	// A wrong password is told apart from an unreachable server, and quickly.
	if cfg.User != "" {
		wrong := *cfg
		start := time.Now()
		_, err := Open(ctx, &wrong, Credentials{Password: "wrong"})
		if err == nil || !strings.Contains(err.Error(), "Authentication failed") || time.Since(start) > 10*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	}
	if v, err := c.ServerVersion(ctx); err != nil || !strings.HasPrefix(v, "MongoDB ") {
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
	must(`db.createView("adults", "people", [{ $match: { age: { $gte: 30 } } }, { $project: { name: 1 } }])`)

	t.Run("introspection", func(t *testing.T) {
		objs, err := c.Objects(ctx, "gt_test")
		if err != nil || len(objs) != 2 || objs[0].Name != "adults" || objs[0].Kind != KindView || objs[1].Kind != KindTable {
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
		if idx, err := c.Indexes(ctx, TableRef{Schema: "gt_test", Name: "adults"}); err != nil || len(idx) != 0 {
			t.Fatal(idx, err)
		}
		def, err := c.Definition(ctx, Object{Schema: "gt_test", Name: "people", Kind: KindTable})
		if err != nil || !strings.Contains(def, `db.createCollection("people")`) || !strings.Contains(def, `db.people.createIndex({"name":1}`) {
			t.Fatal(def, err)
		}
		def, err = c.Definition(ctx, Object{Schema: "gt_test", Name: "adults", Kind: KindView})
		if err != nil || !strings.Contains(def, `"viewOn":"people"`) {
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
		if n, err := c.CountRows(ctx, TableRef{Schema: "gt_test", Name: "adults"}, Page{}, nil); err != nil || n != 2 {
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
		if err := c.ApplyChanges(ctx, tbl, dup, true); err == nil || !strings.Contains(err.Error(), "E11000") {
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

	t.Run("export and import", func(t *testing.T) {
		must(`db.dump.insertMany([
			{ _id: ObjectId("507f1f77bcf86cd799439011"), s: "a;b\n'c'", n: NumberLong("5"), i: 7, d: 1.5, dec: NumberDecimal("1.50"), t: true, nul: null,
			  at: ISODate("2024-03-15T10:20:30.123Z"), u: UUID("0123456789abcdef0123456789abcdef"), bin: BinData(128, "AQID"), re: /a\/b/i,
			  nest: { x: [1, { y: NumberLong("2") }] } },
			{ _id: 2, extra: "only here" },
		])`)
		must(`db.dump.createIndex({ s: 1 }, { unique: true, sparse: true })`)
		must(`db.createView("dumpview", "dump", [{ $match: { i: 7 } }])`)
		objs := []Object{{Schema: "gt_test", Name: "dump", Kind: KindTable}, {Schema: "gt_test", Name: "dumpview", Kind: KindView}}
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
		same("db.dumpview.find()")
		objects, _ := c.Objects(ctx, "gt_copy")
		if len(objects) != 2 || objects[0].Name != "dump" || objects[1].Kind != KindView {
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
