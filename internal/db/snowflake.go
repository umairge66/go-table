package db

// Snowflake, a cloud warehouse, through its own Go client, wrapped in a
// database/sql driver of ours (sfConn) so values come out exact.
//
//   - Queries run on a warehouse, which is billed while it runs. The catalog
//     is read with SHOW and DESCRIBE, which need none, and rows are counted
//     only where Snowflake knows the number without reading them.
//   - Keys are kept but not enforced, so a "primary key" may match several
//     rows: an edit that would change more than one is undone.
//   - A foreign key with an action (ON DELETE CASCADE…) is accepted and
//     silently not created, so GoTable refuses those.
//   - DROP TABLE drops other tables' keys to it unless told RESTRICT.
//   - DDL commits by itself. JSON, binary and dates need conversions that
//     VALUES doesn't take, so writes are INSERT … SELECT … FROM VALUES.
//   - Names written without quotes are stored in upper case.

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sf "github.com/snowflakedb/gosnowflake/v2"
	"github.com/youmark/pkcs8"

	"gotable/internal/model"
)

type snowflakeEngine struct {
	// views holds what is known not to be a plain table, by schema and name
	// (its count would run a query); guarded by the Conn's mu.
	views map[TableRef]bool
}

func init() {
	openers[model.DriverSnowflake] = func(cfg *model.Connection, _, password string) (*sql.DB, engine, error) {
		sc, err := snowflakeConfig(cfg, password)
		if err != nil {
			return nil, nil, err
		}
		quietSnowflake()
		return sql.OpenDB(sfConnector{sf.NewConnector(sf.SnowflakeDriver{}, *sc)}), &snowflakeEngine{views: map[TableRef]bool{}}, nil
	}
}

// quietSnowflake keeps the client to the job: it would otherwise unpack and
// load a native library, ask cloud metadata addresses where it runs, and
// log every failed statement.
var quietSnowflake = sync.OnceFunc(func() {
	os.Setenv("SF_DISABLE_MINICORE", "true")
	os.Setenv("SNOWFLAKE_DISABLE_PLATFORM_DETECTION", "true")
	_ = sf.GetLogger().SetLogLevel("OFF")
})

// sfTransport answers the client's telemetry itself, so it stays here:
// Snowflake ignores CLIENT_TELEMETRY_ENABLED = false from this client.
type sfTransport struct{ base http.RoundTripper }

func (t sfTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(r.URL.Path, "/telemetry/") {
		return t.base.RoundTrip(r)
	}
	if r.Body != nil {
		r.Body.Close()
	}
	return &http.Response{Status: "200 OK", StatusCode: http.StatusOK, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Request: r,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   io.NopCloser(strings.NewReader(`{"data":"Log Received","code":null,"message":null,"success":true}`))}, nil
}

func (t sfTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// SignInTime is how long opening a connection may take: a browser sign-in
// waits for the user.
func SignInTime(cfg *model.Connection, usual time.Duration) time.Duration {
	if cfg.Driver == model.DriverSnowflake && cfg.Auth == model.AuthBrowser {
		return 3 * time.Minute
	}
	if cfg.Driver == model.DriverSnowflake { // a push to confirm, maybe
		return max(usual, time.Minute)
	}
	return usual
}

var sfHostSuffix = regexp.MustCompile(`(?i)\.snowflakecomputing\.(com|cn)$`)

func snowflakeConfig(cfg *model.Connection, password string) (*sf.Config, error) {
	account := strings.TrimSpace(cfg.Host)
	if _, rest, ok := strings.Cut(account, "://"); ok {
		account = rest
	}
	account, _, _ = strings.Cut(account, "/")
	c := &sf.Config{
		User: cfg.User, Warehouse: cfg.Warehouse, Role: cfg.Role, Application: "GoTable",
		ValidateDefaultParameters: sf.ConfigBoolTrue,
		Transporter:               sfTransport{http.DefaultTransport.(*http.Transport).Clone()},
		// The client's own stores and caches stay unused.
		ClientRequestMfaToken:          sf.ConfigBoolFalse,
		ClientStoreTemporaryCredential: sf.ConfigBoolFalse,
		LoginTimeout:                   time.Minute,
		CleanupTimeout:                 10 * time.Second,
	}
	if sfHostSuffix.MatchString(account) { // a whole host name, e.g. a private link's
		c.Host, c.Port, c.Protocol = account, 443, "https"
		account, _, _ = strings.Cut(sfHostSuffix.ReplaceAllString(account, ""), ".")
	}
	c.Account = account
	switch cfg.Auth {
	case "":
		c.Password = password
	case model.AuthToken:
		c.Authenticator, c.Token = sf.AuthTypePat, password
	case model.AuthBrowser:
		c.Authenticator, c.ExternalBrowserTimeout = sf.AuthTypeExternalBrowser, 3*time.Minute
	case model.AuthKey:
		k, err := snowflakeKey(cfg.KeyPath, password)
		if err != nil {
			return nil, err
		}
		c.Authenticator, c.PrivateKey = sf.AuthTypeJwt, k
	}
	return c, nil
}

// snowflakeKey reads an RSA private key (PEM, encrypted or not).
func snowflakeKey(path, passphrase string) (*rsa.PrivateKey, error) {
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the private key: %w", err)
	}
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, errors.New("the private key file isn't PEM: choose the .p8 file made for Snowflake")
	}
	var k any
	switch p.Type {
	case "RSA PRIVATE KEY":
		k, err = x509.ParsePKCS1PrivateKey(p.Bytes)
	case "ENCRYPTED PRIVATE KEY":
		if passphrase == "" {
			return nil, errors.New("the private key is encrypted: enter its passphrase")
		}
		if k, err = pkcs8.ParsePKCS8PrivateKey(p.Bytes, []byte(passphrase)); err != nil {
			return nil, errors.New("the private key couldn't be decrypted: the passphrase is wrong")
		}
	case "PUBLIC KEY":
		return nil, errors.New("this is the public key: choose the private key file")
	default:
		k, err = x509.ParsePKCS8PrivateKey(p.Bytes)
	}
	if err != nil {
		return nil, fmt.Errorf("the private key: %w", err)
	}
	r, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("Snowflake takes RSA keys only")
	}
	return r, nil
}

