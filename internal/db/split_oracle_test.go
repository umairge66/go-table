package db

import (
	"strings"
	"testing"

	"gotable/internal/model"
)

func TestOracleSplit(t *testing.T) {
	script := "SELECT 1 FROM dual;\nCREATE OR REPLACE PROCEDURE p AS\nBEGIN\n  NULL;\n  INSERT INTO t VALUES ('a;b');\nEND;\n/\nBEGIN p; END;\n/\nSELECT 2 FROM dual"
	want := []string{"SELECT 1 FROM dual", "CREATE OR REPLACE PROCEDURE p AS\nBEGIN\n  NULL;\n  INSERT INTO t VALUES ('a;b');\nEND;", "BEGIN p; END;", "SELECT 2 FROM dual"}
	var got []string
	for _, s := range SplitStatements(model.DriverOracle, script) {
		got = append(got, s.SQL)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("split:\n%q\nwant\n%q", got, want)
	}
	stmts, _ := scanAll(t, model.DriverOracle, script)
	got = got[:0]
	for _, s := range stmts {
		got = append(got, s.SQL)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("scanner:\n%q\nwant\n%q", got, want)
	}
}
