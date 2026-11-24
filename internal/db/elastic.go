package db

// Elasticsearch and OpenSearch: JSON over HTTP, documents in indices.
//
//   - The cluster is the only schema. An index is a table whose columns are
//     _id and the mapping's top-level fields; objects and lists are JSON.
//     Aliases and data streams are views.
//   - Filters and sorts run on the server as Query DSL. Text is analysed, so
//     whole-value comparisons, "contains" and sorting need a keyword field
//     (or a text field's keyword sub-field); without one, text is matched by
//     words.
//   - Saving writes each document by itself (_bulk): there are no
//     transactions. A document changed between reading it for the save and
//     writing it is refused.
//   - The editor takes requests as Kibana's Dev Tools does, SQL, and on
//     Elasticsearch ES|QL (elastic_query.go).

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gotable/internal/model"
)

const (
	esWindow = 10_000 // from + size that a search takes (index.max_result_window)
	esBatch  = 1000   // documents per scroll page and bulk request
)

type elastic struct {
	base    string
	auth    string // the Authorization header
	http    *http.Client
	product string // Elasticsearch or OpenSearch
	version string
	cluster string
}

// remoteHTTP is a client whose connections go to dialAddr if not "" (an SSH
// tunnel's end) while requests keep their own host.
func remoteHTTP(dialAddr string, tlsConf *tls.Config) *http.Client {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConf, TLSHandshakeTimeout: 15 * time.Second, IdleConnTimeout: time.Minute, ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if dialAddr != "" {
				addr = dialAddr
			}
			return d.DialContext(ctx, network, addr)
		}}}
}

// remoteBase is a connection's URL and client.
func remoteBase(cfg *model.Connection, addr string, tunneled bool) (string, *http.Client, error) {
	t, err := mongoTLS(cfg, tunneled)
	if err != nil {
		return "", nil, err
	}
	scheme, dial := "http", ""
	if t != nil {
		scheme = "https"
	}
	if tunneled {
		dial = addr
	}
	return scheme + "://" + net.JoinHostPort(strings.Trim(cfg.HostOrDefault(), "[]"), strconv.Itoa(cfg.PortOrDefault())), remoteHTTP(dial, t), nil
}

func openElastic(ctx context.Context, cfg *model.Connection, addr, password string, tunnel io.Closer) (*Conn, error) {
	fail := func(err error) (*Conn, error) {
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, err
	}
	e := &elastic{}
	var err error
	if e.base, e.http, err = remoteBase(cfg, addr, tunnel != nil); err != nil {
		return fail(err)
	}
	switch {
	case cfg.User != "":
		req, _ := http.NewRequest("GET", e.base, nil)
		req.SetBasicAuth(cfg.User, password)
		e.auth = req.Header.Get("Authorization")
	case password != "":
		e.auth = "ApiKey " + strings.TrimSpace(password)
	}
	var info struct {
		Cluster string `json:"cluster_name"`
		Version struct {
			Number       string `json:"number"`
			Distribution string `json:"distribution"`
		} `json:"version"`
	}
	if err := e.get(ctx, "/", &info); err != nil {
		return fail(err)
	}
	if info.Version.Number == "" {
		return fail(errors.New("the server's answer isn't Elasticsearch's or OpenSearch's"))
	}
	e.product, e.version, e.cluster = "Elasticsearch", info.Version.Number, info.Cluster
	if strings.EqualFold(info.Version.Distribution, "opensearch") {
		e.product = "OpenSearch"
	}
	if e.cluster == "" {
		e.cluster = "cluster"
	}
	return &Conn{Config: cfg, Dialect: DialectFor(cfg.Driver), eng: e, remote: e, tunnel: tunnel, schema: e.cluster}, nil
}

// ---------------------------------------------------------------- requests

// esError is what the server says about a request it refused.
type esError struct {
	Status int
	Type   string
	Reason string
}

func (e *esError) Error() string {
	if e.Type == "" {
		return e.Reason
	}
	return e.Type + ": " + e.Reason
}

