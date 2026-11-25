package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gotable/internal/model"
)

// Users and privileges: the accounts of a server, what they may do, and the
// statements that change it. Like everything that changes a server, a change
// is planned first (the SQL is shown), then run. Passwords are in the
// statements that run but never in the ones that are shown or remembered.

// UserAdmin is what a server's accounts are like, for the page to offer.
type UserAdmin struct {
	// Me is the account GoTable is signed in as.
	Me string
	// HostLabel names the second part of an account ("" if there is none):
	// MySQL's host, MongoDB's authentication database.
	HostLabel, HostDefault string
	// Attributes are switches of an account.
	Attributes []UserAttribute
	Levels     []GrantLevel
	// Roles that can be given to an account.
	Roles []string
	// Databases privileges can be given on.
	Databases []string
	// GrantOption: a privilege can be given with the right to pass it on.
	GrantOption bool
	CanLock     bool
	// CanReassign: what a dropped account owns can be handed over first.
	CanReassign bool
	// PrivilegeWord is "privilege", or "role" where those are granted.
	PrivilegeWord string
	// Current is the database schema and table grants are for ("" = any).
	Current string
}

type UserAttribute struct{ Key, Label, Hint string }

// GrantLevel is where privileges apply: the server, a database, a table…
type GrantLevel struct {
	Key, Label string
	Privileges []string
	// Needs: which of database, schema and object the level is on.
	Needs []string
	// Future: the grant can also cover tables made later.
	Future bool
	// Presets fill in the usual choices.
	Presets []GrantPreset
}

type GrantPreset struct {
	Label      string
	Privileges []string
}

// Account is a user or role.
type Account struct {
	Name, Host string
	Attributes map[string]bool
	Roles      []string
	Locked     bool
	// Role: it can't sign in; System: GoTable doesn't change it.
	Role, System bool
	Note         string
}

// Grant is what an account may do at one place.
type Grant struct {
	Level, Database, Schema, Object string
	Privileges                      []string
	GrantOption                     bool
	// ReadOnly says why it can't be changed here.
	ReadOnly string
}

// UserOp is one change.
type UserOp struct {
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	Host       string          `json:"host"`
	Password   string          `json:"password"`
	Attributes map[string]bool `json:"attributes"`
	Role       string          `json:"role"`
	Level      string          `json:"level"`
	Database   string          `json:"database"`
	Schema     string          `json:"schema"`
	Object     string          `json:"object"`
	// Pattern: Database is a MySQL pattern as SHOW GRANTS writes it.
	Pattern     bool     `json:"pattern"`
	Privileges  []string `json:"privileges"`
	GrantOption bool     `json:"grantOption"`
	Future      bool     `json:"future"`
	Reassign    bool     `json:"reassign"`
}

// userEngine is what an engine knows of accounts. Methods run with c.mu held.
type userEngine interface {
	admin(ctx context.Context, c *Conn) (*UserAdmin, error)
	accounts(ctx context.Context, c *Conn) ([]Account, error)
	grants(ctx context.Context, c *Conn, name, host string) ([]Grant, error)
	// plan returns statements; a password is written as passwordMark.
	plan(ctx context.Context, c *Conn, op UserOp) ([]string, error)
	run(ctx context.Context, c *Conn, stmts []string) error
}

// passwordMark stands for the password in a planned statement: a literal no
// statement has otherwise, replaced by the quoted password as it runs.
const passwordMark = "\x00password\x00"

const passwordShown = "'••••••••'"

// userLiteral quotes a text for a statement.
func userLiteral(c *Conn, s string) string {
	if c.mongo != nil {
		return jsonString(s)
	}
	return c.Dialect.QuoteLiteral(Text(s))
}

var errNoUsers = errors.New("GoTable doesn't manage the users of this database")

func (c *Conn) userEngine() (userEngine, error) {
	if c.mongo != nil && c.Config.Driver == model.DriverMongoDB {
		return mongoUsers{}, nil
	}
	if c.conn == nil {
		return nil, errNoUsers
	}
	switch c.Config.Driver {
	case model.DriverCockroachDB, model.DriverCrateDB, model.DriverQuestDB, // PostgreSQL's protocol, other catalogs
		model.DriverSingleStore, model.DriverPlanetScale, model.DriverStarRocks, model.DriverDoris:
		return nil, errNoUsers
	}
	switch c.Dialect.family {
	case model.DriverPostgres:
		return pgUsers{}, nil
	case model.DriverMySQL:
		return mysqlUsers{}, nil
	case model.DriverMSSQL:
		return mssqlUsers{}, nil
	case model.DriverClickHouse:
		return chUsers{}, nil
	}
	return nil, errNoUsers
}

func (c *Conn) withUsers(ctx context.Context, fn func(u userEngine) error) error {
	u, err := c.userEngine()
	if err != nil {
		return err
	}
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	return fn(u)
}

// Users lists a server's accounts and what can be done to them.
func (c *Conn) Users(ctx context.Context) (admin *UserAdmin, accounts []Account, err error) {
	err = c.withUsers(ctx, func(u userEngine) error {
		if admin, err = u.admin(ctx, c); err != nil {
			return err
		}
		accounts, err = u.accounts(ctx, c)
		return err
	})
	return admin, accounts, err
}

// UserGrants lists what an account may do.
func (c *Conn) UserGrants(ctx context.Context, name, host string) (out []Grant, err error) {
	err = c.withUsers(ctx, func(u userEngine) error { out, err = u.grants(ctx, c, name, host); return err })
	return out, err
}

