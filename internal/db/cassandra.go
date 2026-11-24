package db

// Cassandra (and ScyllaDB): CQL over Cassandra's native protocol.
//
//   - A schema is a keyspace; its tables and materialized views are the
//     objects. A table's primary key is its partition key, then its
//     clustering columns.
//   - Reads have no OFFSET, and only key columns can be filtered or sorted
//     on the server. So a page is read by skipping rows in token order, and
//     the filter bar's conditions and sorts are applied here, over at most
//     cassMaxScan rows. A raw CQL WHERE is sent to the server, with ALLOW
//     FILTERING.
//   - Edits are CQL with lightweight transactions (IF EXISTS, IF NOT
//     EXISTS), so a row deleted meanwhile is reported. There are
//     no transactions: each change commits on its own.
//   - The session talks to the host it was given, which coordinates the
//     cluster: other nodes' addresses may not be reachable from here (an SSH
//     tunnel, Docker, a cloud network).

import (
	"context"
	"crypto/tls"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"gopkg.in/inf.v0"

	"gotable/internal/model"
)

const (
	cassMaxScan  = 100_000 // rows read to filter or sort in GoTable
	cassPageSize = 1000
)

type cassState struct {
	cluster  *gocql.ClusterConfig
	sessions map[string]*gocql.Session // by keyspace ("" for none)
	closed   bool
	server   string
}

type cassEngine struct{}

var errCassNoDesigner = errors.New("a Cassandra table is made with CREATE TABLE in the query editor: its primary key (partition and clustering columns) can't change later")

func openCassandra(ctx context.Context, cfg *model.Connection, addr, password string, tunnel io.Closer) (*Conn, error) {
	fail := func(err error) (*Conn, error) {
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, err
	}
	cl := gocql.NewCluster(addr)
	cl.Timeout = 30 * time.Second
	cl.ConnectTimeout = 15 * time.Second
	cl.Consistency = gocql.LocalQuorum
	// Only the given host: the addresses other nodes advertise may not be
	// reachable from here.
	cl.DisableInitialHostLookup = true
	cl.Dialer = fixedDialer{addr: addr}
	cl.NumConns = 1
	if cfg.User != "" {
		cl.Authenticator = gocql.PasswordAuthenticator{Username: cfg.User, Password: password}
	}
	if cfg.SSLMode == "require" || cfg.SSLMode == "skip-verify" {
		t, err := mongoTLS(cfg, tunnel != nil)
		if err != nil {
			return fail(err)
		}
		if t == nil {
			t = &tls.Config{}
		}
		cl.SslOpts = &gocql.SslOptions{Config: t, EnableHostVerification: !t.InsecureSkipVerify}
	}
	st := &cassState{cluster: cl, sessions: map[string]*gocql.Session{}}
	c := &Conn{Config: cfg, Dialect: DialectFor(cfg.Driver), eng: cassEngine{}, tunnel: tunnel, cass: st}
	ks := cfg.DBName
	if cfg.SelectedSchema != "" {
		ks = cfg.SelectedSchema
	}
	s, err := c.cassSession(ks)
	if err != nil {
		c.closeCass()
		return fail(err)
	}
	st.server = cassServerName(ctx, s)
	c.schema = ks
	return c, nil
}

// fixedDialer dials the one address GoTable was given, whatever the driver
// asks for.
type fixedDialer struct{ addr string }

func (d fixedDialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	var nd net.Dialer
	return nd.DialContext(ctx, network, d.addr)
}

// cassSession is the session whose keyspace is ks (one each: CQL's USE
// can't change a pooled session's keyspace).
func (c *Conn) cassSession(ks string) (*gocql.Session, error) {
	if s := c.cass.sessions[ks]; s != nil {
		return s, nil
	}
	cl := *c.cass.cluster
	cl.Keyspace = ks
	s, err := cl.CreateSession()
	if err != nil {
		return nil, err
	}
	c.cass.sessions[ks] = s
	return s, nil
}

func (c *Conn) cassFor(ks string) (*gocql.Session, error) {
	if ks == "" {
		ks = c.schema
	}
	return c.cassSession(ks)
}

// closeCass is Close for Cassandra. Callers hold c.mu.
func (c *Conn) closeCass() error {
	if c.cass.closed {
		return nil
	}
	c.cass.closed = true
	for _, s := range c.cass.sessions {
		s.Close()
	}
	if c.tunnel != nil {
		return c.tunnel.Close()
	}
	return nil
}

func cassServerName(ctx context.Context, s *gocql.Session) string {
	var v string
	if s.Query("SELECT version FROM system.versions").WithContext(ctx).Scan(&v) == nil && v != "" {
		return "ScyllaDB " + strings.Split(v, "-")[0]
	}
	if s.Query("SELECT release_version FROM system.local").WithContext(ctx).Scan(&v) == nil {
		return "Cassandra " + v
	}
	return "Cassandra"
}

// ---------------------------------------------------------------- engine

var cassSystemKeyspaces = map[string]bool{"system": true, "system_auth": true, "system_distributed": true, "system_schema": true,
	"system_traces": true, "system_views": true, "system_virtual_schema": true, "system_distributed_everywhere": true,
	"system_replicated_keys": true}

func (cassEngine) serverVersion(_ context.Context, c *Conn) (string, error) {
	return c.cass.server, nil
}

func (cassEngine) databases(context.Context, *Conn) ([]string, error) { return []string{}, nil }

