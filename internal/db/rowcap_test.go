package db

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"
)

func TestCappable(t *testing.T) {
	for stmt, want := range map[string]bool{
		"SELECT * FROM t":                                       true,
		"-- all\nselect 1":                                      true,
		"(SELECT 1) UNION (SELECT 2)":                           true,
		"WITH x AS (SELECT 1) SELECT * FROM x":                  true,
		"VALUES (1)":                                            true,
		"SELECT 'insert into' AS \"update\" /* delete */":       true,
		"SELECT $$ delete $$":                                   true,
		"WITH x AS (DELETE FROM t RETURNING *) SELECT * FROM x": false,
		"SELECT * INTO copy FROM t":                             false,
		"SELECT * FROM t FOR UPDATE":                            false,
		"SELECT * FROM t LOCK IN SHARE MODE":                    false,
		"INSERT INTO t VALUES (1) RETURNING id":                 false,
		"SHOW TABLES":                                           false,
		"EXPLAIN SELECT 1":                                      false,
		"":                                                      false,
	} {
		if got := cappable(model.DriverPostgres, stmt); got != want {
			t.Errorf("%q: %v", stmt, got)
		}
	}
}

func TestMySQLHint(t *testing.T) {
	for in, want := range map[string]string{
		"SELECT 1":                              "SELECT /*+ H */ 1",
		"select/*+ NO_ICP(t) */ a FROM t":       "select/*+ H  NO_ICP(t) */ a FROM t",
		"WITH x AS (SELECT 1) SELECT * FROM x":  "WITH x AS (SELECT 1) SELECT /*+ H */ * FROM x",
		"-- SELECT\nSELECT 'it\\'s (' FROM t":   "-- SELECT\nSELECT /*+ H */ 'it\\'s (' FROM t",
		"(SELECT 1) UNION ALL SELECT 2":         "(SELECT 1) UNION ALL SELECT /*+ H */ 2",
		"TABLE t":                               "TABLE t",
		"SELECT (SELECT max(id) FROM u) FROM t": "SELECT /*+ H */ (SELECT max(id) FROM u) FROM t",
	} {
		if got := mysqlHint(model.DriverMySQL, in, "H"); got != want {
			t.Errorf("%q\n got %q\nwant %q", in, got, want)
		}
	}
}

// bigQueries return millions of rows, lazily.
var bigQueries = map[string]struct {
	driver model.Driver
	q      string
	// same: queries whose capped and uncapped results must be equal.
	same []string
}{
	"PG": {model.DriverPostgres, "SELECT generate_series(1, 20000000) AS n, repeat('x', 100) AS pad", pgSame},
	"CRDB": {model.DriverCockroachDB, "SELECT generate_series(1, 20000000) AS n, repeat('x', 100) AS pad",
		[]string{"SELECT 1 AS a, 'x' AS b, NULL AS c, 1.5::DECIMAL AS d, true AS e, DATE '2026-01-02' AS f, '{\"a\":1}'::JSONB AS g, ARRAY[1,2] AS h"}},
	"QUESTDB": {model.DriverQuestDB, "SELECT x, 'xxxxxxxxxxxxxxxxxxxxxxxx' AS pad FROM long_sequence(20000000)",
		[]string{"SELECT 1 AS a, 'x' AS b, 1.5 AS d, true AS e, now() IS NOT NULL AS f"}},
	"CRATEDB": {model.DriverCrateDB, "SELECT n, 'xxxxxxxxxxxxxxxxxxxxxxxx' AS pad FROM generate_series(1, 20000000) AS g(n)",
		[]string{"SELECT 1 AS a, 'x' AS b, NULL AS c, 1.5 AS d, true AS e, [1,2] AS h, {a = 1} AS o"}},
	"MARIADB": {model.DriverMariaDB, "SELECT seq, REPEAT('x', 100) FROM seq_1_to_20000000", mysqlSame},
	"MYSQL":   {model.DriverMySQL, "SELECT a.n, REPEAT('x', 100) FROM " + digits("a") + ", " + digits("b") + ", " + digits("c") + ", " + digits("d") + ", " + digits("e") + ", " + digits("f") + ", " + digits("g"), mysqlSame},
	"CH": {model.DriverClickHouse, "SELECT number, repeat('x', 100) FROM numbers(200000000)", []string{
		"SELECT count() FROM (SELECT number FROM numbers(100000))",
		"WITH x AS (SELECT number FROM numbers(100000)) SELECT sum(number) FROM x",
		"SELECT count() FROM (SELECT number FROM numbers(50000) UNION ALL SELECT number FROM numbers(50000))",
		"SELECT a FROM (SELECT 1 AS a UNION ALL SELECT 2) ORDER BY a DESC",
		"SELECT count() FROM numbers(100000) WHERE number IN (SELECT number * 2 FROM numbers(100000))",
		"SELECT count() FROM numbers(50000) AS a JOIN numbers(50000) AS b ON a.number = b.number",
		"SELECT number FROM numbers(50000) ORDER BY number DESC LIMIT 3",
	}},
}

