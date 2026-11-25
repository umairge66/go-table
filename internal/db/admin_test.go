package db

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"
)

func TestParseMySQLGrant(t *testing.T) {
	for line, want := range map[string]string{
		"GRANT SELECT, INSERT ON `my\\_db`.* TO `u`@`%`":                                      "database my\\_db  SELECT,INSERT false",
		"GRANT ALL PRIVILEGES ON *.* TO `root`@`localhost` WITH GRANT OPTION":                 "global   ALL PRIVILEGES true",
		"GRANT SELECT ON `a``b`.`t ON x` TO 'u'@'%'":                                          "table a`b t ON x SELECT false",
		"GRANT USAGE ON *.* TO `u`@`%`":                                                       "",
		"GRANT SELECT (`a`, `b`), UPDATE ON `d`.`t` TO `u`@`%`":                               "table d t UPDATE false | table d t SELECT (`a`, `b`) false ro",
		"GRANT `reader`@`%`,`w`@`localhost` TO `u`@`%`":                                       "role   reader,w@localhost false",
		"GRANT EXECUTE ON PROCEDURE `d`.`p` TO `u`@`%`":                                       "routine  PROCEDURE `d`.`p` EXECUTE false ro",
		"GRANT PROXY ON ''@'' TO 'root'@'localhost' WITH GRANT OPTION":                        "",
		"GRANT USAGE ON *.* TO `u`@`%` IDENTIFIED BY PASSWORD '*0123' WITH GRANT OPTION":      "global    true",
		"GRANT APPLICATION_PASSWORD_ADMIN,AUDIT_ADMIN ON *.* TO `root`@`%` WITH GRANT OPTION": "global   APPLICATION_PASSWORD_ADMIN,AUDIT_ADMIN true",
		"GRANT SELECT ON `d`.* TO `we ON ird TO`@`%`":                                         "database d  SELECT false",
		"SET DEFAULT ROLE `r` FOR `u`@`%`":                                                    "",
	} {
		gs, _ := parseMySQLGrant(line)
		var parts []string
		for _, g := range gs {
			s := g.Level + " " + g.Database + " " + g.Object + " " + strings.Join(g.Privileges, ",") + " " + map[bool]string{true: "true", false: "false"}[g.GrantOption]
			if g.ReadOnly != "" {
				s += " ro"
			}
			parts = append(parts, s)
		}
		if got := strings.Join(parts, " | "); got != want {
			t.Errorf("%s\n got %q\nwant %q", line, got, want)
		}
	}
}

const testPassword = `Pa'ss\w"0rd-1`

type userTest struct {
	driver model.Driver
	// setup makes gt_secret with a row, in the database signed in to.
	setup, cleanup         []string
	read, write            string // need SELECT and INSERT on it
	schema, host           string
	grant                  UserOp // SELECT on the table
	wide                   UserOp // SELECT and INSERT, wider
	badPassword            string
	role, roleSQL, dropSQL string
	slow                   string
}

