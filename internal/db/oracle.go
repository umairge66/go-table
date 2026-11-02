package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	go_ora "github.com/sijms/go-ora/v2"

	"gotable/internal/model"
)

// oracleEngine is Oracle Database (12c and later) through go-ora, a pure-Go
// driver: no Oracle client libraries needed. A schema is a user (an owner
// of objects); the "database" setting is the service name, e.g. FREEPDB1.
// Names written without quotes are stored in upper case.
type oracleEngine struct{}

func init() {
	openers[model.DriverOracle] = func(cfg *model.Connection, addr, password string) (*sql.DB, engine, error) {
		db, err := sql.Open("oracle", oracleDSN(cfg, addr, password))
		return db, oracleEngine{}, err
	}
}

func oracleDSN(cfg *model.Connection, addr, password string) string {
	host, portText, _ := strings.Cut(addr, ":")
	port, _ := strconv.Atoi(portText)
	opts := map[string]string{"CONNECTION TIMEOUT": "15"}
	switch cfg.SSLMode {
	case "require":
		opts["SSL"] = "true"
	case "skip-verify":
		opts["SSL"] = "true"
		opts["SSL VERIFY"] = "false"
	}
	service := cfg.DBName
	if service == "" {
		service = "FREEPDB1" // Oracle Database Free's pluggable database
	}
	return go_ora.BuildUrl(host, port, service, cfg.User, password, opts)
}