func (cassEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	s, err := c.cassFor("")
	if err != nil {
		return nil, err
	}
	all, err := cassStrings(ctx, s, "SELECT keyspace_name FROM system_schema.keyspaces")
	out := []string{}
	for _, k := range all {
		if !cassSystemKeyspaces[k] && !strings.HasPrefix(k, "system_") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, err
}

func (cassEngine) currentSchema(_ context.Context, c *Conn) (string, error) { return c.schema, nil }

func (cassEngine) useSchema(_ context.Context, c *Conn, schema string) error {
	_, err := c.cassSession(schema)
	return err
}

func cassStrings(ctx context.Context, s *gocql.Session, q string, args ...any) ([]string, error) {
	iter := s.Query(q, args...).WithContext(ctx).Iter()
	var out []string
	var v string
	for iter.Scan(&v) {
		out = append(out, v)
	}
	return out, iter.Close()
}

func (cassEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	s, err := c.cassFor("")
	if err != nil {
		return nil, err
	}
	tables, err := cassStrings(ctx, s, "SELECT table_name FROM system_schema.tables WHERE keyspace_name = ?", schema)
	if err != nil {
		return nil, err
	}
	views, _ := cassStrings(ctx, s, "SELECT view_name FROM system_schema.views WHERE keyspace_name = ?", schema)
	sort.Strings(tables)
	sort.Strings(views)
	out := []Object{}
	for _, t := range tables {
		out = append(out, Object{Schema: schema, Name: t, Kind: KindTable})
	}
	for _, v := range views {
		out = append(out, Object{Schema: schema, Name: v, Kind: KindView})
	}
	return out, nil
}

// cassColumn is a column as system_schema describes it.
type cassColumn struct {
	name, kind, typ, order string
	position               int
}

// cassColumns lists a table's (or view's) columns in the order SELECT *
// returns them: partition key, clustering columns, then the rest by name.
func cassColumns(ctx context.Context, s *gocql.Session, t TableRef) ([]cassColumn, error) {
	iter := s.Query(`SELECT column_name, kind, position, type, clustering_order FROM system_schema.columns
		WHERE keyspace_name = ? AND table_name = ?`, t.Schema, t.Name).WithContext(ctx).Iter()
	var out []cassColumn
	var col cassColumn
	for iter.Scan(&col.name, &col.kind, &col.position, &col.typ, &col.order) {
		out = append(out, col)
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("table %s.%s not found", t.Schema, t.Name)
	}
	rank := map[string]int{"partition_key": 0, "clustering": 1, "static": 2, "regular": 2}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if rank[a.kind] != rank[b.kind] {
			return rank[a.kind] < rank[b.kind]
		}
		if a.kind == "partition_key" || a.kind == "clustering" {
			return a.position < b.position
		}
		return a.name < b.name
	})
	return out, nil
}

func (cassEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	s, err := c.cassFor("")
	if err != nil {
		return nil, err
	}
	cols, err := cassColumns(ctx, s, t)
	if err != nil {
		return nil, err
	}
	out := make([]Column, len(cols))
	for i, cc := range cols {
		out[i] = Column{Name: cc.name, Type: cc.typ, Nullable: cc.kind != "partition_key" && cc.kind != "clustering",
			PrimaryKey: cc.kind == "partition_key" || cc.kind == "clustering"}
		switch cc.kind {
		case "partition_key":
			out[i].Comment = "partition key"
		case "clustering":
			out[i].Comment = "clustering, " + strings.ToLower(cc.order)
		case "static":
			out[i].Comment = "static"
		}
	}
	return out, nil
}

func (cassEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	s, err := c.cassFor("")
	if err != nil {
		return nil, err
	}
	cols, err := cassColumns(ctx, s, t)
	if err != nil {
		return nil, err
	}
	var pk []string
	for _, cc := range cols {
		if cc.kind == "partition_key" || cc.kind == "clustering" {
			pk = append(pk, cc.name)
		}
	}
	out := []Index{{Name: "PRIMARY KEY", Columns: pk, Unique: true, Primary: true, Constraint: true}}
	iter := s.Query("SELECT index_name, kind, options FROM system_schema.indexes WHERE keyspace_name = ? AND table_name = ?",
		t.Schema, t.Name).WithContext(ctx).Iter()
	var name, kind string
	var opts map[string]string
	for iter.Scan(&name, &kind, &opts) {
		target := opts["target"]
		ix := Index{Name: name, Columns: []string{strings.Trim(target, `"`)}, Type: strings.ToLower(kind)}
		if cl := opts["class_name"]; cl != "" { // SASI, SAI
			ix.Type = cl[strings.LastIndexByte(cl, '.')+1:]
		}
		out = append(out, ix)
		opts = nil
	}
	return out, iter.Close()
}

func (cassEngine) foreignKeys(context.Context, *Conn, TableRef) ([]ForeignRef, error) {
	return nil, nil
}

