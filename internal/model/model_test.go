package model

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "connections.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	g := s.AddGroup("Work")
	c1 := NewConnection(DriverPostgres)
	c1.Name, c1.User, c1.GroupID, c1.Tag = "prod db", "app", g.ID, TagProduction
	c2 := NewConnection(DriverSQLite)
	c2.Name, c2.Path = "local", "/tmp/x.db"
	s.Upsert(c1)
	s.Upsert(c2)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store file mode %v %v", fi.Mode(), err)
	}

	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Connection(c1.ID)
	if got == nil || got.Name != "prod db" || got.Port != 5432 || got.Tag != TagProduction {
		t.Fatalf("loaded %+v", got)
	}
	if len(s2.InGroup(g.ID)) != 1 || len(s2.InGroup("")) != 1 {
		t.Fatal("grouping wrong")
	}

	c1b := *got
	c1b.Name = "renamed"
	s2.Upsert(&c1b)
	if len(s2.Connections) != 2 || s2.Connection(c1.ID).Name != "renamed" {
		t.Fatal("upsert should replace")
	}
	s2.DeleteGroup(g.ID)
	if s2.Connection(c1.ID).GroupID != "" || len(s2.Groups) != 0 {
		t.Fatal("deleting group should ungroup its connections")
	}
	s2.Delete(c2.ID)
	if s2.Connection(c2.ID) != nil {
		t.Fatal("delete failed")
	}
}

