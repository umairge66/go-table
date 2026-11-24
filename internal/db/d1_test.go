package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"gotable/internal/model"
)

func TestD1Values(t *testing.T) {
	for in, want := range map[string]string{"1": "1", "-5": "-5", "1.5": "1.5", "1E+21": "1e+21", "9007199254740991": "9007199254740991", "9007199254740992": "≈9007199254740992",
		"-9007199254740992": "≈-9007199254740992", "9223372036854776000": "≈9223372036854776000", "100000000000000000000": "≈100000000000000000000"} {
		v, err := d1Value(json.Number(in))
		if err != nil || fmt.Sprint(v) != want {
			t.Errorf("%s: %v %v", in, v, err)
		}
	}
	v, err := d1Value([]any{json.Number("0"), json.Number("255")})
	if b, ok := v.([]byte); err != nil || !ok || string(b) != "\x00\xff" {
		t.Error(v, err)
	}
	if _, err := d1Value([]any{json.Number("256")}); err == nil {
		t.Error("not a byte")
	}
}

func d1Fake(t *testing.T, answer func(path, body string) (int, string)) (*Conn, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		code, out := answer(r.URL.RequestURI(), string(b))
		w.WriteHeader(code)
		io.WriteString(w, out)
	}))
	t.Cleanup(srv.Close)
	old := d1API
	d1API = srv.URL
	t.Cleanup(func() { d1API = old })
	cfg := model.NewConnection(model.DriverD1)
	cfg.Name, cfg.User, cfg.DBName = "x", "acc", "shop"
	return Open(context.Background(), cfg, Credentials{Password: "tok"})
}

const d1Info = `{"success":true,"result":[{"uuid":"00000000-1111-4222-8333-444444444444","name":"shop"}]}`

