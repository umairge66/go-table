package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gotable/internal/model"
)

// mongoEngine is MongoDB. Databases play the role of schemas and collections
// of tables (as databases do on MySQL). A collection has no columns of its
// own, so they are worked out from its first documents.
type mongoEngine struct{}

// mongoSample is how many documents describe a collection's columns. A field
// missing from them still shows up once a page of rows contains it.
const mongoSample = 500

var (
	errMongoNoColumns        = errors.New("MongoDB collections have no columns to add: a field appears when a document sets it")
	errMongoNoCreateDatabase = errors.New(`MongoDB creates a database with its first collection: run use mydb, then db.createCollection("first")`)
)

// mongoURI builds the connection string. Options come from the connection;
// the user is looked up in admin unless authSource says otherwise.
func mongoURI(cfg *model.Connection, addr, password string, tunneled bool) (string, error) {
	q, err := url.ParseQuery(cfg.Options)
	if err != nil {
		return "", fmt.Errorf("options: %w", err)
	}
	if cfg.User != "" && !q.Has("authSource") {
		q.Set("authSource", "admin")
	}
	if tunneled && !q.Has("directConnection") {
		q.Set("directConnection", "true") // other members aren't behind the tunnel
	}
	u := url.URL{Scheme: "mongodb", Host: addr, Path: "/", RawQuery: q.Encode()}
	if cfg.SRV {
		u.Scheme, u.Host = "mongodb+srv", cfg.Host
	} else if strings.Contains(cfg.Host, ",") {
		u.Host = cfg.Host
	}
	if cfg.User != "" {
		u.User = url.UserPassword(cfg.User, password)
	}
	return u.String(), nil
}

func mongoTLS(cfg *model.Connection, tunneled bool) (*tls.Config, error) {
	if cfg.SSLMode == "" || cfg.SSLMode == "disable" {
		return nil, nil
	}
	t := &tls.Config{InsecureSkipVerify: cfg.SSLMode == "skip-verify"}
	if tunneled {
		t.ServerName = cfg.Host // the address is the tunnel's own
	}
	if cfg.SSLCAPath != "" {
		pem, err := os.ReadFile(cfg.SSLCAPath)
		if err != nil {
			return nil, err
		}
		t.RootCAs = x509.NewCertPool()
		if !t.RootCAs.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s has no certificates", cfg.SSLCAPath)
		}
	}
	if cfg.SSLCertPath != "" {
		key := cfg.SSLKeyPath
		if key == "" {
			key = cfg.SSLCertPath // MongoDB's usual file holds both
		}
		cert, err := tls.LoadX509KeyPair(cfg.SSLCertPath, key)
		if err != nil {
			return nil, err
		}
		t.Certificates = []tls.Certificate{cert}
	}
	return t, nil
}

func openMongo(ctx context.Context, cfg *model.Connection, addr, password string, tunnel io.Closer) (*Conn, error) {
	fail := func(err error) (*Conn, error) {
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, err
	}
	uri, err := mongoURI(cfg, addr, password, tunnel != nil)
	if err != nil {
		return fail(err)
	}
	opts := options.Client().ApplyURI(uri).SetAppName("GoTable").
		SetServerSelectionTimeout(15 * time.Second).SetConnectTimeout(15 * time.Second)
	t, err := mongoTLS(cfg, tunnel != nil)
	if err != nil {
		return fail(err)
	}
	if t != nil {
		opts.SetTLSConfig(t)
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return fail(err)
	}
	c := &Conn{Config: cfg, Dialect: DialectFor(cfg.Driver), mongo: client, eng: mongoEngine{}, tunnel: tunnel}
	var hello struct {
		SetName string `bson:"setName"`
		Msg     string `bson:"msg"`
	}
	// hello needs no login: a ping is what proves the credentials.
	err = client.Ping(ctx, nil)
	if err == nil {
		err = client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello)
	}
	if err != nil {
		client.Disconnect(context.WithoutCancel(ctx))
		return fail(err)
	}
	c.mongoTxn = hello.SetName != "" || hello.Msg == "isdbgrid"
	c.schema = cfg.DBName
	if cfg.SelectedSchema != "" {
		c.schema = cfg.SelectedSchema
	}
	if c.schema == "" {
		dbs, err := mongoDatabases(ctx, c)
		if err != nil {
			client.Disconnect(context.WithoutCancel(ctx))
			return fail(err)
		}
		c.schema = "test" // the shell's default
		if len(dbs) > 0 && !mongoSystemDB(dbs[0]) {
			c.schema = dbs[0]
		}
	}
	return c, nil
}

// closeMongo is Close for MongoDB. Callers hold c.mu.
func (c *Conn) closeMongo() error {
	if c.mongoClosed {
		return nil
	}
	c.mongoClosed = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.mongo.Disconnect(ctx)
	if c.tunnel != nil {
		err = errors.Join(err, c.tunnel.Close())
	}
	return err
}

func mongoSystemDB(name string) bool { return name == "admin" || name == "local" || name == "config" }

// mongoDatabases lists the databases the user may see, system ones last.
func mongoDatabases(ctx context.Context, c *Conn) ([]string, error) {
	names, err := c.mongo.ListDatabaseNames(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
	if err != nil {
		if c.Config.DBName != "" { // the user may only know this one
			return []string{c.Config.DBName}, nil
		}
		return nil, err
	}
	sort.SliceStable(names, func(i, j int) bool {
		si, sj := mongoSystemDB(names[i]), mongoSystemDB(names[j])
		if si != sj {
			return sj
		}
		return names[i] < names[j]
	})
	return names, nil
}

func (mongoEngine) serverVersion(ctx context.Context, c *Conn) (string, error) {
	var info struct {
		Version string `bson:"version"`
	}
	err := c.mongo.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&info)
	return "MongoDB " + info.Version, err
}

func (mongoEngine) databases(ctx context.Context, c *Conn) ([]string, error) {
	return mongoDatabases(ctx, c)
}

func (mongoEngine) schemas(ctx context.Context, c *Conn) ([]string, error) {
	names, err := mongoDatabases(ctx, c)
	if err != nil {
		return nil, err
	}
	// A database doesn't exist until it has a collection, but can be open.
	if c.schema != "" && !slices.Contains(names, c.schema) {
		names = append([]string{c.schema}, names...)
	}
	return names, nil
}

