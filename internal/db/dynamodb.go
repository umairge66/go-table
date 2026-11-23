package db

// DynamoDB: an AWS web service rather than a database protocol.
//
//   - The schema is the region. Items have no fixed attributes, so a
//     table's columns are its keys, then what a sample of items holds.
//   - Scan has no offset and filters after reading, so a page is read by
//     skipping items, and the filter bar and sorts are applied here over at
//     most dynMaxScan items. A raw condition runs on the server as PartiQL.
//     Reads of tables on AWS cost money; large tables show AWS's own
//     (approximate) count instead of scanning for one.
//   - A commit is one TransactWriteItems (per 100 changes). Its conditions
//     catch an item deleted meanwhile, or a new key that exists; another
//     edit of the same attribute is overwritten, as on the SQL engines.
//   - The editor runs PartiQL, and three statements of GoTable's own for
//     what PartiQL can't say: CREATE TABLE, DROP TABLE and PUT.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dyn "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"

	"gotable/internal/model"
)

const (
	dynMaxScan = 100_000 // items read to filter or sort in GoTable
	dynSample  = 500     // items read to learn a table's attributes
	dynCount   = 100_000 // above this, AWS's approximate count is shown
	dynTxItems = 100     // TransactWriteItems' limit
)

type dynState struct {
	cl      *dynamodb.Client
	region  string
	local   bool // an endpoint of its own: DynamoDB Local, LocalStack
	closed  bool
	samples map[string]dynSampled
}

type dynSampled struct {
	at    time.Time
	attrs []string          // not the keys, by name
	types map[string]string // the type most items have
}

type (
	dynEngine struct{}
	dynItem   = map[string]dyn.AttributeValue
)

var (
	errDynNoColumns  = errors.New("DynamoDB items have no fixed columns: add the attribute to an item")
	errDynNoDesigner = errors.New(`a DynamoDB table is made in the query editor: CREATE TABLE "name" ("id" S PARTITION KEY)`)
)

func openDynamo(ctx context.Context, cfg *model.Connection, addr, password string, tunnel io.Closer) (*Conn, error) {
	fail := func(err error) (*Conn, error) {
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, err
	}
	st := &dynState{region: cfg.DBName, local: cfg.Host != "", samples: map[string]dynSampled{}}
	if st.region == "" {
		st.region = "us-east-1" // a local endpoint takes any
	}
	var opts []func(*awsconfig.LoadOptions) error
	opts = append(opts, awsconfig.WithRegion(st.region))
	o, err := url.ParseQuery(cfg.Options)
	if err != nil {
		return fail(fmt.Errorf("options: %w", err))
	}
	switch {
	case cfg.User != "":
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.User, password, "")))
	case st.local:
		// Never sign requests to another endpoint with the machine's AWS keys.
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")))
	case o.Get("profile") != "":
		opts = append(opts, awsconfig.WithSharedConfigProfile(o.Get("profile")))
	}
	if st.local {
		t, err := mongoTLS(cfg, tunnel != nil)
		if err != nil {
			return fail(err)
		}
		if t != nil {
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.TLSClientConfig = t
			opts = append(opts, awsconfig.WithHTTPClient(&http.Client{Transport: tr}))
		}
	}
	ac, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return fail(err)
	}
	st.cl = dynamodb.NewFromConfig(ac, func(o *dynamodb.Options) {
		if st.local {
			scheme := "http://"
			if cfg.SSLMode == "require" || cfg.SSLMode == "skip-verify" {
				scheme = "https://"
			}
			o.BaseEndpoint = aws.String(scheme + addr)
			o.RetryMaxAttempts = 2
		}
	})
	if _, err := st.cl.ListTables(ctx, &dynamodb.ListTablesInput{Limit: aws.Int32(1)}); err != nil {
		return fail(dynErr(err))
	}
	return &Conn{Config: cfg, Dialect: DialectFor(cfg.Driver), eng: dynEngine{}, tunnel: tunnel, dyn: st, schema: st.region}, nil
}

// closeDyn is Close for DynamoDB (HTTP: nothing stays open but a tunnel).
func (c *Conn) closeDyn() error {
	if c.dyn.closed {
		return nil
	}
	c.dyn.closed = true
	if c.tunnel != nil {
		return c.tunnel.Close()
	}
	return nil
}

// dynErr drops the SDK's request trail: "ValidationException: message".
func dynErr(err error) error {
	var tc *dyn.TransactionCanceledException
	if err == nil || errors.As(err, &tc) {
		return err
	}
	var ae smithy.APIError
	if errors.As(err, &ae) && ae.ErrorMessage() != "" {
		return fmt.Errorf("%s: %s", ae.ErrorCode(), ae.ErrorMessage())
	}
	return err
}

// ---------------------------------------------------------------- engine

func (dynEngine) serverVersion(_ context.Context, c *Conn) (string, error) {
	if c.dyn.local {
		return "DynamoDB (local endpoint)", nil
	}
	return "DynamoDB · " + c.dyn.region, nil
}

func (dynEngine) databases(context.Context, *Conn) ([]string, error) { return []string{}, nil }

func (dynEngine) schemas(_ context.Context, c *Conn) ([]string, error) {
	return []string{c.dyn.region}, nil
}

func (dynEngine) currentSchema(_ context.Context, c *Conn) (string, error) { return c.dyn.region, nil }

func (dynEngine) useSchema(_ context.Context, c *Conn, schema string) error {
	if schema != c.dyn.region {
		return fmt.Errorf("this connection is to %s: make another for %s", c.dyn.region, schema)
	}
	return nil
}

func (dynEngine) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	out := []Object{}
	p := dynamodb.NewListTablesPaginator(c.dyn.cl, &dynamodb.ListTablesInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, dynErr(err)
		}
		for _, n := range page.TableNames {
			out = append(out, Object{Schema: schema, Name: n, Kind: KindTable})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// dynTable is a table's description with its keys in order.
type dynTable struct {
	*dyn.TableDescription
	keys  []string
	types map[string]string // of the keys and index keys: S, N or B
}

func (c *Conn) dynDescribe(ctx context.Context, name string) (*dynTable, error) {
	d, err := c.dyn.cl.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: &name})
	if err != nil {
		return nil, dynErr(err)
	}
	t := &dynTable{TableDescription: d.Table, types: map[string]string{}}
	t.keys = dynKeyNames(d.Table.KeySchema)
	for _, a := range d.Table.AttributeDefinitions {
		t.types[aws.ToString(a.AttributeName)] = string(a.AttributeType)
	}
	return t, nil
}

