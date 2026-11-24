package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"
)

// hranaFake answers /v3/pipeline and /v3/cursor with what each test says.
func hranaFake(t *testing.T, token string, answer func(path, baton string, body string) (int, string)) (*sql.DB, *int) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		b, _ := io.ReadAll(r.Body)
		var in struct {
			Baton *string `json:"baton"`
		}
		json.Unmarshal(b, &in)
		baton := ""
		if in.Baton != nil {
			baton = *in.Baton
		}
		if want := "Bearer " + token; token != "" && r.Header.Get("Authorization") != want {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"Unauthorized: bad token"}`)
			return
		}
		code, out := answer(r.URL.Path, baton, string(b))
		w.WriteHeader(code)
		io.WriteString(w, out)
	}))
	t.Cleanup(srv.Close)
	db := sql.OpenDB(newHrana(srv.URL, "", "secret", nil))
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	return db, &n
}

func hranaOK(baton string, autocommit bool) string {
	return fmt.Sprintf(`{"baton":%q,"base_url":null,"results":[{"type":"ok","response":{"type":"execute","result":{"cols":[],"rows":[],"affected_row_count":1,"last_insert_rowid":"7"}}},{"type":"ok","response":{"type":"get_autocommit","is_autocommit":%v}}]}`, baton, autocommit)
}

const hranaExpired = `{"message":"The stream has expired due to inactivity","code":"STREAM_EXPIRED"}`

// A stream that expired between statements is replaced, and the statement
// runs once.
func TestHranaStreamExpires(t *testing.T) {
	ran := 0
	db, _ := hranaFake(t, "secret", func(_, baton, body string) (int, string) {
		if baton == "one" {
			return 400, hranaExpired
		}
		if strings.Contains(body, "INSERT") {
			ran++
		}
		return 200, hranaOK("one", true)
	})
	ctx := context.Background()
	for range 2 {
		res, err := db.ExecContext(ctx, "INSERT INTO t VALUES (1)")
		if err != nil {
			t.Fatal(err)
		}
		if id, _ := res.LastInsertId(); id != 7 {
			t.Errorf("id %d", id)
		}
	}
	if ran != 2 {
		t.Errorf("ran %d times", ran)
	}
}

// In a transaction it can't be replaced: the rest of the transaction must
// fail rather than run outside it.
func TestHranaTransactionLost(t *testing.T) {
	inserts := 0
	db, _ := hranaFake(t, "", func(_, baton, body string) (int, string) {
		switch {
		case strings.Contains(body, "BEGIN"):
			return 200, hranaOK("tx", false)
		case baton == "tx":
			return 400, hranaExpired
		case strings.Contains(body, "INSERT"):
			inserts++
		}
		return 200, hranaOK("new", true)
	})
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO t VALUES (1)"); !errors.Is(err, errHranaLost) {
		t.Errorf("err %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Errorf("rollback %v", err)
	}
	if inserts != 0 {
		t.Errorf("%d inserts ran outside the transaction", inserts)
	}
	if _, err := db.Exec("INSERT INTO t VALUES (2)"); err != nil || inserts != 1 {
		t.Errorf("afterwards: %v, %d", err, inserts)
	}
}

func TestHranaCommitUnknown(t *testing.T) {
	db, _ := hranaFake(t, "", func(_, _, body string) (int, string) {
		if strings.Contains(body, "COMMIT") {
			return 200, `{"baton":` // cut off
		}
		return 200, hranaOK("tx", false)
	})
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil || !strings.Contains(err.Error(), "may or may not") {
		t.Errorf("err %v", err)
	}
}

// An answer cut off in the middle is an error, not a shorter table.
func TestHranaRowsCutOff(t *testing.T) {
	head := `{"baton":"b","base_url":null}` + "\n" + `{"type":"step_begin","step":0,"cols":[{"name":"a","decltype":"INTEGER"}]}` + "\n"
	row := `{"type":"row","row":[{"type":"integer","value":"1"}]}` + "\n"
	end := `{"type":"step_end","affected_row_count":0,"last_insert_rowid":null}` + "\n"
	for name, c := range map[string]struct {
		body string
		ok   bool
	}{"whole": {head + row + row + end, true}, "no end": {head + row + row, false}, "half a row": {head + row + row[:20], false},
		"error after rows": {head + row + `{"type":"step_error","step":0,"error":{"message":"boom","code":"X"}}` + "\n", false}, "nothing": {"", false}} {
		t.Run(name, func(t *testing.T) {
			db, _ := hranaFake(t, "", func(string, string, string) (int, string) { return 200, c.body })
			rows, err := db.Query("SELECT a FROM t")
			n := 0
			if err == nil {
				for rows.Next() {
					n++
				}
				err = rows.Err()
				rows.Close()
			}
			if (err == nil) != c.ok {
				t.Errorf("%d rows, err %v", n, err)
			}
		})
	}
}

func TestHranaAuth(t *testing.T) {
	db, _ := hranaFake(t, "other", func(string, string, string) (int, string) { return 200, hranaOK("b", true) })
	if err := db.Ping(); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("err %v", err)
	}
	turso, _ := hranaFake(t, "", func(string, string, string) (int, string) { return 400, "JWT error: InvalidToken" }) // Turso's words
	if err := turso.Ping(); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("err %v", err)
	}
	// Something that isn't libSQL.
	web, _ := hranaFake(t, "", func(string, string, string) (int, string) { return 404, "<html>nothing here</html>" })
	if err := web.Ping(); err == nil || !strings.Contains(err.Error(), "is this a libSQL server") {
		t.Errorf("err %v", err)
	}
}

// On a real server: exact values, an idle stream, a long result, cancelling.
func TestLibSQLSession(t *testing.T) {
	cfg, pw := serverConfig(t, "LIBSQL", model.DriverLibSQL)
	testLibSQLSession(t, openTest(t, cfg, pw), 60000)
}

// testLibSQLSession uses a table of many rows.
func testLibSQLSession(t *testing.T, c *Conn, many int) {
	ctx := context.Background()
	total := strconv.Itoa(many)
	one := func(q string) []Value {
		t.Helper()
		res, err := c.Execute(ctx, q, 0)
		if err != nil || res[len(res)-1].Err != nil {
			t.Fatalf("%s: %v %+v", q, err, res)
		}
		if s := res[len(res)-1].Set; s != nil && len(s.Rows) > 0 {
			return s.Rows[0]
		}
		return nil
	}
	v := one("SELECT 9007199254740993, -1.5, 'héllo', x'00ff', NULL, ''")
	for i, want := range []string{"9007199254740993", "-1.5", "héllo", "0x00FF", "", ""} {
		if v[i].Text != want || v[i].Null != (i == 4) {
			t.Errorf("value %d: %+v", i, v[i])
		}
	}

	one("DROP TABLE IF EXISTS gt_many")
	one("CREATE TABLE gt_many (id INTEGER PRIMARY KEY, pad TEXT)")
	defer one("DROP TABLE gt_many")
	one("WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < " + total + ") INSERT INTO gt_many SELECT i, hex(randomblob(150)) FROM n")
	n := 0
	err := c.StreamRows(ctx, TableRef{Schema: "main", Name: "gt_many"}, Page{}, nil, func(_, _ []string, _ []Value) error { n++; return nil })
	if err != nil || n != many {
		t.Fatalf("streamed %d rows: %v", n, err)
	}
	// Stopped early, the session goes on.
	stop := errors.New("enough")
	n = 0
	if err := c.StreamRows(ctx, TableRef{Schema: "main", Name: "gt_many"}, Page{}, nil, func(_, _ []string, _ []Value) error {
		if n++; n == 10 {
			return stop
		}
		return nil
	}); !errors.Is(err, stop) {
		t.Errorf("stop: %v", err)
	}
	if v := one("SELECT count(*) FROM gt_many"); v[0].Text != total {
		t.Errorf("count %v", v)
	}

	quick, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	res, err := c.Execute(quick, "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n) SELECT count(*) FROM n", 0)
	if err == nil && res[0].Err == nil {
		t.Error("an endless query wasn't cancelled")
	}
	one("SELECT 1")

	if testing.Short() {
		return
	}
	one("BEGIN")
	one("DELETE FROM gt_many WHERE id = 1")
	time.Sleep(12 * time.Second) // longer than the server keeps an idle stream
	res, err = c.Execute(ctx, "DELETE FROM gt_many WHERE id = 2", 0)
	if err == nil && res[0].Err == nil {
		t.Error("a statement ran outside the transaction it belonged to")
	}
	if v := one("SELECT count(*) FROM gt_many"); v[0].Text != total {
		t.Errorf("count after the lost transaction: %v", v)
	}
	time.Sleep(12 * time.Second) // outside one, nobody notices
	one("SELECT 1")
}

// A dump and a CSV, out and back in.
func testLibSQLTransfer(t *testing.T, c *Conn) {
	ctx := context.Background()
	exec := func(q string) {
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
	}
	drop := func() {
		c.Execute(ctx, "DROP VIEW IF EXISTS gt_tv; DROP TABLE IF EXISTS gt_tchild; DROP TABLE IF EXISTS gt_tparent", 0)
	}
	drop()
	defer drop()
	exec(`CREATE TABLE gt_tparent (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, data BLOB, big INTEGER, f REAL);
CREATE TABLE gt_tchild (id INTEGER PRIMARY KEY, parent INTEGER REFERENCES gt_tparent(id) ON DELETE CASCADE, note TEXT DEFAULT 'a;b');
CREATE INDEX gt_tchild_note ON gt_tchild (note);
CREATE TRIGGER gt_ttrig AFTER INSERT ON gt_tchild WHEN NEW.note = 'fire' BEGIN UPDATE gt_tchild SET note = 'fired'; END;
CREATE VIEW gt_tv AS SELECT name FROM gt_tparent;
INSERT INTO gt_tparent VALUES (1, 'it''s; "q" \ ü -- x', x'00ff10', 9007199254740993, -1.5), (2, '', NULL, NULL, 1e300);
INSERT INTO gt_tchild VALUES (1, 1, 'line1
line2'), (2, 2, NULL);`)
	objs := []Object{{Schema: "main", Name: "gt_tchild", Kind: KindTable}, {Schema: "main", Name: "gt_tparent", Kind: KindTable}, {Schema: "main", Name: "gt_tv", Kind: KindView}}
	state := func() string {
		t.Helper()
		var b strings.Builder
		for _, o := range objs {
			def, err := c.Definition(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&b, def)
			if err := c.StreamRows(ctx, TableRef{Schema: o.Schema, Name: o.Name}, Page{}, nil, func(_, _ []string, row []Value) error { fmt.Fprintf(&b, "%q\n", fmt.Sprint(row)); return nil }); err != nil {
				t.Fatal(err)
			}
		}
		return b.String()
	}
	before := state()
	var dump strings.Builder
	if err := c.Dump(ctx, &dump, DumpOptions{Objects: objs, Structure: true, Data: true}, nil); err != nil {
		t.Fatal(err)
	}
	for _, tx := range []bool{true, false} {
		drop()
		if res, err := c.ImportSQL(ctx, strings.NewReader(dump.String()), ImportOptions{Transaction: tx}, nil); err != nil || len(res.Errors) > 0 {
			t.Fatalf("transaction %v: %v %+v\n%s", tx, err, res, dump.String())
		}
		if after := state(); after != before {
			t.Errorf("transaction %v:\n%s\n%s", tx, before, after)
		}
	}
	// A script that fails halfway leaves nothing, as one transaction.
	if _, err := c.ImportSQL(ctx, strings.NewReader("INSERT INTO gt_tparent (id, name) VALUES (9, 'x');\nINSERT INTO gt_tparent (id, name) VALUES (9, 'y');"), ImportOptions{Transaction: true}, nil); err == nil {
		t.Error("a duplicate key was imported")
	}
	if after := state(); after != before {
		t.Errorf("a failed import left something:\n%s", after)
	}
	n, err := c.ImportCSV(ctx, strings.NewReader("id,name,f\n20,csv 1,1.25\n21,csv 2,\n"), TableRef{Schema: "main", Name: "gt_tparent"},
		CSVImportOptions{Header: true, Columns: []string{"id", "name", "f"}, Mapping: []int{0, 1, 2}, EmptyAsNull: true}, nil)
	if err != nil || n != 2 {
		t.Error(n, err)
	}
	if n, err := c.CountRows(ctx, TableRef{Schema: "main", Name: "gt_tparent"}, Page{}, nil); err != nil || n != 4 {
		t.Error(n, err)
	}
}