// definition: the server's DESCRIBE (Cassandra 4+, ScyllaDB 5.2+), or a
// CREATE TABLE built from system_schema.
func (cassEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	s, err := c.cassFor("")
	if err != nil {
		return "", err
	}
	what := "TABLE"
	if o.Kind == KindView {
		what = "MATERIALIZED VIEW"
	}
	iter := s.Query("DESCRIBE " + what + " " + c.Dialect.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name})).WithContext(ctx).Iter()
	var parts []string
	row := map[string]any{}
	for iter.MapScan(row) {
		if st, ok := row["create_statement"].(string); ok {
			parts = append(parts, st)
		}
		row = map[string]any{}
	}
	if iter.Close() == nil && len(parts) > 0 {
		return strings.Join(parts, "\n\n"), nil
	}
	if o.Kind == KindView {
		return "", errors.New("this server can't DESCRIBE a materialized view")
	}
	return cassBuildCreate(ctx, c, s, TableRef{Schema: o.Schema, Name: o.Name})
}

func cassBuildCreate(ctx context.Context, c *Conn, s *gocql.Session, t TableRef) (string, error) {
	cols, err := cassColumns(ctx, s, t)
	if err != nil {
		return "", err
	}
	q := c.Dialect.QuoteIdent
	var lines, part, clus, order []string
	for _, cc := range cols {
		l := "    " + q(cc.name) + " " + cc.typ
		if cc.kind == "static" {
			l += " static"
		}
		lines = append(lines, l)
		switch cc.kind {
		case "partition_key":
			part = append(part, q(cc.name))
		case "clustering":
			clus = append(clus, q(cc.name))
			order = append(order, q(cc.name)+" "+strings.ToUpper(cc.order))
		}
	}
	pk := strings.Join(part, ", ")
	if len(part) > 1 {
		pk = "(" + pk + ")"
	}
	if len(clus) > 0 {
		pk += ", " + strings.Join(clus, ", ")
	}
	ddl := "CREATE TABLE " + c.Dialect.QualifiedName(t) + " (\n" + strings.Join(lines, ",\n") + ",\n    PRIMARY KEY (" + pk + ")\n)"
	if len(order) > 0 {
		ddl += " WITH CLUSTERING ORDER BY (" + strings.Join(order, ", ") + ")"
	}
	return ddl + ";", nil
}

// tableInfo: the key, the default TTL, compaction and the comment.
func (cassEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	s, err := c.cassFor("")
	if err != nil {
		return TableInfo{}, err
	}
	var comment string
	var ttl int
	var compaction map[string]string
	err = s.Query("SELECT comment, default_time_to_live, compaction FROM system_schema.tables WHERE keyspace_name = ? AND table_name = ?",
		t.Schema, t.Name).WithContext(ctx).Scan(&comment, &ttl, &compaction)
	if err != nil {
		return TableInfo{}, nil // a view
	}
	var parts []string
	if cl := compaction["class"]; cl != "" {
		parts = append(parts, "compaction "+cl[strings.LastIndexByte(cl, '.')+1:])
	}
	if ttl > 0 {
		parts = append(parts, "rows expire after "+seconds(int64(ttl)))
	}
	return TableInfo{Options: strings.Join(parts, " · "), Comment: comment}, nil
}

// ---------------------------------------------------------------- values

// cassScan reads a row into nullable destinations, so NULL stays NULL.
func cassScan(iter *gocql.Iter) ([]any, bool) {
	cols := iter.Columns()
	dest := make([]any, len(cols))
	for i, col := range cols {
		var t reflect.Type
		if z := col.TypeInfo.Zero(); z != nil {
			t = reflect.TypeOf(z)
		} else {
			t = reflect.TypeOf([]byte(nil))
		}
		dest[i] = reflect.New(reflect.PointerTo(t)).Interface()
	}
	if !iter.Scan(dest...) {
		return nil, false
	}
	out := make([]any, len(dest))
	for i, d := range dest {
		p := reflect.ValueOf(d).Elem()
		if !p.IsNil() {
			out[i] = p.Elem().Interface()
		}
	}
	return out, true
}

// cassValue shows a value: plain for a scalar, as a CQL literal inside a
// collection, so it can be edited and written back.
func cassValue(v any, t gocql.TypeInfo) Value {
	if v == nil {
		return NullValue
	}
	switch x := v.(type) {
	case string:
		return Text(x)
	case []byte:
		return Value{Text: "0x" + hex.EncodeToString(x), Hex: true}
	case time.Time:
		return Text(cassTime(x, t))
	case time.Duration: // the time type
		return Text(cassTimeOfDay(x))
	}
	switch t.Type() {
	case gocql.TypeList, gocql.TypeSet, gocql.TypeMap, gocql.TypeTuple, gocql.TypeUDT:
		return Text(cqlLiteralOf(v, t))
	}
	return Text(fmt.Sprint(v))
}

func cassTime(x time.Time, t gocql.TypeInfo) string {
	if t != nil && t.Type() == gocql.TypeDate {
		return x.UTC().Format("2006-01-02")
	}
	return x.UTC().Format("2006-01-02T15:04:05.000Z")
}

func cassTimeOfDay(d time.Duration) string {
	h := d / time.Hour
	m := d % time.Hour / time.Minute
	s := d % time.Minute / time.Second
	ns := d % time.Second
	out := fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	if ns > 0 {
		out += fmt.Sprintf(".%09d", ns)
	}
	return out
}

