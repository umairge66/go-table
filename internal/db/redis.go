package db

// Redis (and Valkey, KeyDB, Dragonfly…): a key-value store, shown the way
// the rest of GoTable shows tables.
//
//   - A database is one of Redis's numbered ones: db0, db1…
//   - A "table" is a SCAN MATCH pattern. The sidebar lists the namespaces
//     found in the keys (user:*, session:*) and * for all keys. A pattern
//     with wildcards lists its keys (key, type, TTL, value); one without
//     names a single key, whose contents are the rows: a hash's fields, a
//     list's items, a set's members, a sorted set's members and scores, a
//     stream's entries, a string's value.
//   - Edits become Redis commands, checked under WATCH and applied in one
//     MULTI/EXEC, so a key changed meanwhile is reported, not overwritten.
//   - The query editor runs redis-cli style commands, one a line.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"gotable/internal/model"
)

const (
	redisMaxKeys   = 100_000 // keys a pattern lists at most
	redisMaxMeta   = 20_000  // keys whose type and TTL are read to filter or sort by them
	redisMaxItems  = 100_000 // items of a hash, set… read at most
	redisMaxInline = 64 << 10
	redisMaxValue  = 4 << 20 // a string shown whole in its own view
	redisPageScan  = 1000    // SCAN COUNT
)

type redisState struct {
	opts    redis.Options
	clients map[int]*redis.Client
	closed  bool
	server  string // Redis, Valkey, Dragonfly…
	dbs     int    // numbered databases
}

type redisEngine struct{}

var errRedisNoSQL = errors.New("Redis has keys, not tables: use its commands in the query editor (SET, HSET, …)")

func openRedis(ctx context.Context, cfg *model.Connection, addr, password string, tunnel io.Closer) (*Conn, error) {
	fail := func(err error) (*Conn, error) {
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, err
	}
	t, err := mongoTLS(cfg, tunnel != nil)
	if err != nil {
		return fail(err)
	}
	st := &redisState{clients: map[int]*redis.Client{}, dbs: 16}
	st.opts = redis.Options{Addr: addr, Username: cfg.User, Password: password, TLSConfig: t,
		Protocol: 2, DialTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 30 * time.Second,
		ContextTimeoutEnabled: true, PoolSize: 4, DisableIdentity: true}
	c := &Conn{Config: cfg, Dialect: DialectFor(cfg.Driver), eng: redisEngine{}, tunnel: tunnel, redis: st}
	start := 0
	if cfg.DBName != "" {
		if start, err = strconv.Atoi(cfg.DBName); err != nil {
			return fail(fmt.Errorf("a Redis database is a number, such as 0"))
		}
	}
	if cfg.SelectedSchema != "" {
		if n, err := redisIndex(cfg.SelectedSchema); err == nil {
			start = n
		}
	}
	cl := c.redisClient(start)
	if err := cl.Ping(ctx).Err(); err != nil {
		c.closeRedis()
		return fail(err)
	}
	info, _ := cl.Info(ctx, "server").Result()
	st.server = redisServerName(info)
	// Managed servers may hide CONFIG; 16 is Redis's default.
	if m, err := cl.ConfigGet(ctx, "databases").Result(); err == nil {
		if n, err := strconv.Atoi(m["databases"]); err == nil && n > 0 {
			st.dbs = min(n, 256)
		}
	}
	c.schema = redisSchema(start)
	return c, nil
}

func redisSchema(n int) string { return "db" + strconv.Itoa(n) }

func redisIndex(schema string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(schema, "db"))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q isn't a Redis database (db0, db1…)", schema)
	}
	return n, nil
}

// redisClient is the client for a numbered database (one pool each).
func (c *Conn) redisClient(db int) *redis.Client {
	if cl := c.redis.clients[db]; cl != nil {
		return cl
	}
	o := c.redis.opts
	o.DB = db
	cl := redis.NewClient(&o)
	c.redis.clients[db] = cl
	return cl
}

// redisFor: the client of a schema (db3), or the session's.
func (c *Conn) redisFor(schema string) (*redis.Client, error) {
	if schema == "" {
		schema = c.schema
	}
	n, err := redisIndex(schema)
	if err != nil {
		return nil, err
	}
	return c.redisClient(n), nil
}

// closeRedis is Close for Redis. Callers hold c.mu.
func (c *Conn) closeRedis() error {
	if c.redis.closed {
		return nil
	}
	c.redis.closed = true
	var errs []error
	for _, cl := range c.redis.clients {
		errs = append(errs, cl.Close())
	}
	if c.tunnel != nil {
		errs = append(errs, c.tunnel.Close())
	}
	return errors.Join(errs...)
}

// redisServerName reads which server answers: Valkey and Dragonfly say so.
func redisServerName(info string) string {
	fields := map[string]string{}
	for _, line := range strings.Split(info, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok {
			fields[k] = v
		}
	}
	switch {
	case fields["dragonfly_version"] != "":
		return "Dragonfly " + strings.TrimPrefix(fields["dragonfly_version"], "df-v")
	case fields["valkey_version"] != "":
		return "Valkey " + fields["valkey_version"]
	case strings.EqualFold(fields["server_name"], "valkey"):
		return "Valkey " + fields["redis_version"]
	case fields["redis_version"] != "":
		return "Redis " + fields["redis_version"]
	}
	return "Redis"
}

// ---------------------------------------------------------------- engine

func (redisEngine) serverVersion(_ context.Context, c *Conn) (string, error) {
	return c.redis.server, nil
}

func (redisEngine) databases(context.Context, *Conn) ([]string, error) { return []string{}, nil }

// schemas lists the numbered databases.
func (redisEngine) schemas(_ context.Context, c *Conn) ([]string, error) {
	out := make([]string, c.redis.dbs)
	for i := range out {
		out[i] = redisSchema(i)
	}
	return out, nil
}

func (redisEngine) currentSchema(_ context.Context, c *Conn) (string, error) { return c.schema, nil }

func (redisEngine) useSchema(_ context.Context, c *Conn, schema string) error {
	n, err := redisIndex(schema)
	if err != nil {
		return err
	}
	if n >= c.redis.dbs {
		return fmt.Errorf("the server has %d databases (db0 to db%d)", c.redis.dbs, c.redis.dbs-1)
	}
	return nil
}

