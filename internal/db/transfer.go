package db

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/stdlib"

	"gotable/internal/model"
)

// Exec runs one statement and discards any rows it returns.
func (c *Conn) Exec(ctx context.Context, stmt string) (int64, error) {
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	res, err := c.conn.ExecContext(ctx, stmt)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CopyFrom runs a PostgreSQL "COPY … FROM stdin" statement with data from r.
func (c *Conn) CopyFrom(ctx context.Context, stmt string, r io.Reader) (int64, error) {
	if c.Config.Driver != model.DriverPostgres {
		return 0, errors.New("COPY … FROM stdin is only supported on PostgreSQL")
	}
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	var n int64
	err := c.conn.Raw(func(dc any) error {
		pc, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected driver connection %T", dc)
		}
		tag, err := pc.Conn().PgConn().CopyFrom(ctx, r, stmt)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// StreamRows calls fn for every row of a table page (Limit 0 = all rows)
// without holding the result in memory.
func (c *Conn) StreamRows(ctx context.Context, t TableRef, p Page, columns []string, fn func(cols, types []string, row []Value) error) error {
	q, args, err := BuildSelect(c.Dialect, t, p, columns)
	if err != nil {
		return err
	}
	return c.StreamQuery(ctx, q, args, fn)
}

// StreamQuery calls fn for every row of a query.
func (c *Conn) StreamQuery(ctx context.Context, q string, args []any, fn func(cols, types []string, row []Value) error) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	rows, err := c.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	types := make([]string, len(cols))
	if cts, err := rows.ColumnTypes(); err == nil {
		for i, ct := range cts {
			types[i] = ct.DatabaseTypeName()
		}
	}
	raw := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	row := make([]Value, len(cols))
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, v := range raw {
			row[i] = toValue(v, types[i])
		}
		if err := fn(cols, types, row); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ---------------------------------------------------------------- SQL import

// ImportError is a statement that failed during an import.
type ImportError struct {
	Line      int    `json:"line"`
	Statement string `json:"statement"`
	Message   string `json:"message"`
}

// ImportOptions control ImportSQL.
type ImportOptions struct {
	// Transaction runs everything in one transaction: any error rolls back
	// the whole import. Transaction statements inside the script (BEGIN,
	// COMMIT, …) are skipped.
	Transaction bool
	// ContinueOnError skips failing statements (without Transaction).
	ContinueOnError bool
	// MaxErrors caps the errors kept in the result.
	MaxErrors int
}

// ImportResult summarizes an import.
type ImportResult struct {
	Statements int64         `json:"statements"`
	Rows       int64         `json:"rows"`
	Failed     int64         `json:"failed"`
	Skipped    int64         `json:"skipped"`
	Errors     []ImportError `json:"errors"`
}

var txControl = regexp.MustCompile(`(?i)^(BEGIN(\s+(TRANSACTION|WORK|DEFERRED|IMMEDIATE|EXCLUSIVE))?|START\s+TRANSACTION|COMMIT(\s+(TRANSACTION|WORK))?|END(\s+TRANSACTION)?|ROLLBACK)\s*$`)

func snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// ImportSQL runs every statement of a script read from r. progress, if
// set, is called after each statement with the running result.
func (c *Conn) ImportSQL(ctx context.Context, r io.Reader, o ImportOptions, progress func(*ImportResult)) (*ImportResult, error) {
	if o.MaxErrors <= 0 {
		o.MaxErrors = 200
	}
	res := &ImportResult{Errors: []ImportError{}}
	sc := NewScanner(r, c.Config.Driver)
	// SQLite ignores "PRAGMA foreign_keys = OFF" inside a transaction, so a
	// dump's own PRAGMA can't turn the checks off: rows of a table would
	// be refused before the table they point at exists. Turn them off
	// around the transaction and check every key before committing.
	sqliteKeys := false
	if o.Transaction && c.Config.Driver == model.DriverSQLite {
		on, err := c.sqliteForeignKeys(ctx)
		if err != nil {
			return res, err
		}
		if on {
			if _, err := c.Exec(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
				return res, err
			}
			sqliteKeys = true
			defer c.Exec(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON")
		}
	}
	if o.Transaction {
		if _, err := c.Exec(ctx, "BEGIN"); err != nil {
			return res, err
		}
	}
	fail := func(err error) (*ImportResult, error) {
		if o.Transaction {
			c.Exec(context.WithoutCancel(ctx), "ROLLBACK")
		}
		return res, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		st, err := sc.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(err)
		}
		if o.Transaction && st.Copy == nil && txControl.MatchString(st.SQL) {
			res.Skipped++
			continue
		}
		var n int64
		if st.Copy == nil && copyFromStdin.MatchString(stripLeadingComments(st.SQL)) {
			// Never send COPY … FROM stdin without its data: the server would
			// wait for it forever.
			err = errors.New("COPY … FROM stdin is only supported in PostgreSQL scripts")
		} else if st.Copy != nil {
			n, err = c.CopyFrom(ctx, st.SQL, st.Copy)
		} else {
			n, err = c.Exec(ctx, st.SQL)
		}
		res.Statements++
		// Count written rows only; a SELECT (e.g. pg_dump's set_config) reports
		// the rows it returned.
		if n > 0 && (st.Copy != nil || !returnsRows(st.SQL)) {
			res.Rows += n
		}
		if err != nil {
			res.Failed++
			if len(res.Errors) < o.MaxErrors {
				res.Errors = append(res.Errors, ImportError{Line: st.Line, Statement: snippet(st.SQL), Message: err.Error()})
			}
			if !o.ContinueOnError || o.Transaction || ctx.Err() != nil {
				r, _ := fail(nil)
				return r, fmt.Errorf("line %d: %w", st.Line, err)
			}
		}
		if progress != nil {
			progress(res)
		}
	}
	if sqliteKeys {
		if n, err := c.sqliteBrokenKeys(ctx); err != nil || n > 0 {
			if err == nil {
				err = fmt.Errorf("the import would leave %d row(s) with a foreign key that points nowhere", n)
			}
			r, _ := fail(nil)
			return r, err
		}
	}
	if o.Transaction {
		if _, err := c.Exec(ctx, "COMMIT"); err != nil {
			return fail(err)
		}
	}
	return res, nil
}

// sqliteForeignKeys reports whether SQLite enforces foreign keys.
func (c *Conn) sqliteForeignKeys(ctx context.Context) (bool, error) {
	if err := c.lock(ctx); err != nil {
		return false, err
	}
	defer c.mu.Unlock()
	var on int
	err := c.conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on)
	return on == 1, err
}

// sqliteBrokenKeys counts rows whose foreign key points nowhere.
func (c *Conn) sqliteBrokenKeys(ctx context.Context) (int, error) {
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	rows, err := c.conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

// ---------------------------------------------------------------- CSV import

// CSVImportOptions control ImportCSV.
type CSVImportOptions struct {
	Delimiter rune
	Header    bool // the first record is a header and is skipped
	// Columns are the table columns to fill; Mapping[i] is the CSV field
	// index for Columns[i].
	Columns []string
	Mapping []int
	// EmptyAsNull stores empty fields as NULL; NullText (e.g. "NULL" or
	// "\N") is always stored as NULL when set.
	EmptyAsNull bool
	NullText    string
	// Truncate deletes all rows first (in the same transaction).
	Truncate bool
}

// maxParams is a safe number of bind parameters per statement.
func (d Dialect) maxParams() int {
	if d.kind == model.DriverSQLite {
		return 30000
	}
	return 60000
}

// ImportCSV inserts the records of a CSV stream into a table in one
// transaction. progress is called after every batch with the rows so far.
func (c *Conn) ImportCSV(ctx context.Context, r io.Reader, t TableRef, o CSVImportOptions, progress func(rows int64)) (int64, error) {
	if len(o.Columns) == 0 || len(o.Columns) != len(o.Mapping) {
		return 0, errors.New("choose at least one column to import")
	}
	if o.Delimiter == 0 {
		o.Delimiter = ','
	}
	cr := csv.NewReader(r)
	cr.Comma = o.Delimiter
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	batch := min(1000, c.Dialect.maxParams()/len(o.Columns))

	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	tx, err := c.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if o.Truncate {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+c.Dialect.QualifiedName(t)); err != nil {
			return 0, err
		}
	}
	qcols := make([]string, len(o.Columns))
	for i, col := range o.Columns {
		qcols[i] = c.Dialect.QuoteIdent(col)
	}
	prefix := "INSERT INTO " + c.Dialect.QualifiedName(t) + " (" + strings.Join(qcols, ", ") + ") VALUES "
	var (
		args     []any
		groups   []string
		total    int64
		firstRec = true
		line     int
	)
	flush := func() error {
		if len(groups) == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, prefix+strings.Join(groups, ", "), args...); err != nil {
			return fmt.Errorf("rows ending at CSV line %d: %w", line, err)
		}
		total += int64(len(groups))
		args, groups = args[:0], groups[:0]
		if progress != nil {
			progress(total)
		}
		return nil
	}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return total, fmt.Errorf("CSV: %w", err)
		}
		line, _ = cr.FieldPos(0)
		if firstRec {
			firstRec = false
			if o.Header {
				continue
			}
		}
		ph := make([]string, len(o.Columns))
		for i, idx := range o.Mapping {
			var v any
			if idx >= 0 && idx < len(rec) {
				f := rec[idx]
				switch {
				case o.NullText != "" && f == o.NullText, o.EmptyAsNull && f == "":
					v = nil
				default:
					v = f
				}
			}
			args = append(args, v)
			ph[i] = c.Dialect.Placeholder(len(args))
		}
		groups = append(groups, "("+strings.Join(ph, ", ")+")")
		if len(groups) >= batch {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	if err := flush(); err != nil {
		return total, err
	}
	return total, tx.Commit()
}

// ---------------------------------------------------------------- dump

// DumpOptions control Dump.
type DumpOptions struct {
	Objects   []Object
	Structure bool
	Data      bool
	DropFirst bool
	BatchSize int // rows per INSERT (default 200)
}

// DumpProgress reports Dump's progress.
type DumpProgress struct {
	Table      string
	TableIndex int   // 0-based
	Tables     int   // total
	Rows       int64 // rows written so far, all tables
	TotalRows  int64 // estimate from COUNT(*), all tables
}

type sqlWriter struct {
	w   io.Writer
	err error
}

func (s *sqlWriter) printf(format string, a ...any) {
	if s.err == nil {
		_, s.err = fmt.Fprintf(s.w, format, a...)
	}
}

// Dump writes a portable SQL dump of the given tables and views. Table
// names are written without a schema so the dump can be loaded anywhere.
func (c *Conn) Dump(ctx context.Context, w io.Writer, o DumpOptions, progress func(DumpProgress)) error {
	if o.BatchSize <= 0 {
		o.BatchSize = 200
	}
	d := c.Dialect
	var tables, views []Object
	for _, ob := range o.Objects {
		if ob.Kind == KindView {
			views = append(views, ob)
		} else {
			tables = append(tables, ob)
		}
	}
	p := DumpProgress{Tables: len(o.Objects)}
	if o.Data {
		for _, t := range tables {
			if n, err := c.CountRows(ctx, TableRef{Schema: t.Schema, Name: t.Name}, Page{}, nil); err == nil {
				p.TotalRows += n
			}
		}
	}
	sw := &sqlWriter{w: w}
	ver, _ := c.ServerVersion(ctx)
	sw.printf("-- GoTable dump of %s (%s)\n-- Tables: %d, views: %d\n\n", c.Config.Name, ver, len(tables), len(views))

	var post []string
	switch c.Config.Driver {
	case model.DriverPostgres:
		sw.printf("SET client_encoding = 'UTF8';\nSET standard_conforming_strings = on;\n\n")
	case model.DriverMySQL, model.DriverMariaDB:
		sw.printf("SET NAMES utf8mb4;\nSET FOREIGN_KEY_CHECKS = 0;\n\n")
	case model.DriverSQLite:
		sw.printf("PRAGMA foreign_keys = OFF;\nBEGIN TRANSACTION;\n\n")
	}
	if o.DropFirst {
		for i := len(views) - 1; i >= 0; i-- {
			sw.printf("DROP VIEW IF EXISTS %s;\n", d.QuoteIdent(views[i].Name))
		}
		for i := len(tables) - 1; i >= 0; i-- {
			cascade := ""
			if c.Config.Driver == model.DriverPostgres {
				cascade = " CASCADE"
			}
			sw.printf("DROP TABLE IF EXISTS %s%s;\n", d.QuoteIdent(tables[i].Name), cascade)
		}
		sw.printf("\n")
	}
	var seqs []pgSequence
	if o.Structure && c.Config.Driver == model.DriverPostgres && len(tables) > 0 {
		var err error
		if seqs, err = c.pgSequences(ctx, tables[0].Schema); err != nil {
			return err
		}
		for _, s := range seqs {
			if !s.identity {
				sw.printf("CREATE SEQUENCE IF NOT EXISTS %s;\n", d.QuoteIdent(s.name))
			}
		}
		if len(seqs) > 0 {
			sw.printf("\n")
		}
	}
	for i, t := range tables {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.Table, p.TableIndex = t.Name, i
		if progress != nil {
			progress(p)
		}
		ref := TableRef{Schema: t.Schema, Name: t.Name}
		if o.Structure {
			create, after, err := c.dumpDefinition(ctx, t)
			if err != nil {
				return fmt.Errorf("%s: %w", t.Name, err)
			}
			sw.printf("-- Table %s\n%s\n\n", t.Name, strings.TrimRight(create, ";\n")+";")
			post = append(post, after...)
		}
		if !o.Data {
			continue
		}
		var cols string
		var batch []string
		flush := func() {
			if len(batch) > 0 {
				sw.printf("INSERT INTO %s (%s) VALUES\n  %s;\n", d.QuoteIdent(t.Name), cols, strings.Join(batch, ",\n  "))
				batch = batch[:0]
			}
		}
		err := c.StreamRows(ctx, ref, Page{}, nil, func(names, _ []string, row []Value) error {
			if cols == "" {
				q := make([]string, len(names))
				for i, n := range names {
					q[i] = d.QuoteIdent(n)
				}
				cols = strings.Join(q, ", ")
			}
			vals := make([]string, len(row))
			for i, v := range row {
				vals[i] = d.QuoteLiteral(v)
			}
			batch = append(batch, "("+strings.Join(vals, ", ")+")")
			if len(batch) >= o.BatchSize {
				flush()
			}
			p.Rows++
			if progress != nil && p.Rows%500 == 0 {
				progress(p)
			}
			return sw.err
		})
		if err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
		flush()
		sw.printf("\n")
	}
	for _, s := range post {
		sw.printf("%s\n", strings.TrimRight(s, ";")+";")
	}
	if len(post) > 0 {
		sw.printf("\n")
	}
	if o.Structure {
		for i, v := range views {
			p.Table, p.TableIndex = v.Name, len(tables)+i
			if progress != nil {
				progress(p)
			}
			def, err := c.dumpView(ctx, v)
			if err != nil {
				return fmt.Errorf("%s: %w", v.Name, err)
			}
			sw.printf("-- View %s\n%s;\n\n", v.Name, strings.TrimRight(def, ";\n"))
		}
	}
	if o.Data {
		for _, s := range seqs {
			if s.last != nil {
				sw.printf("SELECT setval('%s', %d, true);\n", strings.ReplaceAll(d.QuoteIdent(s.name), "'", "''"), *s.last)
			}
		}
	}
	switch c.Config.Driver {
	case model.DriverMySQL, model.DriverMariaDB:
		sw.printf("SET FOREIGN_KEY_CHECKS = 1;\n")
	case model.DriverSQLite:
		sw.printf("COMMIT;\nPRAGMA foreign_keys = ON;\n")
	}
	p.Table, p.TableIndex = "", len(o.Objects)
	if progress != nil {
		progress(p)
	}
	return sw.err
}

type pgSequence struct {
	name     string
	last     *int64
	identity bool
}

func (c *Conn) pgSequences(ctx context.Context, schema string) ([]pgSequence, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	rows, err := c.conn.QueryContext(ctx, `SELECT s.sequencename, s.last_value,
			EXISTS (SELECT 1 FROM pg_depend dp JOIN pg_class cl ON cl.oid = dp.objid
				JOIN pg_namespace n ON n.oid = cl.relnamespace
				WHERE n.nspname = s.schemaname AND cl.relname = s.sequencename AND dp.deptype = 'i')
		FROM pg_sequences s WHERE s.schemaname = $1 ORDER BY 1`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pgSequence
	for rows.Next() {
		var s pgSequence
		var last sql.NullInt64
		if err := rows.Scan(&s.name, &last, &s.identity); err != nil {
			return nil, err
		}
		if last.Valid {
			v := last.Int64
			s.last = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// dumpDefinition returns a CREATE TABLE statement without a schema, plus
// statements to run after the data (foreign keys and indexes).
func (c *Conn) dumpDefinition(ctx context.Context, o Object) (string, []string, error) {
	if err := c.lock(ctx); err != nil {
		return "", nil, err
	}
	defer c.mu.Unlock()
	d := c.Dialect
	name := d.QuoteIdent(o.Name)
	switch c.Config.Driver {
	case model.DriverPostgres:
		rows, err := c.conn.QueryContext(ctx, `SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
				pg_get_expr(d.adbin, d.adrelid), a.attidentity::text, a.attgenerated::text
			FROM pg_attribute a JOIN pg_class cl ON cl.oid = a.attrelid JOIN pg_namespace n ON n.oid = cl.relnamespace
			LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE n.nspname = $1 AND cl.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, o.Schema, o.Name)
		if err != nil {
			return "", nil, err
		}
		var lines []string
		for rows.Next() {
			var col, typ, ident, gen string
			var notNull bool
			var def sql.NullString
			if err := rows.Scan(&col, &typ, &notNull, &def, &ident, &gen); err != nil {
				rows.Close()
				return "", nil, err
			}
			l := "  " + d.QuoteIdent(col) + " " + typ
			switch {
			case gen == "s" && def.Valid:
				l += " GENERATED ALWAYS AS (" + def.String + ") STORED"
			case ident == "a":
				l += " GENERATED ALWAYS AS IDENTITY"
			case ident == "d":
				l += " GENERATED BY DEFAULT AS IDENTITY"
			case def.Valid:
				l += " DEFAULT " + def.String
			}
			if notNull {
				l += " NOT NULL"
			}
			lines = append(lines, l)
		}
		rows.Close()
		var post []string
		crs, err := c.conn.QueryContext(ctx, `SELECT co.conname, co.contype::text, pg_get_constraintdef(co.oid, true) FROM pg_constraint co
			JOIN pg_class cl ON cl.oid = co.conrelid JOIN pg_namespace n ON n.oid = cl.relnamespace
			WHERE n.nspname = $1 AND cl.relname = $2 AND co.contype IN ('p', 'u', 'c', 'f', 'x')
			ORDER BY CASE co.contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'c' THEN 2 ELSE 3 END, co.conname`, o.Schema, o.Name)
		if err != nil {
			return "", nil, err
		}
		for crs.Next() {
			var cn, typ, def string
			if err := crs.Scan(&cn, &typ, &def); err != nil {
				crs.Close()
				return "", nil, err
			}
			if typ == "f" {
				// Foreign keys reference tables by schema; strip ours for portability.
				def = strings.ReplaceAll(def, d.QuoteIdent(o.Schema)+".", "")
				def = strings.ReplaceAll(def, o.Schema+".", "")
				post = append(post, fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s %s", name, d.QuoteIdent(cn), def))
				continue
			}
			lines = append(lines, "  CONSTRAINT "+d.QuoteIdent(cn)+" "+def)
		}
		crs.Close()
		idx, err := c.queryStrings(ctx, `SELECT pg_get_indexdef(ix.indexrelid) FROM pg_index ix
			JOIN pg_class tb ON tb.oid = ix.indrelid JOIN pg_namespace n ON n.oid = tb.relnamespace
			WHERE n.nspname = $1 AND tb.relname = $2
			  AND NOT EXISTS (SELECT 1 FROM pg_constraint co WHERE co.conindid = ix.indexrelid) ORDER BY 1`, o.Schema, o.Name)
		if err != nil {
			return "", nil, err
		}
		for _, s := range idx {
			post = append(post, strings.Replace(s, " ON "+d.QuoteIdent(o.Schema)+".", " ON ", 1))
			post[len(post)-1] = strings.Replace(post[len(post)-1], " ON "+o.Schema+".", " ON ", 1)
		}
		return "CREATE TABLE " + name + " (\n" + strings.Join(lines, ",\n") + "\n)", post, nil
	case model.DriverMySQL, model.DriverMariaDB:
		var tn, ddl string
		if err := c.conn.QueryRowContext(ctx, "SHOW CREATE TABLE "+d.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name})).Scan(&tn, &ddl); err != nil {
			return "", nil, err
		}
		return ddl, nil, nil
	default:
		defs, err := c.queryStrings(ctx, "SELECT sql FROM "+sqliteEngine{}.master(c, o.Schema)+
			" WHERE tbl_name = ? AND sql IS NOT NULL ORDER BY type = 'table' DESC, name", o.Name)
		if err != nil || len(defs) == 0 {
			return "", nil, fmt.Errorf("no definition for %s: %v", o.Name, err)
		}
		return defs[0], defs[1:], nil
	}
}

// dumpView returns a CREATE VIEW statement without a schema.
func (c *Conn) dumpView(ctx context.Context, o Object) (string, error) {
	switch c.Config.Driver {
	case model.DriverPostgres:
		if err := c.lock(ctx); err != nil {
			return "", err
		}
		defer c.mu.Unlock()
		var kind, def string
		err := c.conn.QueryRowContext(ctx, `SELECT c.relkind::text, pg_get_viewdef(c.oid, true) FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2`, o.Schema, o.Name).Scan(&kind, &def)
		if err != nil {
			return "", err
		}
		if kind == "m" {
			return "CREATE MATERIALIZED VIEW " + c.Dialect.QuoteIdent(o.Name) + " AS\n" + def, nil
		}
		return "CREATE OR REPLACE VIEW " + c.Dialect.QuoteIdent(o.Name) + " AS\n" + def, nil
	case model.DriverMySQL, model.DriverMariaDB:
		def, err := c.Definition(ctx, o)
		if err != nil {
			return "", err
		}
		// SHOW CREATE VIEW names the definer and the schema; drop both.
		def = regexp.MustCompile(`(?i)\s+DEFINER=\S+`).ReplaceAllString(def, "")
		def = strings.ReplaceAll(def, c.Dialect.QuoteIdent(o.Schema)+".", "")
		return def, nil
	default:
		return c.Definition(ctx, o)
	}
}