// esReadError picks the most telling reason out of an error answer: the
// innermost cause.
func esReadError(status int, body []byte) error {
	var out struct {
		Error json.RawMessage `json:"error"`
	}
	json.Unmarshal(body, &out)
	type cause struct {
		Type      string          `json:"type"`
		Reason    string          `json:"reason"`
		CausedBy  json.RawMessage `json:"caused_by"`
		RootCause []struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"root_cause"`
		FailedShards []struct {
			Reason json.RawMessage `json:"reason"`
		} `json:"failed_shards"`
		Details string `json:"details"` // OpenSearch's SQL
	}
	e := &esError{Status: status}
	var walk func(raw json.RawMessage, depth int)
	walk = func(raw json.RawMessage, depth int) {
		var c cause
		if depth > 8 || json.Unmarshal(raw, &c) != nil || c.Type == "" && c.Reason == "" {
			return
		}
		if c.Reason != "" {
			e.Type, e.Reason = c.Type, c.Reason
		}
		if c.Details != "" {
			e.Reason += ": " + c.Details
		}
		switch {
		case len(c.CausedBy) > 0:
			walk(c.CausedBy, depth+1)
		case len(c.FailedShards) > 0:
			walk(c.FailedShards[0].Reason, depth+1)
		case len(c.RootCause) > 0 && c.RootCause[0].Reason != "":
			e.Type, e.Reason = c.RootCause[0].Type, c.RootCause[0].Reason
		}
	}
	walk(out.Error, 0)
	if e.Reason == "" {
		var s string
		if json.Unmarshal(out.Error, &s) == nil && s != "" {
			e.Reason = s
		} else if e.Reason = strings.TrimSpace(string(body)); e.Reason == "" || strings.HasPrefix(e.Reason, "<") || len(e.Reason) > 500 {
			e.Reason = fmt.Sprintf("the server answered %d %s", status, http.StatusText(status))
		}
	}
	return e
}

// do sends a request. An answer outside 2xx is an *esError.
func (e *elastic) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		ct := "application/json"
		if p, _, _ := strings.Cut(path, "?"); strings.HasSuffix(p, "/_bulk") || strings.HasSuffix(p, "/_msearch") {
			ct = "application/x-ndjson"
		}
		req.Header.Set("Content-Type", ct)
	}
	if e.auth != "" {
		req.Header.Set("Authorization", e.auth)
	}
	res, err := e.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		return nil, esReadError(res.StatusCode, b)
	}
	return res, nil
}

// call sends JSON (or nothing) and reads the JSON answer into out. Numbers
// keep every digit.
func (e *elastic) call(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	res, err := e.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil {
		_, err = io.Copy(io.Discard, res.Body)
		return err
	}
	dec := json.NewDecoder(res.Body)
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the server's answer couldn't be read: %w", err)
	}
	return nil
}

func (e *elastic) get(ctx context.Context, path string, out any) error {
	return e.call(ctx, http.MethodGet, path, nil, out)
}

func esStatus(err error) int {
	var ee *esError
	if errors.As(err, &ee) {
		return ee.Status
	}
	return 0
}

// esPath is an index's name in a URL.
func esPath(name string) string { return "/" + url.PathEscape(name) }

func (e *elastic) ping(ctx context.Context, _ *Conn) error { return e.get(ctx, "/", nil) }

// ---------------------------------------------------------------- engine

func (e *elastic) serverVersion(context.Context, *Conn) (string, error) {
	return e.product + " " + e.version, nil
}

func (e *elastic) databases(context.Context, *Conn) ([]string, error) { return []string{}, nil }

func (e *elastic) schemas(context.Context, *Conn) ([]string, error) { return []string{e.cluster}, nil }

func (e *elastic) currentSchema(context.Context, *Conn) (string, error) { return e.cluster, nil }

func (e *elastic) useSchema(_ context.Context, _ *Conn, schema string) error {
	if schema != e.cluster {
		return fmt.Errorf("this connection is to the cluster %s", e.cluster)
	}
	return nil
}

// objects lists what the user may see (_resolve needs no right on the rest).
func (e *elastic) objects(ctx context.Context, _ *Conn, schema string) ([]Object, error) {
	out := []Object{}
	seen := map[string]bool{}
	add := func(name string, k ObjectKind) {
		if name != "" && name[0] != '.' && !seen[name] {
			seen[name] = true
			out = append(out, Object{Schema: schema, Name: name, Kind: k})
		}
	}
	var all struct {
		Indices []struct {
			Name       string   `json:"name"`
			Attributes []string `json:"attributes"`
			DataStream string   `json:"data_stream"`
		} `json:"indices"`
		Aliases []struct {
			Name string `json:"name"`
		} `json:"aliases"`
		Streams []struct {
			Name string `json:"name"`
		} `json:"data_streams"`
	}
	if err := e.get(ctx, "/_resolve/index/*?expand_wildcards=open", &all); err != nil {
		if esStatus(err) != http.StatusBadRequest && esStatus(err) != http.StatusNotFound && esStatus(err) != http.StatusMethodNotAllowed {
			return nil, err
		}
		var idx []struct {
			Index string `json:"index"`
		}
		if err := e.get(ctx, "/_cat/indices?format=json&h=index", &idx); err != nil { // before 7.9
			return nil, err
		}
		for _, x := range idx {
			add(x.Index, KindTable)
		}
		var aliases map[string]struct {
			Aliases map[string]json.RawMessage `json:"aliases"`
		}
		if e.get(ctx, "/_alias", &aliases) == nil {
			for _, x := range aliases {
				for name := range x.Aliases {
					add(name, KindView)
				}
			}
		}
	}
	for _, x := range all.Indices {
		if x.DataStream == "" && !slices.Contains(x.Attributes, "hidden") && !slices.Contains(x.Attributes, "system") {
			add(x.Name, KindTable)
		}
	}
	for _, x := range all.Aliases {
		add(x.Name, KindView)
	}
	for _, x := range all.Streams {
		add(x.Name, KindView)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type esProp struct {
	Type       string            `json:"type"`
	Properties map[string]esProp `json:"properties"`
	Fields     map[string]esProp `json:"fields"`
}

// esField is a top-level field. exact is the path to compare whole values
// and sort with, "" for text without a keyword sub-field.
type esField struct {
	typ   string
	exact string
}

// fields reads the mapping of an index (or of all behind an alias).
func (e *elastic) fields(ctx context.Context, name string) (map[string]esField, error) {
	var out map[string]struct {
		Mappings struct {
			Properties map[string]esProp `json:"properties"`
		} `json:"mappings"`
	}
	if err := e.get(ctx, esPath(name)+"/_mapping", &out); err != nil {
		if esStatus(err) == http.StatusNotFound {
			return nil, fmt.Errorf("index %s not found", name)
		}
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("index %s not found", name)
	}
	fields := map[string]esField{}
	for _, idx := range out {
		for n, p := range idx.Mappings.Properties {
			if p.Type == "alias" { // another name for a field, not in documents
				continue
			}
			f := esField{typ: p.Type, exact: n}
			if f.typ == "" {
				f.typ = "object"
			}
			switch f.typ {
			case "text", "match_only_text", "search_as_you_type", "annotated_text":
				f.exact = ""
				subs := make([]string, 0, len(p.Fields))
				for s, sp := range p.Fields {
					if sp.Type == "keyword" || sp.Type == "wildcard" || sp.Type == "constant_keyword" {
						subs = append(subs, s)
					}
				}
				if len(subs) > 0 {
					slices.Sort(subs)
					f.exact = n + "." + subs[0]
				}
			case "object", "nested", "flattened", "geo_point", "geo_shape", "point", "shape", "dense_vector", "sparse_vector", "rank_feature", "rank_features", "join", "percolator", "completion", "binary", "knn_vector":
				f.exact = ""
			}
			if old, ok := fields[n]; ok && old.typ != f.typ && !slices.Contains(strings.Split(old.typ, "|"), f.typ) {
				f.typ = old.typ + "|" + f.typ
			}
			fields[n] = f
		}
	}
	return fields, nil
}

func esNames(fields map[string]esField) []string {
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (e *elastic) columns(ctx context.Context, _ *Conn, t TableRef) ([]Column, error) {
	fields, err := e.fields(ctx, t.Name)
	if err != nil {
		return nil, err
	}
	out := []Column{{Name: "_id", Type: "_id", PrimaryKey: true, Nullable: true, AutoIncrement: true}}
	for _, n := range esNames(fields) {
		out = append(out, Column{Name: n, Type: fields[n].typ, Nullable: true})
	}
	return out, nil
}

func (e *elastic) indexes(context.Context, *Conn, TableRef) ([]Index, error) { return []Index{}, nil }

func (e *elastic) foreignKeys(context.Context, *Conn, TableRef) ([]ForeignRef, error) {
	return nil, nil
}

func (e *elastic) tableInfo(ctx context.Context, _ *Conn, t TableRef) (TableInfo, error) {
	var st []struct {
		Health string `json:"health"`
		Pri    string `json:"pri"`
		Rep    string `json:"rep"`
		Size   string `json:"store.size"`
	}
	if err := e.get(ctx, "/_cat/indices"+esPath(t.Name)+"?format=json&h=health,pri,rep,store.size", &st); err != nil || len(st) == 0 {
		return TableInfo{}, nil // rights, or a data stream: not worth an error
	}
	if len(st) > 1 {
		return TableInfo{Options: fmt.Sprintf("%d indices", len(st))}, nil
	}
	s := st[0]
	return TableInfo{Options: fmt.Sprintf("%s · %s primary shard(s), %s replica(s) · %s", s.Health, s.Pri, s.Rep, s.Size)}, nil
}

// definition is the request that makes the index: settings, mappings and
// aliases, without what the server adds itself.
func (e *elastic) definition(ctx context.Context, _ *Conn, o Object) (string, error) {
	if o.Kind == KindView {
		var ds any
		if err := e.get(ctx, "/_data_stream"+esPath(o.Name), &ds); err == nil {
			return "# A data stream: made with an index template, see\nGET /_data_stream/" + o.Name + "\n\n# now:\n# " + strings.ReplaceAll(esPretty(ds), "\n", "\n# "), nil
		}
		var al map[string]struct {
			Aliases map[string]json.RawMessage `json:"aliases"`
		}
		if err := e.get(ctx, "/_alias"+esPath(o.Name), &al); err != nil {
			return "", err
		}
		names := make([]string, 0, len(al))
		for n := range al {
			names = append(names, n)
		}
		slices.Sort(names)
		actions := []any{}
		for _, n := range names {
			add := map[string]any{}
			json.Unmarshal(al[n].Aliases[o.Name], &add)
			add["index"], add["alias"] = n, o.Name
			actions = append(actions, map[string]any{"add": add})
		}
		return "POST /_aliases\n" + esPretty(map[string]any{"actions": actions}), nil
	}
	body, err := e.create(ctx, o.Name)
	if err != nil {
		return "", err
	}
	return "PUT /" + url.PathEscape(o.Name) + "\n" + esPretty(body), nil
}

// create is the body that makes an index again.
func (e *elastic) create(ctx context.Context, name string) (map[string]any, error) {
	var out map[string]map[string]any
	if err := e.get(ctx, esPath(name), &out); err != nil {
		return nil, err
	}
	def, ok := out[name]
	if !ok {
		return nil, fmt.Errorf("%s isn't one index", name)
	}
	if s, ok := def["settings"].(map[string]any); ok {
		if ix, ok := s["index"].(map[string]any); ok {
			for _, k := range []string{"uuid", "creation_date", "provided_name", "version", "resize", "history_uuid", "routing", "replication"} {
				delete(ix, k)
			}
			def["settings"] = map[string]any{"index": ix}
		}
	}
	for k := range def {
		if k != "settings" && k != "mappings" && k != "aliases" {
			delete(def, k) // data_stream, lifecycle…: only for backing indices
		}
	}
	return def, nil
}

func esPretty(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(v)
	return strings.TrimSpace(b.String())
}

func esCompact(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return strings.TrimSpace(b.String())
}

// ---------------------------------------------------------------- reading

type esHit struct {
	Index   string                     `json:"_index"`
	ID      string                     `json:"_id"`
	Routing string                     `json:"_routing"`
	SeqNo   json.Number                `json:"_seq_no"`
	Term    json.Number                `json:"_primary_term"`
	Raw     json.RawMessage            `json:"_source"`
	source  map[string]json.RawMessage // of Raw
}

func (h *esHit) fields() (map[string]json.RawMessage, error) {
	if h.source == nil {
		h.source = map[string]json.RawMessage{}
		if len(h.Raw) > 0 {
			if err := json.Unmarshal(h.Raw, &h.source); err != nil {
				return nil, err
			}
		}
	}
	return h.source, nil
}

type esSearch struct {
	ScrollID string `json:"_scroll_id"`
	TimedOut bool   `json:"timed_out"`
	Hits     struct {
		Hits []esHit `json:"hits"`
	} `json:"hits"`
}

// esValue shows a JSON value in a cell.
func esValue(raw json.RawMessage) Value {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return NullValue
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return Text(s)
		}
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) == nil {
		return Text(b.String())
	}
	return Text(string(raw))
}

// table makes rows of documents: _id, the mapped fields, and what else the
// documents hold (fields the mapping leaves out).
func esTable(hits []esHit, fields map[string]esField) (*ResultSet, error) {
	cols := append([]string{"_id"}, esNames(fields)...)
	extra := map[string]bool{}
	for i := range hits {
		src, err := hits[i].fields()
		if err != nil {
			return nil, err
		}
		for k := range src {
			if _, ok := fields[k]; !ok && k != "_id" {
				extra[k] = true
			}
		}
	}
	more := make([]string, 0, len(extra))
	for k := range extra {
		more = append(more, k)
	}
	slices.Sort(more)
	cols = append(cols, more...)
	rs := &ResultSet{Columns: cols, Types: make([]string, len(cols)), Rows: make([][]Value, 0, len(hits))}
	rs.Types[0] = "_id"
	for i, n := range cols[1:] {
		rs.Types[i+1] = fields[n].typ
	}
	for i := range hits {
		src, _ := hits[i].fields()
		rs.Rows = append(rs.Rows, esRow(&hits[i], src, cols))
	}
	return rs, nil
}

func esRow(h *esHit, src map[string]json.RawMessage, cols []string) []Value {
	row := make([]Value, len(cols))
	row[0] = Text(h.ID)
	for i, n := range cols[1:] {
		row[i+1] = esValue(src[n])
	}
	return row
}

// body is a search's query and sort.
func (e *elastic) body(p Page, fields map[string]esField) (map[string]any, error) {
	q, err := esQuery(p, fields)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"query": q}
	if len(p.Sort) > 0 {
		var sorts []any
		for _, s := range p.Sort {
			f, ok := fields[s.Column]
			switch {
			case s.Column == "_id":
				return nil, errors.New("the server doesn't sort by _id: sort by a field that holds it")
			case !ok:
				return nil, fmt.Errorf("%s isn't in the mapping, so it can't be sorted by", s.Column)
			case f.exact == "":
				return nil, fmt.Errorf("a %s field can't be sorted by: %s has no keyword sub-field", f.typ, s.Column)
			}
			dir := "asc"
			if s.Dir == Desc {
				dir = "desc"
			}
			sorts = append(sorts, map[string]any{f.exact: map[string]any{"order": dir, "unmapped_type": "keyword"}})
		}
		out["sort"] = sorts
	}
	return out, nil
}

func (e *elastic) search(ctx context.Context, name, params string, body map[string]any) (*esSearch, error) {
	var out esSearch
	if params != "" {
		params = "&" + params
	}
	// Some shards' documents alone would be a wrong answer.
	if err := e.call(ctx, http.MethodPost, esPath(name)+"/_search?allow_partial_search_results=false"+params, body, &out); err != nil {
		return nil, err
	}
	if out.TimedOut {
		return nil, errors.New("the search timed out on the server")
	}
	return &out, nil
}

func (e *elastic) fetchRows(ctx context.Context, _ *Conn, t TableRef, p Page) (*ResultSet, error) {
	fields, err := e.fields(ctx, t.Name)
	if err != nil {
		return nil, err
	}
	body, err := e.body(p, fields)
	if err != nil {
		return nil, err
	}
	if p.Limit > 0 && p.Offset+p.Limit <= esWindow {
		body["from"], body["size"] = p.Offset, p.Limit
		body["track_total_hits"] = false
		res, err := e.search(ctx, t.Name, "", body)
		if err != nil {
			return nil, err
		}
		return esTable(res.Hits.Hits, fields)
	}
	// Further than a search reaches: read on from the start.
	var hits []esHit
	skip := p.Offset
	err = e.scroll(ctx, t.Name, body, func(h esHit) (bool, error) {
		if skip > 0 {
			skip--
			return true, nil
		}
		hits = append(hits, h)
		return p.Limit <= 0 || len(hits) < p.Limit, nil
	})
	if err != nil {
		return nil, err
	}
	return esTable(hits, fields)
}

// scroll calls fn for every document a search finds, until it returns false.
func (e *elastic) scroll(ctx context.Context, name string, body map[string]any, fn func(esHit) (bool, error)) error {
	body["size"] = esBatch
	if body["sort"] == nil {
		body["sort"] = []any{"_doc"}
	}
	res, err := e.search(ctx, name, "scroll=2m", body)
	if err != nil {
		return err
	}
	defer func() {
		e.call(context.WithoutCancel(ctx), http.MethodDelete, "/_search/scroll", map[string]any{"scroll_id": []string{res.ScrollID}}, nil)
	}()
	for len(res.Hits.Hits) > 0 {
		for _, h := range res.Hits.Hits {
			if more, err := fn(h); err != nil || !more {
				return err
			}
		}
		id := res.ScrollID
		var next esSearch
		if err := e.call(ctx, http.MethodPost, "/_search/scroll", map[string]any{"scroll": "2m", "scroll_id": id}, &next); err != nil {
			return err
		}
		if next.ScrollID == "" {
			next.ScrollID = id
		}
		res = &next
	}
	return nil
}

func (e *elastic) countRows(ctx context.Context, _ *Conn, t TableRef, p Page) (int64, error) {
	fields, err := e.fields(ctx, t.Name)
	if err != nil {
		return 0, err
	}
	q, err := esQuery(p, fields)
	if err != nil {
		return 0, err
	}
	var out struct {
		Count  int64 `json:"count"`
		Shards struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
	}
	if err := e.call(ctx, http.MethodPost, esPath(t.Name)+"/_count", map[string]any{"query": q}, &out); err != nil {
		return 0, err
	}
	if out.Shards.Failed > 0 {
		return 0, fmt.Errorf("%d shard(s) couldn't be counted", out.Shards.Failed)
	}
	return out.Count, nil
}

func (e *elastic) streamRows(ctx context.Context, _ *Conn, t TableRef, p Page, fn func(cols, types []string, row []Value) error) error {
	fields, err := e.fields(ctx, t.Name)
	if err != nil {
		return err
	}
	body, err := e.body(p, fields)
	if err != nil {
		return err
	}
	cols := append([]string{"_id"}, esNames(fields)...)
	types := make([]string, len(cols))
	types[0] = "_id"
	for i, n := range cols[1:] {
		types[i+1] = fields[n].typ
	}
	skip, left := p.Offset, p.Limit
	return e.scroll(ctx, t.Name, body, func(h esHit) (bool, error) {
		if skip > 0 {
			skip--
			return true, nil
		}
		src, err := h.fields()
		if err != nil {
			return false, err
		}
		if err := fn(cols, types, esRow(&h, src, cols)); err != nil {
			return false, err
		}
		left--
		return p.Limit <= 0 || left > 0, nil
	})
}

// ---------------------------------------------------------------- filters

// esQuery turns a page's filters into Query DSL.
func esQuery(p Page, fields map[string]esField) (any, error) {
	var all []any
	for _, f := range p.Filters {
		q, err := esCondition(f, fields)
		if err != nil {
			return nil, err
		}
		all = append(all, q)
	}
	if raw := strings.TrimSpace(p.RawWhere); raw != "" {
		if strings.HasPrefix(raw, "{") {
			if !json.Valid([]byte(raw)) {
				return nil, errors.New(`the query isn't valid JSON: {"term": {"status": "active"}}`)
			}
			var v map[string]json.RawMessage
			json.Unmarshal([]byte(raw), &v)
			if inner, ok := v["query"]; ok && len(v) == 1 { // as in a search's body
				all = append(all, inner)
			} else {
				all = append(all, json.RawMessage(raw))
			}
		} else {
			all = append(all, map[string]any{"query_string": map[string]any{"query": raw}})
		}
	}
	if len(all) == 0 {
		return map[string]any{"match_all": map[string]any{}}, nil
	}
	return map[string]any{"bool": map[string]any{"filter": all}}, nil
}