// ChangeUsers plans changes, and runs them unless dryRun. It returns the
// statements without the passwords in them.
func (c *Conn) ChangeUsers(ctx context.Context, ops []UserOp, dryRun bool) (shown []string, err error) {
	err = c.withUsers(ctx, func(u userEngine) error {
		var real []string
		secret := ""
		for _, op := range ops {
			if op.Name == "" {
				return errors.New("the name is missing")
			}
			if strings.ContainsRune(op.Name+op.Host+op.Password, 0) {
				return errors.New("a name or password can't hold a NUL character")
			}
			stmts, err := u.plan(ctx, c, op)
			if err != nil {
				return err
			}
			if op.Password != "" {
				secret = op.Password
			}
			for _, s := range stmts {
				mask := passwordShown
				if c.mongo != nil {
					mask = `"••••••••"`
				}
				shown = append(shown, strings.ReplaceAll(s, passwordMark, mask))
				real = append(real, strings.ReplaceAll(s, passwordMark, userLiteral(c, op.Password)))
			}
		}
		if len(real) == 0 {
			return errors.New("nothing to change")
		}
		if dryRun {
			return nil
		}
		if err := u.run(ctx, c, real); err != nil {
			if secret != "" { // a syntax error quotes the statement
				return errors.New(strings.ReplaceAll(err.Error(), secret, "••••••••"))
			}
			return err
		}
		return nil
	})
	return shown, err
}

// runEach runs statements one by one.
func runEach(ctx context.Context, c *Conn, stmts []string, tx bool) error {
	if !tx {
		for i, s := range stmts {
			if _, err := c.conn.ExecContext(ctx, s); err != nil {
				if i > 0 {
					return fmt.Errorf("%w (the %d statement(s) before it stay applied)", err, i)
				}
				return err
			}
		}
		return nil
	}
	t, err := c.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, s := range stmts {
		if _, err := t.ExecContext(ctx, s); err != nil {
			t.Rollback()
			return err
		}
	}
	return t.Commit()
}

// levelOf finds an op's level, checks its privileges and that it says where.
func levelOf(a *UserAdmin, op UserOp) (*GrantLevel, error) {
	i := slices.IndexFunc(a.Levels, func(l GrantLevel) bool { return l.Key == op.Level })
	if i < 0 {
		return nil, fmt.Errorf("unknown level %q", op.Level)
	}
	l := &a.Levels[i]
	for _, n := range l.Needs {
		if v := map[string]string{"database": op.Database, "schema": op.Schema, "object": op.Object}[n]; v == "" {
			return nil, fmt.Errorf("choose the %s", map[string]string{"database": "database", "schema": "schema", "object": "table"}[n])
		}
	}
	if len(op.Privileges) == 0 {
		return nil, fmt.Errorf("choose at least one %s", a.PrivilegeWord)
	}
	return l, nil
}

// privilegeList checks names that go into a statement unquoted.
func privilegeList(ps []string) (string, error) {
	for _, p := range ps {
		for _, r := range p {
			if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ' ' || r == '_') {
				return "", fmt.Errorf("%q isn't a privilege", p)
			}
		}
		if strings.TrimSpace(p) == "" {
			return "", errors.New("an empty privilege")
		}
	}
	return strings.Join(ps, ", "), nil
}

// ---------------------------------------------------------------- PostgreSQL

type pgUsers struct{}

var pgTablePrivileges = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}

var (
	presetRead  = GrantPreset{"Read only", []string{"SELECT"}}
	presetWrite = GrantPreset{"Read and write", []string{"SELECT", "INSERT", "UPDATE", "DELETE"}}
)

func (pgUsers) admin(ctx context.Context, c *Conn) (*UserAdmin, error) {
	a := &UserAdmin{GrantOption: true, CanReassign: true, PrivilegeWord: "privilege",
		Attributes: []UserAttribute{
			{"rolcanlogin", "Can sign in", "Off: a role, to be given to users."},
			{"rolsuper", "Superuser", "May do anything."},
			{"rolcreatedb", "Create databases", ""},
			{"rolcreaterole", "Create roles", ""},
			{"rolreplication", "Replication", ""},
			{"rolbypassrls", "Bypass row security", ""},
		},
		Levels: []GrantLevel{
			{Key: "tables", Label: "All tables in a schema", Needs: []string{"schema"}, Privileges: pgTablePrivileges, Future: true, Presets: []GrantPreset{presetRead, presetWrite, {"Everything", pgTablePrivileges}}},
			{Key: "table", Label: "One table or view", Needs: []string{"schema", "object"}, Privileges: pgTablePrivileges, Presets: []GrantPreset{presetRead, presetWrite, {"Everything", pgTablePrivileges}}},
			{Key: "schema", Label: "Schema", Needs: []string{"schema"}, Privileges: []string{"USAGE", "CREATE"}},
			{Key: "database", Label: "Database", Needs: []string{"database"}, Privileges: []string{"CONNECT", "CREATE", "TEMPORARY"}},
		}}
	if err := c.conn.QueryRowContext(ctx, "SELECT current_user, current_database()").Scan(&a.Me, &a.Current); err != nil {
		return nil, err
	}
	var err error
	if a.Databases, err = c.eng.databases(ctx, c); err != nil {
		return nil, err
	}
	a.Roles, err = c.queryStrings(ctx, `SELECT rolname FROM pg_roles ORDER BY rolname LIKE 'pg\_%', rolname`)
	return a, err
}

