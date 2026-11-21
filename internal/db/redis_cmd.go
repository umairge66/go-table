package db

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Redis commands as redis-cli takes them: one a line, words separated by
// spaces, "double quotes" with escapes (\n, \t, \xHH, \", \\) and 'single
// quotes' (only \' escaped). Lines starting with # or // are comments.

// splitRedisArgs reads one command line into its words.
func splitRedisArgs(line string) ([]string, error) {
	var args []string
	i := 0
	for {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			return args, nil
		}
		var b strings.Builder
		switch line[i] {
		case '"':
			i++
			for {
				if i >= len(line) {
					return nil, errors.New("a double quote isn't closed")
				}
				c := line[i]
				if c == '"' {
					i++
					break
				}
				if c == '\\' && i+1 < len(line) {
					i++
					switch e := line[i]; e {
					case 'n':
						b.WriteByte('\n')
					case 'r':
						b.WriteByte('\r')
					case 't':
						b.WriteByte('\t')
					case 'b':
						b.WriteByte('\b')
					case 'a':
						b.WriteByte('\a')
					case 'x':
						if i+2 < len(line) {
							if n, err := strconv.ParseUint(line[i+1:i+3], 16, 8); err == nil {
								b.WriteByte(byte(n))
								i += 3
								continue
							}
						}
						b.WriteByte('x')
					default:
						b.WriteByte(e)
					}
					i++
					continue
				}
				b.WriteByte(c)
				i++
			}
		case '\'':
			i++
			for {
				if i >= len(line) {
					return nil, errors.New("a single quote isn't closed")
				}
				c := line[i]
				if c == '\\' && i+1 < len(line) && line[i+1] == '\'' {
					b.WriteByte('\'')
					i += 2
					continue
				}
				if c == '\'' {
					i++
					break
				}
				b.WriteByte(c)
				i++
			}
		default:
			for i < len(line) && line[i] != ' ' && line[i] != '\t' {
				b.WriteByte(line[i])
				i++
			}
			args = append(args, b.String())
			continue
		}
		// A quoted word must end there.
		if i < len(line) && line[i] != ' ' && line[i] != '\t' {
			return nil, errors.New("a quoted word must be followed by a space")
		}
		args = append(args, b.String())
	}
}

// quoteRedisArg writes an argument so splitRedisArgs reads it back.
func quoteRedisArg(s string) string {
	plain := s != ""
	for i := 0; i < len(s) && plain; i++ {
		c := s[i]
		if c <= ' ' || c == '"' || c == '\'' || c == '\\' || c >= 0x7f {
			plain = false
		}
	}
	if plain {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == utf8.RuneError && size == 1, r < 0x20, r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
	return b.String()
}

// redisLine writes a command as a line.
func redisLine(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = quoteRedisArg(a)
	}
	return strings.Join(q, " ")
}

// redisStatements splits a script into its command lines, with their line
// numbers (from 1); comments and blank lines are skipped.
func redisStatements(script string) []Span {
	var out []Span
	pos := 0
	for _, line := range strings.SplitAfter(script, "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "//") {
			start := pos + strings.Index(line, t)
			out = append(out, Span{SQL: t, Start: start, End: start + len(t)})
		}
		pos += len(line)
	}
	return out
}

// ---------------------------------------------------------------- replies

// redisValue turns a reply value into a cell.
func redisValue(v any) Value {
	switch x := v.(type) {
	case nil:
		return NullValue
	case string:
		return redisText(x)
	case []byte:
		return redisText(string(x))
	case int64:
		return Text(strconv.FormatInt(x, 10))
	case float64:
		return Text(strconv.FormatFloat(x, 'f', -1, 64))
	case bool:
		return Text(strconv.FormatBool(x))
	case error:
		return Text("(error) " + x.Error())
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if c := redisValue(e); c.Null {
				parts[i] = "null"
			} else {
				parts[i] = strconv.Quote(c.Text)
			}
		}
		return Text("[" + strings.Join(parts, ", ") + "]")
	}
	return Text(fmt.Sprint(v))
}

// redisText: binary data that isn't UTF-8 shows as 0x… hex, as elsewhere.
func redisText(s string) Value {
	if utf8.ValidString(s) {
		return Text(s)
	}
	return Value{Text: fmt.Sprintf("0x%x", s), Hex: true}
}

// pairCommands answer field, value, field, value…
var pairCommands = map[string][2]string{
	"HGETALL": {"field", "value"}, "CONFIG GET": {"parameter", "value"}, "MEMORY STATS": {"name", "value"},
	"XINFO STREAM": {"name", "value"}, "XINFO CONSUMERS": {"name", "value"},
}

