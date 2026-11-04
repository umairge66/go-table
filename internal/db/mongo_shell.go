package db

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// MongoDB's query language is the JavaScript of its shell. GoTable never
// evaluates JavaScript: it reads the commands' shape (db.people.find({…})…)
// and literal values only, the way they are usually written, and turns the
// values into Extended JSON for the driver to decode.

// shellCall is method(args).
type shellCall struct {
	name string
	args []any
}

// shellCmd is a parsed statement: show …, use …, db.method(…) or
// db.collection.method(…).chained(…)….
type shellCmd struct {
	show, use  string
	database   string // getSiblingDB("…"); empty for the current one
	collection string // empty for a command of the database
	calls      []shellCall
}

const mongoSyntaxHint = `MongoDB runs shell commands here, not SQL, e.g. db.people.find({ age: { $gt: 30 } }).sort({ name: 1 }).limit(10)`

var showUse = regexp.MustCompile(`^(?i:(show|use))\s+([^\s;]+)\s*;?$`)

// parseShell reads one statement.
func parseShell(stmt string) (*shellCmd, error) {
	p := &shellParser{s: stmt}
	p.space()
	if m := showUse.FindStringSubmatch(stmt[p.i:]); m != nil {
		if strings.EqualFold(m[1], "show") {
			return &shellCmd{show: strings.ToLower(m[2])}, nil
		}
		return &shellCmd{use: m[2]}, nil
	}
	if p.word() != "db" {
		return nil, errors.New(mongoSyntaxHint)
	}
	cmd := &shellCmd{}
	var path []string
	for {
		p.space()
		switch p.peek() {
		case '.':
			p.i++
			p.space()
			w := p.word()
			if w == "" {
				return nil, p.errorf("a name after the dot")
			}
			p.space()
			if p.peek() != '(' {
				path = append(path, w)
				continue
			}
			args, err := p.args()
			if err != nil {
				return nil, err
			}
			// db.getCollection("x") and db.getSiblingDB("x") only select.
			if len(path) == 0 && (w == "getCollection" || w == "getSiblingDB") {
				name, ok := firstString(args)
				if !ok {
					return nil, fmt.Errorf("%s needs a name in quotes", w)
				}
				if w == "getSiblingDB" {
					cmd.database = name
				} else {
					cmd.collection = name
				}
				continue
			}
			if len(cmd.calls) > 0 && len(path) > 0 {
				return nil, p.errorf("a method call, not ." + path[0])
			}
			if cmd.collection == "" {
				cmd.collection = strings.Join(path, ".")
			} else if len(path) > 0 {
				cmd.collection += "." + strings.Join(path, ".")
			}
			path = nil
			cmd.calls = append(cmd.calls, shellCall{w, args})
			continue
		case '[': // db["odd-name"]
			p.i++
			p.space()
			if c := p.peek(); c != '"' && c != '\'' && c != '`' {
				return nil, p.errorf("a name in quotes")
			}
			name, err := p.str()
			if err != nil {
				return nil, err
			}
			p.space()
			if p.peek() != ']' {
				return nil, p.errorf("]")
			}
			p.i++
			path = append(path, name)
			continue
		}
		break
	}
	if p.peek() == ';' {
		p.i++
		p.space()
	}
	if p.i < len(p.s) {
		return nil, p.errorf("the end of the statement")
	}
	if len(cmd.calls) == 0 {
		return nil, errors.New("this names a collection but runs nothing: add a method, e.g. .find()")
	}
	return cmd, nil
}

func firstString(args []any) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	s, ok := args[0].(string)
	return s, ok
}

// parseMongoValue reads one literal: a document, array, string, number,
// ObjectId("…"), ISODate("…")…
func parseMongoValue(text string) (any, error) {
	p := &shellParser{s: text}
	v, err := p.decoded()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.i < len(p.s) {
		return nil, p.errorf("the end of the value")
	}
	return v, nil
}

// shellParser reads the literal subset of JavaScript that shell commands use.
type shellParser struct {
	s string
	i int
}

