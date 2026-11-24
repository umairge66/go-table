package db

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"

	sf "github.com/snowflakedb/gosnowflake/v2"
	"github.com/youmark/pkcs8"
)

func TestSnowflakeStatements(t *testing.T) {
	got := []string{}
	script := "SELECT 'a;\\'b'; // one; two\nSELECT \"x\\\" ; SELECT $$ a; 'b $$, $1; SELECT 'it''s'; -- end;\n"
	for _, s := range SplitStatements(model.DriverSnowflake, script) {
		got = append(got, stripLeadingComments(s.SQL))
	}
	want := []string{`SELECT 'a;\'b'`, `SELECT "x\"`, "SELECT $$ a; 'b $$, $1", "SELECT 'it''s'"}
	if !slices.Equal(got, want) {
		t.Errorf("split %q", got)
	}
	sc := NewScanner(strings.NewReader(script), model.DriverSnowflake)
	got = nil
	for {
		st, err := sc.Next()
		if err != nil {
			break
		}
		got = append(got, stripLeadingComments(st.SQL))
	}
	if !slices.Equal(got, want) {
		t.Errorf("scan %q", got)
	}
}

func TestSnowflakeDDL(t *testing.T) {
	d := DialectFor(model.DriverSnowflake)
	tbl := TableRef{Schema: "S", Name: "T"}
	st := TableState{Columns: []Column{{Name: "ID", Type: "NUMBER(38,0)"}, {Name: "C", Type: "VARCHAR(10)", Nullable: true, DefaultKind: DefaultValue, DefaultText: "x"}},
		Indexes: []Index{{Name: "PK", Columns: []string{"ID"}, Primary: true, Unique: true, Constraint: true}}}
	for _, c := range []struct {
		op   AlterOp
		want string // or, after "!", part of the error
	}{
		{AlterOp{Kind: AlterAddForeignKey, ForeignKey: &ForeignKeyDef{Columns: []string{"ID"}, RefTable: "P", RefColumns: []string{"ID"}, OnDelete: "CASCADE"}}, "!NO ACTION"},
		{AlterOp{Kind: AlterAddForeignKey, ForeignKey: &ForeignKeyDef{Name: "F", Columns: []string{"ID"}, RefTable: "P", RefColumns: []string{"ID"}, OnDelete: "NO ACTION"}},
			`ALTER TABLE "S"."T" ADD CONSTRAINT "F" FOREIGN KEY ("ID") REFERENCES "S"."P" ("ID");`},
		{AlterOp{Kind: AlterAddIndex, Index: &IndexDef{Columns: []string{"C"}}}, "!no indexes"},
		{AlterOp{Kind: AlterAddIndex, Index: &IndexDef{Name: "U", Columns: []string{"C"}, Unique: true}}, `ALTER TABLE "S"."T" ADD CONSTRAINT "U" UNIQUE ("C");`},
		{AlterOp{Kind: AlterRenameTable, NewName: "n"}, `ALTER TABLE "S"."T" RENAME TO "S"."n";`},
		{AlterOp{Kind: AlterModifyColumn, Column: "C", Def: &ColumnDef{Name: "C", Type: "VARCHAR(10)", Nullable: true, DefaultKind: DefaultValue, Default: "y"}}, "!only remove"},
		{AlterOp{Kind: AlterModifyColumn, Column: "C", Def: &ColumnDef{Name: "D", Type: "VARCHAR(20)", Nullable: false, Comment: `a\b`}},
			`ALTER TABLE "S"."T" RENAME COLUMN "C" TO "D"; ALTER TABLE "S"."T" ALTER COLUMN "D" SET DATA TYPE VARCHAR(20); ALTER TABLE "S"."T" ALTER COLUMN "D" DROP DEFAULT; ALTER TABLE "S"."T" ALTER COLUMN "D" SET NOT NULL; ALTER TABLE "S"."T" ALTER COLUMN "D" COMMENT 'a\\b';`},
		{AlterOp{Kind: AlterSetPrimaryKey, Columns: []string{"C"}}, `ALTER TABLE "S"."T" DROP PRIMARY KEY; ALTER TABLE "S"."T" ADD PRIMARY KEY ("C");`},
	} {
		p, err := BuildAlter(d, tbl, st, []AlterOp{c.op})
		if w, bad := strings.CutPrefix(c.want, "!"); bad {
			if err == nil || !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %v %v", c.op.Kind, p.Statements, err)
			}
			continue
		}
		if got := strings.Join(p.Statements, " "); err != nil || got != c.want {
			t.Errorf("%s:\n%s\n%s\n%v", c.op.Kind, got, c.want, err)
		}
	}
	for typ, want := range map[string]string{"NUMBER(38,0)": "5", "INT": "5", "VARCHAR": "'5'", "VARIANT": "'5'", "BOOLEAN": "'5'", "DATE": "'5'::DATE"} {
		if got := sfDefault(d, typ, "5"); got != want {
			t.Errorf("default of %s: %s", typ, got)
		}
	}
	for typ, ok := range map[string]bool{"VECTOR(FLOAT, 3)": true, "MAP(VARCHAR(10), NUMBER(38,0))": true, "NUMBER(38,0)": true, "VARCHAR": true, "INT); DROP TABLE x": false, "A(B": false, "A'": false} {
		if sfTypeOK(typ) != ok {
			t.Errorf("type %q: %v", typ, !ok)
		}
	}
	drops, _ := BuildDrop(d, []Object{{Schema: "S", Name: "T", Kind: KindTable}, {Schema: "S", Name: "V", Kind: KindView}}, false)
	if !slices.Equal(drops, []string{`DROP VIEW "S"."V";`, `DROP TABLE "S"."T" RESTRICT;`}) {
		t.Errorf("drop %q", drops)
	}
	if err := sfCheck("/* x */ PUT file:///etc/passwd @~"); err == nil {
		t.Error("PUT ran")
	}
	if err := sfCheck("-- x\n// y\nget @~/a file:///tmp"); err == nil {
		t.Error("GET ran")
	}
}