func (pgUsers) accounts(ctx context.Context, c *Conn) ([]Account, error) {
	// As JSON: which columns there are depends on the version.
	ms, err := c.rowMaps(ctx, `SELECT r.rolname, r.rolcanlogin, r.rolsuper, r.rolcreatedb, r.rolcreaterole, row_to_json(r)::text AS j, r.rolconnlimit, r.rolvaliduntil::text AS until,
			(SELECT string_agg(g.rolname, E'\n' ORDER BY g.rolname) FROM pg_auth_members m JOIN pg_roles g ON g.oid = m.roleid WHERE m.member = r.oid) AS roles
		FROM pg_roles r WHERE r.rolname NOT LIKE 'pg\_%' ORDER BY NOT r.rolcanlogin, r.rolname`)
	if err != nil {
		return nil, err
	}
	out := make([]Account, len(ms))
	for i, m := range ms {
		a := Account{Name: m["rolname"], Role: m["rolcanlogin"] != "true", Attributes: map[string]bool{}}
		for _, k := range []string{"rolcanlogin", "rolsuper", "rolcreatedb", "rolcreaterole"} {
			a.Attributes[k] = m[k] == "true"
		}
		for _, k := range []string{"rolreplication", "rolbypassrls"} {
			a.Attributes[k] = strings.Contains(m["j"], `"`+k+`":true`)
		}
		if m["roles"] != "" {
			a.Roles = strings.Split(m["roles"], "\n")
		}
		if u := m["until"]; u != "" && u != "infinity" {
			a.Note = "password valid until " + u
		}
		out[i] = a
	}
	return out, nil
}