func digits(alias string) string {
	return "(SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) " + alias
}

var pgSame = []string{
	`SELECT 1::int2 a, 2::int4 b, 3::int8 c, 1.5::float4 d, 2.25::float8 e, 12345678901234567890.123456789::numeric f, true g, 'x'::text h,
	 '\xdeadbeef'::bytea i, DATE '2026-01-02' j, TIMESTAMP '2026-01-02 03:04:05.678' k, TIMESTAMPTZ '2026-01-02 03:04:05+02' l, TIME '03:04' m,
	 INTERVAL '1 day 2 hours' n, '{"a": [1, 2]}'::json o, '{"a": 1}'::jsonb p, ARRAY[1, 2, NULL] q, ARRAY['a', 'b,c'] r, '10.0.0.1/8'::inet s,
	 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11'::uuid t, '(1,2)'::point u, '<a/>'::xml v, NULL::int w, 'infinity'::timestamp x, 'infinity'::date y,
	 'NaN'::float8 z, '[1,5)'::int4range aa, 1::oid ab, '$1.50'::money ac, 'abc'::name ad, 'c'::"char" ae, B'101' af, row(1, 'a') ag, '1'::xid ah`,
	"SELECT count(*) FROM (SELECT generate_series(1, 50000)) s",
	"VALUES (1, 'a'), (2, NULL)",
	"SELECT * FROM pg_class WHERE false",
	"SELECT",
}

var mysqlSame = []string{
	"SELECT 1 AS a, 'x' AS b, NULL AS c, 1.50 AS d, TRUE AS e, DATE '2026-01-02' AS f, TIMESTAMP '2026-01-02 03:04:05' AS g, X'DEAD' AS h",
	"SELECT COUNT(*) FROM (SELECT a.n FROM " + digits("a") + ", " + digits("b") + ") t",
	"WITH x AS (SELECT a.n FROM " + digits("a") + ", " + digits("b") + ") SELECT COUNT(*) FROM x",
	"SELECT 1 UNION SELECT 2 UNION SELECT 3 ORDER BY 1",
	"(SELECT 1) UNION ALL (SELECT 2)",
	"SELECT 5 LIMIT 1",
}

// TestRowCap: a capped query stops on the server, and leaves the session, its
// temporary tables and its open transaction as they were.
func TestRowCap(t *testing.T) {
	for name, x := range bigQueries {
		t.Run(name, func(t *testing.T) {
			cfg, pw := serverConfig(t, name, x.driver)
			c := openTest(t, cfg, pw)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			run := func(script string, rowCap int) []Result {
				t.Helper()
				rs, err := c.Execute(ctx, script, rowCap)
				if err != nil {
					t.Fatalf("%s: %v", script, err)
				}
				for _, r := range rs {
					if r.Err != nil {
						t.Fatalf("%s: %v", r.Statement, r.Err)
					}
				}
				return rs
			}

			txn := x.driver.Family() != model.DriverClickHouse && x.driver != model.DriverQuestDB && x.driver != model.DriverCrateDB
			if txn {
				tmp := "CREATE TEMPORARY TABLE gt_cap (n INT)"
				if x.driver == model.DriverCockroachDB {
					tmp = "SET experimental_enable_temp_tables = on; " + tmp
				}
				run(tmp, 0)
				run("BEGIN; INSERT INTO gt_cap VALUES (1)", 0)
			}
			start := time.Now()
			rs := run(x.q, 100)
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("the capped query took %v", d)
			} else {
				t.Logf("capped in %v", d)
			}
			if n := len(rs[0].Set.Rows); n != 100 || !rs[0].Truncated {
				t.Errorf("%d rows, truncated %v", n, rs[0].Truncated)
			}
			if txn {
				// The transaction is still open and whole: DDL in it isn't blocked either.
				run("INSERT INTO gt_cap VALUES (2)", 0)
				if x.driver.Family() == model.DriverPostgres && x.driver != model.DriverCockroachDB {
					run("SELECT * FROM gt_cap", 1)
					run("ALTER TABLE gt_cap ADD COLUMN m INT", 0)
				}
				run("ROLLBACK", 0)
				if got := run("SELECT COUNT(*) FROM gt_cap", 100)[0].Set.Rows[0][0].Text; got != "0" {
					t.Errorf("rows after rollback: %s", got)
				}
			}
			// Exactly the limit isn't truncated.
			if txn {
				run("INSERT INTO gt_cap VALUES (1), (2), (3)", 0)
				for rowCap, trunc := range map[int]bool{2: true, 3: false, 4: false} {
					if r := run("SELECT n FROM gt_cap ORDER BY n", rowCap)[0]; r.Truncated != trunc || len(r.Set.Rows) != min(rowCap, 3) {
						t.Errorf("cap %d: %d rows, truncated %v", rowCap, len(r.Set.Rows), r.Truncated)
					}
				}
			}
			// Neither the limit nor the protocol changes what is returned.
			for _, q := range x.same {
				a, b := run(q, 0)[0], run(q, 100)[0]
				if !reflect.DeepEqual(a.Set, b.Set) {
					t.Errorf("%s\nuncapped %+v\n  capped %+v", q, a.Set, b.Set)
				}
			}
			// An error leaves the session usable.
			if rs, err := c.Execute(ctx, "SELECT * FROM gt_no_such_table", 10); err != nil || rs[0].Err == nil || strings.Contains(rs[0].Err.Error(), "bad connection") {
				t.Errorf("error: %v %+v", err, rs)
			}
			run("SELECT 1", 10)
			// A limit applies to its own statement only.
			if r := run(x.q+" LIMIT 250", 0)[0]; len(r.Set.Rows) != 250 {
				t.Errorf("uncapped after capped: %d rows", len(r.Set.Rows))
			}
		})
	}
}