// ------------------------------------------------------------- the driver

type sfConnector struct{ driver.Connector }

func (c sfConnector) Connect(ctx context.Context) (driver.Conn, error) {
	in, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, sfLoginError(err)
	}
	return &sfConn{in}, nil
}

// sfFull is what the client's connection implements.
type sfFull interface {
	driver.Conn
	driver.ExecerContext
	driver.QueryerContext
	driver.ConnBeginTx
	driver.NamedValueChecker
}

type sfConn struct{ in driver.Conn }

func (c *sfConn) full() sfFull                               { return c.in.(sfFull) }
func (c *sfConn) Prepare(q string) (driver.Stmt, error)      { return c.in.Prepare(q) }
func (c *sfConn) Close() error                               { return c.in.Close() }
func (c *sfConn) Begin() (driver.Tx, error)                  { return nil, errors.New("use BeginTx") }
func (c *sfConn) CheckNamedValue(v *driver.NamedValue) error { return c.full().CheckNamedValue(v) }

func (c *sfConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	tx, err := c.full().BeginTx(ctx, o)
	return tx, sfError(err)
}

// sfFiles are statements that copy files between this computer and a stage.
var sfFiles = regexp.MustCompile(`(?i)^(PUT|GET)\s`)

func sfCheck(q string) error {
	if sfFiles.MatchString(stripLeadingComments(q)) {
		return errors.New("GoTable doesn't run PUT and GET, which copy files between this computer and a stage")
	}
	return nil
}

func (c *sfConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := sfCheck(q); err != nil {
		return nil, err
	}
	r, err := c.full().ExecContext(ctx, q, args)
	if err != nil {
		return nil, sfContext(ctx, err)
	}
	return r, nil
}

func (c *sfConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := sfCheck(q); err != nil {
		return nil, err
	}
	id := make(chan string, 1)
	rows, err := c.full().QueryContext(sf.WithQueryIDChan(ctx, id), q, args)
	if err != nil && strings.Contains(err.Error(), "unsupported data type") {
		select {
		case qid := <-id:
			return c.readVectors(ctx, qid)
		default:
		}
	}
	if err != nil {
		return nil, sfContext(ctx, err)
	}
	return newSFRows(rows, nil), nil
}

var sfQueryID = regexp.MustCompile(`^[0-9a-fA-F-]+$`)

// readVectors reads a finished query's result again with its VECTOR columns
// as arrays: the client can't decode vectors.
func (c *sfConn) readVectors(ctx context.Context, id string) (driver.Rows, error) {
	if !sfQueryID.MatchString(id) {
		return nil, errors.New("the result has a type that can't be shown")
	}
	d, err := c.full().QueryContext(ctx, "DESCRIBE RESULT '"+id+"'", nil)
	if err != nil {
		return nil, sfContext(ctx, err)
	}
	desc, err := sfReadAll(newSFRows(d, nil))
	if err != nil {
		return nil, sfContext(ctx, err)
	}
	var exprs []string
	vec := map[int]bool{}
	for i, r := range desc {
		e := "$" + strconv.Itoa(i+1)
		if strings.HasPrefix(r["type"], "VECTOR") {
			e += "::ARRAY"
			vec[i] = true
		}
		exprs = append(exprs, e+` AS "`+strings.ReplaceAll(r["name"], `"`, `""`)+`"`)
	}
	rows, err := c.full().QueryContext(ctx, "SELECT "+strings.Join(exprs, ", ")+" FROM TABLE(RESULT_SCAN('"+id+"'))", nil)
	if err != nil {
		return nil, sfContext(ctx, err)
	}
	return newSFRows(rows, vec), nil
}

// sfReadAll reads a SHOW or DESCRIBE answer as maps by column name.
func sfReadAll(rows driver.Rows) ([]map[string]string, error) {
	defer rows.Close()
	names := rows.Columns()
	dest := make([]driver.Value, len(names))
	var out []map[string]string
	for {
		err := rows.Next(dest)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		m := make(map[string]string, len(names))
		for i, n := range names {
			if dest[i] != nil {
				m[n] = fmt.Sprint(dest[i])
			}
		}
		out = append(out, m)
	}
}

// sfContext prefers the context's error (a cancelled query) to what the
// client says about it.
func sfContext(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return sfError(err)
}

// sfError tidies an error: the statement's own message, or, when the session
// is gone, ErrBadConn so that a new one is opened.
func sfError(err error) error {
	var e *sf.SnowflakeError
	if !errors.As(err, &e) {
		return err
	}
	switch e.Number {
	case 390111, 390112, 390114, 390115: // session gone, its token or master token expired
		return driver.ErrBadConn
	case 606: // no active warehouse
		return fmt.Errorf("no warehouse is selected: choose one in the connection, or run USE WAREHOUSE name (%w)", e)
	}
	return err
}

// sfLoginError explains a failed sign-in.
func sfLoginError(err error) error {
	var e *sf.SnowflakeError
	if !errors.As(err, &e) {
		return err
	}
	m := strings.ToLower(e.Message)
	switch {
	case e.Number == 390189 || e.Number == 390201: // the role, the warehouse
		return err
	case e.Number == 390100:
		return fmt.Errorf("authentication failed: wrong user name or password (%w)", e)
	case e.Number == 390144 || strings.Contains(m, "jwt token is invalid"):
		return fmt.Errorf("authentication failed: Snowflake doesn't know this key for this user; set it with ALTER USER … SET RSA_PUBLIC_KEY = '…' (%w)", e)
	case e.Number == 390913 || e.Number == 390422 || strings.Contains(m, "network policy"):
		return fmt.Errorf("authentication failed: a network policy of the account refuses this (%w)", e)
	case strings.Contains(m, "mfa") || strings.Contains(m, "multi-factor") || strings.Contains(m, "duo"):
		return fmt.Errorf("authentication failed: this user needs a second factor; sign in with a key pair, an access token or the browser (%w)", e)
	case e.Number >= 390100 && e.Number < 390400 || e.Number >= 394300 && e.Number < 394600:
		return fmt.Errorf("authentication failed (%w)", e)
	case e.Number == 261004 || e.Number == 260008 || strings.Contains(m, "403") || strings.Contains(m, "404"):
		return errors.New("no Snowflake account answers there: check the account identifier, written ORGANIZATION-ACCOUNT with a dash")
	}
	return err
}