// objects: * for every key, then the namespaces (prefix:*) found in a
// sample of the keys.
func (redisEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	cl, err := c.redisFor(schema)
	if err != nil {
		return nil, err
	}
	prefixes := map[string]bool{}
	var cursor uint64
	seen := 0
	for {
		keys, next, err := cl.Scan(ctx, cursor, "", redisPageScan).Result()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if i := strings.IndexByte(k, ':'); i > 0 {
				prefixes[k[:i]] = true
			}
		}
		seen += len(keys)
		cursor = next
		if cursor == 0 || seen >= 20_000 {
			break
		}
	}
	names := make([]string, 0, len(prefixes))
	for p := range prefixes {
		names = append(names, globEscape(p)+":*")
	}
	sort.Strings(names)
	out := []Object{{Schema: schema, Name: "*", Kind: KindTable}}
	for _, n := range names {
		out = append(out, Object{Schema: schema, Name: n, Kind: KindTable})
	}
	return out, nil
}

// redisKind: a pattern's view (keys) or a key's type.
func (c *Conn) redisKind(ctx context.Context, cl *redis.Client, t TableRef) (key, typ string, err error) {
	key, ok := literalKey(t.Name)
	if !ok {
		return "", "keys", nil
	}
	typ, err = cl.Type(ctx, key).Result()
	if err == nil && typ == "none" {
		err = fmt.Errorf("the key %q doesn't exist (any more)", key)
	}
	return key, typ, err
}

func rcol(name, typ string, pk, nullable bool) Column {
	return Column{Name: name, Type: typ, PrimaryKey: pk, Nullable: nullable}
}

// redisColumns are a view's columns: a key list's, or a key type's.
func redisColumns(typ string, streamFields []string) []Column {
	switch typ {
	case "keys":
		return []Column{rcol("key", "key", true, false), rcol("type", "type", false, false), rcol("ttl", "integer", false, true), rcol("value", "text", false, true)}
	case "hash":
		return []Column{rcol("field", "text", true, false), rcol("value", "text", false, false)}
	case "list":
		return []Column{rcol("index", "integer", true, false), rcol("value", "text", false, false)}
	case "set":
		return []Column{rcol("member", "text", true, false)}
	case "zset":
		return []Column{rcol("member", "text", true, false), rcol("score", "double", false, false)}
	case "stream":
		cols := []Column{rcol("id", "text", true, false)}
		for _, f := range streamFields {
			cols = append(cols, rcol(f, "text", false, true))
		}
		return cols
	}
	// string, JSON and module types: the value.
	return []Column{rcol("key", "key", true, false), rcol("value", "text", false, false)}
}

func (redisEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	cl, err := c.redisFor(t.Schema)
	if err != nil {
		return nil, err
	}
	key, typ, err := c.redisKind(ctx, cl, t)
	if err != nil {
		return nil, err
	}
	var fields []string
	if typ == "stream" {
		entries, err := cl.XRangeN(ctx, key, "-", "+", 200).Result()
		if err != nil {
			return nil, err
		}
		fields = streamFieldNames(entries)
	}
	return redisColumns(typ, fields), nil
}

