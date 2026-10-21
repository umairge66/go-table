package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"gotable/internal/db"
	"gotable/internal/model"
)

type harness struct {
	t   *testing.T
	srv *Server
	ts  *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store, err := model.OpenStore(filepath.Join(t.TempDir(), "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}, "assets/app.js": {Data: []byte("js")}}
	s := New(store, model.NewMemorySecrets(), static)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	return &harness{t, s, ts}
}

// do sends an authenticated request and decodes the JSON response into out.
func (h *harness) do(method, path string, body, out any) int {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.ts.URL+path, rd)
	req.Header.Set("X-GoTable-Token", h.srv.Token())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func sqliteFile(t *testing.T) string {
	path := filepath.Join(t.TempDir(), "shop.db")
	cfg := model.NewConnection(model.DriverSQLite)
	cfg.Name, cfg.Path = "x", path
	c, err := db.Open(context.Background(), cfg, db.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Execute(context.Background(), `CREATE TABLE people (id INTEGER PRIMARY KEY, name TEXT NOT NULL, city TEXT);
		INSERT INTO people (name, city) VALUES ('Alice', 'Berlin'), ('Bob', NULL), ('Carol', 'Paris');
		CREATE VIEW berliners AS SELECT name FROM people WHERE city = 'Berlin';`, 0)
	if err != nil || res[len(res)-1].Err != nil {
		t.Fatal(err, res)
	}
	return path
}

func TestGuard(t *testing.T) {
	h := newHarness(t)
	get := func(url string, hdr map[string]string) *http.Response {
		req, _ := http.NewRequest("GET", url, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if r := get(h.ts.URL+"/api/connections", nil); r.StatusCode != 401 {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	if r := get(h.ts.URL+"/", nil); r.StatusCode != 401 {
		t.Fatalf("UI without token: %d", r.StatusCode)
	}
	if r := get(h.ts.URL+"/?token=wrong", nil); r.StatusCode != 403 {
		t.Fatalf("wrong token: %d", r.StatusCode)
	}
	// The start URL token becomes an HttpOnly, SameSite=Strict cookie, and
	// the page moves on to "/" itself (same-site, so the cookie is sent).
	r := get(h.ts.URL+"/?token="+h.srv.Token(), nil)
	if r.StatusCode != 200 || len(r.Cookies()) != 1 || !r.Cookies()[0].HttpOnly || r.Cookies()[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("token exchange: %d %v", r.StatusCode, r.Cookies())
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("token page headers %v", r.Header)
	}
	cookie := tokenCookie + "=" + h.srv.Token()
	if r := get(h.ts.URL+"/", map[string]string{"Cookie": cookie}); r.StatusCode != 200 {
		t.Fatalf("UI with cookie: %d", r.StatusCode)
	}
	// Unknown paths serve the single page app; assets are served as files.
	if r := get(h.ts.URL+"/some/route", map[string]string{"Cookie": cookie}); r.StatusCode != 200 {
		t.Fatalf("spa fallback: %d", r.StatusCode)
	}
	// DNS rebinding: a foreign Host header is rejected even with the token.
	req, _ := http.NewRequest("GET", h.ts.URL+"/api/connections", nil)
	req.Host = "evil.example.com"
	req.Header.Set("X-GoTable-Token", h.srv.Token())
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 403 {
		t.Fatalf("foreign host: %d", res.StatusCode)
	}
	// Cross-site POSTs are rejected.
	req, _ = http.NewRequest("POST", h.ts.URL+"/api/groups", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", "https://evil.example.com")
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", res.StatusCode)
	}
}

func TestConnectionsAPI(t *testing.T) {
	h := newHarness(t)
	pw := "s3cret"
	c := map[string]any{"name": " Shop ", "driver": "postgres", "host": "db", "user": "app", "port": 5432}
	var saved map[string]any
	if code := h.do("PUT", "/api/connections/C1", map[string]any{"connection": c, "secrets": map[string]any{"password": pw}}, &saved); code != 200 {
		t.Fatalf("save: %d %v", code, saved)
	}
	if saved["name"] != "Shop" || saved["saved"].(map[string]any)["password"] != true {
		t.Fatalf("saved %v", saved)
	}
	// The password is never sent back.
	var list struct {
		Connections []map[string]any
		Groups      []map[string]any
	}
	h.do("GET", "/api/connections", nil, &list)
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), pw) || len(list.Connections) != 1 {
		t.Fatalf("list leaked password or wrong length: %s", raw)
	}
	// Saving without a password keeps the stored one; an empty string clears it.
	h.do("PUT", "/api/connections/C1", map[string]any{"connection": c, "secrets": map[string]any{}}, &saved)
	if saved["saved"].(map[string]any)["password"] != true {
		t.Fatal("password should be kept")
	}
	h.do("PUT", "/api/connections/C1", map[string]any{"connection": c, "secrets": map[string]any{"password": ""}}, &saved)
	if saved["saved"].(map[string]any)["password"] != false {
		t.Fatal("password should be cleared")
	}
	// Validation errors come back explained.
	var errBody struct{ Error apiError }
	if code := h.do("PUT", "/api/connections/C2", map[string]any{"connection": map[string]any{"driver": "postgres"}}, &errBody); code != 400 ||
		errBody.Error.Detail != "user is required" {
		t.Fatalf("validation: %d %+v", code, errBody)
	}
	// Groups, duplicate, delete.
	var g model.Group
	h.do("POST", "/api/groups", map[string]string{"name": "Servers"}, &g)
	h.do("PATCH", "/api/groups/"+g.ID, map[string]any{"expanded": false}, &g)
	if g.Expanded || g.Name != "Servers" {
		t.Fatalf("group %+v", g)
	}
	var dup map[string]any
	h.do("POST", "/api/connections/C1/duplicate", nil, &dup)
	if dup["id"] == "C1" || !strings.HasSuffix(dup["name"].(string), "copy") {
		t.Fatalf("dup %v", dup)
	}
	h.do("DELETE", "/api/connections/C1", nil, nil)
	h.do("DELETE", "/api/groups/"+g.ID, nil, nil)
	h.do("GET", "/api/connections", nil, &list)
	if len(list.Connections) != 1 || len(list.Groups) != 0 {
		t.Fatalf("after delete %+v", list)
	}
	// URL parsing.
	var parsed struct {
		Connection model.Connection
		Password   string
	}
	h.do("POST", "/api/parse-url", map[string]string{"url": "mysql://root:pw@h:3307/app"}, &parsed)
	if parsed.Connection.Driver != model.DriverMySQL || parsed.Connection.Port != 3307 || parsed.Password != "pw" {
		t.Fatalf("parse %+v", parsed)
	}
}

func TestSessionAPI(t *testing.T) {
	h := newHarness(t)
	path := sqliteFile(t)
	h.do("PUT", "/api/connections/S1", map[string]any{"connection": map[string]any{"driver": "sqlite", "path": path}}, nil)

	var test struct{ Version string }
	if code := h.do("POST", "/api/test", map[string]any{"connection": map[string]any{"id": "S1", "driver": "sqlite", "path": path, "name": "x"}}, &test); code != 200 ||
		!strings.HasPrefix(test.Version, "SQLite") {
		t.Fatalf("test: %d %+v", code, test)
	}

	var sess struct {
		ID      string
		Schema  string
		Schemas []string
		Version string
	}
	if code := h.do("POST", "/api/sessions", map[string]string{"connectionId": "S1"}, &sess); code != 200 || sess.Schema != "main" {
		t.Fatalf("open: %d %+v", code, sess)
	}
	sid := "/api/sessions/" + sess.ID

	var objs []objectDTO
	h.do("GET", sid+"/objects?schema=main", nil, &objs)
	if len(objs) != 2 || objs[0].Name != "berliners" || objs[0].Kind != "view" {
		t.Fatalf("objects %+v", objs)
	}
	var st struct {
		Columns []columnDTO
		Indexes []indexDTO
	}
	h.do("GET", sid+"/structure?schema=main&table=people", nil, &st)
	if len(st.Columns) != 3 || !st.Columns[0].PrimaryKey || st.Columns[1].Nullable {
		t.Fatalf("structure %+v", st)
	}

	table := map[string]string{"schema": "main", "name": "people"}
	var rows struct {
		Columns []string
		Rows    [][]*string
		Total   int
	}
	h.do("POST", sid+"/rows", map[string]any{"table": table, "columns": []string{"id", "name", "city"},
		"page": map[string]any{"filters": []map[string]string{{"column": "*", "op": "Contains", "value": "ar"}},
			"sort": []map[string]string{{"column": "name", "dir": "desc"}}, "limit": 10}}, &rows)
	if rows.Total != 1 || *rows.Rows[0][1] != "Carol" {
		t.Fatalf("rows %+v", rows)
	}
	h.do("POST", sid+"/rows", map[string]any{"table": table, "page": map[string]any{"limit": 10}}, &rows)
	if rows.Total != 3 || rows.Rows[1][2] != nil {
		t.Fatal("NULL must be JSON null")
	}

	changes := map[string]any{"table": table, "hasPK": true, "dryRun": true, "changes": []map[string]any{
		{"kind": "update", "key": map[string]any{"id": "2"}, "values": map[string]any{"city": "Rome"}},
		{"kind": "insert", "values": map[string]any{"name": "Dave", "city": nil}},
		{"kind": "delete", "key": map[string]any{"id": "1"}},
	}}
	var ch struct {
		Statements []string
		OK         bool
	}
	h.do("POST", sid+"/changes", changes, &ch)
	if len(ch.Statements) != 3 || ch.Statements[0] != `UPDATE "people" SET "city" = 'Rome' WHERE "id" = '2';` {
		t.Fatalf("preview %+v", ch)
	}
	changes["dryRun"] = false
	if code := h.do("POST", sid+"/changes", changes, &ch); code != 200 || !ch.OK {
		t.Fatalf("apply %d %+v", code, ch)
	}

	var ex struct {
		Results []resultDTO
		Schema  string
	}
	h.do("POST", sid+"/execute", map[string]any{"sql": "SELECT name, city FROM people ORDER BY name; UPDATE people SET city = 'X' WHERE id = 99; SELECT * FROM nope", "rowCap": 100}, &ex)
	if len(ex.Results) != 3 {
		t.Fatalf("results %+v", ex.Results)
	}
	if got := len(ex.Results[0].Rows); got != 3 || *ex.Results[0].Rows[0][0] != "Bob" || *ex.Results[0].Rows[0][1] != "Rome" || ex.Results[0].Rows[2][1] != nil {
		t.Fatalf("select %+v", ex.Results[0])
	}
	if ex.Results[1].HasRows || ex.Results[1].RowsAffected != 0 {
		t.Fatalf("update %+v", ex.Results[1])
	}
	if e := ex.Results[2].Error; e == nil || e.Title != "Table not found" {
		t.Fatalf("error %+v", ex.Results[2])
	}

	var comp struct{ Tables map[string][]string }
	h.do("GET", sid+"/completions?schema=main", nil, &comp)
	if strings.Join(comp.Tables["people"], ",") != "id,name,city" {
		t.Fatalf("completions %+v", comp)
	}
	var def struct{ DDL string }
	h.do("GET", sid+"/definition?schema=main&table=people&kind=table", nil, &def)
	if !strings.Contains(def.DDL, "CREATE TABLE people") {
		t.Fatalf("ddl %q", def.DDL)
	}

	// Add a column: preview, then apply; errors are explained.
	col := map[string]any{"name": "email", "type": "text", "nullable": true}
	var ac struct {
		Statements []string
		OK         bool
	}
	h.do("POST", sid+"/columns", map[string]any{"table": table, "column": col, "dryRun": true}, &ac)
	if len(ac.Statements) != 1 || ac.Statements[0] != `ALTER TABLE "people" ADD COLUMN "email" text;` || ac.OK {
		t.Fatalf("preview %+v", ac)
	}
	if code := h.do("POST", sid+"/columns", map[string]any{"table": table, "column": col}, &ac); code != 200 || !ac.OK {
		t.Fatalf("add column %d %+v", code, ac)
	}
	var colErr struct{ Error apiError }
	if code := h.do("POST", sid+"/columns", map[string]any{"table": table, "column": col}, &colErr); code != 400 || colErr.Error.Title != "That name is already taken" {
		t.Fatalf("duplicate column %d %+v", code, colErr)
	}
	if code := h.do("POST", sid+"/columns", map[string]any{"table": table, "column": map[string]any{"name": "x", "type": "int"}, "dryRun": true}, &colErr); code != 400 ||
		colErr.Error.Title != "Please check the column" || !strings.Contains(colErr.Error.Detail, "NOT NULL column if it has a default") {
		t.Fatalf("sqlite rule %d %+v", code, colErr)
	}
	h.do("POST", sid+"/execute", map[string]any{"sql": "ALTER TABLE people DROP COLUMN email"}, nil)

	// Cancel of an unknown query is harmless; closing ends the session.
	var cancelled struct{ Cancelled bool }
	h.do("POST", sid+"/cancel", map[string]string{"queryId": "nope"}, &cancelled)
	h.do("DELETE", sid, nil, nil)
	var gone struct{ Error apiError }
	if code := h.do("GET", sid+"/objects?schema=main", nil, &gone); code != 410 || !strings.Contains(gone.Error.Detail, "closed") {
		t.Fatalf("after close: %d %+v", code, gone)
	}
}

func TestOpenSessionFailureIsExplained(t *testing.T) {
	h := newHarness(t)
	h.do("PUT", "/api/connections/P1", map[string]any{"connection": map[string]any{"driver": "postgres", "host": "127.0.0.1", "port": 1, "user": "x", "ssl_mode": "disable"}}, nil)
	var e struct{ Error apiError }
	if code := h.do("POST", "/api/sessions", map[string]string{"connectionId": "P1"}, &e); code != 400 || e.Error.Title != "Can't reach the server" || e.Error.Hint == "" {
		t.Fatalf("%d %+v", code, e)
	}
}

func TestExplain(t *testing.T) {
	for msg, want := range map[string]string{
		"dial tcp 127.0.0.1:5432: connect: connection refused":                "Can't reach the server",
		`FATAL: password authentication failed for user "x" (SQLSTATE 28P01)`: "Wrong user name or password",
		"ssh: handshake failed: ssh: unable to authenticate":                  "SSH login failed",
		"Error 1049 (42000): Unknown database 'shopp'":                        "Database not found",
		`ERROR: relation "nope" does not exist (SQLSTATE 42P01)`:              "Table not found",
		"ERROR: duplicate key value violates unique constraint":               "Duplicate value",
		`ERROR: invalid input syntax for type integer: "abc"`:                 "Value doesn't fit the column type",
		"something odd": "Something went wrong",
	} {
		if got := Explain(errors.New(msg)).Title; got != want {
			t.Errorf("%q: %q, want %q", msg, got, want)
		}
	}
	if Explain(context.Canceled).Title != "Query stopped" {
		t.Error("cancel")
	}
}

// waitJob polls a job until it is no longer running.
func (h *harness) waitJob(id string) *Job {
	h.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var j Job
		h.do("GET", "/api/jobs/"+id, nil, &j)
		if j.Status != "running" {
			return &j
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatal("job did not finish")
	return nil
}

func (h *harness) upload(name, content string) string {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.ts.URL+"/api/uploads", strings.NewReader(content))
	req.Header.Set("X-GoTable-Token", h.srv.Token())
	req.Header.Set("X-File-Name", name)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	var up struct {
		ID   string
		Size int64
	}
	json.NewDecoder(res.Body).Decode(&up)
	if up.ID == "" || up.Size != int64(len(content)) {
		h.t.Fatalf("upload %+v", up)
	}
	return up.ID
}

func (h *harness) download(id string) (string, string) {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.ts.URL+"/api/jobs/"+id+"/download", nil)
	req.Header.Set("X-GoTable-Token", h.srv.Token())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.Header.Get("Content-Disposition"), string(b)
}

func TestJobs(t *testing.T) {
	h := newHarness(t)
	path := sqliteFile(t)
	h.do("PUT", "/api/connections/S1", map[string]any{"connection": map[string]any{"driver": "sqlite", "path": path}}, nil)
	var sess struct{ ID string }
	h.do("POST", "/api/sessions", map[string]string{"connectionId": "S1"}, &sess)
	sid := "/api/sessions/" + sess.ID
	table := map[string]string{"schema": "main", "name": "people"}

	// Import an uploaded SQL script.
	script := "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT);\n"
	for i := 0; i < 500; i++ {
		script += fmt.Sprintf("INSERT INTO notes (body) VALUES ('note %d; with semicolon');\n", i)
	}
	up := h.upload("notes.sql", script)
	var j Job
	h.do("POST", sid+"/jobs", map[string]any{"kind": "import-sql", "source": map[string]string{"uploadId": up}, "transaction": true}, &j)
	done := h.waitJob(j.ID)
	if done.Status != "done" || done.Statements != 501 || done.Rows != 500 || done.Done != int64(len(script)) || done.Total != int64(len(script)) || done.Unit != "bytes" {
		t.Fatalf("import job %+v", done)
	}
	// The import used its own connection; the UI session sees the result.
	var rows struct{ Total int }
	h.do("POST", sid+"/rows", map[string]any{"table": map[string]string{"schema": "main", "name": "notes"}, "page": map[string]any{"limit": 1}}, &rows)
	if rows.Total != 500 {
		t.Fatalf("notes %d", rows.Total)
	}

	// A failing import reports the line and the error, and rolls back.
	up = h.upload("bad.sql", "INSERT INTO notes (body) VALUES ('ok');\nINSERT INTO nope VALUES (1);\n")
	h.do("POST", sid+"/jobs", map[string]any{"kind": "import-sql", "source": map[string]string{"uploadId": up}, "transaction": true}, &j)
	done = h.waitJob(j.ID)
	if done.Status != "failed" || done.Error == nil || done.Error.Title != "Table not found" || len(done.Errors) != 1 || done.Errors[0].Line != 2 {
		t.Fatalf("failed job %+v", done)
	}
	h.do("POST", sid+"/rows", map[string]any{"table": map[string]string{"schema": "main", "name": "notes"}, "page": map[string]any{"limit": 1}}, &rows)
	if rows.Total != 500 {
		t.Fatalf("rollback: %d", rows.Total)
	}

	// Import from a local path (no upload).
	local := filepath.Join(t.TempDir(), "more.sql")
	os.WriteFile(local, []byte("INSERT INTO notes (body) VALUES ('from path');"), 0o600)
	h.do("POST", sid+"/jobs", map[string]any{"kind": "import-sql", "source": map[string]string{"path": local}}, &j)
	if done = h.waitJob(j.ID); done.Status != "done" || done.Rows != 1 {
		t.Fatalf("path import %+v", done)
	}

	// CSV: preview detects the delimiter, then import with a column mapping.
	csvData := "Name;Town\nDora;Oslo\nEd;\n"
	up = h.upload("people.csv", csvData)
	var prev struct {
		Delimiter string
		Rows      [][]string
	}
	h.do("POST", "/api/csv-preview", map[string]string{"uploadId": up}, &prev)
	if prev.Delimiter != ";" || len(prev.Rows) != 3 || prev.Rows[0][1] != "Town" {
		t.Fatalf("preview %+v", prev)
	}
	h.do("POST", sid+"/jobs", map[string]any{"kind": "import-csv", "source": map[string]string{"uploadId": up}, "table": table,
		"delimiter": ";", "header": true, "emptyAsNull": true,
		"mapping": []map[string]any{{"column": "name", "field": 0}, {"column": "city", "field": 1}}}, &j)
	if done = h.waitJob(j.ID); done.Status != "done" || done.Rows != 2 {
		t.Fatalf("csv job %+v", done)
	}

	// Export a table to CSV and download it.
	h.do("POST", sid+"/jobs", map[string]any{"kind": "export-table", "format": "csv", "table": table, "allRows": true,
		"columns": []string{"id", "name", "city"}, "page": map[string]any{"sort": []map[string]string{{"column": "id"}}, "limit": 1},
		"csv": map[string]any{"header": true, "delimiter": ","}}, &j)
	done = h.waitJob(j.ID)
	if done.Status != "done" || done.Rows != 5 || done.Total != 5 || done.File != "people.csv" || done.Size == 0 {
		t.Fatalf("export job %+v", done)
	}
	disp, body := h.download(j.ID)
	if disp != `attachment; filename="people.csv"` || body != "id,name,city\n1,Alice,Berlin\n2,Bob,NULL\n3,Carol,Paris\n4,Dora,Oslo\n5,Ed,NULL\n" {
		t.Fatalf("download %q %q", disp, body)
	}

	// Export the whole database as SQL, then load it into a new database.
	objs := []map[string]string{{"schema": "main", "name": "notes", "kind": "table"}, {"schema": "main", "name": "people", "kind": "table"},
		{"schema": "main", "name": "berliners", "kind": "view"}}
	h.do("POST", sid+"/jobs", map[string]any{"kind": "export-database", "format": "sql", "objects": objs, "structure": true, "data": true}, &j)
	done = h.waitJob(j.ID)
	if done.Status != "done" || done.Rows != 506 || !strings.HasSuffix(done.File, ".sql") {
		t.Fatalf("db export %+v", done)
	}
	_, dump := h.download(j.ID)
	path2 := filepath.Join(t.TempDir(), "copy.db")
	h.do("PUT", "/api/connections/S2", map[string]any{"connection": map[string]any{"driver": "sqlite", "path": path2}}, nil)
	var sess2 struct{ ID string }
	h.do("POST", "/api/sessions", map[string]string{"connectionId": "S2"}, &sess2)
	h.do("POST", "/api/sessions/"+sess2.ID+"/jobs", map[string]any{"kind": "import-sql", "source": map[string]string{"uploadId": h.upload("dump.sql", dump)}}, &j)
	if done = h.waitJob(j.ID); done.Status != "done" {
		t.Fatalf("re-import %+v", done)
	}
	var ex struct{ Results []resultDTO }
	h.do("POST", "/api/sessions/"+sess2.ID+"/execute", map[string]any{"sql": "SELECT (SELECT COUNT(*) FROM notes), (SELECT COUNT(*) FROM people), (SELECT COUNT(*) FROM berliners)"}, &ex)
	if r := ex.Results[0].Rows[0]; *r[0] != "501" || *r[1] != "5" || *r[2] != "1" {
		t.Fatalf("copied counts %v %v %v", *r[0], *r[1], *r[2])
	}

	// CSV ZIP export of the database.
	h.do("POST", sid+"/jobs", map[string]any{"kind": "export-database", "format": "csv-zip", "objects": objs[:2], "csv": map[string]any{"header": true}}, &j)
	done = h.waitJob(j.ID)
	_, zipBody := h.download(j.ID)
	zr, err := zip.NewReader(bytes.NewReader([]byte(zipBody)), int64(len(zipBody)))
	if done.Status != "done" || err != nil || len(zr.File) != 2 || zr.File[1].Name != "people.csv" {
		t.Fatalf("zip %+v %v", done, err)
	}

	// Finished jobs can be discarded; their files are removed.
	h.do("DELETE", "/api/jobs/"+j.ID, nil, nil)
	if code := h.do("GET", "/api/jobs/"+j.ID, nil, nil); code != 404 {
		t.Fatalf("discarded job: %d", code)
	}
	// Validation errors are returned before a job starts.
	var e struct{ Error apiError }
	if code := h.do("POST", sid+"/jobs", map[string]any{"kind": "import-csv", "table": table, "source": map[string]string{"uploadId": up}}, &e); code != 400 {
		t.Fatalf("mapping required: %d %+v", code, e)
	}
	if code := h.do("POST", sid+"/jobs", map[string]any{"kind": "import-sql", "source": map[string]string{"path": "/nonexistent.sql"}}, &e); code != 400 {
		t.Fatalf("missing file: %d", code)
	}
}

func TestJobCancel(t *testing.T) {
	h := newHarness(t)
	path := sqliteFile(t)
	h.do("PUT", "/api/connections/S1", map[string]any{"connection": map[string]any{"driver": "sqlite", "path": path}}, nil)
	var sess struct{ ID string }
	h.do("POST", "/api/sessions", map[string]string{"connectionId": "S1"}, &sess)
	// A slow statement: a recursive CTE counting to 50 million.
	up := h.upload("slow.sql", "CREATE TABLE t AS WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 50000000) SELECT i FROM n;")
	var j Job
	h.do("POST", "/api/sessions/"+sess.ID+"/jobs", map[string]any{"kind": "import-sql", "source": map[string]string{"uploadId": up}}, &j)
	time.Sleep(300 * time.Millisecond)
	h.do("DELETE", "/api/jobs/"+j.ID, nil, nil)
	if done := h.waitJob(j.ID); done.Status != "cancelled" {
		t.Fatalf("cancel: %+v", done)
	}
}

func TestDropAPI(t *testing.T) {
	h := newHarness(t)
	path := sqliteFile(t)
	h.do("PUT", "/api/connections/S1", map[string]any{"connection": map[string]any{"driver": "sqlite", "path": path}}, nil)
	var sess struct{ ID string }
	h.do("POST", "/api/sessions", map[string]string{"connectionId": "S1"}, &sess)
	sid := "/api/sessions/" + sess.ID
	h.do("POST", sid+"/execute", map[string]any{"sql": "CREATE TABLE pets (id INTEGER PRIMARY KEY, owner INT REFERENCES people(id)); INSERT INTO pets (owner) VALUES (1)"}, nil)
	people := map[string]string{"schema": "main", "name": "people", "kind": "table"}

	var res struct {
		Statements []string
		OK         bool
	}
	h.do("POST", sid+"/drop", map[string]any{"objects": []any{people}, "dryRun": true}, &res)
	if len(res.Statements) != 1 || res.Statements[0] != `DROP TABLE "people";` || res.OK {
		t.Fatalf("preview %+v", res)
	}
	var e struct{ Error apiError }
	if code := h.do("POST", sid+"/drop", map[string]any{"objects": []any{people}}, &e); code != 400 || e.Error.Title != "Other tables or views depend on it" {
		t.Fatalf("dependency error %d %+v", code, e)
	}
	if code := h.do("POST", sid+"/drop", map[string]any{"objects": []any{people, map[string]string{"schema": "main", "name": "berliners", "kind": "view"}}, "cascade": true}, &res); code != 200 || !res.OK {
		t.Fatalf("cascade drop %d %+v", code, res)
	}
	var objs []objectDTO
	h.do("GET", sid+"/objects?schema=main", nil, &objs)
	if len(objs) != 1 || objs[0].Name != "pets" {
		t.Fatalf("objects after drop %+v", objs)
	}
}

func TestSchemasAndDatabases(t *testing.T) {
	h := newHarness(t)
	path := sqliteFile(t)
	h.do("PUT", "/api/connections/S1", map[string]any{"connection": map[string]any{"driver": "sqlite", "path": path}}, nil)
	var sess struct{ ID, Database string }
	h.do("POST", "/api/sessions", map[string]string{"connectionId": "S1"}, &sess)
	sid := "/api/sessions/" + sess.ID
	if sess.Database != "shop.db" {
		t.Fatalf("database %q", sess.Database)
	}
	aux := filepath.Join(t.TempDir(), "aux.db")
	h.do("POST", sid+"/execute", map[string]any{"sql": "ATTACH DATABASE '" + aux + "' AS aux; CREATE TABLE aux.extra (x INT)"}, nil)
	var sc struct {
		Schema  string
		Schemas []string
	}
	h.do("GET", sid+"/schemas", nil, &sc)
	if sc.Schema != "main" || strings.Join(sc.Schemas, ",") != "main,aux" {
		t.Fatalf("schemas %+v", sc)
	}
	var e struct{ Error apiError }
	if code := h.do("POST", sid+"/fork", map[string]string{"database": "other"}, &e); code != 400 || !strings.Contains(e.Error.Detail, "one file") {
		t.Fatalf("sqlite switch %d %+v", code, e)
	}
}

// TestForkPostgres opens a second database of the same server side by side.
func TestForkPostgres(t *testing.T) {
	host := os.Getenv("GOTABLE_TEST_PG_HOST")
	if host == "" {
		t.Skip("GOTABLE_TEST_PG_HOST not set")
	}
	h := newHarness(t)
	port, _ := strconv.Atoi(os.Getenv("GOTABLE_TEST_PG_PORT"))
	h.do("PUT", "/api/connections/P1", map[string]any{"connection": map[string]any{"driver": "postgres", "host": host, "port": port,
		"user": os.Getenv("GOTABLE_TEST_PG_USER"), "database": "postgres", "ssl_mode": "disable"},
		"secrets": map[string]string{"password": os.Getenv("GOTABLE_TEST_PG_PASSWORD")}}, nil)
	var sess struct{ ID, Database string }
	h.do("POST", "/api/sessions", map[string]string{"connectionId": "P1"}, &sess)
	sid := "/api/sessions/" + sess.ID
	h.do("POST", sid+"/execute", map[string]any{"sql": "DROP DATABASE IF EXISTS gt_other"}, nil)
	h.do("POST", sid+"/execute", map[string]any{"sql": "CREATE DATABASE gt_other"}, nil)
	defer h.do("POST", sid+"/execute", map[string]any{"sql": "DROP DATABASE IF EXISTS gt_other WITH (FORCE)"}, nil)

	var dbs struct {
		Database  string
		Databases []string
	}
	h.do("GET", sid+"/databases", nil, &dbs)
	if dbs.Database != "postgres" || !slices.Contains(dbs.Databases, "gt_other") {
		t.Fatalf("databases %+v", dbs)
	}
	var fork struct {
		ID, Database, Schema string
		Connection           map[string]any
	}
	if code := h.do("POST", sid+"/fork", map[string]string{"database": "gt_other"}, &fork); code != 200 || fork.Database != "gt_other" || fork.Schema != "public" || fork.Connection["id"] != "P1" {
		t.Fatalf("fork %d %+v", code, fork)
	}
	// Both sessions work at the same time, each in its own database.
	cur := func(id string) string {
		var ex struct{ Results []resultDTO }
		h.do("POST", "/api/sessions/"+id+"/execute", map[string]any{"sql": "SELECT current_database()"}, &ex)
		return *ex.Results[0].Rows[0][0]
	}
	if a, b := cur(sess.ID), cur(fork.ID); a != "postgres" || b != "gt_other" {
		t.Fatalf("current databases %s %s", a, b)
	}
	var e struct{ Error apiError }
	if code := h.do("POST", sid+"/fork", map[string]string{"database": "does_not_exist"}, &e); code != 400 || e.Error.Title != "Database not found" {
		t.Fatalf("bad fork %d %+v", code, e)
	}
	h.do("DELETE", "/api/sessions/"+fork.ID, nil, nil)
}

func TestExecuteInSchema(t *testing.T) {
	h := newHarness(t)
	path := sqliteFile(t)
	h.do("PUT", "/api/connections/S1", map[string]any{"connection": map[string]any{"driver": "sqlite", "path": path}}, nil)
	var sess struct{ ID string }
	h.do("POST", "/api/sessions", map[string]string{"connectionId": "S1"}, &sess)
	sid := "/api/sessions/" + sess.ID
	aux := filepath.Join(t.TempDir(), "aux.db")
	h.do("POST", sid+"/execute", map[string]any{"sql": "ATTACH DATABASE '" + aux + "' AS aux"}, nil)
	var ex struct {
		Results []resultDTO
		Schema  string
	}
	h.do("POST", sid+"/execute", map[string]any{"sql": "SELECT 1", "schema": "aux"}, &ex)
	if ex.Schema != "aux" || ex.Results[0].Error != nil {
		t.Fatalf("execute in aux %+v", ex)
	}
	var e struct{ Error apiError }
	if code := h.do("POST", sid+"/execute", map[string]any{"sql": "SELECT 1", "schema": "nope"}, &e); code != 400 {
		t.Fatalf("unknown schema: %d", code)
	}
}

func TestQueriesAndHistory(t *testing.T) {
	h := newHarness(t)
	path := sqliteFile(t)
	h.do("PUT", "/api/connections/S1", map[string]any{"connection": map[string]any{"driver": "sqlite", "path": path}}, nil)
	var sess struct{ ID string }
	h.do("POST", "/api/sessions", map[string]string{"connectionId": "S1"}, &sess)
	sid := "/api/sessions/" + sess.ID

	h.do("POST", sid+"/execute", map[string]any{"sql": "SELECT name FROM people; UPDATE people SET city = 'X' WHERE id = 1"}, nil)
	h.do("POST", sid+"/execute", map[string]any{"sql": "SELECT * FROM nope"}, nil)
	h.do("POST", sid+"/changes", map[string]any{"table": map[string]string{"schema": "main", "name": "people"}, "hasPK": true,
		"changes": []map[string]any{{"kind": "update", "key": map[string]any{"id": "2"}, "values": map[string]any{"city": "Rome"}}}}, nil)

	var hist []model.HistoryEntry
	h.do("GET", "/api/history?connection=S1", nil, &hist)
	if len(hist) != 4 {
		t.Fatalf("history %d: %+v", len(hist), hist)
	}
	if hist[0].Kind != "commit" || !strings.Contains(hist[0].SQL, `UPDATE "people" SET "city" = 'Rome'`) || hist[0].Rows != 1 {
		t.Errorf("commit entry %+v", hist[0])
	}
	if hist[1].SQL != "SELECT * FROM nope" || !strings.Contains(hist[1].Error, "no such table") {
		t.Errorf("error entry %+v", hist[1])
	}
	if hist[2].Rows != 1 || hist[3].Rows != 3 || hist[3].Database != "shop.db" || hist[3].Schema != "main" {
		t.Errorf("query entries %+v %+v", hist[2], hist[3])
	}
	h.do("GET", "/api/history?connection=S1&q=update", nil, &hist)
	if len(hist) != 2 {
		t.Errorf("search %d", len(hist))
	}
	h.do("DELETE", "/api/history?connection=S1", nil, nil)
	h.do("GET", "/api/history?connection=S1", nil, &hist)
	if len(hist) != 0 {
		t.Errorf("after clear %d", len(hist))
	}

	var q model.SavedQuery
	if code := h.do("PUT", "/api/queries/Q1", map[string]any{"connectionId": "S1", "database": "shop.db", "schema": "main", "name": "Berliners", "sql": "SELECT 1"}, &q); code != 200 || q.ID != "Q1" {
		t.Fatalf("save %d %+v", code, q)
	}
	h.do("PUT", "/api/queries/Q1", map[string]any{"connectionId": "S1", "name": "Berliners v2", "sql": "SELECT 2"}, &q)
	var list []model.SavedQuery
	h.do("GET", "/api/queries?connection=S1", nil, &list)
	if len(list) != 1 || list[0].Name != "Berliners v2" || list[0].SQL != "SELECT 2" {
		t.Fatalf("list %+v", list)
	}
	var e struct{ Error apiError }
	if code := h.do("PUT", "/api/queries/Q2", map[string]any{"connectionId": "S1", "name": " "}, &e); code != 400 {
		t.Fatalf("empty name %d", code)
	}
	h.do("DELETE", "/api/queries/Q1", nil, nil)
	h.do("GET", "/api/queries?connection=S1", nil, &list)
	if len(list) != 0 {
		t.Fatalf("after delete %+v", list)
	}
}

type fakeDialogs struct{ save, open string }

func (f fakeDialogs) SaveFile(string, string, []FileFilter) (string, error) { return f.save, nil }
func (f fakeDialogs) OpenFile(string, string, []FileFilter) (string, error) { return f.open, nil }

func TestNativeDialogs(t *testing.T) {
	h := newHarness(t)
	var info struct{ Dialogs bool }
	h.do("GET", "/api/native", nil, &info)
	if info.Dialogs {
		t.Fatal("a plain server has no dialogs")
	}
	if code := h.do("POST", "/api/dialog/save", map[string]string{"name": "x.sql", "kind": "sql"}, nil); code != 404 {
		t.Fatalf("save without dialogs: %d", code)
	}

	dir := t.TempDir()
	h.srv.SetDialogs(fakeDialogs{save: filepath.Join(dir, "people")})
	h.do("GET", "/api/native", nil, &info)
	if !info.Dialogs {
		t.Fatal("dialogs not reported")
	}

	// Export a table, then save it where the dialog said (extension added).
	path := sqliteFile(t)
	h.do("PUT", "/api/connections/S1", map[string]any{"connection": map[string]any{"driver": "sqlite", "path": path}}, nil)
	var sess struct{ ID string }
	h.do("POST", "/api/sessions", map[string]string{"connectionId": "S1"}, &sess)
	var j Job
	h.do("POST", "/api/sessions/"+sess.ID+"/jobs", map[string]any{"kind": "export-table", "format": "csv", "allRows": true,
		"table": map[string]string{"schema": "main", "name": "people"}, "columns": []string{"id", "name"},
		"page": map[string]any{"sort": []map[string]string{{"column": "id"}}}, "csv": map[string]any{"header": true, "delimiter": ","}}, &j)
	if done := h.waitJob(j.ID); done.Status != "done" {
		t.Fatalf("export %+v", done)
	}
	var target struct{ Handle, Name string }
	h.do("POST", "/api/dialog/save", map[string]string{"name": "people.csv", "kind": "csv"}, &target)
	if target.Handle == "" || target.Name != "people.csv" {
		t.Fatalf("save dialog %+v", target)
	}
	if code := h.do("POST", "/api/jobs/"+j.ID+"/save", map[string]string{"handle": target.Handle}, nil); code != 200 {
		t.Fatalf("save job %d", code)
	}
	data, err := os.ReadFile(filepath.Join(dir, "people.csv"))
	if err != nil || !strings.HasPrefix(string(data), "id,name,city\n1,Alice,Berlin\n") {
		t.Fatalf("saved export %q %v", data, err)
	}
	// A handle works once; made-up handles never write anything.
	if code := h.do("POST", "/api/jobs/"+j.ID+"/save", map[string]string{"handle": target.Handle}, nil); code != 400 {
		t.Fatalf("reused handle %d", code)
	}

	// Text made in the page.
	h.srv.SetDialogs(fakeDialogs{save: filepath.Join(dir, "result.json")})
	h.do("POST", "/api/dialog/save", map[string]string{"name": "result.json", "kind": "json"}, &target)
	req, _ := http.NewRequest("POST", h.ts.URL+"/api/files?handle="+target.Handle, strings.NewReader(`[{"a":1}]`))
	req.Header.Set("X-GoTable-Token", h.srv.Token())
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("save text %v %v", res, err)
	}
	res.Body.Close()
	if data, _ := os.ReadFile(filepath.Join(dir, "result.json")); string(data) != `[{"a":1}]` {
		t.Fatalf("saved text %q", data)
	}
	if st, _ := os.Stat(filepath.Join(dir, "result.json")); st.Mode().Perm() != 0o644 {
		t.Fatalf("saved file mode %v", st.Mode())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".*.part")); len(leftovers) > 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}

	// Cancelling, and opening a file to import.
	h.srv.SetDialogs(fakeDialogs{open: path})
	var cancelled struct{ Cancelled bool }
	h.do("POST", "/api/dialog/save", map[string]string{"name": "x.sql", "kind": "sql"}, &cancelled)
	if !cancelled.Cancelled {
		t.Fatal("empty path should report cancelled")
	}
	var opened struct {
		Path, Name string
		Size       int64
	}
	h.do("POST", "/api/dialog/open", map[string]any{"kinds": []string{"sql"}}, &opened)
	if opened.Path != path || opened.Name != "shop.db" || opened.Size == 0 {
		t.Fatalf("open dialog %+v", opened)
	}
}