func (mongoEngine) currentSchema(_ context.Context, c *Conn) (string, error) { return c.schema, nil }

func (mongoEngine) useSchema(_ context.Context, _ *Conn, schema string) error {
	if schema == "" {
		return errors.New("choose a database")
	}
	return nil
}

func (c *Conn) mongoDB(schema string) *mongo.Database {
	if schema == "" {
		schema = c.schema
	}
	return c.mongo.Database(schema)
}

func (c *Conn) mongoColl(t TableRef) *mongo.Collection { return c.mongoDB(t.Schema).Collection(t.Name) }

func (mongoEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	if schema == "" {
		schema = c.schema
	}
	cur, err := c.mongoDB(schema).ListCollections(ctx, bson.D{}, options.ListCollections().SetNameOnly(true).SetAuthorizedCollections(true))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []Object{}
	for cur.Next(ctx) {
		var e struct {
			Name string `bson:"name"`
			Type string `bson:"type"`
		}
		if err := cur.Decode(&e); err != nil {
			return nil, err
		}
		if strings.HasPrefix(e.Name, "system.") {
			continue
		}
		kind := KindTable
		if e.Type == "view" {
			kind = KindView
		}
		out = append(out, Object{Schema: schema, Name: e.Name, Kind: kind})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, cur.Err()
}

func (mongoEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	f, err := c.mongoSampleFields(ctx, t, mongoSample)
	if err != nil {
		return nil, err
	}
	cols := f.columns()
	if f.docs == 0 { // an empty collection still has its key
		d := "ObjectId()"
		return []Column{{Name: "_id", Type: "objectId", PrimaryKey: true, Default: &d}}, nil
	}
	return cols, nil
}

func (c *Conn) mongoSampleFields(ctx context.Context, t TableRef, limit int64) (*mongoFields, error) {
	cur, err := c.mongoColl(t).Find(ctx, bson.D{}, options.Find().SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	f := newMongoFields()
	for cur.Next(ctx) {
		var d bson.D
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		f.add(d)
	}
	return f, cur.Err()
}

func (mongoEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	specs, err := c.mongoIndexes(ctx, t)
	if err != nil {
		if mongoIsView(err) { // a view has no indexes
			return []Index{}, nil
		}
		return nil, err
	}
	out := make([]Index, 0, len(specs))
	for _, spec := range specs {
		name, _ := mongoGet(spec, "name")
		x := Index{Type: "btree"}
		x.Name, _ = name.(string)
		x.Primary = x.Name == "_id_"
		if u, _ := mongoGet(spec, "unique"); u == true {
			x.Unique = true
		}
		if key, ok := mongoGet(spec, "key"); ok {
			if kd, ok := key.(bson.D); ok {
				for _, e := range kd {
					x.Columns = append(x.Columns, e.Key)
					if s, ok := e.Value.(string); ok {
						x.Type = s // text, hashed, 2dsphere…
					}
				}
			}
		}
		out = append(out, x)
	}
	return out, nil
}

func mongoIsView(err error) bool {
	var ce mongo.CommandError
	return errors.As(err, &ce) && ce.Code == 166 // CommandNotSupportedOnView
}

func (c *Conn) mongoIndexes(ctx context.Context, t TableRef) ([]bson.D, error) {
	cur, err := c.mongoColl(t).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []bson.D
	for cur.Next(ctx) {
		var d bson.D
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, cur.Err()
}

func (mongoEngine) foreignKeys(context.Context, *Conn, TableRef) ([]ForeignRef, error) {
	return nil, nil
}

func (mongoEngine) tableInfo(context.Context, *Conn, TableRef) (TableInfo, error) {
	return TableInfo{}, nil
}

// definition writes the shell commands that make the collection or view.
func (e mongoEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	stmts, err := c.mongoDefinition(ctx, o)
	if err != nil {
		return "", err
	}
	return strings.Join(stmts, "\n"), nil
}

func (c *Conn) mongoDefinition(ctx context.Context, o Object) ([]string, error) {
	t := TableRef{Schema: o.Schema, Name: o.Name}
	cur, err := c.mongoDB(o.Schema).ListCollections(ctx, bson.D{{Key: "name", Value: o.Name}})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var created bson.D
	found := false
	view := false
	for cur.Next(ctx) {
		var e struct {
			Type    string `bson:"type"`
			Options bson.D `bson:"options"`
		}
		if err := cur.Decode(&e); err != nil {
			return nil, err
		}
		created, found, view = e.Options, true, e.Type == "view"
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%s doesn't exist", o.Name)
	}
	stmt := "db.createCollection(" + jsonString(o.Name)
	if len(created) > 0 {
		stmt += ", " + mongoJSON(created, false)
	}
	stmts := []string{stmt + ");"}
	if view {
		return stmts, nil
	}
	specs, err := c.mongoIndexes(ctx, t)
	if err != nil {
		return nil, err
	}
	for _, spec := range specs {
		name, _ := mongoGet(spec, "name")
		if name == "_id_" {
			continue
		}
		key, _ := mongoGet(spec, "key")
		var rest bson.D
		for _, el := range spec {
			if el.Key != "key" && el.Key != "v" && el.Key != "ns" {
				rest = append(rest, el)
			}
		}
		stmts = append(stmts, shellColl(o.Name)+".createIndex("+mongoJSON(key, false)+", "+mongoJSON(rest, false)+");")
	}
	return stmts, nil
}

var plainName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// shellColl writes how the shell reaches a collection.
func shellColl(name string) string {
	if plainName.MatchString(name) {
		return "db." + name
	}
	return "db.getCollection(" + jsonString(name) + ")"
}

// mongoQuickFields lists a collection's fields from a few documents, for the
// editor's autocomplete: it asks for every collection when a database opens.
func (c *Conn) mongoQuickFields(ctx context.Context, t TableRef) ([]string, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	f, err := c.mongoSampleFields(ctx, t, 20)
	if err != nil {
		return nil, err
	}
	return f.ordered(nil), nil
}

// ---------------------------------------------------------------- browsing

func (c *Conn) mongoFetchRows(ctx context.Context, t TableRef, p Page, columns []string) (*ResultSet, error) {
	filter, err := mongoFilter(p, columns)
	if err != nil {
		return nil, err
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	cur, err := c.mongoColl(t).Find(ctx, filter, mongoFindOptions(p))
	if err != nil {
		return nil, err
	}
	docs, _, err := drainMongo(ctx, cur, 0)
	if err != nil {
		return nil, err
	}
	return mongoResultSet(docs, columns), nil
}

func mongoFindOptions(p Page) *options.FindOptionsBuilder {
	o := options.Find()
	if s := mongoSort(p); len(s) > 0 {
		o.SetSort(s).SetAllowDiskUse(true)
	}
	if p.Limit > 0 {
		o.SetLimit(int64(p.Limit))
	}
	if p.Offset > 0 {
		o.SetSkip(int64(p.Offset))
	}
	return o
}

// drainMongo reads a cursor's documents, up to limit when limit > 0.
func drainMongo(ctx context.Context, cur *mongo.Cursor, limit int) ([]bson.D, bool, error) {
	defer cur.Close(ctx)
	docs := []bson.D{}
	for cur.Next(ctx) {
		if limit > 0 && len(docs) >= limit {
			return docs, true, nil
		}
		var d bson.D
		if err := cur.Decode(&d); err != nil {
			return nil, false, err
		}
		docs = append(docs, d)
	}
	return docs, false, cur.Err()
}

func (c *Conn) mongoCountRows(ctx context.Context, t TableRef, p Page, columns []string) (int64, error) {
	filter, err := mongoFilter(p, columns)
	if err != nil {
		return 0, err
	}
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	coll := c.mongoColl(t)
	if len(filter) == 0 {
		// From the collection's metadata, as counting scans it. Views have none.
		if n, err := coll.EstimatedDocumentCount(ctx); err == nil {
			return n, nil
		}
	}
	return coll.CountDocuments(ctx, filter)
}

// mongoStreamRows streams a page. Rows of a table are flat, so a first pass
// finds every field: an export mustn't lose a field the sample didn't see.
func (c *Conn) mongoStreamRows(ctx context.Context, t TableRef, p Page, columns []string, fn func(cols, types []string, row []Value) error) error {
	filter, err := mongoFilter(p, columns)
	if err != nil {
		return err
	}
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	coll := c.mongoColl(t)
	cur, err := coll.Find(ctx, filter, mongoFindOptions(p))
	if err != nil {
		return err
	}
	f := newMongoFields()
	for cur.Next(ctx) {
		var d bson.D
		if err := cur.Decode(&d); err != nil {
			cur.Close(ctx)
			return err
		}
		f.add(d)
	}
	cur.Close(ctx)
	if err := cur.Err(); err != nil {
		return err
	}
	cols := f.ordered(columns)
	types := make([]string, len(cols))
	idx := make(map[string]int, len(cols))
	for i, n := range cols {
		types[i] = f.typeOf(n)
		idx[n] = i
	}
	if cur, err = coll.Find(ctx, filter, mongoFindOptions(p)); err != nil {
		return err
	}
	defer cur.Close(ctx)
	row := make([]Value, len(cols))
	for cur.Next(ctx) {
		var d bson.D
		if err := cur.Decode(&d); err != nil {
			return err
		}
		for i := range row {
			row[i] = NullValue
		}
		for _, e := range d {
			if i, ok := idx[e.Key]; ok { // a document that arrived between the passes may have more
				row[i] = mongoValue(e.Value)
			}
		}
		if err := fn(cols, types, row); err != nil {
			return err
		}
	}
	return cur.Err()
}

// ---------------------------------------------------------------- editing

// mongoOp is one write of a commit: an insert, or an update or delete of the
// document with _id = id.
type mongoOp struct {
	kind ChangeKind
	id   any
	doc  bson.D // the inserted document, or the $set of an update
}

func (o mongoOp) preview(coll string) string {
	q := shellColl(coll)
	id := mongoJSON(bson.D{{Key: "_id", Value: o.id}}, false)
	switch o.kind {
	case ChangeInsert:
		return q + ".insertOne(" + mongoJSON(o.doc, false) + ");"
	case ChangeUpdate:
		return q + ".updateOne(" + id + ", " + mongoJSON(bson.D{{Key: "$set", Value: o.doc}}, false) + ");"
	}
	return q + ".deleteOne(" + id + ");"
}

var errMongoRowGone = errors.New("row no longer exists or was changed by someone else")

// mongoPlan turns grid edits into writes. Text becomes the type each field
// holds in the document, or else the type it mostly has in the collection.
// Callers hold c.mu.
func (c *Conn) mongoPlan(ctx context.Context, t TableRef, changes []Change) ([]mongoOp, error) {
	coll := c.mongoColl(t)
	var sampled *mongoFields
	typeHint := func(field string) (string, error) {
		if sampled == nil {
			f, err := c.mongoSampleFields(ctx, t, mongoSample)
			if err != nil {
				return "", err
			}
			sampled = f
		}
		return sampled.typeOf(field), nil
	}
	ops := make([]mongoOp, 0, len(changes))
	for _, ch := range changes {
		switch ch.Kind {
		case ChangeInsert:
			var doc bson.D
			for _, k := range sortedKeys(ch.Values) {
				v := ch.Values[k]
				if v.Null { // an empty cell of a new row: no such field
					continue
				}
				hint, err := typeHint(k)
				if err != nil {
					return nil, err
				}
				val, err := coerceMongoValue(v.Text, nil, hint)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", k, err)
				}
				doc = append(doc, bson.E{Key: k, Value: val})
			}
			if doc == nil {
				doc = bson.D{}
			}
			ops = append(ops, mongoOp{kind: ChangeInsert, doc: doc})
		case ChangeUpdate, ChangeDelete:
			key, ok := ch.Key["_id"]
			if !ok {
				return nil, errors.New("this row has no _id, so it can't be matched safely")
			}
			if ch.Kind == ChangeUpdate && len(ch.Values) == 0 {
				return nil, errors.New("update without values")
			}
			existing, err := mongoFindByKey(ctx, coll, key)
			if err != nil {
				return nil, err
			}
			id, _ := mongoGet(existing, "_id")
			op := mongoOp{kind: ch.Kind, id: id}
			for _, k := range sortedKeys(ch.Values) {
				v := ch.Values[k]
				if v.Null {
					op.doc = append(op.doc, bson.E{Key: k, Value: nil})
					continue
				}
				old, present := mongoGet(existing, k)
				hint := ""
				if present {
					hint = bsonType(old)
				}
				if hint == "" || hint == "null" {
					if hint, err = typeHint(k); err != nil {
						return nil, err
					}
				}
				val, err := coerceMongoValue(v.Text, old, hint)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", k, err)
				}
				op.doc = append(op.doc, bson.E{Key: k, Value: val})
			}
			ops = append(ops, op)
		default:
			return nil, fmt.Errorf("unknown change kind %d", ch.Kind)
		}
	}
	return ops, nil
}

// mongoFindByKey finds the one document a cell's _id text stands for. Text
// can mean several values (ObjectId or string, 1 or "1").
func mongoFindByKey(ctx context.Context, coll *mongo.Collection, key Value) (bson.D, error) {
	if key.Null {
		return nil, errors.New("this row has no _id, so it can't be matched safely")
	}
	cur, err := coll.Find(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: mongoCandidates(key.Text)}}}}, options.Find().SetLimit(2))
	if err != nil {
		return nil, err
	}
	docs, _, err := drainMongo(ctx, cur, 2)
	if err != nil {
		return nil, err
	}
	switch len(docs) {
	case 0:
		return nil, errMongoRowGone
	case 1:
		return docs[0], nil
	}
	return nil, fmt.Errorf("the _id %q matches documents of different types: change them with a query", key.Text)
}

