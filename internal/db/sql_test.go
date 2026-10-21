package db

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"gotable/internal/model"
)

var (
	pg     = DialectFor(model.DriverPostgres)
	my     = DialectFor(model.DriverMySQL)
	sqlite = DialectFor(model.DriverSQLite)
)

func TestQuoteIdent(t *testing.T) {
	if got := pg.QuoteIdent(`a"b`); got != `"a""b"` {
		t.Error(got)
	}
	if got := my.QuoteIdent("a`b"); got != "`a``b`" {
		t.Error(got)
	}
	if got := sqlite.QualifiedName(TableRef{Schema: "main", Name: "t"}); got != `"t"` {
		t.Error(got)
	}
	if got := pg.QualifiedName(TableRef{Schema: "s", Name: "t"}); got != `"s"."t"` {
		t.Error(got)
	}
}

func TestQuoteLiteral(t *testing.T) {
	v := Text(`it's \ok`)
	if got := pg.QuoteLiteral(v); got != `'it''s \ok'` {
		t.Error(got)
	}
	if got := my.QuoteLiteral(v); got != `'it''s \\ok'` {
		t.Error(got)
	}
	if got := pg.QuoteLiteral(NullValue); got != "NULL" {
		t.Error(got)
	}
	bin := Value{Text: "0xDEAD", Hex: true}
	if got := pg.QuoteLiteral(bin); got != `'\xdead'` {
		t.Error(got)
	}
	if got := my.QuoteLiteral(bin); got != "X'DEAD'" {
		t.Error(got)
	}
}

func TestBuildSelect(t *testing.T) {
	p := Page{
		Filters: []Filter{
			{Column: "name", Op: OpContains, Value: "50%"},
			{Column: "id", Op: OpIn, Value: "1, 2,,3"},
			{Column: "x", Op: OpIsNull},
		},
		RawWhere: "a = 1 OR b = 2",
		Sort:     []Sort{{Column: "id", Dir: Desc}, {Column: "name"}},
		Limit:    10, Offset: 20,
	}
	q, args, err := BuildSelect(pg, TableRef{Schema: "public", Name: "t"}, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT * FROM "public"."t" WHERE "name"::text ILIKE $1 AND "id" IN ($2, $3, $4) AND "x" IS NULL AND (a = 1 OR b = 2) ORDER BY "id" DESC, "name" ASC LIMIT 10 OFFSET 20`
	if q != want {
		t.Errorf("got  %s\nwant %s", q, want)
	}
	if !reflect.DeepEqual(args, []any{`%50\%%`, "1", "2", "3"}) {
		t.Errorf("args %v", args)
	}

	q, _, _ = BuildSelect(my, TableRef{Schema: "db", Name: "t"}, Page{Filters: []Filter{{Column: "n", Op: OpBeginsWith, Value: "a"}}}, nil)
	if q != "SELECT * FROM `db`.`t` WHERE CAST(`n` AS CHAR) LIKE ?" {
		t.Error(q)
	}
	q, _, _ = BuildSelect(sqlite, TableRef{Name: "t"}, Page{Filters: []Filter{{Column: "n", Op: OpEndsWith, Value: "a"}}, Offset: 5}, nil)
	if q != `SELECT * FROM "t" WHERE "n" LIKE ? ESCAPE '\' LIMIT -1 OFFSET 5` {
		t.Error(q)
	}
}

