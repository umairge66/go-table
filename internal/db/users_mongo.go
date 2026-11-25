package db

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// mongoUsers: an account belongs to the database it signs in to, and has
// roles on databases. The commands are written, and run, as Extended JSON.
type mongoUsers struct{}

var mongoDBRoles = []string{"read", "readWrite", "dbAdmin", "userAdmin", "dbOwner"}

func (mongoUsers) admin(ctx context.Context, c *Conn) (*UserAdmin, error) {
	a := &UserAdmin{HostLabel: "Authentication database", HostDefault: "admin", PrivilegeWord: "role",
		Levels: []GrantLevel{
			{Key: "database", Label: "Database", Needs: []string{"database"}, Privileges: mongoDBRoles, Presets: []GrantPreset{{"Read only", []string{"read"}}, {"Read and write", []string{"readWrite"}}, {"Owner", []string{"dbOwner"}}}},
			{Key: "global", Label: "The whole server", Privileges: []string{"readAnyDatabase", "readWriteAnyDatabase", "dbAdminAnyDatabase", "userAdminAnyDatabase", "clusterMonitor", "clusterAdmin", "backup", "restore", "root"}},
		}}
	var st struct {
		Info struct {
			Users []struct {
				User string `bson:"user"`
				DB   string `bson:"db"`
			} `bson:"authenticatedUsers"`
		} `bson:"authInfo"`
	}
	if err := c.mongo.Database("admin").RunCommand(ctx, bson.D{{Key: "connectionStatus", Value: 1}}).Decode(&st); err != nil {
		return nil, err
	}
	if len(st.Info.Users) > 0 {
		a.Me = st.Info.Users[0].User + "@" + st.Info.Users[0].DB
	}
	var err error
	a.Databases, err = mongoDatabases(ctx, c)
	return a, err
}

type mongoRole struct {
	Role string `bson:"role"`
	DB   string `bson:"db"`
}

type mongoUser struct {
	User  string      `bson:"user"`
	DB    string      `bson:"db"`
	Roles []mongoRole `bson:"roles"`
}

func (mongoUsers) read(ctx context.Context, c *Conn) ([]mongoUser, error) {
	var out struct {
		Users []mongoUser `bson:"users"`
	}
	err := c.mongo.Database("admin").RunCommand(ctx, bson.D{{Key: "usersInfo", Value: bson.D{{Key: "forAllDBs", Value: true}}}}).Decode(&out)
	return out.Users, err
}

func (u mongoUsers) accounts(ctx context.Context, c *Conn) ([]Account, error) {
	users, err := u.read(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]Account, len(users))
	for i, x := range users {
		out[i] = Account{Name: x.User, Host: x.DB, Attributes: map[string]bool{}}
	}
	return out, nil
}

func (u mongoUsers) grants(ctx context.Context, c *Conn, name, host string) ([]Grant, error) {
	users, err := u.read(ctx, c)
	if err != nil {
		return nil, err
	}
	out := []Grant{}
	for _, x := range users {
		if x.User != name || x.DB != host {
			continue
		}
		at := map[string]int{}
		for _, r := range x.Roles {
			g := Grant{Level: "database", Database: r.DB}
			if r.DB == "admin" && !contains(mongoDBRoles, r.Role) {
				g = Grant{Level: "global"}
			}
			key := g.Level + " " + g.Database
			i, ok := at[key]
			if !ok {
				i = len(out)
				at[key] = i
				out = append(out, g)
			}
			out[i].Privileges = append(out[i].Privileges, r.Role)
		}
	}
	return out, nil
}

func (mongoUsers) plan(_ context.Context, _ *Conn, op UserOp) ([]string, error) {
	in := op.Host
	if in == "" {
		in = "admin"
	}
	cmd := func(body string) []string {
		return []string{"db.getSiblingDB(" + jsonString(in) + ").runCommand({" + body + "})"}
	}
	name := jsonString(op.Name)
	switch op.Kind {
	case "create":
		if op.Password == "" {
			return nil, errors.New("a user needs a password")
		}
		return cmd(`"createUser": ` + name + `, "pwd": ` + passwordMark + `, "roles": []`), nil
	case "password":
		if op.Password == "" {
			return nil, errors.New("the password is missing")
		}
		return cmd(`"updateUser": ` + name + `, "pwd": ` + passwordMark), nil
	case "drop":
		return cmd(`"dropUser": ` + name), nil
	case "grant", "revoke":
	default:
		return nil, fmt.Errorf("unknown change %q", op.Kind)
	}
	a := &UserAdmin{PrivilegeWord: "role", Levels: []GrantLevel{{Key: "global"}, {Key: "database", Needs: []string{"database"}}}}
	if _, err := levelOf(a, op); err != nil {
		return nil, err
	}
	on := op.Database
	if op.Level == "global" {
		on = "admin"
	}
	roles := make(bson.A, len(op.Privileges))
	for i, r := range op.Privileges {
		roles[i] = bson.D{{Key: "role", Value: r}, {Key: "db", Value: on}}
	}
	verb := map[string]string{"grant": "grantRolesToUser", "revoke": "revokeRolesFromUser"}[op.Kind]
	return cmd(`"` + verb + `": ` + name + `, "roles": ` + mongoJSON(roles, false)), nil
}

// run reads back the commands plan wrote.
func (mongoUsers) run(ctx context.Context, c *Conn, stmts []string) error {
	for _, s := range stmts {
		cmd, err := parseShell(s)
		if err != nil {
			return err
		}
		if len(cmd.calls) != 1 || len(cmd.calls[0].args) != 1 {
			return fmt.Errorf("unexpected command %q", s)
		}
		body, ok := cmd.calls[0].args[0].(bson.D)
		if !ok {
			return fmt.Errorf("unexpected command %q", s)
		}
		if err := c.mongo.Database(cmd.database).RunCommand(ctx, body).Err(); err != nil {
			return err
		}
	}
	return nil
}