// cqlLiteralOf writes a read value as CQL: 'text', [1, 2], {'k': 1}…
func cqlLiteralOf(v any, t gocql.TypeInfo) string {
	if v == nil {
		return "null"
	}
	var typ gocql.Type
	if t != nil {
		typ = t.Type()
	}
	switch x := v.(type) {
	case string:
		if typ == gocql.TypeInet || typ == gocql.TypeVarchar || typ == gocql.TypeText || typ == gocql.TypeAscii || t == nil {
			return quoteCQL(x)
		}
		return quoteCQL(x)
	case []byte:
		return "0x" + hex.EncodeToString(x)
	case time.Time:
		return quoteCQL(cassTime(x, t))
	case time.Duration:
		return quoteCQL(cassTimeOfDay(x))
	case gocql.UUID:
		return x.String()
	case net.IP:
		return quoteCQL(x.String())
	case *inf.Dec, *big.Int, gocql.Duration:
		return fmt.Sprint(x)
	case bool:
		return strconv.FormatBool(x)
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		var elem gocql.TypeInfo
		var elems []gocql.TypeInfo
		switch ct := t.(type) {
		case gocql.CollectionType:
			elem = ct.Elem
		case gocql.TupleTypeInfo:
			elems = ct.Elems
		}
		parts := make([]string, rv.Len())
		for i := range parts {
			et := elem
			if i < len(elems) {
				et = elems[i]
			}
			parts[i] = cqlLiteralOf(rv.Index(i).Interface(), et)
		}
		switch typ {
		case gocql.TypeSet:
			return "{" + strings.Join(parts, ", ") + "}"
		case gocql.TypeTuple:
			return "(" + strings.Join(parts, ", ") + ")"
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case reflect.Map:
		var kt, vt gocql.TypeInfo
		fields := map[string]gocql.TypeInfo{}
		switch ct := t.(type) {
		case gocql.CollectionType:
			kt, vt = ct.Key, ct.Elem
		case gocql.UDTTypeInfo:
			for _, f := range ct.Elements {
				fields[f.Name] = f.Type
			}
		}
		keys := rv.MapKeys()
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if typ == gocql.TypeUDT {
				name := fmt.Sprint(k.Interface())
				parts = append(parts, name+": "+cqlLiteralOf(rv.MapIndex(k).Interface(), fields[name]))
			} else {
				parts = append(parts, cqlLiteralOf(k.Interface(), kt)+": "+cqlLiteralOf(rv.MapIndex(k).Interface(), vt))
			}
		}
		sort.Strings(parts)
		return "{" + strings.Join(parts, ", ") + "}"
	case reflect.Pointer:
		if rv.IsNil() {
			return "null"
		}
		return cqlLiteralOf(rv.Elem().Interface(), t)
	}
	return fmt.Sprint(v)
}

