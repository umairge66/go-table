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
		"unknown drv":   func(c *Connection) { c.Driver = "oracle" },
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
	for _, bad := range []string{"", "hello", "oracle://x", "postgres://h:99999/x", "sqlite://"} {
		if _, _, err := ParseURL(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}
