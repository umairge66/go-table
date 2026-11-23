package db

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"
)

func TestSplitRedisArgs(t *testing.T) {
	for line, want := range map[string][]string{
		`SET user:1 Alice`:            {"SET", "user:1", "Alice"},
		`  HSET  h  f   "two words" `: {"HSET", "h", "f", "two words"},
		`SET k "a\"b\\c\n\x41"`:       {"SET", "k", "a\"b\\c\nA"},
		`SET k 'it\'s' ''`:            {"SET", "k", "it's", ""},
		`SET k ""`:                    {"SET", "k", ""},
	} {
		got, err := splitRedisArgs(line)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q: %q %v", line, got, err)
		}
	}
	for _, bad := range []string{`SET k "open`, `SET k 'open`, `SET k "a"b`} {
		if _, err := splitRedisArgs(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	// What quoteRedisArg writes reads back the same.
	for _, s := range []string{"plain", "two words", "", `q"uote`, "back\\slash", "new\nline", "\x00\xffbin", "ünïcode", "it's"} {
		got, err := splitRedisArgs("X " + quoteRedisArg(s))
		if err != nil || len(got) != 2 || got[1] != s {
			t.Errorf("%q → %s → %q %v", s, quoteRedisArg(s), got, err)
		}
	}
	spans := redisStatements("# a comment\nSET a 1\n\n  GET a  \n// another\n")
	if len(spans) != 2 || spans[0].SQL != "SET a 1" || spans[1].SQL != "GET a" {
		t.Fatal(spans)
	}
}

func TestRedisPatterns(t *testing.T) {
	for k, lit := range map[string]bool{"user:*": false, "a?c": false, "[ab]": false, "user:1": true, `a\*b`: true} {
		if _, ok := literalKey(k); ok != lit {
			t.Errorf("%q literal = %v", k, ok)
		}
	}
	for _, key := range []string{"plain", "a*b", "q?[x]", `back\slash`} {
		if got, ok := literalKey(globEscape(key)); !ok || got != key {
			t.Errorf("%q → %q → %q %v", key, globEscape(key), got, ok)
		}
	}
	cols := []string{"key", "ttl"}
	rows := [][]Value{{Text("b"), Text("20")}, {Text("a"), NullValue}, {Text("c"), Text("100")}}
	got, _ := filterRows(cols, rows, []Filter{{Column: "ttl", Op: OpGt, Value: "9"}})
	if len(got) != 2 { // numbers compare as numbers: 100 > 9
		t.Fatal(got)
	}
	got, _ = filterRows(cols, rows, []Filter{{Column: "ttl", Op: OpIsNull}})
	if len(got) != 1 || got[0][0].Text != "a" {
		t.Fatal(got)
	}
	sortRows(cols, rows, []Sort{{Column: "ttl", Dir: Desc}})
	if rows[0][0].Text != "c" || rows[2][0].Text != "a" { // null first ascending, so last descending
		t.Fatal(rows)
	}
}

// TestRedis covers GoTable on a real Redis-protocol server (Redis, Valkey).
func TestRedis(t *testing.T) {
	ran := false
	for name, d := range map[string]model.Driver{"REDIS": model.DriverRedis, "VALKEY": model.DriverValkey,
		"DRAGONFLY": model.DriverDragonfly, "KEYDB": model.DriverKeyDB, "GARNET": model.DriverGarnet} {
		if os.Getenv("GOTABLE_TEST_"+name+"_HOST") == "" {
			continue
		}
		ran = true
		t.Run(strings.ToLower(name), func(t *testing.T) { testRedisServer(t, name, d) })
	}
	if !ran {
		t.Skip("GOTABLE_TEST_REDIS_HOST not set")
	}
}

func testRedisServer(t *testing.T, name string, d model.Driver) {
	cfg, pw := serverConfig(t, name, d)
	cfg.DBName = "1"
	c := openTest(t, cfg, pw)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	must := func(script string) []Result {
		t.Helper()
		res, err := c.Execute(ctx, script, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if r.Err != nil {
				t.Fatalf("%s: %v", r.Statement, r.Err)
			}
		}
		return res
	}
	cell := func(r Result, row, col int) string {
		t.Helper()
		if r.Set == nil || row >= len(r.Set.Rows) {
			t.Fatalf("%s: no row %d in %+v", r.Statement, row, r.Set)
		}
		return r.Set.Rows[row][col].Text
	}
	db1 := TableRef{Schema: "db1", Name: "*"}
	key := func(k string) TableRef { return TableRef{Schema: "db1", Name: globEscape(k)} }
	must("FLUSHDB")
	defer c.Execute(context.Background(), "FLUSHDB", 0)

	// A wrong password is reported as such.
	if pw != "" {
		wrong := *cfg
		if _, err := Open(ctx, &wrong, Credentials{Password: "nope"}); err == nil || !strings.Contains(strings.ToUpper(err.Error()), "WRONGPASS") {
			t.Fatal(err)
		}
	}
	if v, err := c.ServerVersion(ctx); err != nil || !strings.HasPrefix(v, strings.Fields(d.Label())[0]+" ") {
		t.Fatal(v, err)
	}
	if s, _ := c.Schemas(ctx); len(s) != 16 || s[0] != "db0" || c.Schema() != "db1" {
		t.Fatal(s, c.Schema())
	}

	// Garnet has no streams: there, events is a string.
	streams := d != model.DriverGarnet
	seedEvents := "XADD events 1-1 type login who alice"
	if !streams {
		seedEvents = "SET events none"
	}

	t.Run("query editor", func(t *testing.T) {
		res := must(`# a comment
SET user:1 Alice
HSET user:1:profile name Alice age 41
RPUSH queue a b c
SADD tags x y
ZADD scores 10 alice 20 bob
` + seedEvents + `
SET "odd*key" v
SET counter 5
EXPIRE counter 300
GET user:1
HGETALL user:1:profile
ZRANGE scores 0 -1 WITHSCORES
INCR counter
GET missing`)
		if cell(res[9], 0, 0) != "Alice" {
			t.Fatal(res[9].Set)
		}
		if h := res[10].Set; !slices.Equal(h.Columns, []string{"field", "value"}) || len(h.Rows) != 2 {
			t.Fatal(h)
		}
		if z := res[11].Set; z.Columns[1] != "score" || z.Rows[1][0].Text != "bob" || z.Rows[1][1].Text != "20" {
			t.Fatal(z)
		}
		if cell(res[12], 0, 0) != "6" || !res[13].Set.Rows[0][0].Null {
			t.Fatal(res[12].Set, res[13].Set)
		}
		info := must("INFO server")[0].Set
		if info.Columns[0] != "section" || len(info.Rows) < 5 {
			t.Fatal(info)
		}
		scan := must("SCAN 0 MATCH user:* COUNT 100")[0].Set
		if !strings.HasPrefix(scan.Columns[0], "key (next cursor:") || len(scan.Rows) != 2 {
			t.Fatal(scan)
		}
		// An error stops the script; MULTI…EXEC runs on one connection.
		r, _ := c.Execute(ctx, "SET a 1\nLPUSH user:1 x\nSET b 2", 0)
		if len(r) != 2 || r[1].Err == nil || !strings.Contains(r[1].Err.Error(), "WRONGTYPE") {
			t.Fatal(r)
		}
		tx := must("MULTI\nINCR counter\nINCR counter\nEXEC")
		if e := tx[3].Set; len(e.Rows) != 2 || e.Rows[1][1].Text != "8" {
			t.Fatal(e)
		}
		if r, _ := c.Execute(ctx, "SUBSCRIBE news", 0); r[0].Err == nil {
			t.Fatal("SUBSCRIBE ran")
		}
		// SELECT moves the session.
		must("SELECT 2\nSET only-in-2 x\nSELECT 1")
		if c.Schema() != "db1" {
			t.Fatal(c.Schema())
		}
		must("DEL a")
	})

	t.Run("browse", func(t *testing.T) {
		objs, err := c.Objects(ctx, "db1")
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, o := range objs {
			names = append(names, o.Name)
		}
		if !slices.Equal(names, []string{"*", "user:*"}) {
			t.Fatal(names)
		}
		cols, _ := c.Columns(ctx, db1)
		if len(cols) != 4 || !cols[0].PrimaryKey || cols[2].Name != "ttl" {
			t.Fatal(cols)
		}
		rs, err := c.FetchRows(ctx, db1, Page{Limit: 3}, nil)
		if err != nil || len(rs.Rows) != 3 || rs.Rows[0][0].Text != "counter" || rs.Rows[1][0].Text != "events" {
			t.Fatal(rs, err)
		}
		if n, _ := c.CountRows(ctx, db1, Page{}, nil); n != 8 {
			t.Fatal(n)
		}
		row := func(k string) []Value {
			rs, _ := c.FetchRows(ctx, db1, Page{Filters: []Filter{{Column: "key", Op: OpEq, Value: k}}}, nil)
			if len(rs.Rows) != 1 {
				t.Fatalf("%s: %+v", k, rs.Rows)
			}
			return rs.Rows[0]
		}
		if r := row("counter"); r[1].Text != "string" || r[2].Null || r[3].Text != "8" {
			t.Fatal(r)
		}
		if r := row("user:1:profile"); r[1].Text != "hash" || !r[2].Null || r[3].Text != "‹hash: 2 fields›" {
			t.Fatal(r)
		}
		if r := row("scores"); r[1].Text != "sorted set" || r[3].Text != "‹sorted set: 2 members›" {
			t.Fatal(r)
		}
		// Filters and sorts by type, TTL and value read every key's type.
		rs, _ = c.FetchRows(ctx, db1, Page{Filters: []Filter{{Column: "type", Op: OpEq, Value: "list"}}}, nil)
		if len(rs.Rows) != 1 || rs.Rows[0][0].Text != "queue" {
			t.Fatal(rs.Rows)
		}
		rs, _ = c.FetchRows(ctx, db1, Page{Filters: []Filter{{Column: "ttl", Op: OpIsNotNull}}}, nil)
		if len(rs.Rows) != 1 || rs.Rows[0][0].Text != "counter" {
			t.Fatal(rs.Rows)
		}
		rs, _ = c.FetchRows(ctx, db1, Page{Sort: []Sort{{Column: "type", Dir: Desc}}, Limit: 1}, nil)
		if rs.Rows[0][1].Text != "string" {
			t.Fatal(rs.Rows)
		}
		rs, _ = c.FetchRows(ctx, db1, Page{Filters: []Filter{{Column: AnyColumn, Value: "alic"}}}, nil)
		if len(rs.Rows) != 1 || rs.Rows[0][0].Text != "user:1" { // the value Alice
			t.Fatal(rs.Rows)
		}
		// A namespace; a key whose name has a wildcard in it.
		if n, _ := c.CountRows(ctx, TableRef{Schema: "db1", Name: "user:*"}, Page{}, nil); n != 2 {
			t.Fatal(n)
		}
		rs, err = c.FetchRows(ctx, key("odd*key"), Page{}, nil)
		if err != nil || rs.Rows[0][1].Text != "v" {
			t.Fatal(rs, err)
		}
		// Each type's contents.
		contents := map[string]string{
			"user:1:profile": "field,value:age=41,name=Alice",
			"queue":          "index,value:0=a,1=b,2=c",
			"tags":           "member:x,y",
			"scores":         "member,score:alice=10,bob=20",
			"events":         "id,type,who:1-1=login=alice",
			"user:1":         "key,value:user:1=Alice",
		}
		if !streams {
			delete(contents, "events")
		}
		for k, want := range contents {
			rs, err := c.FetchRows(ctx, key(k), Page{}, nil)
			if err != nil {
				t.Fatal(k, err)
			}
			var rows []string
			for _, r := range rs.Rows {
				var vs []string
				for _, v := range r {
					vs = append(vs, v.Text)
				}
				rows = append(rows, strings.Join(vs, "="))
			}
			if got := strings.Join(rs.Columns, ",") + ":" + strings.Join(rows, ","); got != want {
				t.Errorf("%s: %s", k, got)
			}
		}
		if _, err := c.FetchRows(ctx, key("gone"), Page{}, nil); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
			t.Fatal(err)
		}
		info, _ := c.TableInfo(ctx, key("counter"))
		if !strings.Contains(info.Options, "string") || !strings.Contains(info.Options, "expires in") {
			t.Fatal(info.Options)
		}
	})

	apply := func(t *testing.T, ref TableRef, changes ...Change) {
		t.Helper()
		if err := c.ApplyChanges(ctx, ref, changes, true); err != nil {
			t.Fatal(err)
		}
	}
	refuse := func(t *testing.T, want string, ref TableRef, changes ...Change) {
		t.Helper()
		if err := c.ApplyChanges(ctx, ref, changes, true); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got %v", want, err)
		}
	}
	k := func(col, v string) map[string]Value { return map[string]Value{col: Text(v)} }
	got := func(script string) string { return cell(must(script)[0], 0, 0) }
	all := func(script string) string {
		var out []string
		for _, r := range must(script)[0].Set.Rows {
			out = append(out, r[len(r)-1].Text)
		}
		return strings.Join(out, ",")
	}

	t.Run("key list edits", func(t *testing.T) {
		stmts, err := c.PreviewChanges(ctx, db1, []Change{{Kind: ChangeUpdate, Key: k("key", "user:1"), Values: map[string]Value{"key": Text("user:01"), "value": Text("Al ice")}}}, true)
		if err != nil || strings.Join(stmts, "\n") != "RENAME user:1 user:01\nSET user:01 \"Al ice\" KEEPTTL" {
			t.Fatal(stmts, err)
		}
		apply(t, db1, Change{Kind: ChangeUpdate, Key: k("key", "user:1"), Values: map[string]Value{"key": Text("user:01"), "value": Text("Al ice")}})
		if got("GET user:01") != "Al ice" {
			t.Fatal()
		}
		apply(t, db1, Change{Kind: ChangeUpdate, Key: k("key", "counter"), Values: map[string]Value{"value": Text("100")}})
		if got("TTL counter") == "-1" { // KEEPTTL
			t.Fatal("the TTL was lost")
		}
		apply(t, db1, Change{Kind: ChangeUpdate, Key: k("key", "counter"), Values: map[string]Value{"ttl": NullValue}})
		if got("TTL counter") != "-1" {
			t.Fatal("not persisted")
		}
		apply(t, db1, Change{Kind: ChangeInsert, Values: map[string]Value{"key": Text("fresh"), "value": Text("new"), "ttl": Text("60")}},
			Change{Kind: ChangeDelete, Key: k("key", "tags")})
		if got("GET fresh") != "new" || got("EXISTS tags") != "0" || got("TTL fresh") == "-1" {
			t.Fatal()
		}
		refuse(t, "already exists", db1, Change{Kind: ChangeInsert, Values: map[string]Value{"key": Text("fresh"), "value": Text("x")}})
		refuse(t, "already exists", db1, Change{Kind: ChangeUpdate, Key: k("key", "fresh"), Values: map[string]Value{"key": Text("queue")}})
		refuse(t, "open it to change it", db1, Change{Kind: ChangeUpdate, Key: k("key", "queue"), Values: map[string]Value{"value": Text("x")}})
		refuse(t, "type can't be changed", db1, Change{Kind: ChangeUpdate, Key: k("key", "queue"), Values: map[string]Value{"type": Text("hash")}})
		refuse(t, "a new key here is a string", db1, Change{Kind: ChangeInsert, Values: map[string]Value{"key": Text("h2"), "type": Text("hash"), "value": Text("x")}})
		refuse(t, "seconds", db1, Change{Kind: ChangeUpdate, Key: k("key", "fresh"), Values: map[string]Value{"ttl": Text("soon")}})
		// A refused change leaves the others in the commit undone.
		refuse(t, "already exists", db1, Change{Kind: ChangeDelete, Key: k("key", "fresh")}, Change{Kind: ChangeInsert, Values: map[string]Value{"key": Text("queue"), "value": Text("x")}})
		if got("EXISTS fresh") != "1" {
			t.Fatal("half a commit was applied")
		}
	})

	t.Run("hash, list, set, sorted set, stream, string", func(t *testing.T) {
		h := key("user:1:profile")
		apply(t, h, Change{Kind: ChangeInsert, Values: map[string]Value{"field": Text("city"), "value": Text("Berlin")}},
			Change{Kind: ChangeUpdate, Key: k("field", "age"), Values: map[string]Value{"value": Text("42")}},
			Change{Kind: ChangeUpdate, Key: k("field", "name"), Values: map[string]Value{"field": Text("first"), "value": Text("Alice")}})
		if got("HGET user:1:profile age") != "42" || got("HGET user:1:profile first") != "Alice" || got("HEXISTS user:1:profile name") != "0" {
			t.Fatal(all("HGETALL user:1:profile"))
		}
		refuse(t, "already exists", h, Change{Kind: ChangeInsert, Values: map[string]Value{"field": Text("city"), "value": Text("x")}})
		apply(t, h, Change{Kind: ChangeDelete, Key: k("field", "city")})

		l := key("queue")
		apply(t, l, Change{Kind: ChangeUpdate, Key: k("index", "1"), Values: map[string]Value{"value": Text("B")}},
			Change{Kind: ChangeDelete, Key: k("index", "0")}, Change{Kind: ChangeDelete, Key: k("index", "2")},
			Change{Kind: ChangeInsert, Values: map[string]Value{"value": Text("d")}})
		if all("LRANGE queue 0 -1") != "B,d" {
			t.Fatal(all("LRANGE queue 0 -1"))
		}
		refuse(t, "no longer exists", l, Change{Kind: ChangeUpdate, Key: k("index", "9"), Values: map[string]Value{"value": Text("x")}})

		must("SADD tags x y")
		s := key("tags")
		apply(t, s, Change{Kind: ChangeUpdate, Key: k("member", "x"), Values: map[string]Value{"member": Text("xx")}},
			Change{Kind: ChangeInsert, Values: map[string]Value{"member": Text("z")}}, Change{Kind: ChangeDelete, Key: k("member", "y")})
		members := strings.Split(all("SMEMBERS tags"), ",")
		if slices.Sort(members); strings.Join(members, ",") != "xx,z" {
			t.Fatal(members)
		}
		refuse(t, "already in the set", s, Change{Kind: ChangeInsert, Values: map[string]Value{"member": Text("z")}})

		z := key("scores")
		apply(t, z, Change{Kind: ChangeUpdate, Key: k("member", "alice"), Values: map[string]Value{"score": Text("30")}},
			Change{Kind: ChangeInsert, Values: map[string]Value{"member": Text("carol"), "score": Text("1.5")}},
			Change{Kind: ChangeUpdate, Key: k("member", "bob"), Values: map[string]Value{"member": Text("robert"), "score": Text("20")}})
		if all("ZRANGE scores 0 -1") != "carol,robert,alice" {
			t.Fatal(all("ZRANGE scores 0 -1"))
		}
		refuse(t, "a score is a number", z, Change{Kind: ChangeInsert, Values: map[string]Value{"member": Text("dan"), "score": Text("lots")}})

		if streams {
			e := key("events")
			apply(t, e, Change{Kind: ChangeInsert, Values: map[string]Value{"id": Text("2-1"), "type": Text("logout"), "who": Text("alice")}})
			if got("XLEN events") != "2" {
				t.Fatal()
			}
			refuse(t, "can't be changed", e, Change{Kind: ChangeUpdate, Key: k("id", "1-1"), Values: map[string]Value{"type": Text("x")}})
			apply(t, e, Change{Kind: ChangeDelete, Key: k("id", "1-1")})
		}

		must("SET greeting hello\nEXPIRE greeting 500")
		apply(t, key("greeting"), Change{Kind: ChangeUpdate, Key: k("key", "greeting"), Values: map[string]Value{"value": Text("hi")}})
		if got("GET greeting") != "hi" || got("TTL greeting") == "-1" {
			t.Fatal()
		}
		// The key changed type since it was opened.
		must("DEL greeting\nRPUSH greeting x")
		refuse(t, "changed meanwhile", key("greeting"), Change{Kind: ChangeUpdate, Key: k("key", "greeting"), Values: map[string]Value{"value": Text("x")}})
	})

	t.Run("binary values", func(t *testing.T) {
		must(`SET bin "\x00\xff\x01"`)
		rs, _ := c.FetchRows(ctx, key("bin"), Page{}, nil)
		if v := rs.Rows[0][1]; !v.Hex || v.Text != "0x00ff01" {
			t.Fatal(v)
		}
		must("DEL bin")
	})

	t.Run("dump and import", func(t *testing.T) {
		must("SET ttl-key v\nPEXPIRE ttl-key 600000")
		snapshot := func() string {
			var parts []string
			for _, kname := range []string{"counter", "fresh", "queue", "tags", "scores", "events", "user:1:profile", "user:01", "odd*key"} {
				parts = append(parts, kname+"="+got("TYPE "+quoteRedisArg(kname)))
			}
			return strings.Join(parts, " ") + " | " + all("LRANGE queue 0 -1") + " | " + all("ZRANGE scores 0 -1 WITHSCORES")
		}
		before := snapshot()
		var buf bytes.Buffer
		if err := c.Dump(ctx, &buf, DumpOptions{Objects: []Object{{Schema: "db1", Name: "*"}, {Schema: "db1", Name: "user:*"}}, Data: true}, nil); err != nil {
			t.Fatal(err)
		}
		dump := buf.String()
		if !strings.Contains(dump, "SELECT 1") || !strings.Contains(dump, `SET odd*key v`) || !strings.Contains(dump, "PEXPIREAT ttl-key") ||
			strings.Count(dump, "HSET user:1:profile") != 1 {
			t.Fatal(dump)
		}
		must("FLUSHDB")
		res, err := c.ImportSQL(ctx, strings.NewReader(dump), ImportOptions{}, nil)
		if err != nil || res.Failed != 0 {
			t.Fatal(res, err)
		}
		if after := snapshot(); after != before {
			t.Fatalf("\n%s\n%s", before, after)
		}
		if ttl := got("PTTL ttl-key"); ttl == "-1" || ttl == "-2" {
			t.Fatal("expiry lost", ttl)
		}
		def, _ := c.Definition(ctx, Object{Schema: "db1", Name: "scores"})
		if !strings.HasPrefix(def, "ZADD scores") {
			t.Fatal(def)
		}
	})

	t.Run("delete", func(t *testing.T) {
		must("SET user:2 b\nSET user:3 c")
		stmts, err := BuildDrop(c.Dialect, []Object{{Schema: "db1", Name: "user:*"}, {Schema: "db1", Name: "fresh"}}, false)
		if err != nil || !strings.Contains(strings.Join(stmts, "\n"), "UNLINK fresh") {
			t.Fatal(stmts, err)
		}
		if _, err := c.Drop(ctx, []Object{{Schema: "db1", Name: "user:*"}, {Schema: "db1", Name: "fresh"}}, false); err != nil {
			t.Fatal(err)
		}
		if got("EXISTS user:2 user:3 user:01 user:1:profile fresh") != "0" || got("EXISTS queue") != "1" {
			t.Fatal()
		}
		if _, err := c.Alter(ctx, db1, []AlterOp{{Kind: AlterTruncate}}, true); err == nil {
			t.Fatal("Alter on Redis")
		}
	})
}

// Every database type can be picked (DuckDB only needs cgo).
func TestEveryDriverSupported(t *testing.T) {
	for _, d := range model.Drivers {
		if d != model.DriverDuckDB && !Supported(d) {
			t.Errorf("%s can't be picked in the connection form", d)
		}
	}
}