func streamFieldNames(entries []redis.XMessage) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		var names []string
		for f := range e.Values {
			if !seen[f] {
				names = append(names, f)
			}
		}
		sort.Strings(names)
		for _, f := range names {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func (redisEngine) indexes(context.Context, *Conn, TableRef) ([]Index, error) { return []Index{}, nil }

func (redisEngine) foreignKeys(context.Context, *Conn, TableRef) ([]ForeignRef, error) {
	return nil, nil
}

// tableInfo says, for a key, its type, encoding, TTL, size and memory.
func (redisEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	cl, err := c.redisFor(t.Schema)
	if err != nil {
		return TableInfo{}, err
	}
	key, typ, err := c.redisKind(ctx, cl, t)
	if err != nil || typ == "keys" {
		return TableInfo{}, err
	}
	parts := []string{redisTypeName(typ)}
	if enc, err := cl.ObjectEncoding(ctx, key).Result(); err == nil {
		parts = append(parts, "encoded as "+enc)
	}
	if ms, err := cl.PTTL(ctx, key).Result(); err == nil && ms > 0 {
		parts = append(parts, "expires in "+seconds(int64(math.Ceil(ms.Seconds()))))
	} else {
		parts = append(parts, "no expiry")
	}
	if n, err := cl.MemoryUsage(ctx, key).Result(); err == nil {
		parts = append(parts, bytesText(n)+" of memory")
	}
	return TableInfo{Options: strings.Join(parts, " · ")}, nil
}

func redisTypeName(typ string) string {
	switch typ {
	case "zset":
		return "sorted set"
	case "ReJSON-RL":
		return "JSON"
	}
	return typ
}

// definition: the commands that make the key again; for a pattern, how to list it.
func (redisEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	cl, err := c.redisFor(o.Schema)
	if err != nil {
		return "", err
	}
	key, ok := literalKey(o.Name)
	if !ok {
		return "# Keys matching " + o.Name + "\nSCAN 0 MATCH " + quoteRedisArg(o.Name) + " COUNT 1000\n", nil
	}
	var b strings.Builder
	if err := redisDumpKey(ctx, cl, &b, key, false); err != nil {
		return "", err
	}
	return b.String(), nil
}

// ---------------------------------------------------------------- rows

// redisView reads a view whole or a page of it: its columns, the page's
// rows and how many rows the view has.
func (c *Conn) redisView(ctx context.Context, t TableRef, p Page, all bool) ([]string, [][]Value, int64, error) {
	if err := c.lock(ctx); err != nil {
		return nil, nil, 0, err
	}
	defer c.mu.Unlock()
	cl, err := c.redisFor(t.Schema)
	if err != nil {
		return nil, nil, 0, err
	}
	if strings.TrimSpace(p.RawWhere) != "" {
		return nil, nil, 0, errors.New("Redis has no WHERE: filter by the columns, or use a key pattern")
	}
	key, typ, err := c.redisKind(ctx, cl, t)
	if err != nil {
		return nil, nil, 0, err
	}
	if typ == "keys" {
		return redisKeyRows(ctx, cl, t.Name, p, all)
	}
	cols, rows, err := redisKeyContents(ctx, cl, key, typ)
	if err != nil {
		return nil, nil, 0, err
	}
	if rows, err = filterRows(cols, rows, p.Filters); err != nil {
		return nil, nil, 0, err
	}
	if err := sortRows(cols, rows, p.Sort); err != nil {
		return nil, nil, 0, err
	}
	total := int64(len(rows))
	if !all {
		rows = pageOf(rows, p)
	}
	return cols, rows, total, nil
}

func pageOf[T any](rows []T, p Page) []T {
	if p.Offset >= len(rows) {
		return rows[:0]
	}
	rows = rows[p.Offset:]
	if p.Limit > 0 && p.Limit < len(rows) {
		rows = rows[:p.Limit]
	}
	return rows
}

// redisKeyRows lists the keys of a pattern: SCAN, then the type, TTL and a
// value of the page's keys (of all of them when filtered or sorted by those).
func redisKeyRows(ctx context.Context, cl *redis.Client, pattern string, p Page, all bool) ([]string, [][]Value, int64, error) {
	cols := []string{"key", "type", "ttl", "value"}
	// SCAN filters by type itself.
	typeOnly := ""
	var keyFilters, metaFilters []Filter
	for _, f := range p.Filters {
		switch {
		case f.Column == "key":
			keyFilters = append(keyFilters, f)
		case f.Column == "type" && f.Op == OpEq && typeOnly == "":
			typeOnly = redisTypeCode(f.Value)
		default:
			metaFilters = append(metaFilters, f)
		}
	}
	keys, err := scanKeys(ctx, cl, pattern, typeOnly, redisMaxKeys)
	if err != nil {
		return nil, nil, 0, err
	}
	if len(keyFilters) > 0 {
		var kept []string
		for _, k := range keys {
			rows, _ := filterRows([]string{"key"}, [][]Value{{Text(k)}}, keyFilters)
			if len(rows) == 1 {
				kept = append(kept, k)
			}
		}
		keys = kept
	}
	byKeyOnly := true
	for _, s := range p.Sort {
		if s.Column != "key" {
			byKeyOnly = false
		}
	}
	if len(metaFilters) == 0 && byKeyOnly {
		sort.Strings(keys)
		if len(p.Sort) > 0 && p.Sort[0].Dir == Desc {
			for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
		total := int64(len(keys))
		if !all {
			keys = pageOf(keys, p)
		}
		rows, err := redisKeyMeta(ctx, cl, keys)
		return cols, rows, total, err
	}
	if len(keys) > redisMaxMeta {
		return nil, nil, 0, fmt.Errorf("%d keys match: too many to filter or sort by type, TTL or value; narrow the key pattern first", len(keys))
	}
	rows, err := redisKeyMeta(ctx, cl, keys)
	if err != nil {
		return nil, nil, 0, err
	}
	if rows, err = filterRows(cols, rows, metaFilters); err != nil {
		return nil, nil, 0, err
	}
	sorts := p.Sort
	if len(sorts) == 0 {
		sorts = []Sort{{Column: "key", Dir: Asc}}
	}
	if err := sortRows(cols, rows, sorts); err != nil {
		return nil, nil, 0, err
	}
	total := int64(len(rows))
	if !all {
		rows = pageOf(rows, p)
	}
	return cols, rows, total, nil
}

// redisTypeCode: the name SCAN TYPE takes for a type as GoTable shows it.
func redisTypeCode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "sorted set":
		return "zset"
	case "json":
		return "ReJSON-RL"
	}
	return strings.TrimSpace(s)
}

func scanKeys(ctx context.Context, cl *redis.Client, pattern, typ string, max int) ([]string, error) {
	if pattern == "*" {
		pattern = ""
	}
	seen := map[string]bool{}
	var out []string
	var cursor uint64
	for {
		var keys []string
		var next uint64
		var err error
		if typ != "" {
			keys, next, err = cl.ScanType(ctx, cursor, pattern, redisPageScan, typ).Result()
		} else {
			keys, next, err = cl.Scan(ctx, cursor, pattern, redisPageScan).Result()
		}
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if !seen[k] { // SCAN may return a key twice
				seen[k] = true
				out = append(out, k)
			}
		}
		cursor = next
		if cursor == 0 || len(out) >= max {
			return out, nil
		}
	}
}

// redisKeyMeta reads keys' type and TTL, then a short form of their value.
func redisKeyMeta(ctx context.Context, cl *redis.Client, keys []string) ([][]Value, error) {
	rows := make([][]Value, len(keys))
	if len(keys) == 0 {
		return rows, nil
	}
	types := make([]*redis.StatusCmd, len(keys))
	ttls := make([]*redis.DurationCmd, len(keys))
	if _, err := cl.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, k := range keys {
			types[i] = p.Type(ctx, k)
			ttls[i] = p.PTTL(ctx, k)
		}
		return nil
	}); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	sizes := make([]*redis.IntCmd, len(keys))
	if _, err := cl.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, k := range keys {
			switch types[i].Val() {
			case "string":
				sizes[i] = p.StrLen(ctx, k)
			case "hash":
				sizes[i] = p.HLen(ctx, k)
			case "list":
				sizes[i] = p.LLen(ctx, k)
			case "set":
				sizes[i] = p.SCard(ctx, k)
			case "zset":
				sizes[i] = p.ZCard(ctx, k)
			case "stream":
				sizes[i] = p.XLen(ctx, k)
			}
		}
		return nil
	}); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	// Short strings are shown whole; the rest by their size.
	strs := make([]*redis.StringCmd, len(keys))
	if _, err := cl.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, k := range keys {
			if types[i].Val() == "string" && sizes[i] != nil && sizes[i].Val() <= redisMaxInline {
				strs[i] = p.Get(ctx, k)
			}
		}
		return nil
	}); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	for i, k := range keys {
		typ := types[i].Val()
		ttl := NullValue
		if ms := ttls[i].Val(); ms > 0 {
			ttl = Text(strconv.FormatInt(int64(math.Ceil(ms.Seconds())), 10))
		}
		val := NullValue
		switch {
		case strs[i] != nil:
			val = redisText(strs[i].Val())
		case typ == "none":
			typ = "(deleted)"
		case sizes[i] != nil:
			val = Text(redisSummary(typ, sizes[i].Val()))
		default:
			val = Text("‹" + redisTypeName(typ) + "›")
		}
		rows[i] = []Value{redisText(k), Text(redisTypeName(typ)), ttl, val}
	}
	return rows, nil
}