// TestRowCapMySQL: what other statements see of the one before is kept.
func TestRowCapMySQL(t *testing.T) {
	for name, d := range map[string]model.Driver{"MYSQL": model.DriverMySQL, "MARIADB": model.DriverMariaDB} {
		t.Run(name, func(t *testing.T) {
			cfg, pw := serverConfig(t, name, d)
			c := openTest(t, cfg, pw)
			ctx := context.Background()
			last := func(script string, rowCap int) []Value {
				t.Helper()
				rs, err := c.Execute(ctx, script, rowCap)
				if err != nil {
					t.Fatal(err)
				}
				r := rs[len(rs)-1]
				if r.Err != nil || len(r.Set.Rows) == 0 {
					t.Fatalf("%s: %+v", r.Statement, r)
				}
				return r.Set.Rows[len(r.Set.Rows)-1]
			}
			if v := last("SELECT SQL_CALC_FOUND_ROWS n FROM "+digits("a")+" LIMIT 2; SELECT FOUND_ROWS()", 5); v[0].Text != "10" {
				t.Errorf("FOUND_ROWS %v", v)
			}
			if v := last("SELECT n FROM "+digits("a")+"; SELECT FOUND_ROWS()", 5); v[0].Text != "10" {
				t.Errorf("FOUND_ROWS without LIMIT %v", v)
			}
			if v := last("CREATE TEMPORARY TABLE gt_rc (n INT); INSERT INTO gt_rc VALUES (1), (2), (3); SELECT ROW_COUNT()", 5); v[0].Text != "3" {
				t.Errorf("ROW_COUNT %v", v)
			}
			if v := last("SELECT CAST('x' AS SIGNED); SHOW WARNINGS", 5); !strings.Contains(v[2].Text, "Truncated") {
				t.Errorf("SHOW WARNINGS %v", v)
			}
			if v := last("SELECT @@sql_select_limit", 5); v[0].Text != "18446744073709551615" {
				t.Errorf("the session's own limit is %v", v)
			}
			// The user's own limit stays in charge.
			rs, err := c.Execute(ctx, "SET sql_select_limit = 2", 5)
			if err != nil || rs[0].Err != nil {
				t.Fatal(err, rs[0].Err)
			}
			if rs, _ = c.Execute(ctx, "SELECT n FROM "+digits("a"), 5); len(rs[0].Set.Rows) != 2 {
				t.Errorf("own limit: %d rows", len(rs[0].Set.Rows))
			}
		})
	}
}

// TestStream: rows arrive while the query runs, and stopping it keeps them.
func TestStream(t *testing.T) {
	for name, x := range bigQueries {
		t.Run(name, func(t *testing.T) {
			cfg, pw := serverConfig(t, name, x.driver)
			c := openTest(t, cfg, pw)
			for _, rowCap := range []int{0, 5_000_000} {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				var rows int
				var end Result
				start := time.Now()
				var first time.Duration
				err := c.ExecuteStream(ctx, "", x.q, rowCap, Sink{
					Begin: func(_ int, _ string, cols, _ []string) error {
						if len(cols) != 2 {
							t.Errorf("columns %v", cols)
						}
						return nil
					},
					Row: func(int, []Value) error {
						if rows++; rows == 1 {
							first = time.Since(start)
						}
						if rows == 10_000 {
							cancel()
						}
						return nil
					},
					End: func(_ int, r Result) { end = r },
				})
				cancel()
				if err != nil || !errors.Is(end.Err, context.Canceled) || rows < 10_000 || end.RowCount != rows {
					t.Errorf("cap %d: %v, %d rows, %+v", rowCap, err, rows, end)
				}
				t.Logf("cap %d: first row after %v, stopped after %v with %d rows", rowCap, first, time.Since(start), rows)
				// The session works again after a stop.
				if rs, err := c.Execute(context.Background(), "SELECT 1", 10); err != nil || rs[0].Err != nil {
					rs, err = c.Execute(context.Background(), "SELECT 1", 10)
					if err != nil || rs[0].Err != nil {
						t.Errorf("after a stop: %v %+v", err, rs)
					} else {
						t.Log("the session came back on the second query")
					}
				}
			}
		})
	}
}