var userTests = map[string]userTest{
	"PG": {driver: model.DriverPostgres, schema: "public", slow: "SELECT pg_sleep(60)",
		grant: UserOp{Level: "table", Schema: "public", Object: "gt_secret", Privileges: []string{"SELECT"}},
		wide:  UserOp{Level: "tables", Schema: "public", Privileges: []string{"SELECT", "INSERT"}, Future: true},
		role:  "gt_role", roleSQL: "CREATE ROLE gt_role", dropSQL: "DROP ROLE IF EXISTS gt_role"},
	"MYSQL": {driver: model.DriverMySQL, host: "%", slow: "SELECT SLEEP(60)",
		grant: UserOp{Level: "table", Object: "gt_secret", Privileges: []string{"SELECT"}},
		wide:  UserOp{Level: "database", Privileges: []string{"SELECT", "INSERT"}, GrantOption: true},
		role:  "gt_role", roleSQL: "CREATE ROLE gt_role", dropSQL: "DROP ROLE IF EXISTS gt_role"},
	"MARIADB": {driver: model.DriverMariaDB, host: "%", slow: "SELECT SLEEP(60)",
		grant: UserOp{Level: "table", Object: "gt_secret", Privileges: []string{"SELECT"}},
		wide:  UserOp{Level: "database", Privileges: []string{"SELECT", "INSERT"}, GrantOption: true},
		role:  "gt_role", roleSQL: "CREATE ROLE gt_role", dropSQL: "DROP ROLE IF EXISTS gt_role"},
	"TIDB": {driver: model.DriverTiDB, host: "%", slow: "SELECT SLEEP(60)",
		grant: UserOp{Level: "table", Object: "gt_secret", Privileges: []string{"SELECT"}},
		wide:  UserOp{Level: "database", Privileges: []string{"SELECT", "INSERT"}}},
	"MSSQL": {driver: model.DriverMSSQL, schema: "dbo", slow: "WAITFOR DELAY '00:01:00'; SELECT 1",
		grant: UserOp{Level: "table", Schema: "dbo", Object: "gt_secret", Privileges: []string{"SELECT"}},
		wide:  UserOp{Level: "schema", Schema: "dbo", Privileges: []string{"SELECT", "INSERT"}},
		role:  "gt_role", roleSQL: "CREATE ROLE gt_role", dropSQL: "DROP ROLE IF EXISTS gt_role"},
	"CH": {driver: model.DriverClickHouse, slow: "SELECT sleepEachRow(1) FROM numbers(60) SETTINGS max_block_size = 1, function_sleep_max_microseconds_per_block = 100000000",
		grant: UserOp{Level: "table", Object: "gt_secret", Privileges: []string{"SELECT"}},
		wide:  UserOp{Level: "database", Privileges: []string{"SELECT", "INSERT"}},
		role:  "gt_role", roleSQL: "CREATE ROLE gt_role", dropSQL: "DROP ROLE IF EXISTS gt_role"},
}