func dynKeyNames(ks []dyn.KeySchemaElement) []string {
	var out []string
	for _, kt := range []dyn.KeyType{dyn.KeyTypeHash, dyn.KeyTypeRange} {
		for _, k := range ks {
			if k.KeyType == kt {
				out = append(out, aws.ToString(k.AttributeName))
			}
		}
	}
	return out
}

// dynSampleOf learns a table's attributes from its first items.
func (c *Conn) dynSampleOf(ctx context.Context, t *dynTable) (dynSampled, error) {
	name := aws.ToString(t.TableName)
	if s, ok := c.dyn.samples[name]; ok && time.Since(s.at) < 20*time.Second {
		return s, nil
	}
	s := dynSampled{at: time.Now(), types: map[string]string{}}
	out, err := c.dyn.cl.Scan(ctx, &dynamodb.ScanInput{TableName: &name, Limit: aws.Int32(dynSample)})
	if err != nil {
		return s, dynErr(err)
	}
	counts := map[string]map[string]int{}
	for _, it := range out.Items {
		for a, v := range it {
			if counts[a] == nil {
				counts[a] = map[string]int{}
			}
			counts[a][dynType(v)]++
		}
	}
	for a, by := range counts {
		best := ""
		for ty, n := range by {
			if ty != "NULL" && (best == "" || n > by[best] || n == by[best] && ty < best) {
				best = ty
			}
		}
		s.types[a] = best
		if _, key := t.types[a]; !key || !contains(t.keys, a) {
			s.attrs = append(s.attrs, a)
		}
	}
	sort.Strings(s.attrs)
	c.dyn.samples[name] = s
	return s, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (dynEngine) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	d, err := c.dynDescribe(ctx, t.Name)
	if err != nil {
		return nil, err
	}
	s, err := c.dynSampleOf(ctx, d)
	if err != nil {
		return nil, err
	}
	var out []Column
	for i, k := range d.keys {
		out = append(out, Column{Name: k, Type: d.types[k], PrimaryKey: true, Comment: []string{"partition key", "sort key"}[min(i, 1)]})
	}
	for _, a := range s.attrs {
		out = append(out, Column{Name: a, Type: s.types[a], Nullable: true})
	}
	return out, nil
}

func (dynEngine) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	d, err := c.dynDescribe(ctx, t.Name)
	if err != nil {
		return nil, err
	}
	out := []Index{{Name: "PRIMARY KEY", Columns: d.keys, Unique: true, Primary: true, Constraint: true}}
	for _, g := range d.GlobalSecondaryIndexes {
		out = append(out, Index{Name: aws.ToString(g.IndexName), Columns: dynKeyNames(g.KeySchema), Type: "global"})
	}
	for _, l := range d.LocalSecondaryIndexes {
		out = append(out, Index{Name: aws.ToString(l.IndexName), Columns: dynKeyNames(l.KeySchema), Type: "local"})
	}
	return out, nil
}

func (dynEngine) foreignKeys(context.Context, *Conn, TableRef) ([]ForeignRef, error) { return nil, nil }

// definition: CREATE TABLE … WITH the JSON the AWS CLI's create-table takes.
func (dynEngine) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	d, err := c.dynDescribe(ctx, o.Name)
	if err != nil {
		return "", err
	}
	in := map[string]any{"AttributeDefinitions": d.AttributeDefinitions, "KeySchema": d.KeySchema}
	onDemand := d.BillingModeSummary != nil && d.BillingModeSummary.BillingMode == dyn.BillingModePayPerRequest
	through := func(p *dyn.ProvisionedThroughputDescription) map[string]any {
		if p == nil {
			return nil
		}
		return map[string]any{"ReadCapacityUnits": aws.ToInt64(p.ReadCapacityUnits), "WriteCapacityUnits": aws.ToInt64(p.WriteCapacityUnits)}
	}
	if onDemand {
		in["BillingMode"] = "PAY_PER_REQUEST"
	} else if tp := through(d.ProvisionedThroughput); tp != nil {
		in["ProvisionedThroughput"] = tp
	}
	var gs, ls []map[string]any
	for _, g := range d.GlobalSecondaryIndexes {
		m := map[string]any{"IndexName": aws.ToString(g.IndexName), "KeySchema": g.KeySchema, "Projection": g.Projection}
		if tp := through(g.ProvisionedThroughput); tp != nil && !onDemand {
			m["ProvisionedThroughput"] = tp
		}
		gs = append(gs, m)
	}
	for _, l := range d.LocalSecondaryIndexes {
		ls = append(ls, map[string]any{"IndexName": aws.ToString(l.IndexName), "KeySchema": l.KeySchema, "Projection": l.Projection})
	}
	if gs != nil {
		in["GlobalSecondaryIndexes"] = gs
	}
	if ls != nil {
		in["LocalSecondaryIndexes"] = ls
	}
	b, err := json.MarshalIndent(in, "", "  ")
	return "CREATE TABLE " + dynQuote(o.Name) + " WITH " + string(b) + ";", err
}

func (dynEngine) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	d, err := c.dynDescribe(ctx, t.Name)
	if err != nil {
		return TableInfo{}, err
	}
	parts := []string{"provisioned capacity"}
	if d.BillingModeSummary != nil && d.BillingModeSummary.BillingMode == dyn.BillingModePayPerRequest {
		parts[0] = "on-demand capacity"
	}
	parts = append(parts, fmt.Sprintf("about %d items, %s (AWS updates this every few hours)", aws.ToInt64(d.ItemCount), bytesText(aws.ToInt64(d.TableSizeBytes))))
	return TableInfo{Options: strings.Join(parts, " · ")}, nil
}

// ---------------------------------------------------------------- values