// snowBinary is a BINARY value, which is always shown as hex.
type snowBinary []byte

// sfRows turns the client's values into text that reads back exactly.
type sfRows struct {
	in    driver.Rows
	types []string
}

func newSFRows(in driver.Rows, vectors map[int]bool) *sfRows {
	r := &sfRows{in: in, types: make([]string, len(in.Columns()))}
	tn, ok := in.(driver.RowsColumnTypeDatabaseTypeName)
	for i := range r.types {
		if ok {
			r.types[i] = sfTypeName(tn.ColumnTypeDatabaseTypeName(i))
		}
		if vectors[i] {
			r.types[i] = "VECTOR"
		}
	}
	return r
}

// sfTypeName is a type's name in SQL.
func sfTypeName(t string) string {
	switch t {
	case "FIXED":
		return "NUMBER"
	case "REAL":
		return "FLOAT"
	case "TEXT":
		return "VARCHAR"
	}
	return t
}

func (r *sfRows) Columns() []string                       { return r.in.Columns() }
func (r *sfRows) Close() error                            { return r.in.Close() }
func (r *sfRows) ColumnTypeDatabaseTypeName(i int) string { return r.types[i] }

func (r *sfRows) Next(dest []driver.Value) error {
	if err := r.in.Next(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return io.EOF
		}
		return sfError(err)
	}
	for i, v := range dest {
		switch x := v.(type) {
		case time.Time:
			dest[i] = sfTime(x, r.types[i])
		case []byte:
			dest[i] = snowBinary(bytes.Clone(x))
		case string:
			switch r.types[i] {
			case "VARIANT", "OBJECT", "ARRAY", "MAP", "VECTOR":
				dest[i] = sfCompact(x)
			}
		}
	}
	return nil
}

// sfTime writes a date or time with all its digits (nine).
func sfTime(t time.Time, typ string) string {
	switch typ {
	case "DATE":
		return t.Format("2006-01-02")
	case "TIME":
		return t.Format("15:04:05.999999999")
	case "TIMESTAMP_LTZ", "TIMESTAMP_TZ":
		return t.Format("2006-01-02 15:04:05.999999999 -07:00")
	}
	return t.Format("2006-01-02 15:04:05.999999999")
}

// sfCompact puts JSON on one line, its numbers as they are. Semi-structured
// values that aren't JSON (undefined, NaN) stay as they come.
func sfCompact(s string) string {
	var b bytes.Buffer
	if json.Compact(&b, []byte(s)) != nil {
		return s
	}
	return b.String()
}

// ------------------------------------------------------------- the engine

