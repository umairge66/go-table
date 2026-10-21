package db

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gotable/internal/model"
)

// AlterKind names a change to a table's structure.
type AlterKind string

const (
	AlterAddColumn      AlterKind = "add_column"
	AlterModifyColumn   AlterKind = "modify_column"
	AlterDropColumn     AlterKind = "drop_column"
	AlterAddIndex       AlterKind = "add_index"
	AlterDropIndex      AlterKind = "drop_index"
	AlterRenameIndex    AlterKind = "rename_index"
	AlterSetPrimaryKey  AlterKind = "set_primary_key" // Columns; none drops the key
	AlterAddForeignKey  AlterKind = "add_foreign_key"
	AlterDropForeignKey AlterKind = "drop_foreign_key"
	AlterAddCheck       AlterKind = "add_check"
	AlterDropCheck      AlterKind = "drop_check"
	AlterRenameTable    AlterKind = "rename_table"
	AlterTableComment   AlterKind = "table_comment"
	AlterTableOptions   AlterKind = "table_options"
	AlterTruncate       AlterKind = "truncate"
)

// AlterOp is one change. Which fields apply depends on Kind.
type AlterOp struct {
	Kind AlterKind `json:"kind"`
	// Column is the existing column to modify or drop.
	Column string     `json:"column,omitempty"`
	Def    *ColumnDef `json:"def,omitempty"`
	// AutoIncrement keeps (or makes) a modified column AUTO_INCREMENT
	// (MySQL/MariaDB, which restate the whole column).
	AutoIncrement bool           `json:"autoIncrement,omitempty"`
	Index         *IndexDef      `json:"index,omitempty"`
	ForeignKey    *ForeignKeyDef `json:"foreignKey,omitempty"`
	Check         *CheckDef      `json:"check,omitempty"`
	// Name is the index, foreign key or check to drop or rename.
	Name    string        `json:"name,omitempty"`
	NewName string        `json:"newName,omitempty"`
	Columns []string      `json:"columns,omitempty"`
	Comment string        `json:"comment,omitempty"`
	Options *TableOptions `json:"options,omitempty"`
}

// IndexDef describes an index to create.
type IndexDef struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
	// Kind is "", "fulltext" or "spatial" (MySQL/MariaDB).
	Kind string `json:"kind"`
	// Method is the index method: btree or hash (MySQL/MariaDB); btree,
	// hash, gin, gist, brin or spgist (PostgreSQL).
	Method string `json:"method"`
}

// ForeignKeyDef describes a foreign key to add.
type ForeignKeyDef struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete"`
	OnUpdate   string   `json:"onUpdate"`
}

// CheckDef describes a CHECK constraint to add.
type CheckDef struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
}

// TableOptions are MySQL/MariaDB table options; empty ones stay as they are.
type TableOptions struct {
	Engine        string `json:"engine"`
	Collation     string `json:"collation"`
	AutoIncrement int64  `json:"autoIncrement"`
	// Convert also converts every text column to Collation's character set.
	Convert bool `json:"convert"`
}

// TableState is the table an alteration starts from.
type TableState struct {
	Columns     []Column
	Indexes     []Index
	ForeignKeys []ForeignRef
	Info        TableInfo
	// SQLite: what a table rebuild needs.
	SQLite *SQLiteTable
}

// AlterPlan is what runs. Statements run in one transaction (MySQL
// commits each DDL statement). A SQLite rebuild runs with foreign key
// enforcement off and checks the keys before committing (CheckTable).
type AlterPlan struct {
	Statements []string
	Rebuild    bool
	CheckTable string
}

// Preview lists every statement, including a rebuild's PRAGMAs.
func (p AlterPlan) Preview(d Dialect) []string {
	if !p.Rebuild {
		return p.Statements
	}
	out := []string{"PRAGMA foreign_keys = OFF;"}
	out = append(out, p.Statements...)
	return append(out, fmt.Sprintf("PRAGMA foreign_key_check(%s);", d.QuoteIdent(p.CheckTable)), "PRAGMA foreign_keys = ON;")
}