// initSession runs on every new session: dates and timestamps read and
// written as text use ISO formats, so values typed in the grid work.
func (oracleEngine) initSession(ctx context.Context, c *Conn) error {
	for _, s := range []string{
		"ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY-MM-DD HH24:MI:SS'",
		"ALTER SESSION SET NLS_TIMESTAMP_FORMAT = 'YYYY-MM-DD HH24:MI:SS.FF'",
		"ALTER SESSION SET NLS_TIMESTAMP_TZ_FORMAT = 'YYYY-MM-DD HH24:MI:SS.FF TZH:TZM'",
		"ALTER SESSION SET NLS_NUMERIC_CHARACTERS = '.,'",
	} {
		if _, err := c.conn.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (oracleEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var product, version string
	err := c.conn.QueryRowContext(ctx, `SELECT product, version FROM product_component_version
		WHERE product LIKE 'Oracle%' FETCH FIRST 1 ROWS ONLY`).Scan(&product, &version)
	if err != nil {
		return "", err
	}
	// "Oracle Database 23ai Free" → "Oracle 23ai Free 23.0.0.0.0"
	name := strings.TrimSpace(strings.Replace(product, "Oracle Database", "Oracle", 1))
	return name + " " + version, nil
}

// databases is the pluggable database (or service) the session is in;
// other ones are separate connections.
func (oracleEngine) databases(ctx context.Context, c *Conn) ([]string, error) {
	return c.queryStrings(ctx, "SELECT SYS_CONTEXT('USERENV', 'CON_NAME') FROM dual")
}

// schemas: users that aren't Oracle's own, and the current one.
func (oracleEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	return c.queryStrings(ctx, `SELECT username FROM all_users WHERE oracle_maintained = 'N'
		UNION SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual ORDER BY 1`)
}

func (oracleEngine) currentSchema(ctx context.Context, c *Conn) (string, error) {
	var s string
	err := c.conn.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual").Scan(&s)
	return s, err
}

func (oracleEngine) useSchema(ctx context.Context, c *Conn, schema string) error {
	_, err := c.conn.ExecContext(ctx, "ALTER SESSION SET CURRENT_SCHEMA = "+c.Dialect.QuoteIdent(schema))
	return err
}

func (oracleEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT table_name, 'table' FROM all_tables
			WHERE owner = :1 AND dropped = 'NO' AND nested = 'NO' AND secondary = 'N' AND (iot_type IS NULL OR iot_type = 'IOT')
		UNION ALL SELECT view_name, 'view' FROM all_views WHERE owner = :2
		ORDER BY 1`, schema, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		o := Object{Schema: schema}
		if err := rows.Scan(&o.Name, &o.Kind); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// oracleType spells a column type as it is declared: VARCHAR2(50),
// NUMBER(10,2), INTEGER (NUMBER(*,0)), TIMESTAMP(6)…
func oracleType(typ string, length, charLength int, prec, scale sql.NullInt64) string {
	switch typ {
	case "VARCHAR2", "NVARCHAR2", "CHAR", "NCHAR":
		n := charLength
		if n == 0 {
			n = length
		}
		return fmt.Sprintf("%s(%d)", typ, n)
	case "RAW":
		return fmt.Sprintf("RAW(%d)", length)
	case "NUMBER":
		switch {
		case !prec.Valid && !scale.Valid:
			return "NUMBER"
		case !prec.Valid && scale.Int64 == 0:
			return "INTEGER"
		case scale.Int64 == 0:
			return fmt.Sprintf("NUMBER(%d)", prec.Int64)
		}
		return fmt.Sprintf("NUMBER(%d,%d)", prec.Int64, scale.Int64)
	case "FLOAT":
		if prec.Valid && prec.Int64 != 126 {
			return fmt.Sprintf("FLOAT(%d)", prec.Int64)
		}
	}
	return typ
}

// oracleTable filters the all_* views to one table (:1 owner, :2 name).
const oracleTable = "owner = :1 AND table_name = :2"

func (oracleEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	// Three plain queries joined here: correlated subqueries on the
	// all_* views made this take half a second per table.
	pk, err := c.queryStrings(ctx, `SELECT kc.column_name FROM all_constraints k
		JOIN all_cons_columns kc ON kc.owner = k.owner AND kc.constraint_name = k.constraint_name
		WHERE k.owner = :1 AND k.table_name = :2 AND k.constraint_type = 'P'`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	comments := map[string]string{}
	crows, err := c.conn.QueryContext(ctx, "SELECT column_name, comments FROM all_col_comments WHERE "+oracleTable+" AND comments IS NOT NULL", t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var name, comment string
		if err := crows.Scan(&name, &comment); err != nil {
			crows.Close()
			return nil, err
		}
		comments[name] = comment
	}
	crows.Close()
	rows, err := c.conn.QueryContext(ctx, `SELECT column_name, data_type, data_length, char_length, data_precision, data_scale,
			nullable, data_default, identity_column
		FROM all_tab_columns WHERE `+oracleTable+` ORDER BY column_id`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		var typ, nullable, identity string
		var length, charLength int
		var prec, scale sql.NullInt64
		var def sql.NullString
		if err := rows.Scan(&col.Name, &typ, &length, &charLength, &prec, &scale, &nullable, &def, &identity); err != nil {
			return nil, err
		}
		col.Type = oracleType(typ, length, charLength, prec, scale)
		col.Nullable = nullable == "Y"
		col.PrimaryKey = slices.Contains(pk, col.Name)
		col.AutoIncrement = identity == "YES"
		col.Comment = comments[col.Name]
		if d := strings.TrimSpace(def.String); def.Valid && d != "" && !col.AutoIncrement && !strings.EqualFold(d, "NULL") {
			col.Default = &d
			col.DefaultKind, col.DefaultText = mssqlDefault(d) // 'text', 12, SYSDATE
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

func (oracleEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT i.index_name, i.uniqueness, LOWER(i.index_type), ic.column_name,
			NVL((SELECT k.constraint_type FROM all_constraints k WHERE k.owner = i.table_owner AND k.table_name = i.table_name
				AND k.index_name = i.index_name AND k.constraint_type IN ('P', 'U') FETCH FIRST 1 ROWS ONLY), '-')
		FROM all_indexes i JOIN all_ind_columns ic ON ic.index_owner = i.owner AND ic.index_name = i.index_name
		WHERE i.table_owner = :1 AND i.table_name = :2 AND i.index_type NOT LIKE 'LOB%'
		ORDER BY CASE WHEN i.index_name IN (SELECT k.index_name FROM all_constraints k WHERE k.owner = i.table_owner
			AND k.table_name = i.table_name AND k.constraint_type = 'P') THEN 0 ELSE 1 END, i.index_name, ic.column_position`, t.Schema, t.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Index
	for rows.Next() {
		var x Index
		var uniq, col, kind string
		if err := rows.Scan(&x.Name, &uniq, &x.Type, &col, &kind); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].Name == x.Name {
			out[n-1].Columns = append(out[n-1].Columns, col)
			continue
		}
		x.Unique = uniq == "UNIQUE"
		x.Primary = kind == "P"
		x.Constraint = kind != "-"
		x.Columns = []string{col}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (oracleEngine) foreignKeys(ctx context.Context, c *Conn, t TableRef) ([]ForeignRef, error) {
	// Oracle has ON DELETE CASCADE / SET NULL and no ON UPDATE actions.
	return scanForeignKeys(c.conn.QueryContext(ctx, `SELECT k.constraint_name, fc.column_name, r.owner, r.table_name, rc.column_name,
			k.delete_rule, 'NO ACTION'
		FROM all_constraints k
		JOIN all_cons_columns fc ON fc.owner = k.owner AND fc.constraint_name = k.constraint_name
		JOIN all_constraints r ON r.owner = k.r_owner AND r.constraint_name = k.r_constraint_name
		JOIN all_cons_columns rc ON rc.owner = r.owner AND rc.constraint_name = r.constraint_name AND rc.position = fc.position
		WHERE k.owner = :1 AND k.table_name = :2 AND k.constraint_type = 'R'
		ORDER BY k.constraint_name, fc.position`, t.Schema, t.Name))
}

// notNullCheck is the CHECK Oracle makes for a NOT NULL column.
var notNullCheck = regexp.MustCompile(`^"[^"]+" IS NOT NULL$`)

func (oracleEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	var ti TableInfo
	var comment sql.NullString
	err := c.conn.QueryRowContext(ctx, "SELECT comments FROM all_tab_comments WHERE "+oracleTable, t.Schema, t.Name).Scan(&comment)
	if err != nil && err != sql.ErrNoRows {
		return ti, err
	}
	ti.Comment = comment.String
	rows, err := c.conn.QueryContext(ctx, `SELECT constraint_name, search_condition_vc, generated FROM all_constraints
		WHERE `+oracleTable+` AND constraint_type = 'C' ORDER BY constraint_name`, t.Schema, t.Name)
	if err != nil {
		return ti, err
	}
	defer rows.Close()
	for rows.Next() {
		var ck Check
		var generated string
		if err := rows.Scan(&ck.Name, &ck.Expression, &generated); err != nil {
			return ti, err
		}
		if generated != "USER NAME" && notNullCheck.MatchString(strings.TrimSpace(ck.Expression)) {
			continue // the column's NOT NULL
		}
		ti.Checks = append(ti.Checks, ck)
	}
	return ti, rows.Err()
}

func (oracleEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	// Leave out storage clauses, which only matter to a DBA.
	c.conn.ExecContext(ctx, `BEGIN
		DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SEGMENT_ATTRIBUTES', FALSE);
		DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'STORAGE', FALSE);
		DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SQLTERMINATOR', TRUE);
	END;`)
	kind := "TABLE"
	if o.Kind == KindView {
		kind = "VIEW"
	}
	var ddl string
	if err := c.conn.QueryRowContext(ctx, "SELECT DBMS_METADATA.GET_DDL(:1, :2, :3) FROM dual", kind, o.Name, o.Schema).Scan(&ddl); err != nil {
		return "", err
	}
	return strings.TrimSpace(ddl), nil
}