func quoteCQL(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

var (
	cassNumber = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$|^[+-]?(NaN|Infinity)$`)
	cassUUID   = regexp.MustCompile(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	cassBlob   = regexp.MustCompile(`^(?i)0x[0-9a-f]*$`)
)

// cqlLiteral writes text typed into a cell as a literal of the column's
// CQL type. Collections, tuples and user types are typed as CQL.
func cqlLiteral(cqlType string, v Value) (string, error) {
	if v.Null {
		return "null", nil
	}
	s := strings.TrimSpace(v.Text)
	base := strings.ToLower(cqlType)
	for strings.HasPrefix(base, "frozen<") {
		base = strings.TrimSuffix(strings.TrimPrefix(base, "frozen<"), ">")
	}
	switch base {
	case "text", "varchar", "ascii", "inet", "timestamp", "date", "time":
		return quoteCQL(v.Text), nil
	case "int", "bigint", "smallint", "tinyint", "varint", "float", "double", "decimal":
		if !cassNumber.MatchString(s) {
			return "", fmt.Errorf("%q isn't a number", v.Text)
		}
		return s, nil
	case "boolean":
		switch strings.ToLower(s) {
		case "true", "false":
			return strings.ToLower(s), nil
		}
		return "", fmt.Errorf("%q isn't true or false", v.Text)
	case "uuid", "timeuuid":
		if !cassUUID.MatchString(s) {
			return "", fmt.Errorf("%q isn't a UUID", v.Text)
		}
		return s, nil
	case "blob":
		if !cassBlob.MatchString(s) {
			return "", fmt.Errorf("a blob is written 0x…, not %q", v.Text)
		}
		return s, nil
	case "duration":
		return s, nil // 1h30m, P1D
	case "counter":
		return "", errors.New("a counter changes only by UPDATE … SET c = c + n: use the query editor")
	}
	return s, nil // list<…>, set<…>, map<…>, tuple<…>, a user type: CQL as typed
}

// ---------------------------------------------------------------- rows

// cassRead reads the rows of a table or view: the page's, or (to filter
// or sort here) all of them up to cassMaxScan. total is the count, or -1
// if not counted.
func (c *Conn) cassRead(ctx context.Context, t TableRef, p Page, all bool, fn func(cols []string, types []string, row []Value) error) (int64, error) {
	s, err := c.cassFor("")
	if err != nil {
		return 0, err
	}
	q := "SELECT * FROM " + c.Dialect.QualifiedName(t)
	if raw := strings.TrimSpace(p.RawWhere); raw != "" {
		q += " WHERE " + raw
		if !strings.Contains(strings.ToUpper(raw), "ALLOW FILTERING") {
			q += " ALLOW FILTERING"
		}
	}
	iter := s.Query(q).WithContext(ctx).PageSize(cassPageSize).Iter()
	infos := iter.Columns()
	cols := make([]string, len(infos))
	types := make([]string, len(infos))
	for i, ci := range infos {
		cols[i] = ci.Name
		types[i] = fmt.Sprint(ci.TypeInfo)
	}
	local := len(p.Filters) > 0 || len(p.Sort) > 0
	if !local {
		// Straight through: skip to the page, read it.
		var n int64
		for {
			vals, ok := cassScan(iter)
			if !ok {
				break
			}
			if !all && n < int64(p.Offset) {
				n++
				continue
			}
			if !all && p.Limit > 0 && n >= int64(p.Offset+p.Limit) {
				break
			}
			row := make([]Value, len(vals))
			for i, v := range vals {
				row[i] = cassValue(v, infos[i].TypeInfo)
			}
			if err := fn(cols, types, row); err != nil {
				iter.Close()
				return 0, err
			}
			n++
		}
		return -1, iter.Close()
	}
	var rows [][]Value
	for {
		vals, ok := cassScan(iter)
		if !ok {
			break
		}
		if len(rows) >= cassMaxScan {
			iter.Close()
			return 0, fmt.Errorf("the table has more than %d rows: too many to filter or sort here; narrow it with a CQL filter (WHERE) on its key", cassMaxScan)
		}
		row := make([]Value, len(vals))
		for i, v := range vals {
			row[i] = cassValue(v, infos[i].TypeInfo)
		}
		rows = append(rows, row)
	}
	if err := iter.Close(); err != nil {
		return 0, err
	}
	if rows, err = filterRows(cols, rows, p.Filters); err != nil {
		return 0, err
	}
	if err := sortRows(cols, rows, p.Sort); err != nil {
		return 0, err
	}
	total := int64(len(rows))
	if !all {
		rows = pageOf(rows, p)
	}
	for _, r := range rows {
		if err := fn(cols, types, r); err != nil {
			return 0, err
		}
	}
	return total, nil
}

func (c *Conn) cassFetchRows(ctx context.Context, t TableRef, p Page) (*ResultSet, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	rs := &ResultSet{Rows: [][]Value{}}
	_, err := c.cassRead(ctx, t, p, false, func(cols, types []string, row []Value) error {
		rs.Columns, rs.Types = cols, types
		rs.Rows = append(rs.Rows, row)
		return nil
	})
	if rs.Columns == nil && err == nil { // no rows: the columns still
		if cols, cerr := (cassEngine{}).columns(ctx, c, t); cerr == nil {
			for _, col := range cols {
				rs.Columns = append(rs.Columns, col.Name)
				rs.Types = append(rs.Types, col.Type)
			}
		}
	}
	return rs, err
}

func (c *Conn) cassCountRows(ctx context.Context, t TableRef, p Page) (int64, error) {
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	if len(p.Filters) == 0 {
		s, err := c.cassFor("")
		if err != nil {
			return 0, err
		}
		q := "SELECT COUNT(*) FROM " + c.Dialect.QualifiedName(t)
		if raw := strings.TrimSpace(p.RawWhere); raw != "" {
			q += " WHERE " + raw
			if !strings.Contains(strings.ToUpper(raw), "ALLOW FILTERING") {
				q += " ALLOW FILTERING"
			}
		}
		var n int64
		err = s.Query(q).WithContext(ctx).Scan(&n)
		return n, err
	}
	return c.cassRead(ctx, t, Page{Filters: p.Filters, RawWhere: p.RawWhere, Sort: []Sort{}}, false, func([]string, []string, []Value) error { return nil })
}

func (c *Conn) cassStreamRows(ctx context.Context, t TableRef, p Page, fn func(cols, types []string, row []Value) error) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	_, err := c.cassRead(ctx, t, p, p.Limit == 0, fn)
	return err
}

// ---------------------------------------------------------------- editing

// cassPlan turns grid edits into CQL with lightweight transactions.
func (c *Conn) cassPlan(ctx context.Context, t TableRef, changes []Change) ([]string, error) {
	s, err := c.cassFor("")
	if err != nil {
		return nil, err
	}
	cols, err := cassColumns(ctx, s, t)
	if err != nil {
		var isView bool
		if vs, _ := cassStrings(ctx, s, "SELECT view_name FROM system_schema.views WHERE keyspace_name = ? AND view_name = ?", t.Schema, t.Name); len(vs) > 0 {
			isView = true
		}
		if isView {
			return nil, errors.New("a materialized view can't be edited: change its base table")
		}
		return nil, err
	}
	typ := map[string]string{}
	var key []string
	for _, cc := range cols {
		typ[cc.name] = cc.typ
		if cc.kind == "partition_key" || cc.kind == "clustering" {
			key = append(key, cc.name)
		}
		if cc.typ == "counter" {
			return nil, errors.New("a counter table changes only by UPDATE … SET c = c + n: use the query editor")
		}
	}
	q := c.Dialect.QuoteIdent
	name := c.Dialect.QualifiedName(t)
	where := func(k map[string]Value) (string, error) {
		var conds []string
		for _, col := range key {
			v, ok := k[col]
			if !ok || v.Null {
				return "", fmt.Errorf("the row's key %s is missing", col)
			}
			lit, err := cqlLiteral(typ[col], v)
			if err != nil {
				return "", fmt.Errorf("%s: %w", col, err)
			}
			conds = append(conds, q(col)+" = "+lit)
		}
		return strings.Join(conds, " AND "), nil
	}
	isKey := map[string]bool{}
	for _, k := range key {
		isKey[k] = true
	}
	var out []string
	for _, ch := range changes {
		switch ch.Kind {
		case ChangeInsert:
			var names, vals []string
			for _, col := range sortedKeys(ch.Values) {
				v := ch.Values[col]
				if v.Null && !isKey[col] {
					continue
				}
				if _, ok := typ[col]; !ok {
					return nil, fmt.Errorf("no column %s", col)
				}
				lit, err := cqlLiteral(typ[col], v)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", col, err)
				}
				names = append(names, q(col))
				vals = append(vals, lit)
			}
			for _, k := range key {
				if v, ok := ch.Values[k]; !ok || v.Null {
					return nil, fmt.Errorf("a new row needs its key: %s", strings.Join(key, ", "))
				}
			}
			out = append(out, "INSERT INTO "+name+" ("+strings.Join(names, ", ")+") VALUES ("+strings.Join(vals, ", ")+") IF NOT EXISTS")
		case ChangeUpdate:
			w, err := where(ch.Key)
			if err != nil {
				return nil, err
			}
			var sets []string
			for _, col := range sortedKeys(ch.Values) {
				if isKey[col] {
					return nil, fmt.Errorf("%s is part of the primary key, which can't change: add a new row and delete this one", col)
				}
				lit, err := cqlLiteral(typ[col], ch.Values[col])
				if err != nil {
					return nil, fmt.Errorf("%s: %w", col, err)
				}
				sets = append(sets, q(col)+" = "+lit)
			}
			out = append(out, "UPDATE "+name+" SET "+strings.Join(sets, ", ")+" WHERE "+w+" IF EXISTS")
		case ChangeDelete:
			w, err := where(ch.Key)
			if err != nil {
				return nil, err
			}
			out = append(out, "DELETE FROM "+name+" WHERE "+w+" IF EXISTS")
		}
	}
	return out, nil
}

func (c *Conn) cassPreviewChanges(ctx context.Context, t TableRef, changes []Change) ([]string, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	stmts, err := c.cassPlan(ctx, t, changes)
	for i := range stmts {
		stmts[i] += ";"
	}
	return stmts, err
}

func (c *Conn) cassApplyChanges(ctx context.Context, t TableRef, changes []Change) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	stmts, err := c.cassPlan(ctx, t, changes)
	if err != nil {
		return err
	}
	s, err := c.cassFor("")
	if err != nil {
		return err
	}
	for i, st := range stmts {
		applied, err := s.Query(st).WithContext(ctx).MapScanCAS(map[string]any{})
		if err == nil && !applied {
			if strings.HasPrefix(st, "INSERT") {
				err = errors.New("a row with that key already exists")
			} else {
				err = errors.New("the row no longer exists")
			}
		}
		if err != nil {
			if i > 0 {
				return fmt.Errorf("%s: %w (the %d change(s) before it were applied: Cassandra has no transactions)", st, err, i)
			}
			return fmt.Errorf("%s: %w", st, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- the query editor

var cassUse = regexp.MustCompile(`(?is)^\s*USE\s+("(?:[^"]|"")+"|\w+)\s*;?\s*$`)

func (c *Conn) cassExecuteIn(ctx context.Context, schema, script string, rowCap int) ([]Result, error) {
	spans := SplitStatements(c.Config.Driver, script)
	if len(spans) == 0 {
		return nil, fmt.Errorf("nothing to execute")
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	if schema != "" && schema != c.schema {
		if _, err := c.cassSession(schema); err != nil {
			return nil, err
		}
		c.schema = schema
	}
	var results []Result
	for _, sp := range spans {
		r := c.cassRun(ctx, sp.SQL, rowCap)
		results = append(results, r)
		if r.Err != nil {
			break
		}
	}
	return results, nil
}

// cassRun runs one statement. Callers hold c.mu.
func (c *Conn) cassRun(ctx context.Context, stmt string, rowCap int) (r Result) {
	r = Result{Statement: stmt}
	start := time.Now()
	defer func() { r.Duration = time.Since(start) }()
	if m := cassUse.FindStringSubmatch(stmt); m != nil {
		ks := m[1]
		if strings.HasPrefix(ks, `"`) {
			ks = strings.ReplaceAll(ks[1:len(ks)-1], `""`, `"`)
		} else {
			ks = strings.ToLower(ks) // unquoted names are case-insensitive
		}
		if _, err := c.cassSession(ks); err != nil {
			r.Err = err
			return r
		}
		c.schema = ks
		return r
	}
	s, err := c.cassFor("")
	if err != nil {
		r.Err = err
		return r
	}
	iter := s.Query(strings.TrimSuffix(strings.TrimSpace(stmt), ";")).WithContext(ctx).PageSize(cassPageSize).Iter()
	infos := iter.Columns()
	if len(infos) > 0 {
		set := &ResultSet{Rows: [][]Value{}}
		for _, ci := range infos {
			set.Columns = append(set.Columns, ci.Name)
			set.Types = append(set.Types, fmt.Sprint(ci.TypeInfo))
		}
		for {
			vals, ok := cassScan(iter)
			if !ok {
				break
			}
			if rowCap > 0 && len(set.Rows) >= rowCap {
				r.Truncated = true
				break
			}
			row := make([]Value, len(vals))
			for i, v := range vals {
				row[i] = cassValue(v, infos[i].TypeInfo)
			}
			set.Rows = append(set.Rows, row)
		}
		r.Set = set
	}
	if err := iter.Close(); err != nil {
		r.Err = err
		r.Set = nil
	}
	return r
}

func (c *Conn) cassExec(ctx context.Context, stmt string) (int64, error) {
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	return 0, c.cassRun(ctx, stmt, 1).Err
}

// ---------------------------------------------------------------- structure

func buildCassDrop(d Dialect, objs []Object) []string {
	var out []string
	for _, o := range objs { // views first: they depend on their tables
		if o.Kind == KindView {
			out = append(out, "DROP MATERIALIZED VIEW "+d.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name})+";")
		}
	}
	for _, o := range objs {
		if o.Kind != KindView {
			out = append(out, "DROP TABLE "+d.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name})+";")
		}
	}
	return out
}