func esNegative(op Op) bool {
	return op == OpNe || op == OpNotIn || op == OpNotLike || op == OpNotContain
}

func esNot(q any) any { return map[string]any{"bool": map[string]any{"must_not": []any{q}}} }

func esAny(qs []any) any {
	return map[string]any{"bool": map[string]any{"should": qs, "minimum_should_match": 1}}
}

func esExists(n string, f esField) any {
	q := map[string]any{"exists": map[string]any{"field": n}}
	if f.typ == "nested" {
		return map[string]any{"nested": map[string]any{"path": n, "query": map[string]any{"match_all": map[string]any{}}}}
	}
	return q
}

func esCondition(f Filter, fields map[string]esField) (any, error) {
	if f.Column == AnyColumn {
		// Fields that can't hold the value don't match it; that is no error.
		var qs []any
		for _, n := range append([]string{"_id"}, esNames(fields)...) {
			pos := f
			pos.Column = n
			switch f.Op {
			case OpNe:
				pos.Op = OpEq
			case OpNotIn:
				pos.Op = OpIn
			case OpNotLike:
				pos.Op = OpLike
			case OpNotContain:
				pos.Op = OpContains
			}
			if n == "_id" && pos.Op == OpContains {
				pos.Op = OpEq
			}
			if n != "_id" && !esTakes(fields[n].typ, pos) {
				continue
			}
			if q, err := esPositive(pos, fields); err == nil {
				qs = append(qs, q)
			}
		}
		if len(qs) == 0 {
			return nil, errors.New("no field can be searched for that")
		}
		if esNegative(f.Op) {
			return esNot(esAny(qs)), nil
		}
		return esAny(qs), nil
	}
	if f.Column == "" {
		return nil, errors.New("filter column is empty")
	}
	if _, ok := fields[f.Column]; !ok && f.Column != "_id" {
		return nil, fmt.Errorf("%s isn't in the mapping, so it can't be filtered by", f.Column)
	}
	if !esNegative(f.Op) {
		return esPositive(f, fields)
	}
	pos := f
	pos.Op = map[Op]Op{OpNe: OpEq, OpNotIn: OpIn, OpNotLike: OpLike, OpNotContain: OpContains}[f.Op]
	q, err := esPositive(pos, fields)
	if err != nil {
		return nil, err
	}
	if f.Column == "_id" {
		return esNot(q), nil
	}
	// As in SQL, a missing value is neither equal nor different.
	return map[string]any{"bool": map[string]any{"filter": []any{esExists(f.Column, fields[f.Column])}, "must_not": []any{q}}}, nil
}