// TestUsers makes a user, gives and takes privileges, and checks by signing
// in as it that they are what the server made of them.
func TestUsers(t *testing.T) {
	for name, x := range userTests {
		t.Run(name, func(t *testing.T) {
			cfg, pw := serverConfig(t, name, x.driver)
			c := openTest(t, cfg, pw)
			ctx := context.Background()
			exec := func(q string) error {
				rs, err := c.Execute(ctx, q, 0)
				if err == nil {
					err = rs[len(rs)-1].Err
				}
				return err
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			// A name that needs quoting.
			user := `gt u'se"r`
			if x.driver.Family() == model.DriverMSSQL || x.driver.Family() == model.DriverClickHouse {
				user = `gt u'ser`
			}
			change := func(ops ...UserOp) ([]string, error) {
				for i := range ops {
					ops[i].Name, ops[i].Host = user, x.host
					if ops[i].Level != "" && ops[i].Database == "" && x.schema == "" {
						ops[i].Database = cfg.DBName
					}
				}
				pre, err := c.ChangeUsers(ctx, ops, true)
				if err != nil {
					return nil, err
				}
				if strings.Contains(strings.Join(pre, "\n"), "ss") && strings.Contains(strings.Join(pre, "\n"), "0rd-1") {
					t.Errorf("the password shows: %v", pre)
				}
				return c.ChangeUsers(ctx, ops, false)
			}
			c.ChangeUsers(ctx, []UserOp{{Kind: "revoke", Name: user, Host: x.host, Level: "tables", Schema: "public", Privileges: []string{"SELECT", "INSERT"}, Future: true}}, false)
			c.ChangeUsers(ctx, []UserOp{{Kind: "drop", Name: user, Host: x.host, Reassign: x.driver.Family() == model.DriverPostgres}}, false)
			exec("DROP TABLE IF EXISTS gt_secret")
			tbl := "CREATE TABLE gt_secret (a INT)"
			if x.driver.Family() == model.DriverClickHouse {
				tbl += " ENGINE = MergeTree ORDER BY a"
			}
			must(exec(tbl))
			must(exec("INSERT INTO gt_secret VALUES (42)"))
			if x.role != "" {
				exec(x.dropSQL)
				must(exec(x.roleSQL))
			}
			defer func() {
				exec("DROP TABLE IF EXISTS gt_secret")
				if x.role != "" {
					exec(x.dropSQL)
				}
			}()

			create := UserOp{Kind: "create", Password: testPassword, Attributes: map[string]bool{"rolcanlogin": true}}
			if _, err := change(create); err != nil {
				t.Fatal(err)
			}
			admin, accounts, err := c.Users(ctx)
			must(err)
			if admin.Me == "" || len(admin.Levels) == 0 {
				t.Errorf("admin %+v", admin)
			}
			find := func() *Account {
				t.Helper()
				_, as, err := c.Users(ctx)
				must(err)
				for i := range as {
					if as[i].Name == user {
						return &as[i]
					}
				}
				return nil
			}
			if a := find(); a == nil || a.Role || a.System || a.Locked || a.Host != x.host {
				t.Fatalf("account %+v of %+v", a, accounts)
			}

			// as runs a query as the user.
			as := func(pass, q string) error {
				uc := *cfg
				uc.User, uc.SelectedSchema = user, ""
				if x.driver.Family() == model.DriverMySQL {
					uc.DBName = ""
				}
				u, err := Open(ctx, &uc, Credentials{Password: pass})
				if err != nil {
					return err
				}
				defer u.Close()
				if x.schema == "" {
					q = strings.Replace(q, "gt_secret", c.Dialect.QuoteIdent(cfg.DBName)+".gt_secret", 1)
				}
				rs, err := u.Execute(ctx, q, 0)
				if err == nil {
					err = rs[0].Err
				}
				return err
			}
			const read, write = "SELECT a FROM gt_secret", "INSERT INTO gt_secret VALUES (1)"
			canRead := func(want bool, what string) {
				t.Helper()
				if err := as(testPassword, read); (err == nil) != want {
					t.Errorf("%s: read %v", what, err)
				}
			}
			canWrite := func(want bool, what string) {
				t.Helper()
				if err := as(testPassword, write); (err == nil) != want {
					t.Errorf("%s: write %v", what, err)
				}
			}
			if err := as("wrong", "SELECT 1"); err == nil {
				t.Error("a wrong password")
			}
			canRead(false, "new user")

			x.grant.Kind = "grant"
			pre, err := change(x.grant)
			must(err)
			t.Log(pre)
			canRead(true, "granted")
			canWrite(false, "granted")
			grants := func() []Grant {
				t.Helper()
				g, err := c.UserGrants(ctx, user, x.host)
				must(err)
				return g
			}
			has := func(level, priv string) bool {
				return slices.ContainsFunc(grants(), func(g Grant) bool { return g.Level == level && slices.Contains(g.Privileges, priv) })
			}
			if !has("table", "SELECT") {
				t.Errorf("grants %+v", grants())
			}
			// What is listed can be revoked as it is.
			for _, g := range grants() {
				if g.Level == "table" {
					_, err := change(UserOp{Kind: "revoke", Level: g.Level, Database: g.Database, Schema: g.Schema, Object: g.Object, Pattern: true, Privileges: g.Privileges})
					must(err)
				}
			}
			canRead(false, "revoked")

			wide := x.wide
			wide.Kind = "grant"
			pre, err = change(wide)
			must(err)
			t.Log(pre)
			canRead(true, "wide")
			canWrite(true, "wide")
			if wide.Future {
				must(exec("DROP TABLE IF EXISTS gt_later"))
				must(exec("CREATE TABLE gt_later (id SERIAL, a INT)"))
				if err := as(testPassword, "INSERT INTO gt_later (a) VALUES (1)"); err != nil {
					t.Errorf("a later table: %v", err)
				}
				if !has("future", "INSERT") {
					t.Errorf("no future grant in %+v", grants())
				}
				must(exec("DROP TABLE gt_later"))
			}
			if wide.Level == "database" && !has("database", "INSERT") {
				t.Errorf("grants %+v", grants())
			}
			if wide.GrantOption && !slices.ContainsFunc(grants(), func(g Grant) bool { return g.GrantOption }) {
				t.Errorf("no grant option in %+v", grants())
			}
			for _, g := range grants() {
				if g.ReadOnly == "" && g.Level != "table" && (g.Level != "database" || x.schema == "") {
					if _, err := change(UserOp{Kind: "revoke", Level: g.Level, Database: g.Database, Schema: g.Schema, Object: g.Object, Pattern: true, Privileges: g.Privileges, GrantOption: g.GrantOption}); err != nil {
						t.Errorf("revoke %+v: %v", g, err)
					}
				}
			}
			if x.driver.Family() == model.DriverPostgres { // ALL TABLES gave it table by table
				wide.Kind = "revoke"
				_, err = change(wide)
				must(err)
			}
			canRead(false, "all revoked")

			// Roles, the password, locking.
			if x.role != "" {
				if _, err := change(UserOp{Kind: "grant_role", Role: x.role}); err != nil {
					t.Error(err)
				}
				g := find()
				inGrants := slices.ContainsFunc(grants(), func(g Grant) bool { return g.Level == "role" && slices.Contains(g.Privileges, x.role) })
				if !slices.Contains(g.Roles, x.role) && !inGrants {
					t.Errorf("roles %+v, grants %+v", g, grants())
				}
				if !slices.Contains(admin.Roles, x.role) {
					t.Errorf("no %s in %v", x.role, admin.Roles)
				}
				if _, err := change(UserOp{Kind: "revoke_role", Role: x.role}); err != nil {
					t.Error(err)
				}
			}
			if _, err := change(UserOp{Kind: "password", Password: testPassword + "2"}); err != nil {
				t.Error(err)
			}
			if err := as(testPassword, "SELECT 1"); err == nil {
				t.Error("the old password still works")
			}
			if err := as(testPassword+"2", "SELECT 1"); err != nil {
				t.Errorf("the new password: %v", err)
			}
			if admin.CanLock {
				if _, err := change(UserOp{Kind: "lock"}); err != nil {
					t.Error(err)
				}
				if err := as(testPassword+"2", "SELECT 1"); err == nil || !find().Locked {
					t.Errorf("locked: %v %+v", err, find())
				}
				if _, err := change(UserOp{Kind: "unlock"}); err != nil {
					t.Error(err)
				}
				if err := as(testPassword+"2", "SELECT 1"); err != nil || find().Locked {
					t.Errorf("unlocked: %v", err)
				}
			}
			if len(admin.Attributes) > 0 {
				if _, err := change(UserOp{Kind: "attributes", Attributes: map[string]bool{"rolcreatedb": true, "rolcanlogin": false}}); err != nil {
					t.Error(err)
				}
				if a := find(); !a.Attributes["rolcreatedb"] || a.Attributes["rolcanlogin"] || !a.Role {
					t.Errorf("attributes %+v", a)
				}
			}
			// Not a way to run other SQL.
			if _, err := change(UserOp{Kind: "grant", Level: x.grant.Level, Schema: x.grant.Schema, Object: x.grant.Object, Privileges: []string{"SELECT ON x; DROP TABLE y"}}); err == nil {
				t.Error("a privilege with SQL in it")
			}
			if _, err := change(UserOp{Kind: "drop", Reassign: x.driver.Family() == model.DriverPostgres}); err != nil {
				t.Fatal(err)
			}
			if find() != nil {
				t.Error("the user is still there")
			}
		})
	}
}

func TestMongoUsers(t *testing.T) {
	cfg, pw := serverConfig(t, "MONGODB", model.DriverMongoDB)
	c := openTest(t, cfg, pw)
	ctx := context.Background()
	me := func(op UserOp) UserOp { op.Name, op.Host = `gt u'se"r`, "admin"; return op }
	c.ChangeUsers(ctx, []UserOp{me(UserOp{Kind: "drop"})}, false)
	change := func(op UserOp) {
		t.Helper()
		shown, err := c.ChangeUsers(ctx, []UserOp{me(op)}, false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(shown[0], "0rd") {
			t.Errorf("the password shows: %s", shown)
		}
	}
	change(UserOp{Kind: "create", Password: testPassword})
	admin, users, err := c.Users(ctx)
	if err != nil || admin.Me == "" || !slices.ContainsFunc(users, func(a Account) bool { return a.Name == `gt u'se"r` && a.Host == "admin" }) {
		t.Fatalf("%v %+v %+v", err, admin, users)
	}
	c.Execute(ctx, `db.getSiblingDB("gt_users").t.drop()`, 0)
	if rs, err := c.Execute(ctx, `db.getSiblingDB("gt_users").t.insertOne({a: 1})`, 0); err != nil || rs[0].Err != nil {
		t.Fatal(err, rs)
	}
	as := func(q string) error {
		uc := *cfg
		uc.User, uc.DBName, uc.Options = `gt u'se"r`, "gt_users", "authSource=admin"
		u, err := Open(ctx, &uc, Credentials{Password: testPassword})
		if err != nil {
			return err
		}
		defer u.Close()
		rs, err := u.ExecuteIn(ctx, "gt_users", q, 0)
		if err == nil {
			err = rs[0].Err
		}
		return err
	}
	if err := as(`db.t.find()`); err == nil {
		t.Error("reads without a role")
	}
	change(UserOp{Kind: "grant", Level: "database", Database: "gt_users", Privileges: []string{"read"}})
	if err := as(`db.t.find()`); err != nil {
		t.Error(err)
	}
	if err := as(`db.t.insertOne({a: 2})`); err == nil {
		t.Error("writes with read")
	}
	gs, err := c.UserGrants(ctx, `gt u'se"r`, "admin")
	if err != nil || len(gs) != 1 || gs[0].Database != "gt_users" || gs[0].Privileges[0] != "read" {
		t.Errorf("%v %+v", err, gs)
	}
	change(UserOp{Kind: "grant", Level: "global", Privileges: []string{"readWriteAnyDatabase"}})
	if err := as(`db.t.insertOne({a: 2})`); err != nil {
		t.Error(err)
	}
	change(UserOp{Kind: "revoke", Level: "global", Privileges: []string{"readWriteAnyDatabase"}})
	change(UserOp{Kind: "revoke", Level: "database", Database: "gt_users", Privileges: []string{"read"}})
	if err := as(`db.t.find()`); err == nil {
		t.Error("reads after the revoke")
	}
	change(UserOp{Kind: "drop"})
	c.Execute(ctx, `db.getSiblingDB("gt_users").dropDatabase()`, 0)
}

// TestProcesses finds a slow query from a second connection, and stops it.
func TestProcesses(t *testing.T) {
	tests := map[string]userTest{"CRDB": {driver: model.DriverCockroachDB, slow: "SELECT pg_sleep(60)"}, "MONGODB": {driver: model.DriverMongoDB, slow: `db.gt_slow.find({$where: "sleep(1000) || true"})`}}
	for k, v := range userTests {
		tests[k] = v
	}
	for name, x := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, pw := serverConfig(t, name, x.driver)
			slow, admin := openTest(t, cfg, pw), openTest(t, cfg, pw)
			ctx := context.Background()
			if x.driver == model.DriverMongoDB {
				slow.Execute(ctx, `db.gt_slow.drop()`, 0)
				slow.Execute(ctx, `db.gt_slow.insertMany([{a:1},{a:2},{a:3},{a:4},{a:5},{a:6},{a:7},{a:8},{a:9},{a:10},{a:11},{a:12},{a:13},{a:14},{a:15},{a:16},{a:17},{a:18},{a:19},{a:20}])`, 0)
			}
			for _, kill := range []bool{false, true} {
				done := make(chan error, 1)
				start := time.Now()
				go func() {
					rs, err := slow.Execute(ctx, x.slow, 0)
					if err == nil {
						err = rs[0].Err
					}
					done <- err
				}()
				var p *Process
				var list *ProcessList
				for range 50 {
					time.Sleep(100 * time.Millisecond)
					var err error
					if list, err = admin.Processes(ctx); err != nil {
						t.Fatal(err)
					}
					for i := range list.Processes {
						q := strings.ToLower(list.Processes[i].Query)
						if list.Processes[i].Active && (strings.Contains(q, "sleep") || strings.Contains(q, "waitfor")) {
							p = &list.Processes[i]
						}
					}
					if p != nil && time.Since(start) > 1200*time.Millisecond {
						break
					}
				}
				if p == nil {
					t.Fatalf("the slow query isn't in %+v", list)
				}
				t.Logf("%+v", *p)
				if p.Seconds > 30 || p.ID == "" {
					t.Errorf("process %+v", p)
				}
				if kill && !list.CanKill || !kill && !list.CanStop {
					kill = !kill
				}
				if err := admin.StopProcess(ctx, p.ID, kill); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					// MySQL's SLEEP answers 1 when its query is killed.
					if err == nil && x.driver.Family() != model.DriverMySQL {
						t.Error("the query ended well")
					}
				case <-time.After(20 * time.Second):
					t.Fatal("the query goes on")
				}
				if time.Since(start) > 25*time.Second {
					t.Error("the query ended by itself")
				}
				// The session is there again (a killed one reconnects).
				var err error
				for range 3 {
					if x.driver == model.DriverMongoDB {
						break
					}
					var rs []Result
					if rs, err = slow.Execute(ctx, "SELECT 1", 0); err == nil {
						err = rs[0].Err
					}
					if err == nil {
						break
					}
				}
				if err != nil {
					t.Errorf("after it: %v", err)
				}
			}
			if err := admin.StopProcess(ctx, "999999999", true); err == nil && x.driver.Family() != model.DriverClickHouse && x.driver != model.DriverMongoDB && x.driver != model.DriverTiDB {
				t.Error("stopping nothing")
			}
			if err := admin.StopProcess(ctx, "1; DROP TABLE x", true); err == nil && x.driver != model.DriverMongoDB && x.driver.Family() != model.DriverClickHouse {
				t.Error("an id with SQL in it")
			}
		})
	}
}