// redisResult shows a reply as a table: a value, a list, pairs, stream
// entries or INFO's sections.
func redisResult(args []string, reply any, rowCap int, r *Result) {
	name := strings.ToUpper(args[0])
	sub := name
	if len(args) > 1 {
		sub = name + " " + strings.ToUpper(args[1])
	}
	hasArg := func(a string) bool {
		for _, x := range args[1:] {
			if strings.EqualFold(x, a) {
				return true
			}
		}
		return false
	}
	set := &ResultSet{}
	add := func(row ...Value) bool {
		if rowCap > 0 && len(set.Rows) >= rowCap {
			r.Truncated = true
			return false
		}
		set.Rows = append(set.Rows, row)
		return true
	}
	defer func() {
		for range set.Columns {
			set.Types = append(set.Types, "")
		}
		r.Set = set
	}()
	arr, isArr := reply.([]any)
	switch {
	case name == "INFO":
		s, _ := reply.(string)
		set.Columns = []string{"section", "name", "value"}
		section := ""
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "# ") {
				section = strings.TrimPrefix(line, "# ")
				continue
			}
			if k, v, ok := strings.Cut(line, ":"); ok && !add(Text(section), Text(k), Text(v)) {
				return
			}
		}
		return
	case isArr && (name == "SCAN" || name == "HSCAN" || name == "SSCAN" || name == "ZSCAN") && len(arr) == 2:
		cursor := redisValue(arr[0]).Text
		items, _ := arr[1].([]any)
		col := map[string]string{"SCAN": "key", "HSCAN": "field", "SSCAN": "member", "ZSCAN": "member"}[name]
		set.Columns = []string{col + " (next cursor: " + cursor + ")"}
		if name == "HSCAN" || name == "ZSCAN" {
			set.Columns = append(set.Columns, map[string]string{"HSCAN": "value", "ZSCAN": "score"}[name])
			for i := 0; i+1 < len(items); i += 2 {
				if !add(redisValue(items[i]), redisValue(items[i+1])) {
					return
				}
			}
			return
		}
		for _, it := range items {
			if !add(redisValue(it)) {
				return
			}
		}
		return
	case isArr && (name == "XRANGE" || name == "XREVRANGE"):
		redisStreamRows(arr, set, add)
		return
	case isArr && len(arr)%2 == 0 && (pairCommands[sub] != [2]string{} || pairCommands[name] != [2]string{} ||
		(strings.HasPrefix(name, "Z") && hasArg("WITHSCORES")) || (name == "HRANDFIELD" && hasArg("WITHVALUES")) ||
		name == "ZPOPMIN" || name == "ZPOPMAX"):
		cols := pairCommands[sub]
		if cols == [2]string{} {
			cols = pairCommands[name]
		}
		if cols == [2]string{} {
			cols = [2]string{"member", "score"}
			if name == "HRANDFIELD" {
				cols = [2]string{"field", "value"}
			}
		}
		set.Columns = cols[:]
		for i := 0; i+1 < len(arr); i += 2 {
			if !add(redisValue(arr[i]), redisValue(arr[i+1])) {
				return
			}
		}
		return
	case isArr:
		set.Columns = []string{"#", "value"}
		for i, e := range arr {
			if !add(Text(strconv.Itoa(i+1)), redisValue(e)) {
				return
			}
		}
		return
	}
	set.Columns = []string{"value"}
	add(redisValue(reply))
}

// redisStreamRows lays stream entries out as id plus a column per field.
func redisStreamRows(entries []any, set *ResultSet, add func(...Value) bool) {
	type entry struct {
		id     string
		fields map[string]Value
	}
	var list []entry
	idx := map[string]int{}
	var names []string
	for _, e := range entries {
		pair, _ := e.([]any)
		if len(pair) != 2 {
			continue
		}
		fv, _ := pair[1].([]any)
		en := entry{id: redisValue(pair[0]).Text, fields: map[string]Value{}}
		for i := 0; i+1 < len(fv); i += 2 {
			f := redisValue(fv[i]).Text
			if _, ok := idx[f]; !ok {
				idx[f] = len(names)
				names = append(names, f)
			}
			en.fields[f] = redisValue(fv[i+1])
		}
		list = append(list, en)
	}
	set.Columns = append([]string{"id"}, names...)
	for _, en := range list {
		row := []Value{Text(en.id)}
		for _, n := range names {
			v, ok := en.fields[n]
			if !ok {
				v = NullValue
			}
			row = append(row, v)
		}
		if !add(row...) {
			return
		}
	}
}

// ---------------------------------------------------------------- patterns

// A sidebar entry, and so a "table", is a SCAN MATCH pattern: user:* lists
// keys; a pattern without wildcards is one key, whose contents are shown.

