package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	dyn "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"gotable/internal/model"
)

var dynEvery = dynItem{
	"id": &dyn.AttributeValueMemberS{Value: "a"}, "s": &dyn.AttributeValueMemberS{Value: "it's; \"q\" \\ -- $$ `x` <b>\n\t ünï 🎉 /* c */"},
	"empty": &dyn.AttributeValueMemberS{Value: ""}, "n": &dyn.AttributeValueMemberN{Value: "12345678901234567890123456789012345678"},
	"b": &dyn.AttributeValueMemberB{Value: []byte{0, 1, 255}}, "yes": &dyn.AttributeValueMemberBOOL{Value: true},
	"nul": &dyn.AttributeValueMemberNULL{Value: true}, "ss": &dyn.AttributeValueMemberSS{Value: []string{"a", "b;'c"}},
	"ns": &dyn.AttributeValueMemberNS{Value: []string{"1", "2.5"}}, "bs": &dyn.AttributeValueMemberBS{Value: [][]byte{{1, 2}, {9}}},
	"l": &dyn.AttributeValueMemberL{Value: []dyn.AttributeValue{&dyn.AttributeValueMemberN{Value: "1"}, &dyn.AttributeValueMemberS{Value: "1"}, &dyn.AttributeValueMemberNULL{Value: true}}},
	"m": &dyn.AttributeValueMemberM{Value: dynItem{"odd \"key;--": &dyn.AttributeValueMemberSS{Value: []string{"x"}}, "b": &dyn.AttributeValueMemberB{Value: []byte{7}}}},
}

// TestDynamoValues: every type survives being shown and typed back, and
// being written as a PUT, split and read.
func TestDynamoValues(t *testing.T) {
	for name, v := range dynEvery {
		if dynType(v) == "NULL" {
			continue
		}
		back, err := dynCoerce(dynType(v), dynValue(v))
		if err != nil || !reflect.DeepEqual(back, v) {
			t.Errorf("%s: %q → %#v %v", name, dynValue(v).Text, back, err)
		}
	}
	put := dynPut("odd; \"t", dynEvery)
	spans := SplitStatements(model.DriverDynamoDB, put+"\n"+put)
	if len(spans) != 2 {
		t.Fatalf("%d statements in %s", len(spans), put)
	}
	m := dynPutRe.FindStringSubmatch(spans[0].SQL)
	if m == nil || dynUnquote(m[1]) != "odd; \"t" {
		t.Fatal(m)
	}
	av, err := dynParseJSON(m[2])
	if err != nil || !reflect.DeepEqual(av.(*dyn.AttributeValueMemberM).Value, dynEvery) {
		t.Fatalf("%v\n%#v", err, av)
	}
	for typ, bad := range map[string]string{"N": "lots", "BOOL": "maybe", "B": "xyz", "L": `{"a":1}`, "SS": `["a"]`, "M": "{", "NS": `{"$ns":["1"]}`} {
		if _, err := dynCoerce(typ, Text(bad)); err == nil {
			t.Errorf("%s took %q", typ, bad)
		}
	}
	for in, typ := range map[string]string{"5": "N", "true": "BOOL", "[1]": "L", `"5"`: "S", "hello": "S", "null": "S", "": "S"} {
		if av, err := dynCoerce("", Text(in)); err != nil || dynType(av) != typ {
			t.Errorf("%q read as %s, want %s (%v)", in, dynType(av), typ, err)
		}
	}
}

// TestDynamoLocalCredentials: the machine's AWS keys never sign a request
// to an endpoint of the user's own.
func TestDynamoLocalCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "REALKEY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "real")
	t.Setenv("AWS_SESSION_TOKEN", "realtoken")
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization")+r.Header.Get("X-Amz-Security-Token"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		json.NewEncoder(w).Encode(map[string]any{"TableNames": []string{}})
	}))
	defer srv.Close()
	cfg := model.NewConnection(model.DriverDynamoDB)
	cfg.Name = "local"
	if _, err := fmt.Sscanf(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"), "%d", &cfg.Port); err != nil {
		t.Fatal(err)
	}
	cfg.Host = "127.0.0.1"
	c, err := Open(context.Background(), cfg, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if len(seen) == 0 || strings.Contains(strings.Join(seen, " "), "REALKEY") || strings.Contains(strings.Join(seen, " "), "realtoken") {
		t.Fatal(seen)
	}
}