func TestDiagram(t *testing.T) {
	for name, d := range map[string]model.Driver{"PG": model.DriverPostgres, "MYSQL": model.DriverMySQL, "MARIADB": model.DriverMariaDB, "MSSQL": model.DriverMSSQL, "CRDB": model.DriverCockroachDB, "SQLITE": model.DriverSQLite} {
		t.Run(name, func(t *testing.T) {
			var c *Conn
			if name == "SQLITE" {
				cfg := model.NewConnection(model.DriverSQLite)
				cfg.Name, cfg.Path = "x", t.TempDir()+"/d.db"
				c = openTest(t, cfg, "")
			} else {
				cfg, pw := serverConfig(t, name, d)
				c = openTest(t, cfg, pw)
			}
			ctx := context.Background()
			clean := "DROP TABLE IF EXISTS gt_er_item; DROP TABLE IF EXISTS gt_er_order; DROP TABLE IF EXISTS gt_er_person"
			c.Execute(ctx, clean, 0)
			rs, err := c.Execute(ctx, `CREATE TABLE gt_er_person (id INT NOT NULL PRIMARY KEY, boss INT, FOREIGN KEY (boss) REFERENCES gt_er_person (id));
				CREATE TABLE gt_er_order (a INT NOT NULL, b INT NOT NULL, person INT NOT NULL, PRIMARY KEY (a, b), FOREIGN KEY (person) REFERENCES gt_er_person (id) ON DELETE CASCADE);
				CREATE TABLE gt_er_item (id INT NOT NULL PRIMARY KEY, oa INT, ob INT, FOREIGN KEY (oa, ob) REFERENCES gt_er_order (a, b))`, 0)
			if err != nil || rs[len(rs)-1].Err != nil {
				t.Fatal(err, rs[len(rs)-1].Err)
			}
			defer c.Execute(ctx, clean, 0)
			tables, err := c.Diagram(ctx, c.Schema())
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]DiagramTable{}
			for _, x := range tables {
				got[x.Name] = x
			}
			o, i, p := got["gt_er_order"], got["gt_er_item"], got["gt_er_person"]
			if len(o.Columns) != 3 || !o.Columns[0].PrimaryKey || !o.Columns[1].PrimaryKey || o.Columns[2].PrimaryKey || o.Columns[2].Nullable || o.Columns[2].Name != "person" || o.Columns[2].Type == "" {
				t.Errorf("columns %+v", o.Columns)
			}
			if len(o.ForeignKeys) != 1 || o.ForeignKeys[0].Table != "gt_er_person" || o.ForeignKeys[0].OnDelete != "CASCADE" || o.ForeignKeys[0].Columns[0] != "id" {
				t.Errorf("order keys %+v", o.ForeignKeys)
			}
			if len(i.ForeignKeys) != 1 || strings.Join(i.ForeignKeys[0].From, ",") != "oa,ob" || strings.Join(i.ForeignKeys[0].Columns, ",") != "a,b" {
				t.Errorf("item keys %+v", i.ForeignKeys)
			}
			if len(p.ForeignKeys) != 1 || p.ForeignKeys[0].Table != "gt_er_person" || !p.Columns[1].Nullable {
				t.Errorf("person %+v", p)
			}
		})
	}
}
