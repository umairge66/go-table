package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	_ "github.com/microsoft/go-mssqldb" // registers the "sqlserver" driver

	"gotable/internal/model"
)

// mssqlEngine is Microsoft SQL Server (and Azure SQL). A "schema" here is a
// schema of the connected database (dbo, …); other databases open as their
// own connection, like PostgreSQL's.
type mssqlEngine struct{}

// mssqlDSN builds the go-mssqldb URL. The SSL modes map to its encrypt
// setting: optional encrypts the login only and, like SQL Server's own
// tools, accepts the server's self-signed certificate for it.
func mssqlDSN(cfg *model.Connection, addr, password string) string {
	u := url.URL{Scheme: "sqlserver", Host: addr, User: url.UserPassword(cfg.User, password)}
	q := url.Values{}
	if cfg.DBName != "" {
		q.Set("database", cfg.DBName)
	}
	switch cfg.SSLMode {
	case "disable":
		q.Set("encrypt", "disable")
	case "required":
		q.Set("encrypt", "true")
	case "trust":
		q.Set("encrypt", "true")
		q.Set("TrustServerCertificate", "true")
	case "strict":
		q.Set("encrypt", "strict")
	default: // optional
		q.Set("encrypt", "false")
		q.Set("TrustServerCertificate", "true")
	}
	if cfg.SSLCAPath != "" {
		q.Set("certificate", cfg.SSLCAPath)
	}
	q.Set("app name", "GoTable")
	q.Set("dial timeout", "15")
	u.RawQuery = q.Encode()
	return u.String()
}

func openMSSQL(cfg *model.Connection, addr, password string) (*sql.DB, error) {
	return sql.Open("sqlserver", mssqlDSN(cfg, addr, password))
}

var mssqlYear = regexp.MustCompile(`SQL (Server \d{4}|Azure)`)

func (mssqlEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var full, ver, edition string
	err := c.conn.QueryRowContext(ctx, `SELECT @@VERSION, CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)),
		CAST(SERVERPROPERTY('Edition') AS nvarchar(128))`).Scan(&full, &ver, &edition)
	if err != nil {
		return "", err
	}
	name := "SQL Server"
	if m := mssqlYear.FindStringSubmatch(full); m != nil {
		name = "SQL " + m[1] // SQL Server 2022, SQL Azure
	}
	return fmt.Sprintf("%s %s (%s)", name, ver, strings.TrimSuffix(edition, " (64-bit)")), nil
}

func (mssqlEngine) databases(ctx context.Context, c *Conn) ([]string, error) {
	return c.queryStrings(ctx, "SELECT name FROM sys.databases WHERE HAS_DBACCESS(name) = 1 ORDER BY name")
}

func (mssqlEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	// Below 16384: schemas, not the fixed database roles.
	return c.queryStrings(ctx, `SELECT name FROM sys.schemas
		WHERE schema_id < 16384 AND name NOT IN ('sys', 'INFORMATION_SCHEMA', 'guest') ORDER BY name`)
}

// SQL Server has no per-session schema switch: unqualified names resolve to
// the user's default schema. The chosen schema is GoTable's own.
func (mssqlEngine) currentSchema(ctx context.Context, c *Conn) (string, error) {
	if c.schema != "" {
		return c.schema, nil
	}
	var s string
	err := c.conn.QueryRowContext(ctx, "SELECT SCHEMA_NAME()").Scan(&s)
	return s, err
}

func (mssqlEngine) useSchema(ctx context.Context, c *Conn, schema string) error {
	var n int
	if err := c.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.schemas WHERE name = @p1", schema).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("schema %q not found", schema)
	}
	c.schema = schema
	return nil
}