var esNumber = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][+-]?\d+)?$`)

func esIsNumeric(typ string) bool {
	switch typ {
	case "long", "integer", "short", "byte", "unsigned_long", "double", "float", "half_float", "scaled_float":
		return true
	}
	return false
}

func esIsText(typ string) bool {
	switch typ {
	case "keyword", "constant_keyword", "wildcard", "text", "match_only_text", "search_as_you_type", "annotated_text", "version":
		return true
	}
	return false
}

// esTakes: whether a field of this type could hold the value searched for.
func esTakes(typ string, f Filter) bool {
	switch {
	case f.Op == OpIsNull || f.Op == OpIsNotNull:
		return true
	case esIsText(typ):
		return true
	case esIsNumeric(typ):
		return esNumber.MatchString(strings.TrimSpace(f.Value))
	case typ == "boolean":
		return f.Value == "true" || f.Value == "false"
	}
	return false
}

func esWildcard(path, pattern string, ci bool) any {
	w := map[string]any{"value": pattern}
	if ci {
		w["case_insensitive"] = true
	}
	return map[string]any{"wildcard": map[string]any{path: w}}
}

var esEscape = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`)

// esPositive builds a condition that says what must match.
func esPositive(f Filter, fields map[string]esField) (any, error) {
	if f.Column == "_id" {
		switch f.Op {
		case OpEq:
			return map[string]any{"ids": map[string]any{"values": []string{f.Value}}}, nil
		case OpIn:
			return map[string]any{"ids": map[string]any{"values": splitList(f.Value)}}, nil
		case OpIsNotNull:
			return map[string]any{"match_all": map[string]any{}}, nil
		case OpIsNull:
			return esNot(map[string]any{"match_all": map[string]any{}}), nil
		}
		return nil, errors.New("the server compares _id only as a whole: use =, <>, IN or NOT IN")
	}
	fd := fields[f.Column]
	switch f.Op {
	case OpIsNotNull:
		return esExists(f.Column, fd), nil
	case OpIsNull:
		return esNot(esExists(f.Column, fd)), nil
	}
	if fd.typ == "object" || fd.typ == "nested" {
		return nil, fmt.Errorf("%s holds objects: filter their fields with a query, e.g. %s.name:value", f.Column, f.Column)
	}
	path := fd.exact
	words := path == "" // analysed text only
	one := func(v string) any {
		if words {
			return map[string]any{"match_phrase": map[string]any{f.Column: v}}
		}
		return map[string]any{"term": map[string]any{path: map[string]any{"value": v}}}
	}
	switch f.Op {
	case OpEq:
		return one(f.Value), nil
	case OpIn:
		items := splitList(f.Value)
		if len(items) == 0 {
			return nil, fmt.Errorf("%s needs at least one value", f.Op)
		}
		if !words {
			return map[string]any{"terms": map[string]any{path: items}}, nil
		}
		var qs []any
		for _, it := range items {
			qs = append(qs, one(it))
		}
		return esAny(qs), nil
	case OpLt, OpGt, OpLe, OpGe, OpBetween:
		if words {
			return nil, fmt.Errorf("a %s field has no order: %s has no keyword sub-field", fd.typ, f.Column)
		}
		r := map[string]any{}
		switch f.Op {
		case OpLt:
			r["lt"] = f.Value
		case OpGt:
			r["gt"] = f.Value
		case OpLe:
			r["lte"] = f.Value
		case OpGe:
			r["gte"] = f.Value
		default:
			r["gte"], r["lte"] = f.Value, f.Value2
		}
		return map[string]any{"range": map[string]any{path: r}}, nil
	}
	if !esIsText(fd.typ) {
		if f.Op == OpContains || f.Op == OpBeginsWith || f.Op == OpEndsWith {
			if esTakes(fd.typ, f) {
				return one(f.Value), nil // a number is found whole
			}
		}
		return nil, fmt.Errorf("a %s field can't be searched for part of a value: use =, <, > or BETWEEN", fd.typ)
	}
	if words {
		if f.Op == OpContains || f.Op == OpBeginsWith {
			return map[string]any{"match_phrase_prefix": map[string]any{f.Column: f.Value}}, nil
		}
		return nil, fmt.Errorf("%s is analysed text without a keyword sub-field: it can be searched by words (Contains, =)", f.Column)
	}
	switch f.Op {
	case OpContains:
		return esWildcard(path, "*"+esEscape.Replace(f.Value)+"*", true), nil
	case OpBeginsWith:
		return esWildcard(path, esEscape.Replace(f.Value)+"*", true), nil
	case OpEndsWith:
		return esWildcard(path, "*"+esEscape.Replace(f.Value), true), nil
	case OpLike:
		return esWildcard(path, strings.NewReplacer("%", "*", "_", "?").Replace(esEscape.Replace(f.Value)), false), nil
	}
	return nil, fmt.Errorf("unknown operator %q", f.Op)
}

