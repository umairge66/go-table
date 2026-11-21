package db

import (
	"context"
	"slices"
	"strings"
	"testing"

	"gotable/internal/model"
)

func TestBuildDropDatabase(t *testing.T) {
	for _, tc := range []struct {
		d    model.Driver
		dd   DropDatabase
		want string // or an error containing it, after "!"
	}{
		{model.DriverPostgres, DropDatabase{Name: "shop"}, `DROP DATABASE "shop";`},
		{model.DriverPostgres, DropDatabase{Name: "sales", Kind: "schema"}, `DROP SCHEMA "sales" RESTRICT;`},
		{model.DriverPostgres, DropDatabase{Name: "sales", Kind: "schema", Cascade: true}, `DROP SCHEMA "sales" CASCADE;`},
		{model.DriverCockroachDB, DropDatabase{Name: "odd \"name\""}, `DROP DATABASE "odd ""name""";`},
		{model.DriverMySQL, DropDatabase{Name: "shop"}, "DROP DATABASE `shop`;"},
		{model.DriverMariaDB, DropDatabase{Name: "shop", Kind: "schema"}, "!a schema is a database"},
		{model.DriverMSSQL, DropDatabase{Name: "shop"}, "DROP DATABASE [shop];"},
		{model.DriverMSSQL, DropDatabase{Name: "s", Kind: "schema"}, "DROP SCHEMA [s];"},
		{model.DriverMSSQL, DropDatabase{Name: "s", Kind: "schema", Cascade: true}, "!only delete an empty schema"},
		{model.DriverClickHouse, DropDatabase{Name: "logs"}, "DROP DATABASE `logs`;"},
		{model.DriverDuckDB, DropDatabase{Name: "stage", Kind: "schema", Cascade: true}, `DROP SCHEMA "stage" CASCADE;`},
		{model.DriverDuckDB, DropDatabase{Name: "x"}, "!one database per file"},
		{model.DriverMongoDB, DropDatabase{Name: "shop"}, `db.getSiblingDB("shop").dropDatabase()`},
		{model.DriverSQLite, DropDatabase{Name: "main"}, "!is a file"},
		{model.DriverOracle, DropDatabase{Name: "HR", Kind: "schema"}, "!DROP USER"},
		// What the server itself needs.
		{model.DriverPostgres, DropDatabase{Name: "template1"}, "!can't be deleted"},
		{model.DriverPostgres, DropDatabase{Name: "information_schema", Kind: "schema"}, "!can't be deleted"},
		{model.DriverMySQL, DropDatabase{Name: "MySQL"}, "!can't be deleted"},
		{model.DriverMSSQL, DropDatabase{Name: "tempdb"}, "!can't be deleted"},
		{model.DriverMongoDB, DropDatabase{Name: "admin"}, "!can't be deleted"},
		{model.DriverPostgres, DropDatabase{Name: "  "}, "!empty"},
		{model.DriverPostgres, DropDatabase{Name: "x", Kind: "table"}, "!unknown kind"},
	} {
		got, err := BuildDropDatabase(DialectFor(tc.d), tc.dd)
		if strings.HasPrefix(tc.want, "!") {
			if err == nil || !strings.Contains(err.Error(), tc.want[1:]) {
				t.Errorf("%s %+v: %q, %v; want error %q", tc.d, tc.dd, got, err, tc.want[1:])
			}
		} else if err != nil || got != tc.want {
			t.Errorf("%s %+v: %q, %v; want %q", tc.d, tc.dd, got, err, tc.want)
		}
	}
}

// TestDropDatabaseLive creates a database (or schema) on each server the
// test environment has, deletes it, and checks it is gone.
func TestDropDatabaseLive(t *testing.T) {
	ctx := context.Background()
	type server struct {
		name   string
		driver model.Driver
		create string // SQL; "" for MongoDB
		kind   string
	}
	for _, sv := range []server{
		{"PG", model.DriverPostgres, `CREATE DATABASE gt_dropme`, "database"},
		{"PG", model.DriverPostgres, `CREATE SCHEMA gt_dropme; CREATE TABLE gt_dropme.t (x int)`, "schema"},
		{"MYSQL", model.DriverMySQL, "CREATE DATABASE gt_dropme", "database"},
		{"MARIADB", model.DriverMariaDB, "CREATE DATABASE gt_dropme", "database"},
		{"MSSQL", model.DriverMSSQL, "CREATE DATABASE gt_dropme", "database"},
		{"CH", model.DriverClickHouse, "CREATE DATABASE gt_dropme", "database"},
		{"MONGODB", model.DriverMongoDB, `db.getSiblingDB("gt_dropme").t.insertOne({ a: 1 })`, "database"},
	} {
		t.Run(sv.name+"/"+sv.kind, func(t *testing.T) {
			cfg, pw := serverConfig(t, sv.name, sv.driver)
			c := openTest(t, cfg, pw)
			list := func() []string {
				var names []string
				var err error
				if sv.kind == "schema" {
					names, err = c.Schemas(ctx)
				} else if sv.driver == model.DriverPostgres || sv.driver == model.DriverMSSQL {
					names, err = c.Databases(ctx)
				} else {
					names, err = c.Schemas(ctx)
				}
				if err != nil {
					t.Fatal(err)
				}
				return names
			}
			res, err := c.Execute(ctx, sv.create, 0)
			if err != nil || res[len(res)-1].Err != nil {
				t.Fatal(err, res)
			}
			if !slices.Contains(list(), "gt_dropme") {
				t.Fatal("not created", list())
			}
			dd := DropDatabase{Name: "gt_dropme", Kind: sv.kind}
			if sv.kind == "schema" {
				// A schema with a table in it needs CASCADE.
				if _, err := c.DropDatabase(ctx, dd); err == nil {
					t.Fatal("dropped a schema that isn't empty without CASCADE")
				}
				dd.Cascade = true
			}
			if _, err := c.DropDatabase(ctx, dd); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(list(), "gt_dropme") {
				t.Fatal("still there", list())
			}
			// The database the connection uses is refused, with a reason.
			if sv.kind == "database" && (sv.driver == model.DriverPostgres || sv.driver == model.DriverMSSQL) {
				cur := c.CurrentDatabase(ctx)
				if _, err := c.DropDatabase(ctx, DropDatabase{Name: cur}); err == nil || !strings.Contains(err.Error(), "connected to") {
					t.Fatal(cur, err)
				}
			}
		})
	}
}

func TestDropDatabaseDuckDB(t *testing.T) {
	ctx := context.Background()
	cfg := model.NewConnection(model.DriverDuckDB)
	cfg.Name, cfg.Path = "drop", t.TempDir()+"/drop.duckdb"
	c := openTest(t, cfg, "")
	res, err := c.Execute(ctx, "CREATE SCHEMA stage; CREATE TABLE stage.t (x int)", 0)
	if err != nil || res[len(res)-1].Err != nil {
		t.Fatal(err, res)
	}
	if _, err := c.DropDatabase(ctx, DropDatabase{Name: "stage", Kind: "schema"}); err == nil {
		t.Fatal("dropped a schema with a table without CASCADE")
	}
	if _, err := c.DropDatabase(ctx, DropDatabase{Name: "stage", Kind: "schema", Cascade: true}); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Schemas(ctx); slices.Contains(s, "stage") {
		t.Fatal(s)
	}
}
