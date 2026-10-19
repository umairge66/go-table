package db

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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
	if d == model.DriverPostgres {
		cfg.SSLMode = "disable"
	} else {
		cfg.SSLMode = "disabled"
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
