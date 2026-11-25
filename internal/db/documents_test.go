package db

import (
	"context"
	"strings"
	"testing"

	"gotable/internal/model"
)

// docTest edits documents through the same calls the page makes.
type docTest struct {
	t *testing.T
	c *Conn
	r TableRef
}

func (d docTest) run(script string) {
	d.t.Helper()
	rs, err := d.c.ExecuteIn(context.Background(), d.r.Schema, script, 0)
	if err != nil {
		d.t.Fatalf("%s: %v", script, err)
	}
	for _, r := range rs {
		if r.Err != nil {
			d.t.Fatalf("%s: %v", r.Statement, r.Err)
		}
	}
}

func (d docTest) all() []Document {
	d.t.Helper()
	docs, err := d.c.FetchDocuments(context.Background(), d.r, Page{Limit: 100}, nil)
	if err != nil {
		d.t.Fatal(err)
	}
	return docs
}

func (d docTest) get(label string) Document {
	d.t.Helper()
	for _, x := range d.all() {
		if x.Label == label {
			return x
		}
	}
	d.t.Fatalf("no document %s", label)
	return Document{}
}

func (d docTest) edit(kind ChangeKind, doc Document, js string) (string, error) {
	ch := []Change{{Kind: kind, Doc: &DocEdit{ID: doc.ID, JSON: js, Original: doc.JSON}}}
	pre, err := d.c.PreviewChanges(context.Background(), d.r, ch, true)
	if err != nil {
		return "", err
	}
	return strings.Join(pre, "\n"), d.c.ApplyChanges(context.Background(), d.r, ch, true)
}

// replace edits a document by replacing some of its JSON.
func (d docTest) replace(label, from, to string) string {
	d.t.Helper()
	doc := d.get(label)
	if !strings.Contains(doc.JSON, from) {
		d.t.Fatalf("%s isn't in %s", from, doc.JSON)
	}
	pre, err := d.edit(ChangeUpdate, doc, strings.Replace(doc.JSON, from, to, 1))
	if err != nil {
		d.t.Fatalf("%s: %v", label, err)
	}
	return pre
}

func (d docTest) refused(label, js, want string) {
	d.t.Helper()
	doc := d.get(label)
	if js == "" {
		js = doc.JSON
	}
	if _, err := d.edit(ChangeUpdate, doc, js); err == nil || !strings.Contains(err.Error(), want) {
		d.t.Errorf("%s ← %s: %v, want %q", label, js, err, want)
	}
}

func (d docTest) want(label string, parts ...string) {
	d.t.Helper()
	js := d.get(label).JSON
	for _, p := range parts {
		if !strings.Contains(js, p) {
			d.t.Errorf("%s: no %s in %s", label, p, js)
		}
	}
}

