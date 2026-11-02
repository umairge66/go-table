// Package model holds the persistent configuration objects: saved
// connections, connection groups and the store that persists them.
package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Driver identifies a database engine.
type Driver string

const (
	DriverPostgres Driver = "postgres"
	DriverMySQL    Driver = "mysql"
	DriverMariaDB  Driver = "mariadb"
	DriverSQLite   Driver = "sqlite"
	DriverMSSQL    Driver = "sqlserver"
	DriverDuckDB   Driver = "duckdb"
	// DriverClickHouse connects over ClickHouse's native protocol.
	DriverClickHouse Driver = "clickhouse"
	DriverOracle     Driver = "oracle"

	// Servers that speak the PostgreSQL protocol and SQL dialect.
	DriverCockroachDB Driver = "cockroachdb"
	DriverYugabyteDB  Driver = "yugabytedb"
	DriverTimescaleDB Driver = "timescaledb"
	DriverNeon        Driver = "neon"
	DriverSupabase    Driver = "supabase"
	// Servers that speak the MySQL protocol and SQL dialect.
	DriverTiDB        Driver = "tidb"
	DriverPercona     Driver = "percona"
	DriverSingleStore Driver = "singlestore"
	DriverPlanetScale Driver = "planetscale"
)

// driverSpec describes a driver. family is the engine whose protocol and SQL
// dialect it uses; hosted services require TLS, so it is their default.
type driverSpec struct {
	label  string
	family Driver
	port   int
	hosted bool
}

var specs = map[Driver]driverSpec{
	DriverPostgres:    {"PostgreSQL", DriverPostgres, 5432, false},
	DriverMySQL:       {"MySQL", DriverMySQL, 3306, false},
	DriverMariaDB:     {"MariaDB", DriverMySQL, 3306, false},
	DriverSQLite:      {"SQLite", DriverSQLite, 0, false},
	DriverMSSQL:       {"SQL Server", DriverMSSQL, 1433, false},
	DriverDuckDB:      {"DuckDB", DriverDuckDB, 0, false},
	DriverClickHouse:  {"ClickHouse", DriverClickHouse, 9000, false},
	DriverOracle:      {"Oracle", DriverOracle, 1521, false},
	DriverCockroachDB: {"CockroachDB", DriverPostgres, 26257, false},
	DriverYugabyteDB:  {"YugabyteDB", DriverPostgres, 5433, false},
	DriverTimescaleDB: {"TimescaleDB", DriverPostgres, 5432, false},
	DriverNeon:        {"Neon", DriverPostgres, 5432, true},
	DriverSupabase:    {"Supabase", DriverPostgres, 5432, true},
	DriverTiDB:        {"TiDB", DriverMySQL, 4000, false},
	DriverPercona:     {"Percona Server", DriverMySQL, 3306, false},
	DriverSingleStore: {"SingleStore", DriverMySQL, 3306, false},
	DriverPlanetScale: {"PlanetScale", DriverMySQL, 3306, true},
}

// Drivers lists every driver in display order: the engines, then the
// servers and services compatible with one of them.
var Drivers = []Driver{DriverPostgres, DriverMySQL, DriverMariaDB, DriverSQLite, DriverMSSQL, DriverOracle, DriverDuckDB, DriverClickHouse,
	DriverCockroachDB, DriverYugabyteDB, DriverTimescaleDB, DriverNeon, DriverSupabase,
	DriverTiDB, DriverPercona, DriverSingleStore, DriverPlanetScale}

// Family returns the engine whose protocol and SQL dialect the driver uses:
// DriverPostgres, DriverMySQL, DriverSQLite, DriverMSSQL, DriverOracle,
// DriverDuckDB or DriverClickHouse. Code that only cares about syntax compares families;
// code for one product's quirks compares drivers.
func (d Driver) Family() Driver {
	if s, ok := specs[d]; ok {
		return s.family
	}
	return d
}

// Label returns the human readable driver name.
func (d Driver) Label() string {
	if s, ok := specs[d]; ok {
		return s.label
	}
	return string(d)
}

// DefaultPort returns the conventional TCP port for the driver, or 0 for
// file based engines.
func (d Driver) DefaultPort() int { return specs[d].port }

// IsFileBased reports whether the driver opens a local file instead of a
// network server.
func (d Driver) IsFileBased() bool {
	f := d.Family()
	return f == DriverSQLite || f == DriverDuckDB
}

// IsHosted reports a cloud service, which only accepts TLS connections.
func (d Driver) IsHosted() bool { return specs[d].hosted }

// SSLModes returns the SSL modes the driver accepts, first entry is default.
func (d Driver) SSLModes() []string {
	var modes []string
	switch d.Family() {
	case DriverPostgres:
		modes = []string{"prefer", "disable", "allow", "require", "verify-ca", "verify-full"}
	case DriverMySQL:
		modes = []string{"preferred", "disabled", "required", "verify_ca", "verify_identity"}
	case DriverMSSQL:
		// go-mssqldb's encrypt setting; "trust" accepts a self-signed certificate.
		modes = []string{"optional", "disable", "required", "trust", "strict"}
	case DriverClickHouse, DriverOracle:
		modes = []string{"disable", "require", "skip-verify"}
	default:
		return nil
	}
	if d.IsHosted() { // TLS first: the service refuses anything else
		switch d.Family() {
		case DriverPostgres:
			return []string{"require", "verify-full", "verify-ca"}
		case DriverMySQL:
			return []string{"required", "verify_identity", "verify_ca"}
		}
	}
	return modes
}

