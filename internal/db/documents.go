package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dyn "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Documents: MongoDB, Elasticsearch, DynamoDB and Redis values read and
// written whole, as JSON. The grid shows a document's top-level fields as
// cells; here it is one piece, so fields can be added, removed, nested and
// given other types. A save writes only what changed, and only if the
// document is still the one that was read.

// Document is one document (item, key) of a table.
type Document struct {
	// ID names the document exactly (MongoDB: _id in canonical Extended
	// JSON; Elasticsearch: _id; Redis: the key; DynamoDB: the key as JSON).
	ID string
	// Label is how it is called on screen, Meta what else there is to know.
	Label, Meta string
	JSON        string
	// ReadOnly says why it can't be edited here ("" when it can).
	ReadOnly string
}

// DocEdit is a document's new JSON, in a Change. ID and Original are the
// document as it was read ("" for a new one).
type DocEdit struct {
	ID, JSON, Original string
}

var (
	errDocChanged = errors.New("the document was changed by someone else since it was read: reload it, then make the change again")
	errNoDocs     = errors.New("this database has no documents")
)

// HasDocuments tells the engines whose tables hold JSON documents.
func (c *Conn) HasDocuments() bool {
	if c.mongo != nil || c.redis != nil || c.dyn != nil {
		return true
	}
	_, ok := c.remote.(*elastic)
	return ok
}

// FetchDocuments reads a page of documents.
func (c *Conn) FetchDocuments(ctx context.Context, t TableRef, p Page, columns []string) ([]Document, error) {
	var filter bson.D
	if c.mongo != nil {
		var err error
		if filter, err = c.mongoQuery(p, columns); err != nil {
			return nil, err
		}
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	switch {
	case c.mongo != nil:
		cur, err := c.mongoColl(t).Find(ctx, filter, mongoFindOptions(p))
		if err != nil {
			return nil, err
		}
		docs, _, err := drainMongo(ctx, cur, 0)
		if err != nil {
			return nil, err
		}
		return mongoDocuments(docs), nil
	case c.redis != nil:
		return c.redisDocuments(ctx, t, p)
	case c.dyn != nil:
		return c.dynDocuments(ctx, t, p)
	}
	if e, ok := c.remote.(*elastic); ok {
		hits, _, err := e.fetchHits(ctx, t, p)
		if err != nil {
			return nil, err
		}
		out := make([]Document, len(hits))
		for i, h := range hits {
			out[i] = Document{ID: h.ID, Label: h.ID, JSON: string(h.Raw)}
			if len(h.Raw) == 0 {
				out[i].JSON, out[i].ReadOnly = "{}", "The index doesn't keep its documents (_source is off)."
			}
		}
		return out, nil
	}
	return nil, errNoDocs
}

// decodeJSON reads one JSON value, numbers as written.
func decodeJSON(text string) (any, error) {
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("not JSON: there is text after the value")
	}
	return v, nil
}

// ---------------------------------------------------------------- MongoDB

func mongoDocuments(docs []bson.D) []Document {
	out := make([]Document, len(docs))
	for i, d := range docs {
		id, ok := mongoGet(d, "_id")
		out[i] = Document{JSON: mongoJSON(d, false)}
		if !ok {
			out[i].ReadOnly = "This document has no _id, so it can't be matched safely."
			continue
		}
		out[i].ID, out[i].Label = mongoJSON(id, true), mongoValue(id).Text
	}
	return out
}

