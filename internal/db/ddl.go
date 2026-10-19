package db

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gotable/internal/model"
)

// DefaultKind says how ColumnDef.Default is interpreted.
type DefaultKind string

const (
	DefaultNone       DefaultKind = ""           // no DEFAULT clause
	DefaultValue      DefaultKind = "value"      // quoted as a literal
	DefaultExpression DefaultKind = "expression" // inserted as SQL, e.g. now()
	DefaultNull       DefaultKind = "null"       // DEFAULT NULL
)

// ColumnDef describes a column to add to a table.
type ColumnDef struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Nullable    bool        `json:"nullable"`
	DefaultKind DefaultKind `json:"defaultKind"`
	Default     string      `json:"default"`
	Comment     string      `json:"comment"`
	// Collation sorts and compares text, e.g. utf8mb4_bin, "C" or NOCASE.
	Collation string `json:"collation"`
	// OnUpdateNow sets the column to the current time on every UPDATE
	// (MySQL/MariaDB DATETIME and TIMESTAMP).
	OnUpdateNow bool `json:"onUpdateNow"`
	// Position is "" (end), "first", or the name of the column to add after.
	// Only MySQL and MariaDB support it.
	Position string `json:"position"`
}

// Type names: words, digits and spaces, with an optional (precision, scale)
// or ('enum', 'values') list, and [] array suffixes.
var typePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_ ]*(\(\s*(\d+\s*(,\s*\d+\s*)?|'[^']*'(\s*,\s*'[^']*')*\s*)\))?( ?[A-Za-z][A-Za-z0-9_ ]*)*(\[\])*$`)

func (c ColumnDef) validate(d Dialect) error { return c.check(d, false) }

// check validates a column for ALTER TABLE … ADD COLUMN, or for CREATE
// TABLE (inCreate), where SQLite's ADD COLUMN limits don't apply.
func (c ColumnDef) check(d Dialect, inCreate bool) error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("column name is required")
	}
	t := strings.TrimSpace(c.Type)
	if t == "" {
		return errors.New("column type is required")
	}
	if !typePattern.MatchString(t) {
		return fmt.Errorf("%q doesn't look like a column type (e.g. text, varchar(255), numeric(10,2))", t)
	}
	switch c.DefaultKind {
	case DefaultNone, DefaultValue, DefaultExpression, DefaultNull:
	default:
		return fmt.Errorf("unknown default kind %q", c.DefaultKind)
	}
	if c.DefaultKind == DefaultExpression && strings.TrimSpace(c.Default) == "" {
		return errors.New("enter the default expression, e.g. now() or CURRENT_TIMESTAMP")
	}
	if c.DefaultKind == DefaultExpression && strings.ContainsAny(c.Default, ";") {
		return errors.New("the default expression can't contain ';'")
	}
	if !c.Nullable && c.DefaultKind == DefaultNull {
		return errors.New("a NOT NULL column can't default to NULL")
	}
	if d.kind == model.DriverSQLite && !inCreate {
		if !c.Nullable && (c.DefaultKind == DefaultNone) {
			return errors.New("SQLite can only add a NOT NULL column if it has a default value; set a default or allow NULL")
		}
		if c.DefaultKind == DefaultExpression && !sqliteConstantDefault(c.Default) {
			return errors.New("SQLite only allows a constant default when adding a column, such as 0 or 'x' (not CURRENT_TIMESTAMP or a (bracketed) expression)")
		}
	}
	if c.Position != "" && !d.mysqlLike() {
		return errors.New("column position can only be chosen on MySQL and MariaDB")
	}
	if c.Collation != "" && !collationPattern.MatchString(c.Collation) {
		return fmt.Errorf("%q isn't a collation name", c.Collation)
	}
	if c.OnUpdateNow {
		if !d.mysqlLike() {
			return errors.New("ON UPDATE CURRENT_TIMESTAMP is only available on MySQL and MariaDB")
		}
		if !timeType.MatchString(t) {
			return errors.New("ON UPDATE CURRENT_TIMESTAMP needs a DATETIME or TIMESTAMP column")
		}
	}
	return nil
}

var (
	collationPattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)
	timeType         = regexp.MustCompile(`(?i)^(datetime|timestamp)(\s*\(\s*\d\s*\))?$`)
)

// columnSQL renders `name type [COLLATE] [NOT NULL] [DEFAULT] [ON UPDATE]`
// plus extra (e.g. AUTO_INCREMENT) and, on MySQL, the comment.
func columnSQL(d Dialect, c ColumnDef, notNull bool, extra string) string {
	var b strings.Builder
	b.WriteString(d.QuoteIdent(strings.TrimSpace(c.Name)) + " " + strings.TrimSpace(c.Type))
	if cl := strings.TrimSpace(c.Collation); cl != "" {
		if d.kind == model.DriverPostgres {
			cl = d.QuoteIdent(cl)
		}
		b.WriteString(" COLLATE " + cl)
	}
	if notNull {
		b.WriteString(" NOT NULL")
	}
	switch c.DefaultKind {
	case DefaultValue:
		b.WriteString(" DEFAULT " + d.QuoteLiteral(Text(c.Default)))
	case DefaultExpression:
		e := strings.TrimSpace(c.Default)
		if d.kind == model.DriverSQLite && !sqliteLiteral.MatchString(e) && !strings.HasPrefix(e, "(") {
			e = "(" + e + ")" // SQLite wants expressions in brackets
		}
		b.WriteString(" DEFAULT " + e)
	case DefaultNull:
		b.WriteString(" DEFAULT NULL")
	}
	if c.OnUpdateNow {
		b.WriteString(" ON UPDATE CURRENT_TIMESTAMP")
	}
	b.WriteString(extra)
	if cm := strings.TrimSpace(c.Comment); cm != "" && d.mysqlLike() {
		b.WriteString(" COMMENT " + d.QuoteLiteral(Text(cm)))
	}
	return b.String()
}

// sqliteConstantDefault reports whether an expression is a literal SQLite
// accepts as an ALTER TABLE ADD COLUMN default.
func sqliteConstantDefault(e string) bool {
	e = strings.TrimSpace(e)
	if strings.HasPrefix(e, "(") || strings.Contains(strings.ToUpper(e), "CURRENT_") {
		return false
	}
	return true
}

// BuildAddColumn returns the statements that add a column: ALTER TABLE …
// ADD COLUMN, plus COMMENT ON COLUMN for PostgreSQL.
func BuildAddColumn(d Dialect, t TableRef, c ColumnDef) ([]string, error) {
	if err := c.validate(d); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("ALTER TABLE " + d.QualifiedName(t) + " ADD COLUMN " + columnSQL(d, c, !c.Nullable, ""))
	comment := strings.TrimSpace(c.Comment)
	switch {
	case c.Position == "first":
		b.WriteString(" FIRST")
	case c.Position != "":
		b.WriteString(" AFTER " + d.QuoteIdent(c.Position))
	}
	out := []string{b.String() + ";"}
	if comment != "" {
		switch d.kind {
		case model.DriverPostgres:
			out = append(out, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s;", d.QualifiedName(t), d.QuoteIdent(strings.TrimSpace(c.Name)), d.QuoteLiteral(Text(comment))))
		case model.DriverSQLite:
			return nil, errors.New("SQLite doesn't support column comments")
		}
	}
	return out, nil
}

// AddColumn adds a column. On PostgreSQL and SQLite the statements run in a
// transaction; MySQL commits DDL implicitly.
func (c *Conn) AddColumn(ctx context.Context, t TableRef, def ColumnDef) ([]string, error) {
	stmts, err := BuildAddColumn(c.Dialect, t, def)
	if err != nil {
		return nil, err
	}
	return stmts, c.runDDL(ctx, stmts)
}

// runDDL runs statements in one transaction (PostgreSQL and SQLite roll back
// on failure; MySQL commits DDL implicitly).
func (c *Conn) runDDL(ctx context.Context, stmts []string) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	tx, err := c.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s %w", s, err)
		}
	}
	return tx.Commit()
}

// BuildDrop returns the statements that drop tables and views: views first
// (they may depend on the tables), then tables. With cascade, objects that
// depend on them (views, foreign keys) go too.
func BuildDrop(d Dialect, objs []Object, cascade bool) ([]string, error) {
	if len(objs) == 0 {
		return nil, errors.New("choose at least one table")
	}
	var views, tables []string
	for _, o := range objs {
		if strings.TrimSpace(o.Name) == "" {
			return nil, errors.New("empty table name")
		}
		n := d.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name})
		if o.Kind == KindView {
			views = append(views, n)
		} else {
			tables = append(tables, n)
		}
	}
	var out []string
	switch d.kind {
	case model.DriverPostgres:
		suffix := ";"
		if cascade {
			suffix = " CASCADE;"
		}
		if len(views) > 0 {
			out = append(out, "DROP VIEW "+strings.Join(views, ", ")+suffix)
		}
		if len(tables) > 0 {
			out = append(out, "DROP TABLE "+strings.Join(tables, ", ")+suffix)
		}
	case model.DriverMySQL, model.DriverMariaDB:
		// One statement drops tables that reference each other; CASCADE is
		// done by turning foreign key checks off for the session.
		if cascade {
			out = append(out, "SET FOREIGN_KEY_CHECKS = 0;")
		}
		if len(views) > 0 {
			out = append(out, "DROP VIEW "+strings.Join(views, ", ")+";")
		}
		if len(tables) > 0 {
			out = append(out, "DROP TABLE "+strings.Join(tables, ", ")+";")
		}
		if cascade {
			out = append(out, "SET FOREIGN_KEY_CHECKS = 1;")
		}
	default:
		if cascade {
			out = append(out, "PRAGMA foreign_keys = OFF;")
		}
		for _, v := range views {
			out = append(out, "DROP VIEW "+v+";")
		}
		for _, t := range tables {
			out = append(out, "DROP TABLE "+t+";")
		}
		if cascade {
			out = append(out, "PRAGMA foreign_keys = ON;")
		}
	}
	return out, nil
}

// Drop drops tables and views. PostgreSQL and SQLite do it in one
// transaction (all or nothing); MySQL commits DDL immediately.
func (c *Conn) Drop(ctx context.Context, objs []Object, cascade bool) ([]string, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	if c.Config.Driver != model.DriverPostgres {
		// PostgreSQL resolves dependencies within one DROP statement; the
		// others drop in the order given, so children must come first.
		objs = c.dropOrder(ctx, objs)
	}
	stmts, err := BuildDrop(c.Dialect, objs, cascade)
	if err != nil {
		return nil, err
	}
	// PRAGMA foreign_keys only takes effect outside a transaction.
	var pre, post []string
	body := stmts
	if c.Config.Driver == model.DriverSQLite && cascade {
		pre, post, body = stmts[:1], stmts[len(stmts)-1:], stmts[1:len(stmts)-1]
	}
	for _, s := range pre {
		if _, err := c.conn.ExecContext(ctx, s); err != nil {
			return nil, fmt.Errorf("%s %w", s, err)
		}
	}
	defer func() {
		for _, s := range post {
			c.conn.ExecContext(context.WithoutCancel(ctx), s)
		}
	}()
	if c.Dialect.mysqlLike() {
		for _, s := range body {
			if _, err := c.conn.ExecContext(ctx, s); err != nil {
				if strings.HasPrefix(body[0], "SET FOREIGN_KEY_CHECKS = 0") {
					c.conn.ExecContext(context.WithoutCancel(ctx), "SET FOREIGN_KEY_CHECKS = 1")
				}
				return nil, fmt.Errorf("%s %w", s, err)
			}
		}
		return stmts, nil
	}
	tx, err := c.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, s := range body {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("%s %w", s, err)
		}
	}
	return stmts, tx.Commit()
}

// dropOrder puts tables that reference others (children) before the
// tables they reference, so dropping a related set doesn't trip foreign
// keys. Callers hold c.mu.
func (c *Conn) dropOrder(ctx context.Context, objs []Object) []Object {
	in := map[string]bool{}
	for _, o := range objs {
		in[o.Name] = true
	}
	refs := map[string][]string{} // child -> parents within the set
	for _, o := range objs {
		var parents []string
		var err error
		if c.Config.Driver == model.DriverSQLite {
			parents, err = c.queryStrings(ctx, "SELECT DISTINCT \"table\" FROM pragma_foreign_key_list(?, ?)", o.Name, schemaOrMain(o.Schema))
		} else {
			parents, err = c.queryStrings(ctx, `SELECT DISTINCT REFERENCED_TABLE_NAME FROM information_schema.KEY_COLUMN_USAGE
				WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL`, o.Schema, o.Name)
		}
		if err != nil {
			continue
		}
		for _, p := range parents {
			if in[p] && p != o.Name {
				refs[o.Name] = append(refs[o.Name], p)
			}
		}
	}
	// Depth-first: emit a table after every table that references it.
	byName := map[string]Object{}
	for _, o := range objs {
		byName[o.Name] = o
	}
	children := map[string][]string{}
	for child, parents := range refs {
		for _, p := range parents {
			children[p] = append(children[p], child)
		}
	}
	var out []Object
	seen := map[string]bool{}
	var visit func(string)
	visit = func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		for _, ch := range children[n] {
			visit(ch)
		}
		out = append(out, byName[n])
	}
	for _, o := range objs {
		visit(o.Name)
	}
	return out
}