func TestSSHKeyDialogAndCheck(t *testing.T) {
	h := newHarness(t)
	key := filepath.Join(t.TempDir(), "notakey")
	os.WriteFile(key, []byte("hello"), 0o600)
	h.srv.SetDialogs(fakeDialogs{open: key})
	var opened struct{ Path string }
	h.do("POST", "/api/dialog/open", map[string]any{"kinds": []string{"sshkey"}}, &opened)
	if opened.Path != key {
		t.Fatalf("open dialog %+v", opened)
	}
	var check struct {
		OK, Encrypted bool
		Problem       string
	}
	h.do("POST", "/api/ssh-key/check", map[string]string{"path": key}, &check)
	if check.OK || !strings.Contains(check.Problem, "not an SSH private key") {
		t.Fatalf("check %+v", check)
	}
}

func TestCleanWorkDirs(t *testing.T) {
	base := t.TempDir()
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(base, name)
		os.Mkdir(p, 0o700)
		old := time.Now().Add(-age)
		os.Chtimes(p, old, old)
		return p
	}
	gone := mk("work-999999999-abc", 0)                    // process doesn't exist
	mine := mk(fmt.Sprintf("work-%d-abc", os.Getpid()), 0) // still running
	legacyNew := mk("work-12345", time.Hour)               // old naming, recent
	legacyOld := mk("work-67890", 8*24*time.Hour)          // old naming, stale
	other := mk("WebKitCache", 30*24*time.Hour)            // not ours to touch
	cleanWorkDirs(base)
	for p, want := range map[string]bool{gone: false, mine: true, legacyNew: true, legacyOld: false, other: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}