var (
	refActions  = []string{"", "NO ACTION", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT"}
	pgMethods   = []string{"", "btree", "hash", "gin", "gist", "brin", "spgist"}
	myMethods   = []string{"", "btree", "hash"}
	charsetOf   = regexp.MustCompile(`^([A-Za-z0-9]+)_`)
	sqliteIndex = regexp.MustCompile(`(?is)^(\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?)("(?:[^"]|"")+"|` + "`[^`]+`" + `|\[[^\]]+\]|[^\s(]+)`)
)

// BuildAlter returns the statements for ops on table t.
func BuildAlter(d Dialect, t TableRef, st TableState, ops []AlterOp) (AlterPlan, error) {
	if len(ops) == 0 {
		return AlterPlan{}, errors.New("nothing to change")
	}
	if d.kind == model.DriverSQLite {
		for _, op := range ops {
			if sqliteNeedsRebuild(st, op) {
				return buildSQLiteRebuild(d, t, st, ops)
			}
		}
	}
	var out []string
	for _, op := range ops {
		stmts, err := buildOp(d, t, st, op)
		if err != nil {
			return AlterPlan{}, err
		}
		out = append(out, stmts...)
	}
	return AlterPlan{Statements: out}, nil
}

func findColumn(st TableState, name string) (Column, bool) {
	for _, c := range st.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

func findIndex(st TableState, name string) (Index, bool) {
	for _, x := range st.Indexes {
		if x.Name == name {
			return x, true
		}
	}
	return Index{}, false
}

func primaryIndex(st TableState) (Index, bool) {
	for _, x := range st.Indexes {
		if x.Primary {
			return x, true
		}
	}
	return Index{}, false
}

// checkColumns makes sure every name is a column of the table, once.
func checkColumns(st TableState, cols []string, what string) error {
	if len(cols) == 0 {
		return fmt.Errorf("choose the %s columns", what)
	}
	for i, c := range cols {
		if _, ok := findColumn(st, c); !ok {
			return fmt.Errorf("there is no column %q", c)
		}
		if slices.Index(cols, c) != i {
			return fmt.Errorf("column %q is listed twice", c)
		}
	}
	return nil
}

func quoteList(d Dialect, names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = d.QuoteIdent(n)
	}
	return strings.Join(q, ", ")
}

func requireName(n, what string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" {
		return "", fmt.Errorf("the %s needs a name", what)
	}
	return n, nil
}