func (pgUsers) grants(ctx context.Context, c *Conn, name, _ string) ([]Grant, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT level, db, sch, obj, privilege_type, is_grantable FROM (
			SELECT 1 AS o, 'database' AS level, d.datname::text AS db, '' AS sch, '' AS obj, x.grantee, x.privilege_type, x.is_grantable FROM pg_database d, LATERAL aclexplode(d.datacl) x
			UNION ALL SELECT 2, 'schema', '', n.nspname, '', x.grantee, x.privilege_type, x.is_grantable FROM pg_namespace n, LATERAL aclexplode(n.nspacl) x
			UNION ALL SELECT 3, 'future', '', coalesce(n.nspname, ''), '', x.grantee, x.privilege_type, x.is_grantable
				FROM pg_default_acl d LEFT JOIN pg_namespace n ON n.oid = d.defaclnamespace, LATERAL aclexplode(d.defaclacl) x WHERE d.defaclobjtype = 'r'
			UNION ALL SELECT 4, CASE WHEN k.relkind = 'S' THEN 'sequence' ELSE 'table' END, '', n.nspname, k.relname, x.grantee, x.privilege_type, x.is_grantable
				FROM pg_class k JOIN pg_namespace n ON n.oid = k.relnamespace, LATERAL aclexplode(k.relacl) x WHERE k.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
		) g WHERE g.grantee = (SELECT oid FROM pg_roles WHERE rolname = $1) AND sch NOT IN ('pg_catalog', 'information_schema')
		ORDER BY o, db, sch, obj, privilege_type`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		var p string
		if err := rows.Scan(&g.Level, &g.Database, &g.Schema, &g.Object, &p, &g.GrantOption); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].Level == g.Level && out[n-1].Database == g.Database && out[n-1].Schema == g.Schema && out[n-1].Object == g.Object {
			if !slices.Contains(out[n-1].Privileges, p) {
				out[n-1].Privileges = append(out[n-1].Privileges, p)
			}
			out[n-1].GrantOption = out[n-1].GrantOption && g.GrantOption
			continue
		}
		g.Privileges = []string{p}
		if g.Level == "sequence" {
			g.ReadOnly = "Comes with “All tables in a schema”."
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (u pgUsers) plan(ctx context.Context, c *Conn, op UserOp) ([]string, error) {
	q := c.Dialect.QuoteIdent
	who := q(op.Name)
	attrs := func(all bool) string {
		var out []string
		for _, a := range []struct{ key, kw string }{{"rolcanlogin", "LOGIN"}, {"rolsuper", "SUPERUSER"}, {"rolcreatedb", "CREATEDB"}, {"rolcreaterole", "CREATEROLE"}, {"rolreplication", "REPLICATION"}, {"rolbypassrls", "BYPASSRLS"}} {
			on, set := op.Attributes[a.key]
			if on {
				out = append(out, a.kw)
			} else if set && all {
				out = append(out, "NO"+a.kw)
			}
		}
		return strings.Join(out, " ")
	}
	switch op.Kind {
	case "create":
		s := "CREATE ROLE " + who
		if a := attrs(false); a != "" {
			s += " " + a
		}
		if op.Password != "" {
			s += " PASSWORD " + passwordMark
		}
		return []string{s}, nil
	case "attributes":
		if a := attrs(true); a != "" {
			return []string{"ALTER ROLE " + who + " " + a}, nil
		}
		return nil, errors.New("nothing to change")
	case "password":
		if op.Password == "" {
			return []string{"ALTER ROLE " + who + " PASSWORD NULL"}, nil
		}
		return []string{"ALTER ROLE " + who + " PASSWORD " + passwordMark}, nil
	case "drop":
		if op.Reassign {
			// What it owns in this database goes to us; then only its privileges are left.
			return []string{"REASSIGN OWNED BY " + who + " TO CURRENT_USER", "DROP OWNED BY " + who, "DROP ROLE " + who}, nil
		}
		return []string{"DROP ROLE " + who}, nil
	case "grant_role":
		return []string{"GRANT " + q(op.Role) + " TO " + who}, nil
	case "revoke_role":
		return []string{"REVOKE " + q(op.Role) + " FROM " + who}, nil
	case "grant", "revoke":
	default:
		return nil, fmt.Errorf("unknown change %q", op.Kind)
	}
	a := &UserAdmin{PrivilegeWord: "privilege", Levels: []GrantLevel{{Key: "tables", Needs: []string{"schema"}}, {Key: "future"}, {Key: "table", Needs: []string{"schema", "object"}}, {Key: "schema", Needs: []string{"schema"}}, {Key: "database", Needs: []string{"database"}}}}
	if _, err := levelOf(a, op); err != nil {
		return nil, err
	}
	privs, err := privilegeList(op.Privileges)
	if err != nil {
		return nil, err
	}
	grant := op.Kind == "grant"
	verb, to, with := "REVOKE ", " FROM ", ""
	if grant {
		verb, to = "GRANT ", " TO "
		if op.GrantOption {
			with = " WITH GRANT OPTION"
		}
	}
	on := func(what string) string { return verb + privs + " ON " + what + to + who + with }
	// A table is reached through its schema.
	usage := "GRANT USAGE ON SCHEMA " + q(op.Schema) + " TO " + who
	defaults := "ALTER DEFAULT PRIVILEGES "
	if op.Schema != "" {
		defaults += "IN SCHEMA " + q(op.Schema) + " "
	}
	var out []string
	switch op.Level {
	case "database":
		return []string{on("DATABASE " + q(op.Database))}, nil
	case "schema":
		return []string{on("SCHEMA " + q(op.Schema))}, nil
	case "future":
		return []string{defaults + on("TABLES")}, nil
	case "table":
		if grant {
			out = append(out, usage)
		}
		return append(out, on("TABLE "+q(op.Schema)+"."+q(op.Object))), nil
	}
	if grant {
		out = append(out, usage)
	}
	out = append(out, on("ALL TABLES IN SCHEMA "+q(op.Schema)))
	if slices.Contains(op.Privileges, "INSERT") { // serial columns
		seq := "USAGE, SELECT ON ALL SEQUENCES IN SCHEMA " + q(op.Schema)
		out = append(out, verb+seq+to+who)
		if op.Future {
			out = append(out, defaults+verb+"USAGE, SELECT ON SEQUENCES"+to+who)
		}
	}
	if op.Future {
		out = append(out, defaults+on("TABLES"))
	}
	return out, nil
}

func (pgUsers) run(ctx context.Context, c *Conn, stmts []string) error {
	return runEach(ctx, c, stmts, true)
}

// ---------------------------------------------------------------- MySQL, MariaDB, TiDB

type mysqlUsers struct{}

var (
	mysqlTablePrivileges = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "REFERENCES", "INDEX", "ALTER", "CREATE VIEW", "SHOW VIEW", "TRIGGER"}
	mysqlDBPrivileges    = append(slices.Clone(mysqlTablePrivileges), "CREATE TEMPORARY TABLES", "LOCK TABLES", "EXECUTE", "CREATE ROUTINE", "ALTER ROUTINE", "EVENT")
	mysqlAllPrivileges   = append(slices.Clone(mysqlDBPrivileges), "CREATE USER", "FILE", "PROCESS", "RELOAD", "REPLICATION CLIENT", "REPLICATION SLAVE", "SHOW DATABASES", "SHUTDOWN", "SUPER")
)

func (mysqlUsers) maria(ctx context.Context, c *Conn) bool {
	v, _ := c.eng.serverVersion(ctx, c)
	return strings.HasPrefix(v, "MariaDB")
}

func (mysqlUsers) admin(ctx context.Context, c *Conn) (*UserAdmin, error) {
	all := GrantPreset{"Everything", []string{"ALL PRIVILEGES"}}
	design := GrantPreset{"Design", []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "INDEX", "ALTER", "REFERENCES"}}
	a := &UserAdmin{HostLabel: "Host", HostDefault: "%", GrantOption: true, CanLock: true, PrivilegeWord: "privilege",
		Levels: []GrantLevel{
			{Key: "database", Label: "Database", Needs: []string{"database"}, Privileges: mysqlDBPrivileges, Presets: []GrantPreset{presetRead, presetWrite, design, all}},
			{Key: "table", Label: "One table", Needs: []string{"database", "object"}, Privileges: mysqlTablePrivileges, Presets: []GrantPreset{presetRead, presetWrite, design, all}},
			{Key: "global", Label: "The whole server", Privileges: mysqlAllPrivileges, Presets: []GrantPreset{presetRead, presetWrite, all}},
		}}
	if err := c.conn.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&a.Me); err != nil {
		return nil, err
	}
	var err error
	if a.Databases, err = c.eng.databases(ctx, c); err != nil {
		return nil, err
	}
	accounts, err := mysqlUsers{}.accounts(ctx, c)
	maria := mysqlUsers{}.maria(ctx, c) // by the server, whatever the connection calls it
	for _, x := range accounts {
		if x.Role || !maria && !x.System {
			a.Roles = append(a.Roles, mysqlRoleName(x))
		}
	}
	return a, err
}

func mysqlRoleName(a Account) string {
	if a.Host == "" || a.Host == "%" {
		return a.Name
	}
	return a.Name + "@" + a.Host
}

func (u mysqlUsers) accounts(ctx context.Context, c *Conn) ([]Account, error) {
	ms, err := c.rowMaps(ctx, "SELECT * FROM mysql.user ORDER BY User, Host")
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(ms))
	maria := u.maria(ctx, c)
	locked := map[string]bool{}
	if maria {
		if ls, err := c.rowMaps(ctx, "SELECT User, Host FROM mysql.global_priv WHERE JSON_VALUE(Priv, '$.account_locked') IN ('true', '1')"); err == nil {
			for _, l := range ls {
				locked[l["user"]+"@"+l["host"]] = true
			}
		}
	}
	for _, m := range ms {
		a := Account{Name: m["user"], Host: m["host"], Attributes: map[string]bool{}}
		a.Locked = m["account_locked"] == "Y" || locked[a.Name+"@"+a.Host]
		a.Role = m["is_role"] == "Y" || !maria && a.Locked && m["password_expired"] == "Y" && m["authentication_string"] == ""
		a.System = strings.HasPrefix(a.Name, "mysql.") || a.Name == "mariadb.sys" || a.Name == "PUBLIC" && a.Host == "" || a.Name == ""
		if a.Role {
			a.Locked = false
		}
		if p := m["plugin"]; p == "auth_socket" || p == "unix_socket" {
			a.Note = "signs in through the server's socket"
		}
		out = append(out, a)
	}
	return out, nil
}

func (u mysqlUsers) account(c *Conn, name, host string) string {
	s := userLiteral(c, name)
	if host != "" {
		s += "@" + userLiteral(c, host)
	}
	return s
}

func (u mysqlUsers) role(c *Conn, role string) string {
	if i := strings.LastIndexByte(role, '@'); i > 0 {
		return u.account(c, role[:i], role[i+1:])
	}
	return userLiteral(c, role)
}

func (u mysqlUsers) grants(ctx context.Context, c *Conn, name, host string) ([]Grant, error) {
	lines, err := c.queryStrings(ctx, "SHOW GRANTS FOR "+u.account(c, name, host))
	if err != nil {
		return nil, err
	}
	out := []Grant{}
	for _, l := range lines {
		if g, ok := parseMySQLGrant(l); ok {
			out = append(out, g...)
		}
	}
	return out, nil
}

// parseMySQLGrant reads a line of SHOW GRANTS.
func parseMySQLGrant(line string) ([]Grant, bool) {
	rest, ok := strings.CutPrefix(line, "GRANT ")
	if !ok {
		return nil, false
	}
	// The words outside quotes: ON and TO.
	on, to := -1, -1
	depth := 0
	for i := 0; i < len(rest); i++ {
		switch ch := rest[i]; {
		case ch == '\'' || ch == '"' || ch == '`':
			i = skipQuoted(rest, i, ch, ch != '`') - 1
		case ch == '(':
			depth++
		case ch == ')':
			depth--
		case depth == 0 && strings.HasPrefix(rest[i:], " ON ") && on < 0 && to < 0:
			on = i
		case depth == 0 && strings.HasPrefix(rest[i:], " TO ") && to < 0:
			to = i
		}
	}
	if to < 0 {
		return nil, false
	}
	option := strings.Contains(rest[to:], "WITH GRANT OPTION")
	if on < 0 { // roles
		g := Grant{Level: "role", GrantOption: strings.Contains(rest[to:], "WITH ADMIN OPTION")}
		for _, r := range splitTop(rest[:to]) {
			r = strings.ReplaceAll(strings.NewReplacer("`", "", "'", "").Replace(r), "@%", "")
			g.Privileges = append(g.Privileges, r)
		}
		return []Grant{g}, true
	}
	target := strings.TrimSpace(rest[on+4 : to])
	g := Grant{GrantOption: option}
	for _, kw := range []string{"FUNCTION ", "PROCEDURE ", "PACKAGE ", "PACKAGE BODY "} {
		if strings.HasPrefix(target, kw) {
			g.Level, g.Object, g.ReadOnly = "routine", target, "A routine's privileges are changed in the query editor."
		}
	}
	if g.Level == "" {
		target = strings.TrimPrefix(target, "TABLE ")
		dbName, table := mysqlTarget(target)
		switch {
		case dbName == "" && table == "": // PROXY ON 'user'@'host'
			return nil, false
		case dbName == "*":
			g.Level = "global"
		case table == "*":
			g.Level, g.Database = "database", dbName
		default:
			g.Level, g.Database, g.Object = "table", dbName, table
		}
	}
	var plain, columns []string
	for _, p := range splitTop(rest[:on]) {
		switch {
		case strings.Contains(p, "("):
			columns = append(columns, p)
		case p == "USAGE":
		case p == "PROXY":
			return nil, false
		default:
			plain = append(plain, p)
		}
	}
	var out []Grant
	if len(plain) > 0 || len(columns) == 0 && g.Level != "global" || option {
		g.Privileges = plain
		if len(plain) > 0 || option {
			out = append(out, g)
		}
	}
	if len(columns) > 0 {
		cg := g
		cg.Privileges, cg.ReadOnly = columns, "Privileges on single columns are changed in the query editor."
		out = append(out, cg)
	}
	return out, len(out) > 0
}