// ---------------------------------------------------------------- editing

// esCoerce reads a cell's text as the JSON its field takes. now is what the
// field holds: a list or an object stays one.
func esCoerce(typ string, now json.RawMessage, v Value) (json.RawMessage, error) {
	if v.Null {
		return json.RawMessage("null"), nil
	}
	s := strings.TrimSpace(v.Text)
	quoted := func() json.RawMessage { b, _ := json.Marshal(v.Text); return b }
	now = bytes.TrimSpace(now)
	isJSON := strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{")
	if len(now) > 0 && (now[0] == '[' || now[0] == '{') {
		if !isJSON || !json.Valid([]byte(s)) {
			return nil, errors.New("this field holds a JSON list or object: enter one")
		}
		return json.RawMessage(s), nil
	}
	switch {
	case typ == "long" || typ == "integer" || typ == "short" || typ == "byte" || typ == "unsigned_long":
		if !regexp.MustCompile(`^-?\d+$`).MatchString(s) && !(isJSON && json.Valid([]byte(s))) {
			return nil, fmt.Errorf("%q isn't a whole number", v.Text)
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return json.RawMessage(strconv.FormatInt(n, 10)), nil // 007 isn't JSON
		}
		return json.RawMessage(s), nil
	case esIsNumeric(typ):
		if !json.Valid([]byte(s)) || !esNumber.MatchString(s) && !isJSON {
			return nil, fmt.Errorf("%q isn't a number", v.Text)
		}
		return json.RawMessage(s), nil
	case typ == "boolean":
		switch strings.ToLower(s) {
		case "true", "1", "yes":
			return json.RawMessage("true"), nil
		case "false", "0", "no":
			return json.RawMessage("false"), nil
		}
		return nil, fmt.Errorf("%q isn't true or false", v.Text)
	case typ == "object" || typ == "nested" || typ == "flattened":
		if !isJSON || !json.Valid([]byte(s)) {
			return nil, fmt.Errorf("a %s field takes a JSON object", typ)
		}
		return json.RawMessage(s), nil
	case typ == "date" || typ == "date_nanos":
		if len(now) > 0 && now[0] != '"' && regexp.MustCompile(`^-?\d+$`).MatchString(s) {
			return json.RawMessage(s), nil // epoch milliseconds, as it was
		}
		return quoted(), nil
	case esIsText(typ) || typ == "ip" || typ == "binary":
		return quoted(), nil
	case typ == "": // not in the mapping: as it was
		if len(now) > 0 && now[0] != '"' && json.Valid([]byte(s)) {
			return json.RawMessage(s), nil
		}
		return quoted(), nil
	}
	if json.Valid([]byte(s)) { // geo_point, ranges, vectors…
		return json.RawMessage(s), nil
	}
	return quoted(), nil
}

// refresh makes recent writes findable. A user without the right to goes on
// without: the server refreshes by itself every second or so.
func (e *elastic) refresh(ctx context.Context, name string) error {
	err := e.call(ctx, http.MethodPost, esPath(name)+"/_refresh", nil, nil)
	if esStatus(err) == http.StatusForbidden {
		return nil
	}
	return err
}

type esOp struct {
	preview string
	lines   []any // of the bulk request
}

// plan reads the documents to change, and turns edits into bulk actions.
func (e *elastic) plan(ctx context.Context, t TableRef, changes []Change) ([]esOp, error) {
	fields, err := e.fields(ctx, t.Name)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, ch := range changes {
		if ch.Kind == ChangeInsert {
			continue
		}
		id, ok := ch.Key["_id"]
		if !ok || id.Null {
			return nil, errors.New("the document's _id is missing")
		}
		ids = append(ids, id.Text)
	}
	docs := map[string]*esHit{}
	if len(ids) > 0 {
		// What was written a moment ago must be found.
		if err := e.refresh(ctx, t.Name); err != nil {
			return nil, err
		}
	}
	for part := range slices.Chunk(ids, esBatch) {
		res, err := e.search(ctx, t.Name, "", map[string]any{"query": map[string]any{"ids": map[string]any{"values": part}},
			"seq_no_primary_term": true, "size": len(part)})
		if err != nil {
			return nil, err
		}
		for i := range res.Hits.Hits {
			h := &res.Hits.Hits[i]
			if docs[h.ID] != nil {
				return nil, fmt.Errorf("more than one document has the _id %s (custom routing): edit it in the query editor", h.ID)
			}
			docs[h.ID] = h
		}
	}
	at := "/" + t.Name + "/_doc/"
	var out []esOp
	for _, ch := range changes {
		if ch.Kind == ChangeInsert {
			doc := map[string]json.RawMessage{}
			action := map[string]any{}
			for _, n := range sortedKeys(ch.Values) {
				v := ch.Values[n]
				if n == "_id" {
					if !v.Null && v.Text != "" {
						action["_id"] = v.Text
					}
					continue
				}
				if v.Null {
					continue
				}
				if doc[n], err = esCoerce(fields[n].typ, nil, v); err != nil {
					return nil, fmt.Errorf("%s: %w", n, err)
				}
			}
			op := esOp{preview: "POST " + at[:len(at)-1] + " " + esCompact(doc), lines: []any{map[string]any{"create": action}, doc}}
			if id, ok := action["_id"]; ok {
				op.preview = fmt.Sprintf("PUT /%s/_create/%s %s", t.Name, id, esCompact(doc))
			}
			out = append(out, op)
			continue
		}
		id := ch.Key["_id"].Text
		h := docs[id]
		if h == nil {
			return nil, fmt.Errorf("%s: %w", id, errMongoRowGone)
		}
		action := map[string]any{"_id": id, "if_seq_no": h.SeqNo, "if_primary_term": h.Term}
		if h.Routing != "" {
			action["routing"] = h.Routing
		}
		if ch.Kind == ChangeDelete {
			out = append(out, esOp{preview: "DELETE " + at + id, lines: []any{map[string]any{"delete": action}}})
			continue
		}
		src, err := h.fields()
		if err != nil {
			return nil, err
		}
		set := map[string]json.RawMessage{}
		for _, n := range sortedKeys(ch.Values) {
			if n == "_id" {
				return nil, errors.New("a document's _id can't change: add a new document and delete this one")
			}
			if set[n], err = esCoerce(fields[n].typ, src[n], ch.Values[n]); err != nil {
				return nil, fmt.Errorf("%s: %w", n, err)
			}
		}
		if len(set) == 0 {
			return nil, errors.New("update without values")
		}
		// A script, not a partial document: that would merge an object into
		// the old one rather than replace it.
		script := map[string]any{"script": map[string]any{"source": "for (def e : params.set.entrySet()) { ctx._source[e.getKey()] = e.getValue() }", "params": map[string]any{"set": set}}}
		out = append(out, esOp{preview: "POST /" + t.Name + "/_update/" + id + " — set " + esCompact(set), lines: []any{map[string]any{"update": action}, script}})
	}
	return out, nil
}

func (e *elastic) previewChanges(ctx context.Context, _ *Conn, t TableRef, changes []Change) ([]string, error) {
	ops, err := e.plan(ctx, t, changes)
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = op.preview
	}
	return out, err
}