func buildOp(d Dialect, t TableRef, st TableState, op AlterOp) ([]string, error) {
	tbl := d.QualifiedName(t)
	alter := "ALTER TABLE " + tbl + " "
	switch op.Kind {
	case AlterAddColumn:
		if op.Def == nil {
			return nil, errors.New("missing column definition")
		}
		if _, ok := findColumn(st, strings.TrimSpace(op.Def.Name)); ok {
			return nil, fmt.Errorf("column %q already exists", op.Def.Name)
		}
		return BuildAddColumn(d, t, *op.Def)

	case AlterModifyColumn:
		return buildModifyColumn(d, t, st, op)

	case AlterDropColumn:
		if _, ok := findColumn(st, op.Column); !ok {
			return nil, fmt.Errorf("there is no column %q", op.Column)
		}
		if len(st.Columns) == 1 {
			return nil, errors.New("a table needs at least one column; drop the table instead")
		}
		return []string{alter + "DROP COLUMN " + d.QuoteIdent(op.Column) + ";"}, nil

	case AlterAddIndex:
		return buildAddIndex(d, t, st, op.Index)

	case AlterDropIndex:
		x, ok := findIndex(st, op.Name)
		if !ok {
			return nil, fmt.Errorf("there is no index %q", op.Name)
		}
		if x.Primary {
			return buildOp(d, t, st, AlterOp{Kind: AlterSetPrimaryKey})
		}
		switch {
		case d.mysqlLike():
			return []string{alter + "DROP INDEX " + d.QuoteIdent(x.Name) + ";"}, nil
		case x.Constraint:
			return []string{alter + "DROP CONSTRAINT " + d.QuoteIdent(x.Name) + ";"}, nil
		}
		return []string{"DROP INDEX " + d.QualifiedName(TableRef{Schema: t.Schema, Name: x.Name}) + ";"}, nil

	case AlterRenameIndex:
		x, ok := findIndex(st, op.Name)
		if !ok {
			return nil, fmt.Errorf("there is no index %q", op.Name)
		}
		n, err := requireName(op.NewName, "index")
		if err != nil {
			return nil, err
		}
		if x.Primary && d.mysqlLike() {
			return nil, errors.New("the primary key is always called PRIMARY")
		}
		switch d.kind {
		case model.DriverPostgres:
			return []string{"ALTER INDEX " + d.QualifiedName(TableRef{Schema: t.Schema, Name: x.Name}) + " RENAME TO " + d.QuoteIdent(n) + ";"}, nil
		case model.DriverSQLite:
			if x.Constraint {
				return nil, errors.New("SQLite names the index of a PRIMARY KEY or UNIQUE constraint itself; it can't be renamed")
			}
			def := st.SQLite.IndexSQL[x.Name]
			if !sqliteIndex.MatchString(def) {
				return nil, fmt.Errorf("can't read the definition of index %q", x.Name)
			}
			def = sqliteIndex.ReplaceAllLiteralString(def, sqliteIndex.FindStringSubmatch(def)[1]+d.QuoteIdent(n))
			return []string{"DROP INDEX " + d.QualifiedName(TableRef{Schema: t.Schema, Name: x.Name}) + ";", def + ";"}, nil
		}
		return []string{alter + "RENAME INDEX " + d.QuoteIdent(x.Name) + " TO " + d.QuoteIdent(n) + ";"}, nil

	case AlterSetPrimaryKey:
		pk, has := primaryIndex(st)
		if len(op.Columns) == 0 && !has {
			return nil, errors.New("the table has no primary key")
		}
		if len(op.Columns) > 0 {
			if err := checkColumns(st, op.Columns, "primary key"); err != nil {
				return nil, err
			}
		}
		var parts []string
		if has {
			if d.mysqlLike() {
				parts = append(parts, "DROP PRIMARY KEY")
			} else {
				parts = append(parts, "DROP CONSTRAINT "+d.QuoteIdent(pk.Name))
			}
		}
		if len(op.Columns) > 0 {
			parts = append(parts, "ADD PRIMARY KEY ("+quoteList(d, op.Columns)+")")
		}
		return []string{alter + strings.Join(parts, ", ") + ";"}, nil

	case AlterAddForeignKey:
		fk := op.ForeignKey
		if fk == nil {
			return nil, errors.New("missing foreign key")
		}
		if err := checkColumns(st, fk.Columns, "foreign key"); err != nil {
			return nil, err
		}
		clause, err := foreignKeyClause(d, t, *fk)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(fk.Name)
		if name == "" {
			name = indexName(t.Name, strings.Join(fk.Columns, "_"), "fkey")
		}
		return []string{alter + "ADD CONSTRAINT " + d.QuoteIdent(name) + " " + clause + ";"}, nil

	case AlterDropForeignKey:
		if !slices.ContainsFunc(st.ForeignKeys, func(f ForeignRef) bool { return f.Name == op.Name }) {
			return nil, fmt.Errorf("there is no foreign key %q", op.Name)
		}
		if d.mysqlLike() {
			return []string{alter + "DROP FOREIGN KEY " + d.QuoteIdent(op.Name) + ";"}, nil
		}
		return []string{alter + "DROP CONSTRAINT " + d.QuoteIdent(op.Name) + ";"}, nil

	case AlterAddCheck:
		ck := op.Check
		if ck == nil || strings.TrimSpace(ck.Expression) == "" {
			return nil, errors.New("enter the condition, e.g. price > 0")
		}
		if strings.Contains(ck.Expression, ";") {
			return nil, errors.New("the condition can't contain ';'")
		}
		if d.kind == model.DriverSQLite {
			return nil, errors.New("SQLite can't add a CHECK constraint to an existing table")
		}
		s := alter + "ADD "
		if n := strings.TrimSpace(ck.Name); n != "" {
			s += "CONSTRAINT " + d.QuoteIdent(n) + " "
		}
		return []string{s + "CHECK (" + strings.TrimSpace(ck.Expression) + ");"}, nil

	case AlterDropCheck:
		if !slices.ContainsFunc(st.Info.Checks, func(c Check) bool { return c.Name == op.Name }) {
			return nil, fmt.Errorf("there is no CHECK constraint %q", op.Name)
		}
		if d.kind == model.DriverMySQL {
			return []string{alter + "DROP CHECK " + d.QuoteIdent(op.Name) + ";"}, nil
		}
		return []string{alter + "DROP CONSTRAINT " + d.QuoteIdent(op.Name) + ";"}, nil

	case AlterRenameTable:
		n, err := requireName(op.NewName, "table")
		if err != nil {
			return nil, err
		}
		target := d.QuoteIdent(n)
		if d.mysqlLike() {
			target = d.QualifiedName(TableRef{Schema: t.Schema, Name: n}) // stay in the same database
		}
		return []string{alter + "RENAME TO " + target + ";"}, nil

	case AlterTableComment:
		switch d.kind {
		case model.DriverSQLite:
			return nil, errors.New("SQLite tables have no comments")
		case model.DriverPostgres:
			v := "NULL"
			if strings.TrimSpace(op.Comment) != "" {
				v = d.QuoteLiteral(Text(op.Comment))
			}
			return []string{"COMMENT ON TABLE " + tbl + " IS " + v + ";"}, nil
		}
		return []string{alter + "COMMENT = " + d.QuoteLiteral(Text(op.Comment)) + ";"}, nil

	case AlterTableOptions:
		o := op.Options
		if !d.mysqlLike() {
			return nil, errors.New("storage engine, collation and AUTO_INCREMENT are MySQL/MariaDB table options")
		}
		if o == nil {
			return nil, errors.New("missing table options")
		}
		var parts []string
		for _, v := range []string{o.Engine, o.Collation} {
			if v != "" && !optionName.MatchString(v) {
				return nil, fmt.Errorf("%q isn't a valid option value", v)
			}
		}
		if o.Engine != "" && !strings.EqualFold(o.Engine, st.Info.Engine) {
			parts = append(parts, "ENGINE = "+o.Engine)
		}
		if o.Collation != "" && (o.Convert || !strings.EqualFold(o.Collation, st.Info.Collation)) {
			cs := charsetOf.FindStringSubmatch(o.Collation)
			switch {
			case cs == nil:
				return nil, fmt.Errorf("can't tell the character set of collation %q", o.Collation)
			case o.Convert:
				parts = append(parts, "CONVERT TO CHARACTER SET "+cs[1]+" COLLATE "+o.Collation)
			default:
				parts = append(parts, "DEFAULT CHARACTER SET "+cs[1]+" COLLATE "+o.Collation)
			}
		}
		if o.AutoIncrement < 0 {
			return nil, errors.New("AUTO_INCREMENT must be a positive number")
		}
		if o.AutoIncrement > 0 && o.AutoIncrement != st.Info.AutoIncrement {
			parts = append(parts, fmt.Sprintf("AUTO_INCREMENT = %d", o.AutoIncrement))
		}
		if len(parts) == 0 {
			return nil, errors.New("nothing to change")
		}
		return []string{alter + strings.Join(parts, ", ") + ";"}, nil

	case AlterTruncate:
		if d.kind == model.DriverSQLite {
			return []string{"DELETE FROM " + tbl + ";"}, nil
		}
		return []string{"TRUNCATE TABLE " + tbl + ";"}, nil
	}
	return nil, fmt.Errorf("unknown change %q", op.Kind)
}