// redisSummary stands for a value too big or too structured for a cell.
func redisSummary(typ string, n int64) string {
	unit := map[string][2]string{"hash": {"field", "fields"}, "list": {"item", "items"}, "set": {"member", "members"},
		"zset": {"member", "members"}, "stream": {"entry", "entries"}}[typ]
	if typ == "string" {
		return "‹string, " + bytesText(n) + ": open the key›"
	}
	word := unit[1]
	if n == 1 {
		word = unit[0]
	}
	return fmt.Sprintf("‹%s: %d %s›", redisTypeName(typ), n, word)
}

// redisKeyContents reads one key whole: a hash's fields, a list's items…
func redisKeyContents(ctx context.Context, cl *redis.Client, key, typ string) ([]string, [][]Value, error) {
	var rows [][]Value
	switch typ {
	case "string":
		n, err := cl.StrLen(ctx, key).Result()
		if err != nil {
			return nil, nil, err
		}
		if n > redisMaxValue {
			return nil, nil, fmt.Errorf("the value is %s: too big to show here; read part of it with GETRANGE in the query editor", bytesText(n))
		}
		v, err := cl.Get(ctx, key).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, nil, err
		}
		return []string{"key", "value"}, [][]Value{{redisText(key), redisText(v)}}, nil
	case "ReJSON-RL":
		v, err := cl.Do(ctx, "JSON.GET", key).Text()
		if err != nil {
			return nil, nil, err
		}
		return []string{"key", "value"}, [][]Value{{redisText(key), Text(v)}}, nil
	case "hash":
		var cursor uint64
		for {
			kv, next, err := cl.HScan(ctx, key, cursor, "", redisPageScan).Result()
			if err != nil {
				return nil, nil, err
			}
			for i := 0; i+1 < len(kv); i += 2 {
				rows = append(rows, []Value{redisText(kv[i]), redisText(kv[i+1])})
			}
			cursor = next
			if cursor == 0 || len(rows) >= redisMaxItems {
				break
			}
		}
		rows = uniqueRows(rows)
		sort.Slice(rows, func(i, j int) bool { return rows[i][0].Text < rows[j][0].Text })
		return []string{"field", "value"}, rows, nil
	case "list":
		items, err := cl.LRange(ctx, key, 0, redisMaxItems-1).Result()
		if err != nil {
			return nil, nil, err
		}
		for i, v := range items {
			rows = append(rows, []Value{Text(strconv.Itoa(i)), redisText(v)})
		}
		return []string{"index", "value"}, rows, nil
	case "set":
		var cursor uint64
		for {
			ms, next, err := cl.SScan(ctx, key, cursor, "", redisPageScan).Result()
			if err != nil {
				return nil, nil, err
			}
			for _, m := range ms {
				rows = append(rows, []Value{redisText(m)})
			}
			cursor = next
			if cursor == 0 || len(rows) >= redisMaxItems {
				break
			}
		}
		rows = uniqueRows(rows)
		sort.Slice(rows, func(i, j int) bool { return rows[i][0].Text < rows[j][0].Text })
		return []string{"member"}, rows, nil
	case "zset":
		zs, err := cl.ZRangeWithScores(ctx, key, 0, redisMaxItems-1).Result()
		if err != nil {
			return nil, nil, err
		}
		for _, z := range zs {
			m, _ := z.Member.(string)
			rows = append(rows, []Value{redisText(m), Text(strconv.FormatFloat(z.Score, 'f', -1, 64))})
		}
		return []string{"member", "score"}, rows, nil
	case "stream":
		entries, err := cl.XRangeN(ctx, key, "-", "+", redisMaxItems).Result()
		if err != nil {
			return nil, nil, err
		}
		fields := streamFieldNames(entries)
		for _, e := range entries {
			row := []Value{Text(e.ID)}
			for _, f := range fields {
				if v, ok := e.Values[f]; ok {
					row = append(row, redisText(fmt.Sprint(v)))
				} else {
					row = append(row, NullValue)
				}
			}
			rows = append(rows, row)
		}
		return append([]string{"id"}, fields...), rows, nil
	}
	return nil, nil, fmt.Errorf("GoTable can't show a %s key: read it in the query editor", typ)
}

// uniqueRows drops repeats (HSCAN and SSCAN may return an item twice).
func uniqueRows(rows [][]Value) [][]Value {
	seen := map[string]bool{}
	out := rows[:0]
	for _, r := range rows {
		if !seen[r[0].Text] {
			seen[r[0].Text] = true
			out = append(out, r)
		}
	}
	return out
}

func (c *Conn) redisFetchRows(ctx context.Context, t TableRef, p Page) (*ResultSet, error) {
	cols, rows, _, err := c.redisView(ctx, t, p, false)
	if err != nil {
		return nil, err
	}
	return &ResultSet{Columns: cols, Types: make([]string, len(cols)), Rows: rows}, nil
}

func (c *Conn) redisCountRows(ctx context.Context, t TableRef, p Page) (int64, error) {
	_, _, total, err := c.redisView(ctx, t, Page{Filters: p.Filters, RawWhere: p.RawWhere}, false)
	return total, err
}

