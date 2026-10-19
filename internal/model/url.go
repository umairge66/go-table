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
// sqlite:///path/to/file.db or a plain path to a .db/.sqlite file. The
// password, if present, is returned separately so it can go to the keyring.
func ParseURL(raw string) (*Connection, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", fmt.Errorf("paste a connection URL")
	}
	if !strings.Contains(raw, "://") {
		ext := strings.ToLower(filepath.Ext(raw))
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
	case "sqlite", "sqlite3", "file":
		c := NewConnection(DriverSQLite)
		c.Path = u.Path
		if u.Host != "" && u.Host != "localhost" {
			c.Path = u.Host + u.Path // sqlite://relative/path.db
		}
		if c.Path == "" {
			return nil, "", fmt.Errorf("the SQLite URL has no file path")
		}
		c.Name = strings.TrimSuffix(filepath.Base(c.Path), filepath.Ext(c.Path))
		return c, "", nil
	default:
		return nil, "", fmt.Errorf("unsupported database type %q", u.Scheme)
	}
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
	if m := q.Get("sslmode"); m != "" && d == DriverPostgres {
		c.SSLMode = m
	}
	if m := q.Get("ssl-mode"); m != "" && d != DriverPostgres {
		c.SSLMode = strings.ToLower(m)
	}
	c.Name = c.Host
	if c.DBName != "" {
		c.Name = c.DBName + " @ " + c.Host
	}
	return c, password, nil
}