func foreignKeyClause(d Dialect, t TableRef, fk ForeignKeyDef) (string, error) {
	if strings.TrimSpace(fk.RefTable) == "" {
		return "", errors.New("choose the table the key refers to")
	}
	if len(fk.RefColumns) != len(fk.Columns) {
		return "", errors.New("choose a referenced column for every column of the key")
	}
	for _, c := range fk.RefColumns {
		if strings.TrimSpace(c) == "" {
			return "", errors.New("choose a referenced column for every column of the key")
		}
	}
	for _, a := range []string{fk.OnDelete, fk.OnUpdate} {
		if !slices.Contains(refActions, strings.ToUpper(a)) {
			return "", fmt.Errorf("unknown referential action %q", a)
		}
	}
	ref := TableRef{Schema: fk.RefSchema, Name: fk.RefTable}
	if ref.Schema == "" {
		ref.Schema = t.Schema
	}
	refName := d.QualifiedName(ref)
	if d.kind == model.DriverSQLite {
		if ref.Schema != t.Schema {
			return "", errors.New("SQLite foreign keys can only refer to tables in the same database")
		}
		refName = d.QuoteIdent(ref.Name)
	}
	s := "FOREIGN KEY (" + quoteList(d, fk.Columns) + ") REFERENCES " + refName + " (" + quoteList(d, fk.RefColumns) + ")"
	if a := strings.ToUpper(fk.OnDelete); a != "" && a != "NO ACTION" {
		s += " ON DELETE " + a
	}
	if a := strings.ToUpper(fk.OnUpdate); a != "" && a != "NO ACTION" {
		s += " ON UPDATE " + a
	}
	return s, nil
}