func (e *elastic) applyChanges(ctx context.Context, _ *Conn, t TableRef, changes []Change) error {
	ops, err := e.plan(ctx, t, changes)
	if err != nil {
		return err
	}
	done := 0
	for part := range slices.Chunk(ops, esBatch) {
		w := esBulk{e: e, index: t.Name}
		for _, op := range part {
			w.add(op.lines...)
		}
		n, failed, err := w.send(ctx, "true")
		if err != nil {
			if done += n; failed >= 0 {
				err = fmt.Errorf("%s: %w", part[failed].preview, err)
			}
			if len(ops) > 1 {
				return fmt.Errorf("%w (%d of %d changes were saved: %s has no transactions)", err, done, len(ops), e.product)
			}
			return err
		}
		done += n
	}
	return nil
}

// esBulk collects the lines of a _bulk request.
type esBulk struct {
	e     *elastic
	index string
	buf   bytes.Buffer
	n     int // lines' actions
}

func (w *esBulk) add(lines ...any) {
	for _, l := range lines {
		if raw, ok := l.(json.RawMessage); ok {
			json.Compact(&w.buf, raw)
		} else {
			w.buf.WriteString(esCompact(l))
		}
		w.buf.WriteByte('\n')
	}
	w.n++
}

// send writes what was collected: how many actions worked, and the first
// that didn't (-1 if the request itself failed).
func (w *esBulk) send(ctx context.Context, refresh string) (done, failed int, err error) {
	if w.n == 0 {
		return 0, -1, nil
	}
	defer func() { w.buf.Reset(); w.n = 0 }()
	res, err := w.e.do(ctx, http.MethodPost, esPath(w.index)+"/_bulk?refresh="+refresh, w.buf.Bytes())
	if err != nil {
		return 0, -1, err
	}
	defer res.Body.Close()
	return esBulkAnswer(res.Body)
}