func dynType(v dyn.AttributeValue) string {
	switch v.(type) {
	case *dyn.AttributeValueMemberS:
		return "S"
	case *dyn.AttributeValueMemberN:
		return "N"
	case *dyn.AttributeValueMemberB:
		return "B"
	case *dyn.AttributeValueMemberBOOL:
		return "BOOL"
	case *dyn.AttributeValueMemberNULL:
		return "NULL"
	case *dyn.AttributeValueMemberL:
		return "L"
	case *dyn.AttributeValueMemberM:
		return "M"
	case *dyn.AttributeValueMemberSS:
		return "SS"
	case *dyn.AttributeValueMemberNS:
		return "NS"
	case *dyn.AttributeValueMemberBS:
		return "BS"
	}
	return ""
}

// dynValue shows an attribute: scalars plain, the rest as JSON, where
// what JSON lacks is {"$ss": […]}, {"$ns": […]}, {"$bs": […]}, {"$b": base64}.
func dynValue(v dyn.AttributeValue) Value {
	switch x := v.(type) {
	case nil, *dyn.AttributeValueMemberNULL:
		return NullValue
	case *dyn.AttributeValueMemberS:
		return Text(x.Value)
	case *dyn.AttributeValueMemberN:
		return Text(x.Value)
	case *dyn.AttributeValueMemberBOOL:
		return Text(strconv.FormatBool(x.Value))
	case *dyn.AttributeValueMemberB:
		return Value{Text: "0x" + hex.EncodeToString(x.Value), Hex: true}
	}
	return Text(dynJSON(v))
}

func dynJSON(v dyn.AttributeValue) string {
	var b bytes.Buffer
	dynWriteJSON(&b, v)
	return b.String()
}

func dynWriteJSON(b *bytes.Buffer, v dyn.AttributeValue) {
	str := func(s string) {
		var tmp bytes.Buffer
		e := json.NewEncoder(&tmp)
		e.SetEscapeHTML(false)
		e.Encode(s)
		b.Write(bytes.TrimSpace(tmp.Bytes()))
	}
	list := func(n int, open, end string, at func(i int)) {
		b.WriteString(open)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			at(i)
		}
		b.WriteString(end)
	}
	switch x := v.(type) {
	case *dyn.AttributeValueMemberS:
		str(x.Value)
	case *dyn.AttributeValueMemberN:
		b.WriteString(x.Value)
	case *dyn.AttributeValueMemberBOOL:
		b.WriteString(strconv.FormatBool(x.Value))
	case *dyn.AttributeValueMemberB:
		b.WriteString(`{"$b":"` + base64.StdEncoding.EncodeToString(x.Value) + `"}`)
	case *dyn.AttributeValueMemberSS:
		vs := append([]string{}, x.Value...)
		sort.Strings(vs)
		list(len(vs), `{"$ss":[`, "]}", func(i int) { str(vs[i]) })
	case *dyn.AttributeValueMemberNS:
		vs := append([]string{}, x.Value...)
		sort.Strings(vs)
		list(len(vs), `{"$ns":[`, "]}", func(i int) { b.WriteString(vs[i]) })
	case *dyn.AttributeValueMemberBS:
		list(len(x.Value), `{"$bs":[`, "]}", func(i int) { str(base64.StdEncoding.EncodeToString(x.Value[i])) })
	case *dyn.AttributeValueMemberL:
		list(len(x.Value), "[", "]", func(i int) { dynWriteJSON(b, x.Value[i]) })
	case *dyn.AttributeValueMemberM:
		keys := make([]string, 0, len(x.Value))
		for k := range x.Value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		list(len(keys), "{", "}", func(i int) { str(keys[i]); b.WriteByte(':'); dynWriteJSON(b, x.Value[keys[i]]) })
	default:
		b.WriteString("null")
	}
}

func dynParseJSON(text string) (dyn.AttributeValue, error) {
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber() // DynamoDB numbers have 38 digits
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("not JSON: there is text after the value")
	}
	return dynFromJSON(v)
}

func dynFromJSON(v any) (dyn.AttributeValue, error) {
	switch x := v.(type) {
	case nil:
		return &dyn.AttributeValueMemberNULL{Value: true}, nil
	case string:
		return &dyn.AttributeValueMemberS{Value: x}, nil
	case json.Number:
		return &dyn.AttributeValueMemberN{Value: x.String()}, nil
	case bool:
		return &dyn.AttributeValueMemberBOOL{Value: x}, nil
	case []any:
		out := make([]dyn.AttributeValue, len(x))
		for i, e := range x {
			av, err := dynFromJSON(e)
			if err != nil {
				return nil, err
			}
			out[i] = av
		}
		return &dyn.AttributeValueMemberL{Value: out}, nil
	case map[string]any:
		if len(x) == 1 {
			for k, e := range x {
				if l, ok := e.([]any); ok && (k == "$ss" || k == "$ns" || k == "$bs") {
					return dynSet(strings.ToUpper(k[1:]), l)
				}
				if s, ok := e.(string); ok && k == "$b" {
					raw, err := base64.StdEncoding.DecodeString(s)
					return &dyn.AttributeValueMemberB{Value: raw}, err
				}
			}
		}
		out := make(dynItem, len(x))
		for k, e := range x {
			av, err := dynFromJSON(e)
			if err != nil {
				return nil, err
			}
			out[k] = av
		}
		return &dyn.AttributeValueMemberM{Value: out}, nil
	}
	return nil, fmt.Errorf("unexpected %T", v)
}

func dynSet(typ string, l []any) (dyn.AttributeValue, error) {
	if len(l) == 0 {
		return nil, errors.New("DynamoDB has no empty set: clear the attribute (NULL) instead")
	}
	var strs []string
	var bins [][]byte
	for _, e := range l {
		switch x := e.(type) {
		case string:
			if typ == "BS" {
				raw, err := base64.StdEncoding.DecodeString(x)
				if err != nil {
					return nil, fmt.Errorf("a binary set holds base64: %w", err)
				}
				bins = append(bins, raw)
				continue
			}
			if typ == "SS" {
				strs = append(strs, x)
				continue
			}
		case json.Number:
			if typ == "NS" {
				strs = append(strs, x.String())
				continue
			}
		}
		return nil, fmt.Errorf("%v doesn't belong in a set of type %s", e, typ)
	}
	switch typ {
	case "SS":
		return &dyn.AttributeValueMemberSS{Value: strs}, nil
	case "NS":
		return &dyn.AttributeValueMemberNS{Value: strs}, nil
	}
	return &dyn.AttributeValueMemberBS{Value: bins}, nil
}