func TestOpenStoreErrors(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || len(s.Connections) != 0 {
		t.Fatal("missing file should give empty store", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte("{"), 0o600)
	if _, err := OpenStore(bad); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestValidate(t *testing.T) {
	ok := NewConnection(DriverMySQL)
	ok.Name, ok.User = "x", "root"
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(c *Connection){
		"no name":       func(c *Connection) { c.Name = " " },
		"no user":       func(c *Connection) { c.User = "" },
		"bad port":      func(c *Connection) { c.Port = 70000 },
		"ssh no host":   func(c *Connection) { c.UseSSHTunnel = true; c.SSHUser = "u" },
		"unknown drv":   func(c *Connection) { c.Driver = "db2" },
		"sqlite nofile": func(c *Connection) { c.Driver = DriverSQLite },
		"sqlite ssh": func(c *Connection) {
			c.Driver, c.Path, c.UseSSHTunnel, c.SSHHost, c.SSHUser = DriverSQLite, "/x", true, "h", "u"
		},
	}
	for name, mut := range cases {
		c := *ok
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestDefaults(t *testing.T) {
	c := &Connection{Driver: DriverPostgres}
	if c.Address() != "127.0.0.1:5432" || c.PageSize() != 300 || c.SSHPortOrDefault() != 22 {
		t.Fatal(c.Address(), c.PageSize())
	}
	if NewConnection(DriverMySQL).SSLMode != "preferred" || NewConnection(DriverSQLite).Port != 0 {
		t.Fatal("driver defaults")
	}
	cl := NewConnection(DriverPostgres)
	cl.Favorites = []string{"a"}
	cp := cl.Clone()
	cp.Favorites[0] = "b"
	if cp.ID == cl.ID || cl.Favorites[0] != "a" {
		t.Fatal("clone must be deep with a new ID")
	}
}

func TestMemorySecrets(t *testing.T) {
	s := NewMemorySecrets()
	s.Set("c", SecretDBPassword, "pw")
	s.Set("c", SecretSSHPassword, "ssh")
	if v, _ := s.Get("c", SecretDBPassword); v != "pw" {
		t.Fatal(v)
	}
	s.Set("c", SecretDBPassword, "")
	if v, _ := s.Get("c", SecretDBPassword); v != "" {
		t.Fatal("empty Set should delete")
	}
	s.Clear("c")
	if v, _ := s.Get("c", SecretSSHPassword); v != "" {
		t.Fatal("Clear failed")
	}
}

func TestParseURL(t *testing.T) {
	c, pw, err := ParseURL("postgresql://app:p%40ss@db.example.com:6543/shop?sslmode=verify-full")
	if err != nil || c.Driver != DriverPostgres || c.Host != "db.example.com" || c.Port != 6543 || c.User != "app" ||
		pw != "p@ss" || c.DBName != "shop" || c.SSLMode != "verify-full" || c.Name != "shop @ db.example.com" {
		t.Fatalf("%+v %q %v", c, pw, err)
	}
	c, _, _ = ParseURL("mysql://root@localhost/")
	if c.Driver != DriverMySQL || c.Port != 3306 || c.DBName != "" || c.Name != "localhost" {
		t.Fatalf("%+v", c)
	}
	c, _, _ = ParseURL("mariadb://u@h:3307/d?ssl-mode=REQUIRED")
	if c.Driver != DriverMariaDB || c.Port != 3307 || c.SSLMode != "required" {
		t.Fatalf("%+v", c)
	}
	c, _, _ = ParseURL("sqlite:///home/me/data/app.db")
	if c.Driver != DriverSQLite || c.Path != "/home/me/data/app.db" || c.Name != "app" {
		t.Fatalf("%+v", c)
	}
	c, _, _ = ParseURL("/tmp/notes.sqlite3")
	if c.Driver != DriverSQLite || c.Name != "notes" {
		t.Fatalf("%+v", c)
	}
	for _, bad := range []string{"", "hello", "db2://x", "postgres://h:99999/x", "sqlite://"} {
		if _, _, err := ParseURL(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestCompatibleDrivers(t *testing.T) {
	for _, c := range []struct {
		d      Driver
		family Driver
		port   int
		label  string
	}{
		{DriverCockroachDB, DriverPostgres, 26257, "CockroachDB"},
		{DriverYugabyteDB, DriverPostgres, 5433, "YugabyteDB"},
		{DriverTiDB, DriverMySQL, 4000, "TiDB"},
		{DriverMariaDB, DriverMySQL, 3306, "MariaDB"},
		{DriverPostgres, DriverPostgres, 5432, "PostgreSQL"},
	} {
		if c.d.Family() != c.family || c.d.DefaultPort() != c.port || c.d.Label() != c.label {
			t.Errorf("%s: family %s port %d label %q", c.d, c.d.Family(), c.d.DefaultPort(), c.d.Label())
		}
		if got, want := c.d.SSLModes()[0], c.family.SSLModes()[0]; got != want {
			t.Errorf("%s: SSL mode %q, want %q", c.d, got, want)
		}
		conn := NewConnection(c.d)
		conn.Name, conn.User = "x", "root"
		if err := conn.Validate(); err != nil {
			t.Errorf("%s: %v", c.d, err)
		}
	}
	for raw, want := range map[string]Driver{
		"cockroachdb://root@localhost:26257/defaultdb?sslmode=disable":  DriverCockroachDB,
		"yugabytedb://yugabyte@db:5433/yugabyte":                        DriverYugabyteDB,
		"tidb://root@gateway.tidbcloud.com:4000/test?ssl-mode=REQUIRED": DriverTiDB,
	} {
		c, _, err := ParseURL(raw)
		if err != nil || c.Driver != want {
			t.Fatalf("%s: %v %v", raw, c, err)
		}
		if want == DriverTiDB && c.SSLMode != "required" || want == DriverCockroachDB && c.SSLMode != "disable" {
			t.Errorf("%s: SSL mode %q", raw, c.SSLMode)
		}
	}
	bad := NewConnection("db2")
	bad.Name, bad.User = "x", "u"
	if bad.Validate() == nil {
		t.Error("unknown driver accepted")
	}
}

func TestMoreDrivers(t *testing.T) {
	for raw, want := range map[string]struct {
		d   Driver
		ssl string
		db  string
	}{
		"postgresql://u:p@ep-cool-name-123.eu-central-1.aws.neon.tech/neondb":                    {DriverNeon, "require", "neondb"},
		"postgres://postgres:p@db.abcdefgh.supabase.co:5432/postgres":                            {DriverSupabase, "require", "postgres"},
		"mysql://u:p@aws.connect.psdb.cloud/shop?ssl-mode=VERIFY_IDENTITY":                       {DriverPlanetScale, "verify_identity", "shop"},
		"postgres://u@localhost/app":                                                             {DriverPostgres, "prefer", "app"},
		"timescaledb://u@localhost/metrics":                                                      {DriverTimescaleDB, "prefer", "metrics"},
		"percona://root@localhost/shop":                                                          {DriverPercona, "preferred", "shop"},
		"singlestore://admin@svc-1.singlestore.com:3306/db":                                      {DriverSingleStore, "preferred", "db"},
		"sqlserver://sa:p@localhost:1433?database=shop&encrypt=true&TrustServerCertificate=true": {DriverMSSQL, "trust", "shop"},
		"clickhouse://default@localhost:9440/logs?secure=true":                                   {DriverClickHouse, "require", "logs"},
	} {
		c, _, err := ParseURL(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if c.Driver != want.d || c.SSLMode != want.ssl || c.DBName != want.db {
			t.Errorf("%s: %s %q %q", raw, c.Driver, c.SSLMode, c.DBName)
		}
	}
	for _, raw := range []string{"duckdb:///data/warehouse.duckdb", "/data/warehouse.duckdb"} {
		c, _, err := ParseURL(raw)
		if err != nil || c.Driver != DriverDuckDB || c.Path != "/data/warehouse.duckdb" || c.Name != "warehouse" {
			t.Errorf("%s: %+v %v", raw, c, err)
		}
	}
	if !DriverDuckDB.IsFileBased() || DriverMSSQL.IsFileBased() || DriverMSSQL.DefaultPort() != 1433 || DriverClickHouse.DefaultPort() != 9000 {
		t.Error("file-based or port wrong")
	}
	if DriverNeon.SSLModes()[0] != "require" || DriverPlanetScale.SSLModes()[0] != "required" {
		t.Error("hosted services must default to TLS")
	}
	for _, d := range Drivers {
		if d.Label() == string(d) || d.Family() == "" {
			t.Errorf("%s has no spec", d)
		}
	}
}

func TestOracleURL(t *testing.T) {
	c, pw, err := ParseURL("oracle://scott:tiger@db.example.com:1522/FREEPDB1?ssl=true")
	if err != nil || c.Driver != DriverOracle || c.Port != 1522 || c.DBName != "FREEPDB1" || c.SSLMode != "require" || pw != "tiger" {
		t.Fatalf("%+v %q %v", c, pw, err)
	}
	if DriverOracle.DefaultPort() != 1521 || DriverOracle.Family() != DriverOracle || NewConnection(DriverOracle).SSLMode != "disable" {
		t.Error("oracle defaults")
	}
}