func esBulkAnswer(r io.Reader) (done, failed int, err error) {
	var out struct {
		Items []map[string]struct {
			Status int             `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"items"`
	}
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return 0, -1, fmt.Errorf("the server's answer couldn't be read, so some changes may be saved: %w", err)
	}
	failed = -1
	for i, it := range out.Items {
		for action, x := range it {
			if x.Status < 300 {
				done++
				continue
			}
			if err != nil {
				continue
			}
			failed = i
			switch {
			case x.Status == http.StatusConflict && action == "create":
				err = errors.New("a document with that _id already exists")
			case x.Status == http.StatusConflict:
				err = errors.New("the document was changed by someone else in the meantime")
			case x.Status == http.StatusNotFound && len(x.Error) == 0:
				err = errMongoRowGone
			default:
				err = esReadError(x.Status, []byte(`{"error":`+string(x.Error)+`}`))
			}
		}
	}
	return done, failed, err
}

// ---------------------------------------------------------------- structure

var errESDesigner = errors.New("an index is made in the query editor: PUT /name with its mappings")

func (e *elastic) createTable(context.Context, *Conn, string, NewTable) ([]string, error) {
	return nil, errESDesigner
}

func (e *elastic) alter(ctx context.Context, _ *Conn, t TableRef, ops []AlterOp, dryRun bool) ([]string, error) {
	for _, op := range ops {
		if op.Kind != AlterTruncate {
			return nil, fmt.Errorf("GoTable can't %s here: change the mapping in the query editor, PUT /%s/_mapping", strings.ReplaceAll(string(op.Kind), "_", " "), t.Name)
		}
	}
	stmts := []string{"POST /" + t.Name + `/_delete_by_query?refresh=true {"query": {"match_all": {}}}`}
	if dryRun {
		return stmts, nil
	}
	return stmts, e.empty(ctx, t.Name)
}