func (c *Conn) redisStreamRows(ctx context.Context, t TableRef, p Page, fn func(cols, types []string, row []Value) error) error {
	cols, rows, _, err := c.redisView(ctx, t, p, p.Limit == 0)
	if err != nil {
		return err
	}
	types := make([]string, len(cols))
	for _, r := range rows {
		if err := fn(cols, types, r); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- editing

// redisPlan is a commit: the commands, and what WATCH must check first.
type redisPlan struct {
	watch  []string
	checks []func(ctx context.Context, tx *redis.Tx) error
	cmds   [][]string
}

func (pl *redisPlan) add(args ...string) { pl.cmds = append(pl.cmds, args) }

func (pl *redisPlan) check(f func(ctx context.Context, tx *redis.Tx) error) {
	pl.checks = append(pl.checks, f)
}

func valueOf(m map[string]Value, k string) (Value, bool) { v, ok := m[k]; return v, ok }

// redisPlanChanges turns grid edits into commands.
func (c *Conn) redisPlanChanges(ctx context.Context, cl *redis.Client, t TableRef, changes []Change) (*redisPlan, error) {
	key, typ, err := c.redisKind(ctx, cl, t)
	if err != nil {
		return nil, err
	}
	pl := &redisPlan{}
	if typ == "keys" {
		return pl, redisPlanKeys(pl, changes)
	}
	// The edits were made on the key as it was: if they don't fit its type
	// now (a string's "key" column on what has become a list), it changed.
	pk := map[string]string{"hash": "field", "list": "index", "set": "member", "zset": "member", "stream": "id"}[typ]
	if pk == "" {
		pk = "key"
	}
	cols := map[string]bool{}
	for _, c := range redisColumns(typ, nil) {
		cols[c.Name] = true
	}
	for _, ch := range changes {
		_, hasPK := ch.Key[pk]
		fits := ch.Kind == ChangeInsert || hasPK
		for name := range ch.Values {
			if !cols[name] && typ != "stream" { // a stream's fields are its own
				fits = false
			}
		}
		if !fits {
			return nil, fmt.Errorf("%s changed meanwhile (it is now a %s): reload it", key, redisTypeName(typ))
		}
	}
	pl.watch = []string{key}
	// The key must still be what it was when the plan was made.
	pl.check(func(ctx context.Context, tx *redis.Tx) error {
		if now, err := tx.Type(ctx, key).Result(); err != nil || now != typ {
			return fmt.Errorf("%s changed meanwhile (it is now a %s): reload it", key, redisTypeName(now))
		}
		return nil
	})
	switch typ {
	case "string", "ReJSON-RL":
		return pl, redisPlanString(pl, key, typ, changes)
	case "hash":
		return pl, redisPlanHash(pl, key, changes)
	case "list":
		return pl, redisPlanList(pl, key, changes)
	case "set":
		return pl, redisPlanSet(pl, key, changes)
	case "zset":
		return pl, redisPlanZSet(pl, key, changes)
	case "stream":
		return pl, redisPlanStream(pl, key, changes)
	}
	return nil, fmt.Errorf("GoTable can't change a %s key: use the query editor", typ)
}

func text(v Value, what string) (string, error) {
	if v.Null {
		return "", fmt.Errorf("%s can't be empty (NULL)", what)
	}
	return v.Text, nil
}

func ttlSeconds(v Value) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(v.Text), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("TTL is a number of seconds (1 or more), or empty for no expiry")
	}
	return n, nil
}

func redisPlanKeys(pl *redisPlan, changes []Change) error {
	for _, ch := range changes {
		switch ch.Kind {
		case ChangeDelete:
			k, err := text(ch.Key["key"], "the key")
			if err != nil {
				return err
			}
			pl.add("UNLINK", k)
		case ChangeInsert:
			k, err := text(ch.Values["key"], "the key")
			if err != nil {
				return err
			}
			if t, ok := ch.Values["type"]; ok && !t.Null && t.Text != "" && t.Text != "string" {
				return fmt.Errorf("a new key here is a string; make a %s with its command in the query editor (e.g. HSET, RPUSH)", t.Text)
			}
			v, err := text(ch.Values["value"], "the value")
			if err != nil {
				return err
			}
			pl.watch = append(pl.watch, k)
			pl.check(func(ctx context.Context, tx *redis.Tx) error {
				if n, err := tx.Exists(ctx, k).Result(); err != nil || n > 0 {
					return fmt.Errorf("%s already exists", k)
				}
				return nil
			})
			pl.add("SET", k, v)
			if ttl, ok := ch.Values["ttl"]; ok && !ttl.Null {
				n, err := ttlSeconds(ttl)
				if err != nil {
					return err
				}
				pl.add("EXPIRE", k, strconv.FormatInt(n, 10))
			}
		case ChangeUpdate:
			k, err := text(ch.Key["key"], "the key")
			if err != nil {
				return err
			}
			if t, ok := ch.Values["type"]; ok {
				_ = t
				return errors.New("a key's type can't be changed: make a new key")
			}
			pl.watch = append(pl.watch, k)
			target := k
			if nk, ok := valueOf(ch.Values, "key"); ok {
				n, err := text(nk, "the key")
				if err != nil {
					return err
				}
				if n != k {
					pl.watch = append(pl.watch, n)
					pl.check(func(ctx context.Context, tx *redis.Tx) error {
						if x, err := tx.Exists(ctx, k).Result(); err != nil || x == 0 {
							return fmt.Errorf("%s no longer exists", k)
						}
						if x, err := tx.Exists(ctx, n).Result(); err != nil || x > 0 {
							return fmt.Errorf("%s already exists: rename to another name", n)
						}
						return nil
					})
					pl.add("RENAME", k, n)
					target = n
				}
			}
			if v, ok := ch.Values["value"]; ok {
				s, err := text(v, "the value")
				if err != nil {
					return err
				}
				pl.check(func(ctx context.Context, tx *redis.Tx) error {
					typ, err := tx.Type(ctx, k).Result()
					if err != nil || typ == "none" {
						return fmt.Errorf("%s no longer exists", k)
					}
					if typ != "string" {
						return fmt.Errorf("%s is a %s: open it to change it", k, redisTypeName(typ))
					}
					if n, _ := tx.StrLen(ctx, k).Result(); n > redisMaxInline {
						return fmt.Errorf("%s is too long to change here: open the key", k)
					}
					return nil
				})
				pl.add("SET", target, s, "KEEPTTL")
			}
			if ttl, ok := ch.Values["ttl"]; ok {
				if ttl.Null || strings.TrimSpace(ttl.Text) == "" {
					pl.add("PERSIST", target)
				} else {
					n, err := ttlSeconds(ttl)
					if err != nil {
						return err
					}
					pl.add("EXPIRE", target, strconv.FormatInt(n, 10))
				}
			}
		}
	}
	return nil
}

func redisPlanString(pl *redisPlan, key, typ string, changes []Change) error {
	for _, ch := range changes {
		if ch.Kind != ChangeUpdate {
			return errors.New("a string key has one value: change it, or delete the key from its key list")
		}
		if _, ok := ch.Values["key"]; ok {
			return errors.New("rename the key from its key list")
		}
		v, err := text(ch.Values["value"], "the value")
		if err != nil {
			return err
		}
		if typ == "ReJSON-RL" {
			pl.add("JSON.SET", key, "$", v)
		} else {
			pl.add("SET", key, v, "KEEPTTL")
		}
	}
	return nil
}

