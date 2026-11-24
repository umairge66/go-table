package db

// The query editor on Elasticsearch and OpenSearch: requests as Kibana's Dev
// Tools takes them (a method and a path, then a JSON or NDJSON body), SQL,
// and on Elasticsearch ES|QL (FROM … | WHERE …).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	esRequestRe = regexp.MustCompile(`^\s*(GET|POST|PUT|DELETE|HEAD|PATCH)\s+(\S+)[ \t]*`)
	esWordRe    = regexp.MustCompile(`^\s*[A-Za-z]`)
	esTriple    = regexp.MustCompile(`(?s)"""(.*?)"""`)
)

func esComment(line string) bool {
	s := strings.TrimSpace(line)
	return strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//")
}

// split: a request runs to the next line that starts a statement outside its
// JSON; SQL and ES|QL run to a semicolon, or to the next request.
func (e *elastic) split(script string) []Span {
	var out []Span
	start, end := -1, 0 // of the statement being read
	request := false
	var js esScan
	flush := func() {
		if start >= 0 {
			if s := strings.TrimSpace(script[start:end]); s != "" {
				out = append(out, Span{SQL: s, Start: start, End: end})
			}
		}
		start = -1
	}
	for at := 0; at < len(script); {
		next := strings.IndexByte(script[at:], '\n')
		if next < 0 {
			next = len(script)
		} else {
			next += at + 1
		}
		line := script[at:next]
		inside := start >= 0 && request && js.open()
		switch {
		case !inside && esRequestRe.MatchString(line):
			flush()
			start, end, request, js = at, next, true, esScan{}
			js.feed(line[len(esRequestRe.FindString(line)):])
		case inside, start >= 0 && request && !esWordRe.MatchString(line):
			if !inside && esComment(line) { // before the next request, perhaps
				break
			}
			js.feed(line)
			if strings.TrimSpace(line) != "" {
				end = next
			}
		case strings.TrimSpace(line) == "" || esComment(line) || strings.HasPrefix(strings.TrimSpace(line), "--"):
			if start >= 0 {
				end = next
			}
		default: // SQL or ES|QL
			if request {
				flush()
			}
			request = false
			if start < 0 {
				start = at
			}
			end = next
			if strings.HasSuffix(strings.TrimSpace(line), ";") {
				flush()
			}
		}
		at = next
	}
	flush()
	return out
}

// esScan follows JSON's brackets and strings.
type esScan struct {
	depth  int
	str    bool
	esc    bool
	triple bool
}

func (s *esScan) open() bool { return s.depth > 0 || s.triple }

func (s *esScan) feed(line string) {
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case s.triple:
			if strings.HasPrefix(line[i:], `"""`) {
				s.triple = false
				i += 2
			}
		case s.str:
			switch {
			case s.esc:
				s.esc = false
			case ch == '\\':
				s.esc = true
			case ch == '"' || ch == '\n': // a JSON string ends with its line
				s.str = false
			}
		case strings.HasPrefix(line[i:], `"""`):
			s.triple = true
			i += 2
		case ch == '"':
			s.str = true
		case ch == '{' || ch == '[':
			s.depth++
		case ch == '}' || ch == ']':
			s.depth--
		}
	}
}

// esStrip drops comment lines in front of a statement.
func esStrip(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		if !esComment(s) && !strings.HasPrefix(s, "--") {
			return s
		}
		_, rest, ok := strings.Cut(s, "\n")
		if !ok {
			return ""
		}
		s = rest
	}
}