func buildAddIndex(d Dialect, t TableRef, st TableState, x *IndexDef) ([]string, error) {
	if x == nil {
		return nil, errors.New("missing index")
	}
	if err := checkColumns(st, x.Columns, "index"); err != nil {
		return nil, err
	}
	kind := strings.ToLower(x.Kind)
	method := strings.ToLower(x.Method)
	switch {
	case kind != "" && kind != "fulltext" && kind != "spatial":
		return nil, fmt.Errorf("unknown index kind %q", x.Kind)
	case kind != "" && !d.mysqlLike():
		return nil, fmt.Errorf("%s indexes are a MySQL/MariaDB feature", strings.ToUpper(kind))
	case kind != "" && x.Unique:
		return nil, fmt.Errorf("a %s index can't be unique", strings.ToUpper(kind))
	case kind != "" && method != "":
		return nil, fmt.Errorf("a %s index has no method", strings.ToUpper(kind))
	case d.kind == model.DriverPostgres && !slices.Contains(pgMethods, method):
		return nil, fmt.Errorf("unknown index method %q", x.Method)
	case d.mysqlLike() && !slices.Contains(myMethods, method):
		return nil, fmt.Errorf("unknown index method %q", x.Method)
	case d.kind == model.DriverSQLite && method != "" && method != "btree":
		return nil, errors.New("SQLite indexes are always B-trees")
	}
	name := strings.TrimSpace(x.Name)
	if name == "" {
		suffix := "idx"
		if x.Unique {
			suffix = "key"
		}
		name = indexName(t.Name, strings.Join(x.Columns, "_"), suffix)
	}
	if _, ok := findIndex(st, name); ok {
		return nil, fmt.Errorf("an index named %q already exists", name)
	}
	cols := "(" + quoteList(d, x.Columns) + ")"
	if d.mysqlLike() {
		s := "ALTER TABLE " + d.QualifiedName(t) + " ADD "
		switch {
		case x.Unique:
			s += "UNIQUE "
		case kind != "":
			s += strings.ToUpper(kind) + " "
		}
		s += "INDEX " + d.QuoteIdent(name) + " " + cols
		if method != "" {
			s += " USING " + strings.ToUpper(method)
		}
		return []string{s + ";"}, nil
	}
	s := "CREATE "
	if x.Unique {
		s += "UNIQUE "
	}
	if d.kind == model.DriverSQLite {
		// The index goes in the table's database; the table name stays bare.
		s += "INDEX " + d.QualifiedName(TableRef{Schema: t.Schema, Name: name}) + " ON " + d.QuoteIdent(t.Name)
	} else {
		s += "INDEX " + d.QuoteIdent(name) + " ON " + d.QualifiedName(t)
	}
	if method != "" && d.kind == model.DriverPostgres {
		s += " USING " + method
	}
	return []string{s + " " + cols + ";"}, nil
}

func sameType(a, b string) bool {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	return norm(a) == norm(b)
}