func TestCreateTableDatabaseAndSQLiteFile(t *testing.T) {
	h := newHarness(t)
	path := sqliteFile(t)
	h.do("PUT", "/api/connections/S1", map[string]any{"connection": map[string]any{"driver": "sqlite", "path": path}}, nil)
	var sess struct{ ID string }
	h.do("POST", "/api/sessions", map[string]string{"connectionId": "S1"}, &sess)
	sid := "/api/sessions/" + sess.ID
	table := map[string]any{"name": "tasks", "columns": []map[string]any{
		{"name": "id", "type": "INTEGER", "primaryKey": true, "autoIncrement": true},
		{"name": "title", "type": "text", "nullable": false},
	}}
	var out struct{ Statements []string }
	if code := h.do("POST", sid+"/tables", map[string]any{"table": table, "dryRun": true}, &out); code != 200 || !strings.HasPrefix(out.Statements[0], `CREATE TABLE "tasks"`) {
		t.Fatalf("preview %d %v", code, out)
	}
	var objs []struct{ Name string }
	h.do("GET", sid+"/objects", nil, &objs)
	for _, o := range objs {
		if o.Name == "tasks" {
			t.Fatal("a preview must not create the table")
		}
	}
	if code := h.do("POST", sid+"/tables", map[string]any{"table": table}, &out); code != 200 {
		t.Fatalf("create %d", code)
	}
	h.do("GET", sid+"/objects", nil, &objs)
	found := false
	for _, o := range objs {
		found = found || o.Name == "tasks"
	}
	if !found {
		t.Fatal("table not created")
	}
	var e struct{ Error apiError }
	if code := h.do("POST", sid+"/tables", map[string]any{"table": table}, &e); code != 400 || !strings.Contains(e.Error.Detail+e.Error.Title, "exists") {
		t.Fatalf("duplicate: %d %+v", code, e)
	}
	if code := h.do("POST", sid+"/databases", map[string]any{"database": map[string]string{"name": "x"}}, nil); code != 400 {
		t.Fatalf("SQLite CREATE DATABASE should be refused: %d", code)
	}

	// A new SQLite file becomes a saved connection; existing files are never replaced.
	dir := t.TempDir()
	var c struct{ ID, Name, Path string }
	if code := h.do("POST", "/api/sqlite-files", map[string]any{"path": filepath.Join(dir, "sub", "notes.sqlite")}, &c); code != 200 || c.Name != "notes" || c.ID == "" {
		t.Fatalf("new file %d %+v", code, c)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "notes.sqlite")); err != nil {
		t.Fatal(err)
	}
	if code := h.do("POST", "/api/sqlite-files", map[string]any{"path": filepath.Join(dir, "sub", "notes.sqlite")}, &e); code != 400 {
		t.Fatalf("existing file must not be replaced: %d", code)
	}
	if code := h.do("POST", "/api/sqlite-files", map[string]any{"path": "relative.db"}, nil); code != 400 {
		t.Fatalf("relative path: %d", code)
	}
	h.srv.SetDialogs(fakeDialogs{save: filepath.Join(dir, "picked.sqlite")})
	var target struct{ Handle string }
	h.do("POST", "/api/dialog/save", map[string]string{"name": "picked.sqlite", "kind": "sqlite"}, &target)
	if code := h.do("POST", "/api/sqlite-files", map[string]any{"handle": target.Handle, "name": "Picked"}, &c); code != 200 || c.Name != "Picked" {
		t.Fatalf("via dialog %d %+v", code, c)
	}
}

