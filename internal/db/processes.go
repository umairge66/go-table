package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"gotable/internal/model"
)

var errNoDiagram = errors.New("this database has no tables with foreign keys to draw")

// Process is a connection to the server and what it runs.
type Process struct {
	ID, User, Client, App, Database string
	// State in the server's words, and what it waits for.
	State, Wait string
	Query       string
	// Active: it runs something now, for Seconds; else it has been idle that long.
	Active  bool
	Seconds float64
	// BlockedBy are the processes whose locks it waits for.
	BlockedBy []string
}

// ProcessList is the server's processes, and what can be done to one.
type ProcessList struct {
	Processes []Process
	// CanStop ends what a process runs; CanKill closes its connection.
	CanStop, CanKill bool
}

var errNoProcesses = errors.New("this database has no process list GoTable can read")

// Processes lists what the server runs. Run it on a connection of its own:
// the session's is busy while its query runs.
func (c *Conn) Processes(ctx context.Context) (*ProcessList, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	switch {
	case c.mongo != nil:
		return c.mongoProcesses(ctx)
	case c.redis != nil:
		return c.redisProcesses(ctx)
	case c.conn == nil:
		return nil, errNoProcesses
	}
	switch {
	case c.Config.Driver == model.DriverCockroachDB:
		return c.crdbProcesses(ctx)
	case c.Config.Driver == model.DriverCrateDB || c.Config.Driver == model.DriverQuestDB:
		return nil, errNoProcesses
	}
	switch c.Dialect.family {
	case model.DriverPostgres:
		return c.pgProcesses(ctx)
	case model.DriverMySQL:
		return c.mysqlProcesses(ctx)
	case model.DriverMSSQL:
		return c.mssqlProcesses(ctx)
	case model.DriverClickHouse:
		return c.chProcesses(ctx)
	}
	return nil, errNoProcesses
}

// StopProcess ends what a process runs, or (kill) closes its connection.
func (c *Conn) StopProcess(ctx context.Context, id string, kill bool) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	gone := fmt.Errorf("process %s is no longer there", id)
	switch {
	case c.mongo != nil:
		var op any = id // "shard:123" on mongos
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			op = n
		}
		return c.mongo.Database("admin").RunCommand(ctx, bson.D{{Key: "killOp", Value: 1}, {Key: "op", Value: op}}).Err()
	case c.redis != nil:
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return gone
		}
		killed, err := c.redisClient(0).ClientKillByFilter(ctx, "ID", strconv.FormatInt(n, 10)).Result()
		if err == nil && killed == 0 {
			err = gone
		}
		return err
	case c.conn == nil:
		return errNoProcesses
	}
	if c.Config.Driver == model.DriverCockroachDB {
		_, err := c.conn.ExecContext(ctx, "CANCEL QUERY "+c.Dialect.QuoteLiteral(Text(id)))
		return err
	}
	if c.Dialect.family == model.DriverClickHouse {
		_, err := c.conn.ExecContext(ctx, "KILL QUERY WHERE query_id = "+c.Dialect.QuoteLiteral(Text(id))+" ASYNC")
		return err
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return gone
	}
	num := strconv.FormatInt(n, 10)
	switch c.Dialect.family {
	case model.DriverPostgres:
		fn := "pg_cancel_backend"
		if kill {
			fn = "pg_terminate_backend"
		}
		var ok bool
		if err := c.conn.QueryRowContext(ctx, "SELECT "+fn+"("+num+")").Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return gone
		}
		return nil
	case model.DriverMySQL:
		q := "KILL QUERY " + num
		if kill {
			q = "KILL " + num
		}
		_, err := c.conn.ExecContext(ctx, q)
		return err
	case model.DriverMSSQL:
		_, err := c.conn.ExecContext(ctx, "KILL "+num)
		return err
	}
	return errNoProcesses
}

func (c *Conn) pgProcesses(ctx context.Context) (*ProcessList, error) {
	const cols = `SELECT pid::text, coalesce(usename, ''), coalesce(host(client_addr), ''), coalesce(application_name, ''), coalesce(datname, ''), coalesce(state, ''), `
	const rest = `, coalesce(query, ''), coalesce(state, '') IN ('active', 'fastpath function call'),
		greatest(0, coalesce(extract(epoch FROM clock_timestamp() - CASE WHEN state = 'active' THEN query_start ELSE state_change END), 0))::float8, `
	q := cols + `coalesce(wait_event_type || ': ' || wait_event, '')` + rest + `array_to_string(pg_blocking_pids(pid), ',')
		FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND backend_type = 'client backend'`
	rows, err := c.conn.QueryContext(ctx, q)
	if err != nil { // Greenplum, older servers
		if rows, err = c.conn.QueryContext(ctx, cols+`''`+rest+`'' FROM pg_stat_activity WHERE pid <> pg_backend_pid()`); err != nil {
			return nil, err
		}
	}
	defer rows.Close()
	out := &ProcessList{Processes: []Process{}, CanStop: true, CanKill: true}
	for rows.Next() {
		var p Process
		var by string
		if err := rows.Scan(&p.ID, &p.User, &p.Client, &p.App, &p.Database, &p.State, &p.Wait, &p.Query, &p.Active, &p.Seconds, &by); err != nil {
			return nil, err
		}
		if by != "" {
			p.BlockedBy = strings.Split(by, ",")
		}
		if !p.Active || p.Wait == "Client: ClientRead" {
			p.Wait = ""
		}
		out.Processes = append(out.Processes, p)
	}
	return out, rows.Err()
}