// sfShow runs a SHOW or DESCRIBE. Callers hold c.mu.
func sfShow(ctx context.Context, c *Conn, q string) ([]map[string]string, error) {
	rows, err := c.conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	vals := make([]any, len(names))
	ptrs := make([]any, len(names))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	var out []map[string]string
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]string, len(names))
		for i, n := range names {
			if vals[i] != nil {
				m[n] = fmt.Sprint(vals[i])
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// sfLike is a SHOW … LIKE pattern that matches one name (in any case).
func sfLike(name string) string {
	return "'" + strings.NewReplacer(`\`, `\\\\`, `'`, `''`, `_`, `\\_`, `%`, `\\%`).Replace(name) + "'"
}

// initSession opens the connection's database: the name as it is, or, typed
// in another case, as Snowflake reads a name without quotes.
func (e *snowflakeEngine) initSession(ctx context.Context, c *Conn) error {
	db := strings.TrimSpace(c.Config.DBName)
	if db == "" {
		return nil
	}
	_, err := c.conn.ExecContext(ctx, "USE DATABASE "+c.Dialect.QuoteIdent(db))
	if up := strings.ToUpper(db); err != nil && up != db {
		if _, err2 := c.conn.ExecContext(ctx, "USE DATABASE "+c.Dialect.QuoteIdent(up)); err2 == nil {
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("the database %s doesn't exist, or the role can't use it (%w)", db, err)
	}
	return nil
}

func (*snowflakeEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var v string
	err := c.conn.QueryRowContext(ctx, "SELECT CURRENT_VERSION()").Scan(&v)
	return "Snowflake " + v, err
}

func (*snowflakeEngine) databases(ctx context.Context, c *Conn) ([]string, error) {
	rows, err := sfShow(ctx, c, "SHOW TERSE DATABASES")
	out := []string{}
	for _, r := range rows {
		out = append(out, r["name"])
	}
	sort.Strings(out)
	return out, err
}

// schemas lists none until a database is chosen.
func (*snowflakeEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	db, err := sfDatabase(ctx, c)
	if err != nil || db == "" {
		return []string{}, err
	}
	rows, err := sfShow(ctx, c, "SHOW TERSE SCHEMAS IN DATABASE "+c.Dialect.QuoteIdent(db))
	out := []string{}
	for _, r := range rows {
		if r["name"] != "INFORMATION_SCHEMA" {
			out = append(out, r["name"])
		}
	}
	sort.Strings(out)
	return out, err
}

func sfDatabase(ctx context.Context, c *Conn) (string, error) {
	var s sql.NullString
	err := c.conn.QueryRowContext(ctx, "SELECT CURRENT_DATABASE()").Scan(&s)
	return s.String, err
}

func (*snowflakeEngine) currentSchema(ctx context.Context, c *Conn) (string, error) {
	var s sql.NullString
	err := c.conn.QueryRowContext(ctx, "SELECT CURRENT_SCHEMA()").Scan(&s)
	return s.String, err
}

func (*snowflakeEngine) useSchema(ctx context.Context, c *Conn, schema string) error {
	_, err := c.conn.ExecContext(ctx, "USE SCHEMA "+c.Dialect.QuoteIdent(schema))
	return err
}

const sfShowPage = 10000 // the most a SHOW returns

func (e *snowflakeEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	out := []Object{}
	from := ""
	for {
		q := "SHOW OBJECTS IN SCHEMA " + c.Dialect.QuoteIdent(schema) + " LIMIT " + strconv.Itoa(sfShowPage)
		if from != "" {
			q += " FROM '" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(from) + "'"
		}
		rows, err := sfShow(ctx, c, q)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			k := KindTable
			if r["kind"] != "TABLE" && r["kind"] != "TRANSIENT" && r["kind"] != "TEMPORARY" || r["is_dynamic"] == "Y" {
				k = KindView // views, and tables that rows can't be written to
			}
			e.views[TableRef{Schema: schema, Name: r["name"]}] = k == KindView
			out = append(out, Object{Schema: schema, Name: r["name"], Kind: k})
		}
		if len(rows) < sfShowPage {
			break
		}
		from = rows[len(rows)-1]["name"]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// isView reports an object whose count would run a query.
func (e *snowflakeEngine) isView(ctx context.Context, c *Conn, t TableRef) bool {
	if v, ok := e.views[t]; ok {
		return v
	}
	k, err := sfKind(ctx, c, t)
	v := err != nil || k != "TABLE"
	if err == nil {
		e.views[t] = v
	}
	return v
}

// sfKind is what an object is in DDL: TABLE, VIEW, MATERIALIZED VIEW,
// DYNAMIC TABLE, EXTERNAL TABLE… Callers hold c.mu.
func sfKind(ctx context.Context, c *Conn, t TableRef) (string, error) {
	in := " LIKE " + sfLike(t.Name) + " IN SCHEMA " + c.Dialect.QuoteIdent(t.Schema)
	rows, err := sfShow(ctx, c, "SHOW TABLES"+in)
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r["name"] != t.Name {
			continue
		}
		for col, kind := range map[string]string{"is_dynamic": "DYNAMIC TABLE", "is_external": "EXTERNAL TABLE", "is_event": "EVENT TABLE"} {
			if r[col] == "Y" {
				return kind, nil
			}
		}
		return "TABLE", nil
	}
	if rows, err = sfShow(ctx, c, "SHOW VIEWS"+in); err != nil {
		return "", err
	}
	for _, r := range rows {
		if r["name"] == t.Name {
			if r["is_materialized"] == "true" {
				return "MATERIALIZED VIEW", nil
			}
			return "VIEW", nil
		}
	}
	if rows, err = sfShow(ctx, c, "SHOW OBJECTS"+in); err != nil {
		return "", err
	}
	for _, r := range rows {
		if r["name"] == t.Name && r["kind"] != "" {
			return r["kind"], nil
		}
	}
	return "", fmt.Errorf("%s not found", c.Dialect.QualifiedName(t))
}

var sfCastDefault = regexp.MustCompile(`(?s)^CAST\('((?:[^']|'')*)' AS [A-Z_0-9(), ]+\)$`)

func (*snowflakeEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	rows, err := sfShow(ctx, c, "DESCRIBE TABLE "+c.Dialect.QualifiedName(t))
	if err != nil {
		return nil, err
	}
	var out []Column
	for _, r := range rows {
		col := Column{Name: r["name"], Type: r["type"], Nullable: r["null?"] == "Y", PrimaryKey: r["primary key"] == "Y", Comment: r["comment"]}
		if i := strings.Index(col.Type, " COLLATE '"); i >= 0 {
			col.Collation = strings.TrimSuffix(col.Type[i+len(" COLLATE '"):], "'")
			col.Type = col.Type[:i]
		}
		if def, ok := r["default"]; ok && def != "" {
			switch {
			case strings.HasPrefix(def, "IDENTITY "):
				col.AutoIncrement = true
			default:
				col.Default = &def
				if m := sfCastDefault.FindStringSubmatch(def); m != nil {
					col.DefaultKind, col.DefaultText = DefaultValue, strings.ReplaceAll(m[1], "''", "'")
				} else if strings.EqualFold(def, "true") || strings.EqualFold(def, "false") {
					col.DefaultKind, col.DefaultText = DefaultValue, strings.ToLower(def)
				} else {
					col.DefaultKind, col.DefaultText = mssqlDefault(def) // 'text', 12, f()
					if col.DefaultKind == DefaultValue && strings.HasPrefix(def, "'") {
						col.DefaultText = strings.ReplaceAll(col.DefaultText, `\\`, `\`)
					}
				}
			}
		}
		out = append(out, col)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("table %s not found", c.Dialect.QualifiedName(t))
	}
	return out, nil
}

// indexes: Snowflake has none, but primary and unique keys (not enforced).
func (*snowflakeEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	out := []Index{}
	for _, kind := range []string{"PRIMARY", "UNIQUE"} {
		rows, err := sfShow(ctx, c, "SHOW "+kind+" KEYS IN TABLE "+c.Dialect.QualifiedName(t))
		if err != nil {
			return nil, err
		}
		sfByKey(rows, "constraint_name")
		for _, r := range rows {
			if n := len(out); n == 0 || out[n-1].Name != r["constraint_name"] {
				out = append(out, Index{Name: r["constraint_name"], Unique: true, Primary: kind == "PRIMARY", Constraint: true, Type: "not enforced"})
			}
			x := &out[len(out)-1]
			x.Columns = append(x.Columns, r["column_name"])
		}
	}
	return out, nil
}

// sfByKey orders key columns by constraint and position.
func sfByKey(rows []map[string]string, name string) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i][name] != rows[j][name] {
			return rows[i][name] < rows[j][name]
		}
		a, _ := strconv.Atoi(rows[i]["key_sequence"])
		b, _ := strconv.Atoi(rows[j]["key_sequence"])
		return a < b
	})
}

func (*snowflakeEngine) foreignKeys(ctx context.Context, c *Conn, t TableRef) ([]ForeignRef, error) {
	rows, err := sfShow(ctx, c, "SHOW IMPORTED KEYS IN TABLE "+c.Dialect.QualifiedName(t))
	if err != nil {
		return nil, err
	}
	sfByKey(rows, "fk_name")
	var out []ForeignRef
	for _, r := range rows {
		if n := len(out); n == 0 || out[n-1].Name != r["fk_name"] {
			out = append(out, ForeignRef{Name: r["fk_name"], Schema: r["pk_schema_name"], Table: r["pk_table_name"], OnDelete: r["delete_rule"], OnUpdate: r["update_rule"]})
		}
		fk := &out[len(out)-1]
		fk.From = append(fk.From, r["fk_column_name"])
		fk.Columns = append(fk.Columns, r["pk_column_name"])
	}
	return out, nil
}

var sfOrReplace = regexp.MustCompile(`(?i)^\s*create or replace `)

// definition is Snowflake's own DDL, without the OR REPLACE that would drop
// a table of the same name where it's run.
func (*snowflakeEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	kind := "TABLE"
	if o.Kind == KindView {
		kind = "VIEW"
		if k, err := sfKind(ctx, c, TableRef{Schema: o.Schema, Name: o.Name}); err == nil && strings.HasSuffix(k, "TABLE") {
			kind = "TABLE"
		}
	}
	name := c.Dialect.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name})
	var ddl string
	err := c.conn.QueryRowContext(ctx, "SELECT GET_DDL('"+kind+"', "+c.Dialect.QuoteLiteral(Text(name))+")").Scan(&ddl)
	return strings.TrimSpace(sfOrReplace.ReplaceAllString(ddl, "CREATE ")), err
}

func (*snowflakeEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	rows, err := sfShow(ctx, c, "SHOW TABLES LIKE "+sfLike(t.Name)+" IN SCHEMA "+c.Dialect.QuoteIdent(t.Schema))
	if err != nil {
		return TableInfo{}, err
	}
	for _, r := range rows {
		if r["name"] != t.Name {
			continue
		}
		ti := TableInfo{Comment: r["comment"]}
		var parts []string
		if k := r["kind"]; k != "TABLE" && k != "" {
			parts = append(parts, strings.ToLower(k))
		}
		if cb := r["cluster_by"]; cb != "" {
			parts = append(parts, "clustered by "+strings.TrimPrefix(cb, "LINEAR"))
		}
		switch d := r["retention_time"]; d {
		case "", "0":
		case "1":
			parts = append(parts, "time travel 1 day")
		default:
			parts = append(parts, "time travel "+d+" days")
		}
		ti.Options = strings.Join(parts, " · ")
		return ti, nil
	}
	return TableInfo{}, nil
}

// ------------------------------------------------------------- writing

// sfBase is a type without its sizes: NUMBER of NUMBER(38,0).
func sfBase(typ string) string {
	typ = strings.ToUpper(strings.TrimSpace(typ))
	if i := strings.IndexAny(typ, "( "); i >= 0 {
		typ = typ[:i]
	}
	return typ
}

var (
	sfISODate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	sfISOTime = regexp.MustCompile(`^(\d{1,2}):(\d{2})(?::(\d{2})(?:\.(\d{1,9}))?)?$`)
	sfISOFull = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})(?:[ T](\d{1,2}:\d{2}(?::\d{2}(?:\.\d{1,9})?)?))?\s*(Z|[+-]\d{2}(?::?\d{2})?)?$`)
)

const (
	sfFmtDate = "YYYY-MM-DD"
	sfFmtTime = "HH24:MI:SS.FF9"
	sfFmtFull = sfFmtDate + " " + sfFmtTime
	sfFmtZone = sfFmtFull + " TZH:TZM"
)

// sfISO rewrites an ISO date or time in full, with its format; "" when it
// isn't one (the session's own format then reads it). Even the format AUTO
// means the session's where that is set.
func sfISO(typ, v string) (text, format string) {
	v = strings.TrimSpace(v)
	clock := func(s string) string {
		m := sfISOTime.FindStringSubmatch(s)
		if m == nil {
			return ""
		}
		return fmt.Sprintf("%02s:%s:%02s.%s", m[1], m[2], m[3], (m[4] + "000000000")[:9])
	}
	switch b := sfBase(typ); b {
	case "DATE":
		if sfISODate.MatchString(v) {
			return v, sfFmtDate
		}
	case "TIME":
		if t := clock(v); t != "" {
			return t, sfFmtTime
		}
	case "DATETIME", "TIMESTAMP", "TIMESTAMP_NTZ", "TIMESTAMP_LTZ", "TIMESTAMP_TZ":
		m := sfISOFull.FindStringSubmatch(v)
		if m == nil {
			break
		}
		t := "00:00:00.000000000"
		if m[2] != "" {
			t = clock(m[2])
		}
		zone := strings.ReplaceAll(m[3], ":", "")
		switch {
		case zone == "":
			return m[1] + " " + t, sfFmtFull
		case b != "TIMESTAMP_LTZ" && b != "TIMESTAMP_TZ":
			return "", "" // a time zone where none is kept: let Snowflake say
		case zone == "Z":
			zone = "+0000"
		case len(zone) == 3:
			zone += "00"
		}
		return m[1] + " " + t + " " + zone[:3] + ":" + zone[3:], sfFmtZone
	}
	return "", ""
}

// sfValue is sfExpr for one value: the text to bind, and the expression
// around its placeholder.
func sfValue(typ, v string) (text, expr string) {
	if iso, f := sfISO(typ, v); f != "" {
		return iso, "TO_" + sfTimeBase(typ) + "(?, '" + f + "')"
	}
	return v, sfExpr(typ, "?")
}

// sfTimeBase is the type of a date or time in function names.
func sfTimeBase(typ string) string {
	switch b := sfBase(typ); b {
	case "DATETIME", "TIMESTAMP":
		return "TIMESTAMP_NTZ"
	default:
		return b
	}
}

// sfExpr converts v, text as GoTable shows it, to a column's type (a date
// in the session's format).
func sfExpr(typ, v string) string {
	switch b := sfBase(typ); b {
	case "VARIANT":
		return "PARSE_JSON(" + v + ")"
	case "OBJECT", "ARRAY", "MAP", "VECTOR":
		return "PARSE_JSON(" + v + ")::" + typ
	case "BINARY", "VARBINARY":
		return "TO_BINARY(REGEXP_REPLACE(" + v + ", '^0[xX]'), 'HEX')"
	case "GEOGRAPHY", "GEOMETRY":
		return "TO_" + b + "(" + v + ")"
	case "DATE", "TIME", "DATETIME", "TIMESTAMP", "TIMESTAMP_NTZ", "TIMESTAMP_LTZ", "TIMESTAMP_TZ":
		return "TO_" + sfTimeBase(typ) + "(" + v + ")"
	}
	return v
}

// sfMatch compares a column with a value as GoTable shows it, bound as text.
// Geometries have no =, so their GeoJSON is compared.
func sfMatch(d Dialect, col Column, v string) (text, cond string) {
	q := d.QuoteIdent(col.Name)
	text, expr := sfValue(col.Type, v)
	switch sfBase(col.Type) {
	case "GEOGRAPHY", "GEOMETRY":
		return text, "ST_ASGEOJSON(" + q + ") = ST_ASGEOJSON(" + expr + ")"
	}
	return text, q + " = " + expr
}

func sfColumn(cols []Column, name string) (Column, error) {
	i := slices.IndexFunc(cols, func(c Column) bool { return c.Name == name })
	if i < 0 {
		return Column{}, fmt.Errorf("there is no column %q", name)
	}
	return cols[i], nil
}

// sfBuildChange is BuildChange with every value converted to its column's
// type.
func sfBuildChange(d Dialect, t TableRef, cols []Column, c Change) (Statement, error) {
	name := d.QualifiedName(t)
	var args []any
	value := func(n string, v Value) (string, error) {
		col, err := sfColumn(cols, n)
		if err != nil || v.Null {
			return "NULL", err
		}
		text, expr := sfValue(col.Type, v.Text)
		args = append(args, text)
		return expr, nil
	}
	if c.Kind == ChangeInsert {
		if len(c.Values) == 0 {
			return Statement{}, errors.New("Snowflake can't insert a row without values: fill in at least one column")
		}
		var names, exprs []string
		for _, k := range sortedKeys(c.Values) {
			e, err := value(k, c.Values[k])
			if err != nil {
				return Statement{}, err
			}
			names, exprs = append(names, d.QuoteIdent(k)), append(exprs, e)
		}
		return Statement{SQL: "INSERT INTO " + name + " (" + strings.Join(names, ", ") + ") SELECT " + strings.Join(exprs, ", "), Args: args}, nil
	}
	if len(c.Key) == 0 {
		return Statement{}, errors.New("change without row key")
	}
	q := "DELETE FROM " + name
	if c.Kind == ChangeUpdate {
		if len(c.Values) == 0 {
			return Statement{}, errors.New("update without values")
		}
		var sets []string
		for _, k := range sortedKeys(c.Values) {
			e, err := value(k, c.Values[k])
			if err != nil {
				return Statement{}, err
			}
			sets = append(sets, d.QuoteIdent(k)+" = "+e)
		}
		q = "UPDATE " + name + " SET " + strings.Join(sets, ", ")
	}
	var conds []string
	for _, k := range sortedKeys(c.Key) {
		col, err := sfColumn(cols, k)
		if err != nil {
			return Statement{}, err
		}
		if c.Key[k].Null {
			conds = append(conds, d.QuoteIdent(k)+" IS NULL")
			continue
		}
		text, cond := sfMatch(d, col, c.Key[k].Text)
		args = append(args, text)
		conds = append(conds, cond)
	}
	return Statement{SQL: q + " WHERE " + strings.Join(conds, " AND "), Args: args}, nil
}

// buildChanges renders pending edits as statements.
func (c *Conn) buildChanges(ctx context.Context, t TableRef, changes []Change, hasPK bool) ([]Statement, error) {
	var cols []Column
	if c.Dialect.family == model.DriverSnowflake {
		if err := c.lock(ctx); err != nil {
			return nil, err
		}
		var err error
		cols, err = c.eng.columns(ctx, c, t)
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	out := make([]Statement, 0, len(changes))
	for _, ch := range changes {
		var s Statement
		var err error
		if cols != nil {
			s, err = sfBuildChange(c.Dialect, t, cols, ch)
		} else {
			s, err = BuildChange(c.Dialect, t, ch, hasPK)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// sfBulk builds a many-row INSERT, whose values are all text converted by
// one expression for each column. Dates are written as ISO where they can
// be; formats holds each column's format ("" until a value is seen, sfPlain
// for the session's own), and rows that need another one start a new INSERT.
type sfBulk struct {
	d       Dialect
	table   string
	names   []string
	cols    []Column
	formats []string
}

const sfPlain = "-"

func newSFBulk(d Dialect, table string, all []Column, names []string) (*sfBulk, error) {
	b := &sfBulk{d: d, table: table, names: names, formats: make([]string, len(names))}
	for _, n := range names {
		col, err := sfColumn(all, n)
		if err != nil {
			return nil, err
		}
		b.cols = append(b.cols, col)
	}
	return b, nil
}

// row rewrites a row's values (nil for NULL) in place, and reports whether
// it fits the INSERT so far; if not, its formats are the next one's.
func (b *sfBulk) row(vals []any) (fits bool) {
	fits = true
	next := make([]string, len(vals))
	for i, v := range vals {
		s, ok := v.(string)
		if !ok {
			continue
		}
		next[i] = sfPlain
		if iso, f := sfISO(b.cols[i].Type, s); f != "" {
			vals[i], next[i] = iso, f
		}
		fits = fits && (b.formats[i] == "" || b.formats[i] == next[i])
	}
	if !fits {
		return false
	}
	b.use(next)
	return true
}

func (b *sfBulk) use(formats []string) {
	for i, f := range formats {
		if f != "" {
			b.formats[i] = f
		}
	}
}

func (b *sfBulk) reset() { clear(b.formats) }

// prefix is the INSERT up to its VALUES.
func (b *sfBulk) prefix() string {
	exprs := make([]string, len(b.cols))
	for i, col := range b.cols {
		v := "column" + strconv.Itoa(i+1)
		if f := b.formats[i]; f != "" && f != sfPlain {
			exprs[i] = "TO_" + sfTimeBase(col.Type) + "(" + v + ", '" + f + "')"
		} else {
			exprs[i] = sfExpr(col.Type, v)
		}
	}
	return "INSERT INTO " + b.table + " (" + quoteList(b.d, b.names) + ") SELECT " + strings.Join(exprs, ", ") + " FROM VALUES "
}

var sfRowStatements = regexp.MustCompile(`(?i)^(LIST|LS|COPY|EXECUTE|REMOVE|RM)\b`)

// sfReturnsRows: Snowflake's own statements with an answer worth showing.
func sfReturnsRows(stmt string) bool { return sfRowStatements.MatchString(stripLeadingComments(stmt)) }

// searchable: the columns a search of any column can read as text.
func (c *Conn) searchable(ctx context.Context, t TableRef, p Page, names []string) ([]string, error) {
	if c.Dialect.family != model.DriverSnowflake {
		return names, nil
	}
	return sfSearchable(ctx, c, t, p, names)
}

// sfSearchable leaves out the columns that can't be searched as text.
func sfSearchable(ctx context.Context, c *Conn, t TableRef, p Page, names []string) ([]string, error) {
	if !slices.ContainsFunc(p.Filters, func(f Filter) bool { return f.Column == AnyColumn }) {
		return names, nil
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	cols, err := c.eng.columns(ctx, c, t)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		col, err := sfColumn(cols, n)
		if err != nil {
			return nil, err
		}
		switch sfBase(col.Type) {
		case "GEOGRAPHY", "GEOMETRY", "VECTOR", "MAP", "FILE":
		default:
			if !strings.Contains(col.Type, "(") || sfBase(col.Type) != "OBJECT" && sfBase(col.Type) != "ARRAY" { // structured ones have no text
				out = append(out, n)
			}
		}
	}
	return out, nil
}

// CountIsCostly reports that counting the rows of a page would read them,
// which is billed: the UI then counts only when asked.
func (c *Conn) CountIsCostly(ctx context.Context, t TableRef, p Page) bool {
	if x, ok := c.remote.(*bigquery); ok {
		if c.lock(ctx) != nil {
			return true
		}
		defer c.mu.Unlock()
		return x.costlyCount(ctx, t, p)
	}
	e, ok := c.eng.(*snowflakeEngine)
	if !ok {
		return false
	}
	if len(p.Filters) > 0 || strings.TrimSpace(p.RawWhere) != "" {
		return true
	}
	if c.lock(ctx) != nil {
		return true
	}
	defer c.mu.Unlock()
	return e.isView(ctx, c, t)
}

// sfManyRows is the error of an edit that matches several rows.
func sfManyRows(n int64, hasPK bool) error {
	if hasPK {
		return fmt.Errorf("%d rows have this key (Snowflake doesn't enforce keys), so nothing was changed", n)
	}
	return fmt.Errorf("%d identical rows match and the table has no primary key to tell them apart, so nothing was changed", n)
}

// ------------------------------------------------------------- structure

// sfTypes: also VECTOR(FLOAT, 3), MAP(VARCHAR, NUMBER(10,2)), ARRAY(NUMBER).
var sfTypes = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_ ]*(\([A-Za-z0-9_ ,()]*\))?( ?[A-Za-z][A-Za-z0-9_ ]*)?$`)

func sfTypeOK(t string) bool {
	return sfTypes.MatchString(t) && balanced(t)
}

var sfNumber = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)

// sfDefault writes a default value the way its column's type wants it:
// Snowflake refuses DEFAULT '5' on a number.
func sfDefault(d Dialect, typ, v string) string {
	switch b := sfBase(typ); b {
	case "NUMBER", "DECIMAL", "NUMERIC", "DEC", "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "BYTEINT", "FLOAT", "FLOAT4", "FLOAT8", "DOUBLE", "REAL", "DECFLOAT":
		if sfNumber.MatchString(strings.TrimSpace(v)) {
			return strings.TrimSpace(v)
		}
	case "BOOLEAN", "BOOL":
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "t":
			return "TRUE"
		case "false", "0", "no", "f":
			return "FALSE"
		}
	case "DATE", "TIME", "DATETIME", "TIMESTAMP", "TIMESTAMP_NTZ", "TIMESTAMP_LTZ", "TIMESTAMP_TZ":
		return d.QuoteLiteral(Text(v)) + "::" + b
	}
	return d.QuoteLiteral(Text(v))
}

// sfModifyColumn changes a column: one clause for each change.
func sfModifyColumn(d Dialect, t TableRef, old Column, def ColumnDef, alter string) ([]string, error) {
	var out []string
	if def.Collation != old.Collation {
		return nil, errors.New("Snowflake can't change a column's collation")
	}
	if def.DefaultKind != old.DefaultKind || def.DefaultKind != DefaultNone && def.Default != old.DefaultText {
		if def.DefaultKind != DefaultNone && !(def.DefaultKind == DefaultNull && old.DefaultKind == DefaultNone) {
			return nil, errors.New("Snowflake can only remove a column's default, not set another one: add a new column instead")
		}
	}
	name := old.Name
	if def.Name != old.Name {
		out = append(out, alter+"RENAME COLUMN "+d.QuoteIdent(old.Name)+" TO "+d.QuoteIdent(def.Name)+";")
		name = def.Name
	}
	col := d.QuoteIdent(name)
	if newType := strings.TrimSpace(def.Type); !sameType(newType, old.Type) {
		out = append(out, alter+"ALTER COLUMN "+col+" SET DATA TYPE "+newType+";")
	}
	if old.DefaultKind != DefaultNone && def.DefaultKind == DefaultNone {
		out = append(out, alter+"ALTER COLUMN "+col+" DROP DEFAULT;")
	}
	if def.Nullable != old.Nullable {
		if def.Nullable {
			out = append(out, alter+"ALTER COLUMN "+col+" DROP NOT NULL;")
		} else {
			out = append(out, alter+"ALTER COLUMN "+col+" SET NOT NULL;")
		}
	}
	if cm := strings.TrimSpace(def.Comment); cm != old.Comment {
		out = append(out, alter+"ALTER COLUMN "+col+" COMMENT "+d.QuoteLiteral(Text(cm))+";")
	}
	if len(out) == 0 {
		return nil, errors.New("nothing to change")
	}
	return out, nil
}

var (
	sfIntType    = regexp.MustCompile(`(?i)^(number|decimal|numeric|int|integer|bigint|smallint|tinyint|byteint)(\s*\(\s*\d+\s*(,\s*0\s*)?\))?$`)
	errSFNoIndex = errors.New("Snowflake tables have no indexes: add a unique key, or cluster the table with ALTER TABLE … CLUSTER BY")
)

// sfKeyActions refuses a foreign key's actions: Snowflake accepts the
// statement and doesn't create the key.
func sfKeyActions(fk *ForeignKeyDef) error {
	for _, a := range []*string{&fk.OnDelete, &fk.OnUpdate} {
		if *a != "" && !strings.EqualFold(*a, "NO ACTION") {
			return errors.New("Snowflake doesn't act on foreign keys: use NO ACTION (it would silently not create this one)")
		}
		*a = ""
	}
	return nil
}

// sfBuildDrop drops views and tables by their kind.
func sfBuildDrop(d Dialect, objs []Object, kinds map[Object]string, cascade bool) []string {
	var views, tables []string
	for _, o := range objs {
		kind := kinds[o]
		if kind == "" {
			kind = "TABLE"
			if o.Kind == KindView {
				kind = "VIEW"
			}
		}
		n := d.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name})
		switch {
		case kind == "TABLE" && cascade:
			tables = append(tables, "DROP TABLE "+n+" CASCADE;")
		case kind == "TABLE":
			tables = append(tables, "DROP TABLE "+n+" RESTRICT;") // without it, keys to it go silently
		default:
			views = append(views, "DROP "+kind+" "+n+";")
		}
	}
	return append(views, tables...)
}

// sfDrop drops tables and views, each by itself. Callers hold c.mu.
func sfDrop(ctx context.Context, c *Conn, objs []Object, cascade bool) ([]string, error) {
	objs = c.dropOrder(ctx, objs)
	kinds := map[Object]string{}
	for _, o := range objs {
		k, err := sfKind(ctx, c, TableRef{Schema: o.Schema, Name: o.Name})
		if err != nil {
			return nil, err
		}
		kinds[o] = k
	}
	stmts := sfBuildDrop(c.Dialect, objs, kinds, cascade)
	for i, s := range stmts {
		if _, err := c.conn.ExecContext(ctx, s); err != nil {
			if strings.Contains(err.Error(), "because of dependencies") {
				err = errors.New("another table has a foreign key to it: delete that table too, or choose to drop such keys")
			}
			if i > 0 {
				err = fmt.Errorf("%w (%d of %d were already deleted; UNDROP TABLE brings one back)", err, i, len(stmts))
			}
			return nil, fmt.Errorf("%s %w", s, err)
		}
	}
	e := c.eng.(*snowflakeEngine)
	for _, o := range objs {
		delete(e.views, TableRef{Schema: o.Schema, Name: o.Name})
	}
	return stmts, nil
}

// ------------------------------------------------------------- dumps

const sfStatement = 500_000 // bytes of a dump's INSERT (a statement takes 1 MB)

var sfForeignKey = regexp.MustCompile(`(?i)^\s*(constraint\s+("(?:[^"]|"")+"|\S+)\s+)?foreign key \(`)

// sfDumpTable is a table's DDL without its foreign keys, and those as
// statements for after the data. Callers hold c.mu.
func sfDumpTable(ctx context.Context, c *Conn, o Object) (string, []string, error) {
	ddl, err := c.eng.definition(ctx, c, o)
	if err != nil {
		return "", nil, err
	}
	fks, err := c.eng.foreignKeys(ctx, c, TableRef{Schema: o.Schema, Name: o.Name})
	if err != nil {
		return "", nil, err
	}
	if len(fks) > 0 {
		var keep []string
		for _, l := range strings.Split(ddl, "\n") {
			if !sfForeignKey.MatchString(l) {
				keep = append(keep, l)
			} else if !strings.HasSuffix(strings.TrimSpace(l), ",") && len(keep) > 0 { // it was the last line
				keep[len(keep)-1] = strings.TrimSuffix(strings.TrimRight(keep[len(keep)-1], " "), ",")
			}
		}
		ddl = strings.Join(keep, "\n")
	}
	var post []string
	for _, fk := range fks {
		ref := c.Dialect.QuoteIdent(fk.Table)
		if fk.Schema != o.Schema {
			ref = c.Dialect.QualifiedName(TableRef{Schema: fk.Schema, Name: fk.Table})
		}
		post = append(post, "ALTER TABLE "+c.Dialect.QuoteIdent(o.Name)+" ADD CONSTRAINT "+c.Dialect.QuoteIdent(fk.Name)+
			" FOREIGN KEY ("+quoteList(c.Dialect, fk.From)+") REFERENCES "+ref+" ("+quoteList(c.Dialect, fk.Columns)+")")
	}
	return ddl, post, nil
}