func (e *elastic) run(ctx context.Context, _ *Conn, stmt string, rowCap int) (r Result) {
	r = Result{Statement: stmt}
	start := time.Now()
	defer func() { r.Duration = time.Since(start) }()
	s := esStrip(stmt)
	if m := esRequestRe.FindStringSubmatch(s); m != nil {
		e.request(ctx, &r, m[1], m[2], s[len(m[0]):], rowCap)
		return r
	}
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ";"))
	word := strings.ToUpper(strings.FieldsFunc(s+" ", func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' || r == '(' })[0])
	switch word {
	case "SELECT", "DESCRIBE", "DESC", "SHOW", "WITH", "EXPLAIN":
		if word == "SHOW" && strings.HasPrefix(strings.ToUpper(strings.Join(strings.Fields(s), " ")), "SHOW INFO") {
			e.esql(ctx, &r, s, rowCap)
			return r
		}
		e.sql(ctx, &r, s, rowCap)
	case "FROM", "ROW", "TS":
		e.esql(ctx, &r, s, rowCap)
	default:
		r.Err = fmt.Errorf("%s: a statement is a request (GET /index/_search), SQL, or ES|QL (FROM index | …)", snippet(s))
	}
	return r
}

// request runs METHOD path body.
func (e *elastic) request(ctx context.Context, r *Result, method, path, body string, rowCap int) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	route, query, _ := strings.Cut(path, "?")
	body = strings.TrimSpace(body)
	var send []byte
	if body != "" {
		body = esTriple.ReplaceAllStringFunc(body, func(s string) string { return esCompact(s[3 : len(s)-3]) })
		send = []byte(body)
		if strings.HasSuffix(route, "/_bulk") || strings.HasSuffix(route, "/_msearch") {
			send = append(send, '\n')
		}
	}
	if strings.HasPrefix(route, "/_cat/") && !strings.Contains(query, "format=") && !strings.Contains(query, "help") {
		path = route + "?format=json"
		if query != "" {
			path += "&" + query
		}
	}
	res, err := e.do(ctx, method, path, send)
	if err != nil {
		r.Err = err
		return
	}
	defer res.Body.Close()
	if method == http.MethodHead {
		r.Set = esOneCell("status", res.Status)
		return
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		r.Err = err
		return
	}
	last := route[strings.LastIndexByte(route, '/')+1:]
	if last == "_bulk" {
		done, failed, err := esBulkAnswer(bytes.NewReader(data))
		r.RowsAffected = int64(done)
		if err != nil {
			if failed >= 0 {
				err = fmt.Errorf("action %d: %w (%d others were done)", failed+1, err, done)
			}
			r.Err = err
		}
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		r.Set = esOneCell("response", strings.TrimRight(string(data), "\n"))
		return
	}
	switch x := v.(type) {
	case []any:
		if rs := esObjects(x, data); rs != nil {
			r.Set = rs
			r.cut(rowCap)
			return
		}
	case map[string]any:
		if f, ok := x["failures"].([]any); ok && len(f) > 0 {
			r.Err = fmt.Errorf("%d documents failed: %s", len(f), snippet(esCompact(f[0])))
			return
		}
		if hits, ok := x["hits"].(map[string]any); ok {
			if rs, err := esHitsTable(data); err == nil {
				if list, _ := hits["hits"].([]any); len(list) > 0 || x["aggregations"] == nil {
					r.Set = rs
					r.cut(rowCap)
					return
				}
			}
		}
		if aggs, ok := x["aggregations"].(map[string]any); ok {
			r.Set = esAggs(aggs)
			r.cut(rowCap)
			return
		}
		if n, ok := esAffected(method, last, x); ok {
			r.RowsAffected = n
			return
		}
		if _, ok := x["_source"]; ok && x["_id"] != nil {
			var h esHit
			if json.Unmarshal(data, &h) == nil {
				if rs, err := esTable([]esHit{h}, nil); err == nil {
					r.Set = rs
					return
				}
			}
		}
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, data, "", "  ") != nil {
		pretty.Write(data)
	}
	r.Set = esOneCell("response", strings.TrimSpace(pretty.String()))
}

func esOneCell(col, text string) *ResultSet {
	return &ResultSet{Columns: []string{col}, Types: []string{""}, Rows: [][]Value{{Text(text)}}}
}

// cut keeps at most n rows.
func (r *Result) cut(n int) {
	if n > 0 && r.Set != nil && len(r.Set.Rows) > n {
		r.Set.Rows, r.Truncated = r.Set.Rows[:n], true
	}
}

// esAffected: how many documents a write changed, if the answer is a write's.
func esAffected(method, last string, x map[string]any) (int64, bool) {
	num := func(k string) int64 {
		n, _ := x[k].(json.Number)
		i, _ := n.Int64()
		return i
	}
	switch last {
	case "_delete_by_query":
		return num("deleted"), x["deleted"] != nil
	case "_update_by_query":
		return num("updated"), x["updated"] != nil
	case "_reindex":
		return num("created") + num("updated"), x["created"] != nil
	}
	if method == http.MethodGet {
		return 0, false
	}
	switch x["result"] {
	case "created", "updated", "deleted":
		return 1, true
	case "noop":
		return 0, true
	}
	if ok, _ := x["acknowledged"].(bool); ok {
		return 0, true
	}
	if last == "_refresh" || last == "_flush" || last == "_forcemerge" {
		return 0, true
	}
	return 0, false
}

func esHitsTable(data []byte) (*ResultSet, error) {
	var s esSearch
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	rs, err := esTable(s.Hits.Hits, nil)
	if err != nil {
		return nil, err
	}
	// Hits of several indices say which.
	many := false
	for _, h := range s.Hits.Hits {
		many = many || h.Index != s.Hits.Hits[0].Index
	}
	if many {
		rs.Columns = append([]string{"_index"}, rs.Columns...)
		rs.Types = append([]string{""}, rs.Types...)
		for i := range rs.Rows {
			rs.Rows[i] = append([]Value{Text(s.Hits.Hits[i].Index)}, rs.Rows[i]...)
		}
	}
	return rs, nil
}

// esObjects shows a list of objects (_cat) as a table, its columns in the
// order the server sent them; nil if it isn't one.
func esObjects(list []any, data []byte) *ResultSet {
	var raws []json.RawMessage
	if json.Unmarshal(data, &raws) != nil {
		return nil
	}
	rs := &ResultSet{Columns: []string{}, Types: []string{}, Rows: [][]Value{}}
	at := map[string]int{}
	for i, x := range list {
		m, ok := x.(map[string]any)
		if !ok {
			return nil
		}
		for _, k := range jsonKeys(raws[i]) {
			if _, ok := at[k]; !ok {
				at[k] = len(rs.Columns)
				rs.Columns = append(rs.Columns, k)
				rs.Types = append(rs.Types, "")
			}
		}
		row := make([]Value, len(rs.Columns))
		for j := range row {
			row[j] = NullValue
		}
		for k, v := range m {
			row[at[k]] = esAnyValue(v)
		}
		rs.Rows = append(rs.Rows, row)
	}
	for i, row := range rs.Rows { // columns a later object brought
		for len(row) < len(rs.Columns) {
			row = append(row, NullValue)
		}
		rs.Rows[i] = row
	}
	return rs
}

// jsonKeys lists an object's keys in order.
func jsonKeys(raw json.RawMessage) []string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var out []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return out
		}
		k, _ := t.(string)
		out = append(out, k)
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return out
		}
	}
	return out
}