// mongoPlanDoc compares a document with its new JSON: the fields that differ
// are set, those that are gone unset, and nothing else is touched. The write
// matches the old values too, so a change made meanwhile isn't lost.
func (c *Conn) mongoPlanDoc(ctx context.Context, coll *mongo.Collection, ch Change) (mongoOp, error) {
	op := mongoOp{kind: ch.Kind}
	if ch.Kind == ChangeInsert {
		doc, err := mongoParseDoc(ch.Doc.JSON)
		op.doc = doc
		return op, err
	}
	id, err := decodeExtJSON(ch.Doc.ID)
	if err != nil || ch.Doc.ID == "" {
		return op, errors.New("this document has no _id, so it can't be matched safely")
	}
	op.id = id
	cur, err := coll.Find(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$eq", Value: id}}}})
	if err != nil {
		return op, err
	}
	found, _, err := drainMongo(ctx, cur, 1)
	if err != nil {
		return op, err
	}
	if len(found) == 0 {
		return op, errMongoRowGone
	}
	old := found[0]
	if mongoJSON(old, false) != ch.Doc.Original {
		return op, errDocChanged
	}
	if ch.Kind == ChangeDelete {
		return op, nil
	}
	doc, err := mongoParseDoc(ch.Doc.JSON)
	if err != nil {
		return op, err
	}
	if newID, ok := mongoGet(doc, "_id"); !ok || mongoJSON(newID, false) != mongoJSON(id, false) {
		return op, errors.New("a document's _id can't change: add a new document and delete this one")
	}
	paths := false // a name $set would read as a path
	for i, e := range doc {
		if strings.Contains(e.Key, ".") || strings.HasPrefix(e.Key, "$") {
			paths = true
		}
		was, had := mongoGet(old, e.Key)
		if had && (e.Key == "_id" || mongoJSON(was, false) == mongoJSON(e.Value, false)) {
			doc[i].Value = was // the very value, with the type JSON doesn't show
			continue
		}
		if had {
			doc[i].Value = keepNumberTypes(e.Value, was, ch.Doc.JSON)
		}
		op.doc = append(op.doc, doc[i])
		op.guard = append(op.guard, mongoWas(e.Key, was, had))
	}
	for _, e := range old {
		if _, ok := mongoGet(doc, e.Key); !ok {
			if strings.Contains(e.Key, ".") || strings.HasPrefix(e.Key, "$") {
				paths = true
			}
			op.unset = append(op.unset, bson.E{Key: e.Key, Value: ""})
			op.guard = append(op.guard, mongoWas(e.Key, e.Value, true))
		}
	}
	if len(op.doc)+len(op.unset) == 0 {
		return op, errors.New("nothing to change")
	}
	if paths {
		op.replace, op.doc, op.unset, op.guard = doc, nil, nil, nil
	}
	return op, nil
}

func mongoWas(field string, v any, had bool) bson.E {
	if !had {
		return bson.E{Key: field, Value: bson.D{{Key: "$exists", Value: false}}}
	}
	return bson.E{Key: field, Value: bson.D{{Key: "$eq", Value: v}}}
}

func mongoParseDoc(text string) (bson.D, error) {
	v, err := parseMongoValue(strings.TrimSpace(text))
	if err != nil {
		return nil, err
	}
	d, ok := v.(bson.D)
	if !ok {
		return nil, errors.New("a document is an object: { … }")
	}
	seen := map[string]bool{}
	for _, e := range d {
		if seen[e.Key] {
			return nil, fmt.Errorf("the field %q is there twice", e.Key)
		}
		seen[e.Key] = true
	}
	return d, nil
}

// ---------------------------------------------------------------- Elasticsearch

func jsonCompact(s string) string {
	var b bytes.Buffer
	if json.Compact(&b, []byte(s)) != nil {
		return s
	}
	return b.String()
}

// esPlanDoc writes a document whole, if its version is the one read.
func (e *elastic) esPlanDoc(t TableRef, ch Change, h *esHit) (esOp, error) {
	if ch.Kind != ChangeInsert {
		if h == nil {
			return esOp{}, fmt.Errorf("%s: %w", ch.Doc.ID, errMongoRowGone)
		}
		if jsonCompact(string(h.Raw)) != jsonCompact(ch.Doc.Original) {
			return esOp{}, errDocChanged
		}
	}
	if ch.Kind == ChangeDelete {
		return esOp{preview: "DELETE /" + t.Name + "/_doc/" + h.ID, lines: []any{map[string]any{"delete": esVersion(h)}}}, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ch.Doc.JSON), &doc); err != nil || doc == nil {
		if err == nil {
			err = errors.New("a document is an object: { … }")
		}
		return esOp{}, fmt.Errorf("not a JSON document: %w", err)
	}
	raw := json.RawMessage(ch.Doc.JSON)
	if ch.Kind == ChangeInsert {
		action := map[string]any{}
		if id, ok := doc["_id"]; ok { // a field the server wouldn't take: the document's own id
			var s string
			if json.Unmarshal(id, &s) != nil || s == "" {
				return esOp{}, errors.New(`_id is a text, e.g. "_id": "1"`)
			}
			action["_id"] = s
			delete(doc, "_id")
			b, _ := json.Marshal(doc)
			raw = b
		}
		preview := "POST /" + t.Name + "/_doc " + jsonCompact(string(raw))
		if id, ok := action["_id"]; ok {
			preview = fmt.Sprintf("PUT /%s/_create/%s %s", t.Name, id, jsonCompact(string(raw)))
		}
		return esOp{preview: preview, lines: []any{map[string]any{"create": action}, raw}}, nil
	}
	if jsonCompact(ch.Doc.JSON) == jsonCompact(ch.Doc.Original) {
		return esOp{}, errors.New("nothing to change")
	}
	return esOp{preview: "PUT /" + t.Name + "/_doc/" + h.ID + " " + jsonCompact(ch.Doc.JSON),
		lines: []any{map[string]any{"index": esVersion(h)}, raw}}, nil
}