type roundTrips struct{ paths []string }

func (l *roundTrips) RoundTrip(r *http.Request) (*http.Response, error) {
	l.paths = append(l.paths, r.URL.Path)
	return http.DefaultTransport.RoundTrip(r)
}

// Telemetry isn't sent.
func TestSnowflakeTelemetry(t *testing.T) {
	log := &roundTrips{}
	req := httptest.NewRequest("POST", "https://a.snowflakecomputing.com/telemetry/send?x=1", strings.NewReader("{}"))
	res, err := sfTransport{log}.RoundTrip(req)
	if err != nil || res.StatusCode != 200 || len(log.paths) != 0 {
		t.Fatal(res, err, log.paths)
	}
	cfg, _ := serverConfig(t, "SNOWFLAKE", model.DriverSnowflake) // and on the service
	cfg.Auth, cfg.KeyPath = model.AuthKey, os.Getenv("GOTABLE_TEST_SNOWFLAKE_KEY")
	c, err := snowflakeConfig(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	quietSnowflake()
	c.Transporter = sfTransport{log}
	db := sql.OpenDB(sfConnector{sf.NewConnector(sf.SnowflakeDriver{}, *c)})
	db.Exec("SELEC 1")
	if err := db.Ping(); err != nil {
		t.Error(err)
	}
	db.Close()
	if len(log.paths) < 3 || slices.ContainsFunc(log.paths, func(p string) bool { return strings.Contains(p, "telemetry") }) {
		t.Error(log.paths)
	}
	t.Log(log.paths)
}

func TestSnowflakeKey(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	dir := t.TempDir()
	write := func(name, typ string, b []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b}), 0o600)
		return p
	}
	plain, _ := x509.MarshalPKCS8PrivateKey(k)
	enc, err := pkcs8.MarshalPrivateKey(k, []byte("pw"), nil)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	for _, c := range []struct{ path, pass, err string }{
		{write("a", "PRIVATE KEY", plain), "", ""},
		{write("b", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(k)), "", ""},
		{write("c", "ENCRYPTED PRIVATE KEY", enc), "pw", ""},
		{write("d", "ENCRYPTED PRIVATE KEY", enc), "", "passphrase"},
		{write("e", "ENCRYPTED PRIVATE KEY", enc), "no", "passphrase is wrong"},
		{write("f", "PUBLIC KEY", pub), "", "public key"},
		{filepath.Join(dir, "none"), "", "no such file"},
	} {
		got, err := snowflakeKey(c.path, c.pass)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %v", c.path, err)
			}
		} else if err != nil || !got.Equal(k) {
			t.Errorf("%s: %v", c.path, err)
		}
	}
}

// snowflakeTest opens the account of GOTABLE_TEST_SNOWFLAKE_{HOST,USER,KEY,
// ROLE,WAREHOUSE,DB}, in a schema of its own that is dropped at the end.
func snowflakeTest(t *testing.T) (*Conn, context.Context) {
	cfg, pw := serverConfig(t, "SNOWFLAKE", model.DriverSnowflake)
	cfg.Port = 0
	cfg.KeyPath, cfg.Auth = os.Getenv("GOTABLE_TEST_SNOWFLAKE_KEY"), model.AuthKey
	cfg.Role, cfg.Warehouse = os.Getenv("GOTABLE_TEST_SNOWFLAKE_ROLE"), os.Getenv("GOTABLE_TEST_SNOWFLAKE_WAREHOUSE")
	c := openTest(t, cfg, pw)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	schema := "GT_SUITE"
	sfRun(t, c, ctx, "CREATE OR REPLACE SCHEMA "+schema)
	t.Cleanup(func() { c.Execute(context.Background(), "DROP SCHEMA IF EXISTS "+schema, 0) })
	if err := c.UseSchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	return c, ctx
}