func TestMongoDocuments(t *testing.T) {
	cfg, pw := serverConfig(t, "MONGODB", model.DriverMongoDB)
	cfg.DBName = "gt_docs"
	d := docTest{t, openTest(t, cfg, pw), TableRef{Schema: "gt_docs", Name: "people"}}
	d.run(`db.people.drop()`)
	d.run(`db.people.insertMany([
	  {_id: "a", long: NumberLong(5), big: NumberLong("9007199254740993"), int: NumberInt(5), dbl: 2.0, dec: NumberDecimal("1.10"),
	   at: ISODate("2026-01-02T03:04:05Z"), oid: ObjectId("507f1f77bcf86cd799439011"), bin: BinData(0, "AQI="), re: /x/i, nul: null,
	   nested: {long: NumberLong(7), list: [NumberLong(1), {z: 1.0}], "dotted.name": 1}, uuid: UUID("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11")},
	  {_id: "b", "a.b": 1, x: 1}, {_id: NumberLong(1), n: "long id"}, {_id: "1", n: "text id"}])`)
	ctx := context.Background()
	types := func() map[string]string {
		rs, err := d.c.ExecuteIn(ctx, "gt_docs", `db.people.aggregate([{$match: {_id: "a"}}, {$project: {_id: 0, long: {$type: "$long"}, big: {$type: "$big"}, int: {$type: "$int"}, dbl: {$type: "$dbl"}, dec: {$type: "$dec"}, at: {$type: "$at"}, oid: {$type: "$oid"}, bin: {$type: "$bin"}, re: {$type: "$re"}, nul: {$type: "$nul"}, nlong: {$type: "$nested.long"}, l0: {$type: {$arrayElemAt: ["$nested.list", 0]}}, uuid: {$type: "$uuid"}}}])`, 0)
		if err != nil || rs[0].Err != nil {
			t.Fatal(err, rs)
		}
		out := map[string]string{}
		for i, c := range rs[0].Set.Columns {
			out[c] = rs[0].Set.Rows[0][i].Text
		}
		return out
	}
	before := types()

	// Only what changed is written, and the rest keeps its exact type.
	pre := d.replace("a", `"int":5`, `"int":6,"added":{"k":[1,2]}`)
	if strings.Contains(pre, `"big"`) || !strings.Contains(pre, `"added"`) || !strings.Contains(pre, "$set") || strings.Contains(pre, "$unset") {
		t.Errorf("preview %s", pre)
	}
	d.want("a", `"int":6`, `"added":{"k":[1,2]}`, `9007199254740993`)
	// Inside a nested document too.
	d.replace("a", `"list":[1,{"z":1.0}]`, `"list":[1,{"z":1.0},"new"]`)
	after := types()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("type of %s became %s (was %s)", k, after[k], v)
		}
	}
	// A field goes, one gets another type on purpose.
	if pre := d.replace("a", `"nul":null,`, ``); !strings.Contains(pre, `"$unset":{"nul":""}`) {
		t.Errorf("unset %s", pre)
	}
	d.replace("a", `"dec":{"$numberDecimal":"1.10"}`, `"dec":"text now"`)
	if types()["dec"] != "string" {
		t.Error("dec isn't a string")
	}
	// Names $set would read as paths.
	if pre := d.replace("b", `"a.b":1`, `"a.b":2`); !strings.Contains(pre, "replaceOne") {
		t.Errorf("dotted %s", pre)
	}
	d.want("b", `"a.b":2`, `"x":1`)

	// What can't be, isn't.
	d.refused("a", strings.Replace(d.get("a").JSON, `"_id":"a"`, `"_id":"c"`, 1), "_id can't change")
	d.refused("a", "", "nothing to change")
	d.refused("a", `[1]`, "an object")
	d.refused("a", `{"_id":"a",`, "")
	d.refused("a", `{"_id":"a","x":1,"x":2}`, "twice")
	stale := d.get("a")
	d.run(`db.people.updateOne({_id: "a"}, {$set: {int: 99}})`)
	if _, err := d.edit(ChangeUpdate, stale, strings.Replace(stale.JSON, `"int":6`, `"int":7`, 1)); err != errDocChanged {
		t.Errorf("stale edit: %v", err)
	}
	if _, err := d.edit(ChangeDelete, stale, ""); err != errDocChanged {
		t.Errorf("stale delete: %v", err)
	}

	// _ids that look alike are told apart; new documents; deletes.
	var text, long Document
	for _, x := range d.all() {
		if strings.Contains(x.JSON, "text id") {
			text = x
		} else if strings.Contains(x.JSON, "long id") {
			long = x
		}
	}
	if text.ID == long.ID {
		t.Fatalf("ids %s %s", text.ID, long.ID)
	}
	if _, err := d.edit(ChangeDelete, text, ""); err != nil {
		t.Fatal(err)
	}
	d.want("1", "long id")
	if _, err := d.edit(ChangeInsert, Document{}, `{"_id": "new", "when": {"$date": "2026-05-06T00:00:00Z"}, "f": ISODate("2026-01-01")}`); err != nil {
		t.Fatal(err)
	}
	d.want("new", `"when":{"$date":"2026-05-06T00:00:00Z"}`, `"f":{"$date"`)
	// The filter and sort of the grid apply.
	docs, err := d.c.FetchDocuments(ctx, d.r, Page{Limit: 10, RawWhere: `{x: 1}`}, nil)
	if err != nil || len(docs) != 1 || docs[0].Label != "b" {
		t.Errorf("filtered %v %v", docs, err)
	}
	d.run(`db.people.drop()`)
}

