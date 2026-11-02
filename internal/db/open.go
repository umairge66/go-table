package db

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"gotable/internal/model"
	"gotable/internal/sshtunnel"
)

// Credentials are the secrets needed to open a connection.
type Credentials struct {
	Password      string
	SSHPassword   string
	SSHPassphrase string
}

// LoadCredentials reads a connection's secrets from the store.
func LoadCredentials(s model.Secrets, connID string) (Credentials, error) {
	var c Credentials
	var err error
	if c.Password, err = s.Get(connID, model.SecretDBPassword); err != nil {
		return c, err
	}
	if c.SSHPassword, err = s.Get(connID, model.SecretSSHPassword); err != nil {
		return c, err
	}
	c.SSHPassphrase, err = s.Get(connID, model.SecretSSHPassphrase)
	return c, err
}

// Open connects to the database described by cfg, through an SSH tunnel
// when configured.
func Open(ctx context.Context, cfg *model.Connection, cred Credentials) (*Conn, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	addr := cfg.Address()
	var tunnel io.Closer
	if cfg.UseSSHTunnel {
		t, err := sshtunnel.Open(sshtunnel.Config{
			Host:          cfg.SSHHost,
			Port:          cfg.SSHPortOrDefault(),
			User:          cfg.SSHUser,
			Password:      cred.SSHPassword,
			KeyPath:       cfg.SSHKeyPath,
			KeyPassphrase: cred.SSHPassphrase,
		}, addr)
		if err != nil {
			return nil, err
		}
		tunnel = t
		addr = t.LocalAddr()
	}
	var (
		sqlDB *sql.DB
		eng   engine
		err   error
	)
	switch cfg.Driver.Family() {
	case model.DriverPostgres:
		sqlDB, err = openPostgres(cfg, addr, cred.Password)
		eng = postgresEngine{}
	case model.DriverMySQL:
		sqlDB, err = openMySQL(cfg, addr, cred.Password)
		eng = mysqlEngine{}
	case model.DriverSQLite:
		sqlDB, err = openSQLite(cfg.Path)
		eng = sqliteEngine{}
	case model.DriverMSSQL:
		sqlDB, err = openMSSQL(cfg, addr, cred.Password)
		eng = mssqlEngine{}
	default:
		if open, ok := openers[cfg.Driver.Family()]; ok {
			sqlDB, eng, err = open(cfg, addr, cred.Password)
		} else {
			err = fmt.Errorf("%s isn't available in this build of GoTable", cfg.Driver.Label())
		}
	}
	if err != nil {
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, err
	}
	return newConn(ctx, cfg, sqlDB, eng, tunnel)
}

// Test opens and immediately closes a connection, returning the server
// version on success.
func Test(ctx context.Context, cfg *model.Connection, cred Credentials) (string, error) {
	c, err := Open(ctx, cfg, cred)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.ServerVersion(ctx)
}

// openers holds engines registered by files that are only part of some
// builds (DuckDB needs cgo).
var openers = map[model.Driver]func(cfg *model.Connection, addr, password string) (*sql.DB, engine, error){}

// Supported reports whether this build can open connections of driver d.
func Supported(d model.Driver) bool {
	switch d.Family() {
	case model.DriverPostgres, model.DriverMySQL, model.DriverSQLite, model.DriverMSSQL:
		return true
	}
	_, ok := openers[d.Family()]
	return ok
}