func esVersion(h *esHit) map[string]any {
	action := map[string]any{"_id": h.ID, "if_seq_no": h.SeqNo, "if_primary_term": h.Term}
	if h.Routing != "" {
		action["routing"] = h.Routing
	}
	return action
}

// ---------------------------------------------------------------- DynamoDB

// dynItemJSON writes an item, its key first.
func dynItemJSON(it dynItem, keys []string) string {
	names := make([]string, 0, len(it))
	for k := range it {
		if !contains(keys, k) {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	for _, k := range append(append([]string{}, keys...), names...) {
		v, ok := it[k]
		if !ok {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(jsonString(k))
		b.WriteByte(':')
		dynWriteJSON(&b, v)
	}
	b.WriteByte('}')
	return b.String()
}

func (c *Conn) dynDocuments(ctx context.Context, t TableRef, p Page) ([]Document, error) {
	d, err := c.dynDescribe(ctx, t.Name)
	if err != nil {
		return nil, err
	}
	items, err := c.dynReadItems(ctx, t, p)
	if err != nil {
		return nil, err
	}
	out := make([]Document, len(items))
	for i, it := range items {
		key := dynItem{}
		var label []string
		for _, k := range d.keys {
			key[k] = it[k]
			label = append(label, dynValue(it[k]).Text)
		}
		out[i] = Document{ID: dynItemJSON(key, d.keys), Label: strings.Join(label, " · "), JSON: dynItemJSON(it, d.keys)}
	}
	return out, nil
}

// dynReadItems reads a page of items as dynRead does their rows.
func (c *Conn) dynReadItems(ctx context.Context, t TableRef, p Page) ([]dynItem, error) {
	byRow := map[*Value]dynItem{}
	_, _, rows, _, err := c.dynReadInto(ctx, t, p, false, byRow)
	if err != nil {
		return nil, err
	}
	out := make([]dynItem, len(rows))
	for i, r := range rows {
		out[i] = byRow[&r[0]]
	}
	return out, nil
}

func dynParseItem(text string) (dynItem, error) {
	av, err := dynParseJSON(text)
	if err != nil {
		return nil, err
	}
	m, ok := av.(*dyn.AttributeValueMemberM)
	if !ok {
		return nil, errors.New("an item is an object: { … }")
	}
	return m.Value, nil
}

// dynPlanDoc sets the attributes that differ and removes those that are
// gone, if they still hold what was read.
func (c *Conn) dynPlanDoc(ctx context.Context, t TableRef, d *dynTable, ch Change) (dynOp, error) {
	var op dynOp
	names := map[string]string{"#pk": d.keys[0]}
	if ch.Kind == ChangeInsert {
		item, err := dynParseItem(ch.Doc.JSON)
		if err != nil {
			return op, err
		}
		for _, k := range d.keys {
			if item[k] == nil {
				return op, fmt.Errorf("the key %s is missing", k)
			}
			if got := dynType(item[k]); d.types[k] != "" && got != d.types[k] {
				return op, fmt.Errorf("the key %s is of type %s, and that is %s", k, d.types[k], got)
			}
		}
		op.insert = true
		op.item.Put = &dyn.Put{TableName: &t.Name, Item: item, ConditionExpression: aws.String("attribute_not_exists(#pk)"), ExpressionAttributeNames: names}
		op.preview = dynPut(t.Name, item) + " -- if it doesn't exist"
		return op, nil
	}
	key, err := dynParseItem(ch.Doc.ID)
	if err != nil {
		return op, err
	}
	got, err := c.dyn.cl.GetItem(ctx, &dynamodb.GetItemInput{TableName: &t.Name, Key: key, ConsistentRead: aws.Bool(true)})
	if err != nil {
		return op, dynErr(err)
	}
	if got.Item == nil {
		return op, errMongoRowGone
	}
	if dynItemJSON(got.Item, d.keys) != ch.Doc.Original {
		return op, errDocChanged
	}
	if ch.Kind == ChangeDelete {
		op.item.Delete = &dyn.Delete{TableName: &t.Name, Key: key, ConditionExpression: aws.String("attribute_exists(#pk)"), ExpressionAttributeNames: names}
		op.preview = "DELETE FROM " + dynQuote(t.Name) + dynWhere(d.keys, key)
		return op, nil
	}
	item, err := dynParseItem(ch.Doc.JSON)
	if err != nil {
		return op, err
	}
	for _, k := range d.keys {
		if item[k] == nil || dynJSON(item[k]) != dynJSON(got.Item[k]) {
			return op, fmt.Errorf("%s is part of the key, which can't change: add a new item and delete this one", k)
		}
	}
	all := map[string]bool{}
	for k := range item {
		all[k] = true
	}
	for k := range got.Item {
		all[k] = true
	}
	attrs := make([]string, 0, len(all))
	for k := range all {
		attrs = append(attrs, k)
	}
	sort.Strings(attrs)
	vals := dynItem{}
	conds := []string{"attribute_exists(#pk)"}
	var sets, removes, show []string
	for i, a := range attrs {
		was, now := got.Item[a], item[a]
		if was != nil && now != nil && dynType(was) == dynType(now) && dynJSON(was) == dynJSON(now) {
			continue
		}
		n := strconv.Itoa(i)
		names["#a"+n] = a
		if was == nil {
			conds = append(conds, "attribute_not_exists(#a"+n+")")
		} else {
			vals[":o"+n] = was
			conds = append(conds, "#a"+n+" = :o"+n)
		}
		if now == nil {
			removes = append(removes, "#a"+n)
			show = append(show, "REMOVE "+dynQuote(a))
			continue
		}
		vals[":v"+n] = now
		sets = append(sets, "#a"+n+" = :v"+n)
		show = append(show, "SET "+dynQuote(a)+" = "+dynJSON(now))
	}
	if len(show) == 0 {
		return op, errors.New("nothing to change")
	}
	var expr []string
	if len(sets) > 0 {
		expr = append(expr, "SET "+strings.Join(sets, ", "))
	}
	if len(removes) > 0 {
		expr = append(expr, "REMOVE "+strings.Join(removes, ", "))
	}
	op.item.Update = &dyn.Update{TableName: &t.Name, Key: key, UpdateExpression: aws.String(strings.Join(expr, " ")),
		ConditionExpression: aws.String(strings.Join(conds, " AND ")), ExpressionAttributeNames: names}
	if len(vals) > 0 {
		op.item.Update.ExpressionAttributeValues = vals
	}
	op.preview = "UPDATE " + dynQuote(t.Name) + " " + strings.Join(show, " ") + dynWhere(d.keys, key)
	return op, nil
}

// ---------------------------------------------------------------- Redis

const (
	redisDocItems = 500   // items of a key in a list of keys
	redisOneItems = 10000 // and of the one key that is open
)

func (c *Conn) redisDocuments(ctx context.Context, t TableRef, p Page) ([]Document, error) {
	cl, err := c.redisFor(t.Schema)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.RawWhere) != "" {
		return nil, errors.New("Redis has no WHERE: filter by the columns, or use a key pattern")
	}
	if key, ok := literalKey(t.Name); ok {
		docs, err := redisDocs(ctx, cl, []string{key}, redisOneItems, redisMaxValue)
		if err == nil && len(docs) == 0 {
			err = fmt.Errorf("the key %q doesn't exist (any more)", key)
		}
		return docs, err
	}
	_, rows, _, err := redisKeyRows(ctx, cl, t.Name, p, false)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		if !r[0].Hex {
			keys = append(keys, r[0].Text)
		}
	}
	return redisDocs(ctx, cl, keys, redisDocItems, redisMaxInline)
}

// redisDocs reads keys' values as JSON: a hash is an object, a list and a
// set are lists, a sorted set an object of scores; a string that holds a
// JSON object or list is shown as that. Keys that are gone are left out.
func redisDocs(ctx context.Context, cl redis.Cmdable, keys []string, maxItems, maxBytes int64) ([]Document, error) {
	if len(keys) == 0 {
		return []Document{}, nil
	}
	types := make([]*redis.StatusCmd, len(keys))
	ttls := make([]*redis.DurationCmd, len(keys))
	sizes := make([]*redis.IntCmd, len(keys))
	pipe := func(fn func(p redis.Pipeliner, i int, k string)) error {
		_, err := cl.Pipelined(ctx, func(p redis.Pipeliner) error {
			for i, k := range keys {
				fn(p, i, k)
			}
			return nil
		})
		var re redis.Error
		if errors.As(err, &re) { // a reply: each command has its own
			return nil
		}
		return err
	}
	if err := pipe(func(p redis.Pipeliner, i int, k string) { types[i], ttls[i] = p.Type(ctx, k), p.PTTL(ctx, k) }); err != nil {
		return nil, err
	}
	if err := pipe(func(p redis.Pipeliner, i int, k string) {
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
	}); err != nil {
		return nil, err
	}
	big := func(i int) bool {
		if sizes[i] == nil {
			return false
		}
		if types[i].Val() == "string" {
			return sizes[i].Val() > maxBytes
		}
		return sizes[i].Val() > maxItems
	}
	vals := make([]redis.Cmder, len(keys))
	if err := pipe(func(p redis.Pipeliner, i int, k string) {
		if big(i) {
			return
		}
		switch types[i].Val() {
		case "string":
			vals[i] = p.Get(ctx, k)
		case "hash":
			vals[i] = p.HGetAll(ctx, k)
		case "list":
			vals[i] = p.LRange(ctx, k, 0, -1)
		case "set":
			vals[i] = p.SMembers(ctx, k)
		case "zset":
			vals[i] = p.ZRangeWithScores(ctx, k, 0, -1)
		case "stream":
			vals[i] = p.XRange(ctx, k, "-", "+")
		case "ReJSON-RL":
			vals[i] = p.Do(ctx, "JSON.GET", k)
		}
	}); err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(keys))
	for i, k := range keys {
		typ := types[i].Val()
		if typ == "none" || typ == "" {
			continue
		}
		d := Document{ID: k, Label: k, Meta: redisTypeName(typ), JSON: "null"}
		if ms := ttls[i].Val(); ms > 0 {
			d.Meta += fmt.Sprintf(" · expires in %s", seconds(int64(math.Ceil(ms.Seconds()))))
		}
		switch {
		case big(i):
			d.JSON = jsonString(redisSummary(typ, sizes[i].Val()))
			d.ReadOnly = "Too big to show here: open the key."
		case vals[i] == nil:
			d.ReadOnly = "GoTable can't show a " + redisTypeName(typ) + " key: read it in the query editor."
		case vals[i].Err() != nil && !errors.Is(vals[i].Err(), redis.Nil):
			return nil, fmt.Errorf("%s: %w", k, vals[i].Err())
		default:
			js, ok := redisJSON(vals[i])
			d.JSON = js
			if !ok {
				d.ReadOnly = "It holds binary data, which JSON has no way to write."
			} else if typ == "stream" {
				d.ReadOnly = "A stream's entries can't change: add or delete them in the table view."
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// redisJSON writes a key's value; false if some of it isn't text.
func redisJSON(cmd redis.Cmder) (string, bool) {
	ok := true
	str := func(s string) string {
		if !utf8.ValidString(s) {
			ok = false
			return jsonString(redisText(s).Text)
		}
		return jsonString(s)
	}
	list := func(items []string) string {
		for i, s := range items {
			items[i] = str(s)
		}
		return "[" + strings.Join(items, ",") + "]"
	}
	object := func(keys []string, val func(k string) string) string {
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = str(k) + ":" + val(k)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	var js string
	switch x := cmd.(type) {
	case *redis.StringCmd:
		js = redisStringJSON(x.Val(), str)
	case *redis.MapStringStringCmd:
		m := x.Val()
		js = object(sortedStrings(m), func(k string) string { return str(m[k]) })
	case *redis.StringSliceCmd:
		items := slices.Clone(x.Val())
		if strings.EqualFold(x.Name(), "smembers") {
			sort.Strings(items)
		}
		js = list(items)
	case *redis.ZSliceCmd:
		keys, scores := []string{}, map[string]string{}
		for _, z := range x.Val() {
			m, _ := z.Member.(string)
			keys = append(keys, m)
			scores[m] = redisScoreJSON(z.Score)
		}
		js = object(keys, func(k string) string { return scores[k] })
	case *redis.XMessageSliceCmd:
		var parts []string
		for _, e := range x.Val() {
			names := sortedAny(e.Values)
			parts = append(parts, `{"id":`+jsonString(e.ID)+`,"fields":`+object(names, func(k string) string { return str(fmt.Sprint(e.Values[k])) })+"}")
		}
		js = "[" + strings.Join(parts, ",") + "]"
	case *redis.Cmd:
		js, _ = x.Text()
	}
	return js, ok
}

func redisScoreJSON(f float64) string {
	if math.IsInf(f, 0) {
		return jsonString(strings.ToLower(strings.TrimPrefix(strconv.FormatFloat(f, 'f', -1, 64), "+")))
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// redisStringJSON: a JSON object or list in a string is shown as itself.
func redisStringJSON(s string, str func(string) string) string {
	if t := strings.TrimSpace(s); t != "" && (t[0] == '{' || t[0] == '[') && json.Valid([]byte(t)) && utf8.ValidString(t) {
		return t
	}
	return str(s)
}

// redisScalar is a JSON value as Redis keeps it: text.
func redisScalar(v any, what string) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case bool:
		return strconv.FormatBool(x), nil
	case nil:
		return "", fmt.Errorf("%s can't be null", what)
	}
	return jsonAny(v), nil
}

func jsonAny(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(v)
	return strings.TrimSpace(b.String())
}

// redisTop: the JSON a string key takes: an object or list as written.
func redisTop(text string) (string, error) {
	v, err := decodeJSON(text)
	if err != nil {
		return "", err
	}
	switch v.(type) {
	case map[string]any, []any:
		return text, nil
	}
	return redisScalar(v, "the value")
}

func redisObject(v any, what string) (map[string]string, error) {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil, fmt.Errorf("%s is an object with at least one field (an empty key doesn't exist: delete it instead)", what)
	}
	out := make(map[string]string, len(m))
	for k, e := range m {
		s, err := redisScalar(e, k)
		if err != nil {
			return nil, err
		}
		out[k] = s
	}
	return out, nil
}

func redisList(v any, what string) ([]string, error) {
	l, ok := v.([]any)
	if !ok || len(l) == 0 {
		return nil, fmt.Errorf("%s is a list with at least one item (an empty key doesn't exist: delete it instead)", what)
	}
	out := make([]string, len(l))
	for i, e := range l {
		s, err := redisScalar(e, "an item")
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func redisScores(v any) (map[string]string, error) {
	m, err := redisObject(v, "a sorted set")
	for k, s := range m {
		if _, perr := strconv.ParseFloat(s, 64); perr != nil || strings.EqualFold(s, "nan") {
			return nil, fmt.Errorf("%s: %q isn't a score (a number)", k, s)
		}
	}
	return m, err
}

// redisPlanDocs turns edited documents into the commands that make the
// difference. A key keeps its expiry: it is never removed and made anew.
func (c *Conn) redisPlanDocs(ctx context.Context, cl *redis.Client, changes []Change) (*redisPlan, error) {
	pl := &redisPlan{}
	for _, ch := range changes {
		if ch.Doc == nil {
			return nil, errors.New("save the table's changes and the documents' separately")
		}
		if ch.Kind == ChangeInsert {
			if err := redisPlanNewDoc(pl, ch.Doc.JSON); err != nil {
				return nil, err
			}
			continue
		}
		key, original := ch.Doc.ID, ch.Doc.Original
		pl.watch = append(pl.watch, key)
		same := func(ctx context.Context, cl redis.Cmdable) error {
			docs, err := redisDocs(ctx, cl, []string{key}, redisOneItems, redisMaxValue)
			if err != nil {
				return err
			}
			if len(docs) == 0 {
				return fmt.Errorf("%s no longer exists", key)
			}
			if ch.Kind == ChangeUpdate && docs[0].JSON != original {
				return fmt.Errorf("%s: %w", key, errDocChanged)
			}
			if ch.Kind == ChangeUpdate && docs[0].ReadOnly != "" {
				return fmt.Errorf("%s: %s", key, docs[0].ReadOnly)
			}
			return nil
		}
		if err := same(ctx, cl); err != nil {
			return nil, err
		}
		pl.check(func(ctx context.Context, tx *redis.Tx) error { return same(ctx, tx) })
		if ch.Kind == ChangeDelete {
			pl.add("UNLINK", key)
			continue
		}
		typ, err := cl.Type(ctx, key).Result()
		if err != nil {
			return nil, err
		}
		if err := redisPlanDoc(pl, key, typ, ch.Doc); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	return pl, nil
}

func redisPlanDoc(pl *redisPlan, key, typ string, d *DocEdit) error {
	was, err := decodeJSON(d.Original)
	if err != nil {
		return err
	}
	now, err := decodeJSON(d.JSON)
	if err != nil {
		return err
	}
	before := len(pl.cmds)
	switch typ {
	case "string":
		s, err := redisTop(d.JSON)
		if err != nil {
			return err
		}
		if old, _ := redisTop(d.Original); old != s {
			pl.add("SET", key, s, "KEEPTTL")
		}
	case "ReJSON-RL":
		if jsonCompact(d.JSON) != jsonCompact(d.Original) {
			pl.add("JSON.SET", key, "$", jsonCompact(d.JSON))
		}
	case "hash":
		a, _ := redisObject(was, "a hash")
		b, err := redisObject(now, "a hash")
		if err != nil {
			return err
		}
		redisDiff(pl, key, "HSET", "HDEL", a, b, false)
	case "zset":
		a, _ := redisScores(was)
		b, err := redisScores(now)
		if err != nil {
			return err
		}
		redisDiff(pl, key, "ZADD", "ZREM", a, b, true)
	case "set":
		a, _ := redisList(was, "a set")
		b, err := redisList(now, "a set")
		if err != nil {
			return err
		}
		add, del := []string{"SADD", key}, []string{"SREM", key}
		for _, m := range b {
			if !slices.Contains(a, m) && !slices.Contains(add[2:], m) {
				add = append(add, m)
			}
		}
		for _, m := range a {
			if !slices.Contains(b, m) {
				del = append(del, m)
			}
		}
		if len(add) > 2 {
			pl.add(add...)
		}
		if len(del) > 2 {
			pl.add(del...)
		}
	case "list":
		a, _ := redisList(was, "a list")
		b, err := redisList(now, "a list")
		if err != nil {
			return err
		}
		if len(b) >= len(a) && slices.Equal(a, b[:min(len(a), len(b))]) || len(a) == len(b) {
			for i := range a {
				if a[i] != b[i] {
					pl.add("LSET", key, strconv.Itoa(i), b[i])
				}
			}
			if len(b) > len(a) {
				pl.add(append([]string{"RPUSH", key}, b[len(a):]...)...)
			}
		} else {
			// The new items after the old ones, then the old ones go: the key
			// exists throughout.
			pl.add(append([]string{"RPUSH", key}, b...)...)
			pl.add("LTRIM", key, strconv.Itoa(len(a)), "-1")
		}
	default:
		return fmt.Errorf("a %s can't be changed here", redisTypeName(typ))
	}
	if len(pl.cmds) == before {
		return errors.New("nothing to change")
	}
	return nil
}

// redisDiff adds what is new or different first, then removes: the key
// never becomes empty in between.
func redisDiff(pl *redisPlan, key, add, del string, a, b map[string]string, scoreFirst bool) {
	set, rem := []string{add, key}, []string{del, key}
	for _, k := range sortedStrings(b) {
		if v, ok := a[k]; ok && v == b[k] {
			continue
		}
		if scoreFirst {
			set = append(set, b[k], k)
		} else {
			set = append(set, k, b[k])
		}
	}
	for _, k := range sortedStrings(a) {
		if _, ok := b[k]; !ok {
			rem = append(rem, k)
		}
	}
	if len(set) > 2 {
		pl.add(set...)
	}
	if len(rem) > 2 {
		pl.add(rem...)
	}
}

// redisPlanNewDoc makes a key from {"key": …, "type": …, "ttl": …, "value": …}.
func redisPlanNewDoc(pl *redisPlan, text string) error {
	var in struct {
		Key   string          `json:"key"`
		Type  string          `json:"type"`
		TTL   json.Number     `json:"ttl"`
		Value json.RawMessage `json:"value"`
	}
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return fmt.Errorf(`a new key is written {"key": "…", "type": "hash", "ttl": null, "value": {…}}: %w`, err)
	}
	if in.Key == "" {
		return errors.New("the key is missing")
	}
	if len(in.Value) == 0 {
		return errors.New("the value is missing")
	}
	v, err := decodeJSON(string(in.Value))
	if err != nil {
		return err
	}
	key := in.Key
	pl.watch = append(pl.watch, key)
	pl.check(func(ctx context.Context, tx *redis.Tx) error {
		if n, err := tx.Exists(ctx, key).Result(); err != nil || n > 0 {
			return fmt.Errorf("%s already exists", key)
		}
		return nil
	})
	switch strings.ToLower(in.Type) {
	case "", "string":
		s, err := redisTop(string(in.Value))
		if err != nil {
			return err
		}
		pl.add("SET", key, s)
	case "json":
		pl.add("JSON.SET", key, "$", jsonCompact(string(in.Value)))
	case "hash":
		m, err := redisObject(v, "a hash's value")
		if err != nil {
			return err
		}
		redisDiff(pl, key, "HSET", "", nil, m, false)
	case "zset", "sorted set":
		m, err := redisScores(v)
		if err != nil {
			return err
		}
		redisDiff(pl, key, "ZADD", "", nil, m, true)
	case "list", "set":
		l, err := redisList(v, "a "+strings.ToLower(in.Type)+"'s value")
		if err != nil {
			return err
		}
		cmd := "RPUSH"
		if strings.ToLower(in.Type) == "set" {
			cmd = "SADD"
		}
		pl.add(append([]string{cmd, key}, l...)...)
	default:
		return fmt.Errorf("the type is string, hash, list, set, zset or json, not %q", in.Type)
	}
	if in.TTL != "" {
		n, err := ttlSeconds(Text(in.TTL.String()))
		if err != nil {
			return err
		}
		pl.add("EXPIRE", key, strconv.FormatInt(n, 10))
	}
	return nil
}

// hasDocEdits tells whether a commit edits documents rather than cells.
func hasDocEdits(changes []Change) bool {
	return slices.ContainsFunc(changes, func(c Change) bool { return c.Doc != nil })
}

// IsRedisKey: the name is one key, not a pattern of keys.
func IsRedisKey(name string) bool {
	_, ok := literalKey(name)
	return ok
}