func (c *Conn) mongoPreviewChanges(ctx context.Context, t TableRef, changes []Change) ([]string, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	ops, err := c.mongoPlan(ctx, t, changes)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ops))
	for i, o := range ops {
		out[i] = o.preview(t.Name)
	}
	return out, nil
}

func (c *Conn) mongoApplyChanges(ctx context.Context, t TableRef, changes []Change) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	ops, err := c.mongoPlan(ctx, t, changes)
	if err != nil {
		return err
	}
	coll := c.mongoColl(t)
	apply := func(ctx context.Context) error {
		for i, o := range ops {
			var err error
			var matched int64 = 1
			switch o.kind {
			case ChangeInsert:
				_, err = coll.InsertOne(ctx, o.doc)
			case ChangeUpdate:
				var r *mongo.UpdateResult
				if r, err = coll.UpdateOne(ctx, bson.D{{Key: "_id", Value: o.id}}, bson.D{{Key: "$set", Value: o.doc}}); err == nil {
					matched = r.MatchedCount
				}
			case ChangeDelete:
				var r *mongo.DeleteResult
				if r, err = coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: o.id}}); err == nil {
					matched = r.DeletedCount
				}
			}
			if err == nil && matched == 0 {
				err = errMongoRowGone
			}
			if err != nil {
				if !c.mongoTxn && i > 0 {
					return fmt.Errorf("%s: %w (the %d change(s) before it were applied: a standalone MongoDB server has no transactions)", o.preview(t.Name), err, i)
				}
				return fmt.Errorf("%s: %w", o.preview(t.Name), err)
			}
		}
		return nil
	}
	if !c.mongoTxn || len(ops) < 2 {
		return apply(ctx)
	}
	sess, err := c.mongo.StartSession()
	if err != nil {
		return err
	}
	defer sess.EndSession(context.WithoutCancel(ctx))
	_, err = sess.WithTransaction(ctx, func(ctx context.Context) (any, error) { return nil, apply(ctx) })
	return err
}