func (p *shellParser) errorf(want string) error {
	got := "the end"
	if p.i < len(p.s) {
		got = strconv.Quote(string([]rune(p.s[p.i:])[:min(12, len([]rune(p.s[p.i:])))]))
	}
	return fmt.Errorf("expected %s at offset %d, found %s", want, p.i, got)
}

func (p *shellParser) peek() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

// space skips white space and comments.
func (p *shellParser) space() {
	for p.i < len(p.s) {
		switch c := p.s[p.i]; {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.i++
		case strings.HasPrefix(p.s[p.i:], "//"):
			if nl := strings.IndexByte(p.s[p.i:], '\n'); nl >= 0 {
				p.i += nl
			} else {
				p.i = len(p.s)
			}
		case strings.HasPrefix(p.s[p.i:], "/*"):
			if end := strings.Index(p.s[p.i+2:], "*/"); end >= 0 {
				p.i += end + 4
			} else {
				p.i = len(p.s)
			}
		default:
			return
		}
	}
}

func isWordByte(c byte, first bool) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80 || !first && c >= '0' && c <= '9'
}

func (p *shellParser) word() string {
	start := p.i
	for p.i < len(p.s) && isWordByte(p.s[p.i], p.i == start) {
		p.i++
	}
	return p.s[start:p.i]
}

// args reads (value, value, …) and decodes each value.
func (p *shellParser) args() ([]any, error) {
	p.i++ // (
	args := []any{}
	for {
		p.space()
		if p.peek() == ')' {
			p.i++
			return args, nil
		}
		v, err := p.decoded()
		if err != nil {
			return nil, err
		}
		args = append(args, v)
		p.space()
		switch p.peek() {
		case ',':
			p.i++
		case ')':
		default:
			return nil, p.errorf(", or )")
		}
	}
}

func (p *shellParser) decoded() (any, error) {
	var b strings.Builder
	if err := p.value(&b); err != nil {
		return nil, err
	}
	return decodeExtJSON(b.String())
}

// decodeExtJSON decodes one Extended JSON value of any type (the driver only
// takes documents, so it goes in one and comes out again).
func decodeExtJSON(text string) (any, error) {
	var d bson.D
	if err := bson.UnmarshalExtJSON([]byte(`{"v":`+text+`}`), false, &d); err != nil {
		return nil, err
	}
	if len(d) != 1 {
		return nil, errors.New("not a value")
	}
	return d[0].Value, nil
}

func jsonString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// value writes the literal at the cursor as Extended JSON.
func (p *shellParser) value(b *strings.Builder) error {
	p.space()
	switch c := p.peek(); {
	case c == '{':
		return p.object(b)
	case c == '[':
		return p.array(b)
	case c == '"' || c == '\'' || c == '`':
		s, err := p.str()
		if err != nil {
			return err
		}
		b.WriteString(jsonString(s))
		return nil
	case c == '/':
		return p.regex(b)
	case c == '-' || c == '+' || c == '.' || c >= '0' && c <= '9':
		return p.number(b)
	case isWordByte(c, true):
		return p.identifier(b)
	}
	return p.errorf("a value")
}

func (p *shellParser) object(b *strings.Builder) error {
	p.i++ // {
	b.WriteByte('{')
	for first := true; ; first = false {
		p.space()
		if p.peek() == '}' {
			p.i++
			b.WriteByte('}')
			return nil
		}
		var key string
		switch c := p.peek(); {
		case c == '"' || c == '\'' || c == '`':
			s, err := p.str()
			if err != nil {
				return err
			}
			key = s
		case c >= '0' && c <= '9':
			start := p.i
			for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
				p.i++
			}
			key = p.s[start:p.i]
		default:
			if key = p.word(); key == "" {
				return p.errorf("a field name")
			}
		}
		p.space()
		if p.peek() != ':' {
			return p.errorf(": after the field name")
		}
		p.i++
		if !first {
			b.WriteByte(',')
		}
		b.WriteString(jsonString(key) + ":")
		if err := p.value(b); err != nil {
			return err
		}
		p.space()
		switch p.peek() {
		case ',':
			p.i++
		case '}':
		default:
			return p.errorf(", or }")
		}
	}
}

