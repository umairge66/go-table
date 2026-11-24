package db

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"
)

func openTest(t *testing.T, cfg *model.Connection, password string) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Open(ctx, cfg, Credentials{Password: password})
	if err != nil {
		t.Fatalf("open %s: %v", cfg.Driver, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestSQLite(t *testing.T) {
	cfg := model.NewConnection(model.DriverSQLite)
	cfg.Name = "test"
	cfg.Path = filepath.Join(t.TempDir(), "test.db")
	runSuite(t, openTest(t, cfg, ""))
}

// serverConfig builds a connection from GOTABLE_TEST_<NAME>_{HOST,PORT,USER,
// PASSWORD,DB}; the test is skipped when HOST is unset. scripts/test-dbs.sh
// starts suitable containers.
func serverConfig(t *testing.T, name string, d model.Driver) (*model.Connection, string) {
	env := func(k string) string { return os.Getenv("GOTABLE_TEST_" + name + "_" + k) }
	if env("HOST") == "" && t != nil {
		t.Skipf("GOTABLE_TEST_%s_HOST not set", name)
	}
	cfg := model.NewConnection(d)
	cfg.Name = name
	cfg.Host = env("HOST")
	cfg.Port, _ = strconv.Atoi(env("PORT"))
	cfg.User = env("USER")
	cfg.DBName = env("DB")
	switch d.Family() {
	case model.DriverPostgres:
		cfg.SSLMode = "disable"
	case model.DriverMySQL:
		cfg.SSLMode = "disabled"
	default:
		cfg.SSLMode = model.NewConnection(d).SSLMode
	}
	return cfg, env("PASSWORD")
}

// serverConfigOptional is serverConfig without skipping: nil when unset.
func serverConfigOptional(name string, d model.Driver) (*model.Connection, string) {
	if os.Getenv("GOTABLE_TEST_"+name+"_HOST") == "" {
		return nil, ""
	}
	return serverConfig(nil, name, d)
}

func TestPostgres(t *testing.T) {
	cfg, pw := serverConfig(t, "PG", model.DriverPostgres)
	c := openTest(t, cfg, pw)
	runSuite(t, c)

	t.Run("schema switch", func(t *testing.T) {
		ctx := context.Background()
		c.Execute(ctx, "DROP SCHEMA IF EXISTS gt_other CASCADE; CREATE SCHEMA gt_other; CREATE TABLE gt_other.t (x int)", 0)
		defer c.Execute(ctx, "DROP SCHEMA gt_other CASCADE", 0)
		if err := c.UseSchema(ctx, "gt_other"); err != nil {
			t.Fatal(err)
		}
		if c.Schema() != "gt_other" {
			t.Fatalf("schema %q", c.Schema())
		}
		res, _ := c.Execute(ctx, "INSERT INTO t VALUES (1)", 0)
		if res[0].Err != nil {
			t.Fatal(res[0].Err)
		}
		c.UseSchema(ctx, "public")
	})

	t.Run("dollar quoted function", func(t *testing.T) {
		ctx := context.Background()
		res, err := c.Execute(ctx, `CREATE OR REPLACE FUNCTION gt_f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql;
			SELECT gt_f(); DROP FUNCTION gt_f()`, 0)
		if err != nil || len(res) != 3 || res[1].Err != nil || res[1].Set.Rows[0][0].Text != "1" {
			t.Fatalf("%v %+v", err, res)
		}
	})

	t.Run("types render as text", func(t *testing.T) {
		res, _ := c.Execute(context.Background(), `SELECT 1.50::numeric AS n, true AS b, '2024-01-02'::date AS d,
			'2024-01-02 03:04:05'::timestamp AS ts, 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11'::uuid AS u,
			'{"k":1}'::jsonb AS j, '\xdeadbeef'::bytea AS by, ARRAY[1,2] AS arr`, 0)
		if res[0].Err != nil {
			t.Fatal(res[0].Err)
		}
		want := []string{"1.50", "true", "2024-01-02", "2024-01-02 03:04:05",
			"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", `{"k": 1}`, "0xDEADBEEF", "{1,2}"}
		for i, w := range want {
			if got := res[0].Set.Rows[0][i].Text; got != w {
				t.Errorf("%s = %q, want %q", res[0].Set.Columns[i], got, w)
			}
		}
	})
}

func TestMySQL(t *testing.T) {
	cfg, pw := serverConfig(t, "MYSQL", model.DriverMySQL)
	runSuite(t, openTest(t, cfg, pw))
}

func TestMariaDB(t *testing.T) {
	cfg, pw := serverConfig(t, "MARIADB", model.DriverMariaDB)
	c := openTest(t, cfg, pw)
	runSuite(t, c)

	t.Run("delimiter procedure", func(t *testing.T) {
		ctx := context.Background()
		res, err := c.Execute(ctx, "DROP PROCEDURE IF EXISTS gt_p;\nDELIMITER //\nCREATE PROCEDURE gt_p() BEGIN SELECT 42; END //\nDELIMITER ;\nCALL gt_p();\nDROP PROCEDURE gt_p", 0)
		if err != nil || len(res) != 4 {
			t.Fatalf("%v %+v", err, res)
		}
		for _, r := range res {
			if r.Err != nil {
				t.Fatalf("%s: %v", r.Statement, r.Err)
			}
		}
		if res[2].Set == nil || res[2].Set.Rows[0][0].Text != "42" {
			t.Fatalf("call result %+v", res[2].Set)
		}
	})
}

// PostgreSQL- and MySQL-compatible servers run the same suite.

func TestCockroachDB(t *testing.T) {
	cfg, pw := serverConfig(t, "CRDB", model.DriverCockroachDB)
	runSuite(t, openTest(t, cfg, pw))
}

func TestYugabyteDB(t *testing.T) {
	cfg, pw := serverConfig(t, "YB", model.DriverYugabyteDB)
	runSuite(t, openTest(t, cfg, pw))
}

func TestTiDB(t *testing.T) {
	cfg, pw := serverConfig(t, "TIDB", model.DriverTiDB)
	runSuite(t, openTest(t, cfg, pw))
}

func TestMSSQL(t *testing.T) {
	cfg, pw := serverConfig(t, "MSSQL", model.DriverMSSQL)
	c := openTest(t, cfg, pw)
	runSuite(t, c)

	t.Run("types render as text", func(t *testing.T) {
		res, _ := c.Execute(context.Background(), `SELECT CAST('6F9619FF-8B86-D011-B42D-00C04FC964FF' AS uniqueidentifier) AS g,
			CAST(1.50 AS decimal(5,2)) AS d, CAST(1 AS bit) AS b, CAST('2024-01-02 03:04:05.5' AS datetime2) AS ts, N'ünï' AS n`, 0)
		if res[0].Err != nil {
			t.Fatal(res[0].Err)
		}
		want := []string{"6F9619FF-8B86-D011-B42D-00C04FC964FF", "1.50", "true", "2024-01-02 03:04:05.5", "ünï"}
		for i, w := range want {
			if got := res[0].Set.Rows[0][i].Text; got != w {
				t.Errorf("%s = %q, want %q", res[0].Set.Columns[i], got, w)
			}
		}
	})

	t.Run("GO batches", func(t *testing.T) {
		res, err := c.Execute(context.Background(), "SELECT 1 AS a\nGO\nSELECT [x]]y] = 2\ngo 1\nSELECT 3", 0)
		if err != nil || len(res) != 3 || res[1].Err != nil || res[1].Set.Columns[0] != "x]y" {
			t.Fatalf("%v %+v", err, res)
		}
	})
}

func TestTimescaleDB(t *testing.T) {
	cfg, pw := serverConfig(t, "TIMESCALE", model.DriverTimescaleDB)
	c := openTest(t, cfg, pw)
	if v, _ := c.ServerVersion(context.Background()); !strings.HasPrefix(v, "TimescaleDB ") {
		t.Errorf("version %q", v)
	}
	runSuite(t, c)
}

func TestPercona(t *testing.T) {
	cfg, pw := serverConfig(t, "PERCONA", model.DriverPercona)
	c := openTest(t, cfg, pw)
	if v, _ := c.ServerVersion(context.Background()); !strings.HasPrefix(v, "Percona Server ") {
		t.Errorf("version %q", v)
	}
	runSuite(t, c)
}

func TestSingleStore(t *testing.T) {
	cfg, pw := serverConfig(t, "SINGLESTORE", model.DriverSingleStore)
	runSuite(t, openTest(t, cfg, pw))
}

// Servers with their own SQL behind the PostgreSQL or MySQL protocol.

func TestCrateDB(t *testing.T) {
	cfg, pw := serverConfig(t, "CRATEDB", model.DriverCrateDB)
	runCompatSuite(t, openTest(t, cfg, pw), compatCaps{
		version: "CrateDB ",
		setup: []string{"DROP VIEW IF EXISTS gt_cv", "DROP TABLE IF EXISTS gt_c", "DROP TABLE IF EXISTS gt_copy",
			"CREATE TABLE gt_c (id INT PRIMARY KEY, name TEXT NOT NULL, city TEXT, score INT DEFAULT 0)",
			"INSERT INTO gt_c (id, name, city, score) VALUES " + compatRows},
		after: "REFRESH TABLE gt_c",
		views: true, edits: true, pk: true, addCol: true, csv: true, dump: true,
	})
}

func TestQuestDB(t *testing.T) {
	cfg, pw := serverConfig(t, "QUESTDB", model.DriverQuestDB)
	runCompatSuite(t, openTest(t, cfg, pw), compatCaps{
		version: "QuestDB ",
		setup: []string{"DROP VIEW IF EXISTS gt_cv", "DROP TABLE IF EXISTS gt_c", "DROP TABLE IF EXISTS gt_copy",
			"CREATE TABLE gt_c (id INT, name VARCHAR, city VARCHAR, score INT)",
			"INSERT INTO gt_c (id, name, city, score) VALUES " + compatRows},
		views: true, edits: true, noDelete: true, addCol: true, textType: "VARCHAR", csv: true, dump: true, nullNotEqual: true,
	})
}

func TestStarRocks(t *testing.T) {
	cfg, pw := serverConfig(t, "STARROCKS", model.DriverStarRocks)
	runCompatSuite(t, openTest(t, cfg, pw), compatCaps{
		version: "StarRocks ",
		setup: []string{"DROP VIEW IF EXISTS gt_cv", "DROP TABLE IF EXISTS gt_c", "DROP TABLE IF EXISTS gt_copy",
			"CREATE TABLE gt_c (id INT NOT NULL, name VARCHAR(100) NOT NULL, city VARCHAR(100), score INT DEFAULT '0') PRIMARY KEY (id) DISTRIBUTED BY HASH(id) PROPERTIES ('replication_num' = '1')",
			"INSERT INTO gt_c (id, name, city, score) VALUES " + compatRows},
		views: true, edits: true, pk: true, addCol: true, csv: true, dump: true,
	})
}

func TestDoris(t *testing.T) {
	cfg, pw := serverConfig(t, "DORIS", model.DriverDoris)
	runCompatSuite(t, openTest(t, cfg, pw), compatCaps{
		version: "Apache Doris ",
		setup: []string{"DROP VIEW IF EXISTS gt_cv", "DROP TABLE IF EXISTS gt_c", "DROP TABLE IF EXISTS gt_copy",
			`CREATE TABLE gt_c (id INT NOT NULL, name VARCHAR(100) NOT NULL, city VARCHAR(100), score INT DEFAULT "0")
			UNIQUE KEY (id) DISTRIBUTED BY HASH(id) BUCKETS 1
			PROPERTIES ("replication_num" = "1", "enable_unique_key_merge_on_write" = "true")`,
			"INSERT INTO gt_c (id, name, city, score) VALUES " + compatRows},
		views: true, edits: true, pk: true, addCol: true, csv: true, dump: true,
	})
}

// Cassandra and ScyllaDB: CQL.
func TestCassandra(t *testing.T) {
	ran := false
	for name, d := range map[string]model.Driver{"CASSANDRA": model.DriverCassandra, "SCYLLA": model.DriverScyllaDB} {
		if os.Getenv("GOTABLE_TEST_"+name+"_HOST") == "" {
			continue
		}
		ran = true
		t.Run(strings.ToLower(name), func(t *testing.T) {
			cfg, pw := serverConfig(t, name, d)
			runCompatSuite(t, openTest(t, cfg, pw), compatCaps{
				version: strings.Fields(d.Label())[0] + " ",
				setup: []string{"DROP TABLE IF EXISTS gt_c", "DROP TABLE IF EXISTS gt_copy",
					"CREATE TABLE gt_c (id int PRIMARY KEY, name text, city text, score int)",
					"INSERT INTO gt_c (id, name, city, score) VALUES (1, 'Alice', 'Berlin', 90)",
					"INSERT INTO gt_c (id, name, city, score) VALUES (2, 'Bob', 'Paris', 70)",
					"INSERT INTO gt_c (id, name, score) VALUES (3, 'Carol', 85)",
					"INSERT INTO gt_c (id, name, city, score) VALUES (4, 'Dave', 'Berlin', 60)",
					"INSERT INTO gt_c (id, name, city, score) VALUES (5, 'Eve_x', 'Rome', 99)"},
				edits: true, pk: true, addCol: true, textType: "text", csv: true, dump: true,
				editorOK:   "SELECT name FROM gt_c WHERE id IN (1, 5);\nSELECT COUNT(*) AS n FROM gt_c",
				editorStop: "SELECT * FROM gt_c WHERE id = 1;\nSELECT * FROM gt_no_such_table;\nSELECT * FROM gt_c",
			})
		})
	}
	if !ran {
		t.Skip("GOTABLE_TEST_CASSANDRA_HOST not set")
	}
}

// TestCassandraTypes: CQL's types, keys and edits, beyond the shared suite.
func TestCassandraTypes(t *testing.T) {
	for name, d := range map[string]model.Driver{"CASSANDRA": model.DriverCassandra, "SCYLLA": model.DriverScyllaDB} {
		if os.Getenv("GOTABLE_TEST_"+name+"_HOST") == "" {
			continue
		}
		t.Run(strings.ToLower(name), func(t *testing.T) { testCassandraTypes(t, name, d) })
	}
}

func testCassandraTypes(t *testing.T, name string, d model.Driver) {
	cfg, pw := serverConfig(t, name, d)
	c := openTest(t, cfg, pw)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	must := func(q string) Result {
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
	must("DROP TABLE IF EXISTS gt_t; DROP TABLE IF EXISTS gt_t2")
	must(`CREATE TABLE gt_t (tenant text, day date, at timestamp, id uuid, ok boolean, ratio double, data blob, note text,
		tags set<text>, nums list<int>, attrs map<text, int>, PRIMARY KEY ((tenant, day), at, id)) WITH CLUSTERING ORDER BY (at DESC, id ASC)`)
	must(`INSERT INTO gt_t (tenant, day, at, id, ok, ratio, data, note, tags, nums, attrs) VALUES ('acme', '2024-03-15', '2024-03-15T10:20:30.123Z',
		0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0, true, 2.5, 0x0102ff, '', {'a', 'b'}, [3, 1, 2], {'x': 1, 'y': 2})`)
	must(`INSERT INTO gt_t (tenant, day, at, id) VALUES ('acme', '2024-03-15', '2024-03-15T09:00:00Z', 00000000-0000-0000-0000-000000000001)`)
	ref := TableRef{Schema: "gotable", Name: "gt_t"}

	cols, err := c.Columns(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, col := range cols {
		if col.PrimaryKey {
			keys = append(keys, col.Name+":"+col.Comment)
		}
	}
	if strings.Join(keys, " ") != "tenant:partition key day:partition key at:clustering, desc id:clustering, asc" {
		t.Fatal(keys)
	}
	if def, err := c.Definition(ctx, Object{Schema: "gotable", Name: "gt_t", Kind: KindTable}); err != nil || !strings.Contains(def, "CLUSTERING ORDER BY") {
		t.Fatal(def, err)
	}

	rs, err := c.FetchRows(ctx, ref, Page{}, nil)
	if err != nil || len(rs.Rows) != 2 {
		t.Fatal(rs, err)
	}
	row := map[string]Value{}
	for i, col := range rs.Columns {
		row[col] = rs.Rows[0][i] // clustering DESC: the 10:20 row first
	}
	for col, want := range map[string]string{"day": "2024-03-15", "at": "2024-03-15T10:20:30.123Z", "id": "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0",
		"ok": "true", "ratio": "2.5", "data": "0x0102ff", "tags": "{'a', 'b'}", "nums": "[3, 1, 2]", "attrs": "{'x': 1, 'y': 2}"} {
		if row[col].Text != want {
			t.Errorf("%s = %q, want %q", col, row[col].Text, want)
		}
	}
	if row["note"].Null || row["note"].Text != "" { // empty, not NULL
		t.Errorf("note %+v", row["note"])
	}
	other := map[string]Value{}
	for i, col := range rs.Columns {
		other[col] = rs.Rows[1][i]
	}
	if !other["note"].Null || !other["tags"].Null {
		t.Errorf("unset columns should be NULL: %+v", other)
	}

	// Edits write each type as CQL, keyed on the whole primary key.
	key := map[string]Value{"tenant": Text("acme"), "day": Text("2024-03-15"), "at": row["at"], "id": row["id"]}
	edit := []Change{{Kind: ChangeUpdate, Key: key, Values: map[string]Value{"ok": Text("false"), "ratio": Text("1e3"), "note": Text("it's"),
		"tags": Text("{'c'}"), "attrs": Text("{'z': 9}"), "data": NullValue}}}
	stmts, err := c.PreviewChanges(ctx, ref, edit, true)
	if err != nil || !strings.Contains(stmts[0], "IF EXISTS") || !strings.Contains(stmts[0], `"note" = 'it''s'`) {
		t.Fatal(stmts, err)
	}
	if err := c.ApplyChanges(ctx, ref, edit, true); err != nil {
		t.Fatal(err)
	}
	got := must(`SELECT ok, ratio, note, tags, attrs, data FROM gt_t WHERE tenant = 'acme' AND day = '2024-03-15' AND at = '2024-03-15T10:20:30.123Z'`).Set.Rows[0]
	if got[0].Text != "false" || got[1].Text != "1000" || got[2].Text != "it's" || got[3].Text != "{'c'}" || got[4].Text != "{'z': 9}" || !got[5].Null {
		t.Fatalf("%+v", got)
	}
	for _, bad := range []map[string]Value{{"ratio": Text("lots")}, {"ok": Text("maybe")}, {"at": Text("2024-01-01")}} {
		if err := c.ApplyChanges(ctx, ref, []Change{{Kind: ChangeUpdate, Key: key, Values: bad}}, true); err == nil {
			t.Errorf("%v: expected an error", bad)
		}
	}
	// A row deleted meanwhile: the lightweight transaction says so.
	must(`DELETE FROM gt_t WHERE tenant = 'acme' AND day = '2024-03-15' AND at = '2024-03-15T10:20:30.123Z' AND id = 0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0`)
	if err := c.ApplyChanges(ctx, ref, edit, true); err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Fatal(err)
	}
	ins := []Change{{Kind: ChangeInsert, Values: map[string]Value{"tenant": Text("acme"), "day": Text("2024-03-15"), "at": Text("2024-03-15T09:00:00.000Z"),
		"id": Text("00000000-0000-0000-0000-000000000001"), "note": Text("dup")}}}
	if err := c.ApplyChanges(ctx, ref, ins, true); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatal(err)
	}

	// A CQL filter runs on the server.
	if rs, err := c.FetchRows(ctx, ref, Page{RawWhere: "tenant = 'acme' AND day = '2024-03-15'"}, nil); err != nil || len(rs.Rows) != 1 {
		t.Fatal(rs, err)
	}
	if n, err := c.CountRows(ctx, ref, Page{RawWhere: "tenant = 'nobody' AND day = '2024-03-15'"}, nil); err != nil || n != 0 {
		t.Fatal(n, err)
	}

	// Exported and loaded back, the types are the same.
	must(`INSERT INTO gt_t (tenant, day, at, id, ok, ratio, data, note, tags, nums, attrs) VALUES ('acme', '2024-03-16', '2024-03-16T00:00:00Z',
		0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0, true, 0.1, 0x00, 'a''b', {'q'}, [1], {'k': -5})`)
	before := must("SELECT * FROM gt_t").Set.Rows
	var buf bytes.Buffer
	if err := c.Dump(ctx, &buf, DumpOptions{Objects: []Object{{Schema: "gotable", Name: "gt_t", Kind: KindTable}}, Structure: true, Data: true}, nil); err != nil {
		t.Fatal(err)
	}
	dump := strings.ReplaceAll(buf.String(), "gt_t", "gt_t2")
	if _, err := c.ImportSQL(ctx, strings.NewReader(dump), ImportOptions{}, nil); err != nil {
		t.Fatalf("%v\n%s", err, dump)
	}
	if after := must("SELECT * FROM gt_t2").Set.Rows; !reflect.DeepEqual(before, after) {
		t.Fatalf("\n%v\n%v", before, after)
	}

	// Indexes and columns change; the key doesn't.
	if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterAddIndex, Index: &IndexDef{Name: "gt_t_note", Columns: []string{"note"}}}}, false); err != nil {
		t.Fatal(err)
	}
	if ix, _ := c.Indexes(ctx, ref); len(ix) != 2 || ix[1].Name != "gt_t_note" {
		t.Fatal(ix)
	}
	if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterDropIndex, Name: "gt_t_note"}, {Kind: AlterDropColumn, Column: "nums"}}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Alter(ctx, ref, []AlterOp{{Kind: AlterSetPrimaryKey, Columns: []string{"id"}}}, true); err == nil {
		t.Fatal("changed a primary key")
	}

	// Keyspaces: USE in the editor, and one made, then deleted.
	must("CREATE KEYSPACE IF NOT EXISTS gt_ks WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': 1}")
	must("USE gt_ks; CREATE TABLE IF NOT EXISTS x (a int PRIMARY KEY)")
	if c.Schema() != "gt_ks" {
		t.Fatal(c.Schema())
	}
	if objs, _ := c.Objects(ctx, "gt_ks"); len(objs) != 1 || objs[0].Name != "x" {
		t.Fatal(objs)
	}
	if _, err := c.DropDatabase(ctx, DropDatabase{Name: "gt_ks"}); err == nil {
		t.Fatal("dropped the keyspace in use")
	}
	must("USE gotable")
	if _, err := c.DropDatabase(ctx, DropDatabase{Name: "gt_ks"}); err != nil {
		t.Fatal(err)
	}
	if ks, _ := c.Schemas(ctx); slices.Contains(ks, "gt_ks") || !slices.Contains(ks, "gotable") {
		t.Fatal(ks)
	}
	must("DROP TABLE gt_t; DROP TABLE gt_t2")
}

