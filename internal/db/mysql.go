package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"os"
	"regexp"
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
			COLUMN_DEFAULT, COLUMN_KEY = 'PRI', COLUMN_COMMENT, COALESCE(COLLATION_NAME, ''), EXTRA
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
		var extra string
		if err := rows.Scan(&col.Name, &col.Type, &col.Nullable, &def, &col.PrimaryKey, &col.Comment, &col.Collation, &extra); err != nil {
			return nil, err
		}
		if def.Valid {
			col.Default = &def.String
		}
		col.Values = enumValues(col.Type)
		lower := strings.ToLower(extra)
		col.AutoIncrement = strings.Contains(lower, "auto_increment")
		col.OnUpdateNow = strings.Contains(lower, "on update")
		col.DefaultKind, col.DefaultText = mysqlDefault(def, lower, c.Config.Driver == model.DriverMariaDB)
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

var (
	timeFunction = regexp.MustCompile(`(?i)^(current_timestamp|current_date|current_time|now|localtime|localtimestamp)\b`)
	sqlFunction  = regexp.MustCompile(`(?i)^[a-z_]+\(.*\)$`)
)

// mysqlDefault reads COLUMN_DEFAULT. MySQL 8 shows literals unquoted and
// marks expressions DEFAULT_GENERATED; MariaDB quotes literals, shows
// expressions as they are and a NULL default as NULL.
func mysqlDefault(def sql.NullString, extra string, mariadb bool) (DefaultKind, string) {
	v := def.String
	generated := strings.Contains(extra, "default_generated")
	switch {
	case !def.Valid || (v == "NULL" && !generated):
		return DefaultNone, ""
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		return DefaultValue, unquoteSQL(v, true)
	case generated:
		// Expressions other than CURRENT_TIMESTAMP and friends need brackets.
		if !timeFunction.MatchString(v) && !strings.HasPrefix(v, "(") {
			v = "(" + v + ")"
		}
		return DefaultExpression, v
	case mariadb || timeFunction.MatchString(v) || sqlFunction.MatchString(v):
		return DefaultExpression, v
	}
	return DefaultValue, v
}

// unquoteSQL turns 'it”s' into it's; with backslash (MySQL), 'it\'s' too.
func unquoteSQL(s string, backslash bool) string {
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case backslash && s[i] == '\\' && i+1 < len(s):
			i++
		case s[i] == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// enumValues returns the choices of an enum('a','b') or set(…) column
// type, or nil for other types.
func enumValues(colType string) []string {
	lower := strings.ToLower(colType)
	if !(strings.HasPrefix(lower, "enum(") || strings.HasPrefix(lower, "set(")) || !strings.HasSuffix(colType, ")") {
		return nil
	}
	list := colType[strings.IndexByte(colType, '(')+1 : len(colType)-1]
	var out []string
	for i := 0; i < len(list); i++ {
		if list[i] != '\'' {
			continue
		}
		var b strings.Builder
		for i++; i < len(list); i++ {
			if list[i] == '\'' {
				if i+1 < len(list) && list[i+1] == '\'' { // '' is a quote
					b.WriteByte('\'')
					i++
					continue
				}
				break
			}
			if list[i] == '\\' && i+1 < len(list) {
				i++
			}
			b.WriteByte(list[i])
		}
		out = append(out, b.String())
	}
	return out
}

func (mysqlEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT INDEX_NAME, MAX(NON_UNIQUE) = 0,
			GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ','), LOWER(MAX(INDEX_TYPE))
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
		if err := rows.Scan(&ix.Name, &ix.Unique, &cols, &ix.Type); err != nil {
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
	return scanForeignKeys(c.conn.QueryContext(ctx, `SELECT k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
			r.DELETE_RULE, r.UPDATE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k
		JOIN information_schema.REFERENTIAL_CONSTRAINTS r
			ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.TABLE_NAME = k.TABLE_NAME
		WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, t.Schema, t.Name))
}

func (mysqlEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	var ti TableInfo
	var engine, collation sql.NullString
	var next sql.NullInt64
	err := c.conn.QueryRowContext(ctx, `SELECT TABLE_COMMENT, ENGINE, TABLE_COLLATION, AUTO_INCREMENT
		FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, t.Schema, t.Name).Scan(&ti.Comment, &engine, &collation, &next)
	if err != nil {
		return ti, err
	}
	ti.Engine, ti.Collation, ti.AutoIncrement = engine.String, collation.String, next.Int64
	// CHECK constraints: MySQL 8.0.16+ and MariaDB 10.2+. MariaDB allows
	// the same name on different tables, so match the table there too.
	match := ""
	if c.Config.Driver == model.DriverMariaDB {
		match = " AND cc.TABLE_NAME = tc.TABLE_NAME"
	}
	rows, err := c.conn.QueryContext(ctx, `SELECT cc.CONSTRAINT_NAME, cc.CHECK_CLAUSE
		FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.CHECK_CONSTRAINTS cc ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME`+match+`
		WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ? AND tc.CONSTRAINT_TYPE = 'CHECK'
		ORDER BY cc.CONSTRAINT_NAME`, t.Schema, t.Name)
	if err != nil {
		return ti, nil // older servers have no CHECK constraints
	}
	defer rows.Close()
	for rows.Next() {
		var ck Check
		if err := rows.Scan(&ck.Name, &ck.Expression); err != nil {
			return ti, err
		}
		ti.Checks = append(ti.Checks, ck)
	}
	return ti, rows.Err()
}