func (p *shellParser) array(b *strings.Builder) error {
	p.i++ // [
	b.WriteByte('[')
	for first := true; ; first = false {
		p.space()
		if p.peek() == ']' {
			p.i++
			b.WriteByte(']')
			return nil
		}
		if !first {
			b.WriteByte(',')
		}
		if err := p.value(b); err != nil {
			return err
		}
		p.space()
		switch p.peek() {
		case ',':
			p.i++
		case ']':
		default:
			return p.errorf(", or ]")
		}
	}
}

// str reads a quoted string (backticks too, without interpolation).
func (p *shellParser) str() (string, error) {
	q := p.s[p.i]
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch c {
		case q:
			p.i++
			return b.String(), nil
		case '\\':
			p.i++
			if p.i >= len(p.s) {
				return "", errors.New("unterminated string")
			}
			e := p.s[p.i]
			p.i++
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'v':
				b.WriteByte('\v')
			case '0':
				b.WriteByte(0)
			case 'x', 'u':
				n := 2
				if e == 'u' {
					n = 4
				}
				if p.i+n > len(p.s) {
					return "", errors.New("unterminated string")
				}
				r, err := strconv.ParseUint(p.s[p.i:p.i+n], 16, 32)
				if err != nil {
					return "", fmt.Errorf(`bad \%c escape in a string`, e)
				}
				p.i += n
				// A surrogate pair, as JSON writes characters beyond U+FFFF.
				if r >= 0xD800 && r < 0xDC00 && strings.HasPrefix(p.s[p.i:], `\u`) && p.i+6 <= len(p.s) {
					if lo, err := strconv.ParseUint(p.s[p.i+2:p.i+6], 16, 32); err == nil && lo >= 0xDC00 && lo < 0xE000 {
						r = 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00)
						p.i += 6
					}
				}
				b.WriteRune(rune(r))
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	return "", errors.New("unterminated string")
}

// regex reads /pattern/flags.
func (p *shellParser) regex(b *strings.Builder) error {
	start := p.i + 1
	inClass := false
	for i := start; i < len(p.s); i++ {
		switch p.s[i] {
		case '\\':
			i++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '\n':
			return errors.New("unterminated regular expression")
		case '/':
			if inClass {
				continue
			}
			pattern := p.s[start:i]
			p.i = i + 1
			flags := p.word()
			writeRegex(b, pattern, flags)
			return nil
		}
	}
	return errors.New("unterminated regular expression")
}

func writeRegex(b *strings.Builder, pattern, flags string) {
	// Extended JSON wants the options sorted; "g" has no meaning to a server.
	var o []byte
	for _, f := range []byte("ilmsux") {
		if strings.IndexByte(flags, f) >= 0 {
			o = append(o, f)
		}
	}
	fmt.Fprintf(b, `{"$regularExpression":{"pattern":%s,"options":%s}}`, jsonString(pattern), jsonString(string(o)))
}