func TestGreenplum(t *testing.T) {
	ran := false
	for _, env := range []string{"GREENPLUM", "GREENPLUM6"} {
		if os.Getenv("GOTABLE_TEST_"+env+"_HOST") == "" {
			continue
		}
		ran = true
		t.Run(strings.ToLower(env), func(t *testing.T) {
			cfg, pw := serverConfig(t, env, model.DriverGreenplum)
			c := openTest(t, cfg, pw)
			if v, _ := c.ServerVersion(context.Background()); !strings.HasPrefix(v, "Greenplum ") {
				t.Errorf("version %q", v)
			}
			runSuite(t, c)
			t.Run("distribution", func(t *testing.T) { testGreenplumDistribution(t, c) })
		})
	}
	if !ran {
		t.Skip("GOTABLE_TEST_GREENPLUM_HOST not set")
	}
}

// A table's distribution is in its definition and in a dump, which loads
// back with it.
func testGreenplumDistribution(t *testing.T, c *Conn) {
	ctx := context.Background()
	exec := func(q string) {
		t.Helper()
		res, err := c.Execute(ctx, q, 0)
		if err != nil || res[len(res)-1].Err != nil {
			t.Fatalf("%s: %v %+v", q, err, res)
		}
	}
	tables := map[string]string{"gt_by_region": "DISTRIBUTED BY (region)", "gt_random": "DISTRIBUTED RANDOMLY", "gt_everywhere": "DISTRIBUTED REPLICATED"}
	drop := func() {
		for n := range tables {
			c.Execute(ctx, "DROP TABLE IF EXISTS "+n, 0)
		}
	}
	drop()
	defer drop()
	var objs []Object
	for n, dist := range tables {
		exec("CREATE TABLE " + n + " (id serial, region text, note text DEFAULT 'a)\n);') " + dist)
		exec("INSERT INTO " + n + " (region, note) VALUES ('eu', 'x'), ('us', NULL)")
		objs = append(objs, Object{Schema: c.Schema(), Name: n, Kind: KindTable})
	}
	check := func() {
		t.Helper()
		for _, o := range objs {
			def, err := c.Definition(ctx, o)
			if err != nil || !strings.Contains(def, "\n"+tables[o.Name]) {
				t.Errorf("%s: %v %s", o.Name, err, def)
			}
			ti, err := c.TableInfo(ctx, TableRef{Schema: o.Schema, Name: o.Name})
			if err != nil || ti.Options != strings.ToLower(tables[o.Name]) {
				t.Errorf("%s: %v %+v", o.Name, err, ti)
			}
		}
	}
	check()
	var dump strings.Builder
	if err := c.Dump(ctx, &dump, DumpOptions{Objects: objs, Structure: true, Data: true}, nil); err != nil {
		t.Fatal(err)
	}
	drop()
	res, err := c.ImportSQL(ctx, strings.NewReader(dump.String()), ImportOptions{Transaction: true}, nil)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("import: %v %+v\n%s", err, res, dump.String())
	}
	check()
	exec("INSERT INTO gt_random (region) VALUES ('next')") // the sequence goes on
	r, _ := c.Execute(ctx, "SELECT max(id), count(*) FROM gt_random", 0)
	if got := r[0].Set.Rows[0]; got[1].Text != "3" {
		t.Errorf("rows after reload: %v", got)
	}
}