func (c *Conn) crdbProcesses(ctx context.Context) (*ProcessList, error) {
	rows, err := c.conn.QueryContext(ctx, `SELECT query_id, coalesce(user_name, ''), coalesce(client_address, ''), coalesce(application_name, ''), coalesce(phase, ''), coalesce(query, ''),
		extract(epoch FROM clock_timestamp() - start::timestamptz)::float8
		FROM crdb_internal.cluster_queries WHERE query NOT LIKE '%crdb_internal.cluster_queries%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &ProcessList{Processes: []Process{}, CanStop: true}
	for rows.Next() {
		p := Process{Active: true}
		if err := rows.Scan(&p.ID, &p.User, &p.Client, &p.App, &p.State, &p.Query, &p.Seconds); err != nil {
			return nil, err
		}
		out.Processes = append(out.Processes, p)
	}
	return out, rows.Err()
}

// rowMaps reads a result whose columns differ between servers, by name.
func (c *Conn) rowMaps(ctx context.Context, q string, args ...any) ([]map[string]string, error) {
	rows, err := c.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	rs, _, err := scanRows(rows, 0)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]string, len(rs.Rows))
	for i, r := range rs.Rows {
		m := make(map[string]string, len(r))
		for j, v := range r {
			if !v.Null {
				m[strings.ToLower(rs.Columns[j])] = v.Text
			}
		}
		out[i] = m
	}
	return out, nil
}

func (c *Conn) mysqlProcesses(ctx context.Context) (*ProcessList, error) {
	var own string
	c.conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&own)
	ms, err := c.rowMaps(ctx, "SHOW FULL PROCESSLIST")
	if err != nil {
		return nil, err
	}
	out := &ProcessList{Processes: []Process{}, CanStop: true, CanKill: true}
	for _, m := range ms {
		if m["id"] == own {
			continue
		}
		p := Process{ID: m["id"], User: m["user"], Client: m["host"], Database: m["db"], State: m["command"], Wait: m["state"], Query: m["info"]}
		p.Seconds, _ = strconv.ParseFloat(m["time"], 64)
		p.Active = m["info"] != "" && m["command"] != "Sleep" && m["command"] != "Daemon"
		out.Processes = append(out.Processes, p)
	}
	return out, nil
}

func (c *Conn) mssqlProcesses(ctx context.Context) (*ProcessList, error) {
	ms, err := c.rowMaps(ctx, `SELECT s.session_id AS id, s.login_name AS [user], s.host_name AS client, s.program_name AS app, DB_NAME(s.database_id) AS db,
			COALESCE(r.status, s.status) AS state, r.wait_type AS wait, r.blocking_session_id AS blocked, COALESCE(t.text, lt.text) AS query,
			CASE WHEN r.session_id IS NULL THEN 0 ELSE 1 END AS active,
			CASE WHEN r.session_id IS NULL THEN DATEDIFF(second, s.last_request_end_time, GETDATE()) ELSE r.total_elapsed_time / 1000.0 END AS seconds
		FROM sys.dm_exec_sessions s
		LEFT JOIN sys.dm_exec_requests r ON r.session_id = s.session_id
		LEFT JOIN sys.dm_exec_connections cn ON cn.session_id = s.session_id AND cn.parent_connection_id IS NULL
		OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t
		OUTER APPLY sys.dm_exec_sql_text(cn.most_recent_sql_handle) lt
		WHERE s.is_user_process = 1 AND s.session_id <> @@SPID`)
	if err != nil {
		return nil, err
	}
	out := &ProcessList{Processes: []Process{}, CanKill: true}
	for _, m := range ms {
		p := Process{ID: m["id"], User: m["user"], Client: m["client"], App: m["app"], Database: m["db"], State: m["state"], Wait: m["wait"], Query: m["query"], Active: m["active"] == "1"}
		p.Seconds, _ = strconv.ParseFloat(m["seconds"], 64)
		p.Seconds = max(0, p.Seconds)
		if b := m["blocked"]; b != "" && b != "0" {
			p.BlockedBy = []string{b}
		}
		out.Processes = append(out.Processes, p)
	}
	return out, nil
}

func (c *Conn) chProcesses(ctx context.Context) (*ProcessList, error) {
	ms, err := c.rowMaps(ctx, `SELECT query_id AS id, user, toString(address) AS client, client_name AS app, current_database AS db, query_kind AS state, query, elapsed AS seconds
		FROM system.processes WHERE query_id <> queryID()`)
	if err != nil {
		return nil, err
	}
	out := &ProcessList{Processes: []Process{}, CanStop: true}
	for _, m := range ms {
		p := Process{ID: m["id"], User: m["user"], Client: strings.TrimPrefix(m["client"], "::ffff:"), App: m["app"], Database: m["db"], State: m["state"], Query: m["query"], Active: true}
		p.Seconds, _ = strconv.ParseFloat(m["seconds"], 64)
		out.Processes = append(out.Processes, p)
	}
	return out, nil
}

func (c *Conn) mongoProcesses(ctx context.Context) (*ProcessList, error) {
	read := func(all bool) ([]bson.D, error) {
		cur, err := c.mongo.Database("admin").Aggregate(ctx, mongo.Pipeline{bson.D{{Key: "$currentOp", Value: bson.D{{Key: "allUsers", Value: all}, {Key: "idleConnections", Value: false}}}}})
		if err != nil {
			return nil, err
		}
		docs, _, err := drainMongo(ctx, cur, 0)
		return docs, err
	}
	docs, err := read(true)
	if err != nil { // without the right to see others', one's own
		if docs, err = read(false); err != nil {
			return nil, err
		}
	}
	str := func(d bson.D, k string) string {
		v, _ := mongoGet(d, k)
		if v == nil {
			return ""
		}
		return mongoValue(v).Text
	}
	out := &ProcessList{Processes: []Process{}, CanStop: true}
	for _, d := range docs {
		cmd, _ := mongoGet(d, "command")
		text := ""
		if cd, ok := cmd.(bson.D); ok {
			kept := bson.D{}
			for _, e := range cd {
				switch e.Key { // the session's business, not the query's
				case "lsid", "$clusterTime", "$db", "$readPreference", "apiVersion", "txnNumber", "autocommit":
				default:
					kept = append(kept, e)
				}
			}
			text = mongoJSON(kept, false)
		}
		if strings.Contains(text, "$currentOp") || str(d, "client") == "" && str(d, "client_s") == "" {
			continue // this very command, and the server's own work
		}
		p := Process{ID: str(d, "opid"), Client: str(d, "client"), App: str(d, "appName"), Database: str(d, "ns"), State: str(d, "op"), Query: text, Active: str(d, "active") == "true"}
		if text == "{}" || text == "" {
			continue
		}
		if us, ok := mongoGet(d, "effectiveUsers"); ok {
			if a, ok := us.(bson.A); ok && len(a) > 0 {
				if u, ok := a[0].(bson.D); ok {
					p.User = str(u, "user")
				}
			}
		}
		if str(d, "waitingForLock") == "true" {
			p.Wait = "waiting for a lock"
		}
		if us, err := strconv.ParseFloat(str(d, "microsecs_running"), 64); err == nil {
			p.Seconds = us / 1e6
		}
		out.Processes = append(out.Processes, p)
	}
	return out, nil
}

func (c *Conn) redisProcesses(ctx context.Context) (*ProcessList, error) {
	cl := c.redisClient(0)
	text, err := cl.ClientList(ctx).Result()
	if err != nil {
		return nil, err
	}
	out := &ProcessList{Processes: []Process{}, CanKill: true}
	for line := range strings.Lines(text) {
		m := map[string]string{}
		for _, f := range strings.Fields(line) {
			k, v, _ := strings.Cut(f, "=")
			m[k] = v
		}
		if m["id"] == "" || strings.HasPrefix(m["cmd"], "client") {
			continue
		}
		p := Process{ID: m["id"], User: m["user"], Client: m["addr"], App: m["name"], Database: "db" + m["db"], Query: strings.ToUpper(strings.ReplaceAll(m["cmd"], "|", " "))}
		idle, _ := strconv.ParseInt(m["idle"], 10, 64)
		p.Seconds = float64(idle)
		p.State = "last command"
		if strings.Contains(m["flags"], "b") {
			p.State, p.Active = "waiting (blocking command)", true
		}
		if age, err := strconv.ParseInt(m["age"], 10, 64); err == nil {
			p.Wait = "connected " + (time.Duration(age) * time.Second).String() + " ago"
		}
		out.Processes = append(out.Processes, p)
	}
	return out, nil
}