// splitTop splits at commas outside quotes and parentheses.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case ch == '\'' || ch == '"' || ch == '`':
			i = skipQuoted(s, i, ch, ch != '`') - 1
		case ch == '(':
			depth++
		case ch == ')':
			depth--
		case ch == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// mysqlTarget reads `db`.`table`, `db`.* or *.*.
func mysqlTarget(s string) (dbName, table string) {
	part := func() (string, bool) {
		if strings.HasPrefix(s, "`") {
			j := skipQuoted(s, 0, '`', false)
			v := strings.ReplaceAll(s[1:j-1], "``", "`")
			s = s[j:]
			return v, true
		}
		j := strings.IndexAny(s, ". ")
		if j < 0 {
			j = len(s)
		}
		v := s[:j]
		s = s[j:]
		return v, v != ""
	}
	a, ok := part()
	if !ok || !strings.HasPrefix(s, ".") {
		return "", ""
	}
	s = s[1:]
	b, _ := part()
	return a, b
}

func (u mysqlUsers) plan(ctx context.Context, c *Conn, op UserOp) ([]string, error) {
	who := u.account(c, op.Name, op.Host)
	q := c.Dialect.QuoteIdent
	switch op.Kind {
	case "create":
		s := "CREATE USER " + who
		if op.Password != "" {
			s += " IDENTIFIED BY " + passwordMark
		}
		return []string{s}, nil
	case "password":
		return []string{"ALTER USER " + who + " IDENTIFIED BY " + passwordMark}, nil
	case "drop":
		return []string{"DROP USER " + who}, nil
	case "lock":
		return []string{"ALTER USER " + who + " ACCOUNT LOCK"}, nil
	case "unlock":
		return []string{"ALTER USER " + who + " ACCOUNT UNLOCK"}, nil
	case "grant_role":
		out := []string{"GRANT " + u.role(c, op.Role) + " TO " + who}
		if u.maria(ctx, c) {
			return out, nil
		}
		// MySQL gives a role without switching it on.
		return append(out, "SET DEFAULT ROLE ALL TO "+who), nil
	case "revoke_role":
		return []string{"REVOKE " + u.role(c, op.Role) + " FROM " + who}, nil
	case "grant", "revoke":
	default:
		return nil, fmt.Errorf("unknown change %q", op.Kind)
	}
	a := &UserAdmin{PrivilegeWord: "privilege", Levels: []GrantLevel{{Key: "global"}, {Key: "database", Needs: []string{"database"}}, {Key: "table", Needs: []string{"database", "object"}}}}
	if op.Kind == "revoke" && op.GrantOption { // GRANT OPTION is a privilege of its own
		op.Privileges = append(slices.Clone(op.Privileges), "GRANT OPTION")
	}
	if _, err := levelOf(a, op); err != nil {
		return nil, err
	}
	privs, err := privilegeList(op.Privileges)
	if err != nil {
		return nil, err
	}
	on := "*.*"
	switch op.Level {
	case "database":
		name := op.Database
		if !op.Pattern { // _ and % stand for any characters here
			name = strings.NewReplacer(`\`, `\\`, "_", `\_`, "%", `\%`).Replace(name)
		}
		on = q(name) + ".*"
	case "table":
		on = q(op.Database) + "." + q(op.Object)
	}
	if op.Kind == "revoke" {
		// ALL PRIVILEGES and GRANT OPTION are only revoked together as one of their own statements.
		if i := slices.Index(op.Privileges, "GRANT OPTION"); i >= 0 && slices.Contains(op.Privileges, "ALL PRIVILEGES") {
			return []string{"REVOKE ALL PRIVILEGES ON " + on + " FROM " + who, "REVOKE GRANT OPTION ON " + on + " FROM " + who}, nil
		}
		return []string{"REVOKE " + privs + " ON " + on + " FROM " + who}, nil
	}
	s := "GRANT " + privs + " ON " + on + " TO " + who
	if op.GrantOption {
		s += " WITH GRANT OPTION"
	}
	return []string{s}, nil
}

func (mysqlUsers) run(ctx context.Context, c *Conn, stmts []string) error {
	return runEach(ctx, c, stmts, false)
}

// ---------------------------------------------------------------- SQL Server

// mssqlUsers: a login signs in to the server, a user of the same name lets
// it into this database. GoTable makes and drops them together.
type mssqlUsers struct{}

var mssqlPrivileges = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "EXECUTE", "REFERENCES", "ALTER", "CONTROL", "VIEW DEFINITION"}

func (mssqlUsers) admin(ctx context.Context, c *Conn) (*UserAdmin, error) {
	a := &UserAdmin{GrantOption: true, CanLock: true, PrivilegeWord: "privilege",
		Levels: []GrantLevel{
			{Key: "database", Label: "This database", Privileges: append([]string{"CONNECT", "CREATE TABLE", "CREATE VIEW", "CREATE SCHEMA"}, mssqlPrivileges...), Presets: []GrantPreset{presetRead, presetWrite}},
			{Key: "schema", Label: "Schema", Needs: []string{"schema"}, Privileges: mssqlPrivileges, Presets: []GrantPreset{presetRead, presetWrite}},
			{Key: "table", Label: "One table or view", Needs: []string{"schema", "object"}, Privileges: []string{"SELECT", "INSERT", "UPDATE", "DELETE", "REFERENCES", "ALTER", "CONTROL", "VIEW DEFINITION"}, Presets: []GrantPreset{presetRead, presetWrite}},
		}}
	if err := c.conn.QueryRowContext(ctx, "SELECT USER_NAME(), DB_NAME()").Scan(&a.Me, &a.Current); err != nil {
		return nil, err
	}
	var err error
	a.Roles, err = c.queryStrings(ctx, "SELECT name FROM sys.database_principals WHERE type = 'R' AND name <> 'public' ORDER BY is_fixed_role DESC, name")
	return a, err
}

func (mssqlUsers) accounts(ctx context.Context, c *Conn) ([]Account, error) {
	ms, err := c.rowMaps(ctx, `SELECT p.name, p.type, l.name AS login, CASE WHEN l.is_disabled = 1 THEN 1 ELSE 0 END AS locked, p.principal_id AS id,
			(SELECT STRING_AGG(r.name, CHAR(10)) FROM sys.database_role_members m JOIN sys.database_principals r ON r.principal_id = m.role_principal_id WHERE m.member_principal_id = p.principal_id) AS roles
		FROM sys.database_principals p LEFT JOIN sys.server_principals l ON l.sid = p.sid
		WHERE (p.type IN ('S', 'U', 'G', 'E', 'X') OR p.type = 'R' AND p.is_fixed_role = 0 AND p.name <> 'public') AND p.name NOT IN ('INFORMATION_SCHEMA', 'sys', 'guest')
		ORDER BY CASE WHEN p.type = 'R' THEN 1 ELSE 0 END, p.name`)
	if err != nil {
		return nil, err
	}
	out := make([]Account, len(ms))
	for i, m := range ms {
		a := Account{Name: m["name"], Role: m["type"] == "R", Locked: m["locked"] == "1", System: m["name"] == "dbo", Attributes: map[string]bool{}}
		if m["roles"] != "" {
			a.Roles = strings.Split(m["roles"], "\n")
		}
		switch {
		case a.Role:
		case m["login"] == "":
			a.Note = "no login"
		case m["login"] != a.Name:
			a.Note = "login " + m["login"]
		}
		out[i] = a
	}
	return out, nil
}

func (mssqlUsers) grants(ctx context.Context, c *Conn, name, _ string) ([]Grant, error) {
	ms, err := c.rowMaps(ctx, `SELECT p.class_desc AS class, p.permission_name AS priv, p.state, COALESCE(s.name, os.name) AS sch, o.name AS obj, p.minor_id AS col
		FROM sys.database_permissions p JOIN sys.database_principals u ON u.principal_id = p.grantee_principal_id
		LEFT JOIN sys.schemas s ON p.class = 3 AND s.schema_id = p.major_id
		LEFT JOIN sys.objects o ON p.class = 1 AND o.object_id = p.major_id
		LEFT JOIN sys.schemas os ON os.schema_id = o.schema_id
		WHERE u.name = @p1 AND p.class IN (0, 1, 3) ORDER BY p.class, sch, obj, p.state, priv`, name)
	if err != nil {
		return nil, err
	}
	out := []Grant{}
	for _, m := range ms {
		g := Grant{Level: map[string]string{"DATABASE": "database", "SCHEMA": "schema"}[m["class"]], Schema: m["sch"], Object: m["obj"], Privileges: []string{m["priv"]}, GrantOption: strings.TrimSpace(m["state"]) == "W"}
		if g.Level == "" {
			g.Level = "table"
		}
		if strings.TrimSpace(m["state"]) == "D" {
			g.ReadOnly = "Denied. Change it in the query editor."
		}
		if m["col"] != "" && m["col"] != "0" {
			g.ReadOnly = "Privileges on single columns are changed in the query editor."
		}
		if n := len(out); n > 0 && out[n-1].Level == g.Level && out[n-1].Schema == g.Schema && out[n-1].Object == g.Object && out[n-1].GrantOption == g.GrantOption && out[n-1].ReadOnly == g.ReadOnly {
			out[n-1].Privileges = append(out[n-1].Privileges, g.Privileges...)
			continue
		}
		out = append(out, g)
	}
	return out, nil
}

func (mssqlUsers) plan(ctx context.Context, c *Conn, op UserOp) ([]string, error) {
	q := c.Dialect.QuoteIdent
	who := q(op.Name)
	var login string
	c.conn.QueryRowContext(ctx, "SELECT l.name FROM sys.database_principals p JOIN sys.server_principals l ON l.sid = p.sid WHERE p.name = @p1", op.Name).Scan(&login)
	needLogin := func() error {
		if login == "" {
			return fmt.Errorf("%s has no login of its own", op.Name)
		}
		return nil
	}
	switch op.Kind {
	case "create":
		if op.Password == "" {
			return nil, errors.New("a login needs a password")
		}
		return []string{"CREATE LOGIN " + who + " WITH PASSWORD = " + passwordMark, "CREATE USER " + who + " FOR LOGIN " + who}, nil
	case "password":
		return []string{"ALTER LOGIN " + q(login) + " WITH PASSWORD = " + passwordMark}, needLogin()
	case "lock":
		return []string{"ALTER LOGIN " + q(login) + " DISABLE"}, needLogin()
	case "unlock":
		return []string{"ALTER LOGIN " + q(login) + " ENABLE"}, needLogin()
	case "drop":
		out := []string{"DROP USER " + who}
		if login != "" {
			out = append(out, "DROP LOGIN "+q(login))
		}
		return out, nil
	case "grant_role":
		return []string{"ALTER ROLE " + q(op.Role) + " ADD MEMBER " + who}, nil
	case "revoke_role":
		return []string{"ALTER ROLE " + q(op.Role) + " DROP MEMBER " + who}, nil
	case "grant", "revoke":
	default:
		return nil, fmt.Errorf("unknown change %q", op.Kind)
	}
	a := &UserAdmin{PrivilegeWord: "privilege", Levels: []GrantLevel{{Key: "database"}, {Key: "schema", Needs: []string{"schema"}}, {Key: "table", Needs: []string{"schema", "object"}}}}
	if _, err := levelOf(a, op); err != nil {
		return nil, err
	}
	privs, err := privilegeList(op.Privileges)
	if err != nil {
		return nil, err
	}
	on := ""
	switch op.Level {
	case "schema":
		on = " ON SCHEMA::" + q(op.Schema)
	case "table":
		on = " ON OBJECT::" + q(op.Schema) + "." + q(op.Object)
	}
	if op.Kind == "revoke" {
		s := "REVOKE " + privs + on + " FROM " + who
		if op.GrantOption {
			s += " CASCADE"
		}
		return []string{s}, nil
	}
	s := "GRANT " + privs + on + " TO " + who
	if op.GrantOption {
		s += " WITH GRANT OPTION"
	}
	return []string{s}, nil
}

func (mssqlUsers) run(ctx context.Context, c *Conn, stmts []string) error {
	return runEach(ctx, c, stmts, true)
}

// ---------------------------------------------------------------- ClickHouse

type chUsers struct{}

var chPrivileges = []string{"SELECT", "INSERT", "ALTER", "CREATE TABLE", "CREATE VIEW", "DROP TABLE", "TRUNCATE", "OPTIMIZE", "SHOW"}

func (chUsers) admin(ctx context.Context, c *Conn) (*UserAdmin, error) {
	all := GrantPreset{"Everything", []string{"ALL"}}
	write := GrantPreset{"Read and write", []string{"SELECT", "INSERT"}}
	a := &UserAdmin{GrantOption: true, PrivilegeWord: "privilege",
		Levels: []GrantLevel{
			{Key: "database", Label: "Database", Needs: []string{"database"}, Privileges: chPrivileges, Presets: []GrantPreset{presetRead, write, all}},
			{Key: "table", Label: "One table", Needs: []string{"database", "object"}, Privileges: chPrivileges, Presets: []GrantPreset{presetRead, write, all}},
			{Key: "global", Label: "The whole server", Privileges: append(slices.Clone(chPrivileges), "CREATE DATABASE", "DROP DATABASE", "ACCESS MANAGEMENT", "SYSTEM", "KILL QUERY"), Presets: []GrantPreset{presetRead, write, all}},
		}}
	if err := c.conn.QueryRowContext(ctx, "SELECT currentUser()").Scan(&a.Me); err != nil {
		return nil, err
	}
	var err error
	if a.Databases, err = c.eng.databases(ctx, c); err != nil {
		return nil, err
	}
	a.Roles, err = c.queryStrings(ctx, "SELECT name FROM system.roles ORDER BY name")
	return a, err
}

func (chUsers) accounts(ctx context.Context, c *Conn) ([]Account, error) {
	ms, err := c.rowMaps(ctx, `SELECT u.name AS name, u.storage AS storage, arrayStringConcat(g.roles, '\n') AS roles FROM system.users AS u
		LEFT JOIN (SELECT user_name, groupArray(granted_role_name) AS roles FROM system.role_grants GROUP BY user_name) AS g ON g.user_name = u.name ORDER BY u.name`)
	if err != nil {
		return nil, err
	}
	out := make([]Account, len(ms))
	for i, m := range ms {
		out[i] = Account{Name: m["name"], Attributes: map[string]bool{}, System: m["storage"] == "users_xml" || m["storage"] == "users.xml"}
		if out[i].System {
			out[i].Note = "kept in the server's configuration files"
		}
		if m["roles"] != "" {
			out[i].Roles = strings.Split(m["roles"], "\n")
		}
	}
	return out, nil
}

func (chUsers) grants(ctx context.Context, c *Conn, name, _ string) ([]Grant, error) {
	ms, err := c.rowMaps(ctx, `SELECT toString(access_type) AS priv, ifNull(database, '') AS db, ifNull(table, '') AS tbl, ifNull(column, '') AS col, is_partial_revoke AS pr, grant_option AS opt
		FROM system.grants WHERE user_name = ? ORDER BY db, tbl, col, pr, opt, priv`, name)
	if err != nil {
		return nil, err
	}
	out := []Grant{}
	for _, m := range ms {
		g := Grant{Level: "global", Database: m["db"], Object: m["tbl"], Privileges: []string{m["priv"]}, GrantOption: m["opt"] == "1"}
		if g.Object != "" {
			g.Level = "table"
		} else if g.Database != "" {
			g.Level = "database"
		}
		if m["col"] != "" {
			g.ReadOnly = "Privileges on single columns are changed in the query editor."
		}
		if m["pr"] == "1" {
			g.ReadOnly = "Taken back from a wider grant. Change it in the query editor."
		}
		if n := len(out); n > 0 && out[n-1].Database == g.Database && out[n-1].Object == g.Object && out[n-1].GrantOption == g.GrantOption && out[n-1].ReadOnly == g.ReadOnly {
			out[n-1].Privileges = append(out[n-1].Privileges, g.Privileges...)
			continue
		}
		out = append(out, g)
	}
	return out, nil
}

func (chUsers) plan(_ context.Context, c *Conn, op UserOp) ([]string, error) {
	q := c.Dialect.QuoteIdent
	who := q(op.Name)
	switch op.Kind {
	case "create":
		s := "CREATE USER " + who
		if op.Password != "" {
			s += " IDENTIFIED BY " + passwordMark
		}
		return []string{s}, nil
	case "password":
		return []string{"ALTER USER " + who + " IDENTIFIED BY " + passwordMark}, nil
	case "drop":
		return []string{"DROP USER " + who}, nil
	case "grant_role":
		return []string{"GRANT " + q(op.Role) + " TO " + who}, nil
	case "revoke_role":
		return []string{"REVOKE " + q(op.Role) + " FROM " + who}, nil
	case "grant", "revoke":
	default:
		return nil, fmt.Errorf("unknown change %q", op.Kind)
	}
	a := &UserAdmin{PrivilegeWord: "privilege", Levels: []GrantLevel{{Key: "global"}, {Key: "database", Needs: []string{"database"}}, {Key: "table", Needs: []string{"database", "object"}}}}
	if _, err := levelOf(a, op); err != nil {
		return nil, err
	}
	privs, err := privilegeList(op.Privileges)
	if err != nil {
		return nil, err
	}
	on := "*.*"
	switch op.Level {
	case "database":
		on = q(op.Database) + ".*"
	case "table":
		on = q(op.Database) + "." + q(op.Object)
	}
	if op.Kind == "revoke" {
		return []string{"REVOKE " + privs + " ON " + on + " FROM " + who}, nil
	}
	s := "GRANT " + privs + " ON " + on + " TO " + who
	if op.GrantOption {
		s += " WITH GRANT OPTION"
	}
	return []string{s}, nil
}

func (chUsers) run(ctx context.Context, c *Conn, stmts []string) error {
	return runEach(ctx, c, stmts, false)
}