func buildModifyColumn(d Dialect, t TableRef, st TableState, op AlterOp) ([]string, error) {
	old, ok := findColumn(st, op.Column)
	if !ok {
		return nil, fmt.Errorf("there is no column %q", op.Column)
	}
	if op.Def == nil {
		return nil, errors.New("missing column definition")
	}
	def := *op.Def
	def.Name = strings.TrimSpace(def.Name)
	if err := def.check(d, true); err != nil {
		return nil, err
	}
	if def.Name != old.Name {
		if _, taken := findColumn(st, def.Name); taken {
			return nil, fmt.Errorf("column %q already exists", def.Name)
		}
	}
	alter := "ALTER TABLE " + d.QualifiedName(t) + " "
	switch d.kind {
	case model.DriverMySQL, model.DriverMariaDB:
		extra := ""
		if op.AutoIncrement {
			extra = " AUTO_INCREMENT"
		}
		s := alter + "CHANGE COLUMN " + d.QuoteIdent(old.Name) + " " + columnSQL(d, def, !def.Nullable, extra)
		switch {
		case def.Position == "first":
			s += " FIRST"
		case def.Position != "":
			if _, ok := findColumn(st, def.Position); !ok || def.Position == old.Name {
				return nil, fmt.Errorf("can't place the column after %q", def.Position)
			}
			s += " AFTER " + d.QuoteIdent(def.Position)
		}
		return []string{s + ";"}, nil

	case model.DriverSQLite:
		// Only a rename runs in place; anything else rebuilds the table.
		if strings.TrimSpace(def.Comment) != "" {
			return nil, errors.New("SQLite doesn't support column comments")
		}
		if def.Name == old.Name {
			return nil, errors.New("nothing to change")
		}
		return []string{alter + "RENAME COLUMN " + d.QuoteIdent(old.Name) + " TO " + d.QuoteIdent(def.Name) + ";"}, nil
	}

	// PostgreSQL: one statement per changed attribute, after a rename.
	var out []string
	name := old.Name
	if def.Name != old.Name {
		out = append(out, alter+"RENAME COLUMN "+d.QuoteIdent(old.Name)+" TO "+d.QuoteIdent(def.Name)+";")
		name = def.Name
	}
	col := d.QuoteIdent(name)
	newType := strings.TrimSpace(def.Type)
	if !sameType(newType, old.Type) || def.Collation != old.Collation {
		s := alter + "ALTER COLUMN " + col + " TYPE " + newType
		if def.Collation != "" {
			s += " COLLATE " + d.QuoteIdent(def.Collation)
		}
		if !sameType(newType, old.Type) {
			s += " USING " + col + "::" + newType
		}
		out = append(out, s+";")
	}
	if def.Nullable != old.Nullable {
		if def.Nullable {
			out = append(out, alter+"ALTER COLUMN "+col+" DROP NOT NULL;")
		} else {
			out = append(out, alter+"ALTER COLUMN "+col+" SET NOT NULL;")
		}
	}
	oldKind := old.DefaultKind
	if def.DefaultKind != oldKind || (def.DefaultKind != DefaultNone && def.Default != old.DefaultText) {
		switch def.DefaultKind {
		case DefaultNone:
			out = append(out, alter+"ALTER COLUMN "+col+" DROP DEFAULT;")
		case DefaultNull:
			out = append(out, alter+"ALTER COLUMN "+col+" SET DEFAULT NULL;")
		case DefaultValue:
			out = append(out, alter+"ALTER COLUMN "+col+" SET DEFAULT "+d.QuoteLiteral(Text(def.Default))+";")
		case DefaultExpression:
			out = append(out, alter+"ALTER COLUMN "+col+" SET DEFAULT "+strings.TrimSpace(def.Default)+";")
		}
	}
	if strings.TrimSpace(def.Comment) != old.Comment {
		v := "NULL"
		if strings.TrimSpace(def.Comment) != "" {
			v = d.QuoteLiteral(Text(strings.TrimSpace(def.Comment)))
		}
		out = append(out, "COMMENT ON COLUMN "+d.QualifiedName(t)+"."+col+" IS "+v+";")
	}
	if len(out) == 0 {
		return nil, errors.New("nothing to change")
	}
	return out, nil
}

// LoadTableState reads what BuildAlter needs. Callers hold c.mu.
func (c *Conn) loadTableState(ctx context.Context, t TableRef) (TableState, error) {
	var st TableState
	var err error
	if st.Columns, err = c.eng.columns(ctx, c, t); err != nil {
		return st, err
	}
	if st.Indexes, err = c.eng.indexes(ctx, c, t); err != nil {
		return st, err
	}
	if st.ForeignKeys, err = c.eng.foreignKeys(ctx, c, t); err != nil {
		return st, err
	}
	if st.Info, err = c.eng.tableInfo(ctx, c, t); err != nil {
		return st, err
	}
	if c.Config.Driver == model.DriverSQLite {
		sq, err := loadSQLiteTable(ctx, c, t)
		if err != nil {
			return st, err
		}
		st.SQLite = sq
	}
	return st, nil
}

// Alter previews (dryRun) or applies changes to a table's structure.
func (c *Conn) Alter(ctx context.Context, t TableRef, ops []AlterOp, dryRun bool) ([]string, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	st, err := c.loadTableState(ctx, t)
	if err != nil {
		return nil, err
	}
	plan, err := BuildAlter(c.Dialect, t, st, ops)
	if err != nil {
		return nil, err
	}
	preview := plan.Preview(c.Dialect)
	if dryRun {
		return preview, nil
	}
	if plan.Rebuild {
		return preview, c.runRebuild(ctx, t, plan)
	}
	return preview, c.runDDLLocked(ctx, plan.Statements)
}