// Tag is the environment label shown next to a connection.
type Tag string

const (
	TagNone        Tag = ""
	TagLocal       Tag = "local"
	TagDevelopment Tag = "development"
	TagTesting     Tag = "testing"
	TagStaging     Tag = "staging"
	TagProduction  Tag = "production"
)

// Tags lists every tag in display order.
var Tags = []Tag{TagNone, TagLocal, TagDevelopment, TagTesting, TagStaging, TagProduction}

// Connection is a saved database connection. Secrets (database password,
// SSH password, key passphrases) are not stored here; they live in the
// secret store keyed by connection ID.
type Connection struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	GroupID string `json:"group_id,omitempty"`
	Color   string `json:"color,omitempty"`
	Tag     Tag    `json:"tag,omitempty"`

	Driver Driver `json:"driver"`
	Host   string `json:"host,omitempty"`
	Port   int    `json:"port,omitempty"`
	User   string `json:"user,omitempty"`
	DBName string `json:"database,omitempty"`
	// Path is the database file for file based drivers.
	Path string `json:"path,omitempty"`

	SSLMode     string `json:"ssl_mode,omitempty"`
	SSLKeyPath  string `json:"ssl_key_path,omitempty"`
	SSLCertPath string `json:"ssl_cert_path,omitempty"`
	SSLCAPath   string `json:"ssl_ca_path,omitempty"`

	UseSSHTunnel bool   `json:"use_ssh_tunnel,omitempty"`
	SSHHost      string `json:"ssh_host,omitempty"`
	SSHPort      int    `json:"ssh_port,omitempty"`
	SSHUser      string `json:"ssh_user,omitempty"`
	SSHKeyPath   string `json:"ssh_key_path,omitempty"`

	// RowsPerPage controls paging in the data view.
	RowsPerPage int `json:"rows_per_page,omitempty"`
	// LimitQueryRows caps rows returned by the query editor (0 = no cap).
	LimitQueryRows int `json:"limit_query_rows,omitempty"`

	SelectedSchema string   `json:"selected_schema,omitempty"`
	Favorites      []string `json:"favorites,omitempty"`
}

// NewID returns a random identifier for connections and groups.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToUpper(hex.EncodeToString(b))
}

// NewConnection returns a connection with defaults for the given driver.
func NewConnection(d Driver) *Connection {
	return &Connection{
		ID:          NewID(),
		Driver:      d,
		Host:        "127.0.0.1",
		Port:        d.DefaultPort(),
		SSLMode:     firstOr(d.SSLModes(), ""),
		SSHPort:     22,
		RowsPerPage: 300,
	}
}

func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

// HostOrDefault returns the configured host, falling back to localhost.
func (c *Connection) HostOrDefault() string {
	if c.Host == "" {
		return "127.0.0.1"
	}
	return c.Host
}

// PortOrDefault returns the configured port, falling back to the driver
// default.
func (c *Connection) PortOrDefault() int {
	if c.Port == 0 {
		return c.Driver.DefaultPort()
	}
	return c.Port
}

// SSHPortOrDefault returns the configured SSH port or 22.
func (c *Connection) SSHPortOrDefault() int {
	if c.SSHPort == 0 {
		return 22
	}
	return c.SSHPort
}

// PageSize returns RowsPerPage with a sane default.
func (c *Connection) PageSize() int {
	if c.RowsPerPage <= 0 {
		return 300
	}
	return c.RowsPerPage
}

// Address returns host:port for network drivers.
func (c *Connection) Address() string {
	return c.HostOrDefault() + ":" + strconv.Itoa(c.PortOrDefault())
}

// Validate checks the fields required to connect.
func (c *Connection) Validate() error {
	if !slices.Contains(Drivers, c.Driver) {
		return fmt.Errorf("unsupported driver %q", c.Driver)
	}
	switch {
	case c.Driver.IsFileBased():
		if c.Path == "" {
			return fmt.Errorf("database file is required")
		}
	default:
		if c.User == "" {
			return fmt.Errorf("user is required")
		}
		if c.Port < 0 || c.Port > 65535 {
			return fmt.Errorf("port %d out of range", c.Port)
		}
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if c.UseSSHTunnel {
		if c.Driver.IsFileBased() {
			return fmt.Errorf("SSH tunnel is not available for %s", c.Driver.Label())
		}
		if c.SSHHost == "" || c.SSHUser == "" {
			return fmt.Errorf("SSH host and user are required")
		}
	}
	return nil
}

// Clone returns a deep copy with a fresh ID.
func (c *Connection) Clone() *Connection {
	cp := *c
	cp.ID = NewID()
	cp.Name = c.Name + " copy"
	cp.Favorites = append([]string(nil), c.Favorites...)
	return &cp
}

// Group is a named folder of connections.
type Group struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Expanded bool   `json:"expanded"`
}
