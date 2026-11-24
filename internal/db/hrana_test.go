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
	// Something that isn't libSQL.
	web, _ := hranaFake(t, "", func(string, string, string) (int, string) { return 404, "<html>nothing here</html>" })
	if err := web.Ping(); err == nil || !strings.Contains(err.Error(), "is this a libSQL server") {
		t.Errorf("err %v", err)
	}
}

// On a real server: exact values, an idle stream, a long result, cancelling.
func TestLibSQLSession(t *testing.T) {
	cfg, pw := serverConfig(t, "LIBSQL", model.DriverLibSQL)
	c := openTest(t, cfg, pw)
	ctx := context.Background()
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
	one("WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 60000) INSERT INTO gt_many SELECT i, hex(randomblob(150)) FROM n")
	n := 0
	err := c.StreamRows(ctx, TableRef{Schema: "main", Name: "gt_many"}, Page{}, nil, func(_, _ []string, _ []Value) error { n++; return nil })
	if err != nil || n != 60000 {
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
	if v := one("SELECT count(*) FROM gt_many"); v[0].Text != "60000" {
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
	if v := one("SELECT count(*) FROM gt_many"); v[0].Text != "60000" {
		t.Errorf("count after the lost transaction: %v", v)
	}
	time.Sleep(12 * time.Second) // outside one, nobody notices
	one("SELECT 1")
}
