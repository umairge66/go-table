package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gotable/internal/model"
)

// DropDatabase names a database or schema to delete. Kind is "database"
// or, where a database holds schemas (PostgreSQL, SQL Server, DuckDB),
// "schema". Cascade also deletes what a schema holds.
type DropDatabase struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Cascade bool   `json:"cascade"`
}

// systemDatabases can't be deleted from GoTable: the server needs them.
var systemDatabases = map[model.Driver][]string{
	model.DriverPostgres:   {"template0", "template1", "pg_catalog", "information_schema", "pg_toast", "crdb_internal", "pg_extension"},
	model.DriverMySQL:      {"mysql", "information_schema", "performance_schema", "sys"},
	model.DriverMSSQL:      {"master", "model", "msdb", "tempdb", "sys", "INFORMATION_SCHEMA", "dbo", "guest"},
	model.DriverClickHouse: {"system", "information_schema", "INFORMATION_SCHEMA"},
	model.DriverDuckDB:     {"main", "information_schema", "pg_catalog", "temp"},
	model.DriverMongoDB:    {"admin", "local", "config"},
}

// BuildDropDatabase returns DROP DATABASE / DROP SCHEMA (or MongoDB's
// dropDatabase) for a database or schema.
func BuildDropDatabase(d Dialect, dd DropDatabase) (string, error) {
	name := strings.TrimSpace(dd.Name)
	if name == "" {
		return "", errors.New("which database? The name is empty")
	}
	if slices.ContainsFunc(systemDatabases[d.family], func(s string) bool { return strings.EqualFold(s, name) }) {
		return "", fmt.Errorf("%s belongs to the server and can't be deleted", name)
	}
	kind := dd.Kind
	if kind == "" {
		kind = "database"
	}
	if kind != "database" && kind != "schema" {
		return "", fmt.Errorf("unknown kind %q", dd.Kind)
	}
	cascade := ""
	switch d.family {
	case model.DriverMongoDB:
		return "db.getSiblingDB(" + jsonString(name) + ").dropDatabase()", nil
	case model.DriverSQLite:
		return "", errors.New("a SQLite database is a file: delete the file, or remove the connection")
	case model.DriverOracle:
		return "", errors.New("on Oracle a schema is a user: drop it with DROP USER … CASCADE in the SQL editor (it needs the privilege to)")
	case model.DriverDuckDB:
		if kind == "database" {
			return "", errors.New("DuckDB keeps one database per file: delete a schema, or the file")
		}
	case model.DriverMySQL, model.DriverClickHouse:
		if kind == "schema" {
			return "", errors.New("here a schema is a database: delete the database")
		}
	case model.DriverMSSQL:
		if kind == "schema" && dd.Cascade {
			return "", errors.New("SQL Server can only delete an empty schema: delete or move its tables first")
		}
	}
	if kind == "schema" && d.family != model.DriverMSSQL {
		cascade = " RESTRICT"
		if dd.Cascade {
			cascade = " CASCADE"
		}
	}
	return "DROP " + strings.ToUpper(kind) + " " + d.QuoteIdent(name) + cascade + ";", nil
}

// DropDatabase deletes a database or schema. It refuses the database the
// connection itself is using (PostgreSQL and SQL Server can't drop it).
func (c *Conn) DropDatabase(ctx context.Context, dd DropDatabase) (string, error) {
	stmt, err := BuildDropDatabase(c.Dialect, dd)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(dd.Name)
	if c.mongo != nil {
		if err := c.lock(ctx); err != nil {
			return "", err
		}
		defer c.mu.Unlock()
		return stmt, c.mongo.Database(name).Drop(ctx)
	}
	if (dd.Kind == "" || dd.Kind == "database") && (c.Dialect.family == model.DriverPostgres || c.Dialect.family == model.DriverMSSQL) {
		if cur := c.CurrentDatabase(ctx); strings.EqualFold(cur, name) {
			return "", fmt.Errorf("GoTable is connected to %s, so it can't delete it: connect to another database on the server and delete it from there", name)
		}
	}
	if err := c.lock(ctx); err != nil {
		return "", err
	}
	defer c.mu.Unlock()
	_, err = c.conn.ExecContext(ctx, stmt)
	return stmt, err
}
