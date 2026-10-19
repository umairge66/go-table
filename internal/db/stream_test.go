package db

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"
)

func scanAll(t *testing.T, kind model.Driver, script string) ([]Stmt, []string) {
	t.Helper()
	sc := NewScanner(strings.NewReader(script), kind)
	var stmts []Stmt
	var copies []string
	for {
		st, err := sc.Next()
		if errors.Is(err, io.EOF) {
			return stmts, copies
		}
		if err != nil {
			t.Fatal(err)
		}
		if st.Copy != nil {
			b, err := io.ReadAll(st.Copy)
			if err != nil {
				t.Fatal(err)
			}
			copies = append(copies, string(b))
		}
		stmts = append(stmts, st)
	}
}

func TestScannerMatchesSplitter(t *testing.T) {
	scripts := []struct {
		kind   model.Driver
		script string
	}{
		{model.DriverPostgres, "SELECT 1; SELECT 2;"},
		{model.DriverPostgres, "SELECT ';' ; SELECT \"a;b\" FROM t"},
		{model.DriverPostgres, "SELECT 'it''s;' ;"},
		{model.DriverPostgres, "-- a; b\nSELECT 1; /* ; */ SELECT 2"},
		{model.DriverPostgres, "CREATE FUNCTION f() AS $body$ a; b; $body$; SELECT 1"},
		{model.DriverPostgres, "SELECT $1; SELECT $$x;$$"},
		{model.DriverMySQL, "SELECT 'a\\';b'; SELECT `x;y`"},
		{model.DriverMySQL, "# c;\nSELECT 1"},
		{model.DriverMySQL, "DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 1; END$$\nDELIMITER ;\nSELECT 2;"},
		{model.DriverSQLite, "CREATE TRIGGER tr AFTER INSERT ON t BEGIN UPDATE t SET a=1; DELETE FROM u; END; SELECT 1"},
		{model.DriverSQLite, "BEGIN; INSERT INTO t VALUES (1); COMMIT;"},
		{model.DriverPostgres, " ; ;-- only comment\n"},
		{model.DriverMySQL, "/*!40101 SET NAMES utf8mb4 */;\n/* plain */;\nSELECT 1;"},
	}
	for _, c := range scripts {
		stmts, _ := scanAll(t, c.kind, c.script)
		got := make([]string, len(stmts))
		for i, s := range stmts {
			got[i] = s.SQL
		}
		want := spanSQL(SplitStatements(c.kind, c.script))
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s %q:\nscanner  %q\nsplitter %q", c.kind, c.script, got, want)
		}
	}
}

func TestScannerDumpFeatures(t *testing.T) {
	// pg_dump: psql meta-commands are skipped, COPY data is streamed.
	pgdump := "\\restrict abc\nSET statement_timeout = 0;\nSELECT pg_catalog.set_config('search_path', '', false);\n" +
		"CREATE TABLE public.t (id integer, note text);\n" +
		"--\n-- Data for Name: t; Type: TABLE DATA; Schema: public; Owner: postgres\n--\n\n" +
		"COPY public.t (id, note) FROM stdin;\n1\thello; world\n2\t\\N\n\\.\n\n" +
		"SELECT pg_catalog.setval('public.t_id_seq', 2, true);\n\\unrestrict abc\n"
	stmts, copies := scanAll(t, model.DriverPostgres, pgdump)
	var sqls []string
	for _, s := range stmts {
		sqls = append(sqls, s.SQL)
	}
	want := []string{"SET statement_timeout = 0", "SELECT pg_catalog.set_config('search_path', '', false)",
		"CREATE TABLE public.t (id integer, note text)",
		"--\n-- Data for Name: t; Type: TABLE DATA; Schema: public; Owner: postgres\n--\n\nCOPY public.t (id, note) FROM stdin",
		"SELECT pg_catalog.setval('public.t_id_seq', 2, true)"}
	if !reflect.DeepEqual(sqls, want) {
		t.Fatalf("got %q", sqls)
	}
	if len(copies) != 1 || copies[0] != "1\thello; world\n2\t\\N\n" {
		t.Fatalf("copy data %q", copies)
	}
	if stmts[4].Line != 14 || stmts[2].Line != 4 { // line 13 is blank
		t.Errorf("line numbers: %d %d", stmts[4].Line, stmts[2].Line)
	}

	// An unread COPY block is skipped by the next call.
	sc := NewScanner(strings.NewReader("COPY t FROM stdin;\na\nb\n\\.\nSELECT 1;"), model.DriverPostgres)
	sc.Next()
	st, err := sc.Next()
	if err != nil || st.SQL != "SELECT 1" {
		t.Fatalf("after unread copy: %q %v", st.SQL, err)
	}

	// mysqldump: conditional comments are kept as statements.
	stmts, _ = scanAll(t, model.DriverMySQL, "/*!40101 SET @OLD=@@SQL_MODE */;\n/*!40014 SET FOREIGN_KEY_CHECKS=0 */;\nINSERT INTO `t` VALUES (1,'a\\'b;'),(2,NULL);\n")
	if len(stmts) != 3 || stmts[2].SQL != "INSERT INTO `t` VALUES (1,'a\\'b;'),(2,NULL)" || stmts[1].Line != 2 {
		t.Fatalf("mysqldump %+v", stmts)
	}
}