func TestNativeTheme(t *testing.T) {
	h := newHarness(t)
	// Without a window the call is accepted and does nothing.
	if code := h.do("POST", "/api/native/theme", map[string]bool{"dark": true}, nil); code != 200 {
		t.Fatalf("no hook: %d", code)
	}
	var got []bool
	h.srv.SetThemeHook(func(dark bool) { got = append(got, dark) })
	h.do("POST", "/api/native/theme", map[string]bool{"dark": false}, nil)
	h.do("POST", "/api/native/theme", map[string]bool{"dark": true}, nil)
	if len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("hook calls %v", got)
	}
}

func TestNativeWindow(t *testing.T) {
	h := newHarness(t)
	var info struct{ Window bool }
	h.do("GET", "/api/native", nil, &info)
	if info.Window {
		t.Fatal("no window hook yet")
	}
	if code := h.do("POST", "/api/native/window", map[string]string{"action": "drag"}, nil); code != 404 {
		t.Fatalf("without a frameless window: %d", code)
	}
	var got []string
	h.srv.SetWindowHook(func(a string) error {
		if a == "bad" {
			return errors.New("unknown")
		}
		got = append(got, a)
		return nil
	})
	h.do("GET", "/api/native", nil, &info)
	if !info.Window {
		t.Fatal("window not reported")
	}
	for _, a := range []string{"drag", "resize-se", "toggle-maximize"} {
		if code := h.do("POST", "/api/native/window", map[string]string{"action": a}, nil); code != 200 {
			t.Fatalf("%s: %d", a, code)
		}
	}
	if code := h.do("POST", "/api/native/window", map[string]string{"action": "bad"}, nil); code != 400 {
		t.Fatalf("bad action: %d", code)
	}
	if strings.Join(got, ",") != "drag,resize-se,toggle-maximize" {
		t.Fatalf("actions %v", got)
	}
}

func TestUnbuiltUI(t *testing.T) {
	store, err := model.OpenStore(filepath.Join(t.TempDir(), "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(store, model.NewMemorySecrets(), fstest.MapFS{".gitkeep": {Data: nil}})
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	req.Header.Set("X-GoTable-Token", srv.Token())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 503 || !strings.Contains(string(body), "make build") {
		t.Fatalf("unbuilt UI: %d %q", res.StatusCode, body)
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	for in, want := range map[string]string{
		"~/dumps/a.sql": filepath.Join(home, "dumps", "a.sql"),
		`~\dumps\a.sql`: filepath.Join(home, `dumps\a.sql`),
		"/abs/a.sql":    "/abs/a.sql",
		`C:\data\a.sql`: `C:\data\a.sql`,
		"~other/a.sql":  "~other/a.sql",
	} {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}