var dynNumber = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

// dynCoerce reads text typed into a cell as the type the attribute has.
// Without one, JSON (a number, true, a list…) is read as JSON, the rest is
// a string.
func dynCoerce(typ string, v Value) (dyn.AttributeValue, error) {
	s := strings.TrimSpace(v.Text)
	switch typ {
	case "S":
		return &dyn.AttributeValueMemberS{Value: v.Text}, nil
	case "N":
		if !dynNumber.MatchString(s) {
			return nil, fmt.Errorf("%q isn't a number, which this attribute holds", v.Text)
		}
		return &dyn.AttributeValueMemberN{Value: s}, nil
	case "BOOL":
		switch strings.ToLower(s) {
		case "true", "false":
			return &dyn.AttributeValueMemberBOOL{Value: strings.EqualFold(s, "true")}, nil
		}
		return nil, fmt.Errorf("%q isn't true or false, which this attribute holds", v.Text)
	case "B":
		if strings.HasPrefix(s, "0x") {
			if raw, err := hex.DecodeString(s[2:]); err == nil {
				return &dyn.AttributeValueMemberB{Value: raw}, nil
			}
		}
		return nil, fmt.Errorf("binary is written 0x…, not %q", v.Text)
	case "L", "M", "SS", "NS", "BS":
		av, err := dynParseJSON(s)
		if err != nil {
			return nil, err
		}
		if got := dynType(av); got != typ {
			return nil, fmt.Errorf("this attribute holds %s, and that is %s (sets are written {\"$ss\": […]}, {\"$ns\": […]})", typ, got)
		}
		return av, nil
	}
	if av, err := dynParseJSON(s); err == nil && dynType(av) != "NULL" {
		return av, nil
	}
	return &dyn.AttributeValueMemberS{Value: v.Text}, nil
}

func dynQuote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// ---------------------------------------------------------------- reading

// dynEach reads a table's items, or those matching a PartiQL condition.
// fn returns false to stop.
func (c *Conn) dynEach(ctx context.Context, name, where string, fn func(dynItem) (bool, error)) error {
	if where = strings.TrimSpace(where); where != "" {
		return c.dynPartiQL(ctx, "SELECT * FROM "+dynQuote(name)+" WHERE "+where, fn)
	}
	p := dynamodb.NewScanPaginator(c.dyn.cl, &dynamodb.ScanInput{TableName: &name, ConsistentRead: aws.Bool(true)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return dynErr(err)
		}
		for _, it := range page.Items {
			if more, err := fn(it); err != nil || !more {
				return err
			}
		}
	}
	return nil
}

func (c *Conn) dynPartiQL(ctx context.Context, stmt string, fn func(dynItem) (bool, error)) error {
	var next *string
	for {
		out, err := c.dyn.cl.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{Statement: &stmt, NextToken: next})
		if err != nil {
			return dynErr(err)
		}
		for _, it := range out.Items {
			if more, err := fn(it); err != nil || !more {
				return err
			}
		}
		if next = out.NextToken; next == nil {
			return nil
		}
	}
}

// dynTabulate lays items out under first, then any other attribute by name.
func dynTabulate(items []dynItem, first []string) ([]string, []string, [][]Value) {
	cols := append([]string{}, first...)
	seen := map[string]bool{}
	for _, c := range cols {
		seen[c] = true
	}
	var more []string
	types := map[string]string{}
	for _, it := range items {
		for a, v := range it {
			if !seen[a] {
				seen[a] = true
				more = append(more, a)
			}
			if types[a] == "" || types[a] == "NULL" {
				types[a] = dynType(v)
			}
		}
	}
	sort.Strings(more)
	cols = append(cols, more...)
	ts := make([]string, len(cols))
	for i, col := range cols {
		ts[i] = types[col]
	}
	rows := make([][]Value, len(items))
	for i, it := range items {
		rows[i] = dynRow(it, cols)
	}
	return cols, ts, rows
}

func dynRow(it dynItem, cols []string) []Value {
	row := make([]Value, len(cols))
	for j, col := range cols {
		row[j] = dynValue(it[col])
	}
	return row
}

// dynRead reads a page of a table (all: every row), and how many rows the
// filters match, or -1 when none are set. Callers hold c.mu.
func (c *Conn) dynRead(ctx context.Context, t TableRef, p Page, all bool) ([]string, []string, [][]Value, int64, error) {
	d, err := c.dynDescribe(ctx, t.Name)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	s, err := c.dynSampleOf(ctx, d)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	first := append(append([]string{}, d.keys...), s.attrs...)
	local := len(p.Filters) > 0 || len(p.Sort) > 0
	var items []dynItem
	n := 0
	err = c.dynEach(ctx, t.Name, p.RawWhere, func(it dynItem) (bool, error) {
		if local {
			if len(items) >= dynMaxScan {
				return false, fmt.Errorf("the table has more than %d items: too many to filter or sort here; narrow it with a PartiQL condition on its key", dynMaxScan)
			}
			items = append(items, it)
			return true, nil
		}
		n++
		if !all && n <= p.Offset {
			return true, nil
		}
		items = append(items, it)
		return all || p.Limit <= 0 || len(items) < p.Limit, nil
	})
	if err != nil {
		return nil, nil, nil, 0, err
	}
	cols, types, rows := dynTabulate(items, first)
	if !local {
		return cols, types, rows, -1, nil
	}
	if rows, err = filterRows(cols, rows, p.Filters); err != nil {
		return nil, nil, nil, 0, err
	}
	if err := sortRows(cols, rows, p.Sort); err != nil {
		return nil, nil, nil, 0, err
	}
	total := int64(len(rows))
	if !all {
		rows = pageOf(rows, p)
	}
	return cols, types, rows, total, nil
}