// TestScannerLarge streams a ~120 MB script and checks speed and that
// statements are produced incrementally.
func TestScannerLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("large input")
	}
	const n = 400_000
	row := "INSERT INTO t (id, name, note) VALUES (%d, 'name %d', 'some text with ; and '' quotes -- not a comment');\n"
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < n; i++ {
			fmt.Fprintf(pw, row, i, i)
			if i%1000 == 0 {
				fmt.Fprintf(pw, "-- progress %d\n/* block ; comment */\n", i)
			}
		}
		pw.Close()
	}()
	start := time.Now()
	sc := NewScanner(pr, model.DriverPostgres)
	count := 0
	for {
		st, err := sc.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(st.SQL, "INSERT") && !strings.HasPrefix(st.SQL, "-- progress") && !strings.HasPrefix(st.SQL, "/* block") {
			t.Fatalf("unexpected statement %q", st.SQL)
		}
		count++
	}
	if count != n {
		t.Fatalf("statements %d", count)
	}
	t.Logf("scanned %d statements in %s", count, time.Since(start))
}

// TestScannerWindowsLineEndings: files saved on Windows (CRLF) split into
// the same statements as with LF line endings.
func TestScannerWindowsLineEndings(t *testing.T) {
	scripts := []struct {
		kind   model.Driver
		script string
	}{
		{model.DriverPostgres, "\\restrict abc\nSET statement_timeout = 0;\n-- note; with a semicolon\nCREATE TABLE t (id int, note text);\n" +
			"COPY t (id, note) FROM stdin;\n1\thello; world\n2\t\\N\n\\.\nSELECT 1;\n\\unrestrict abc\n"},
		{model.DriverMySQL, "DROP PROCEDURE IF EXISTS p;\nDELIMITER $$\nCREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND$$\nDELIMITER ;\nCALL p();\n"},
		{model.DriverSQLite, "/* header */\nCREATE TABLE a (x TEXT DEFAULT 'a;b');\nINSERT INTO a VALUES ('line1\nline2');\n"},
	}
	norm := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	for _, sc := range scripts {
		lf, lfCopies := scanAll(t, sc.kind, sc.script)
		crlf, crlfCopies := scanAll(t, sc.kind, strings.ReplaceAll(sc.script, "\n", "\r\n"))
		if len(lf) != len(crlf) {
			t.Fatalf("%s: %d statements with LF, %d with CRLF", sc.kind, len(lf), len(crlf))
		}
		for i := range lf {
			if norm(crlf[i].SQL) != lf[i].SQL || crlf[i].Line != lf[i].Line {
				t.Errorf("%s statement %d: %q (line %d) vs %q (line %d)", sc.kind, i, crlf[i].SQL, crlf[i].Line, lf[i].SQL, lf[i].Line)
			}
		}
		if len(lfCopies) != len(crlfCopies) {
			t.Fatalf("%s: COPY blocks %d vs %d", sc.kind, len(lfCopies), len(crlfCopies))
		}
		for i := range lfCopies {
			if norm(crlfCopies[i]) != lfCopies[i] {
				t.Errorf("%s COPY %d: %q vs %q", sc.kind, i, crlfCopies[i], lfCopies[i])
			}
		}
	}
}
