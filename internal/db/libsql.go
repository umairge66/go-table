package db

// libSQL servers (sqld, Turso) are SQLite over HTTP (hrana.go). The SQLite
// engine does the work; a server has no ATTACH, temporary tables or VACUUM.

import (
	"context"
	"database/sql"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"gotable/internal/model"
)

type libsqlEngine struct{ sqliteEngine }

func openLibSQL(cfg *model.Connection, addr, token string, tunneled bool) (*sql.DB, error) {
	t, err := mongoTLS(cfg, tunneled)
	if err != nil {
		return nil, err
	}
	scheme := "http"
	if t != nil {
		scheme = "https"
	}
	// The host stays in the URL through a tunnel: a server may tell its
	// databases apart by it.
	base := scheme + "://" + net.JoinHostPort(strings.Trim(cfg.HostOrDefault(), "[]"), strconv.Itoa(cfg.PortOrDefault()))
	dial := ""
	if tunneled {
		dial = addr
	}
	return sql.OpenDB(newHrana(base, dial, strings.TrimSpace(token), t)), nil
}

func (e libsqlEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	v, err := e.sqliteEngine.serverVersion(ctx, c)
	if err != nil {
		return "", err
	}
	if c.Config.Driver == model.DriverTurso {
		return "Turso · " + v, nil
	}
	if s := libsqlServer(ctx, c); s != "" {
		return "libSQL " + s + " · " + v, nil
	}
	return "libSQL · " + v, nil
}

// libsqlServer asks sqld its version: "sqld 0.24.33 (d6c75af6 2026-08-23)".
func libsqlServer(ctx context.Context, c *Conn) string {
	h, ok := c.db.Driver().(hranaDriver)
	if !ok || h.c == nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.c.base+"/version", nil)
	if err != nil {
		return ""
	}
	res, err := h.c.http.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 256))
	if f := strings.Fields(string(b)); res.StatusCode == http.StatusOK && len(f) > 1 && f[0] == "sqld" {
		return f[1]
	}
	return ""
}

func (libsqlEngine) databases(_ context.Context, c *Conn) ([]string, error) {
	return []string{c.Config.Host}, nil
}