func (c *Conn) dynFetchRows(ctx context.Context, t TableRef, p Page) (*ResultSet, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	cols, types, rows, _, err := c.dynRead(ctx, t, p, false)
	if err != nil {
		return nil, err
	}
	return &ResultSet{Columns: cols, Types: types, Rows: rows}, nil
}

func (c *Conn) dynCountRows(ctx context.Context, t TableRef, p Page) (int64, error) {
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	if len(p.Filters) > 0 {
		_, _, _, n, err := c.dynRead(ctx, t, Page{Filters: p.Filters, RawWhere: p.RawWhere}, false)
		return n, err
	}
	var n int64
	if strings.TrimSpace(p.RawWhere) != "" {
		err := c.dynEach(ctx, t.Name, p.RawWhere, func(dynItem) (bool, error) { n++; return true, nil })
		return n, err
	}
	d, err := c.dynDescribe(ctx, t.Name)
	if err != nil {
		return 0, err
	}
	if about := aws.ToInt64(d.ItemCount); about > dynCount && !c.dyn.local {
		return about, nil
	}
	pg := dynamodb.NewScanPaginator(c.dyn.cl, &dynamodb.ScanInput{TableName: &t.Name, Select: dyn.SelectCount, ConsistentRead: aws.Bool(true)})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return 0, dynErr(err)
		}
		n += int64(page.Count)
	}
	return n, nil
}

func (c *Conn) dynStreamRows(ctx context.Context, t TableRef, p Page, fn func(cols, types []string, row []Value) error) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	if len(p.Filters) > 0 || len(p.Sort) > 0 || p.Limit > 0 {
		cols, types, rows, _, err := c.dynRead(ctx, t, p, p.Limit == 0)
		for i := 0; err == nil && i < len(rows); i++ {
			err = fn(cols, types, rows[i])
		}
		return err
	}
	// Twice through: an export's columns come first, and any item may have
	// one the sample didn't.
	d, err := c.dynDescribe(ctx, t.Name)
	if err != nil {
		return err
	}
	seen := map[string]string{}
	if err := c.dynEach(ctx, t.Name, p.RawWhere, func(it dynItem) (bool, error) {
		for a, v := range it {
			if seen[a] == "" {
				seen[a] = dynType(v)
			}
		}
		return true, nil
	}); err != nil {
		return err
	}
	cols := append([]string{}, d.keys...)
	var more []string
	for a := range seen {
		if !contains(d.keys, a) {
			more = append(more, a)
		}
	}
	sort.Strings(more)
	cols = append(cols, more...)
	types := make([]string, len(cols))
	for i, col := range cols {
		types[i] = seen[col]
	}
	return c.dynEach(ctx, t.Name, p.RawWhere, func(it dynItem) (bool, error) { return true, fn(cols, types, dynRow(it, cols)) })
}

// ---------------------------------------------------------------- editing

type dynOp struct {
	preview string
	insert  bool
	item    dyn.TransactWriteItem
}