func TestDynamoDB(t *testing.T) {
	cfg, pw := serverConfig(t, "DYNAMODB", model.DriverDynamoDB)
	runCompatSuite(t, openTest(t, cfg, pw), compatCaps{
		version: "DynamoDB",
		setup: []string{`DROP TABLE IF EXISTS "gt_c"`, `DROP TABLE IF EXISTS "gt_copy"`, `CREATE TABLE "gt_c" ("id" N PARTITION KEY)`,
			`INSERT INTO "gt_c" VALUE {'id': 1, 'name': 'Alice', 'city': 'Berlin', 'score': 90}`,
			`INSERT INTO "gt_c" VALUE {'id': 2, 'name': 'Bob', 'city': 'Paris', 'score': 70}`,
			`INSERT INTO "gt_c" VALUE {'id': 3, 'name': 'Carol', 'score': 85}`,
			`INSERT INTO "gt_c" VALUE {'id': 4, 'name': 'Dave', 'city': 'Berlin', 'score': 60}`,
			`INSERT INTO "gt_c" VALUE {'id': 5, 'name': 'Eve_x', 'city': 'Rome', 'score': 99}`},
		edits: true, pk: true, csv: true, dump: true, keysOnly: true,
		editorOK:   "SELECT name FROM \"gt_c\" WHERE score > 80;\nSELECT id FROM \"gt_c\"",
		editorStop: "SELECT * FROM \"gt_c\" WHERE id = 1;\nSELECT * FROM gt_no_such_table;\nSELECT * FROM \"gt_c\"",
	})
}

