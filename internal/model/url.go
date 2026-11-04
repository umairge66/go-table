package model

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseURL builds a connection from a connection string such as
// postgres://user:pass@host:5432/db?sslmode=require, mysql://…, mariadb://…,
// sqlserver://…, clickhouse://…, mongodb://…, mongodb+srv://…, the compatible servers' own schemes
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
	if l := strings.ToLower(raw); strings.HasPrefix(l, "mongodb://") || strings.HasPrefix(l, "mongodb+srv://") {
		return parseMongoURL(raw)
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

// parseMongoURL reads mongodb://user:pw@host1:1,host2:2/db?options and
// mongodb+srv://. It is parsed by hand as net/url refuses several hosts. In
// such a URL the database is also where the user is defined, unless
// authSource says otherwise; GoTable's default is admin, so that is kept as
// an explicit option.
func parseMongoURL(raw string) (*Connection, string, error) {
	scheme, rest, _ := strings.Cut(raw, "://")
	c := NewConnection(DriverMongoDB)
	c.SRV = strings.EqualFold(scheme, "mongodb+srv")
	authority, path := rest, ""
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		authority, path = rest[:i], rest[i:]
	}
	password := ""
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		user, pw, _ := strings.Cut(authority[:i], ":")
		authority = authority[i+1:]
		var err error
		if c.User, err = url.PathUnescape(user); err != nil {
			return nil, "", fmt.Errorf("invalid user name: %w", err)
		}
		if password, err = url.PathUnescape(pw); err != nil {
			return nil, "", fmt.Errorf("invalid password: %w", err)
		}
	}
	switch {
	case authority == "":
		return nil, "", fmt.Errorf("the MongoDB URL has no host")
	case strings.Contains(authority, ","): // a replica set's seed list
		c.Host, c.Port = authority, 0
	default:
		c.Host = authority
		if h, p, err := net.SplitHostPort(authority); err == nil {
			n, err := strconv.Atoi(p)
			if err != nil || n <= 0 || n > 65535 {
				return nil, "", fmt.Errorf("invalid port %q", p)
			}
			c.Host, c.Port = h, n
		}
	}
	if c.SRV {
		c.Port = 0
		c.SSLMode = "require" // an SRV connection defaults to TLS
	}
	path, query, _ := strings.Cut(path, "?")
	db, err := url.PathUnescape(strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil, "", fmt.Errorf("invalid database name: %w", err)
	}
	c.DBName = db
	q, err := url.ParseQuery(query)
	if err != nil {
		return nil, "", fmt.Errorf("invalid options: %w", err)
	}
	tlsOn := func(k string) bool { return strings.EqualFold(q.Get(k), "true") }
	if tlsOn("tls") || tlsOn("ssl") {
		c.SSLMode = "require"
	}
	if tlsOn("tlsInsecure") || tlsOn("tlsAllowInvalidCertificates") {
		c.SSLMode = "skip-verify"
	}
	if q.Get("tls") == "false" || q.Get("ssl") == "false" {
		c.SSLMode = "disable"
	}
	for _, k := range []string{"tls", "ssl", "tlsInsecure", "tlsAllowInvalidCertificates"} {
		q.Del(k)
	}
	if c.User != "" && c.DBName != "" && !q.Has("authSource") {
		q.Set("authSource", c.DBName)
	}
	c.Options = q.Encode()
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