// dynPlan turns grid edits into transaction items. Callers hold c.mu.
func (c *Conn) dynPlan(ctx context.Context, t TableRef, changes []Change) ([]dynOp, error) {
	d, err := c.dynDescribe(ctx, t.Name)
	if err != nil {
		return nil, err
	}
	s, err := c.dynSampleOf(ctx, d)
	if err != nil {
		return nil, err
	}
	keyOf := func(m map[string]Value) (dynItem, error) {
		key := dynItem{}
		for _, k := range d.keys {
			v, ok := m[k]
			if !ok || v.Null {
				return nil, fmt.Errorf("the key %s is missing", k)
			}
			av, err := dynCoerce(d.types[k], v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			key[k] = av
		}
		return key, nil
	}
	typeOf := func(a string, existing dynItem) string {
		if ty := d.types[a]; ty != "" {
			return ty
		}
		if v := existing[a]; v != nil && dynType(v) != "NULL" {
			return dynType(v)
		}
		return s.types[a]
	}
	names := map[string]string{"#pk": d.keys[0]}
	var out []dynOp
	for _, ch := range changes {
		var op dynOp
		switch ch.Kind {
		case ChangeInsert:
			item, err := keyOf(ch.Values)
			if err != nil {
				return nil, err
			}
			for _, a := range sortedKeys(ch.Values) {
				if contains(d.keys, a) || ch.Values[a].Null {
					continue
				}
				if item[a], err = dynCoerce(typeOf(a, nil), ch.Values[a]); err != nil {
					return nil, fmt.Errorf("%s: %w", a, err)
				}
			}
			op.insert = true
			op.item.Put = &dyn.Put{TableName: &t.Name, Item: item, ConditionExpression: aws.String("attribute_not_exists(#pk)"), ExpressionAttributeNames: names}
			op.preview = dynPut(t.Name, item) + " -- if it doesn't exist"
		case ChangeDelete:
			key, err := keyOf(ch.Key)
			if err != nil {
				return nil, err
			}
			op.item.Delete = &dyn.Delete{TableName: &t.Name, Key: key, ConditionExpression: aws.String("attribute_exists(#pk)"), ExpressionAttributeNames: names}
			op.preview = "DELETE FROM " + dynQuote(t.Name) + dynWhere(d.keys, key)
		case ChangeUpdate:
			key, err := keyOf(ch.Key)
			if err != nil {
				return nil, err
			}
			got, err := c.dyn.cl.GetItem(ctx, &dynamodb.GetItemInput{TableName: &t.Name, Key: key, ConsistentRead: aws.Bool(true)})
			if err != nil {
				return nil, dynErr(err)
			}
			if got.Item == nil {
				return nil, errMongoRowGone
			}
			n := map[string]string{"#pk": d.keys[0]}
			vals := dynItem{}
			var sets, removes, show []string
			for i, a := range sortedKeys(ch.Values) {
				if contains(d.keys, a) {
					return nil, fmt.Errorf("%s is part of the key, which can't change: add a new item and delete this one", a)
				}
				ref := "#a" + strconv.Itoa(i)
				n[ref] = a
				if ch.Values[a].Null {
					removes = append(removes, ref)
					show = append(show, "REMOVE "+dynQuote(a))
					continue
				}
				av, err := dynCoerce(typeOf(a, got.Item), ch.Values[a])
				if err != nil {
					return nil, fmt.Errorf("%s: %w", a, err)
				}
				vals[":v"+strconv.Itoa(i)] = av
				sets = append(sets, ref+" = :v"+strconv.Itoa(i))
				show = append(show, "SET "+dynQuote(a)+" = "+dynJSON(av))
			}
			expr := ""
			if len(sets) > 0 {
				expr = "SET " + strings.Join(sets, ", ")
			}
			if len(removes) > 0 {
				expr = strings.TrimSpace(expr + " REMOVE " + strings.Join(removes, ", "))
			}
			if expr == "" {
				return nil, errors.New("update without values")
			}
			up := &dyn.Update{TableName: &t.Name, Key: key, UpdateExpression: &expr, ConditionExpression: aws.String("attribute_exists(#pk)"), ExpressionAttributeNames: n}
			if len(vals) > 0 {
				up.ExpressionAttributeValues = vals
			}
			op.item.Update = up
			op.preview = "UPDATE " + dynQuote(t.Name) + " " + strings.Join(show, " ") + dynWhere(d.keys, key)
		}
		out = append(out, op)
	}
	return out, nil
}

func dynWhere(keys []string, key dynItem) string {
	var conds []string
	for _, k := range keys {
		conds = append(conds, dynQuote(k)+" = "+dynJSON(key[k]))
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

func (c *Conn) dynPreviewChanges(ctx context.Context, t TableRef, changes []Change) ([]string, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	ops, err := c.dynPlan(ctx, t, changes)
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = op.preview
	}
	return out, err
}

func (c *Conn) dynApplyChanges(ctx context.Context, t TableRef, changes []Change) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	ops, err := c.dynPlan(ctx, t, changes)
	if err != nil {
		return err
	}
	delete(c.dyn.samples, t.Name)
	for start := 0; start < len(ops); start += dynTxItems {
		batch := ops[start:min(start+dynTxItems, len(ops))]
		in := &dynamodb.TransactWriteItemsInput{}
		for _, op := range batch {
			in.TransactItems = append(in.TransactItems, op.item)
		}
		_, err := c.dyn.cl.TransactWriteItems(ctx, in)
		if err == nil {
			continue
		}
		err = dynErr(err)
		var tc *dyn.TransactionCanceledException
		if errors.As(err, &tc) {
			for i, r := range tc.CancellationReasons {
				code := aws.ToString(r.Code)
				if code == "" || code == "None" || i >= len(batch) {
					continue
				}
				switch {
				case code == "ConditionalCheckFailed" && batch[i].insert:
					err = errors.New("an item with that key already exists")
				case code == "ConditionalCheckFailed":
					err = errors.New("the item no longer exists")
				default:
					err = fmt.Errorf("%s: %s", code, aws.ToString(r.Message))
				}
				err = fmt.Errorf("%s: %w", batch[i].preview, err)
				break
			}
		}
		if start > 0 {
			return fmt.Errorf("%w (the first %d changes were applied: a DynamoDB transaction holds %d)", err, start, dynTxItems)
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------- the query editor

func (c *Conn) dynExecuteIn(ctx context.Context, script string, rowCap int) ([]Result, error) {
	spans := SplitStatements(c.Config.Driver, script)
	if len(spans) == 0 {
		return nil, fmt.Errorf("nothing to execute")
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	var results []Result
	for _, sp := range spans {
		r := c.dynRun(ctx, sp.SQL, rowCap)
		results = append(results, r)
		if r.Err != nil {
			break
		}
	}
	return results, nil
}

var (
	dynLeading  = regexp.MustCompile(`^(?:\s+|--[^\n]*(?:\n|$)|/\*(?s:.*?)\*/)+`)
	dynName     = `("(?:[^"]|"")+"|[\w.\-]+)`
	dynCreateRe = regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+` + dynName + `\s*(\(|WITH\b)(.*)$`)
	dynDropRe   = regexp.MustCompile(`(?is)^\s*DROP\s+TABLE\s+(IF\s+EXISTS\s+)?` + dynName + `\s*;?\s*$`)
	dynPutRe    = regexp.MustCompile(`(?is)^\s*PUT\s+` + dynName + `\s*(\{.*\})\s*;?\s*$`)
	dynKeyRe    = regexp.MustCompile(`(?is)^\s*` + dynName + `\s+(S|N|B)\s+(PARTITION|SORT)\s+KEY\s*$`)
)

func dynUnquote(s string) string {
	if strings.HasPrefix(s, `"`) {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

// dynRun runs PartiQL, or one of GoTable's own statements. Callers hold c.mu.
func (c *Conn) dynRun(ctx context.Context, stmt string, rowCap int) Result {
	r := Result{Statement: stmt}
	start := time.Now()
	defer func() { r.Duration = time.Since(start) }()
	stmt = dynLeading.ReplaceAllString(stmt, "") // comments before it
	if m := dynCreateRe.FindStringSubmatch(stmt); m != nil {
		r.Err = c.dynCreate(ctx, dynUnquote(m[1]), m[2], m[3])
		return r
	}
	if m := dynDropRe.FindStringSubmatch(stmt); m != nil {
		r.Err = c.dynDropTable(ctx, dynUnquote(m[2]), m[1] != "")
		return r
	}
	if m := dynPutRe.FindStringSubmatch(stmt); m != nil {
		name := dynUnquote(m[1])
		av, err := dynParseJSON(m[2])
		if err != nil {
			r.Err = err
			return r
		}
		item, ok := av.(*dyn.AttributeValueMemberM)
		if !ok {
			r.Err = errors.New("PUT takes an item: {\"id\": …}")
			return r
		}
		delete(c.dyn.samples, name)
		_, err = c.dyn.cl.PutItem(ctx, &dynamodb.PutItemInput{TableName: &name, Item: item.Value})
		r.Err = dynErr(err)
		if err == nil {
			r.RowsAffected = 1
		}
		return r
	}
	sql := strings.TrimSuffix(strings.TrimSpace(stmt), ";")
	var items []dynItem
	err := c.dynPartiQL(ctx, sql, func(it dynItem) (bool, error) {
		if rowCap > 0 && len(items) >= rowCap {
			r.Truncated = true
			return false, nil
		}
		items = append(items, it)
		return true, nil
	})
	if err != nil {
		r.Err = err
		return r
	}
	if f := strings.Fields(sql); len(f) > 0 && strings.EqualFold(f[0], "SELECT") || len(items) > 0 {
		cols, types, rows := dynTabulate(items, nil)
		r.Set = &ResultSet{Columns: cols, Types: types, Rows: rows}
	} else {
		clear(c.dyn.samples)
	}
	return r
}

// dynCreate: CREATE TABLE "t" ("id" S PARTITION KEY, "at" N SORT KEY), or
// WITH the JSON of the AWS CLI's create-table.
func (c *Conn) dynCreate(ctx context.Context, name, how, rest string) error {
	in := &dynamodb.CreateTableInput{}
	rest = strings.TrimSuffix(strings.TrimSpace(rest), ";")
	if how != "(" {
		if err := json.Unmarshal([]byte(rest), in); err != nil {
			return fmt.Errorf("CREATE TABLE … WITH takes JSON: %w", err)
		}
	} else {
		rest = strings.TrimSpace(rest)
		if !strings.HasSuffix(rest, ")") {
			return errors.New(`CREATE TABLE "name" ("id" S PARTITION KEY, "at" N SORT KEY)`)
		}
		for _, def := range strings.Split(strings.TrimSuffix(rest, ")"), ",") {
			m := dynKeyRe.FindStringSubmatch(def)
			if m == nil {
				return fmt.Errorf(`%q: a key is declared as "name" S PARTITION KEY or "name" N SORT KEY (types S, N, B); other attributes aren't declared`, strings.TrimSpace(def))
			}
			kt := dyn.KeyTypeHash
			if strings.EqualFold(m[3], "SORT") {
				kt = dyn.KeyTypeRange
			}
			attr := aws.String(dynUnquote(m[1]))
			in.AttributeDefinitions = append(in.AttributeDefinitions, dyn.AttributeDefinition{AttributeName: attr, AttributeType: dyn.ScalarAttributeType(strings.ToUpper(m[2]))})
			in.KeySchema = append(in.KeySchema, dyn.KeySchemaElement{AttributeName: attr, KeyType: kt})
		}
		in.BillingMode = dyn.BillingModePayPerRequest
	}
	in.TableName = &name
	if _, err := c.dyn.cl.CreateTable(ctx, in); err != nil {
		return dynErr(err)
	}
	return dynErr(dynamodb.NewTableExistsWaiter(c.dyn.cl, func(o *dynamodb.TableExistsWaiterOptions) { o.MinDelay = 200 * time.Millisecond }).
		Wait(ctx, &dynamodb.DescribeTableInput{TableName: &name}, 5*time.Minute))
}

func (c *Conn) dynDropTable(ctx context.Context, name string, ifExists bool) error {
	delete(c.dyn.samples, name)
	if _, err := c.dyn.cl.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: &name}); err != nil {
		var nf *dyn.ResourceNotFoundException
		if ifExists && errors.As(err, &nf) {
			return nil
		}
		return dynErr(err)
	}
	return dynErr(dynamodb.NewTableNotExistsWaiter(c.dyn.cl, func(o *dynamodb.TableNotExistsWaiterOptions) { o.MinDelay = 200 * time.Millisecond }).
		Wait(ctx, &dynamodb.DescribeTableInput{TableName: &name}, 5*time.Minute))
}

func (c *Conn) dynExec(ctx context.Context, stmt string) (int64, error) {
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	r := c.dynRun(ctx, stmt, 1)
	return r.RowsAffected, r.Err
}

// ---------------------------------------------------------------- structure

func buildDynDrop(objs []Object) []string {
	out := make([]string, len(objs))
	for i, o := range objs {
		out[i] = "DROP TABLE " + dynQuote(o.Name) + ";"
	}
	return out
}

func (c *Conn) dynDrop(ctx context.Context, objs []Object) ([]string, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	for i, o := range objs {
		if err := c.dynDropTable(ctx, o.Name, false); err != nil {
			return nil, fmt.Errorf("%s: %w (%d table(s) before it were deleted)", o.Name, err, i)
		}
	}
	return buildDynDrop(objs), nil
}

// dynAlter empties a table; the rest is AWS's to change.
func (c *Conn) dynAlter(ctx context.Context, t TableRef, ops []AlterOp, dryRun bool) ([]string, error) {
	for _, op := range ops {
		if op.Kind != AlterTruncate {
			return nil, fmt.Errorf("GoTable can't %s on DynamoDB: items have no fixed columns, and indexes are changed with the AWS console or CLI", strings.ReplaceAll(string(op.Kind), "_", " "))
		}
	}
	stmts := []string{"-- delete every item of " + dynQuote(t.Name) + ", in batches"}
	if dryRun {
		return stmts, nil
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	return stmts, c.dynEmpty(ctx, t.Name)
}

func (c *Conn) dynEmpty(ctx context.Context, name string) error {
	d, err := c.dynDescribe(ctx, name)
	if err != nil {
		return err
	}
	delete(c.dyn.samples, name)
	w := dynBatch{c: c, table: name}
	if err := c.dynEach(ctx, name, "", func(it dynItem) (bool, error) {
		key := dynItem{}
		for _, k := range d.keys {
			key[k] = it[k]
		}
		return true, w.add(ctx, dyn.WriteRequest{DeleteRequest: &dyn.DeleteRequest{Key: key}})
	}); err != nil {
		return err
	}
	return w.flush(ctx)
}

// dynBatch writes items 25 at a time, retrying what DynamoDB defers.
type dynBatch struct {
	c     *Conn
	table string
	reqs  []dyn.WriteRequest
	done  int64
}

func (w *dynBatch) add(ctx context.Context, r dyn.WriteRequest) error {
	if w.reqs = append(w.reqs, r); len(w.reqs) == 25 {
		return w.flush(ctx)
	}
	return nil
}

func (w *dynBatch) flush(ctx context.Context) error {
	for wait := 100 * time.Millisecond; len(w.reqs) > 0; wait = min(wait*2, 5*time.Second) {
		out, err := w.c.dyn.cl.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: map[string][]dyn.WriteRequest{w.table: w.reqs}})
		if err != nil {
			return dynErr(err)
		}
		left := out.UnprocessedItems[w.table]
		w.done += int64(len(w.reqs) - len(left))
		if w.reqs = left; len(left) > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	w.reqs = nil
	return nil
}

// ---------------------------------------------------------------- export and import

// dynPut writes an item as a PUT statement. Inside its JSON, quotes,
// semicolons and dashes that a statement splitter could trip over are
// \u escapes.
func dynPut(table string, it dynItem) string {
	js := dynJSON(&dyn.AttributeValueMemberM{Value: it})
	const escape = `\` + `u%04x`
	var b strings.Builder
	in := false
	for i := 0; i < len(js); i++ {
		ch := js[i]
		switch {
		case in && ch == '\\' && (js[i+1] == '"' || js[i+1] == '\\'):
			fmt.Fprintf(&b, escape, js[i+1])
			i++
		case ch == '"':
			in = !in
			b.WriteByte(ch)
		case in && (strings.IndexByte(";'`$", ch) >= 0 || ch == '-' && js[i+1] == '-'):
			fmt.Fprintf(&b, escape, ch)
		default:
			b.WriteByte(ch)
		}
	}
	return "PUT " + dynQuote(table) + " " + b.String() + ";"
}

func (c *Conn) dynDump(ctx context.Context, w io.Writer, o DumpOptions, progress func(DumpProgress)) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.mu.Unlock()
	p := DumpProgress{Tables: len(o.Objects)}
	fmt.Fprintf(w, "-- GoTable dump of DynamoDB, %s\n-- CREATE TABLE … WITH, DROP TABLE and PUT are read by GoTable (Import script).\n\n", time.Now().UTC().Format(time.RFC3339))
	for i, ob := range o.Objects {
		p.Table, p.TableIndex = ob.Name, i
		if o.DropFirst {
			fmt.Fprintf(w, "DROP TABLE IF EXISTS %s;\n", dynQuote(ob.Name))
		}
		if o.Structure {
			def, err := (dynEngine{}).definition(ctx, c, ob)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "%s\n\n", def)
		}
		if !o.Data {
			continue
		}
		if err := c.dynEach(ctx, ob.Name, "", func(it dynItem) (bool, error) {
			p.Rows++
			if progress != nil && p.Rows%1000 == 0 {
				progress(p)
			}
			_, err := fmt.Fprintln(w, dynPut(ob.Name, it))
			return true, err
		}); err != nil {
			return err
		}
		fmt.Fprintln(w)
	}
	if progress != nil {
		progress(p)
	}
	return nil
}

func (c *Conn) dynImportScript(ctx context.Context, r io.Reader, o ImportOptions, progress func(*ImportResult)) (*ImportResult, error) {
	if o.MaxErrors <= 0 {
		o.MaxErrors = 200
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	text := string(data)
	res := &ImportResult{Errors: []ImportError{}}
	if err := c.lock(ctx); err != nil {
		return res, err
	}
	defer c.mu.Unlock()
	for _, sp := range SplitStatements(c.Config.Driver, text) {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		rr := c.dynRun(ctx, sp.SQL, 1)
		res.Statements++
		res.Rows += rr.RowsAffected
		if rr.Err != nil {
			res.Failed++
			line := strings.Count(text[:sp.Start], "\n") + 1
			if len(res.Errors) < o.MaxErrors {
				res.Errors = append(res.Errors, ImportError{Line: line, Statement: snippet(sp.SQL), Message: rr.Err.Error()})
			}
			if !o.ContinueOnError {
				return res, fmt.Errorf("line %d: %w", line, rr.Err)
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

func (c *Conn) dynImportCSV(ctx context.Context, r io.Reader, t TableRef, o CSVImportOptions, progress func(int64)) (int64, error) {
	if err := c.lock(ctx); err != nil {
		return 0, err
	}
	defer c.mu.Unlock()
	d, err := c.dynDescribe(ctx, t.Name)
	if err != nil {
		return 0, err
	}
	s, err := c.dynSampleOf(ctx, d)
	if err != nil {
		return 0, err
	}
	if o.Truncate {
		if err := c.dynEmpty(ctx, t.Name); err != nil {
			return 0, err
		}
	}
	defer delete(c.dyn.samples, t.Name)
	w := dynBatch{c: c, table: t.Name}
	pending := map[string]bool{} // a batch can't hold one key twice
	err = readCSV(r, o, func(line int, rec []string) error {
		it := dynItem{}
		id := ""
		for i, col := range o.Columns {
			if o.Mapping[i] < 0 {
				continue
			}
			f := rec[o.Mapping[i]]
			if o.EmptyAsNull && f == "" || o.NullText != "" && f == o.NullText {
				continue // no such attribute
			}
			typ := d.types[col]
			if typ == "" {
				typ = s.types[col]
			}
			av, err := dynCoerce(typ, Text(f))
			if err != nil {
				return fmt.Errorf("line %d, %s: %w (%d rows were imported)", line, col, err, w.done)
			}
			it[col] = av
		}
		for _, k := range d.keys {
			if it[k] == nil {
				return fmt.Errorf("line %d: the key %s is empty (%d rows were imported)", line, k, w.done)
			}
			id += dynJSON(it[k]) + "\x00"
		}
		if pending[id] {
			if err := w.flush(ctx); err != nil {
				return err
			}
		}
		if len(w.reqs) == 0 {
			clear(pending)
		}
		pending[id] = true
		if err := w.add(ctx, dyn.WriteRequest{PutRequest: &dyn.PutRequest{Item: it}}); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		if progress != nil && len(w.reqs) == 0 {
			progress(w.done)
		}
		return nil
	})
	if err == nil {
		err = w.flush(ctx)
	}
	return w.done, err
}