func (mssqlEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT o.name, RTRIM(o.type) FROM sys.objects o
		JOIN sys.schemas s ON s.schema_id = o.schema_id
		WHERE s.name = @p1 AND o.type IN ('U', 'V') AND o.is_ms_shipped = 0 ORDER BY o.name`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		o := Object{Schema: schema, Kind: KindTable}
		var typ string
		if err := rows.Scan(&o.Name, &typ); err != nil {
			return nil, err
		}
		if typ == "V" {
			o.Kind = KindView
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// mssqlObjectID is the OBJECT_ID() argument for schema.name (@p1, @p2).
const mssqlObjectID = "OBJECT_ID(QUOTENAME(@p1) + '.' + QUOTENAME(@p2))"

// mssqlType spells a column type the way it is declared: nvarchar(50),
// varchar(max), decimal(10,2), datetime2(3).
func mssqlType(name string, maxLen, prec, scale int) string {
	switch strings.ToLower(name) {
	case "varchar", "char", "varbinary", "binary", "nvarchar", "nchar":
		if maxLen == -1 {
			return name + "(max)"
		}
		if strings.HasPrefix(strings.ToLower(name), "n") {
			maxLen /= 2 // bytes of UTF-16
		}
		return name + "(" + strconv.Itoa(maxLen) + ")"
	case "decimal", "numeric":
		return fmt.Sprintf("%s(%d,%d)", name, prec, scale)
	case "datetime2", "datetimeoffset", "time":
		if scale != 7 {
			return fmt.Sprintf("%s(%d)", name, scale)
		}
	case "float":
		if prec != 53 {
			return fmt.Sprintf("%s(%d)", name, prec)
		}
	}
	return name
}

func (mssqlEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT c.name, TYPE_NAME(c.user_type_id), c.max_length, c.precision, c.scale,
			c.is_nullable, OBJECT_DEFINITION(c.default_object_id), ISNULL(dc.name, ''), c.is_identity,
			CASE WHEN c.collation_name <> CAST(DATABASEPROPERTYEX(DB_NAME(), 'Collation') AS sysname) THEN c.collation_name ELSE '' END,
			CASE WHEN EXISTS (SELECT 1 FROM sys.index_columns ic JOIN sys.indexes i ON i.object_id = ic.object_id AND i.index_id = ic.index_id
				WHERE i.is_primary_key = 1 AND ic.object_id = c.object_id AND ic.column_id = c.column_id) THEN 1 ELSE 0 END,
			ISNULL(CAST(ep.value AS nvarchar(4000)), '')
		FROM sys.columns c
		LEFT JOIN sys.default_constraints dc ON dc.object_id = c.default_object_id
		LEFT JOIN sys.extended_properties ep ON ep.class = 1 AND ep.major_id = c.object_id AND ep.minor_id = c.column_id AND ep.name = 'MS_Description'
		WHERE c.object_id = `+mssqlObjectID+` ORDER BY c.column_id`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		var typ string
		var maxLen, prec, scale, pk int
		var def sql.NullString
		if err := rows.Scan(&col.Name, &typ, &maxLen, &prec, &scale, &col.Nullable, &def, &col.defaultConstraint,
			&col.AutoIncrement, &col.Collation, &pk, &col.Comment); err != nil {
			return nil, err
		}
		col.Type = mssqlType(typ, maxLen, prec, scale)
		col.PrimaryKey = pk == 1
		if def.Valid {
			col.Default = &def.String
			col.DefaultKind, col.DefaultText = mssqlDefault(def.String)
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

var mssqlNumber = regexp.MustCompile(`^[+-]?\d+(\.\d+)?$`)

// mssqlDefault reads a default constraint's definition, which SQL Server
// wraps in brackets: ((0)), ('it”s'), (N'x'), (getdate()).
func mssqlDefault(def string) (DefaultKind, string) {
	e := strings.TrimSpace(def)
	for len(e) >= 2 && e[0] == '(' && e[len(e)-1] == ')' && balanced(e[1:len(e)-1]) {
		e = strings.TrimSpace(e[1 : len(e)-1])
	}
	switch {
	case strings.EqualFold(e, "NULL"):
		return DefaultNull, ""
	case mssqlNumber.MatchString(e):
		return DefaultValue, e
	case len(e) >= 2 && e[len(e)-1] == '\'' && (e[0] == '\'' || (len(e) >= 3 && (e[0] == 'N' || e[0] == 'n') && e[1] == '\'')):
		s := strings.TrimPrefix(strings.TrimPrefix(e, "N"), "n")
		if pgLiteralBody(s) {
			return DefaultValue, strings.ReplaceAll(s[1:len(s)-1], "''", "'")
		}
	}
	return DefaultExpression, e
}

// pgLiteralBody reports a single '…' literal (” escapes a quote).
func pgLiteralBody(s string) bool {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return false
	}
	body := s[1 : len(s)-1]
	return !strings.Contains(strings.ReplaceAll(body, "''", ""), "'")
}

func (mssqlEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT i.name, i.is_unique, i.is_primary_key, i.is_unique_constraint, LOWER(i.type_desc), col.name
		FROM sys.indexes i
		JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.is_included_column = 0
		JOIN sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
		WHERE i.object_id = `+mssqlObjectID+` AND i.type > 0
		ORDER BY i.is_primary_key DESC, i.name, ic.key_ordinal`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Index
	for rows.Next() {
		var x Index
		var uc bool
		var col string
		if err := rows.Scan(&x.Name, &x.Unique, &x.Primary, &uc, &x.Type, &col); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].Name == x.Name {
			out[n-1].Columns = append(out[n-1].Columns, col)
			continue
		}
		x.Constraint = x.Primary || uc
		x.Columns = []string{col}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (mssqlEngine) foreignKeys(ctx context.Context, c *Conn, t TableRef) ([]ForeignRef, error) {
	return scanForeignKeys(c.conn.QueryContext(ctx, `SELECT fk.name, pc.name, rs.name, rt.name, rc.name,
			REPLACE(fk.delete_referential_action_desc, '_', ' '), REPLACE(fk.update_referential_action_desc, '_', ' ')
		FROM sys.foreign_keys fk
		JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		JOIN sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
		JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
		JOIN sys.tables rt ON rt.object_id = fkc.referenced_object_id
		JOIN sys.schemas rs ON rs.schema_id = rt.schema_id
		WHERE fk.parent_object_id = `+mssqlObjectID+`
		ORDER BY fk.name, fkc.constraint_column_id`, t.Schema, t.Name))
}

func (mssqlEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	var ti TableInfo
	err := c.conn.QueryRowContext(ctx, `SELECT ISNULL((SELECT CAST(value AS nvarchar(4000)) FROM sys.extended_properties
		WHERE class = 1 AND major_id = `+mssqlObjectID+` AND minor_id = 0 AND name = 'MS_Description'), '')`, t.Schema, t.Name).Scan(&ti.Comment)
	if err != nil {
		return ti, err
	}
	rows, err := c.conn.QueryContext(ctx, `SELECT name, definition FROM sys.check_constraints
		WHERE parent_object_id = `+mssqlObjectID+` ORDER BY name`, t.Schema, t.Name)
	if err != nil {
		return ti, err
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

func (e mssqlEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	if o.Kind == KindView {
		var def sql.NullString
		if err := c.conn.QueryRowContext(ctx, "SELECT OBJECT_DEFINITION("+mssqlObjectID+")", o.Schema, o.Name).Scan(&def); err != nil {
			return "", err
		}
		if !def.Valid {
			return "", fmt.Errorf("no definition for %s (encrypted or no permission)", o.Name)
		}
		return strings.TrimSpace(def.String), nil
	}
	main, post, err := e.createTable(ctx, c, TableRef{Schema: o.Schema, Name: o.Name}, true)
	if err != nil {
		return "", err
	}
	return strings.Join(append([]string{main}, post...), ";\n\n") + ";", nil
}

// createTable writes CREATE TABLE from the catalog (SQL Server has no SHOW
// CREATE TABLE), plus the indexes and foreign keys to add after the data.
// Without qualify, names carry no schema (for portable dumps).
func (mssqlEngine) createTable(ctx context.Context, c *Conn, t TableRef, qualify bool) (string, []string, error) {
	d := c.Dialect
	e := mssqlEngine{}
	cols, err := e.columns(ctx, c, t)
	if err != nil {
		return "", nil, err
	}
	idx, err := e.indexes(ctx, c, t)
	if err != nil {
		return "", nil, err
	}
	fks, err := e.foreignKeys(ctx, c, t)
	if err != nil {
		return "", nil, err
	}
	info, err := e.tableInfo(ctx, c, t)
	if err != nil {
		return "", nil, err
	}
	name := d.QuoteIdent(t.Name)
	if qualify {
		name = d.QualifiedName(t)
	}
	var lines, post []string
	for _, col := range cols {
		l := d.QuoteIdent(col.Name) + " " + col.Type
		if col.Collation != "" {
			l += " COLLATE " + col.Collation
		}
		if col.AutoIncrement {
			l += " IDENTITY(1,1)"
		}
		if !col.Nullable {
			l += " NOT NULL"
		}
		if col.Default != nil {
			l += " DEFAULT " + *col.Default
		}
		lines = append(lines, l)
	}
	for _, x := range idx {
		switch {
		case x.Primary:
			lines = append(lines, "CONSTRAINT "+d.QuoteIdent(x.Name)+" PRIMARY KEY ("+quoteList(d, x.Columns)+")")
		case x.Constraint:
			lines = append(lines, "CONSTRAINT "+d.QuoteIdent(x.Name)+" UNIQUE ("+quoteList(d, x.Columns)+")")
		default:
			u := ""
			if x.Unique {
				u = "UNIQUE "
			}
			post = append(post, "CREATE "+u+"INDEX "+d.QuoteIdent(x.Name)+" ON "+name+" ("+quoteList(d, x.Columns)+")")
		}
	}
	for _, ck := range info.Checks {
		lines = append(lines, "CONSTRAINT "+d.QuoteIdent(ck.Name)+" CHECK "+ck.Expression)
	}
	for _, fk := range fks {
		ref := d.QuoteIdent(fk.Table)
		if qualify || fk.Schema != t.Schema {
			ref = d.QualifiedName(TableRef{Schema: fk.Schema, Name: fk.Table})
		}
		s := "ALTER TABLE " + name + " ADD CONSTRAINT " + d.QuoteIdent(fk.Name) + " FOREIGN KEY (" + quoteList(d, fk.From) +
			") REFERENCES " + ref + " (" + quoteList(d, fk.Columns) + ")"
		if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
			s += " ON DELETE " + fk.OnDelete
		}
		if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
			s += " ON UPDATE " + fk.OnUpdate
		}
		post = append(post, s)
	}
	return "CREATE TABLE " + name + " (\n  " + strings.Join(lines, ",\n  ") + "\n)", post, nil
}

// mssqlComment sets, changes or removes an MS_Description extended
// property (SQL Server's comments) on a table or, with col, a column.
func mssqlComment(d Dialect, t TableRef, col, old, comment string) string {
	comment = strings.TrimSpace(comment)
	if comment == old {
		return ""
	}
	proc := "sp_updateextendedproperty"
	switch {
	case comment == "":
		proc = "sp_dropextendedproperty"
	case old == "":
		proc = "sp_addextendedproperty"
	}
	s := "EXEC sys." + proc + " @name = N'MS_Description', "
	if comment != "" {
		s += "@value = " + d.QuoteLiteral(Text(comment)) + ", "
	}
	s += "@level0type = N'SCHEMA', @level0name = " + d.QuoteLiteral(Text(t.Schema)) +
		", @level1type = N'TABLE', @level1name = " + d.QuoteLiteral(Text(t.Name))
	if col != "" {
		s += ", @level2type = N'COLUMN', @level2name = " + d.QuoteLiteral(Text(col))
	}
	return s + ";"
}

// mssqlRename renames with sp_rename: kind is "" (table), COLUMN or INDEX.
func mssqlRename(d Dialect, t TableRef, object, newName, kind string) string {
	target := d.QualifiedName(t)
	if object != "" {
		target += "." + d.QuoteIdent(object)
	}
	s := "EXEC sp_rename " + d.QuoteLiteral(Text(target)) + ", " + d.QuoteLiteral(Text(newName))
	if kind != "" {
		s += ", N'" + kind + "'"
	}
	return s + ";"
}

// mssqlGUID formats a uniqueidentifier as SQL Server shows it: its first
// three groups are stored little-endian.
func mssqlGUID(b []byte) string {
	return fmt.Sprintf("%X%X%X%X-%X%X-%X%X-%X-%X", b[3], b[2], b[1], b[0], b[5], b[4], b[7], b[6], b[8:10], b[10:16])
}