func sfRun(t *testing.T, c *Conn, ctx context.Context, script string) []Result {
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

const sfEverything = `CREATE TABLE gt_all (id NUMBER(38,0) PRIMARY KEY, n NUMBER(38,10), f FLOAT, df DECFLOAT, b BOOLEAN, s VARCHAR, d DATE, t TIME(9),
	ntz TIMESTAMP_NTZ(9), ltz TIMESTAMP_LTZ(9), tz TIMESTAMP_TZ(9), bin BINARY, v VARIANT, o OBJECT, a ARRAY, g GEOGRAPHY, geo GEOMETRY, vec VECTOR(FLOAT,3), m MAP(VARCHAR, NUMBER))`

var sfAllValues = map[string]string{"n": "1234567890123456789012345678.0123456789", "f": "0.3333333333333333", "b": "true", "s": "it's \\ a\nb ✓ %_^ $$ ?",
	"d": "2024-02-29", "t": "23:59:59.123456789", "ntz": "2024-02-29 01:02:03.123456789", "tz": "2024-02-29 01:02:03.123456789 +05:30",
	"bin": "0x00FFABCD", "v": `{"a":[1,null,12345678901234567890],"b":"x'y\\z"}`, "o": `{"k":1}`, "a": `[1,"a"]`, "m": `{"k":7}`}

func TestSnowflake(t *testing.T) {
	c, ctx := snowflakeTest(t)
	schema := c.Schema()
	ref := func(n string) TableRef { return TableRef{Schema: schema, Name: n} }
	one := func(t *testing.T, q string) string {
		t.Helper()
		return sfRun(t, c, ctx, q)[0].Set.Rows[0][0].String()
	}
	all := func(m map[string]string, id string) map[string]Value {
		out := map[string]Value{"ID": Text(id)}
		for k, v := range m {
			out[strings.ToUpper(k)] = Text(v)
		}
		return out
	}

	t.Run("version and names", func(t *testing.T) {
		v, err := c.ServerVersion(ctx)
		if err != nil || !strings.HasPrefix(v, "Snowflake ") {
			t.Fatalf("%q %v", v, err)
		}
		dbs, err := c.Databases(ctx)
		if err != nil || !slices.Contains(dbs, c.Config.DBName) || c.CurrentDatabase(ctx) != c.Config.DBName {
			t.Fatalf("%v %v", dbs, err)
		}
		s, err := c.Schemas(ctx)
		if err != nil || !slices.Contains(s, schema) || slices.Contains(s, "INFORMATION_SCHEMA") {
			t.Fatalf("%v %v", s, err)
		}
	})

	// Edits and imports don't depend on the account's formats.
	t.Run("formats", func(t *testing.T) {
		sfRun(t, c, ctx, "ALTER SESSION SET DATE_INPUT_FORMAT='DD/MM/YYYY', TIMESTAMP_INPUT_FORMAT='DD/MM/YYYY HH24:MI', TIME_INPUT_FORMAT='HH24:MI', BINARY_INPUT_FORMAT='UTF-8', TIMESTAMP_TYPE_MAPPING='TIMESTAMP_LTZ'")
		defer sfRun(t, c, ctx, "ALTER SESSION UNSET DATE_INPUT_FORMAT, TIMESTAMP_INPUT_FORMAT, TIME_INPUT_FORMAT, BINARY_INPUT_FORMAT, TIMESTAMP_TYPE_MAPPING")
		sfRun(t, c, ctx, "CREATE TABLE gt_fmt (id INT PRIMARY KEY, d DATE, t TIME, n TIMESTAMP_NTZ, z TIMESTAMP_TZ, b BINARY)")
		in := func(v ...string) map[string]Value {
			return map[string]Value{"ID": Text(v[0]), "D": Text(v[1]), "T": Text(v[2]), "N": Text(v[3]), "Z": Text(v[4]), "B": Text(v[5])}
		}
		if err := c.ApplyChanges(ctx, ref("GT_FMT"), []Change{
			{Kind: ChangeInsert, Values: in("1", "2024-02-03", "1:02", "2024-02-03", "2024-02-03T04:05:06.5Z", "0xff")},
			{Kind: ChangeInsert, Values: in("2", "03/02/2024", "01:02", "03/02/2024 04:05", "2024-02-03 04:05:06 -0530", "FF")}}, true); err != nil {
			t.Fatal(err)
		}
		o := CSVImportOptions{Delimiter: ',', Columns: []string{"ID", "D", "T", "N", "Z", "B"}, Mapping: []int{0, 1, 2, 3, 4, 5}}
		if _, err := c.ImportCSV(ctx, strings.NewReader("3,2024-02-03,01:02:03.25,2024-02-03 04:05,2024-02-03 04:05+01,ff\n4,03/02/2024,01:02,03/02/2024 04:05,2024-02-03 04:05:06 +01:00,0xFF\n"), ref("GT_FMT"), o, nil); err != nil {
			t.Fatal(err)
		}
		rs, err := c.FetchRows(ctx, ref("GT_FMT"), Page{Sort: []Sort{{Column: "ID", Dir: Asc}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i, want := range []string{
			"[1 2024-02-03 01:02:00 2024-02-03 00:00:00 2024-02-03 04:05:06.5 +00:00 0xFF]",
			"[2 2024-02-03 01:02:00 2024-02-03 04:05:00 2024-02-03 04:05:06 -05:30 0xFF]",
			"[3 2024-02-03 01:02:03.25 2024-02-03 04:05:00 2024-02-03 04:05:00 +01:00 0xFF]",
			"[4 2024-02-03 01:02:00 2024-02-03 04:05:00 2024-02-03 04:05:06 +01:00 0xFF]"} {
			if got := fmt.Sprint(rs.Rows[i]); got != want {
				t.Errorf("%s, want %s", got, want)
			}
		}
	})

	t.Run("every type", func(t *testing.T) {
		sfRun(t, c, ctx, sfEverything)
		cols, err := c.Columns(ctx, ref("GT_ALL"))
		if err != nil || len(cols) != 19 || !cols[0].PrimaryKey || cols[0].Nullable || cols[18].Type != "MAP(VARCHAR(16777216), NUMBER(38,0))" {
			t.Fatalf("%+v %v", cols, err)
		}
		in := all(sfAllValues, "1")
		in["G"], in["GEO"] = Text("POINT(1 2)"), Text(`{"type":"Point","coordinates":[3,4]}`)
		in["VEC"], in["DF"] = Text("[1.5,2,3]"), Text("1.5e100")
		in["LTZ"] = Text("2024-02-29 01:02:03.123456789 -08:00")
		changes := []Change{{Kind: ChangeInsert, Values: in}, {Kind: ChangeInsert, Values: map[string]Value{"ID": Text("2"), "V": NullValue, "D": NullValue}}}
		prev, err := c.PreviewChanges(ctx, ref("GT_ALL"), changes, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.ApplyChanges(ctx, ref("GT_ALL"), changes, true); err != nil {
			t.Fatal(err)
		}
		rs, err := c.FetchRows(ctx, ref("GT_ALL"), Page{Sort: []Sort{{Column: "ID", Dir: Asc}}, Limit: 10}, nil)
		if err != nil || len(rs.Rows) != 2 {
			t.Fatalf("%+v %v", rs, err)
		}
		for i, n := range rs.Columns {
			if want, ok := sfAllValues[strings.ToLower(n)]; ok && rs.Rows[0][i].Text != want {
				t.Errorf("%s = %q, want %q", n, rs.Rows[0][i].Text, want)
			}
			if i > 0 && !rs.Rows[1][i].Null {
				t.Errorf("%s isn't NULL: %+v", n, rs.Rows[1][i])
			}
		}
		t.Logf("%v", rs.Rows[0])
		// The preview is SQL that does the same.
		sfRun(t, c, ctx, "DELETE FROM gt_all; "+strings.Join(prev, "\n"))
		again, err := c.FetchRows(ctx, ref("GT_ALL"), Page{Sort: []Sort{{Column: "ID", Dir: Asc}}}, nil)
		if err != nil || fmt.Sprint(again.Rows) != fmt.Sprint(rs.Rows) {
			t.Errorf("preview gave\n%v\n%v", again, rs.Rows)
		}

		// Without a key, a row is matched by all its values: each type equals itself.
		sfRun(t, c, ctx, "CREATE TABLE gt_nokey LIKE gt_all; ALTER TABLE gt_nokey DROP PRIMARY KEY; INSERT INTO gt_nokey SELECT * FROM gt_all")
		for _, row := range rs.Rows {
			key := map[string]Value{}
			for i, n := range rs.Columns {
				key[n] = row[i]
			}
			if err := c.ApplyChanges(ctx, ref("GT_NOKEY"), []Change{{Kind: ChangeUpdate, Key: key, Values: map[string]Value{"S": Text("new")}}}, false); err != nil {
				t.Errorf("update without a key: %v", err)
				for i, n := range rs.Columns {
					if !row[i].Null {
						col, _ := sfColumn(cols, n)
						text, cond := sfMatch(c.Dialect, col, row[i].Text)
						q := "SELECT COUNT(*) FROM gt_nokey WHERE " + Preview(c.Dialect, Statement{SQL: cond, Args: []any{text}})
						t.Logf("%s: %v", n, one(t, strings.TrimSuffix(q, ";")))
					}
				}
			}
		}
		if n := one(t, "SELECT COUNT(*) FROM gt_nokey WHERE s = 'new'"); n != "2" {
			t.Errorf("%s rows changed", n)
		}
		// Searching every column skips what has no text.
		if rs, err := c.FetchRows(ctx, ref("GT_ALL"), Page{Filters: []Filter{{Column: AnyColumn, Op: OpContains, Value: "%_^"}}}, rs.Columns); err != nil || len(rs.Rows) != 1 {
			t.Errorf("search %v %v", rs, err)
		}
	})

	t.Run("keys are not unique", func(t *testing.T) {
		sfRun(t, c, ctx, "CREATE TABLE gt_dup (id INT PRIMARY KEY, n INT); INSERT INTO gt_dup VALUES (1, 1), (1, 2), (3, 3)")
		err := c.ApplyChanges(ctx, ref("GT_DUP"), []Change{
			{Kind: ChangeUpdate, Key: map[string]Value{"ID": Text("3")}, Values: map[string]Value{"N": Text("30")}},
			{Kind: ChangeDelete, Key: map[string]Value{"ID": Text("1")}}}, true)
		if err == nil || !strings.Contains(err.Error(), "2 rows have this key") {
			t.Fatal(err)
		}
		if n := one(t, "SELECT LISTAGG(n, ',') WITHIN GROUP (ORDER BY n) FROM gt_dup"); n != "1,2,3" {
			t.Errorf("not undone: %s", n)
		}
		err = c.ApplyChanges(ctx, ref("GT_DUP"), []Change{{Kind: ChangeDelete, Key: map[string]Value{"ID": Text("9")}}}, true)
		if err == nil || !strings.Contains(err.Error(), "no longer exists") {
			t.Error(err)
		}
	})

	t.Run("filters", func(t *testing.T) {
		sfRun(t, c, ctx, `CREATE TABLE gt_f (id INT, name VARCHAR, "Mixed ""x""" INT); INSERT INTO gt_f VALUES (1, 'Alice', 1), (2, 'bob%', 2), (3, 'eve_x', 3), (4, 'a^b', 4), (5, 'c\\d', 5)`)
		for _, f := range []struct {
			f    Filter
			want string
		}{
			{Filter{Column: "NAME", Op: OpContains, Value: "ALI"}, "1"}, {Filter{Column: "NAME", Op: OpEndsWith, Value: "%"}, "2"},
			{Filter{Column: "NAME", Op: OpContains, Value: "e_x"}, "3"}, {Filter{Column: "NAME", Op: OpContains, Value: "^"}, "4"},
			{Filter{Column: "NAME", Op: OpContains, Value: `\`}, "5"}, {Filter{Column: `Mixed "x"`, Op: OpGt, Value: "4"}, "5"},
			{Filter{Column: "ID", Op: OpBetween, Value: "2", Value2: "3"}, "2,3"}, {Filter{Column: "ID", Op: OpIn, Value: "1, 4"}, "1,4"},
			{Filter{Column: "NAME", Op: OpNotContain, Value: "b"}, "1,3,5"},
		} {
			p := Page{Filters: []Filter{f.f}, Sort: []Sort{{Column: "ID", Dir: Asc}}}
			rs, err := c.FetchRows(ctx, ref("GT_F"), p, nil)
			if err != nil {
				t.Errorf("%v: %v", f.f, err)
				continue
			}
			if got := strings.Join(names(rs, 0), ","); got != f.want {
				t.Errorf("%v: %s", f.f, got)
			}
			if n, err := c.CountRows(ctx, ref("GT_F"), p, nil); err != nil || int(n) != len(rs.Rows) {
				t.Errorf("count %d %v", n, err)
			}
			if !c.CountIsCostly(ctx, ref("GT_F"), p) {
				t.Error("a filtered count isn't free")
			}
		}
		if c.CountIsCostly(ctx, ref("GT_F"), Page{}) {
			t.Error("a table's own count is free")
		}
		rs, err := c.FetchRows(ctx, ref("GT_F"), Page{Sort: []Sort{{Column: "ID", Dir: Desc}}, Limit: 2, Offset: 1}, nil)
		if err != nil || strings.Join(names(rs, 0), ",") != "4,3" {
			t.Errorf("%v %v", rs, err)
		}
	})

	t.Run("objects and drops", func(t *testing.T) {
		sfRun(t, c, ctx, `CREATE TABLE gt_par (id INT PRIMARY KEY, a INT, b INT, CONSTRAINT uq UNIQUE (b, a)) COMMENT='it''s';
			CREATE TABLE gt_kid (id INT PRIMARY KEY, p INT, q INT, r INT, CONSTRAINT fk1 FOREIGN KEY (p) REFERENCES gt_par(id), CONSTRAINT fk2 FOREIGN KEY (r, q) REFERENCES gt_par(b, a));
			CREATE VIEW gt_view AS SELECT id FROM gt_par; CREATE MATERIALIZED VIEW gt_mview AS SELECT id FROM gt_par;
			CREATE DYNAMIC TABLE gt_dyn TARGET_LAG='1 day' WAREHOUSE=`+c.Config.Warehouse+` AS SELECT id FROM gt_par;
			CREATE TRANSIENT TABLE gt_tran (id INT); CREATE TEMPORARY TABLE gt_temp (id INT); CREATE TABLE "gt lower'x" (id INT)`)
		objs, err := c.Objects(ctx, schema)
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]ObjectKind{}
		for _, o := range objs {
			kinds[o.Name] = o.Kind
		}
		for n, k := range map[string]ObjectKind{"GT_PAR": KindTable, "GT_VIEW": KindView, "GT_MVIEW": KindView, "GT_DYN": KindView, "GT_TRAN": KindTable, "GT_TEMP": KindTable, "gt lower'x": KindTable} {
			if kinds[n] != k {
				t.Errorf("%s is %v", n, kinds[n])
			}
		}
		if !c.CountIsCostly(ctx, ref("GT_VIEW"), Page{}) || !c.CountIsCostly(ctx, ref("GT_MVIEW"), Page{}) {
			t.Error("a view's count runs it")
		}
		fks, err := c.ForeignKeys(ctx, ref("GT_KID"))
		if err != nil || len(fks) != 2 || fks[0].Table != "GT_PAR" || !slices.Equal(fks[1].From, []string{"R", "Q"}) || !slices.Equal(fks[1].Columns, []string{"B", "A"}) {
			t.Errorf("%+v %v", fks, err)
		}
		idx, err := c.Indexes(ctx, ref("GT_PAR"))
		if err != nil || len(idx) != 2 || !idx[0].Primary || idx[1].Name != "UQ" || !slices.Equal(idx[1].Columns, []string{"B", "A"}) {
			t.Errorf("%+v %v", idx, err)
		}
		ti, err := c.TableInfo(ctx, ref("GT_PAR"))
		if err != nil || ti.Comment != "it's" {
			t.Errorf("%+v %v", ti, err)
		}
		for _, o := range objs {
			def, err := c.Definition(ctx, o)
			if err != nil || !strings.HasPrefix(def, "CREATE ") || strings.Contains(strings.ToLower(def), "or replace") {
				t.Errorf("%s: %q %v", o.Name, def, err)
			}
		}
		// The parent stays while a key points at it, and the key stays.
		_, err = c.Drop(ctx, []Object{{Schema: schema, Name: "GT_PAR", Kind: KindTable}}, false)
		if err == nil || !strings.Contains(err.Error(), "foreign key to it") {
			t.Errorf("drop: %v", err)
		}
		if fks, _ := c.ForeignKeys(ctx, ref("GT_KID")); len(fks) != 2 {
			t.Errorf("keys are gone: %v", fks)
		}
		var rest []Object
		for _, o := range objs {
			switch o.Name {
			case "GT_PAR", "GT_KID", "GT_VIEW", "GT_MVIEW", "GT_DYN", "GT_TRAN", "GT_TEMP", "gt lower'x":
				rest = append(rest, o)
			}
		}
		if _, err := c.Drop(ctx, rest, false); err != nil {
			t.Error(err)
		}
		if objs, _ := c.Objects(ctx, schema); slices.ContainsFunc(objs, func(o Object) bool { return o.Name == "GT_DYN" || o.Name == "GT_PAR" }) {
			t.Errorf("still there: %v", objs)
		}
	})

	t.Run("design", func(t *testing.T) {
		nt := NewTable{Name: "gt_made", Comment: `it's \ c`, Columns: []NewColumn{
			{ColumnDef: ColumnDef{Name: "id", Type: "NUMBER"}, PrimaryKey: true, AutoIncrement: true},
			{ColumnDef: ColumnDef{Name: "label", Type: "VARCHAR(50)", DefaultKind: DefaultValue, Default: `a'\b`, Comment: "c'm"}},
			{ColumnDef: ColumnDef{Name: "email", Type: "VARCHAR", Nullable: true, Collation: "en-ci"}, Index: "unique"},
			{ColumnDef: ColumnDef{Name: "n", Type: "NUMBER(10,2)", Nullable: true, DefaultKind: DefaultValue, Default: "1.5"}},
			{ColumnDef: ColumnDef{Name: "ok", Type: "BOOLEAN", Nullable: true, DefaultKind: DefaultValue, Default: "true"}},
			{ColumnDef: ColumnDef{Name: "day", Type: "DATE", Nullable: true, DefaultKind: DefaultValue, Default: "2024-01-02"}},
			{ColumnDef: ColumnDef{Name: "at", Type: "TIMESTAMP_NTZ", Nullable: true, DefaultKind: DefaultExpression, Default: "CURRENT_TIMESTAMP()"}},
			{ColumnDef: ColumnDef{Name: "vec", Type: "VECTOR(FLOAT, 2)", Nullable: true}}}}
		if _, err := c.CreateTable(ctx, schema, nt); err != nil {
			t.Fatal(err)
		}
		if err := c.ApplyChanges(ctx, ref("gt_made"), []Change{{Kind: ChangeInsert, Values: map[string]Value{"email": Text("a")}}}, true); err != nil {
			t.Fatal(err)
		}
		cols, err := c.Columns(ctx, ref("gt_made"))
		if err != nil {
			t.Fatal(err)
		}
		for i, want := range []string{"", `a'\b`, "", "1.5", "true", "2024-01-02", "CURRENT_TIMESTAMP()", ""} {
			if cols[i].DefaultText != want {
				t.Errorf("%s default %q", cols[i].Name, cols[i].DefaultText)
			}
		}
		if !cols[0].AutoIncrement || cols[1].Comment != "c'm" || cols[2].Collation != "en-ci" || cols[2].Type == "" || strings.Contains(cols[2].Type, "COLLATE") {
			t.Errorf("%+v", cols)
		}
		rs, _ := c.FetchRows(ctx, ref("gt_made"), Page{}, nil)
		if r := rs.Rows[0]; r[0].Text != "1" || r[1].Text != `a'\b` || r[3].Text != "1.50" || r[4].Text != "true" || r[5].Text != "2024-01-02" || r[6].Null {
			t.Errorf("%v", r)
		}
		// Reading a column back and saving it unchanged changes nothing.
		for _, col := range cols {
			def := ColumnDef{Name: col.Name, Type: col.Type, Nullable: col.Nullable, DefaultKind: col.DefaultKind, Default: col.DefaultText, Comment: col.Comment, Collation: col.Collation}
			if st, err := c.Alter(ctx, ref("gt_made"), []AlterOp{{Kind: AlterModifyColumn, Column: col.Name, Def: &def}}, true); err == nil {
				t.Errorf("%s: %v", col.Name, st)
			}
		}
		alter := func(op AlterOp) {
			t.Helper()
			if st, err := c.Alter(ctx, ref("gt_made"), []AlterOp{op}, false); err != nil {
				t.Errorf("%s %v: %v", op.Kind, st, err)
			}
		}
		alter(AlterOp{Kind: AlterAddColumn, Def: &ColumnDef{Name: "extra", Type: "NUMBER", Nullable: true, DefaultKind: DefaultValue, Default: "7", Comment: "x"}})
		alter(AlterOp{Kind: AlterModifyColumn, Column: "label", Def: &ColumnDef{Name: "label", Type: "VARCHAR(60)", Comment: "c'm"}})
		alter(AlterOp{Kind: AlterModifyColumn, Column: "extra", Def: &ColumnDef{Name: "extra2", Type: "NUMBER(38,0)", DefaultKind: DefaultValue, Default: "7", Comment: "y"}})
		alter(AlterOp{Kind: AlterAddIndex, Index: &IndexDef{Name: "gt_u", Columns: []string{"label", "n"}, Unique: true}})
		alter(AlterOp{Kind: AlterRenameIndex, Name: "gt_u", NewName: "gt_u2"})
		alter(AlterOp{Kind: AlterDropIndex, Name: "gt_u2"})
		alter(AlterOp{Kind: AlterSetPrimaryKey, Columns: []string{"id", "label"}})
		alter(AlterOp{Kind: AlterSetPrimaryKey})
		alter(AlterOp{Kind: AlterTableComment, Comment: "n'ew"})
		alter(AlterOp{Kind: AlterAddForeignKey, ForeignKey: &ForeignKeyDef{Name: "gt_fk", Columns: []string{"id"}, RefTable: "GT_DUP", RefColumns: []string{"ID"}}})
		if fks, _ := c.ForeignKeys(ctx, ref("gt_made")); len(fks) != 1 {
			t.Errorf("%v", fks)
		}
		alter(AlterOp{Kind: AlterDropForeignKey, Name: "gt_fk"})
		alter(AlterOp{Kind: AlterDropColumn, Column: "extra2"})
		// Renaming keeps the table where it is, whatever the current schema.
		c.UseSchema(ctx, "PUBLIC")
		alter(AlterOp{Kind: AlterRenameTable, NewName: "gt_made2"})
		c.UseSchema(ctx, schema)
		if ti, err := c.TableInfo(ctx, ref("gt_made2")); err != nil || ti.Comment != "n'ew" {
			t.Errorf("%+v %v", ti, err)
		}
		if _, err := c.Alter(ctx, ref("gt_made2"), []AlterOp{{Kind: AlterTruncate}}, false); err != nil {
			t.Error(err)
		}
	})

	t.Run("databases and schemas", func(t *testing.T) {
		if _, err := c.CreateDatabase(ctx, NewDatabase{Name: "gt_s2", Kind: "schema"}); err != nil {
			t.Fatal(err)
		}
		c.UseSchema(ctx, schema)
		sfRun(t, c, ctx, `CREATE TABLE "gt_s2".t (id INT)`)
		if _, err := c.DropDatabase(ctx, DropDatabase{Name: "gt_s2", Kind: "schema"}); err == nil || !strings.Contains(err.Error(), "holds 1") {
			t.Errorf("dropped a schema with a table: %v", err)
		}
		if _, err := c.DropDatabase(ctx, DropDatabase{Name: "gt_s2", Kind: "schema", Cascade: true}); err != nil {
			t.Error(err)
		}
		if _, err := c.DropDatabase(ctx, DropDatabase{Name: c.Config.DBName, Kind: "database"}); err == nil || !strings.Contains(err.Error(), "connected to") {
			t.Errorf("%v", err)
		}
	})

	t.Run("editor", func(t *testing.T) {
		c.UseSchema(ctx, schema)
		res := sfRun(t, c, ctx, `CREATE TABLE gt_e (id INT, vec VECTOR(FLOAT,2)); INSERT INTO gt_e SELECT 1, [1,2]::VECTOR(FLOAT,2) UNION ALL SELECT 2, NULL;
			UPDATE gt_e SET id = id + 10; SELECT id, vec, vec, 1/3::FLOAT AS "a b" FROM gt_e ORDER BY id; BEGIN; DELETE FROM gt_e; ROLLBACK; SELECT COUNT(*) FROM gt_e;
			SET x = 5; SELECT $x; EXECUTE IMMEDIATE $$ BEGIN LET a := 1; RETURN a + 1; END $$; SHOW TABLES LIKE 'GT_E'; SELECT X'00ff', TRUE, NULL, 1 a, 2 a`)
		if res[1].RowsAffected != 2 || res[2].RowsAffected != 2 || res[5].RowsAffected != 2 {
			t.Errorf("affected %d %d %d", res[1].RowsAffected, res[2].RowsAffected, res[5].RowsAffected)
		}
		if r := res[3].Set; len(r.Rows) != 2 || r.Rows[0][1].Text == "" || r.Types[1] != "VECTOR" || r.Columns[3] != "a b" || r.Rows[0][3].Text != "0.3333333333333333" || !r.Rows[1][2].Null {
			t.Errorf("%+v", r)
		}
		if res[7].Set.Rows[0][0].Text != "2" || res[9].Set.Rows[0][0].Text != "5" || res[10].Set == nil || res[10].Set.Rows[0][0].Text != "2" {
			t.Errorf("%+v %+v %+v", res[7].Set, res[9].Set, res[10].Set)
		}
		if r := res[12].Set.Rows[0]; r[0].Text != "0x00FF" || !r[0].Hex || r[1].Text != "true" || !r[2].Null {
			t.Errorf("%v", r)
		}
		bad, _ := c.Execute(ctx, "SELEC 1; SELECT 2", 0)
		if len(bad) != 1 || bad[0].Err == nil {
			t.Errorf("%+v", bad)
		}
		put, _ := c.Execute(ctx, "PUT file:///etc/hostname @~", 0)
		if put[0].Err == nil || !strings.Contains(put[0].Err.Error(), "PUT and GET") {
			t.Errorf("%+v", put)
		}
		sfRun(t, c, ctx, "USE SCHEMA PUBLIC")
		if c.Schema() != "PUBLIC" {
			t.Errorf("schema %s", c.Schema())
		}
		c.UseSchema(ctx, schema)
	})

	t.Run("cancel", func(t *testing.T) {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		start := time.Now()
		res, err := c.Execute(cctx, "CALL SYSTEM$WAIT(60)", 0)
		if err == nil && res[0].Err == nil || time.Since(start) > 20*time.Second {
			t.Errorf("%v %v after %v", err, res, time.Since(start))
		}
		if n := one(t, "SELECT 1"); n != "1" || c.Schema() != schema {
			t.Errorf("after: %s %s", n, c.Schema())
		}
	})

	t.Run("csv", func(t *testing.T) {
		sfRun(t, c, ctx, "CREATE TABLE gt_csv (id INT, d DATE, v VARIANT, bin BINARY, s VARCHAR)")
		var b strings.Builder
		b.WriteString("id,d,v,bin,s\n1,2024-02-29,\"{\"\"a\"\":1}\",00FF,\"it's \\ x\"\n2,,,,\n")
		for i := 3; i <= 20000; i++ {
			fmt.Fprintf(&b, "%d,2024-01-01,%d,ab,row %d\n", i, i, i)
		}
		o := CSVImportOptions{Delimiter: ',', Header: true, Columns: []string{"ID", "D", "V", "BIN", "S"}, Mapping: []int{0, 1, 2, 3, 4}, EmptyAsNull: true}
		start := time.Now()
		n, err := c.ImportCSV(ctx, strings.NewReader(b.String()), ref("GT_CSV"), o, nil)
		if err != nil || n != 20000 {
			t.Fatalf("%d %v", n, err)
		}
		t.Logf("20,000 rows in %v", time.Since(start).Round(time.Second))
		rs, _ := c.FetchRows(ctx, ref("GT_CSV"), Page{Sort: []Sort{{Column: "ID", Dir: Asc}}, Limit: 2}, nil)
		if got := fmt.Sprint(rs.Rows); !strings.Contains(got, `{"a":1}`) || rs.Rows[0][3].Text != "0x00FF" || rs.Rows[0][4].Text != `it's \ x` || !rs.Rows[1][2].Null {
			t.Error(got)
		}
		o.Truncate = true
		if _, err := c.ImportCSV(ctx, strings.NewReader("id,d,v,bin,s\n1,nonsense,,,\n"), ref("GT_CSV"), o, nil); err == nil {
			t.Error("a wrong date went in")
		}
		if n, _ := c.CountRows(ctx, ref("GT_CSV"), Page{}, nil); n != 20000 {
			t.Errorf("%d rows after a failed import", n)
		}
	})

	t.Run("dump", func(t *testing.T) {
		sfRun(t, c, ctx, `CREATE TABLE gt_a_kid (id INT PRIMARY KEY, p INT, CONSTRAINT kfk FOREIGN KEY (p) REFERENCES gt_dup (id), CONSTRAINT kfk2 FOREIGN KEY (p) REFERENCES gt_dup (id));
			INSERT INTO gt_a_kid VALUES (1, 3); CREATE VIEW gt_dview AS SELECT id FROM gt_dup`)
		objs := []Object{{schema, "GT_A_KID", KindTable}, {schema, "GT_ALL", KindTable}, {schema, "GT_DUP", KindTable}, {schema, "GT_CSV", KindTable}, {schema, "GT_DVIEW", KindView}}
		sum := func() string {
			return one(t, "SELECT (SELECT HASH_AGG(* EXCLUDE (g, geo, vec, m)) FROM gt_all) || '/' || (SELECT HASH_AGG(*) FROM gt_csv) || '/' || (SELECT HASH_AGG(*) FROM gt_dup) || '/' || (SELECT COUNT(*) FROM gt_dview) || '/' || (SELECT LISTAGG(ST_ASTEXT(g) || ST_ASTEXT(geo) || m::OBJECT::VARCHAR || vec::ARRAY::VARCHAR) FROM gt_all)")
		}
		before := sum()
		var buf bytes.Buffer
		if err := c.Dump(ctx, &buf, DumpOptions{Objects: objs, Structure: true, Data: true, DropFirst: true, BatchSize: 1000}, nil); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(buf.String()), "or replace") {
			t.Error("the dump replaces tables")
		}
		sfRun(t, c, ctx, "CREATE OR REPLACE SCHEMA GT_SUITE_COPY")
		defer c.Execute(ctx, "DROP SCHEMA IF EXISTS GT_SUITE_COPY; USE SCHEMA "+schema, 0)
		// Whatever the session's formats are.
		sfRun(t, c, ctx, "ALTER SESSION SET DATE_INPUT_FORMAT='DD/MM/YYYY', TIMESTAMP_INPUT_FORMAT='DD/MM/YYYY HH24:MI', TIME_INPUT_FORMAT='HH24:MI', BINARY_INPUT_FORMAT='UTF-8'")
		start := time.Now()
		res, err := c.ImportSQL(ctx, bytes.NewReader(buf.Bytes()), ImportOptions{}, nil)
		if err != nil {
			t.Fatalf("%v %+v", err, res)
		}
		t.Logf("%d statements in %v", res.Statements, time.Since(start).Round(time.Second))
		sfRun(t, c, ctx, "ALTER SESSION UNSET DATE_INPUT_FORMAT, TIMESTAMP_INPUT_FORMAT, TIME_INPUT_FORMAT, BINARY_INPUT_FORMAT")
		if c.Schema() != "GT_SUITE_COPY" {
			t.Fatalf("imported into %s", c.Schema())
		}
		if after := sum(); after != before {
			t.Errorf("copied data differs\n%s\n%s", before, after)
		}
		if fks, _ := c.ForeignKeys(ctx, TableRef{Schema: "GT_SUITE_COPY", Name: "GT_A_KID"}); len(fks) != 2 || fks[0].Schema != "GT_SUITE_COPY" {
			t.Errorf("%+v", fks)
		}
	})
}

// What a wrong setting says.
func TestSnowflakeSignIn(t *testing.T) {
	cfg, _ := serverConfig(t, "SNOWFLAKE", model.DriverSnowflake)
	key := os.Getenv("GOTABLE_TEST_SNOWFLAKE_KEY")
	base := *cfg
	base.Auth, base.KeyPath, base.Port = model.AuthKey, key, 0
	base.Role, base.Warehouse = os.Getenv("GOTABLE_TEST_SNOWFLAKE_ROLE"), os.Getenv("GOTABLE_TEST_SNOWFLAKE_WAREHOUSE")
	for name, ch := range map[string]func(*model.Connection){
		"password":  func(c *model.Connection) { c.Auth = "" },
		"token":     func(c *model.Connection) { c.Auth = model.AuthToken },
		"user":      func(c *model.Connection) { c.User = "GT_NOBODY" },
		"role":      func(c *model.Connection) { c.Role = "GT_NOROLE" },
		"warehouse": func(c *model.Connection) { c.Warehouse = "GT_NOWH" },
		"database":  func(c *model.Connection) { c.DBName = "GT_NODB" },
		"account":   func(c *model.Connection) { c.Host = "GTNOSUCH-ACCOUNT99" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			ch(&cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			start := time.Now()
			c, err := Open(ctx, &cfg, Credentials{Password: "wrong"})
			if err == nil {
				c.Close()
				t.Fatal("signed in")
			}
			t.Logf("%v: %v", time.Since(start).Round(100*time.Millisecond), err)
		})
	}
	for name, ch := range map[string]func(*model.Connection){
		"lower case":  func(c *model.Connection) { c.DBName = strings.ToLower(c.DBName) },
		"host name":   func(c *model.Connection) { c.Host = "https://" + strings.ToLower(c.Host) + ".snowflakecomputing.com/" },
		"no database": func(c *model.Connection) { c.DBName = "" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			ch(&cfg)
			c := openTest(t, &cfg, "")
			if v, err := c.Schemas(context.Background()); err != nil || cfg.DBName != "" && len(v) == 0 {
				t.Errorf("%v %v", v, err)
			}
		})
	}
}
