package db

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Cells are text in GoTable, while a document's fields keep their own types.
// So a field is shown as text and, when edited, read back as the type it
// already holds; a filter tries every type its text could stand for.

const mongoDateFormat = "2006-01-02T15:04:05.000Z"

// bsonType names a value's type the way MongoDB's $type does (a UUID is
// told apart from other binary data as it shows differently).
func bsonType(v any) string {
	switch x := v.(type) {
	case nil, bson.Null:
		return "null"
	case string:
		return "string"
	case int32:
		return "int"
	case int64:
		return "long"
	case float64:
		return "double"
	case bson.Decimal128:
		return "decimal"
	case bool:
		return "bool"
	case bson.DateTime, time.Time:
		return "date"
	case bson.ObjectID:
		return "objectId"
	case bson.D, bson.M, bson.Raw:
		return "object"
	case bson.A:
		return "array"
	case bson.Binary:
		if isUUID(x) {
			return "uuid"
		}
		return "binData"
	case bson.Regex:
		return "regex"
	case bson.Timestamp:
		return "timestamp"
	case bson.JavaScript, bson.CodeWithScope:
		return "javascript"
	case bson.MinKey:
		return "minKey"
	case bson.MaxKey:
		return "maxKey"
	case bson.Undefined:
		return "undefined"
	case bson.Symbol:
		return "symbol"
	case bson.DBPointer:
		return "dbPointer"
	}
	return fmt.Sprintf("%T", v)
}

func isUUID(b bson.Binary) bool {
	return (b.Subtype == bson.TypeBinaryUUID || b.Subtype == bson.TypeBinaryUUIDOld) && len(b.Data) == 16
}

func formatUUID(b []byte) string {
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func parseUUID(s string) ([]byte, error) {
	raw, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(raw) != 16 {
		return nil, fmt.Errorf("UUID(%q): needs 32 hexadecimal digits", s)
	}
	return raw, nil
}

// mongoValue renders a field as a cell.
func mongoValue(v any) Value {
	switch x := v.(type) {
	case nil, bson.Null, bson.Undefined:
		return NullValue
	case string:
		return Text(x)
	case int32:
		return Text(strconv.FormatInt(int64(x), 10))
	case int64:
		return Text(strconv.FormatInt(x, 10))
	case float64:
		return Text(formatDouble(x))
	case bson.Decimal128:
		return Text(x.String())
	case bool:
		return Text(strconv.FormatBool(x))
	case bson.DateTime:
		return Text(x.Time().UTC().Format(mongoDateFormat))
	case bson.ObjectID:
		return Text(x.Hex())
	case bson.Binary:
		if isUUID(x) {
			return Text(formatUUID(x.Data))
		}
		return Value{Text: "0x" + strings.ToUpper(hex.EncodeToString(x.Data)), Hex: true}
	}
	return Text(mongoJSON(v, false))
}

func formatDouble(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case math.IsNaN(f):
		return "NaN"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// mongoJSON writes any value as one line of Extended JSON. Canonical keeps
// every type (for dumps); relaxed reads better (for cells and previews).
func mongoJSON(v any, canonical bool) string {
	b, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: v}}, canonical, false)
	if err != nil {
		return fmt.Sprint(v)
	}
	// {"v": …}
	s := string(b)
	i := strings.IndexByte(s, ':')
	return strings.TrimSpace(strings.TrimSuffix(s[i+1:], "}"))
}

// parseMongoDate reads the dates people and the date picker write. Without
// a time zone they are UTC, as MongoDB stores them.
func parseMongoDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, f := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999Z0700", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q isn't a date: write it like 2024-03-15 or 2024-03-15T10:20:30Z", s)
}

