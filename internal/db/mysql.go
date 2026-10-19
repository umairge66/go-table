package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"gotable/internal/model"
)

type mysqlEngine struct{}

// mysqlSystemSchemas are hidden from the schema list.
var mysqlSystemSchemas = map[string]bool{
	"information_schema": true, "performance_schema": true, "mysql": true, "sys": true,
}

func mysqlConfig(cfg *model.Connection, addr, password string) (*mysql.Config, error) {
	mc := mysql.NewConfig()
	mc.User = cfg.User
	mc.Passwd = password
	mc.Net = "tcp"
	mc.Addr = addr
	mc.DBName = cfg.DBName
	mc.InterpolateParams = true
	// Report matched rows instead of changed rows, so committing an edit
	// that leaves a value unchanged is not mistaken for a vanished row.
	mc.ClientFoundRows = true
	mc.Timeout = 15 * time.Second
	switch cfg.SSLMode {
	case "", "preferred":
		mc.TLSConfig = "preferred"
	case "disabled":
		mc.TLSConfig = "false"
	case "required":
		mc.TLSConfig = "skip-verify"
	case "verify_ca", "verify_identity":
		tc := &tls.Config{ServerName: cfg.HostOrDefault()}
		if cfg.SSLCAPath != "" {
			pem, err := os.ReadFile(cfg.SSLCAPath)
			if err != nil {
				return nil, fmt.Errorf("read CA: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("no certificates in %s", cfg.SSLCAPath)
			}
			tc.RootCAs = pool
		}
		if cfg.SSLCertPath != "" && cfg.SSLKeyPath != "" {
			cert, err := tls.LoadX509KeyPair(cfg.SSLCertPath, cfg.SSLKeyPath)
			if err != nil {
				return nil, fmt.Errorf("load client certificate: %w", err)
			}
			tc.Certificates = []tls.Certificate{cert}
		}
		if cfg.SSLMode == "verify_ca" {
			// Verify the chain but not the host name.
			tc.InsecureSkipVerify = true
			tc.VerifyPeerCertificate = verifyChainOnly(tc.RootCAs)
		}
		name := "gotable-" + cfg.ID
		if err := mysql.RegisterTLSConfig(name, tc); err != nil {
			return nil, err
		}
		mc.TLSConfig = name
	default:
		return nil, fmt.Errorf("unknown MySQL SSL mode %q", cfg.SSLMode)
	}
	return mc, nil
}

func verifyChainOnly(roots *x509.CertPool) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return fmt.Errorf("server sent no certificate")
		}
		certs := make([]*x509.Certificate, len(raw))
		for i, r := range raw {
			c, err := x509.ParseCertificate(r)
			if err != nil {
				return err
			}
			certs[i] = c
		}
		inter := x509.NewCertPool()
		for _, c := range certs[1:] {
			inter.AddCert(c)
		}
		_, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter})
		return err
	}
}

func openMySQL(cfg *model.Connection, addr, password string) (*sql.DB, error) {
	mc, err := mysqlConfig(cfg, addr, password)
	if err != nil {
		return nil, err
	}
	connector, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

func (mysqlEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var v string
	err := c.conn.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v)
	if strings.Contains(strings.ToLower(v), "mariadb") {
		return "MariaDB " + v, err
	}
	return "MySQL " + v, err
}

func (mysqlEngine) databases(ctx context.Context, c *Conn) ([]string, error) {
	return c.queryStrings(ctx, "SHOW DATABASES")
}

func (e mysqlEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	all, err := e.databases(ctx, c)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, s := range all {
		if !mysqlSystemSchemas[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (mysqlEngine) currentSchema(ctx context.Context, c *Conn) (string, error) {
	var s sql.NullString
	err := c.conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&s)
	return s.String, err
}

func (mysqlEngine) useSchema(ctx context.Context, c *Conn, schema string) error {
	_, err := c.conn.ExecContext(ctx, "USE "+c.Dialect.QuoteIdent(schema))
	return err
}

func (mysqlEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		k := KindTable
		if typ == "VIEW" || typ == "SYSTEM VIEW" {
			k = KindView
		}
		out = append(out, Object{Schema: schema, Name: name, Kind: k})
	}
	return out, rows.Err()
}

func (mysqlEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE = 'YES',
			COLUMN_DEFAULT, COLUMN_KEY = 'PRI', COLUMN_COMMENT
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		var def sql.NullString
		if err := rows.Scan(&col.Name, &col.Type, &col.Nullable, &def, &col.PrimaryKey, &col.Comment); err != nil {
			return nil, err
		}
		if def.Valid {
			col.Default = &def.String
		}
		out = append(out, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("table %s not found", c.Dialect.QualifiedName(t))
	}
	return out, nil
}

func (mysqlEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT INDEX_NAME, MAX(NON_UNIQUE) = 0,
			GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',')
		FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		GROUP BY INDEX_NAME ORDER BY INDEX_NAME = 'PRIMARY' DESC, INDEX_NAME`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Index
	for rows.Next() {
		var ix Index
		var cols string
		if err := rows.Scan(&ix.Name, &ix.Unique, &cols); err != nil {
			return nil, err
		}
		ix.Primary = ix.Name == "PRIMARY"
		ix.Columns = strings.Split(cols, ",")
		out = append(out, ix)
	}
	return out, rows.Err()
}

func (mysqlEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	kw := "TABLE"
	if o.Kind == KindView {
		kw = "VIEW"
	}
	rows, err := c.conn.QueryContext(ctx, "SHOW CREATE "+kw+" "+c.Dialect.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name}))
	if err != nil {
		return "", err
	}
	rs, _, err := scanRows(rows, 1)
	if err != nil {
		return "", err
	}
	if len(rs.Rows) == 0 || len(rs.Rows[0]) < 2 {
		return "", fmt.Errorf("no definition for %s", o.Name)
	}
	return rs.Rows[0][1].Text + ";", nil
}

func (mysqlEngine) foreignKeys(ctx context.Context, c *Conn, t TableRef) ([]ForeignRef, error) {
	return scanForeignKeys(c.conn.QueryContext(ctx, `SELECT CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY CONSTRAINT_NAME, ORDINAL_POSITION`, t.Schema, t.Name))
}