// globEscape writes a key as a pattern that matches only it.
func globEscape(key string) string {
	var b strings.Builder
	for _, r := range key {
		if strings.ContainsRune(`*?[]\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// literalKey reports whether a pattern names one key, and which.
func literalKey(pattern string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c == '\\' && i+1 < len(pattern) {
			i++
			b.WriteByte(pattern[i])
			continue
		}
		if c == '*' || c == '?' || c == '[' {
			return "", false
		}
		b.WriteByte(c)
	}
	return b.String(), true
}

// filterRows keeps rows matching every filter (case-insensitive text, or
// numbers when both sides are numbers), for views read whole into memory.
func filterRows(cols []string, rows [][]Value, filters []Filter) ([][]Value, error) {
	if len(filters) == 0 {
		return rows, nil
	}
	idx := map[string]int{}
	for i, c := range cols {
		idx[c] = i
	}
	match := func(v Value, f Filter) (bool, error) {
		text := strings.ToLower(v.Text)
		val := strings.ToLower(f.Value)
		num := func(s string) (float64, bool) { n, err := strconv.ParseFloat(s, 64); return n, err == nil }
		cmp := func(a, b string) int {
			if x, ok := num(a); ok {
				if y, ok := num(b); ok {
					switch {
					case x < y:
						return -1
					case x > y:
						return 1
					}
					return 0
				}
			}
			return strings.Compare(a, b)
		}
		switch f.Op {
		case OpIsNull:
			return v.Null, nil
		case OpIsNotNull:
			return !v.Null, nil
		}
		if v.Null {
			return false, nil
		}
		switch f.Op {
		case OpEq:
			return cmp(text, val) == 0, nil
		case OpNe:
			return cmp(text, val) != 0, nil
		case OpLt:
			return cmp(text, val) < 0, nil
		case OpGt:
			return cmp(text, val) > 0, nil
		case OpLe:
			return cmp(text, val) <= 0, nil
		case OpGe:
			return cmp(text, val) >= 0, nil
		case OpBetween:
			return cmp(text, val) >= 0 && cmp(text, strings.ToLower(f.Value2)) <= 0, nil
		case OpContains:
			return strings.Contains(text, val), nil
		case OpNotContain:
			return !strings.Contains(text, val), nil
		case OpBeginsWith:
			return strings.HasPrefix(text, val), nil
		case OpEndsWith:
			return strings.HasSuffix(text, val), nil
		case OpIn, OpNotIn:
			in := false
			for _, p := range strings.Split(val, ",") {
				if cmp(text, strings.TrimSpace(p)) == 0 {
					in = true
				}
			}
			return in == (f.Op == OpIn), nil
		case OpLike, OpNotLike:
			ok := likeMatch(text, val)
			return ok == (f.Op == OpLike), nil
		}
		return false, fmt.Errorf("filter %s isn't available here", f.Op)
	}
	var out [][]Value
	for _, row := range rows {
		keep := true
		for _, f := range filters {
			if f.Column == AnyColumn {
				any := false
				for _, v := range row {
					if !v.Null && strings.Contains(strings.ToLower(v.Text), strings.ToLower(f.Value)) {
						any = true
					}
				}
				keep = keep && any
				continue
			}
			i, ok := idx[f.Column]
			if !ok {
				return nil, fmt.Errorf("no column %q", f.Column)
			}
			m, err := match(row[i], f)
			if err != nil {
				return nil, err
			}
			keep = keep && m
		}
		if keep {
			out = append(out, row)
		}
	}
	return out, nil
}

// likeMatch is SQL LIKE: % any run, _ one character.
func likeMatch(s, pattern string) bool {
	if pattern == "" {
		return s == ""
	}
	switch pattern[0] {
	case '%':
		for i := 0; i <= len(s); i++ {
			if likeMatch(s[i:], pattern[1:]) {
				return true
			}
		}
		return false
	case '_':
		return s != "" && likeMatch(s[1:], pattern[1:])
	}
	return s != "" && s[0] == pattern[0] && likeMatch(s[1:], pattern[1:])
}

// sortRows orders rows by the page's sorts (numbers as numbers).
func sortRows(cols []string, rows [][]Value, sorts []Sort) error {
	idx := map[string]int{}
	for i, c := range cols {
		idx[c] = i
	}
	for _, s := range sorts {
		if _, ok := idx[s.Column]; !ok {
			return fmt.Errorf("no column %q", s.Column)
		}
	}
	less := func(a, b Value) int {
		if a.Null != b.Null {
			if a.Null {
				return -1
			}
			return 1
		}
		x, e1 := strconv.ParseFloat(a.Text, 64)
		y, e2 := strconv.ParseFloat(b.Text, 64)
		if e1 == nil && e2 == nil {
			switch {
			case x < y:
				return -1
			case x > y:
				return 1
			}
			return 0
		}
		return strings.Compare(a.Text, b.Text)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, s := range sorts {
			c := less(rows[i][idx[s.Column]], rows[j][idx[s.Column]])
			if s.Dir == Desc {
				c = -c
			}
			if c != 0 {
				return c < 0
			}
		}
		return false
	})
	return nil
}