func TestDynamoDBItems(t *testing.T) {
	cfg, pw := serverConfig(t, "DYNAMODB", model.DriverDynamoDB)
	c := openTest(t, cfg, pw)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	must := func(q string) Result {
		t.Helper()
		res, err := c.Execute(ctx, q, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if r.Err != nil {
				t.Fatalf("%s: %v", r.Statement, r.Err)
			}
		}
		return res[len(res)-1]
	}
	scan := func(name string) map[string]dynItem {
		t.Helper()
		out := map[string]dynItem{}
		if err := c.dynEach(ctx, name, "", func(it dynItem) (bool, error) {
			if bs, ok := it["bs"].(*dyn.AttributeValueMemberBS); ok { // a set has no order
				slices.SortFunc(bs.Value, bytes.Compare)
			}
			for _, a := range []string{"ss", "ns"} {
				switch x := it[a].(type) {
				case *dyn.AttributeValueMemberSS:
					slices.Sort(x.Value)
				case *dyn.AttributeValueMemberNS:
					slices.Sort(x.Value)
				}
			}
			out[dynJSON(it["id"])+dynJSON(it["at"])] = it
			return true, nil
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	must(`DROP TABLE IF EXISTS "gt_d"; DROP TABLE IF EXISTS "gt_d2"`)
	must(`CREATE TABLE "gt_d" WITH {"AttributeDefinitions": [{"AttributeName": "id", "AttributeType": "S"}, {"AttributeName": "at", "AttributeType": "N"}, {"AttributeName": "kind", "AttributeType": "S"}],
		"KeySchema": [{"AttributeName": "id", "KeyType": "HASH"}, {"AttributeName": "at", "KeyType": "RANGE"}], "BillingMode": "PAY_PER_REQUEST",
		"GlobalSecondaryIndexes": [{"IndexName": "by_kind", "KeySchema": [{"AttributeName": "kind", "KeyType": "HASH"}], "Projection": {"ProjectionType": "ALL"}}]}`)
	t.Cleanup(func() { c.Execute(context.Background(), `DROP TABLE "gt_d"; DROP TABLE "gt_d2"`, 0) })
	item := dynItem{"at": &dyn.AttributeValueMemberN{Value: "1"}}
	for k, v := range dynEvery {
		item[k] = v
	}
	must(dynPut("gt_d", item))
	must(`INSERT INTO "gt_d" VALUE {'id': 'b', 'at': 2, 'kind': 'k'}`)
	if got := scan("gt_d")[`"a"1`]; got == nil || dynJSON(&dyn.AttributeValueMemberM{Value: got}) != dynJSON(&dyn.AttributeValueMemberM{Value: item}) {
		t.Fatalf("\n%s\n%s", dynJSON(&dyn.AttributeValueMemberM{Value: got}), dynJSON(&dyn.AttributeValueMemberM{Value: item}))
	}
	ref := TableRef{Schema: "us-east-1", Name: "gt_d"}

	cols, err := c.Columns(ctx, ref)
	if err != nil || cols[0].Comment != "partition key" || cols[1].Comment != "sort key" || cols[1].Type != "N" {
		t.Fatal(cols, err)
	}
	if ix, err := c.Indexes(ctx, ref); err != nil || len(ix) != 2 || ix[1].Name != "by_kind" || ix[1].Columns[0] != "kind" {
		t.Fatal(ix, err)
	}

	// Each type keeps its type when edited; NULL removes the attribute.
	key := map[string]Value{"id": Text("a"), "at": Text("1")}
	upd := func(v map[string]Value) []Change { return []Change{{Kind: ChangeUpdate, Key: key, Values: v}} }
	if err := c.ApplyChanges(ctx, ref, upd(map[string]Value{"n": Text("7"), "yes": Text("false"), "ss": Text(`{"$ss":["z"]}`), "empty": NullValue,
		"m": Text(`{"deep":[1,"1",{"$ns":[3]}]}`), "s": Text("5"), "brand.new": Text("x")}), true); err != nil {
		t.Fatal(err)
	}
	got := scan("gt_d")[`"a"1`]
	for a, want := range map[string]string{"n": "N", "yes": "BOOL", "ss": "SS", "m": "M", "s": "S", "brand.new": "S", "b": "B", "bs": "BS", "empty": ""} {
		if ty := dynType(got[a]); ty != want {
			t.Errorf("%s is %q, want %q", a, ty, want)
		}
	}
	for _, bad := range []map[string]Value{{"n": Text("lots")}, {"at": Text("9")}, {"ss": Text(`["z"]`)}} {
		if err := c.ApplyChanges(ctx, ref, upd(bad), true); err == nil {
			t.Errorf("%v: expected an error", bad)
		}
	}

	// One transaction: a refused change takes the others back with it.
	err = c.ApplyChanges(ctx, ref, []Change{
		{Kind: ChangeUpdate, Key: key, Values: map[string]Value{"n": Text("999")}},
		{Kind: ChangeInsert, Values: map[string]Value{"id": Text("b"), "at": Text("2"), "kind": Text("dup")}}}, true)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatal(err)
	}
	if s := scan("gt_d"); dynJSON(s[`"a"1`]["n"]) != "7" || dynJSON(s[`"b"2`]["kind"]) != `"k"` {
		t.Fatal("half a commit was applied", s)
	}
	for _, k := range []ChangeKind{ChangeUpdate, ChangeDelete} {
		ch := Change{Kind: k, Key: map[string]Value{"id": Text("gone"), "at": Text("1")}, Values: map[string]Value{"n": Text("1")}}
		if err := c.ApplyChanges(ctx, ref, []Change{ch}, true); err == nil || !strings.Contains(err.Error(), "no longer exists") {
			t.Fatal(k, err)
		}
	}

	// A PartiQL condition runs on the server.
	if rs, err := c.FetchRows(ctx, ref, Page{RawWhere: `"id" = 'b'`}, nil); err != nil || len(rs.Rows) != 1 {
		t.Fatal(rs, err)
	}
	if n, err := c.CountRows(ctx, ref, Page{RawWhere: `"id" = 'nobody'`}, nil); err != nil || n != 0 {
		t.Fatal(n, err)
	}

	// Exported and loaded back: the same items, keys and indexes.
	var buf bytes.Buffer
	if err := c.Dump(ctx, &buf, DumpOptions{Objects: []Object{{Name: "gt_d", Kind: KindTable}}, Structure: true, Data: true}, nil); err != nil {
		t.Fatal(err)
	}
	dump := strings.ReplaceAll(buf.String(), `"gt_d"`, `"gt_d2"`)
	if res, err := c.ImportSQL(ctx, strings.NewReader(dump), ImportOptions{}, nil); err != nil || res.Rows != 2 {
		t.Fatalf("%+v %v\n%s", res, err, dump)
	}
	if a, b := scan("gt_d"), scan("gt_d2"); !reflect.DeepEqual(a, b) {
		t.Fatalf("\n%v\n%v", a, b)
	}
	if ix, _ := c.Indexes(ctx, TableRef{Name: "gt_d2"}); len(ix) != 2 {
		t.Fatal(ix)
	}

	// More than a page (1 MB) and a batch: paging, counting, emptying.
	var csv strings.Builder
	csv.WriteString("id,at,pad\n")
	for i := range 1500 {
		fmt.Fprintf(&csv, "p,%d,%s\n", i, strings.Repeat("x", 1000))
	}
	opts := CSVImportOptions{Header: true, Columns: []string{"id", "at", "pad"}, Mapping: []int{0, 1, 2}, Truncate: true}
	if n, err := c.ImportCSV(ctx, strings.NewReader(csv.String()), ref, opts, nil); err != nil || n != 1500 {
		t.Fatal(n, err)
	}
	if n, err := c.CountRows(ctx, ref, Page{}, nil); err != nil || n != 1500 {
		t.Fatal(n, err)
	}
	rs, err := c.FetchRows(ctx, ref, Page{Offset: 1400, Limit: 300}, nil)
	if err != nil || len(rs.Rows) != 100 || rs.Rows[0][1].Text != "1400" {
		t.Fatal(len(rs.Rows), err)
	}
	// The last record of a key wins, as in a spreadsheet read top to bottom.
	if n, err := c.ImportCSV(ctx, strings.NewReader("id,at,pad\nq,1,first\nq,1,second\n"), ref, CSVImportOptions{Header: true, Columns: []string{"id", "at", "pad"}, Mapping: []int{0, 1, 2}}, nil); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if v := dynJSON(scan("gt_d")[`"q"1`]["pad"]); v != `"second"` {
		t.Fatal(v)
	}
	// 150 changes are two transactions.
	var many []Change
	for i := range 150 {
		many = append(many, Change{Kind: ChangeDelete, Key: map[string]Value{"id": Text("p"), "at": Text(fmt.Sprint(i))}})
	}
	if err := c.ApplyChanges(ctx, ref, many, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterTruncate}}, false); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.CountRows(ctx, ref, Page{}, nil); n != 0 {
		t.Fatal(n)
	}
	if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterDropColumn, Column: "x"}}, true); err == nil {
		t.Fatal("dropped a column")
	}
}