func (e *elastic) empty(ctx context.Context, name string) error {
	var out struct {
		Failures []json.RawMessage `json:"failures"`
	}
	if err := e.refresh(ctx, name); err != nil {
		return err
	}
	err := e.call(ctx, http.MethodPost, esPath(name)+"/_delete_by_query?refresh=true", map[string]any{"query": map[string]any{"match_all": map[string]any{}}}, &out)
	if err == nil && len(out.Failures) > 0 {
		err = fmt.Errorf("%d documents couldn't be deleted: %s", len(out.Failures), snippet(string(out.Failures[0])))
	}
	return err
}

func buildESDrop(objs []Object) ([]string, error) {
	out := make([]string, len(objs))
	for i, o := range objs {
		if o.Kind == KindView {
			return nil, fmt.Errorf("%s is an alias or a data stream: remove it in the query editor (POST /_aliases, DELETE /_data_stream/%s)", o.Name, o.Name)
		}
		out[i] = "DELETE /" + o.Name
	}
	return out, nil
}

func (e *elastic) drop(ctx context.Context, _ *Conn, objs []Object) ([]string, error) {
	stmts, err := buildESDrop(objs)
	if err != nil {
		return nil, err
	}
	for i, o := range objs {
		if err := e.call(ctx, http.MethodDelete, esPath(o.Name), nil, nil); err != nil {
			return nil, fmt.Errorf("%s: %w (%d indices before it were deleted)", o.Name, err, i)
		}
	}
	return stmts, nil
}

// ---------------------------------------------------------------- export and import

// dump writes requests that make the indices and their documents again.
func (e *elastic) dump(ctx context.Context, _ *Conn, w io.Writer, o DumpOptions, progress func(DumpProgress)) error {
	p := DumpProgress{Tables: len(o.Objects)}
	out := &sqlWriter{w: w}
	out.printf("# GoTable dump of %s %s, %s\n# Requests as in Kibana's Dev Tools; GoTable's Import runs them.\n\n", e.product, e.version, time.Now().UTC().Format(time.RFC3339))
	for i, ob := range o.Objects {
		p.Table, p.TableIndex = ob.Name, i
		if progress != nil {
			progress(p)
		}
		path := "/" + url.PathEscape(ob.Name)
		if ob.Kind == KindView {
			out.printf("# %s is an alias or a data stream, which a dump leaves out (an index brings its aliases).\n\n", ob.Name)
			continue
		}
		if o.DropFirst {
			out.printf("DELETE %s?ignore_unavailable=true\n\n", path)
		}
		if o.Structure {
			def, err := e.create(ctx, ob.Name)
			if err != nil {
				return fmt.Errorf("%s: %w", ob.Name, err)
			}
			out.printf("PUT %s\n%s\n\n", path, esPretty(def))
		}
		if !o.Data {
			continue
		}
		n := 0
		err := e.scroll(ctx, ob.Name, map[string]any{"query": map[string]any{"match_all": map[string]any{}}}, func(h esHit) (bool, error) {
			if n%500 == 0 {
				out.printf("POST %s/_bulk\n", path)
			}
			n++
			action := map[string]any{"_id": h.ID}
			if h.Routing != "" {
				action["routing"] = h.Routing
			}
			var src bytes.Buffer
			if err := json.Compact(&src, h.Raw); err != nil {
				return false, fmt.Errorf("document %s has no source to export", h.ID)
			}
			out.printf("%s\n%s\n", esCompact(map[string]any{"index": action}), src.String())
			p.Rows++
			if progress != nil && p.Rows%1000 == 0 {
				progress(p)
			}
			return true, out.err
		})
		if err != nil {
			return fmt.Errorf("%s: %w", ob.Name, err)
		}
		out.printf("POST %s/_refresh\n\n", path)
	}
	if progress != nil {
		progress(p)
	}
	return out.err
}

func (e *elastic) importCSV(ctx context.Context, _ *Conn, r io.Reader, t TableRef, o CSVImportOptions, progress func(int64)) (int64, error) {
	fields, err := e.fields(ctx, t.Name)
	if err != nil {
		return 0, err
	}
	if o.Truncate {
		if err := e.empty(ctx, t.Name); err != nil {
			return 0, err
		}
	}
	var done int64
	w := esBulk{e: e, index: t.Name}
	first := 0
	lines := []int{}
	flush := func(refresh string) error {
		n, failed, err := w.send(ctx, refresh)
		done += int64(n)
		if err != nil {
			if failed >= 0 && failed < len(lines) {
				first = lines[failed]
			}
			return fmt.Errorf("line %d: %w (%d rows were imported)", first, err, done)
		}
		lines = lines[:0]
		if progress != nil {
			progress(done)
		}
		return nil
	}
	err = readCSV(r, o, func(line int, rec []string) error {
		doc, action := map[string]json.RawMessage{}, map[string]any{}
		for i, col := range o.Columns {
			if o.Mapping[i] < 0 {
				continue
			}
			f := rec[o.Mapping[i]]
			if o.EmptyAsNull && f == "" || o.NullText != "" && f == o.NullText {
				continue
			}
			if col == "_id" {
				if f != "" {
					action["_id"] = f
				}
				continue
			}
			v, err := esCoerce(fields[col].typ, nil, Text(f))
			if err != nil {
				return fmt.Errorf("line %d, %s: %w (%d rows were imported)", line, col, err, done)
			}
			doc[col] = v
		}
		w.add(map[string]any{"index": action}, doc)
		lines = append(lines, line)
		if w.n >= esBatch {
			return flush("false")
		}
		return nil
	})
	if err == nil {
		err = flush("true")
	}
	if rerr := e.refresh(context.WithoutCancel(ctx), t.Name); err == nil {
		err = rerr
	}
	return done, err
}