func esAnyValue(v any) Value {
	switch x := v.(type) {
	case nil:
		return NullValue
	case string:
		return Text(x)
	case json.Number:
		return Text(x.String())
	}
	return Text(esCompact(v))
}

// esAggs shows aggregations: the buckets of one as rows (key, documents, and
// its metrics), single values as one row, anything deeper as JSON.
func esAggs(aggs map[string]any) *ResultSet {
	names := make([]string, 0, len(aggs))
	for n := range aggs {
		names = append(names, n)
	}
	sort.Strings(names)
	metric := func(v any) (Value, bool) {
		m, ok := v.(map[string]any)
		if !ok {
			return NullValue, false
		}
		if s, ok := m["value_as_string"].(string); ok {
			return Text(s), true
		}
		if x, ok := m["value"]; ok {
			return esAnyValue(x), true
		}
		return NullValue, false
	}
	if len(names) == 1 {
		if m, ok := aggs[names[0]].(map[string]any); ok {
			if buckets, ok := m["buckets"].([]any); ok {
				rs := &ResultSet{Columns: []string{names[0], "doc_count"}, Types: []string{"", "long"}, Rows: [][]Value{}}
				var subs []string
				seen := map[string]bool{"key": true, "key_as_string": true, "doc_count": true}
				for _, b := range buckets {
					if bm, ok := b.(map[string]any); ok {
						for k := range bm {
							if !seen[k] {
								seen[k] = true
								subs = append(subs, k)
							}
						}
					}
				}
				sort.Strings(subs)
				for _, s := range subs {
					rs.Columns, rs.Types = append(rs.Columns, s), append(rs.Types, "")
				}
				for _, b := range buckets {
					bm, _ := b.(map[string]any)
					key := bm["key"]
					if s, ok := bm["key_as_string"]; ok {
						key = s
					}
					row := []Value{esAnyValue(key), esAnyValue(bm["doc_count"])}
					for _, s := range subs {
						v, ok := metric(bm[s])
						if !ok && bm[s] != nil {
							v = esAnyValue(bm[s])
						}
						row = append(row, v)
					}
					rs.Rows = append(rs.Rows, row)
				}
				return rs
			}
		}
	}
	rs := &ResultSet{Columns: names, Types: make([]string, len(names))}
	row := make([]Value, len(names))
	for i, n := range names {
		v, ok := metric(aggs[n])
		if !ok {
			v = Text(esPretty(aggs[n]))
		}
		row[i] = v
	}
	rs.Rows = [][]Value{row}
	return rs
}