func TestD1Connect(t *testing.T) {
	ok := `{"success":true,"result":[{"results":{"columns":["1"],"rows":[[1]]},"meta":{"changes":0}}]}`
	for name, tc := range map[string]struct {
		answer func(path, body string) (int, string)
		want   string
	}{
		"fine": {func(path, _ string) (int, string) {
			switch {
			case strings.Contains(path, "name=shop"):
				return 200, d1Info
			case strings.HasSuffix(path, "/raw"):
				return 200, ok
			}
			return 200, `{"success":true,"result":{"uuid":"00000000-1111-4222-8333-444444444444","name":"shop","running_in_region":"WEUR"}}`
		}, ""},
		"bad token": {func(string, string) (int, string) {
			return 401, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`
		}, "authentication failed"},
		"no database": {func(string, string) (int, string) { return 200, `{"success":true,"result":[]}` }, `no D1 database "shop"`},
		"a similar name only": {func(string, string) (int, string) {
			return 200, `{"success":true,"result":[{"uuid":"00000000-1111-4222-8333-444444444444","name":"shop-old"}]}`
		}, "it has shop-old"},
		"not Cloudflare": {func(string, string) (int, string) { return 502, "<html>" }, "502"},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := d1Fake(t, tc.answer)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if v, _ := c.ServerVersion(context.Background()); v != "Cloudflare D1 · shop (WEUR)" {
					t.Error(v)
				}
				c.Close()
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Error(err)
			}
		})
	}
}

// A commit whose answer is lost says that it may have been saved; nothing is
// sent before COMMIT, or after ROLLBACK.
func TestD1Commit(t *testing.T) {
	var batches []string
	lose := false
	c, err := d1Fake(t, func(path, body string) (int, string) {
		if strings.Contains(path, "name=shop") {
			return 200, d1Info
		}
		if strings.Contains(body, `"batch"`) {
			batches = append(batches, body)
			if lose {
				return 200, `{"success":tr`
			}
		}
		return 200, `{"success":true,"result":[{"results":{"columns":["foreign_keys"],"rows":[[1]]},"meta":{"changes":0}},{"results":{"columns":[],"rows":[]},"meta":{"changes":3}}]}`
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, _ := c.conn.BeginTx(ctx, nil)
	tx.ExecContext(ctx, "INSERT INTO t VALUES (?)", int64(1)<<60)
	tx.Rollback()
	if len(batches) != 0 {
		t.Fatal(batches)
	}
	tx, _ = c.conn.BeginTx(ctx, nil)
	tx.ExecContext(ctx, "INSERT INTO t VALUES (?, ?)", int64(1)<<60, []byte{0, 200})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || !strings.Contains(batches[0], `"1152921504606846976"`) || !strings.Contains(batches[0], "[0,200]") || !strings.Contains(batches[0], "defer_foreign_keys") {
		t.Fatal(batches)
	}
	lose = true
	tx, _ = c.conn.BeginTx(ctx, nil)
	tx.ExecContext(ctx, "INSERT INTO t VALUES (1)")
	if err := tx.Commit(); err == nil || !strings.Contains(err.Error(), "may or may not") {
		t.Error(err)
	}
}

// testD1 is what only the real service shows.
func testD1(t *testing.T, c *Conn) {
	ctx := context.Background()
	exec := func(q string) Result {
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
	drop := func() {
		// Two tables that reference each other go together, in one batch.
		if objs, err := c.Objects(ctx, "main"); err == nil && slices.ContainsFunc(objs, func(o Object) bool { return o.Name == "gt_da" }) {
			if _, err := c.Drop(ctx, []Object{{Schema: "main", Name: "gt_da", Kind: KindTable}, {Schema: "main", Name: "gt_db", Kind: KindTable}}, false); err != nil {
				t.Error(err)
			}
		}
		for _, n := range []string{"gt_dgrand", "gt_dchild", "gt_dparent", "gt_dbig", "gt_dnorow", "gt_dmany"} {
			c.Execute(ctx, "PRAGMA defer_foreign_keys = on; DROP TABLE IF EXISTS "+n, 0)
		}
	}
	drop()
	defer drop()
	count := func(tab string) string { t.Helper(); return exec("SELECT count(*) FROM " + tab).Set.Rows[0][0].Text }

	t.Run("references", func(t *testing.T) {
		exec(`CREATE TABLE gt_dparent (id INTEGER PRIMARY KEY, n TEXT);
CREATE TABLE gt_dchild (id INTEGER PRIMARY KEY, p INTEGER REFERENCES GT_DPARENT(id) ON DELETE CASCADE);
INSERT INTO gt_dparent VALUES (1, 'a'), (2, 'b'); INSERT INTO gt_dchild VALUES (10, 1), (20, 2);`)
		parent := TableRef{Schema: "main", Name: "gt_dparent"}
		// Changing a column's type rebuilds the table, which would empty the child.
		_, err := c.Alter(ctx, parent, []AlterOp{{Kind: AlterModifyColumn, Column: "n", Def: &ColumnDef{Name: "n", Type: "INTEGER", Nullable: true}}}, false)
		if err == nil || !strings.Contains(err.Error(), "gt_dchild references it") {
			t.Error(err)
		}
		for _, cascade := range []bool{false, true} {
			if _, err := c.Drop(ctx, []Object{{Schema: "main", Name: "gt_dparent", Kind: KindTable}}, cascade); err == nil || !strings.Contains(err.Error(), "referenced by gt_dchild") {
				t.Error(cascade, err)
			}
		}
		if count("gt_dchild") != "2" || count("gt_dparent") != "2" {
			t.Fatal("rows were lost")
		}
		// Plain changes to it, and a rebuild of the child, are fine.
		if _, err := c.Alter(ctx, parent, []AlterOp{{Kind: AlterAddColumn, Def: &ColumnDef{Name: "x", Type: "TEXT", Nullable: true}}}, false); err != nil {
			t.Error(err)
		}
		if _, err := c.Alter(ctx, TableRef{Schema: "main", Name: "gt_dchild"}, []AlterOp{{Kind: AlterModifyColumn, Column: "p", Def: &ColumnDef{Name: "p", Type: "BIGINT", Nullable: true}}}, false); err != nil {
			t.Error(err)
		}
		if count("gt_dchild") != "2" {
			t.Error("the rebuild lost rows")
		}
		if _, err := c.Drop(ctx, []Object{{Schema: "main", Name: "gt_dparent", Kind: KindTable}, {Schema: "main", Name: "gt_dchild", Kind: KindTable}}, false); err != nil {
			t.Error(err)
		}
	})

	t.Run("numbers", func(t *testing.T) {
		exec(`CREATE TABLE gt_dbig (id INTEGER PRIMARY KEY, n INTEGER, f REAL, any);
INSERT INTO gt_dbig VALUES (9007199254740993, -9223372036854775808, 1e300, 9007199254740993), (9007199254740992, 9223372036854775807, 9e999, x'00ff'), (1, 5, 0.1, 'text')`)
		ref := TableRef{Schema: "main", Name: "gt_dbig"}
		rs, err := c.FetchRows(ctx, ref, Page{Sort: []Sort{{Column: "id", Dir: Desc}}, Limit: 10}, nil)
		var got []string
		for _, r := range rs.Rows {
			got = append(got, fmt.Sprint(r))
		}
		if want := []string{"[9007199254740993 -9223372036854775808 1.0e+300 9007199254740993]", "[9007199254740992 9223372036854775807 9e999 0x00FF]", "[1 5 0.1 text]"}; err != nil || !slices.Equal(got, want) {
			t.Errorf("%q %v", got, err)
		}
		// Editing by such a key changes that row and not its neighbour.
		if err := c.ApplyChanges(ctx, ref, []Change{{Kind: ChangeUpdate, Key: map[string]Value{"id": Text("9007199254740993")}, Values: map[string]Value{"n": Text("7")}}}, true); err != nil {
			t.Error(err)
		}
		if r := exec("SELECT CAST(id AS TEXT) FROM gt_dbig WHERE n = 7"); len(r.Set.Rows) != 1 || r.Set.Rows[0][0].Text != "9007199254740993" {
			t.Errorf("%+v", r.Set)
		}
		// In the editor it is marked, and can't be taken for a key.
		if r := exec("SELECT id FROM gt_dbig WHERE n = 7"); r.Set.Rows[0][0].Text != "≈9007199254740992" {
			t.Errorf("%+v", r.Set)
		}
		err = c.ApplyChanges(ctx, ref, []Change{{Kind: ChangeUpdate, Key: map[string]Value{"id": Text("1")}, Values: map[string]Value{"n": Text("6")}},
			{Kind: ChangeDelete, Key: map[string]Value{"id": Text("≈9007199254740992")}}}, true)
		if err == nil || !strings.Contains(err.Error(), "≈9007199254740992") || !strings.Contains(err.Error(), "no longer exists") {
			t.Error(err)
		}
		if r := exec("SELECT n FROM gt_dbig WHERE id = 1"); r.Set.Rows[0][0].Text != "5" {
			t.Error("a failed save left something", r.Set)
		}
	})

	t.Run("export", func(t *testing.T) {
		exec(`CREATE TABLE gt_da (id INTEGER PRIMARY KEY, b INTEGER REFERENCES gt_db(id));
CREATE TABLE gt_db (id INTEGER PRIMARY KEY, a INTEGER REFERENCES gt_da(id));
INSERT INTO gt_da VALUES (1, NULL); INSERT INTO gt_db VALUES (1, 1); UPDATE gt_da SET b = 1;
CREATE TABLE gt_dnorow (k TEXT PRIMARY KEY, v BLOB) WITHOUT ROWID;
INSERT INTO gt_dnorow VALUES ('a', x'00ff'), ('b', NULL);
CREATE TABLE gt_dmany (id INTEGER PRIMARY KEY, pad TEXT);
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 4500) INSERT INTO gt_dmany SELECT i * 3, hex(randomblob(400)) FROM n;
INSERT INTO gt_dmany VALUES (9007199254740995, 'far')`)
		seen := map[string]bool{}
		if err := c.StreamRows(ctx, TableRef{Schema: "main", Name: "gt_dmany"}, Page{}, nil, func(cols, _ []string, r []Value) error {
			if len(cols) != 2 || cols[0] != "id" {
				return fmt.Errorf("columns %v", cols)
			}
			seen[r[0].Text] = true
			return nil
		}); err != nil || len(seen) != 4501 || !seen["9007199254740995"] {
			t.Error(len(seen), err)
		}
		n := 0
		if err := c.StreamRows(ctx, TableRef{Schema: "main", Name: "gt_dmany"}, Page{RawWhere: "id < 9000", Filters: []Filter{{Column: "id", Op: OpGt, Value: "300"}}}, nil, func(_, _ []string, _ []Value) error { n++; return nil }); err != nil || n != 2899 {
			t.Error(n, err)
		}
		stop := errors.New("stop")
		if err := c.StreamRows(ctx, TableRef{Schema: "main", Name: "gt_dmany"}, Page{}, nil, func(_, _ []string, _ []Value) error { return stop }); !errors.Is(err, stop) {
			t.Error(err)
		}
		objs := []Object{}
		for _, o := range []string{"gt_da", "gt_db", "gt_dnorow", "gt_dmany", "gt_dbig"} {
			objs = append(objs, Object{Schema: "main", Name: o, Kind: KindTable})
		}
		var dump strings.Builder
		if err := c.Dump(ctx, &dump, DumpOptions{Objects: objs, Structure: true, Data: true, DropFirst: true, BatchSize: 1000}, nil); err != nil {
			t.Fatal(err)
		}
		sums := func() string {
			return fmt.Sprint(exec("SELECT (SELECT group_concat(id || ':' || b) FROM gt_da), (SELECT group_concat(id || ':' || a) FROM gt_db), (SELECT group_concat(k || ':' || coalesce(hex(v), '-')) FROM gt_dnorow), (SELECT count(*) || ':' || sum(length(pad)) || ':' || max(id) FROM gt_dmany), (SELECT group_concat(id || ':' || n || ':' || typeof(f) || ':' || f) FROM (SELECT * FROM gt_dbig ORDER BY id))").Set.Rows)
		}
		before := sums()
		res, err := c.ImportSQL(ctx, strings.NewReader(dump.String()), ImportOptions{Transaction: true}, nil)
		if err != nil || res.Rows < 4500 {
			t.Fatalf("%+v %v", res, err)
		}
		if after := sums(); after != before {
			t.Errorf("%s\n%s", before, after)
		}
	})

	t.Run("a large CSV", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("id,pad\n")
		for i := range 20_000 {
			fmt.Fprintf(&b, "%d,row %d\n", 1_000_000+i, i)
		}
		ref := TableRef{Schema: "main", Name: "gt_dmany"}
		n, err := c.ImportCSV(ctx, strings.NewReader(b.String()), ref, CSVImportOptions{Header: true, Columns: []string{"id", "pad"}, Mapping: []int{0, 1}, Truncate: true}, nil)
		if err != nil || n != 20_000 || count("gt_dmany") != "20000" {
			t.Error(n, err)
		}
		// One bad row leaves the table as it was.
		if _, err := c.ImportCSV(ctx, strings.NewReader("id,pad\n1,a\n1,b\n"), ref, CSVImportOptions{Header: true, Columns: []string{"id", "pad"}, Mapping: []int{0, 1}, Truncate: true}, nil); err == nil {
			t.Error("a duplicate key was imported")
		}
		if count("gt_dmany") != "20000" {
			t.Error("a failed import changed the table")
		}
	})
}