func TestElasticDocuments(t *testing.T) {
	cfg, pw := serverConfig(t, "ELASTIC", model.DriverElasticsearch)
	d := docTest{t, openTest(t, cfg, pw), TableRef{Name: "gt_docs"}}
	d.c.ExecuteIn(context.Background(), "", "DELETE /gt_docs", 0)
	d.run("PUT /gt_docs/_doc/1?refresh=true\n{\"name\": \"Ann\", \"big\": 9007199254740993, \"nested\": {\"tags\": [\"a\", \"b\"], \"z\": 1.0}, \"gone\": 1}")
	if pre := d.replace("1", `"name"`, `"added": [{"x": null}, {"x": 1}], "name"`); !strings.HasPrefix(pre, "PUT /gt_docs/_doc/1 {") {
		t.Errorf("preview %s", pre)
	}
	d.want("1", "9007199254740993", `"added"`, "1.0")
	doc := d.get("1")
	if _, err := d.edit(ChangeUpdate, doc, `{"name": "only"}`); err != nil {
		t.Fatal(err)
	}
	if js := jsonCompact(d.get("1").JSON); js != `{"name":"only"}` {
		t.Errorf("replaced %s", js)
	}
	if _, err := d.edit(ChangeUpdate, doc, `{"name": "stale"}`); err != errDocChanged {
		t.Errorf("stale: %v", err)
	}
	d.refused("1", `[1]`, "JSON document")
	d.refused("1", "", "nothing to change")
	if _, err := d.edit(ChangeInsert, Document{}, `{"_id": "two", "name": "Bob"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.edit(ChangeInsert, Document{}, `{"name": "auto"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.edit(ChangeInsert, Document{}, `{"_id": "two", "name": "again"}`); err == nil {
		t.Error("the same _id twice")
	}
	d.want("two", "Bob")
	if _, err := d.edit(ChangeDelete, d.get("two"), ""); err != nil {
		t.Fatal(err)
	}
	if n := len(d.all()); n != 2 {
		t.Errorf("%d documents", n)
	}
	d.run("DELETE /gt_docs")
}

func TestDynamoDocuments(t *testing.T) {
	cfg, pw := serverConfig(t, "DYNAMODB", model.DriverDynamoDB)
	d := docTest{t, openTest(t, cfg, pw), TableRef{Name: "gt_docs"}}
	d.c.ExecuteIn(context.Background(), "", `DROP TABLE IF EXISTS "gt_docs"`, 0)
	d.run(`CREATE TABLE "gt_docs" ("id" S PARTITION KEY, "n" N SORT KEY)`)
	d.run(`INSERT INTO "gt_docs" VALUE {'id': 'a', 'n': 1, 'num': 12345678901234567890.123456789, 'ss': <<'x','y'>>, 'ns': <<1,2>>, 'm': {'l': [1, 'two', true, NULL]}, 'gone': 'x', 'b': false}`)
	label := "a · 1"
	pre := d.replace(label, `"b":false`, `"b":true,"new":{"k":[1]}`)
	if strings.Contains(pre, `"num"`) || !strings.Contains(pre, `SET "new"`) {
		t.Errorf("preview %s", pre)
	}
	d.want(label, "12345678901234567890.123456789", `"ss":{"$ss":["x","y"]}`, `"ns":{"$ns":[1,2]}`, `"new":{"k":[1]}`, `"b":true`, `{"id":"a","n":1,`)
	if pre := d.replace(label, `"gone":"x",`, ``); !strings.Contains(pre, `REMOVE "gone"`) {
		t.Errorf("remove %s", pre)
	}
	d.replace(label, `{"$ss":["x","y"]}`, `{"$ss":["x","z"]}`)
	d.want(label, `{"$ss":["x","z"]}`)
	d.replace(label, `"two"`, `2`) // a nested type change
	d.want(label, `[1,2,true,null]`)
	doc := d.get(label)
	d.run(`UPDATE "gt_docs" SET "b" = false WHERE "id" = 'a' AND "n" = 1`)
	if _, err := d.edit(ChangeUpdate, doc, strings.Replace(doc.JSON, `"b":true`, `"b":"z"`, 1)); err != errDocChanged {
		t.Errorf("stale: %v", err)
	}
	d.refused(label, strings.Replace(d.get(label).JSON, `"n":1`, `"n":2`, 1), "key")
	d.refused(label, "", "nothing to change")
	if _, err := d.edit(ChangeInsert, Document{}, `{"id": "b"}`); err == nil || !strings.Contains(err.Error(), "key n is missing") {
		t.Errorf("no sort key: %v", err)
	}
	if _, err := d.edit(ChangeInsert, Document{}, `{"id": "b", "n": "x"}`); err == nil || !strings.Contains(err.Error(), "of type N") {
		t.Errorf("wrong type: %v", err)
	}
	if _, err := d.edit(ChangeInsert, Document{}, `{"id": "b", "n": 2, "v": null}`); err != nil {
		t.Fatal(err)
	}
	// Sorted and filtered pages keep the documents with their rows.
	docs, err := d.c.FetchDocuments(context.Background(), d.r, Page{Limit: 10, Sort: []Sort{{Column: "id", Dir: Desc}}}, nil)
	if err != nil || len(docs) != 2 || docs[0].Label != "b · 2" || !strings.Contains(docs[1].JSON, `"id":"a"`) {
		t.Errorf("sorted %v %v", docs, err)
	}
	if _, err := d.edit(ChangeDelete, d.get("b · 2"), ""); err != nil {
		t.Fatal(err)
	}
	if n := len(d.all()); n != 1 {
		t.Errorf("%d items", n)
	}
	d.run(`DROP TABLE "gt_docs"`)
}

func TestRedisDocuments(t *testing.T) {
	cfg, pw := serverConfig(t, "REDIS", model.DriverRedis)
	cfg.DBName = "2"
	d := docTest{t, openTest(t, cfg, pw), TableRef{Schema: "db2", Name: "*"}}
	d.run("FLUSHDB")
	d.run("SET js '{\"a\": 1, \"b\": [1, 2]}'\nSET txt hello\nSET num 5\nHSET h f1 v1 f2 2\nEXPIRE h 5000\nRPUSH l a b c\nEXPIRE l 5000\nSADD s x y\nZADD z 1 one 2.5 two\nXADD st * k v\nSET bin \"\\xff\\xfe\"")
	for label, want := range map[string]string{"js": `{"a": 1, "b": [1, 2]}`, "txt": `"hello"`, "num": `"5"`, "h": `{"f1":"v1","f2":"2"}`,
		"l": `["a","b","c"]`, "s": `["x","y"]`, "z": `{"one":1,"two":2.5}`} {
		if got := d.get(label); got.JSON != want || got.ReadOnly != "" {
			t.Errorf("%s: %s %q", label, got.JSON, got.ReadOnly)
		}
	}
	if d.get("st").ReadOnly == "" || d.get("bin").ReadOnly == "" {
		t.Error("a stream and binary data are read only")
	}
	for _, e := range []struct{ label, js, cmds string }{
		{"js", `{"a": 2}`, `SET js "{\"a\": 2}" KEEPTTL`},
		{"txt", `"bye"`, `SET txt bye KEEPTTL`},
		{"num", `6`, `SET num 6 KEEPTTL`},
		{"h", `{"f2":"2","f3":true}`, "HSET h f3 true\nHDEL h f1"},
		{"l", `["a","B","c","d"]`, "RPUSH l a B c d\nLTRIM l 3 -1"},
		{"s", `["y","z"]`, "SADD s z\nSREM s x"},
		{"z", `{"two":3,"three":"-inf"}`, "ZADD z -inf three 3 two\nZREM z one"},
	} {
		doc := d.get(e.label)
		pre, err := d.edit(ChangeUpdate, doc, e.js)
		if err != nil {
			t.Errorf("%s: %v", e.label, err)
			continue
		}
		if strings.ReplaceAll(pre, ";", "") != e.cmds {
			t.Errorf("%s:\n%s\nwant\n%s", e.label, pre, e.cmds)
		}
	}
	d.want("l", `["a","B","c","d"]`)
	d.want("h", `{"f2":"2","f3":"true"}`)
	d.want("z", `"three":"-inf"`, `"two":3`)
	for _, k := range []string{"h", "l"} {
		if !strings.Contains(d.get(k).Meta, "expires in") {
			t.Errorf("%s lost its expiry: %s", k, d.get(k).Meta)
		}
	}
	// The same length changes in place; more is appended.
	doc := d.get("l")
	if pre, err := d.edit(ChangeUpdate, doc, `["a","b","c","d"]`); err != nil || pre != "LSET l 1 b" {
		t.Errorf("lset %q %v", pre, err)
	}
	if pre, err := d.edit(ChangeUpdate, d.get("l"), `["a","b","c","d","e"]`); err != nil || pre != "RPUSH l e" {
		t.Errorf("rpush %q %v", pre, err)
	}
	if _, err := d.edit(ChangeUpdate, doc, `["x"]`); err == nil || !strings.Contains(err.Error(), "changed by someone else") {
		t.Errorf("stale: %v", err)
	}
	d.refused("h", `{}`, "at least one field")
	d.refused("l", `{"a":1}`, "a list")
	d.refused("z", `{"a":"x"}`, "isn't a score")
	d.refused("txt", `null`, "null")
	d.refused("txt", "", "nothing to change")
	for _, js := range []string{`{"key":"n1","type":"hash","ttl":100,"value":{"a":"b"}}`, `{"key":"n2","type":"zset","ttl":null,"value":{"a":1}}`, `{"key":"n3","value":{"x":1}}`, `{"key":"n4","type":"set","value":["q"]}`} {
		if _, err := d.edit(ChangeInsert, Document{}, js); err != nil {
			t.Errorf("%s: %v", js, err)
		}
	}
	if x := d.get("n1"); x.JSON != `{"a":"b"}` || !strings.Contains(x.Meta, "expires") {
		t.Errorf("new hash %+v", x)
	}
	if _, err := d.edit(ChangeInsert, Document{}, `{"key":"n1","value":"x"}`); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("existing key: %v", err)
	}
	if _, err := d.edit(ChangeDelete, d.get("n1"), ""); err != nil {
		t.Fatal(err)
	}
	// One key by itself.
	one := docTest{t, d.c, TableRef{Schema: "db2", Name: "h"}}
	if docs := one.all(); len(docs) != 1 || docs[0].Label != "h" {
		t.Errorf("one key %+v", docs)
	}
	d.run("FLUSHDB")
}