func (c *Conn) cassRunAll(ctx context.Context, stmts []string) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	for i, st := range stmts {
		if r := c.cassRun(ctx, st, 1); r.Err != nil {
			if i > 0 {
				return fmt.Errorf("%s: %w (the %d statement(s) before it were applied)", st, r.Err, i)
			}
			return fmt.Errorf("%s: %w", st, r.Err)
		}
	}
	return nil
}

func (c *Conn) cassDrop(ctx context.Context, objs []Object) ([]string, error) {
	stmts := buildCassDrop(c.Dialect, objs)
	return stmts, c.cassRunAll(ctx, stmts)
}

func (c *Conn) cassAddColumn(ctx context.Context, t TableRef, def ColumnDef) ([]string, error) {
	if strings.TrimSpace(def.Type) == "" {
		return nil, errors.New("choose a type")
	}
	stmts := []string{"ALTER TABLE " + c.Dialect.QualifiedName(t) + " ADD " + c.Dialect.QuoteIdent(def.Name) + " " + def.Type + ";"}
	return stmts, c.cassRunAll(ctx, stmts)
}

// cassAlter does what CQL can change: columns added or dropped, indexes,
// the comment, emptying the table.
func (c *Conn) cassAlter(ctx context.Context, t TableRef, ops []AlterOp, dryRun bool) ([]string, error) {
	name := c.Dialect.QualifiedName(t)
	q := c.Dialect.QuoteIdent
	var stmts []string
	for _, op := range ops {
		switch op.Kind {
		case AlterAddColumn:
			if op.Def == nil {
				return nil, errors.New("add column: no column")
			}
			stmts = append(stmts, "ALTER TABLE "+name+" ADD "+q(op.Def.Name)+" "+op.Def.Type+";")
		case AlterDropColumn:
			stmts = append(stmts, "ALTER TABLE "+name+" DROP "+q(op.Column)+";")
		case AlterAddIndex:
			if op.Index == nil || len(op.Index.Columns) != 1 {
				return nil, errors.New("a Cassandra index is on one column")
			}
			ix := "CREATE INDEX "
			if op.Index.Name != "" {
				ix += q(op.Index.Name) + " "
			}
			stmts = append(stmts, ix+"ON "+name+" ("+q(op.Index.Columns[0])+");")
		case AlterDropIndex:
			stmts = append(stmts, "DROP INDEX "+c.Dialect.QualifiedName(TableRef{Schema: t.Schema, Name: op.Name})+";")
		case AlterTableComment:
			stmts = append(stmts, "ALTER TABLE "+name+" WITH comment = "+quoteCQL(op.Comment)+";")
		case AlterTruncate:
			stmts = append(stmts, "TRUNCATE "+name+";")
		default:
			return nil, fmt.Errorf("Cassandra can't %s: its tables keep their primary key, and columns their type", strings.ReplaceAll(string(op.Kind), "_", " "))
		}
	}
	if dryRun {
		return stmts, nil
	}
	return stmts, c.cassRunAll(ctx, stmts)
}

