package model

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseURL builds a connection from a connection string such as
// postgres://user:pass@host:5432/db?sslmode=require, mysql://…, mariadb://…,
// sqlserver://…, clickhouse://…, the compatible servers' own schemes
// (cockroachdb://, tidb://, …), sqlite:///path/to/file.db, duckdb:///path or
// a plain path to a .db/.sqlite/.duckdb file. Plain postgres:// and mysql://
// URLs of known cloud services (Neon, Supabase, PlanetScale, …) pick that
// service. The
// password, if present, is returned separately so it can go to the keyring.
func ParseURL(raw string) (*Connection, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", fmt.Errorf("paste a connection URL")
	}
	if !strings.Contains(raw, "://") {
		ext := strings.ToLower(filepath.Ext(raw))
		if ext == ".duckdb" || ext == ".ddb" {
			c := NewConnection(DriverDuckDB)
			c.Path = raw
			c.Name = strings.TrimSuffix(filepath.Base(raw), filepath.Ext(raw))
			return c, "", nil
		}
		if ext == ".db" || ext == ".sqlite" || ext == ".sqlite3" || ext == ".db3" {
			c := NewConnection(DriverSQLite)
			c.Path = raw
			c.Name = strings.TrimSuffix(filepath.Base(raw), filepath.Ext(raw))
			return c, "", nil
		}
		return nil, "", fmt.Errorf("not a connection URL: expected something like postgres://user@host/db")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", fmt.Errorf("invalid URL: %w", err)
	}
	var d Driver
	switch strings.ToLower(u.Scheme) {
	case "postgres", "postgresql", "pg":
		d = DriverPostgres
	case "mysql":
		d = DriverMySQL
	case "mariadb":
		d = DriverMariaDB
	case "cockroachdb", "cockroach", "crdb":
		d = DriverCockroachDB
	case "yugabytedb", "yugabyte", "ysql":
		d = DriverYugabyteDB
	case "tidb":
		d = DriverTiDB
	case "timescaledb", "timescale":
		d = DriverTimescaleDB
	case "neon":
		d = DriverNeon
	case "supabase":
		d = DriverSupabase
	case "percona":
		d = DriverPercona
	case "singlestore", "memsql":
		d = DriverSingleStore
	case "planetscale":
		d = DriverPlanetScale
	case "sqlserver", "mssql":
		d = DriverMSSQL
	case "clickhouse", "ch":
		d = DriverClickHouse
	case "oracle":
		d = DriverOracle // oracle://user:pw@host:1521/SERVICE_NAME
	case "sqlite", "sqlite3", "file", "duckdb":
		fd := DriverSQLite
		if strings.EqualFold(u.Scheme, "duckdb") {
			fd = DriverDuckDB
		}
		c := NewConnection(fd)
		c.Path = u.Path
		if u.Host != "" && u.Host != "localhost" {
			c.Path = u.Host + u.Path // sqlite://relative/path.db
		}
		if c.Path == "" {
			return nil, "", fmt.Errorf("the %s URL has no file path", fd.Label())
		}
		c.Name = strings.TrimSuffix(filepath.Base(c.Path), filepath.Ext(c.Path))
		return c, "", nil
	default:
		return nil, "", fmt.Errorf("unsupported database type %q", u.Scheme)
	}
	d = byHost(d, strings.ToLower(u.Hostname()))
	c := NewConnection(d)
	c.Host = u.Hostname()
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return nil, "", fmt.Errorf("invalid port %q", p)
		}
		c.Port = n
	}
	password := ""
	if u.User != nil {
		c.User = u.User.Username()
		password, _ = u.User.Password()
	}
	c.DBName = strings.TrimPrefix(u.Path, "/")
	q := u.Query()
	if m := q.Get("sslmode"); m != "" && d.Family() == DriverPostgres {
		c.SSLMode = m
	}
	if m := q.Get("ssl-mode"); m != "" && d.Family() == DriverMySQL {
		c.SSLMode = strings.ToLower(m)
	}
	if d == DriverMSSQL {
		if db := q.Get("database"); db != "" { // sqlserver://user@host?database=shop
			c.DBName = db
		}
		switch strings.ToLower(q.Get("encrypt")) {
		case "disable":
			c.SSLMode = "disable"
		case "true":
			c.SSLMode = "required"
		case "strict":
			c.SSLMode = "strict"
		}
		if strings.EqualFold(q.Get("TrustServerCertificate"), "true") {
			c.SSLMode = "trust"
		}
	}
	if d == DriverOracle && strings.EqualFold(q.Get("ssl"), "true") {
		c.SSLMode = "require"
		if strings.EqualFold(q.Get("ssl verify"), "false") {
			c.SSLMode = "skip-verify"
		}
	}
	if d == DriverClickHouse && (q.Get("secure") == "true" || q.Get("secure") == "1") {
		c.SSLMode = "require"
		if q.Get("skip_verify") == "true" {
			c.SSLMode = "skip-verify"
		}
	}
	c.Name = c.Host
	if c.DBName != "" {
		c.Name = c.DBName + " @ " + c.Host
	}
	return c, password, nil
}

// hostedSuffixes maps the host names of cloud services to their driver.
var hostedSuffixes = []struct {
	suffix string
	family Driver
	driver Driver
}{
	{".neon.tech", DriverPostgres, DriverNeon},
	{".supabase.co", DriverPostgres, DriverSupabase},
	{".supabase.com", DriverPostgres, DriverSupabase},
	{".cockroachlabs.cloud", DriverPostgres, DriverCockroachDB},
	{".psdb.cloud", DriverMySQL, DriverPlanetScale},
	{".tidbcloud.com", DriverMySQL, DriverTiDB},
	{".singlestore.com", DriverMySQL, DriverSingleStore},
}

// byHost picks the service a plain postgres:// or mysql:// URL points at.
func byHost(d Driver, host string) Driver {
	if d != DriverPostgres && d != DriverMySQL {
		return d
	}
	for _, h := range hostedSuffixes {
		if h.family == d && strings.HasSuffix(host, h.suffix) {
			return h.driver
		}
	}
	return d
}