// Turso's cloud: GOTABLE_TEST_TURSO_HOST=<database>-<organization>.turso.io
// and GOTABLE_TEST_TURSO_PASSWORD=<token>. Use an empty database: the suite
// makes and drops tables named gt_….
func TestTurso(t *testing.T) {
	cfg, pw := serverConfig(t, "TURSO", model.DriverTurso)
	c := openTest(t, cfg, pw)
	if v, _ := c.ServerVersion(context.Background()); !strings.HasPrefix(v, "Turso · SQLite 3.") {
		t.Errorf("version %q", v)
	}
	runSuite(t, c)
	t.Run("session", func(t *testing.T) { testLibSQLSession(t, c, 8000) })
	t.Run("transfer", func(t *testing.T) { testLibSQLTransfer(t, c) })
}

// Cloudflare D1: GOTABLE_TEST_D1_HOST=cloudflare, _USER=<account ID>,
// _PASSWORD=<API token> and _DB=<an empty database, by name or ID>.
func TestD1(t *testing.T) {
	cfg, pw := serverConfig(t, "D1", model.DriverD1)
	cfg.Host, cfg.Port = "", 0
	c := openTest(t, cfg, pw)
	if v, _ := c.ServerVersion(context.Background()); !strings.HasPrefix(v, "Cloudflare D1 · ") {
		t.Errorf("version %q", v)
	}
	runSuite(t, c)
	t.Run("transfer", func(t *testing.T) { testLibSQLTransfer(t, c) })
	t.Run("d1", func(t *testing.T) { testD1(t, c) })
}

func TestLibSQL(t *testing.T) {
	cfg, pw := serverConfig(t, "LIBSQL", model.DriverLibSQL)
	c := openTest(t, cfg, pw)
	if v, _ := c.ServerVersion(context.Background()); !strings.HasPrefix(v, "libSQL ") || !strings.Contains(v, "SQLite 3.") {
		t.Errorf("version %q", v)
	}
	runSuite(t, c)
	t.Run("transfer", func(t *testing.T) { testLibSQLTransfer(t, c) })
}