func TestAnyColumnFilter(t *testing.T) {
	q, args, err := BuildSelect(pg, TableRef{Name: "t"}, Page{Filters: []Filter{{Column: AnyColumn, Op: OpEq, Value: "x"}}}, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if q != `SELECT * FROM "t" WHERE ("a" = $1 OR "b" = $2)` || len(args) != 2 {
		t.Error(q, args)
	}
	q, _, _ = BuildSelect(pg, TableRef{Name: "t"}, Page{Filters: []Filter{{Column: AnyColumn, Op: OpNotContain, Value: "x"}}}, []string{"a", "b"})
	if !strings.Contains(q, ") AND ") && !strings.Contains(q, " AND ") {
		t.Error("negated any-column filter must AND:", q)
	}
	if _, _, err := BuildSelect(pg, TableRef{Name: "t"}, Page{Filters: []Filter{{Column: AnyColumn, Op: OpEq}}}, nil); err == nil {
		t.Error("expected error without columns")
	}
}

func TestFilterErrors(t *testing.T) {
	for _, f := range []Filter{{Column: "", Op: OpEq}, {Column: "a", Op: OpIn, Value: " , "}, {Column: "a", Op: "BOGUS"}} {
		if _, _, err := BuildSelect(pg, TableRef{Name: "t"}, Page{Filters: []Filter{f}}, nil); err == nil {
			t.Errorf("expected error for %+v", f)
		}
	}
}

func TestBuildChange(t *testing.T) {
	tr := TableRef{Schema: "public", Name: "t"}
	s, err := BuildChange(pg, tr, Change{Kind: ChangeUpdate,
		Key:    map[string]Value{"id": Text("1"), "k": NullValue},
		Values: map[string]Value{"b": NullValue, "a": Text("x")}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if s.SQL != `UPDATE "public"."t" SET "a" = $1, "b" = $2 WHERE "id" = $3 AND "k" IS NULL` {
		t.Error(s.SQL)
	}
	if !reflect.DeepEqual(s.Args, []any{"x", nil, "1"}) {
		t.Error(s.Args)
	}
	if got := Preview(pg, s); got != `UPDATE "public"."t" SET "a" = 'x', "b" = NULL WHERE "id" = '1' AND "k" IS NULL;` {
		t.Error(got)
	}

	s, _ = BuildChange(my, TableRef{Name: "t"}, Change{Kind: ChangeDelete, Key: map[string]Value{"a": Text("?")}}, false)
	if s.SQL != "DELETE FROM `t` WHERE `a` = ? LIMIT 1" {
		t.Error(s.SQL)
	}
	if got := Preview(my, s); got != "DELETE FROM `t` WHERE `a` = '?' LIMIT 1;" {
		t.Error(got)
	}

	s, _ = BuildChange(sqlite, TableRef{Name: "t"}, Change{Kind: ChangeInsert}, true)
	if s.SQL != `INSERT INTO "t" DEFAULT VALUES` {
		t.Error(s.SQL)
	}
	s, _ = BuildChange(my, TableRef{Name: "t"}, Change{Kind: ChangeInsert}, true)
	if s.SQL != "INSERT INTO `t` () VALUES ()" {
		t.Error(s.SQL)
	}
	s, _ = BuildChange(sqlite, TableRef{Name: "t"}, Change{Kind: ChangeInsert, Values: map[string]Value{"b": Text("2"), "a": Text("1")}}, true)
	if s.SQL != `INSERT INTO "t" ("a", "b") VALUES (?, ?)` {
		t.Error(s.SQL)
	}

	for _, c := range []Change{{Kind: ChangeUpdate, Values: map[string]Value{"a": Text("1")}}, {Kind: ChangeUpdate, Key: map[string]Value{"a": Text("1")}}, {Kind: ChangeDelete}} {
		if _, err := BuildChange(pg, tr, c, true); err == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

func spanSQL(sp []Span) []string {
	out := make([]string, len(sp))
	for i, s := range sp {
		out[i] = s.SQL
	}
	return out
}

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		kind   model.Driver
		script string
		want   []string
	}{
		{model.DriverPostgres, "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{model.DriverPostgres, "SELECT ';' ; SELECT \"a;b\" FROM t", []string{"SELECT ';'", `SELECT "a;b" FROM t`}},
		{model.DriverPostgres, "SELECT 'it''s;' ;", []string{"SELECT 'it''s;'"}},
		{model.DriverPostgres, "-- a; b\nSELECT 1; /* ; */ SELECT 2", []string{"-- a; b\nSELECT 1", "/* ; */ SELECT 2"}},
		{model.DriverPostgres, "CREATE FUNCTION f() AS $body$ a; b; $body$; SELECT 1", []string{"CREATE FUNCTION f() AS $body$ a; b; $body$", "SELECT 1"}},
		{model.DriverPostgres, "SELECT $1; SELECT $$x;$$", []string{"SELECT $1", "SELECT $$x;$$"}},
		{model.DriverMySQL, "SELECT 'a\\';b'; SELECT `x;y`", []string{"SELECT 'a\\';b'", "SELECT `x;y`"}},
		{model.DriverMySQL, "# c;\nSELECT 1", []string{"# c;\nSELECT 1"}},
		{model.DriverMySQL, "DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 1; END$$\nDELIMITER ;\nSELECT 2;", []string{"CREATE PROCEDURE p() BEGIN SELECT 1; END", "SELECT 2"}},
		{model.DriverSQLite, "CREATE TRIGGER tr AFTER INSERT ON t BEGIN UPDATE t SET a=1; DELETE FROM u; END; SELECT 1", []string{"CREATE TRIGGER tr AFTER INSERT ON t BEGIN UPDATE t SET a=1; DELETE FROM u; END", "SELECT 1"}},
		{model.DriverSQLite, "BEGIN; INSERT INTO t VALUES (1); COMMIT;", []string{"BEGIN", "INSERT INTO t VALUES (1)", "COMMIT"}},
		{model.DriverPostgres, " ; ;-- only comment\n", []string{}},
	}
	for _, c := range cases {
		got := spanSQL(SplitStatements(c.kind, c.script))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %q:\n got %q\nwant %q", c.kind, c.script, got, c.want)
		}
	}
}

func TestStatementAt(t *testing.T) {
	script := "SELECT 1;\n\nSELECT 2;\nSELECT 3"
	for pos, want := range map[int]string{0: "SELECT 1", 9: "SELECT 1", 11: "SELECT 2", 15: "SELECT 2", len(script): "SELECT 3"} {
		sp, ok := StatementAt(model.DriverPostgres, script, pos)
		if !ok || sp.SQL != want {
			t.Errorf("pos %d: %q", pos, sp.SQL)
		}
		if script[sp.Start:sp.Start+len(sp.SQL)] != sp.SQL {
			t.Errorf("span offsets wrong for %q", sp.SQL)
		}
	}
}

func TestReturnsRows(t *testing.T) {
	for s, want := range map[string]bool{
		"select 1": true, "  (SELECT 1)": true, "WITH x AS (SELECT 1) SELECT * FROM x": true,
		"SHOW TABLES": true, "PRAGMA table_info(t)": true, "-- c\nSELECT 1": true,
		"INSERT INTO t VALUES (1) RETURNING id": true, "UPDATE t SET a=1": false,
		"CREATE TABLE t (a int)": false, "DELETE FROM t": false, "BEGIN": false,
	} {
		if got := returnsRows(s); got != want {
			t.Errorf("%q = %v", s, got)
		}
	}
}

func TestToValue(t *testing.T) {
	cases := map[Value]Value{
		toValue(nil, ""):                NullValue,
		toValue([]byte("hi"), ""):       Text("hi"),
		toValue([]byte{0xff, 0x00}, ""): {Text: "0xFF00", Hex: true},
		toValue(float64(1.25), ""):      Text("1.25"),
		toValue(int64(-3), ""):          Text("-3"),
		toValue(true, ""):               Text("true"),
		toValue(float64(1e21), ""):      Text("1000000000000000000000"),
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	}
}

func TestEnumValues(t *testing.T) {
	cases := map[string][]string{
		"enum('a','b c','it''s')": {"a", "b c", "it's"},
		"SET('x','y')":            {"x", "y"},
		"enum('a,b','')":          {"a,b", ""},
		"varchar(20)":             nil,
		"tinyint(1)":              nil,
	}
	for in, want := range cases {
		if got := enumValues(in); !slices.Equal(got, want) {
			t.Errorf("enumValues(%q) = %q, want %q", in, got, want)
		}
	}
}