func redisPlanHash(pl *redisPlan, key string, changes []Change) error {
	exists := func(f string, want bool) {
		pl.check(func(ctx context.Context, tx *redis.Tx) error {
			ok, err := tx.HExists(ctx, key, f).Result()
			if err != nil {
				return err
			}
			if ok != want {
				if want {
					return fmt.Errorf("field %s no longer exists", f)
				}
				return fmt.Errorf("field %s already exists", f)
			}
			return nil
		})
	}
	for _, ch := range changes {
		switch ch.Kind {
		case ChangeDelete:
			f, err := text(ch.Key["field"], "the field")
			if err != nil {
				return err
			}
			pl.add("HDEL", key, f)
		case ChangeInsert:
			f, err := text(ch.Values["field"], "the field")
			if err != nil {
				return err
			}
			v, err := text(ch.Values["value"], "the value")
			if err != nil {
				return err
			}
			exists(f, false)
			pl.add("HSET", key, f, v)
		case ChangeUpdate:
			f, err := text(ch.Key["field"], "the field")
			if err != nil {
				return err
			}
			exists(f, true)
			nf := f
			if v, ok := ch.Values["field"]; ok {
				if nf, err = text(v, "the field"); err != nil {
					return err
				}
			}
			val, hasVal := ch.Values["value"]
			if nf != f {
				exists(nf, false)
				if !hasVal { // keep the value under the new name
					return fmt.Errorf("renaming field %s: give its value too", f)
				}
				pl.add("HDEL", key, f)
			}
			if hasVal || nf != f {
				s, err := text(val, "the value")
				if err != nil {
					return err
				}
				pl.add("HSET", key, nf, s)
			}
		}
	}
	return nil
}

// A list's items are changed by index; deleted ones are first marked with a
// value nobody uses, then removed together, so indexes don't shift
// between the commands.
func redisPlanList(pl *redisPlan, key string, changes []Change) error {
	marker := fmt.Sprintf("\x00gotable-deleted-%d", time.Now().UnixNano())
	deleted := false
	var pushes [][]string
	idx := func(v Value) (int64, error) {
		n, err := strconv.ParseInt(v.Text, 10, 64)
		if err != nil || n < 0 {
			return 0, errors.New("a list item is addressed by its index")
		}
		return n, nil
	}
	inRange := func(n int64) {
		pl.check(func(ctx context.Context, tx *redis.Tx) error {
			if l, err := tx.LLen(ctx, key).Result(); err != nil || n >= l {
				return fmt.Errorf("item %d no longer exists", n)
			}
			return nil
		})
	}
	for _, ch := range changes {
		switch ch.Kind {
		case ChangeInsert:
			v, err := text(ch.Values["value"], "the value")
			if err != nil {
				return err
			}
			pushes = append(pushes, []string{"RPUSH", key, v})
		case ChangeUpdate:
			if _, ok := ch.Values["index"]; ok {
				return errors.New("an item's index can't be changed: delete it and add it again")
			}
			n, err := idx(ch.Key["index"])
			if err != nil {
				return err
			}
			v, err := text(ch.Values["value"], "the value")
			if err != nil {
				return err
			}
			inRange(n)
			pl.add("LSET", key, strconv.FormatInt(n, 10), v)
		case ChangeDelete:
			n, err := idx(ch.Key["index"])
			if err != nil {
				return err
			}
			inRange(n)
			pl.add("LSET", key, strconv.FormatInt(n, 10), marker)
			deleted = true
		}
	}
	if deleted {
		pl.add("LREM", key, "0", marker)
	}
	pl.cmds = append(pl.cmds, pushes...)
	return nil
}

func redisPlanSet(pl *redisPlan, key string, changes []Change) error {
	member := func(m string, want bool) {
		pl.check(func(ctx context.Context, tx *redis.Tx) error {
			ok, err := tx.SIsMember(ctx, key, m).Result()
			if err != nil {
				return err
			}
			if ok != want {
				if want {
					return fmt.Errorf("%s is no longer in the set", m)
				}
				return fmt.Errorf("%s is already in the set", m)
			}
			return nil
		})
	}
	for _, ch := range changes {
		switch ch.Kind {
		case ChangeDelete:
			m, err := text(ch.Key["member"], "the member")
			if err != nil {
				return err
			}
			pl.add("SREM", key, m)
		case ChangeInsert:
			m, err := text(ch.Values["member"], "the member")
			if err != nil {
				return err
			}
			member(m, false)
			pl.add("SADD", key, m)
		case ChangeUpdate:
			old, err := text(ch.Key["member"], "the member")
			if err != nil {
				return err
			}
			m, err := text(ch.Values["member"], "the member")
			if err != nil {
				return err
			}
			member(old, true)
			member(m, false)
			pl.add("SREM", key, old)
			pl.add("SADD", key, m)
		}
	}
	return nil
}

func score(v Value) (string, error) {
	s := strings.TrimSpace(v.Text)
	if _, err := strconv.ParseFloat(s, 64); err != nil || v.Null {
		return "", fmt.Errorf("a score is a number, not %q", v.Text)
	}
	return s, nil
}

func redisPlanZSet(pl *redisPlan, key string, changes []Change) error {
	member := func(m string, want bool) {
		pl.check(func(ctx context.Context, tx *redis.Tx) error {
			_, err := tx.ZScore(ctx, key, m).Result()
			if err != nil && !errors.Is(err, redis.Nil) {
				return err
			}
			if (err == nil) != want {
				if want {
					return fmt.Errorf("%s is no longer in the sorted set", m)
				}
				return fmt.Errorf("%s is already in the sorted set", m)
			}
			return nil
		})
	}
	for _, ch := range changes {
		switch ch.Kind {
		case ChangeDelete:
			m, err := text(ch.Key["member"], "the member")
			if err != nil {
				return err
			}
			pl.add("ZREM", key, m)
		case ChangeInsert:
			m, err := text(ch.Values["member"], "the member")
			if err != nil {
				return err
			}
			sc, err := score(ch.Values["score"])
			if err != nil {
				return err
			}
			member(m, false)
			pl.add("ZADD", key, sc, m)
		case ChangeUpdate:
			old, err := text(ch.Key["member"], "the member")
			if err != nil {
				return err
			}
			member(old, true)
			m := old
			if v, ok := ch.Values["member"]; ok {
				if m, err = text(v, "the member"); err != nil {
					return err
				}
			}
			sv, hasScore := ch.Values["score"]
			if m != old {
				if !hasScore {
					return fmt.Errorf("renaming %s: give its score too", old)
				}
				member(m, false)
				pl.add("ZREM", key, old)
			}
			if hasScore {
				sc, err := score(sv)
				if err != nil {
					return err
				}
				pl.add("ZADD", key, sc, m)
			}
		}
	}
	return nil
}