// ---------------------------------------------------------------- export and import

// cassDump writes CREATE statements and INSERTs.
func (c *Conn) cassDump(ctx context.Context, w io.Writer, o DumpOptions, progress func(DumpProgress)) error {
	p := DumpProgress{Tables: len(o.Objects)}
	fmt.Fprintf(w, "-- GoTable dump of %s, %s\n\n", c.cass.server, time.Now().UTC().Format(time.RFC3339))
	for i, ob := range o.Objects {
		p.Table, p.TableIndex = ob.Name, i
		t := TableRef{Schema: ob.Schema, Name: ob.Name}
		if o.Structure {
			if err := c.lock(ctx); err != nil {
				return err
			}
			def, err := (cassEngine{}).definition(ctx, c, ob)
			c.mu.Unlock()
			if err != nil {
				return err
			}
			if o.DropFirst {
				what := "TABLE"
				if ob.Kind == KindView {
					what = "MATERIALIZED VIEW"
				}
				fmt.Fprintf(w, "DROP %s IF EXISTS %s;\n", what, c.Dialect.QuoteIdent(ob.Name))
			}
			fmt.Fprintln(w, unqualify(c.Dialect, ob.Schema, cassMainStatement(def)))
			fmt.Fprintln(w)
		}
		if !o.Data || ob.Kind == KindView {
			continue
		}
		if err := c.lock(ctx); err != nil {
			return err
		}
		s, err := c.cassFor("")
		if err != nil {
			c.mu.Unlock()
			return err
		}
		iter := s.Query("SELECT * FROM " + c.Dialect.QualifiedName(t)).WithContext(ctx).PageSize(cassPageSize).Iter()
		infos := iter.Columns()
		names := make([]string, len(infos))
		for i, ci := range infos {
			names[i] = c.Dialect.QuoteIdent(ci.Name)
		}
		head := "INSERT INTO " + c.Dialect.QuoteIdent(ob.Name) + " (" + strings.Join(names, ", ") + ") VALUES ("
		for {
			vals, ok := cassScan(iter)
			if !ok {
				break
			}
			lits := make([]string, len(vals))
			for i, v := range vals {
				lits[i] = cqlLiteralOf(v, infos[i].TypeInfo)
			}
			fmt.Fprintln(w, head+strings.Join(lits, ", ")+");")
			p.Rows++
			if progress != nil && p.Rows%1000 == 0 {
				progress(p)
			}
		}
		err = iter.Close()
		c.mu.Unlock()
		if err != nil {
			return err
		}
		fmt.Fprintln(w)
	}
	if progress != nil {
		progress(p)
	}
	return nil
}