var (
	intText   = regexp.MustCompile(`^[-+]?\d+$`)
	floatText = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)
	hexID     = regexp.MustCompile(`^[0-9a-fA-F]{24}$`)
	uuidText  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexText   = regexp.MustCompile(`^0[xX]([0-9a-fA-F]{2})*$`)
	// typedLiteral: text that spells out its type, such as ObjectId("…").
	typedLiteral = regexp.MustCompile(`^(new\s+)?(ObjectId|ObjectID|ISODate|Date|NumberLong|NumberInt|NumberDecimal|Decimal128|Double|UUID|BinData|HexData|Timestamp|MinKey|MaxKey|RegExp)\s*\(`)
	dateLike     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}([ T]\d{2}:\d{2}.*)?$`)
)

func parseNumber(text string) (any, bool) {
	t := strings.TrimSpace(text)
	if intText.MatchString(t) {
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			f, err := strconv.ParseFloat(t, 64)
			return f, err == nil
		}
		if n >= math.MinInt32 && n <= math.MaxInt32 {
			return int32(n), true
		}
		return n, true
	}
	if floatText.MatchString(t) {
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return nil, false
}

// guessMongoValue reads text where nothing says what type it should be: a
// new field, or one that holds several types.
func guessMongoValue(text string) any {
	t := strings.TrimSpace(text)
	switch t {
	case "true":
		return true
	case "false":
		return false
	}
	if n, ok := parseNumber(t); ok {
		return n
	}
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") || typedLiteral.MatchString(t) {
		if v, err := parseMongoValue(t); err == nil {
			return v
		}
	}
	return text
}

// coerceMongoValue reads a cell's text as the type the field already holds.
// It refuses text that doesn't fit, rather than turning a mistyped number
// into a string; spelling out the type, as in NumberLong("5"), still works.
func coerceMongoValue(text string, existing any, typeName string) (any, error) {
	if typeName != "string" && typedLiteral.MatchString(strings.TrimSpace(text)) {
		if v, err := parseMongoValue(strings.TrimSpace(text)); err == nil {
			return v, nil
		}
	}
	t := strings.TrimSpace(text)
	bad := func(want string) (any, error) {
		return nil, fmt.Errorf("%q isn't %s, which this field holds (to store another type, spell it out, e.g. NumberInt(5) or ISODate(\"2024-03-15\"))", text, want)
	}
	switch typeName {
	case "string":
		return text, nil
	case "int", "long":
		n, ok := parseNumber(t)
		if !ok {
			return bad("a number")
		}
		// Keep a long a long; a fraction or a larger value widens by itself.
		if i, isInt := n.(int32); isInt && typeName == "long" {
			return int64(i), nil
		}
		return n, nil
	case "double":
		if t == "Infinity" || t == "-Infinity" || t == "NaN" {
			f, _ := strconv.ParseFloat(t, 64)
			return f, nil
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return bad("a number")
		}
		return f, nil
	case "decimal":
		d, err := bson.ParseDecimal128(t)
		if err != nil {
			return bad("a decimal number")
		}
		return d, nil
	case "bool":
		switch t {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return bad("true or false")
	case "date":
		d, err := parseMongoDate(t)
		if err != nil {
			return nil, err
		}
		return bson.NewDateTimeFromTime(d), nil
	case "objectId":
		id, err := bson.ObjectIDFromHex(t)
		if err != nil {
			return bad("an ObjectId (24 hexadecimal digits)")
		}
		return id, nil
	case "uuid":
		raw, err := parseUUID(t)
		if err != nil {
			return nil, err
		}
		sub := bson.TypeBinaryUUID
		if b, ok := existing.(bson.Binary); ok {
			sub = b.Subtype
		}
		return bson.Binary{Subtype: sub, Data: raw}, nil
	case "binData":
		if !hexText.MatchString(t) {
			return bad("binary data written as 0x…")
		}
		raw, _ := hex.DecodeString(t[2:])
		var sub byte
		if b, ok := existing.(bson.Binary); ok {
			sub = b.Subtype
		}
		return bson.Binary{Subtype: sub, Data: raw}, nil
	case "object":
		v, err := parseMongoValue(t)
		if err != nil {
			return nil, fmt.Errorf("this field holds a document: %w", err)
		}
		if _, ok := v.(bson.D); !ok {
			return bad("a document")
		}
		return v, nil
	case "array":
		v, err := parseMongoValue(t)
		if err != nil {
			return nil, fmt.Errorf("this field holds an array: %w", err)
		}
		if _, ok := v.(bson.A); !ok {
			return bad("an array")
		}
		return v, nil
	case "", "null", "undefined":
		return guessMongoValue(text), nil
	}
	if strings.Contains(typeName, "|") { // several types: nothing to go by
		return guessMongoValue(text), nil
	}
	// Regular expressions, timestamps…: shown as Extended JSON.
	v, err := parseMongoValue(t)
	if err != nil {
		return nil, fmt.Errorf("this field holds a %s: %w", typeName, err)
	}
	return v, nil
}

// mongoCandidates lists every value a text could mean, the text itself first.
// MongoDB compares within a type only, so a condition can try them all.
func mongoCandidates(text string) []any {
	t := strings.TrimSpace(text)
	out := []any{text}
	if n, ok := parseNumber(t); ok {
		out = append(out, n)
	}
	switch t {
	case "true":
		out = append(out, true)
	case "false":
		out = append(out, false)
	}
	if hexID.MatchString(t) {
		if id, err := bson.ObjectIDFromHex(t); err == nil {
			out = append(out, id)
		}
	}
	if dateLike.MatchString(t) {
		if d, err := parseMongoDate(t); err == nil {
			out = append(out, bson.NewDateTimeFromTime(d))
		}
	}
	if uuidText.MatchString(t) {
		if raw, err := parseUUID(t); err == nil {
			out = append(out, bson.Binary{Subtype: bson.TypeBinaryUUID, Data: raw}, bson.Binary{Subtype: bson.TypeBinaryUUIDOld, Data: raw})
		}
	}
	if hexText.MatchString(t) {
		if raw, err := hex.DecodeString(t[2:]); err == nil {
			for _, sub := range []byte{bson.TypeBinaryGeneric, bson.TypeBinaryBinaryOld, bson.TypeBinaryFunction, bson.TypeBinaryMD5, bson.TypeBinaryEncrypted, bson.TypeBinaryUserDefined} {
				out = append(out, bson.Binary{Subtype: sub, Data: raw})
			}
		}
	}
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") || typedLiteral.MatchString(t) {
		if v, err := parseMongoValue(t); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// ---------------------------------------------------------------- filters

// mongoRegexExpr matches a pattern against a field's value as text, so
// searching finds numbers, dates and ids as well as strings.
func mongoRegexExpr(field, pattern string) bson.D {
	return bson.D{{Key: "$expr", Value: bson.D{{Key: "$regexMatch", Value: bson.D{
		{Key: "input", Value: bson.D{{Key: "$convert", Value: bson.D{
			{Key: "input", Value: mongoFieldRef(field)}, {Key: "to", Value: "string"},
			{Key: "onError", Value: ""}, {Key: "onNull", Value: ""}}}}},
		{Key: "regex", Value: pattern},
		{Key: "options", Value: "is"},
	}}}}}
}

// mongoFieldRef refers to a top-level field in an aggregation expression.
func mongoFieldRef(field string) any {
	if strings.ContainsAny(field, ".$") {
		return bson.D{{Key: "$getField", Value: bson.D{{Key: "field", Value: bson.D{{Key: "$literal", Value: field}}}, {Key: "input", Value: "$$ROOT"}}}}
	}
	return "$" + field
}

// likeToRegex translates an SQL LIKE pattern, as the filter bar's LIKE offers.
func likeToRegex(like string) string {
	var b strings.Builder
	b.WriteByte('^')
	for _, r := range like {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteByte('.')
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteByte('$')
	return b.String()
}

func mongoNot(d bson.D) bson.D {
	return bson.D{{Key: "$nor", Value: bson.A{d}}}
}

func mongoOr(alts []bson.D) bson.D {
	if len(alts) == 1 {
		return alts[0]
	}
	a := make(bson.A, len(alts))
	for i, d := range alts {
		a[i] = d
	}
	return bson.D{{Key: "$or", Value: a}}
}

// mongoCondition renders one filter of the filter bar.
func mongoCondition(f Filter, columns []string) (bson.D, error) {
	if f.Column == AnyColumn {
		if len(columns) == 0 {
			return nil, errors.New("no columns to search")
		}
		parts := make(bson.A, 0, len(columns))
		for _, c := range columns {
			p, err := mongoCondition(Filter{Column: c, Op: f.Op, Value: f.Value, Value2: f.Value2}, nil)
			if err != nil {
				return nil, err
			}
			parts = append(parts, p)
		}
		op := "$or"
		if f.Op == OpNe || f.Op == OpNotIn || f.Op == OpNotLike || f.Op == OpNotContain || f.Op == OpIsNotNull {
			op = "$and"
		}
		return bson.D{{Key: op, Value: parts}}, nil
	}
	if f.Column == "" {
		return nil, errors.New("filter column is empty")
	}
	field := func(op string, v any) bson.D { return bson.D{{Key: f.Column, Value: bson.D{{Key: op, Value: v}}}} }
	list := func(texts []string) bson.A {
		var a bson.A
		for _, t := range texts {
			for _, c := range mongoCandidates(t) {
				a = append(a, c)
			}
		}
		return a
	}
	switch f.Op {
	case OpEq:
		return field("$in", list([]string{f.Value})), nil
	case OpNe:
		return field("$nin", list([]string{f.Value})), nil
	case OpLt, OpGt, OpLe, OpGe:
		op := map[Op]string{OpLt: "$lt", OpGt: "$gt", OpLe: "$lte", OpGe: "$gte"}[f.Op]
		var alts []bson.D
		for _, c := range mongoCandidates(f.Value) {
			alts = append(alts, field(op, c))
		}
		return mongoOr(alts), nil
	case OpIsNull:
		return bson.D{{Key: f.Column, Value: nil}}, nil
	case OpIsNotNull:
		return field("$ne", nil), nil
	case OpIn, OpNotIn:
		items := splitList(f.Value)
		if len(items) == 0 {
			return nil, fmt.Errorf("%s needs at least one value", f.Op)
		}
		if f.Op == OpIn {
			return field("$in", list(items)), nil
		}
		return field("$nin", list(items)), nil
	case OpBetween:
		// Both ends must be of one type: pair up the readings they share.
		var alts []bson.D
		highs := mongoCandidates(f.Value2)
		for _, lo := range mongoCandidates(f.Value) {
			i := slices.IndexFunc(highs, func(h any) bool { return mongoSameBracket(lo, h) })
			if i >= 0 {
				alts = append(alts, bson.D{{Key: f.Column, Value: bson.D{{Key: "$gte", Value: lo}, {Key: "$lte", Value: highs[i]}}}})
			}
		}
		return mongoOr(alts), nil
	case OpLike:
		return mongoRegexExpr(f.Column, likeToRegex(f.Value)), nil
	case OpNotLike:
		return mongoNot(mongoRegexExpr(f.Column, likeToRegex(f.Value))), nil
	case OpContains:
		return mongoRegexExpr(f.Column, regexp.QuoteMeta(f.Value)), nil
	case OpNotContain:
		return mongoNot(mongoRegexExpr(f.Column, regexp.QuoteMeta(f.Value))), nil
	case OpBeginsWith:
		return mongoRegexExpr(f.Column, "^"+regexp.QuoteMeta(f.Value)), nil
	case OpEndsWith:
		return mongoRegexExpr(f.Column, regexp.QuoteMeta(f.Value)+"$"), nil
	}
	return nil, fmt.Errorf("unknown operator %q", f.Op)
}

// mongoSameBracket: MongoDB compares these two values with each other (all
// numbers compare; otherwise the type must match).
func mongoSameBracket(a, b any) bool {
	num := func(v any) bool {
		switch v.(type) {
		case int32, int64, float64:
			return true
		}
		return false
	}
	return num(a) && num(b) || bsonType(a) == bsonType(b)
}

// mongoFilter builds the query of a page.
func mongoFilter(p Page, columns []string) (bson.D, error) {
	var parts bson.A
	for _, f := range p.Filters {
		c, err := mongoCondition(f, columns)
		if err != nil {
			return nil, err
		}
		parts = append(parts, c)
	}
	if raw := strings.TrimSpace(p.RawWhere); raw != "" {
		v, err := parseMongoValue(raw)
		if err != nil {
			return nil, fmt.Errorf("the query filter: %w", err)
		}
		d, ok := v.(bson.D)
		if !ok {
			return nil, errors.New(`the query filter must be a document, e.g. { age: { $gt: 30 } }`)
		}
		parts = append(parts, d)
	}
	switch len(parts) {
	case 0:
		return bson.D{}, nil
	case 1:
		return parts[0].(bson.D), nil
	}
	return bson.D{{Key: "$and", Value: parts}}, nil
}

func mongoSort(p Page) bson.D {
	var d bson.D
	for _, s := range p.Sort {
		dir := 1
		if s.Dir == Desc {
			dir = -1
		}
		d = append(d, bson.E{Key: s.Column, Value: dir})
	}
	return d
}

// ---------------------------------------------------------------- tables

// mongoFields is the fields of some documents: first seen first, _id before
// the rest, and how often each type appears.
type mongoFields struct {
	names []string
	count map[string]int
	types map[string]map[string]int
	docs  int
}

func newMongoFields() *mongoFields {
	return &mongoFields{count: map[string]int{}, types: map[string]map[string]int{}}
}

func (f *mongoFields) add(d bson.D) {
	f.docs++
	for _, e := range d {
		f.seen(e.Key, bsonType(e.Value))
	}
}

func (f *mongoFields) seen(name, typeName string) {
	if _, ok := f.count[name]; !ok {
		f.names = append(f.names, name)
		f.types[name] = map[string]int{}
	}
	f.count[name]++
	f.types[name][typeName]++
}

// ordered returns the given names in their order, then every other field;
// without given names, _id comes first. The data view reads rows by position
// in the columns it asked for, so those never move.
func (f *mongoFields) ordered(first []string) []string {
	out := make([]string, 0, len(f.names)+len(first))
	if len(first) == 0 && slices.Contains(f.names, "_id") {
		out = append(out, "_id")
	}
	out = append(out, first...)
	for _, n := range f.names {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// typeOf is a field's type, or its types (the most frequent first) when
// documents disagree. A null says nothing about the type.
func (f *mongoFields) typeOf(name string) string {
	var types []string
	for t := range f.types[name] {
		if t != "null" && t != "undefined" {
			types = append(types, t)
		}
	}
	if len(types) == 0 {
		return ""
	}
	slices.SortFunc(types, func(a, b string) int {
		if d := f.types[name][b] - f.types[name][a]; d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	return strings.Join(types, "|")
}

// columns describes the fields as table columns.
func (f *mongoFields) columns() []Column {
	out := []Column{}
	for _, n := range f.ordered(nil) {
		c := Column{Name: n, Type: f.typeOf(n), Nullable: true}
		if n == "_id" {
			// Left out, the server generates one.
			d := "ObjectId()"
			c.PrimaryKey, c.Nullable, c.Default = true, false, &d
		} else if f.count[n] < f.docs {
			c.Comment = fmt.Sprintf("in %d of %d sampled documents", f.count[n], f.docs)
		}
		if c.Type == "" {
			c.Type = "null"
		}
		out = append(out, c)
	}
	return out
}

func mongoGet(d bson.D, name string) (any, bool) {
	for _, e := range d {
		if e.Key == name {
			return e.Value, true
		}
	}
	return nil, false
}

// mongoResultSet turns documents into rows. A field a document lacks reads
// as NULL.
func mongoResultSet(docs []bson.D, first []string) *ResultSet {
	f := newMongoFields()
	for _, d := range docs {
		f.add(d)
	}
	cols := f.ordered(first)
	rs := &ResultSet{Columns: cols, Types: make([]string, len(cols)), Rows: make([][]Value, 0, len(docs))}
	for i, c := range cols {
		rs.Types[i] = f.typeOf(c)
	}
	idx := make(map[string]int, len(cols))
	for i, c := range cols {
		idx[c] = i
	}
	for _, d := range docs {
		row := make([]Value, len(cols))
		for i := range row {
			row[i] = NullValue
		}
		for _, e := range d {
			row[idx[e.Key]] = mongoValue(e.Value)
		}
		rs.Rows = append(rs.Rows, row)
	}
	return rs
}