func redisPlanStream(pl *redisPlan, key string, changes []Change) error {
	for _, ch := range changes {
		switch ch.Kind {
		case ChangeUpdate:
			return errors.New("a stream's entries can't be changed: add a new entry")
		case ChangeDelete:
			id, err := text(ch.Key["id"], "the id")
			if err != nil {
				return err
			}
			pl.add("XDEL", key, id)
		case ChangeInsert:
			id := "*"
			if v, ok := ch.Values["id"]; ok && !v.Null && strings.TrimSpace(v.Text) != "" {
				id = strings.TrimSpace(v.Text)
			}
			args := []string{"XADD", key, id}
			for _, f := range sortedKeys(ch.Values) {
				if v := ch.Values[f]; f != "id" && !v.Null {
					args = append(args, f, v.Text)
				}
			}
			if len(args) == 3 {
				return errors.New("a stream entry needs at least one field")
			}
			pl.add(args...)
		}
	}
	return nil
}

func (c *Conn) redisPreviewChanges(ctx context.Context, t TableRef, changes []Change) ([]string, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	cl, err := c.redisFor(t.Schema)
	if err != nil {
		return nil, err
	}
	pl, err := c.redisPlanChanges(ctx, cl, t, changes)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(pl.cmds))
	for _, cmd := range pl.cmds {
		out = append(out, redisLine(cmd...))
	}
	return out, nil
}

// redisApplyChanges checks the plan under WATCH and runs it in MULTI/EXEC:
// all of it, or nothing if a watched key changed meanwhile.
func (c *Conn) redisApplyChanges(ctx context.Context, t TableRef, changes []Change) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	cl, err := c.redisFor(t.Schema)
	if err != nil {
		return err
	}
	pl, err := c.redisPlanChanges(ctx, cl, t, changes)
	if err != nil {
		return err
	}
	if len(pl.cmds) == 0 {
		return nil
	}
	run := func(tx *redis.Tx) error {
		for _, chk := range pl.checks {
			if err := chk(ctx, tx); err != nil {
				return err
			}
		}
		cmds, err := tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
			for _, args := range pl.cmds {
				a := make([]any, len(args))
				for i, s := range args {
					a[i] = s
				}
				p.Do(ctx, a...)
			}
			return nil
		})
		for i, cmd := range cmds {
			if cmd.Err() != nil && !errors.Is(cmd.Err(), redis.Nil) {
				return fmt.Errorf("%s: %w", redisLine(pl.cmds[i]...), cmd.Err())
			}
		}
		return err
	}
	err = cl.Watch(ctx, run, pl.watch...)
	if errors.Is(err, redis.TxFailedErr) {
		return errors.New("a key changed while the changes were being saved: nothing was saved; reload and try again")
	}
	return err
}

// ---------------------------------------------------------------- the query editor

// redisRefused can't run in the editor: they never return.
var redisRefused = map[string]string{
	"SUBSCRIBE": "subscriptions", "PSUBSCRIBE": "subscriptions", "SSUBSCRIBE": "subscriptions",
	"MONITOR": "MONITOR", "SYNC": "replication", "PSYNC": "replication",
}

func (c *Conn) redisExecuteIn(ctx context.Context, schema, script string, rowCap int) ([]Result, error) {
	spans := redisStatements(script)
	if len(spans) == 0 {
		return nil, fmt.Errorf("nothing to execute")
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	if schema != "" && schema != c.schema {
		if err := c.eng.useSchema(ctx, c, schema); err != nil {
			return nil, err
		}
		c.schema = schema
	}
	cl, err := c.redisFor("")
	if err != nil {
		return nil, err
	}
	// One connection for the script, so MULTI…EXEC and SELECT hold.
	conn := cl.Conn()
	defer conn.Close()
	var results []Result
	for _, sp := range spans {
		r := c.redisRun(ctx, conn, sp.SQL, rowCap)
		results = append(results, r)
		if r.Err != nil {
			break
		}
	}
	return results, nil
}

// redisRun runs one command line. Callers hold c.mu.
func (c *Conn) redisRun(ctx context.Context, conn *redis.Conn, line string, rowCap int) Result {
	r := Result{Statement: line}
	start := time.Now()
	defer func() { r.Duration = time.Since(start) }()
	args, err := splitRedisArgs(line)
	if err != nil {
		r.Err = err
		return r
	}
	if len(args) == 0 {
		return r
	}
	name := strings.ToUpper(args[0])
	if what, ok := redisRefused[name]; ok {
		r.Err = fmt.Errorf("%s don't return, so they can't run here: use redis-cli", what)
		return r
	}
	a := make([]any, len(args))
	for i, s := range args {
		a[i] = s
	}
	reply, err := conn.Do(ctx, a...).Result()
	if errors.Is(err, redis.Nil) {
		reply, err = nil, nil
	}
	if err != nil {
		r.Err = err
		return r
	}
	if name == "SELECT" && len(args) == 2 {
		if n, err := strconv.Atoi(args[1]); err == nil {
			c.schema = redisSchema(n)
		}
	}
	redisResult(args, reply, rowCap, &r)
	return r
}

func (c *Conn) redisExec(ctx context.Context, line string) (int64, error) {
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	cl, err := c.redisFor("")
	if err != nil {
		return 0, err
	}
	conn := cl.Conn()
	defer conn.Close()
	r := c.redisRun(ctx, conn, line, 1)
	return 0, r.Err
}

// ---------------------------------------------------------------- deleting

func buildRedisDrop(objs []Object) ([]string, error) {
	var out []string
	for _, o := range objs {
		if key, ok := literalKey(o.Name); ok {
			out = append(out, redisLine("UNLINK", key))
		} else {
			out = append(out, "# every key matching "+o.Name+", read with SCAN", "SCAN 0 MATCH "+quoteRedisArg(o.Name)+"  →  UNLINK …")
		}
	}
	return out, nil
}

// redisDrop deletes keys, or every key a pattern matches.
func (c *Conn) redisDrop(ctx context.Context, objs []Object) ([]string, error) {
	stmts, _ := buildRedisDrop(objs)
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	for _, o := range objs {
		cl, err := c.redisFor(o.Schema)
		if err != nil {
			return nil, err
		}
		keys := []string{}
		if key, ok := literalKey(o.Name); ok {
			keys = append(keys, key)
		} else if keys, err = scanKeys(ctx, cl, o.Name, "", math.MaxInt); err != nil {
			return nil, err
		}
		for len(keys) > 0 {
			n := min(500, len(keys))
			if err := cl.Unlink(ctx, keys[:n]...).Err(); err != nil {
				return nil, err
			}
			keys = keys[n:]
		}
	}
	return stmts, nil
}

// ---------------------------------------------------------------- export and import

// redisDump writes the keys of the objects as commands that make them again.
func (c *Conn) redisDump(ctx context.Context, w io.Writer, o DumpOptions, progress func(DumpProgress)) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	p := DumpProgress{Tables: len(o.Objects)}
	fmt.Fprintf(w, "# GoTable dump of %s, %s\n# Run it in GoTable's query editor, or: redis-cli < file\n\n",
		c.redis.server, time.Now().UTC().Format(time.RFC3339))
	done := map[string]bool{}
	for i, ob := range o.Objects {
		cl, err := c.redisFor(ob.Schema)
		if err != nil {
			return err
		}
		n, _ := redisIndex(ob.Schema)
		fmt.Fprintf(w, "SELECT %d\n", n)
		var keys []string
		if key, ok := literalKey(ob.Name); ok {
			keys = []string{key}
		} else if keys, err = scanKeys(ctx, cl, ob.Name, "", math.MaxInt); err != nil {
			return err
		}
		sort.Strings(keys)
		p.Table, p.TableIndex = ob.Name, i
		for _, k := range keys {
			if done[ob.Schema+"\x00"+k] { // * and user:* overlap
				continue
			}
			done[ob.Schema+"\x00"+k] = true
			if err := redisDumpKey(ctx, cl, w, k, o.DropFirst); err != nil {
				return err
			}
			p.Rows++
			if progress != nil && p.Rows%100 == 0 {
				progress(p)
			}
		}
	}
	if progress != nil {
		progress(p)
	}
	return nil
}