var numberLiteral = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?`)

func (p *shellParser) number(b *strings.Builder) error {
	rest := p.s[p.i:]
	if strings.HasPrefix(rest, "-Infinity") {
		p.i += len("-Infinity")
		b.WriteString(`{"$numberDouble":"-Infinity"}`)
		return nil
	}
	m := numberLiteral.FindString(rest)
	if m == "" {
		return p.errorf("a number")
	}
	p.i += len(m)
	b.WriteString(jsonNumber(m))
	return nil
}

// jsonNumber makes a JavaScript number valid JSON: no + or bare dots.
func jsonNumber(m string) string {
	m = strings.TrimPrefix(m, "+")
	neg := strings.HasPrefix(m, "-")
	m = strings.TrimPrefix(m, "-")
	if strings.HasPrefix(m, ".") {
		m = "0" + m
	}
	m = strings.Replace(m, ".e", ".0e", 1)
	m = strings.Replace(m, ".E", ".0E", 1)
	m = strings.TrimSuffix(m, ".")
	// JSON has no leading zeros, and a huge integer isn't an int64.
	if t := strings.TrimLeft(m, "0"); t != m && (t == "" || t[0] < '0' || t[0] > '9') {
		m = "0" + t
	} else {
		m = t
	}
	if !strings.ContainsAny(m, ".eE") {
		if _, err := strconv.ParseInt(m, 10, 64); err != nil {
			m += ".0"
		}
	}
	if neg {
		return "-" + m
	}
	return m
}

// callArgs reads the (…) of a constructor as raw decoded values; missing
// parentheses mean no arguments (as in `new Date`).
func (p *shellParser) callArgs() ([]any, error) {
	p.space()
	if p.peek() != '(' {
		return nil, nil
	}
	return p.args()
}

func (p *shellParser) identifier(b *strings.Builder) error {
	w := p.word()
	if w == "new" {
		p.space()
		w = p.word()
	}
	switch w {
	case "true", "false", "null":
		b.WriteString(w)
		return nil
	case "undefined":
		b.WriteString("null")
		return nil
	case "Infinity", "NaN":
		fmt.Fprintf(b, `{"$numberDouble":%q}`, w)
		return nil
	}
	args, err := p.callArgs()
	if err != nil {
		return err
	}
	text := func() string {
		if len(args) == 0 {
			return ""
		}
		switch a := args[0].(type) {
		case string:
			return a
		case int32:
			return strconv.FormatInt(int64(a), 10)
		case int64:
			return strconv.FormatInt(a, 10)
		case float64:
			return strconv.FormatFloat(a, 'f', -1, 64)
		}
		return fmt.Sprint(args[0])
	}
	switch w {
	case "ObjectId", "ObjectID":
		id := bson.NewObjectID()
		if len(args) > 0 {
			if id, err = bson.ObjectIDFromHex(text()); err != nil {
				return fmt.Errorf("ObjectId(%q): needs 24 hexadecimal digits", text())
			}
		}
		fmt.Fprintf(b, `{"$oid":%q}`, id.Hex())
	case "ISODate", "Date":
		t := time.Now()
		if len(args) > 0 {
			switch a := args[0].(type) {
			case string:
				if t, err = parseMongoDate(a); err != nil {
					return err
				}
			case int32:
				t = time.UnixMilli(int64(a))
			case int64:
				t = time.UnixMilli(a)
			case float64:
				t = time.UnixMilli(int64(a))
			default:
				return fmt.Errorf("%s takes a date in quotes or milliseconds", w)
			}
		}
		fmt.Fprintf(b, `{"$date":{"$numberLong":"%d"}}`, t.UnixMilli())
	case "NumberLong", "Long":
		if _, err := strconv.ParseInt(text(), 10, 64); err != nil {
			return fmt.Errorf("NumberLong(%q): not a whole number", text())
		}
		fmt.Fprintf(b, `{"$numberLong":%q}`, text())
	case "NumberInt", "Int32":
		if _, err := strconv.ParseInt(text(), 10, 32); err != nil {
			return fmt.Errorf("NumberInt(%q): not a 32-bit whole number", text())
		}
		fmt.Fprintf(b, `{"$numberInt":%q}`, text())
	case "Double":
		if _, err := strconv.ParseFloat(text(), 64); err != nil {
			return fmt.Errorf("Double(%q): not a number", text())
		}
		fmt.Fprintf(b, `{"$numberDouble":%q}`, text())
	case "NumberDecimal", "Decimal128":
		if _, err := bson.ParseDecimal128(text()); err != nil {
			return fmt.Errorf("NumberDecimal(%q): not a decimal", text())
		}
		fmt.Fprintf(b, `{"$numberDecimal":%q}`, text())
	case "UUID":
		raw, err := parseUUID(text())
		if err != nil {
			return err
		}
		fmt.Fprintf(b, `{"$binary":{"base64":%q,"subType":"04"}}`, base64.StdEncoding.EncodeToString(raw))
	case "BinData":
		if len(args) != 2 {
			return errors.New(`BinData takes a subtype and base64 text: BinData(0, "…")`)
		}
		sub, ok := mongoInt(args[0])
		data, isText := args[1].(string)
		if !ok || !isText || sub < 0 || sub > 255 {
			return errors.New(`BinData takes a subtype and base64 text: BinData(0, "…")`)
		}
		fmt.Fprintf(b, `{"$binary":{"base64":%q,"subType":"%02x"}}`, data, sub)
	case "HexData":
		if len(args) != 2 {
			return errors.New(`HexData takes a subtype and hexadecimal text: HexData(0, "…")`)
		}
		sub, ok := mongoInt(args[0])
		s, isText := args[1].(string)
		raw, herr := hex.DecodeString(s)
		if !ok || !isText || herr != nil || sub < 0 || sub > 255 {
			return errors.New(`HexData takes a subtype and hexadecimal text: HexData(0, "…")`)
		}
		fmt.Fprintf(b, `{"$binary":{"base64":%q,"subType":"%02x"}}`, base64.StdEncoding.EncodeToString(raw), sub)
	case "Timestamp":
		var ts, inc int64
		if len(args) == 2 {
			ts, _ = mongoInt(args[0])
			inc, _ = mongoInt(args[1])
		}
		fmt.Fprintf(b, `{"$timestamp":{"t":%d,"i":%d}}`, ts, inc)
	case "MinKey":
		b.WriteString(`{"$minKey":1}`)
	case "MaxKey":
		b.WriteString(`{"$maxKey":1}`)
	case "RegExp":
		if len(args) == 0 {
			return errors.New("RegExp needs a pattern")
		}
		flags := ""
		if len(args) > 1 {
			flags, _ = args[1].(string)
		}
		writeRegex(b, text(), flags)
	default:
		return fmt.Errorf("%s isn't a value GoTable understands: it reads literal values, not JavaScript", w)
	}
	return nil
}

func mongoInt(v any) (int64, bool) {
	switch a := v.(type) {
	case int32:
		return int64(a), true
	case int64:
		return a, true
	case float64:
		return int64(a), a == float64(int64(a))
	}
	return 0, false
}

// ---------------------------------------------------------------- statements

// mongoScanner cuts a stream of shell commands into statements. A statement
// ends at a semicolon or at the end of a line outside brackets, unless the
// next line continues it with a dot.
type mongoScanner struct {
	r    *bufio.Reader
	off  int // bytes read
	line int
}

func newMongoScanner(r io.Reader) *mongoScanner {
	return &mongoScanner{r: bufio.NewReaderSize(r, 1<<16), line: 1}
}

// mongoStmt is a statement, its first line and its offset in the source.
type mongoStmt struct {
	Text       string
	Line       int
	Start, End int
}

func (s *mongoScanner) read() (byte, error) {
	c, err := s.r.ReadByte()
	if err == nil {
		s.off++
		if c == '\n' {
			s.line++
		}
	}
	return c, err
}

// Next returns the next statement, or io.EOF. Comment-only text is skipped.
func (s *mongoScanner) Next() (mongoStmt, error) {
	for {
		st, err := s.scan()
		if strings.TrimSpace(stripJSComments(st.Text)) != "" {
			return st, nil
		}
		if err != nil {
			return st, err
		}
	}
}

func stripJSComments(t string) string {
	p := &shellParser{s: t}
	var b strings.Builder
	for {
		p.space()
		if p.i >= len(p.s) {
			return b.String()
		}
		// Only leading comments matter: the rest is a statement.
		b.WriteString(p.s[p.i:])
		return b.String()
	}
}

// scan reads up to the end of one statement; err is set at the end of input.
func (s *mongoScanner) scan() (mongoStmt, error) {
	var b strings.Builder
	st := mongoStmt{Start: s.off, Line: s.line}
	depth := 0
	var last byte // last significant byte: tells a regular expression from a division
	finish := func() mongoStmt {
		st.End = s.off
		raw := b.String()
		t := strings.TrimSpace(raw)
		lead := len(raw) - len(strings.TrimLeft(raw, " \t\r\n"))
		st.Start += lead
		st.Line += strings.Count(raw[:lead], "\n")
		st.Text = t
		return st
	}
	for {
		c, err := s.read()
		if err != nil {
			return finish(), err
		}
		switch {
		case c == '"' || c == '\'' || c == '`':
			b.WriteByte(c)
			if err := s.copyQuoted(&b, c); err != nil {
				return finish(), err
			}
			last = c
			continue
		case c == '/':
			next, _ := s.r.Peek(1)
			switch {
			case len(next) == 1 && next[0] == '/':
				b.WriteByte(c)
				for {
					c, err := s.read()
					if err != nil {
						return finish(), err
					}
					if c == '\n' {
						s.r.UnreadByte()
						s.off--
						s.line--
						break
					}
					b.WriteByte(c)
				}
				continue
			case len(next) == 1 && next[0] == '*':
				b.WriteByte(c)
				var prev byte
				for {
					c, err := s.read()
					if err != nil {
						return finish(), err
					}
					b.WriteByte(c)
					if prev == '*' && c == '/' {
						break
					}
					prev = c
				}
				continue
			case last == ':' || last == ',' || last == '(' || last == '[' || last == '{' || last == 0:
				b.WriteByte(c)
				if err := s.copyRegex(&b); err != nil {
					return finish(), err
				}
				last = '/'
				continue
			}
		case c == '{' || c == '[' || c == '(':
			depth++
		case c == '}' || c == ']' || c == ')':
			depth--
		case c == ';' && depth <= 0:
			return finish(), nil
		case c == '\n' && depth <= 0 && strings.TrimSpace(b.String()) != "":
			if last == '.' {
				break
			}
			if s.continuesWithDot(&b) {
				break
			}
			return finish(), nil
		}
		b.WriteByte(c)
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			last = c
		}
	}
}