type esTabular struct {
	Columns []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"columns"`
	Rows   [][]any `json:"rows"`
	Values [][]any `json:"values"` // ES|QL
	Cursor string  `json:"cursor"`
	// OpenSearch.
	Schema []struct {
		Name  string `json:"name"`
		Alias string `json:"alias"`
		Type  string `json:"type"`
	} `json:"schema"`
	Datarows [][]any `json:"datarows"`
}

func (t *esTabular) into(rs *ResultSet) {
	for _, c := range t.Columns {
		rs.Columns, rs.Types = append(rs.Columns, c.Name), append(rs.Types, c.Type)
	}
	for _, c := range t.Schema {
		n := c.Name
		if c.Alias != "" {
			n = c.Alias
		}
		rs.Columns, rs.Types = append(rs.Columns, n), append(rs.Types, c.Type)
	}
	t.more(rs)
}

func (t *esTabular) more(rs *ResultSet) {
	for _, list := range [][][]any{t.Rows, t.Values, t.Datarows} {
		for _, r := range list {
			row := make([]Value, len(r))
			for i, v := range r {
				row[i] = esAnyValue(v)
			}
			rs.Rows = append(rs.Rows, row)
		}
	}
}

// sql runs a SQL query: Elasticsearch's _sql, OpenSearch's SQL plugin.
func (e *elastic) sql(ctx context.Context, r *Result, q string, rowCap int) {
	rs := &ResultSet{Columns: []string{}, Types: []string{}, Rows: [][]Value{}}
	var out esTabular
	if e.product == "OpenSearch" {
		if r.Err = e.call(ctx, http.MethodPost, "/_plugins/_sql", map[string]any{"query": q}, &out); r.Err != nil {
			if s := esStatus(r.Err); s == http.StatusNotFound || s == http.StatusMethodNotAllowed || strings.Contains(r.Err.Error(), "no handler found") {
				r.Err = errors.New("this server has no SQL plugin: use requests, GET /index/_search")
			}
			return
		}
		out.into(rs)
	} else {
		size := esBatch
		if rowCap > 0 {
			size = min(size, rowCap+1)
		}
		if r.Err = e.call(ctx, http.MethodPost, "/_sql?format=json", map[string]any{"query": q, "fetch_size": size}, &out); r.Err != nil {
			return
		}
		out.into(rs)
		for out.Cursor != "" && (rowCap <= 0 || len(rs.Rows) <= rowCap) {
			cursor := out.Cursor
			out = esTabular{}
			if r.Err = e.call(ctx, http.MethodPost, "/_sql?format=json", map[string]any{"cursor": cursor}, &out); r.Err != nil {
				return
			}
			out.more(rs)
		}
		if out.Cursor != "" {
			e.call(context.WithoutCancel(ctx), http.MethodPost, "/_sql/close", map[string]any{"cursor": out.Cursor}, nil)
		}
	}
	r.Set = rs
	r.cut(rowCap)
}

func (e *elastic) esql(ctx context.Context, r *Result, q string, rowCap int) {
	if e.product == "OpenSearch" {
		r.Err = errors.New("OpenSearch has no ES|QL: use SQL or a request, GET /index/_search")
		return
	}
	var out esTabular
	if r.Err = e.call(ctx, http.MethodPost, "/_query?format=json", map[string]any{"query": q}, &out); r.Err != nil {
		return
	}
	rs := &ResultSet{Columns: []string{}, Types: []string{}, Rows: [][]Value{}}
	out.into(rs)
	r.Set = rs
	r.cut(rowCap)
}