// cassMainStatement: DESCRIBE adds a table's indexes after it; the dump
// keeps them all, each ending with a semicolon.
func cassMainStatement(def string) string {
	def = strings.TrimSpace(def)
	if !strings.HasSuffix(def, ";") {
		def += ";"
	}
	return def
}

// cassImportScript runs a CQL script, statement by statement.
func (c *Conn) cassImportScript(ctx context.Context, r io.Reader, o ImportOptions, progress func(*ImportResult)) (*ImportResult, error) {
	if o.MaxErrors <= 0 {
		o.MaxErrors = 200
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	text := string(data)
	res := &ImportResult{Errors: []ImportError{}}
	if err := c.lock(ctx); err != nil {
		return res, err
	}
	defer c.mu.Unlock()
	for _, sp := range SplitStatements(c.Config.Driver, text) {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		rr := c.cassRun(ctx, sp.SQL, 1)
		res.Statements++
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sp.SQL)), "INSERT") && rr.Err == nil {
			res.Rows++
		}
		if rr.Err != nil {
			res.Failed++
			line := strings.Count(text[:sp.Start], "\n") + 1
			if len(res.Errors) < o.MaxErrors {
				res.Errors = append(res.Errors, ImportError{Line: line, Statement: snippet(sp.SQL), Message: rr.Err.Error()})
			}
			if !o.ContinueOnError {
				return res, fmt.Errorf("line %d: %w", line, rr.Err)
			}
		}
		if progress != nil && res.Statements%100 == 0 {
			progress(res)
		}
	}
	if progress != nil {
		progress(res)
	}
	return res, nil
}

// cassImportCSV inserts CSV records, each value written as its column's type.
func (c *Conn) cassImportCSV(ctx context.Context, r io.Reader, t TableRef, o CSVImportOptions, progress func(int64)) (int64, error) {
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	s, err := c.cassFor("")
	if err != nil {
		c.mu.Unlock()
		return 0, err
	}
	cols, err := cassColumns(ctx, s, t)
	c.mu.Unlock()
	if err != nil {
		return 0, err
	}
	typ := map[string]string{}
	for _, cc := range cols {
		typ[cc.name] = cc.typ
	}
	if o.Truncate {
		if err := c.cassRunAll(ctx, []string{"TRUNCATE " + c.Dialect.QualifiedName(t)}); err != nil {
			return 0, err
		}
	}
	names := make([]string, len(o.Columns))
	for i, col := range o.Columns {
		if _, ok := typ[col]; !ok {
			return 0, fmt.Errorf("no column %s", col)
		}
		names[i] = c.Dialect.QuoteIdent(col)
	}
	head := "INSERT INTO " + c.Dialect.QualifiedName(t) + " (" + strings.Join(names, ", ") + ") VALUES ("
	var n int64
	err = readCSV(r, o, func(line int, rec []string) error {
		lits := make([]string, len(o.Columns))
		for i, col := range o.Columns {
			v := Text(rec[o.Mapping[i]])
			if o.EmptyAsNull && v.Text == "" || o.NullText != "" && v.Text == o.NullText {
				v = NullValue
			}
			lit, err := cqlLiteral(typ[col], v)
			if err != nil {
				return fmt.Errorf("line %d, %s: %w", line, col, err)
			}
			lits[i] = lit
		}
		if err := c.cassRunAll(ctx, []string{head + strings.Join(lits, ", ") + ")"}); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		n++
		if progress != nil && n%500 == 0 {
			progress(n)
		}
		return nil
	})
	return n, err
}

// readCSV calls fn with each record (and its line), read as ImportCSV
// reads them: the delimiter, a header to skip, lazy quotes. Fields the
// mapping points past are empty.
func readCSV(r io.Reader, o CSVImportOptions, fn func(line int, rec []string) error) error {
	if len(o.Columns) == 0 || len(o.Columns) != len(o.Mapping) {
		return errors.New("choose at least one column to import")
	}
	if o.Delimiter == 0 {
		o.Delimiter = ','
	}
	cr := csv.NewReader(r)
	cr.Comma = o.Delimiter
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	first := true
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("CSV: %w", err)
		}
		line, _ := cr.FieldPos(0)
		if first {
			first = false
			if o.Header {
				continue
			}
		}
		full := rec
		for _, idx := range o.Mapping {
			if idx >= len(full) {
				full = append(full, make([]string, idx+1-len(full))...)
			}
		}
		if err := fn(line, full); err != nil {
			return err
		}
	}
}
