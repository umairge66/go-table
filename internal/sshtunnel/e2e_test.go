package sshtunnel_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"gotable/internal/db"
	"gotable/internal/model"
	"gotable/internal/sshtunnel"
)

// TestDatabaseOverSSH opens a PostgreSQL connection through the tunnel the
// way the app does. It needs the containers from scripts/test-dbs.sh.
func TestDatabaseOverSSH(t *testing.T) {
	if os.Getenv("GOTABLE_TEST_PG_HOST") == "" {
		t.Skip("GOTABLE_TEST_PG_HOST not set")
	}
	host, port, forwards := sshtunnel.StartTestServer(t, "tunnel", "s3cret", nil)

	cfg := model.NewConnection(model.DriverPostgres)
	cfg.Name = "pg over ssh"
	cfg.Host = os.Getenv("GOTABLE_TEST_PG_HOST")
	cfg.Port, _ = strconv.Atoi(os.Getenv("GOTABLE_TEST_PG_PORT"))
	cfg.User = os.Getenv("GOTABLE_TEST_PG_USER")
	cfg.DBName = os.Getenv("GOTABLE_TEST_PG_DB")
	cfg.SSLMode = "disable"
	cfg.UseSSHTunnel = true
	cfg.SSHHost, cfg.SSHPort, cfg.SSHUser = host, port, "tunnel"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cred := db.Credentials{Password: os.Getenv("GOTABLE_TEST_PG_PASSWORD"), SSHPassword: "s3cret"}
	c, err := db.Open(ctx, cfg, cred)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Execute(ctx, "SELECT inet_server_port(), current_user", 0)
	if err != nil || res[0].Err != nil {
		t.Fatal(err, res)
	}
	if got := res[0].Set.Rows[0][1].Text; got != cfg.User {
		t.Fatalf("current_user %q", got)
	}
	if forwards() == 0 {
		t.Fatal("connection did not go through the SSH server")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	cred.SSHPassword = "wrong"
	if _, err := db.Open(ctx, cfg, cred); err == nil {
		t.Fatal("expected SSH auth failure")
	}
}