// buildMongoDrop returns the commands that drop collections and views.
func buildMongoDrop(objs []Object) ([]string, error) {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		if strings.TrimSpace(o.Name) == "" {
			return nil, errors.New("a table name is empty")
		}
		out = append(out, shellColl(o.Name)+".drop();")
	}
	return out, nil
}

func (c *Conn) mongoDrop(ctx context.Context, objs []Object) ([]string, error) {
	stmts, err := buildMongoDrop(objs)
	if err != nil {
		return nil, err
	}
	schema := ""
	if len(objs) > 0 {
		schema = objs[0].Schema
	}
	return stmts, c.mongoRun(ctx, schema, stmts)
}

// buildMongoAlter translates structure changes. A field exists only in
// documents, so changing one rewrites every document that has it.
func buildMongoAlter(t TableRef, ops []AlterOp) ([]string, error) {
	var out []string
	q := shellColl(t.Name)
	for _, op := range ops {
		switch op.Kind {
		case AlterDropColumn:
			if op.Column == "_id" {
				return nil, errors.New("_id can't be removed: every document has one")
			}
			out = append(out, q+".updateMany({}, "+mongoJSON(bson.D{{Key: "$unset", Value: bson.D{{Key: op.Column, Value: ""}}}}, false)+");")
		case AlterModifyColumn:
			if op.Def == nil || op.Def.Name == "" {
				return nil, errors.New("a field needs a name")
			}
			if op.Column == "_id" || op.Def.Name == "_id" {
				return nil, errors.New("_id can't be renamed")
			}
			if op.Def.Name != op.Column {
				out = append(out, q+".updateMany({}, "+mongoJSON(bson.D{{Key: "$rename", Value: bson.D{{Key: op.Column, Value: op.Def.Name}}}}, false)+");")
			}
		case AlterAddIndex:
			if op.Index == nil || len(op.Index.Columns) == 0 {
				return nil, errors.New("an index needs at least one field")
			}
			keys := bson.D{}
			for _, col := range op.Index.Columns {
				keys = append(keys, bson.E{Key: col, Value: 1})
			}
			var opts bson.D
			if op.Index.Name != "" {
				opts = append(opts, bson.E{Key: "name", Value: op.Index.Name})
			}
			if op.Index.Unique {
				opts = append(opts, bson.E{Key: "unique", Value: true})
			}
			stmt := q + ".createIndex(" + mongoJSON(keys, false)
			if len(opts) > 0 {
				stmt += ", " + mongoJSON(opts, false)
			}
			out = append(out, stmt+");")
		case AlterDropIndex:
			if op.Name == "_id_" {
				return nil, errors.New("the _id index can't be dropped")
			}
			out = append(out, q+".dropIndex("+jsonString(op.Name)+");")
		case AlterRenameTable:
			if strings.TrimSpace(op.NewName) == "" {
				return nil, errors.New("a collection needs a name")
			}
			out = append(out, q+".renameCollection("+jsonString(strings.TrimSpace(op.NewName))+");")
		case AlterTruncate:
			out = append(out, q+".deleteMany({});")
		case AlterAddColumn:
			return nil, errMongoNoColumns
		default:
			return nil, fmt.Errorf("MongoDB has no %s", strings.ReplaceAll(string(op.Kind), "_", " "))
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- queries

func (c *Conn) mongoExecuteIn(ctx context.Context, schema, script string, rowCap int) ([]Result, error) {
	spans := splitMongo(script)
	if len(spans) == 0 {
		return nil, errors.New("nothing to execute")
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	if schema != "" {
		c.schema = schema
	}
	var results []Result
	for _, sp := range spans {
		r := c.execMongo(ctx, sp.SQL, rowCap)
		results = append(results, r)
		if r.Err != nil {
			break
		}
	}
	return results, nil
}

// mongoRun runs commands in a database, stopping at the first error.
func (c *Conn) mongoRun(ctx context.Context, schema string, stmts []string) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	saved := c.schema
	defer func() { c.schema = saved }()
	if schema != "" {
		c.schema = schema
	}
	for _, s := range stmts {
		if r := c.execMongo(ctx, s, 0); r.Err != nil {
			return fmt.Errorf("%s %w", s, r.Err)
		}
	}
	return nil
}

func (c *Conn) mongoExec(ctx context.Context, stmt string) (int64, error) {
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	r := c.execMongo(ctx, stmt, 1)
	return r.RowsAffected, r.Err
}

// execMongo runs one statement. Callers hold c.mu.
func (c *Conn) execMongo(ctx context.Context, stmt string, rowCap int) Result {
	start := time.Now()
	r := Result{Statement: stmt}
	cmd, err := parseShell(stmt)
	if err == nil {
		err = c.runShell(ctx, cmd, rowCap, &r)
	}
	r.Err = err
	r.Duration = time.Since(start)
	return r
}

func docArg(args []any, i int, what string) (bson.D, error) {
	if i >= len(args) {
		return bson.D{}, nil
	}
	d, ok := args[i].(bson.D)
	if !ok {
		return nil, fmt.Errorf("%s must be a document", what)
	}
	return d, nil
}

func docRows(r *Result, docs []bson.D) { r.Set = mongoResultSet(docs, nil) }

func valueRow(r *Result, col string, v any) {
	r.Set = &ResultSet{Columns: []string{col}, Types: []string{bsonType(v)}, Rows: [][]Value{{mongoValue(v)}}}
}

// runShell runs a parsed statement. Callers hold c.mu.
func (c *Conn) runShell(ctx context.Context, cmd *shellCmd, rowCap int, r *Result) error {
	switch {
	case cmd.use != "":
		c.schema = cmd.use
		return nil
	case cmd.show != "":
		return c.runShow(ctx, cmd.show, r)
	}
	db := c.mongoDB(cmd.database)
	if cmd.collection == "" {
		return c.runDatabaseCommand(ctx, db, cmd.calls, r)
	}
	return c.runCollectionCommand(ctx, db, cmd.collection, cmd.calls, rowCap, r)
}

func (c *Conn) runShow(ctx context.Context, what string, r *Result) error {
	var names []string
	var err error
	col := "name"
	switch what {
	case "dbs", "databases":
		names, err = mongoDatabases(ctx, c)
		col = "database"
	case "collections", "tables":
		var objs []Object
		objs, err = mongoEngine{}.objects(ctx, c, c.schema)
		for _, o := range objs {
			names = append(names, o.Name)
		}
		col = "collection"
	default:
		return fmt.Errorf("show %s isn't available here: try show dbs or show collections", what)
	}
	if err != nil {
		return err
	}
	rs := &ResultSet{Columns: []string{col}, Types: []string{"string"}, Rows: [][]Value{}}
	for _, n := range names {
		rs.Rows = append(rs.Rows, []Value{Text(n)})
	}
	r.Set = rs
	return nil
}

func runCommandDoc(ctx context.Context, db *mongo.Database, command bson.D) (bson.D, error) {
	var out bson.D
	err := db.RunCommand(ctx, command).Decode(&out)
	return out, err
}

func (c *Conn) runDatabaseCommand(ctx context.Context, db *mongo.Database, calls []shellCall, r *Result) error {
	if len(calls) != 1 {
		return fmt.Errorf("db.%s() can't be chained", calls[0].name)
	}
	call := calls[0]
	run := func(command bson.D) error {
		d, err := runCommandDoc(ctx, db, command)
		if err == nil {
			docRows(r, []bson.D{d})
		}
		return err
	}
	switch call.name {
	case "runCommand", "adminCommand":
		var command bson.D
		switch a := first(call.args).(type) {
		case bson.D:
			command = a
		case string:
			command = bson.D{{Key: a, Value: 1}}
		default:
			return errors.New(call.name + " takes a command document")
		}
		if call.name == "adminCommand" {
			db = db.Client().Database("admin")
		}
		return run(command)
	case "createCollection":
		name, ok := firstString(call.args)
		if !ok {
			return errors.New("createCollection needs a name in quotes")
		}
		opts, err := docArg(call.args, 1, "the options")
		if err != nil {
			return err
		}
		_, err = runCommandDoc(ctx, db, append(bson.D{{Key: "create", Value: name}}, opts...))
		return err
	case "createView":
		if len(call.args) < 3 {
			return errors.New(`createView takes a name, a source collection and a pipeline`)
		}
		name, _ := call.args[0].(string)
		source, _ := call.args[1].(string)
		if name == "" || source == "" {
			return errors.New(`createView takes a name, a source collection and a pipeline`)
		}
		_, err := runCommandDoc(ctx, db, bson.D{{Key: "create", Value: name}, {Key: "viewOn", Value: source}, {Key: "pipeline", Value: call.args[2]}})
		return err
	case "dropDatabase":
		return db.Drop(ctx)
	case "getCollectionNames", "getCollectionInfos":
		cur, err := db.ListCollections(ctx, bson.D{})
		if err != nil {
			return err
		}
		docs, _, err := drainMongo(ctx, cur, 0)
		if err != nil {
			return err
		}
		if call.name == "getCollectionInfos" {
			docRows(r, docs)
			return nil
		}
		names := make([]bson.D, len(docs))
		for i, d := range docs {
			n, _ := mongoGet(d, "name")
			names[i] = bson.D{{Key: "name", Value: n}}
		}
		docRows(r, names)
		return nil
	case "stats":
		return run(bson.D{{Key: "dbStats", Value: 1}})
	case "serverStatus":
		return run(bson.D{{Key: "serverStatus", Value: 1}})
	case "version":
		v, err := mongoEngine{}.serverVersion(ctx, c)
		if err == nil {
			valueRow(r, "version", strings.TrimPrefix(v, "MongoDB "))
		}
		return err
	}
	return fmt.Errorf("db.%s() isn't available here. For a collection, write db.collection.method(), or db.getCollection(\"name\") for an unusual name", call.name)
}

func first(args []any) any {
	if len(args) > 0 {
		return args[0]
	}
	return nil
}

func (c *Conn) runCollectionCommand(ctx context.Context, db *mongo.Database, name string, calls []shellCall, rowCap int, r *Result) error {
	coll := db.Collection(name)
	call := calls[0]
	chain := calls[1:]
	single := func() error {
		if len(chain) > 0 {
			return fmt.Errorf("%s() can't be followed by .%s()", call.name, chain[0].name)
		}
		return nil
	}
	if call.name != "find" && call.name != "aggregate" {
		if err := single(); err != nil {
			return err
		}
	}
	switch call.name {
	case "find":
		return runFind(ctx, coll, call.args, chain, rowCap, r)
	case "findOne":
		filter, err := docArg(call.args, 0, "the filter")
		if err != nil {
			return err
		}
		projection, err := docArg(call.args, 1, "the projection")
		if err != nil {
			return err
		}
		o := options.FindOne()
		if len(projection) > 0 {
			o.SetProjection(projection)
		}
		var d bson.D
		err = coll.FindOne(ctx, filter, o).Decode(&d)
		if errors.Is(err, mongo.ErrNoDocuments) {
			r.Set = &ResultSet{Columns: []string{}, Types: []string{}, Rows: [][]Value{}}
			return nil
		}
		if err == nil {
			docRows(r, []bson.D{d})
		}
		return err
	case "aggregate":
		return runAggregate(ctx, coll, call.args, chain, rowCap, r)
	case "countDocuments", "count":
		filter, err := docArg(call.args, 0, "the filter")
		if err != nil {
			return err
		}
		n, err := coll.CountDocuments(ctx, filter)
		if err == nil {
			valueRow(r, "count", n)
		}
		return err
	case "estimatedDocumentCount":
		n, err := coll.EstimatedDocumentCount(ctx)
		if err == nil {
			valueRow(r, "count", n)
		}
		return err
	case "distinct":
		field, ok := firstString(call.args)
		if !ok {
			return errors.New("distinct needs a field name in quotes")
		}
		filter, err := docArg(call.args, 1, "the filter")
		if err != nil {
			return err
		}
		res, err := runCommandDoc(ctx, db, bson.D{{Key: "distinct", Value: name}, {Key: "key", Value: field}, {Key: "query", Value: filter}})
		if err != nil {
			return err
		}
		vals, _ := mongoGet(res, "values")
		list, _ := vals.(bson.A)
		docs := make([]bson.D, len(list))
		for i, v := range list {
			docs[i] = bson.D{{Key: field, Value: v}}
		}
		r.Set = mongoResultSet(docs, []string{field})
		return nil
	case "insertOne", "insert", "insertMany":
		many, isList := first(call.args).(bson.A)
		if call.name == "insertMany" && !isList {
			return errors.New("insertMany takes an array of documents")
		}
		if isList {
			if len(many) == 0 {
				return nil
			}
			res, err := coll.InsertMany(ctx, []any(many))
			if err != nil {
				return mongoPartial(err, res, r)
			}
			r.RowsAffected = int64(len(res.InsertedIDs))
			return nil
		}
		doc, ok := first(call.args).(bson.D)
		if !ok {
			return errors.New(call.name + " takes a document")
		}
		_, err := coll.InsertOne(ctx, doc)
		if err == nil {
			r.RowsAffected = 1
		}
		return err
	case "updateOne", "updateMany", "replaceOne":
		return runUpdate(ctx, coll, call, r)
	case "deleteOne", "deleteMany", "remove":
		filter, ok := first(call.args).(bson.D)
		if !ok {
			return errors.New(call.name + " needs a filter document; {} matches every document")
		}
		var res *mongo.DeleteResult
		var err error
		if call.name == "deleteOne" {
			res, err = coll.DeleteOne(ctx, filter)
		} else {
			res, err = coll.DeleteMany(ctx, filter)
		}
		if err == nil {
			r.RowsAffected = res.DeletedCount
		}
		return err
	case "findOneAndUpdate", "findOneAndReplace", "findOneAndDelete":
		return runFindAndModify(ctx, db, name, call, r)
	case "drop":
		return coll.Drop(ctx)
	case "createIndex":
		keys, ok := first(call.args).(bson.D)
		if !ok || len(keys) == 0 {
			return errors.New(`createIndex needs the fields to index, e.g. { name: 1 }`)
		}
		opts, err := docArg(call.args, 1, "the options")
		if err != nil {
			return err
		}
		spec := bson.D{{Key: "key", Value: keys}}
		if _, named := mongoGet(opts, "name"); !named {
			spec = append(spec, bson.E{Key: "name", Value: mongoIndexName(keys)})
		}
		spec = append(spec, opts...)
		_, err = runCommandDoc(ctx, db, bson.D{{Key: "createIndexes", Value: name}, {Key: "indexes", Value: bson.A{spec}}})
		return err
	case "dropIndex":
		if len(call.args) != 1 {
			return errors.New("dropIndex takes an index name or its fields")
		}
		_, err := runCommandDoc(ctx, db, bson.D{{Key: "dropIndexes", Value: name}, {Key: "index", Value: call.args[0]}})
		return err
	case "dropIndexes":
		_, err := runCommandDoc(ctx, db, bson.D{{Key: "dropIndexes", Value: name}, {Key: "index", Value: "*"}})
		return err
	case "getIndexes", "indexes":
		specs, err := c.mongoIndexes(ctx, TableRef{Schema: db.Name(), Name: name})
		if err == nil {
			docRows(r, specs)
		}
		return err
	case "renameCollection":
		to, ok := firstString(call.args)
		if !ok {
			return errors.New("renameCollection needs the new name in quotes")
		}
		_, err := runCommandDoc(ctx, db.Client().Database("admin"), bson.D{{Key: "renameCollection", Value: db.Name() + "." + name}, {Key: "to", Value: db.Name() + "." + to}})
		return err
	case "stats":
		d, err := runCommandDoc(ctx, db, bson.D{{Key: "collStats", Value: name}})
		if err == nil {
			docRows(r, []bson.D{d})
		}
		return err
	}
	return fmt.Errorf("%s() isn't available here. Try find, findOne, aggregate, countDocuments, distinct, insertOne, insertMany, updateOne, updateMany, replaceOne, deleteOne, deleteMany, createIndex, dropIndex, getIndexes, renameCollection, drop", call.name)
}

// mongoPartial counts what a failed insertMany did write.
func mongoPartial(err error, res *mongo.InsertManyResult, r *Result) error {
	if res != nil {
		r.RowsAffected = int64(len(res.InsertedIDs))
	}
	return err
}

func mongoIndexName(keys bson.D) string {
	parts := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		parts = append(parts, k.Key, fmt.Sprint(k.Value))
	}
	return strings.Join(parts, "_")
}

func runFind(ctx context.Context, coll *mongo.Collection, args []any, chain []shellCall, rowCap int, r *Result) error {
	filter, err := docArg(args, 0, "the filter")
	if err != nil {
		return err
	}
	projection, err := docArg(args, 1, "the projection")
	if err != nil {
		return err
	}
	o := options.Find()
	if len(projection) > 0 {
		o.SetProjection(projection)
	}
	counting := false
	for _, ch := range chain {
		a := first(ch.args)
		switch ch.name {
		case "sort":
			d, ok := a.(bson.D)
			if !ok {
				return errors.New("sort takes a document, e.g. { name: 1 }")
			}
			o.SetSort(d)
		case "limit", "skip", "batchSize":
			n, ok := mongoInt(a)
			if !ok || n < 0 {
				return fmt.Errorf("%s takes a number", ch.name)
			}
			switch ch.name {
			case "limit":
				o.SetLimit(n)
			case "skip":
				o.SetSkip(n)
			default:
				o.SetBatchSize(int32(n))
			}
		case "project", "projection":
			d, ok := a.(bson.D)
			if !ok {
				return fmt.Errorf("%s takes a document", ch.name)
			}
			o.SetProjection(d)
		case "hint":
			o.SetHint(a)
		case "allowDiskUse":
			o.SetAllowDiskUse(true)
		case "collation":
			return errors.New("collation isn't available in find() here: use aggregate with $match")
		case "count", "itcount", "size":
			counting = true
		case "toArray", "pretty":
		default:
			return fmt.Errorf(".%s() isn't available after find(): try sort, limit, skip, project, hint or count", ch.name)
		}
	}
	if counting {
		n, err := coll.CountDocuments(ctx, filter)
		if err == nil {
			valueRow(r, "count", n)
		}
		return err
	}
	cur, err := coll.Find(ctx, filter, o)
	if err != nil {
		return err
	}
	docs, truncated, err := drainMongo(ctx, cur, rowCap)
	if err != nil {
		return err
	}
	r.Truncated = truncated
	docRows(r, docs)
	return nil
}

func runAggregate(ctx context.Context, coll *mongo.Collection, args []any, chain []shellCall, rowCap int, r *Result) error {
	for _, ch := range chain {
		if ch.name != "toArray" && ch.name != "pretty" {
			return fmt.Errorf(".%s() isn't available after aggregate(): use a pipeline stage", ch.name)
		}
	}
	var pipeline bson.A
	o := options.Aggregate()
	switch a := first(args).(type) {
	case nil:
	case bson.A:
		pipeline = a
		opts, err := docArg(args, 1, "the options")
		if err != nil {
			return err
		}
		if v, _ := mongoGet(opts, "allowDiskUse"); v == true {
			o.SetAllowDiskUse(true)
		}
	default: // stages as separate arguments
		pipeline = bson.A(args)
	}
	if pipeline == nil {
		pipeline = bson.A{}
	}
	cur, err := coll.Aggregate(ctx, pipeline, o)
	if err != nil {
		return err
	}
	docs, truncated, err := drainMongo(ctx, cur, rowCap)
	if err != nil {
		return err
	}
	r.Truncated = truncated
	docRows(r, docs)
	return nil
}

func runUpdate(ctx context.Context, coll *mongo.Collection, call shellCall, r *Result) error {
	if len(call.args) < 2 {
		return fmt.Errorf("%s takes a filter and the change", call.name)
	}
	filter, ok := call.args[0].(bson.D)
	if !ok {
		return errors.New("the filter must be a document")
	}
	opts, err := docArg(call.args, 2, "the options")
	if err != nil {
		return err
	}
	upsert := false
	if v, _ := mongoGet(opts, "upsert"); v == true {
		upsert = true
	}
	var res *mongo.UpdateResult
	switch call.name {
	case "updateOne":
		res, err = coll.UpdateOne(ctx, filter, call.args[1], options.UpdateOne().SetUpsert(upsert))
	case "updateMany":
		res, err = coll.UpdateMany(ctx, filter, call.args[1], options.UpdateMany().SetUpsert(upsert))
	default:
		res, err = coll.ReplaceOne(ctx, filter, call.args[1], options.Replace().SetUpsert(upsert))
	}
	if err == nil {
		r.RowsAffected = res.ModifiedCount + res.UpsertedCount
	}
	return err
}

func runFindAndModify(ctx context.Context, db *mongo.Database, name string, call shellCall, r *Result) error {
	filter, err := docArg(call.args, 0, "the filter")
	if err != nil {
		return err
	}
	command := bson.D{{Key: "findAndModify", Value: name}, {Key: "query", Value: filter}}
	optsAt := 1
	if call.name == "findOneAndDelete" {
		command = append(command, bson.E{Key: "remove", Value: true})
	} else {
		if len(call.args) < 2 {
			return fmt.Errorf("%s takes a filter and the change", call.name)
		}
		command = append(command, bson.E{Key: "update", Value: call.args[1]})
		optsAt = 2
	}
	opts, err := docArg(call.args, optsAt, "the options")
	if err != nil {
		return err
	}
	for _, e := range opts {
		switch e.Key {
		case "returnDocument":
			command = append(command, bson.E{Key: "new", Value: e.Value == "after"})
		case "returnNewDocument":
			command = append(command, bson.E{Key: "new", Value: e.Value})
		case "projection":
			command = append(command, bson.E{Key: "fields", Value: e.Value})
		default:
			command = append(command, e) // upsert, sort, …
		}
	}
	res, err := runCommandDoc(ctx, db, command)
	if err != nil {
		return err
	}
	val, _ := mongoGet(res, "value")
	if d, ok := val.(bson.D); ok {
		docRows(r, []bson.D{d})
		return nil
	}
	r.Set = &ResultSet{Columns: []string{}, Types: []string{}, Rows: [][]Value{}}
	return nil
}

// ---------------------------------------------------------------- transfer

// mongoImportScript runs a file of shell commands, such as an export.
func (c *Conn) mongoImportScript(ctx context.Context, r io.Reader, o ImportOptions, progress func(*ImportResult)) (*ImportResult, error) {
	if o.MaxErrors <= 0 {
		o.MaxErrors = 200
	}
	res := &ImportResult{Errors: []ImportError{}}
	sc := newMongoScanner(r)
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		st, err := sc.Next()
		if errors.Is(err, io.EOF) {
			return res, nil
		}
		if err != nil {
			return res, err
		}
		n, err := c.mongoExec(ctx, st.Text)
		res.Statements++
		res.Rows += n
		if err != nil {
			res.Failed++
			if len(res.Errors) < o.MaxErrors {
				res.Errors = append(res.Errors, ImportError{Line: st.Line, Statement: snippet(st.Text), Message: err.Error()})
			}
			if !o.ContinueOnError || ctx.Err() != nil {
				return res, fmt.Errorf("line %d: %w", st.Line, err)
			}
		}
		if progress != nil {
			progress(res)
		}
	}
}

// mongoImportCSV inserts CSV records as documents. Fields take the type they
// mostly have in the collection, else what the text looks like; a NULL leaves
// the field out. Inserts aren't undone by an error: there may be no transactions.
func (c *Conn) mongoImportCSV(ctx context.Context, r io.Reader, t TableRef, o CSVImportOptions, progress func(rows int64)) (int64, error) {
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
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	coll := c.mongoColl(t)
	f, err := c.mongoSampleFields(ctx, t, mongoSample)
	if err != nil {
		return 0, err
	}
	if o.Truncate {
		if _, err := coll.DeleteMany(ctx, bson.D{}); err != nil {
			return 0, err
		}
		f = newMongoFields() // the old documents no longer say anything
	}
	types := make([]string, len(o.Columns))
	for i, col := range o.Columns {
		types[i] = f.typeOf(col)
	}
	var (
		batch    []any
		total    int64
		firstRec = true
		line     int
	)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := coll.InsertMany(ctx, batch); err != nil {
			return fmt.Errorf("rows ending at CSV line %d: %w", line, err)
		}
		total += int64(len(batch))
		batch = batch[:0]
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
		doc := make(bson.D, 0, len(o.Columns))
		for i, idx := range o.Mapping {
			if idx < 0 || idx >= len(rec) {
				continue
			}
			text := rec[idx]
			if o.NullText != "" && text == o.NullText || o.EmptyAsNull && text == "" {
				continue
			}
			v, err := coerceMongoValue(text, nil, types[i])
			if err != nil {
				return total, fmt.Errorf("CSV line %d, %s: %w", line, o.Columns[i], err)
			}
			doc = append(doc, bson.E{Key: o.Columns[i], Value: v})
		}
		batch = append(batch, doc)
		if len(batch) >= 1000 {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	return total, flush()
}

// mongoDump writes a script of shell commands in canonical Extended JSON, so
// every value keeps its exact type when the script is imported.
func (c *Conn) mongoDump(ctx context.Context, w io.Writer, o DumpOptions, progress func(DumpProgress)) error {
	if o.BatchSize <= 0 {
		o.BatchSize = 200
	}
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
	sw.printf("// GoTable export of %s (%s)\n// Collections: %d, views: %d\n\n", c.Config.Name, ver, len(tables), len(views))
	emit := func(ob Object, index int) error {
		p.Table, p.TableIndex = ob.Name, index
		if progress != nil {
			progress(p)
		}
		sw.printf("// %s\n", ob.Name)
		if o.DropFirst {
			sw.printf("%s.drop();\n", shellColl(ob.Name))
		}
		if o.Structure {
			if err := c.lock(ctx); err != nil {
				return err
			}
			stmts, err := c.mongoDefinition(ctx, ob)
			c.mu.Unlock()
			if err != nil {
				return fmt.Errorf("%s: %w", ob.Name, err)
			}
			for _, s := range stmts {
				sw.printf("%s\n", s)
			}
		}
		if o.Data && ob.Kind != KindView {
			if err := c.mongoDumpDocuments(ctx, sw, ob, o.BatchSize, &p, progress); err != nil {
				return fmt.Errorf("%s: %w", ob.Name, err)
			}
		}
		sw.printf("\n")
		return sw.err
	}
	for i, ob := range tables {
		if err := emit(ob, i); err != nil {
			return err
		}
	}
	for i, ob := range views {
		if err := emit(ob, len(tables)+i); err != nil {
			return err
		}
	}
	return sw.err
}

func (c *Conn) mongoDumpDocuments(ctx context.Context, sw *sqlWriter, ob Object, batchSize int, p *DumpProgress, progress func(DumpProgress)) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	cur, err := c.mongoColl(TableRef{Schema: ob.Schema, Name: ob.Name}).Find(ctx, bson.D{})
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	n := 0
	for cur.Next(ctx) {
		var d bson.D
		if err := cur.Decode(&d); err != nil {
			return err
		}
		if n == 0 {
			sw.printf("%s.insertMany([\n", shellColl(ob.Name))
		} else {
			sw.printf(",\n")
		}
		sw.printf("%s", mongoJSON(d, true))
		n++
		p.Rows++
		if n >= batchSize {
			sw.printf("\n]);\n")
			n = 0
			if progress != nil {
				progress(*p)
			}
		}
		if sw.err != nil {
			return sw.err
		}
	}
	if n > 0 {
		sw.printf("\n]);\n")
		if progress != nil {
			progress(*p)
		}
	}
	if err := cur.Err(); err != nil {
		return err
	}
	return sw.err
}