// continuesWithDot looks past blank space for a chained .method on a later
// line, keeping what it skips.
func (s *mongoScanner) continuesWithDot(b *strings.Builder) bool {
	var skipped []byte
	for {
		next, err := s.r.Peek(1)
		if err != nil {
			return false
		}
		switch next[0] {
		case ' ', '\t', '\r', '\n':
			c, _ := s.read()
			skipped = append(skipped, c)
		case '.':
			b.WriteByte('\n')
			b.Write(skipped)
			return true
		default:
			return false
		}
	}
}

func (s *mongoScanner) copyQuoted(b *strings.Builder, q byte) error {
	for {
		c, err := s.read()
		if err != nil {
			return err
		}
		b.WriteByte(c)
		switch c {
		case '\\':
			e, err := s.read()
			if err != nil {
				return err
			}
			b.WriteByte(e)
		case q:
			return nil
		case '\n':
			if q != '`' { // an unclosed quote: don't swallow the whole file
				return nil
			}
		}
	}
}

func (s *mongoScanner) copyRegex(b *strings.Builder) error {
	inClass := false
	for {
		c, err := s.read()
		if err != nil {
			return err
		}
		b.WriteByte(c)
		switch c {
		case '\\':
			e, err := s.read()
			if err != nil {
				return err
			}
			b.WriteByte(e)
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '\n':
			return nil
		case '/':
			if !inClass {
				return nil
			}
		}
	}
}

// splitMongo splits a script into statements.
func splitMongo(script string) []Span {
	sc := newMongoScanner(strings.NewReader(script))
	var out []Span
	for {
		st, err := sc.Next()
		if err != nil {
			return out
		}
		out = append(out, Span{SQL: st.Text, Start: st.Start, End: st.End})
	}
}