// redisDumpKey writes one key as commands, with its expiry.
func redisDumpKey(ctx context.Context, cl *redis.Client, w io.Writer, key string, dropFirst bool) error {
	typ, err := cl.Type(ctx, key).Result()
	if err != nil || typ == "none" {
		return err
	}
	line := func(args ...string) { fmt.Fprintln(w, redisLine(args...)) }
	if dropFirst {
		line("DEL", key)
	}
	batch := func(cmd string, items []string, per int) {
		for len(items) > 0 {
			n := min(per, len(items))
			line(append([]string{cmd, key}, items[:n]...)...)
			items = items[n:]
		}
	}
	switch typ {
	case "string":
		v, err := cl.Get(ctx, key).Result()
		if err != nil {
			return err
		}
		line("SET", key, v)
	case "ReJSON-RL":
		v, err := cl.Do(ctx, "JSON.GET", key).Text()
		if err != nil {
			return err
		}
		line("JSON.SET", key, "$", v)
	case "hash":
		m, err := cl.HGetAll(ctx, key).Result()
		if err != nil {
			return err
		}
		var items []string
		for _, f := range sortedStrings(m) {
			items = append(items, f, m[f])
		}
		batch("HSET", items, 200)
	case "list":
		items, err := cl.LRange(ctx, key, 0, -1).Result()
		if err != nil {
			return err
		}
		batch("RPUSH", items, 100)
	case "set":
		items, err := cl.SMembers(ctx, key).Result()
		if err != nil {
			return err
		}
		sort.Strings(items)
		batch("SADD", items, 100)
	case "zset":
		zs, err := cl.ZRangeWithScores(ctx, key, 0, -1).Result()
		if err != nil {
			return err
		}
		var items []string
		for _, z := range zs {
			items = append(items, strconv.FormatFloat(z.Score, 'f', -1, 64), fmt.Sprint(z.Member))
		}
		batch("ZADD", items, 200)
	case "stream":
		entries, err := cl.XRange(ctx, key, "-", "+").Result()
		if err != nil {
			return err
		}
		for _, e := range entries {
			args := []string{"XADD", key, e.ID}
			for _, f := range sortedAny(e.Values) {
				args = append(args, f, fmt.Sprint(e.Values[f]))
			}
			line(args...)
		}
	default:
		fmt.Fprintf(w, "# %s: a %s key, which GoTable doesn't write out\n", quoteRedisArg(key), typ)
		return nil
	}
	if ms, err := cl.PTTL(ctx, key).Result(); err == nil && ms > 0 {
		line("PEXPIREAT", key, strconv.FormatInt(time.Now().Add(ms).UnixMilli(), 10))
	}
	return nil
}

func sortedStrings(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// redisImportScript runs a script of commands, a line each.
func (c *Conn) redisImportScript(ctx context.Context, r io.Reader, o ImportOptions, progress func(*ImportResult)) (*ImportResult, error) {
	if o.MaxErrors <= 0 {
		o.MaxErrors = 200
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	res := &ImportResult{Errors: []ImportError{}}
	if err := c.lock(ctx); err != nil {
		return res, err
	}
	defer c.mu.Unlock()
	cl, err := c.redisFor("")
	if err != nil {
		return res, err
	}
	conn := cl.Conn()
	defer conn.Close()
	text := string(data)
	for _, sp := range redisStatements(text) {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		rr := c.redisRun(ctx, conn, sp.SQL, 1)
		res.Statements++
		if rr.Err != nil {
			res.Failed++
			if len(res.Errors) < o.MaxErrors {
				res.Errors = append(res.Errors, ImportError{Line: strings.Count(text[:sp.Start], "\n") + 1, Statement: snippet(sp.SQL), Message: rr.Err.Error()})
			}
			if !o.ContinueOnError {
				return res, fmt.Errorf("line %d: %w", res.Errors[len(res.Errors)-1].Line, rr.Err)
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
