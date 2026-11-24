package db

// BigQuery, Google's warehouse, over its REST API.
//
//   - A dataset is a schema; a project is a database.
//   - A query is billed for the bytes of the columns it reads, whatever its
//     LIMIT, and 10 MB at least. So tables are browsed, counted and exported
//     through tabledata and their metadata, which are free, and structure
//     comes from the same; a filtered or sorted table, or a view, is a query.
//     Each query has a limit of bytes, past which BigQuery refuses it unbilled.
//   - Keys are kept, not enforced. Dropping a table drops other tables' keys
//     to it, so GoTable refuses that.
//   - A table takes five changes of structure in ten seconds: they are
//     tried again.
//   - Rows aren't edited in the grid (that is DML, which needs a project with
//     billing and hasn't been tested). CSV is loaded by a load job, which
//     is free and all or nothing.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"

	"gotable/internal/model"
)

var bqAPI = "https://bigquery.googleapis.com" // a variable for tests

const (
	bqDefaultGB = 10     // of a query, unless the connection says otherwise
	bqPage      = 10_000 // rows of a tabledata page (and 10 MB)
	bqStatement = 800_000
)

type bigquery struct {
	http     *http.Client
	project  string
	limit    int64 // bytes of a query; 0: none
	location map[string]string
	tables   map[TableRef]bqCached
	// The rows of what was last shown as a page, counted on the way.
	total   int64
	totalOf string
	totalAt time.Time
}

// forget drops what is known of tables: something may have changed them.
func (x *bigquery) forget() {
	clear(x.tables)
	x.totalOf = ""
}

// counted reports whether a page's rows were just counted.
func (x *bigquery) counted(t TableRef, p Page) bool {
	return x.totalOf == bqPageKey(t, p) && time.Since(x.totalAt) < 10*time.Second
}

func (x *bigquery) count(t TableRef, p Page, n int64) {
	x.total, x.totalOf, x.totalAt = n, bqPageKey(t, p), time.Now()
}

type bqCached struct {
	t  *bqTable
	at time.Time
}

var errBQEdit = errors.New("rows can't be edited here on BigQuery yet: change them with INSERT, UPDATE and DELETE in the query editor (the project needs billing)")

// openBigQuery signs in with a credentials file: a service account's key, or
// what “gcloud auth application-default login” writes.
func openBigQuery(ctx context.Context, cfg *model.Connection) (*Conn, error) {
	ts, project, err := bqCredentials(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	if cfg.DBName != "" {
		project = cfg.DBName
	}
	if project == "" {
		return nil, errors.New("the project is required: the credentials don't name one")
	}
	x := &bigquery{http: &http.Client{Transport: &oauth2.Transport{Source: ts}}, project: project, location: map[string]string{}, tables: map[TableRef]bqCached{}}
	if x.limit, err = bqLimit(cfg.Options); err != nil {
		return nil, err
	}
	c := &Conn{Config: cfg, Dialect: DialectFor(cfg.Driver), eng: x, remote: x}
	sets, err := x.schemas(ctx, c)
	if err != nil {
		return nil, err
	}
	if slices.Contains(sets, cfg.SelectedSchema) {
		c.schema = cfg.SelectedSchema
	} else if len(sets) > 0 {
		c.schema = sets[0]
	}
	return c, nil
}

// bqLimit reads the most a query may read, in GB, from the options (limit=10;
// 0 for none).
func bqLimit(options string) (int64, error) {
	q, err := url.ParseQuery(options)
	if err != nil {
		return 0, fmt.Errorf("options: %w", err)
	}
	gb := float64(bqDefaultGB)
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		if gb, err = strconv.ParseFloat(v, 64); err != nil || gb < 0 || gb > 1e7 {
			return 0, fmt.Errorf("the limit of a query is a number of GB, not %q", v)
		}
	}
	return int64(gb * (1 << 30)), nil
}

func bqCredentials(path string) (oauth2.TokenSource, string, error) {
	path = strings.TrimSpace(path)
	given := path != ""
	home, _ := os.UserHomeDir()
	switch {
	case strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`):
		path = filepath.Join(home, path[2:])
	case path == "":
		path = os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	}
	if path == "" {
		path = filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
		if runtime.GOOS == "windows" {
			path = filepath.Join(os.Getenv("APPDATA"), "gcloud", "application_default_credentials.json")
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !given && errors.Is(err, os.ErrNotExist) {
			return nil, "", errors.New("choose a service account's key file, or sign in with: gcloud auth application-default login")
		}
		return nil, "", fmt.Errorf("the credentials file: %w", err)
	}
	var k struct {
		Type         string `json:"type"`
		ProjectID    string `json:"project_id"`
		KeyID        string `json:"private_key_id"`
		Key          string `json:"private_key"`
		Email        string `json:"client_email"`
		TokenURI     string `json:"token_uri"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
		QuotaProject string `json:"quota_project_id"`
	}
	if json.Unmarshal(b, &k) != nil {
		return nil, "", errors.New("the credentials file isn't the JSON file that Google Cloud makes")
	}
	if k.TokenURI == "" {
		k.TokenURI = "https://oauth2.googleapis.com/token"
	}
	// Tokens are fetched for as long as the connection is open.
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: 30 * time.Second})
	switch k.Type {
	case "service_account":
		j := &jwt.Config{Email: k.Email, PrivateKey: []byte(k.Key), PrivateKeyID: k.KeyID, TokenURL: k.TokenURI, Scopes: []string{"https://www.googleapis.com/auth/bigquery"}}
		return j.TokenSource(ctx), k.ProjectID, nil
	case "authorized_user":
		o := &oauth2.Config{ClientID: k.ClientID, ClientSecret: k.ClientSecret, Endpoint: oauth2.Endpoint{TokenURL: k.TokenURI}}
		return o.TokenSource(ctx, &oauth2.Token{RefreshToken: k.RefreshToken}), k.QuotaProject, nil
	}
	return nil, "", fmt.Errorf("credentials of type %q aren't supported: use a service account's key, or gcloud auth application-default login", k.Type)
}

// ---------------------------------------------------------------- requests

type bqError struct {
	Code    int
	Reason  string
	Message string
}

func (e *bqError) Error() string { return e.Message }

type bqProblem struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func (x *bigquery) url(path string, q url.Values) string {
	u := bqAPI + "/bigquery/v2/projects/" + url.PathEscape(x.project) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (x *bigquery) call(ctx context.Context, method, path string, q url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, x.url(path, q), body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return x.do(req, out)
}

func (x *bigquery) do(req *http.Request, out any) error {
	res, err := x.http.Do(req)
	if err != nil {
		if e := req.Context().Err(); e != nil {
			return e
		}
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			msg := re.ErrorDescription
			if msg == "" {
				msg = re.ErrorCode
			}
			return fmt.Errorf("authentication failed: Google refused the credentials (%s)", msg)
		}
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string      `json:"message"`
				Errors  []bqProblem `json:"errors"`
			} `json:"error"`
		}
		json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&e)
		be := &bqError{Code: res.StatusCode, Message: e.Error.Message}
		if len(e.Error.Errors) > 0 {
			be.Reason = e.Error.Errors[0].Reason
		}
		if be.Message == "" {
			be.Message = "BigQuery answered " + res.Status
		}
		if res.StatusCode == http.StatusUnauthorized {
			be.Message = "authentication failed: " + be.Message
		}
		return be
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(bufio.NewReaderSize(res.Body, 1<<16))
	if err := dec.Decode(out); err != nil {
		if e := req.Context().Err(); e != nil {
			return e
		}
		return fmt.Errorf("BigQuery's answer couldn't be read: %w", err)
	}
	return nil
}

type bqField struct {
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Mode        string    `json:"mode"`
	Description string    `json:"description"`
	Fields      []bqField `json:"fields"`
	MaxLength   string    `json:"maxLength"`
	Precision   string    `json:"precision"`
	Scale       string    `json:"scale"`
	Collation   string    `json:"collation"`
	Default     string    `json:"defaultValueExpression"`
	Range       struct {
		Type string `json:"type"`
	} `json:"rangeElementType"`
}

type bqSchema struct {
	Fields []bqField `json:"fields"`
}

var bqNames = map[string]string{"INTEGER": "INT64", "FLOAT": "FLOAT64", "BOOLEAN": "BOOL", "RECORD": "STRUCT"}

// sqlType is a field's type as SQL writes it.
func (f bqField) sqlType() string {
	t := f.Type
	if n, ok := bqNames[t]; ok {
		t = n
	}
	switch {
	case t == "STRUCT":
		parts := make([]string, len(f.Fields))
		for i, s := range f.Fields {
			parts[i] = bqIdent(s.Name) + " " + s.sqlType()
		}
		t += "<" + strings.Join(parts, ", ") + ">"
	case t == "RANGE":
		t += "<" + f.Range.Type + ">"
	case f.MaxLength != "":
		t += "(" + f.MaxLength + ")"
	case f.Precision != "" && f.Scale != "":
		t += "(" + f.Precision + ", " + f.Scale + ")"
	case f.Precision != "":
		t += "(" + f.Precision + ")"
	}
	if f.Mode == "REPEATED" {
		return "ARRAY<" + t + ">"
	}
	return t
}

var bqPlainName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// bqIdent quotes a name in a type only where needed.
func bqIdent(n string) string {
	if bqPlainName.MatchString(n) {
		return n
	}
	return "`" + n + "`"
}

type bqRef struct {
	Project string `json:"projectId,omitempty"`
	Dataset string `json:"datasetId,omitempty"`
	Table   string `json:"tableId,omitempty"`
}

type bqTable struct {
	Ref         bqRef    `json:"tableReference"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Schema      bqSchema `json:"schema"`
	NumRows     string   `json:"numRows"`
	NumBytes    string   `json:"numBytes"`
	Location    string   `json:"location"`
	Expires     string   `json:"expirationTime"`
	View        struct {
		Query string `json:"query"`
	} `json:"view"`
	MView struct {
		Query string `json:"query"`
	} `json:"materializedView"`
	Partition *struct {
		Type  string `json:"type"`
		Field string `json:"field"`
	} `json:"timePartitioning"`
	RangePartition *struct {
		Field string `json:"field"`
		Range struct{ Start, End, Interval string }
	} `json:"rangePartitioning"`
	RequireFilter bool `json:"requirePartitionFilter"`
	Clustering    *struct {
		Fields []string `json:"fields"`
	} `json:"clustering"`
	Constraints struct {
		PrimaryKey struct {
			Columns []string `json:"columns"`
		} `json:"primaryKey"`
		ForeignKeys []struct {
			Name    string `json:"name"`
			Table   bqRef  `json:"referencedTable"`
			Columns []struct {
				From string `json:"referencingColumn"`
				To   string `json:"referencedColumn"`
			} `json:"columnReferences"`
		} `json:"foreignKeys"`
	} `json:"tableConstraints"`
}

// stored reports rows that tabledata can list.
func (t *bqTable) stored() bool {
	return t.Type == "TABLE" || t.Type == "SNAPSHOT" || t.Type == "CLONE"
}

func (x *bigquery) table(ctx context.Context, t TableRef) (*bqTable, error) {
	var out bqTable
	if err := x.call(ctx, http.MethodGet, "/datasets/"+url.PathEscape(t.Schema)+"/tables/"+url.PathEscape(t.Name), nil, nil, &out); err != nil {
		return nil, err
	}
	x.tables[t] = bqCached{&out, time.Now()}
	return &out, nil
}

// recent is table for callers that can live with a few seconds' age: opening
// a table asks for its columns, keys and info one after another.
func (x *bigquery) recent(ctx context.Context, t TableRef) (*bqTable, error) {
	if c, ok := x.tables[t]; ok && time.Since(c.at) < 3*time.Second {
		return c.t, nil
	}
	return x.table(ctx, t)
}

// place is where a dataset is: its jobs run there.
func (x *bigquery) place(ctx context.Context, dataset string) string {
	if dataset == "" {
		return ""
	}
	if l, ok := x.location[dataset]; ok {
		return l
	}
	var d struct {
		Location string `json:"location"`
	}
	if x.call(ctx, http.MethodGet, "/datasets/"+url.PathEscape(dataset), nil, nil, &d) != nil {
		return ""
	}
	x.location[dataset] = d.Location
	return d.Location
}

// ---------------------------------------------------------------- jobs

type bqJobRef struct {
	Project  string `json:"projectId,omitempty"`
	ID       string `json:"jobId,omitempty"`
	Location string `json:"location,omitempty"`
}

type bqJob struct {
	Ref    bqJobRef `json:"jobReference"`
	Status struct {
		State string      `json:"state"`
		Error *bqProblem  `json:"errorResult"`
		All   []bqProblem `json:"errors"`
	} `json:"status"`
	Config struct {
		Query struct {
			Query string `json:"query"`
		} `json:"query"`
	} `json:"configuration"`
	Statistics struct {
		Created string `json:"creationTime"`
		Bytes   string `json:"totalBytesProcessed"`
		Script  struct {
			Kind string `json:"evaluationKind"`
		} `json:"scriptStatistics"`
		Query struct {
			Type  string   `json:"statementType"`
			Bytes string   `json:"totalBytesProcessed"`
			DML   string   `json:"numDmlAffectedRows"`
			Kids  string   `json:"numChildJobs"`
			Sch   bqSchema `json:"schema"`
		} `json:"query"`
		Load struct {
			Rows string `json:"outputRows"`
		} `json:"load"`
	} `json:"statistics"`
}

type bqResults struct {
	Schema   bqSchema    `json:"schema"`
	Rows     []bqRow     `json:"rows"`
	Total    string      `json:"totalRows"`
	Token    string      `json:"pageToken"`
	Complete bool        `json:"jobComplete"`
	DML      string      `json:"numDmlAffectedRows"`
	Job      bqJobRef    `json:"jobReference"`
	Errors   []bqProblem `json:"errors"`
}

type bqRow struct {
	F []struct {
		V json.RawMessage `json:"v"`
	} `json:"f"`
}

// bqParam is a query parameter, always text: SQL casts it.
func bqParam(v string) map[string]any {
	return map[string]any{"parameterType": map[string]string{"type": "STRING"}, "parameterValue": map[string]string{"value": v}}
}

func bqInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// start starts a query, trying again while the table's changes come too fast.
func (x *bigquery) start(ctx context.Context, dataset, sql string, args []string, dry bool) (*bqJob, error) {
	qualified := bqQualifyView(sql, dataset)
	q := map[string]any{"query": qualified, "useLegacySql": false}
	if dataset != "" && qualified == sql && !bqView.MatchString(stripLeadingComments(sql)) {
		q["defaultDataset"] = bqRef{x.project, dataset, ""}
	}
	if x.limit > 0 && !dry {
		q["maximumBytesBilled"] = strconv.FormatInt(max(x.limit, 10<<20), 10)
	}
	if len(args) > 0 {
		ps := make([]any, len(args))
		for i, a := range args {
			ps[i] = bqParam(a)
		}
		q["parameterMode"], q["queryParameters"] = "POSITIONAL", ps
	}
	conf := map[string]any{"query": q}
	if dry {
		conf["dryRun"] = true
	}
	// An ID of our own: a job can be cancelled whenever we give up on it.
	ref := bqJobRef{x.project, "gotable_" + strings.ToLower(model.NewID()), x.place(ctx, dataset)}
	var job bqJob
	err := x.call(ctx, http.MethodPost, "/jobs", nil, map[string]any{"jobReference": ref, "configuration": conf}, &job)
	if err != nil && ctx.Err() != nil && !dry {
		x.cancel(ref)
	}
	if err == nil && job.Status.Error != nil {
		err = bqJobError(job.Status.Error)
	}
	return &job, x.explain(err)
}

var (
	bqView     = regexp.MustCompile(`(?i)^CREATE\s+(OR\s+REPLACE\s+)?(MATERIALIZED\s+)?VIEW\s`)
	bqViewName = regexp.MustCompile("(?i)^(CREATE\\s+(?:OR\\s+REPLACE\\s+)?(?:MATERIALIZED\\s+)?VIEW\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?)(`[^`.]+`|[A-Za-z_][A-Za-z0-9_-]*)(\\s*[^.\\s`A-Za-z0-9_-]|\\s+[^.\\s])")
)

// bqQualifyView names the dataset of a view that is made without one. A
// view's own query goes without a default dataset: BigQuery would save its
// tables' names as they are, and then fail to find them.
func bqQualifyView(sql, dataset string) string {
	body := stripLeadingComments(sql)
	m := bqViewName.FindStringSubmatchIndex(body)
	if m == nil || dataset == "" {
		return sql
	}
	return body[:m[4]] + "`" + dataset + "`." + body[m[4]:]
}

func bqJobError(p *bqProblem) error { return &bqError{Reason: p.Reason, Message: p.Message} }

var bqOverLimit = regexp.MustCompile(`(?i)exceeded limit for bytes billed: (\d+)\. (\d+) or higher required`)

func (x *bigquery) explain(err error) error {
	var e *bqError
	if !errors.As(err, &e) {
		return err
	}
	if m := bqOverLimit.FindStringSubmatch(e.Message); m != nil {
		return fmt.Errorf("this would read %s, over the connection's limit of %s for a query: nothing ran and nothing is billed. Read fewer columns or one partition, or raise the limit in the connection", bqSize(bqInt(m[2])), bqSize(x.limit))
	}
	if e.Reason == "billingNotEnabled" {
		return fmt.Errorf("the project has no billing, so BigQuery refuses this (its free tier has no INSERT, UPDATE, DELETE, MERGE or TRUNCATE): %w", e)
	}
	return err
}

// bqSize writes bytes for people.
func bqSize(n int64) string {
	f, u := float64(n), "bytes"
	for _, next := range []string{"KB", "MB", "GB", "TB", "PB"} {
		if f < 1024 {
			break
		}
		f, u = f/1024, next
	}
	if u == "bytes" || f >= 100 {
		return fmt.Sprintf("%.0f %s", f, u)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0") + " " + u
}

func (x *bigquery) cancel(ref bqJobRef) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	q := url.Values{}
	if ref.Location != "" {
		q.Set("location", ref.Location)
	}
	x.call(ctx, http.MethodPost, "/jobs/"+ref.ID+"/cancel", q, nil, nil)
}

// page reads rows of a finished query, waiting for it.
func (x *bigquery) page(ctx context.Context, ref bqJobRef, offset, limit int, token string) (*bqResults, error) {
	for {
		q := url.Values{"formatOptions.useInt64Timestamp": {"true"}, "timeoutMs": {"10000"}, "maxResults": {strconv.Itoa(limit)}}
		if ref.Location != "" {
			q.Set("location", ref.Location)
		}
		if token != "" {
			q.Set("pageToken", token)
		} else if offset > 0 {
			q.Set("startIndex", strconv.Itoa(offset))
		}
		var r bqResults
		err := x.call(ctx, http.MethodGet, "/queries/"+ref.ID, q, nil, &r)
		if err != nil {
			if ctx.Err() != nil {
				x.cancel(ref)
			}
			return nil, x.explain(err)
		}
		if r.Complete {
			return &r, nil
		}
	}
}

func (x *bigquery) job(ctx context.Context, ref bqJobRef) (*bqJob, error) {
	q := url.Values{}
	if ref.Location != "" {
		q.Set("location", ref.Location)
	}
	var j bqJob
	err := x.call(ctx, http.MethodGet, "/jobs/"+ref.ID, q, nil, &j)
	return &j, err
}

// ddl runs a statement that changes structure.
func (x *bigquery) ddl(ctx context.Context, dataset, stmt string) error {
	for wait := time.Second; ; wait *= 2 {
		job, err := x.start(ctx, dataset, stmt, nil, false)
		if err == nil {
			_, err = x.page(ctx, job.Ref, 0, 1, "")
		}
		var e *bqError
		if err == nil || wait > 10*time.Second || !errors.As(err, &e) || !strings.Contains(e.Message, "table update operations") && e.Reason != "rateLimitExceeded" {
			x.forget()
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// estimate is how many bytes a script would read.
func (x *bigquery) estimate(ctx context.Context, c *Conn, dataset, sql string) (int64, error) {
	job, err := x.start(ctx, dataset, sql, nil, true)
	if err != nil {
		return 0, err
	}
	return bqInt(job.Statistics.Bytes), nil
}

// Estimate returns how many bytes a script would read, where reading is
// billed (BigQuery).
func (c *Conn) Estimate(ctx context.Context, schema, sql string) (int64, error) {
	x, ok := c.remote.(*bigquery)
	if !ok {
		return 0, errors.New("no estimate here")
	}
	// Without c.mu: a query that runs mustn't hold it up. start only reads x,
	// but for the place of a dataset, which is asked for here.
	y := &bigquery{http: x.http, project: x.project, location: map[string]string{}}
	return y.estimate(ctx, c, schema, sql)
}

// ---------------------------------------------------------------- values

// bqValue is a cell as text.
func bqValue(f bqField, raw json.RawMessage) Value {
	if len(raw) == 0 || string(raw) == "null" {
		return NullValue
	}
	if f.Mode == "REPEATED" || f.Type == "RECORD" || f.Type == "STRUCT" {
		var b strings.Builder
		bqJSON(&b, f, raw)
		return Text(b.String())
	}
	s := bqText(raw)
	if f.Type == "BYTES" {
		if b, err := base64.StdEncoding.DecodeString(s); err == nil {
			return Value{Text: "0x" + strings.ToUpper(hex.EncodeToString(b)), Hex: true}
		}
	}
	return Text(bqScalar(f, s))
}

func bqText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return string(raw)
	}
	return s
}

func bqScalar(f bqField, s string) string {
	switch f.Type {
	case "TIMESTAMP":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.UnixMicro(n).UTC().Format("2006-01-02 15:04:05.999999") + "+00"
		}
	case "DATETIME":
		return strings.Replace(s, "T", " ", 1)
	case "FLOAT", "FLOAT64":
		if n, err := strconv.ParseFloat(s, 64); err == nil && !strings.ContainsAny(s, "NI") {
			return strconv.FormatFloat(n, 'g', -1, 64)
		}
	}
	return s
}

// bqJSON writes a repeated or nested value as JSON, its numbers exact.
func bqJSON(b *strings.Builder, f bqField, raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" {
		b.WriteString("null")
		return
	}
	if f.Mode == "REPEATED" {
		var items []struct {
			V json.RawMessage `json:"v"`
		}
		json.Unmarshal(raw, &items)
		one := f
		one.Mode = ""
		b.WriteByte('[')
		for i, it := range items {
			if i > 0 {
				b.WriteByte(',')
			}
			bqJSON(b, one, it.V)
		}
		b.WriteByte(']')
		return
	}
	switch f.Type {
	case "RECORD", "STRUCT":
		var r bqRow
		json.Unmarshal(raw, &r)
		b.WriteByte('{')
		for i, sub := range f.Fields {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(jsonString(sub.Name) + ":")
			if i < len(r.F) {
				bqJSON(b, sub, r.F[i].V)
			} else {
				b.WriteString("null")
			}
		}
		b.WriteByte('}')
	case "INTEGER", "INT64", "BOOLEAN", "BOOL", "JSON":
		b.WriteString(bqText(raw))
	case "FLOAT", "FLOAT64":
		s := bqScalar(f, bqText(raw))
		if strings.ContainsAny(s, "NI") { // NaN, Infinity
			s = jsonString(s)
		}
		b.WriteString(s)
	default:
		b.WriteString(jsonString(bqScalar(f, bqText(raw))))
	}
}

func bqSet(fields []bqField, rows []bqRow) *ResultSet {
	rs := &ResultSet{Columns: []string{}, Types: []string{}, Rows: [][]Value{}}
	for _, f := range fields {
		rs.Columns = append(rs.Columns, f.Name)
		rs.Types = append(rs.Types, f.sqlType())
	}
	for _, r := range rows {
		rs.Rows = append(rs.Rows, bqCells(fields, r))
	}
	return rs
}

func bqCells(fields []bqField, r bqRow) []Value {
	row := make([]Value, len(fields))
	for i, f := range fields {
		if i < len(r.F) {
			row[i] = bqValue(f, r.F[i].V)
		} else {
			row[i] = NullValue
		}
	}
	return row
}

// ---------------------------------------------------------------- engine

func (x *bigquery) ping(ctx context.Context, c *Conn) error {
	return x.call(ctx, http.MethodGet, "/datasets", url.Values{"maxResults": {"1"}}, nil, nil)
}

func (x *bigquery) serverVersion(context.Context, *Conn) (string, error) { return "BigQuery", nil }

// databases lists the projects the account sees.
func (x *bigquery) databases(ctx context.Context, c *Conn) ([]string, error) {
	out := []string{x.project}
	token := ""
	for range 20 {
		q := url.Values{"maxResults": {"500"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, bqAPI+"/bigquery/v2/projects?"+q.Encode(), nil)
		if err != nil {
			return out, err
		}
		var r struct {
			Projects []struct {
				Ref bqRef `json:"projectReference"`
			} `json:"projects"`
			Token string `json:"nextPageToken"`
		}
		if x.do(req, &r) != nil { // it may not be allowed
			break
		}
		for _, p := range r.Projects {
			if p.Ref.Project != x.project {
				out = append(out, p.Ref.Project)
			}
		}
		if token = r.Token; token == "" {
			break
		}
	}
	sort.Strings(out)
	return out, nil
}

func (x *bigquery) schemas(ctx context.Context, c *Conn) ([]string, error) {
	out := []string{}
	q := url.Values{"maxResults": {"1000"}}
	for {
		var r struct {
			Datasets []struct {
				Ref      bqRef  `json:"datasetReference"`
				Location string `json:"location"`
			} `json:"datasets"`
			Token string `json:"nextPageToken"`
		}
		if err := x.call(ctx, http.MethodGet, "/datasets", q, nil, &r); err != nil {
			return nil, err
		}
		for _, d := range r.Datasets {
			out = append(out, d.Ref.Dataset)
			x.location[d.Ref.Dataset] = d.Location
		}
		if r.Token == "" {
			sort.Strings(out)
			return out, nil
		}
		q.Set("pageToken", r.Token)
	}
}

func (x *bigquery) currentSchema(_ context.Context, c *Conn) (string, error) { return c.schema, nil }

func (x *bigquery) useSchema(ctx context.Context, c *Conn, schema string) error {
	return x.call(ctx, http.MethodGet, "/datasets/"+url.PathEscape(schema), nil, nil, nil)
}

func (x *bigquery) objects(ctx context.Context, c *Conn, schema string) ([]Object, error) {
	out := []Object{}
	if schema == "" {
		return out, nil
	}
	q := url.Values{"maxResults": {"1000"}}
	for {
		var r struct {
			Tables []bqTable `json:"tables"`
			Token  string    `json:"nextPageToken"`
		}
		if err := x.call(ctx, http.MethodGet, "/datasets/"+url.PathEscape(schema)+"/tables", q, nil, &r); err != nil {
			return nil, err
		}
		for i := range r.Tables {
			k := KindTable
			if !r.Tables[i].stored() && r.Tables[i].Type != "EXTERNAL" {
				k = KindView
			}
			out = append(out, Object{Schema: schema, Name: r.Tables[i].Ref.Table, Kind: k})
		}
		if r.Token == "" {
			sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			return out, nil
		}
		q.Set("pageToken", r.Token)
	}
}

func (x *bigquery) columns(ctx context.Context, c *Conn, t TableRef) ([]Column, error) {
	bt, err := x.recent(ctx, t)
	if err != nil {
		return nil, err
	}
	out := []Column{}
	for _, f := range bt.Schema.Fields {
		col := Column{Name: f.Name, Type: f.sqlType(), Nullable: f.Mode != "REQUIRED" && f.Mode != "REPEATED", Comment: f.Description,
			Collation: f.Collation, PrimaryKey: slices.Contains(bt.Constraints.PrimaryKey.Columns, f.Name)}
		if f.Default != "" {
			def := f.Default
			col.Default = &def
			col.DefaultKind, col.DefaultText = bqDefault(def)
		}
		out = append(out, col)
	}
	return out, nil
}

// bqDefault reads a default: a literal, or an expression.
func bqDefault(e string) (DefaultKind, string) {
	switch {
	case strings.EqualFold(e, "NULL"):
		return DefaultNull, ""
	case sfNumber.MatchString(e), strings.EqualFold(e, "true"), strings.EqualFold(e, "false"):
		return DefaultValue, strings.ToLower(e)
	case len(e) >= 2 && (e[0] == '\'' || e[0] == '"') && e[len(e)-1] == e[0] && !strings.ContainsAny(e[1:len(e)-1], `\'"`):
		return DefaultValue, e[1 : len(e)-1]
	}
	return DefaultExpression, e
}

func (x *bigquery) indexes(ctx context.Context, c *Conn, t TableRef) ([]Index, error) {
	bt, err := x.recent(ctx, t)
	if err != nil {
		return nil, err
	}
	if pk := bt.Constraints.PrimaryKey.Columns; len(pk) > 0 {
		return []Index{{Name: "PRIMARY KEY", Columns: pk, Unique: true, Primary: true, Constraint: true, Type: "not enforced"}}, nil
	}
	return []Index{}, nil
}

func (x *bigquery) foreignKeys(ctx context.Context, c *Conn, t TableRef) ([]ForeignRef, error) {
	bt, err := x.recent(ctx, t)
	if err != nil {
		return nil, err
	}
	return bt.keys(), nil
}

func (t *bqTable) keys() []ForeignRef {
	out := []ForeignRef{}
	for _, k := range t.Constraints.ForeignKeys {
		name := k.Name
		if i := strings.LastIndexByte(name, '.'); i >= 0 { // table.name
			name = name[i+1:]
		}
		fk := ForeignRef{Name: name, Schema: k.Table.Dataset, Table: k.Table.Table, OnDelete: "NO ACTION", OnUpdate: "NO ACTION"}
		for _, p := range k.Columns {
			fk.From = append(fk.From, p.From)
			fk.Columns = append(fk.Columns, p.To)
		}
		out = append(out, fk)
	}
	return out
}

func (x *bigquery) tableInfo(ctx context.Context, c *Conn, t TableRef) (TableInfo, error) {
	bt, err := x.recent(ctx, t)
	if err != nil {
		return TableInfo{}, err
	}
	var p []string
	if !bt.stored() && bt.Type != "" {
		p = append(p, strings.ToLower(strings.ReplaceAll(bt.Type, "_", " ")))
	} else if n := bqInt(bt.NumBytes); n > 0 {
		p = append(p, bqSize(n))
	}
	if s := bt.partition(); s != "" {
		p = append(p, "partitioned by "+s)
	}
	if bt.RequireFilter {
		p = append(p, "queries need a partition filter")
	}
	if bt.Clustering != nil {
		p = append(p, "clustered by "+strings.Join(bt.Clustering.Fields, ", "))
	}
	if ms := bqInt(bt.Expires); ms > 0 {
		p = append(p, "expires "+time.UnixMilli(ms).UTC().Format("2006-01-02"))
	}
	return TableInfo{Comment: bt.Description, Options: strings.Join(p, " · ")}, nil
}

func (t *bqTable) partition() string {
	if r := t.RangePartition; r != nil {
		return fmt.Sprintf("RANGE_BUCKET(%s, GENERATE_ARRAY(%s, %s, %s))", bqIdent(r.Field), r.Range.Start, r.Range.End, r.Range.Interval)
	}
	if t.Partition == nil {
		return ""
	}
	f := t.Partition.Field
	if f == "" && t.Partition.Type == "DAY" {
		return "_PARTITIONDATE"
	}
	if f == "" {
		return "TIMESTAMP_TRUNC(_PARTITIONTIME, " + t.Partition.Type + ")"
	}
	for _, c := range t.Schema.Fields {
		if c.Name != f {
			continue
		}
		switch {
		case c.Type == "DATE" && t.Partition.Type == "DAY":
			return bqIdent(f)
		case c.Type == "DATE":
			return "DATE_TRUNC(" + bqIdent(f) + ", " + t.Partition.Type + ")"
		case c.Type == "DATETIME":
			return "DATETIME_TRUNC(" + bqIdent(f) + ", " + t.Partition.Type + ")"
		}
	}
	return "TIMESTAMP_TRUNC(" + bqIdent(f) + ", " + t.Partition.Type + ")"
}

func (x *bigquery) definition(ctx context.Context, c *Conn, o Object) (string, error) {
	bt, err := x.recent(ctx, TableRef{Schema: o.Schema, Name: o.Name})
	if err != nil {
		return "", err
	}
	return bt.ddl(c.Dialect, c.Dialect.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name}), true), nil
}

// ddl writes a table's CREATE statement from its metadata (asking BigQuery
// for it is billed).
func (t *bqTable) ddl(d Dialect, name string, keys bool) string {
	switch t.Type {
	case "VIEW":
		return "CREATE VIEW " + name + " AS\n" + strings.TrimRight(t.View.Query, "; \n") + ";"
	case "MATERIALIZED_VIEW":
		return "CREATE MATERIALIZED VIEW " + name + " AS\n" + strings.TrimRight(t.MView.Query, "; \n") + ";"
	case "EXTERNAL":
		return "-- An external table: its definition (where its files are) isn't shown here."
	}
	var lines []string
	for _, f := range t.Schema.Fields {
		l := "  " + d.QuoteIdent(f.Name) + " " + f.sqlType()
		if f.Collation != "" {
			l += " COLLATE " + d.QuoteLiteral(Text(f.Collation))
		}
		if f.Default != "" {
			l += " DEFAULT " + f.Default
		}
		if f.Mode == "REQUIRED" {
			l += " NOT NULL"
		}
		if f.Description != "" {
			l += " OPTIONS(description=" + d.QuoteLiteral(Text(f.Description)) + ")"
		}
		lines = append(lines, l)
	}
	if pk := t.Constraints.PrimaryKey.Columns; len(pk) > 0 {
		lines = append(lines, "  PRIMARY KEY ("+quoteList(d, pk)+") NOT ENFORCED")
	}
	if keys {
		for _, fk := range t.keys() {
			lines = append(lines, "  CONSTRAINT "+d.QuoteIdent(fk.Name)+" "+bqKey(d, fk))
		}
	}
	s := "CREATE TABLE " + name + " (\n" + strings.Join(lines, ",\n") + "\n)"
	if p := t.partition(); p != "" {
		s += "\nPARTITION BY " + p
	}
	if t.Clustering != nil {
		s += "\nCLUSTER BY " + quoteList(d, t.Clustering.Fields)
	}
	var opts []string
	if t.Description != "" {
		opts = append(opts, "description="+d.QuoteLiteral(Text(t.Description)))
	}
	if t.RequireFilter {
		opts = append(opts, "require_partition_filter=true")
	}
	if len(opts) > 0 {
		s += "\nOPTIONS(" + strings.Join(opts, ", ") + ")"
	}
	return s + ";"
}

func bqKey(d Dialect, fk ForeignRef) string {
	return "FOREIGN KEY (" + quoteList(d, fk.From) + ") REFERENCES " + d.QualifiedName(TableRef{Schema: fk.Schema, Name: fk.Table}) + " (" + quoteList(d, fk.Columns) + ") NOT ENFORCED"
}

// ---------------------------------------------------------------- reading

// textExpr is a column as text, in SQL.
func (f bqField) textExpr(d Dialect) string {
	q := d.QuoteIdent(f.Name)
	switch {
	case f.Mode == "REPEATED":
		return "TO_JSON_STRING(" + q + ")"
	case f.Type == "RECORD", f.Type == "JSON", f.Type == "RANGE": // of NULL it would be 'null'
		return "IF(" + q + " IS NULL, NULL, TO_JSON_STRING(" + q + "))"
	case f.Type == "GEOGRAPHY":
		return "ST_ASTEXT(" + q + ")"
	case f.Type == "STRING":
		return q
	case f.Type == "BYTES":
		return "CONCAT('0x', UPPER(TO_HEX(" + q + ")))"
	}
	return "CAST(" + q + " AS STRING)"
}

// compared is a column, and a placeholder, that compare.
func (f bqField) compared(d Dialect) (col, value string) {
	switch {
	case f.Type == "STRING" && f.Mode != "REPEATED":
		return d.QuoteIdent(f.Name), "?"
	case f.Mode == "REPEATED", f.Type == "RECORD", f.Type == "JSON", f.Type == "RANGE", f.Type == "GEOGRAPHY", f.Type == "BYTES", f.Type == "INTERVAL":
		return f.textExpr(d), "?"
	}
	t := f
	t.MaxLength, t.Precision, t.Scale = "", "", ""
	return d.QuoteIdent(f.Name), "CAST(? AS " + t.sqlType() + ")"
}

func bqCondition(d Dialect, fields []bqField, f Filter, args *[]string) (string, error) {
	if f.Column == AnyColumn {
		joiner := " OR "
		if f.Op == OpNe || f.Op == OpNotIn || f.Op == OpNotLike || f.Op == OpNotContain || f.Op == OpIsNotNull {
			joiner = " AND "
		}
		var parts []string
		for _, col := range fields {
			g := f
			g.Column = col.Name
			p, err := bqCondition(d, fields, g, args)
			if err != nil {
				return "", err
			}
			parts = append(parts, p)
		}
		if len(parts) == 0 {
			return "", errors.New("no columns to search")
		}
		return "(" + strings.Join(parts, joiner) + ")", nil
	}
	i := slices.IndexFunc(fields, func(x bqField) bool { return x.Name == f.Column })
	if i < 0 {
		return "", fmt.Errorf("there is no column %q", f.Column)
	}
	col, val := fields[i].compared(d)
	text := fields[i].textExpr(d)
	bind := func(v, expr string) string { *args = append(*args, v); return expr }
	like := func(not, pattern string) string {
		return "LOWER(" + text + ") " + not + "LIKE LOWER(" + bind(pattern, "?") + ")"
	}
	switch f.Op {
	case OpEq, OpNe, OpLt, OpGt, OpLe, OpGe:
		return col + " " + string(f.Op) + " " + bind(f.Value, val), nil
	case OpIsNull, OpIsNotNull:
		return d.QuoteIdent(f.Column) + " " + string(f.Op), nil
	case OpIn, OpNotIn:
		items := splitList(f.Value)
		if len(items) == 0 {
			return "", fmt.Errorf("%s needs at least one value", f.Op)
		}
		ph := make([]string, len(items))
		for i, it := range items {
			ph[i] = bind(it, val)
		}
		return col + " " + string(f.Op) + " (" + strings.Join(ph, ", ") + ")", nil
	case OpBetween:
		a := bind(f.Value, val)
		return col + " BETWEEN " + a + " AND " + bind(f.Value2, val), nil
	case OpLike:
		return text + " LIKE " + bind(f.Value, "?"), nil
	case OpNotLike:
		return text + " NOT LIKE " + bind(f.Value, "?"), nil
	case OpContains:
		return like("", "%"+escapeLike(f.Value)+"%"), nil
	case OpNotContain:
		return like("NOT ", "%"+escapeLike(f.Value)+"%"), nil
	case OpBeginsWith:
		return like("", escapeLike(f.Value)+"%"), nil
	case OpEndsWith:
		return like("", "%"+escapeLike(f.Value)), nil
	}
	return "", fmt.Errorf("unknown operator %q", f.Op)
}

// selectSQL is the query of a page, without LIMIT: what is read is billed
// whatever the LIMIT, the rows are counted on the way, and the same query
// again (another page) comes free from BigQuery's cache.
func (x *bigquery) selectSQL(d Dialect, t TableRef, bt *bqTable, p Page) (string, []string, error) {
	var parts, args []string
	for _, f := range p.Filters {
		s, err := bqCondition(d, bt.Schema.Fields, f, &args)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, s)
	}
	if raw := strings.TrimSpace(p.RawWhere); raw != "" {
		parts = append(parts, "("+raw+"\n)")
	}
	q := "SELECT * FROM " + d.QualifiedName(t)
	if len(parts) > 0 {
		q += " WHERE " + strings.Join(parts, " AND ")
	}
	for i, s := range p.Sort {
		sep := ", "
		if i == 0 {
			sep = " ORDER BY "
		}
		dir := Asc
		if s.Dir == Desc {
			dir = Desc
		}
		q += sep + d.QuoteIdent(s.Column) + " " + string(dir)
	}
	return q, args, nil
}

// plain reports a page that tabledata can list.
func (x *bigquery) plain(bt *bqTable, p Page) bool {
	return bt.stored() && len(p.Filters) == 0 && len(p.Sort) == 0 && strings.TrimSpace(p.RawWhere) == ""
}

type bqData struct {
	Rows  []bqRow `json:"rows"`
	Total string  `json:"totalRows"`
	Token string  `json:"pageToken"`
}

func (x *bigquery) data(ctx context.Context, t TableRef, offset, limit int, token string) (*bqData, error) {
	q := url.Values{"maxResults": {strconv.Itoa(limit)}, "formatOptions.useInt64Timestamp": {"true"}}
	if token != "" {
		q.Set("pageToken", token)
	} else if offset > 0 {
		q.Set("startIndex", strconv.Itoa(offset))
	}
	var d bqData
	err := x.call(ctx, http.MethodGet, "/datasets/"+url.PathEscape(t.Schema)+"/tables/"+url.PathEscape(t.Name)+"/data", q, nil, &d)
	return &d, err
}

func bqPageKey(t TableRef, p Page) string {
	p.Limit, p.Offset, p.Sort = 0, 0, nil
	return fmt.Sprintf("%v %v", t, p)
}

func (x *bigquery) fetchRows(ctx context.Context, c *Conn, t TableRef, p Page) (*ResultSet, error) {
	bt, err := x.recent(ctx, t)
	if err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 {
		limit = bqPage
	}
	x.totalOf = ""
	if x.plain(bt, p) {
		rows := []bqRow{}
		for token := ""; ; { // a page of wide rows may come in pieces
			d, err := x.data(ctx, t, p.Offset, limit-len(rows), token)
			if err != nil {
				return nil, err
			}
			rows = append(rows, d.Rows...)
			x.count(t, p, bqInt(d.Total))
			if token = d.Token; token == "" || len(rows) >= limit {
				break
			}
		}
		return bqSet(bt.Schema.Fields, rows), nil
	}
	q, args, err := x.selectSQL(c.Dialect, t, bt, p)
	if err != nil {
		return nil, err
	}
	fields, rows, total, err := x.read(ctx, t.Schema, q, args, p.Offset, limit)
	var e *bqError
	if errors.As(err, &e) && strings.Contains(e.Reason, "resourcesExceeded") && len(p.Sort) > 0 { // sorting it all is too much
		fields, rows, _, err = x.read(ctx, t.Schema, q+" LIMIT "+strconv.Itoa(p.Offset+limit), args, p.Offset, limit)
		total = -1
	}
	if err != nil {
		return nil, err
	}
	if total >= 0 {
		x.count(t, p, total)
	}
	return bqSet(fields, rows), nil
}

// read runs a query and returns rows offset to offset+limit of its result.
func (x *bigquery) read(ctx context.Context, dataset, q string, args []string, offset, limit int) ([]bqField, []bqRow, int64, error) {
	job, err := x.start(ctx, dataset, q, args, false)
	if err != nil {
		return nil, nil, 0, err
	}
	rows := []bqRow{}
	for token := ""; ; {
		r, err := x.page(ctx, job.Ref, offset, limit-len(rows), token)
		if err != nil {
			return nil, nil, 0, err
		}
		rows = append(rows, r.Rows...)
		if token = r.Token; token == "" || len(rows) >= limit {
			return r.Schema.Fields, rows, bqInt(r.Total), nil
		}
	}
}

func (x *bigquery) countRows(ctx context.Context, c *Conn, t TableRef, p Page) (int64, error) {
	if x.counted(t, p) {
		return x.total, nil
	}
	bt, err := x.recent(ctx, t)
	if err != nil {
		return 0, err
	}
	p.Sort = nil
	if x.plain(bt, p) {
		return bqInt(bt.NumRows), nil
	}
	q, args, err := x.selectSQL(c.Dialect, t, bt, p)
	if err != nil {
		return 0, err
	}
	_, rows, _, err := x.read(ctx, t.Schema, "SELECT COUNT(*) FROM ("+q+"\n)", args, 0, 1)
	if err != nil || len(rows) == 0 || len(rows[0].F) == 0 {
		return 0, err
	}
	return bqInt(bqText(rows[0].F[0].V)), nil
}

// costlyCount reports a count that isn't known from reading the page.
func (x *bigquery) costlyCount(ctx context.Context, t TableRef, p Page) bool {
	if x.counted(t, p) {
		return false
	}
	bt, err := x.recent(ctx, t)
	p.Sort = nil
	return err != nil || !x.plain(bt, p)
}

func (x *bigquery) streamRows(ctx context.Context, c *Conn, t TableRef, p Page, fn func(cols, types []string, row []Value) error) error {
	bt, err := x.table(ctx, t)
	if err != nil {
		return err
	}
	next := func(token string) ([]bqRow, string, error) {
		d, err := x.data(ctx, t, p.Offset, bqPage, token)
		return d.Rows, d.Token, err
	}
	fields := bt.Schema.Fields
	if !x.plain(bt, p) {
		q, args, err := x.selectSQL(c.Dialect, t, bt, p)
		if err != nil {
			return err
		}
		job, err := x.start(ctx, t.Schema, q, args, false)
		if err != nil {
			return err
		}
		next = func(token string) ([]bqRow, string, error) {
			r, err := x.page(ctx, job.Ref, p.Offset, bqPage, token)
			if err != nil {
				return nil, "", err
			}
			return r.Rows, r.Token, nil
		}
	}
	head := bqSet(fields, nil)
	n := 0
	for token := ""; ; {
		rows, t2, err := next(token)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if p.Limit > 0 && n >= p.Limit {
				return nil
			}
			n++
			if err := fn(head.Columns, head.Types, bqCells(fields, r)); err != nil {
				return err
			}
		}
		if token = t2; token == "" || p.Limit > 0 && n >= p.Limit {
			return nil
		}
	}
}

func (x *bigquery) previewChanges(context.Context, *Conn, TableRef, []Change) ([]string, error) {
	return nil, errBQEdit
}

func (x *bigquery) applyChanges(context.Context, *Conn, TableRef, []Change) error { return errBQEdit }

// ---------------------------------------------------------------- editor

// bqScript: statements that only mean something together with the others.
var bqScript = regexp.MustCompile(`(?i)^(DECLARE|SET|BEGIN|COMMIT|ROLLBACK|IF|LOOP|WHILE|REPEAT|FOR|CASE|LEAVE|BREAK|CONTINUE|ITERATE|RAISE|RETURN|END|EXCEPTION|EXECUTE\s+IMMEDIATE|CREATE\s+(OR\s+REPLACE\s+)?TEMP(ORARY)?|CREATE\s+(OR\s+REPLACE\s+)?PROCEDURE)\b`)

// split cuts a script at its semicolons, or leaves it whole where its
// statements belong together (variables, transactions, temporary tables):
// BigQuery then runs it as one script.
func (x *bigquery) split(script string) []Span {
	spans := bqSplit(script)
	for _, s := range spans {
		st := stripLeadingComments(s.SQL)
		if len(spans) > 1 && bqScript.MatchString(st) {
			return []Span{{SQL: strings.TrimSpace(script[spans[0].Start:spans[len(spans)-1].End]), Start: spans[0].Start, End: spans[len(spans)-1].End}}
		}
	}
	return spans
}

func bqSplit(s string) []Span {
	var out []Span
	start := 0
	emit := func(end int) {
		t := strings.TrimSpace(s[start:end])
		if t != "" && !onlyComments(t) {
			lead := len(s[start:end]) - len(strings.TrimLeft(s[start:end], " \t\r\n"))
			out = append(out, Span{SQL: t, Start: start + lead, End: end})
		}
	}
	for i := 0; i < len(s); {
		switch ch := s[i]; {
		case ch == '\'' || ch == '"' || ch == '`':
			raw := ch != '`' && i > 0 && bqRawPrefix(s[:i])
			i = bqSkip(s, i, ch, raw)
			continue
		case ch == '#' || ch == '-' && strings.HasPrefix(s[i:], "--"):
			if eol := strings.IndexByte(s[i:], '\n'); eol >= 0 {
				i += eol
			} else {
				i = len(s)
			}
			continue
		case strings.HasPrefix(s[i:], "/*"):
			if end := strings.Index(s[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				i = len(s)
			}
			continue
		case ch == ';':
			emit(i)
			start = i + 1
		}
		i++
	}
	emit(len(s))
	return out
}

// bqRawPrefix reports an r, br or rb in front of a quote: no escapes inside.
func bqRawPrefix(s string) bool {
	n := len(s)
	k := n
	for k > 0 && n-k < 2 && strings.ContainsRune("rRbB", rune(s[k-1])) {
		k--
	}
	return k < n && (k == 0 || !isIdentByte(s[k-1])) && strings.ContainsAny(s[k:n], "rR")
}

func bqSkip(s string, i int, q byte, raw bool) int {
	tri := strings.Repeat(string(q), 3)
	triple := q != '`' && strings.HasPrefix(s[i:], tri)
	j := i + 1
	if triple {
		j = i + 3
	}
	for ; j < len(s); j++ {
		switch {
		case s[j] == '\\':
			j++ // in a raw string too, a backslash keeps the quote after it
		case triple && strings.HasPrefix(s[j:], tri):
			return j + 3
		case !triple && s[j] == q:
			return j + 1
		case !triple && s[j] == '\n' && q != '`': // not closed: such strings end with the line
			return j
		}
	}
	return len(s)
}

func (x *bigquery) run(ctx context.Context, c *Conn, stmt string, rowCap int) Result {
	rs := x.runAll(ctx, c, stmt, rowCap)
	return rs[len(rs)-1]
}

// runAll runs a statement or a script, with a result for each statement of
// a script that shows something.
func (x *bigquery) runAll(ctx context.Context, c *Conn, stmt string, rowCap int) []Result {
	begin := time.Now()
	fail := func(err error) []Result {
		return []Result{{Statement: stmt, Err: err, Duration: time.Since(begin)}}
	}
	job, err := x.start(ctx, c.schema, stmt, nil, false)
	if err != nil {
		return fail(err)
	}
	first, err := x.page(ctx, job.Ref, 0, bqMax(rowCap), "")
	x.forget()
	if err != nil {
		return fail(err)
	}
	done, err := x.job(ctx, job.Ref)
	if err != nil {
		return fail(err)
	}
	if bqInt(done.Statistics.Query.Kids) == 0 && done.Statistics.Query.Type != "SCRIPT" {
		return []Result{x.result(ctx, stmt, job.Ref, done, first, rowCap, begin)}
	}
	kids, err := x.children(ctx, job.Ref)
	if err != nil {
		return fail(err)
	}
	var out []Result
	for i := range kids {
		k := &kids[i]
		if k.Statistics.Script.Kind == "EXPRESSION" {
			continue
		}
		r := Result{Statement: k.Config.Query.Query}
		if k.Status.Error != nil {
			r.Err = bqJobError(k.Status.Error)
		} else if page, err := x.page(ctx, k.Ref, 0, bqMax(rowCap), ""); err != nil {
			r.Err = err
		} else {
			r = x.result(ctx, r.Statement, k.Ref, k, page, rowCap, begin)
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return []Result{{Statement: stmt, Duration: time.Since(begin)}}
	}
	return out
}

func bqMax(rowCap int) int {
	if rowCap <= 0 || rowCap > bqPage {
		return bqPage
	}
	return rowCap + 1
}

func (x *bigquery) children(ctx context.Context, ref bqJobRef) ([]bqJob, error) {
	var out []bqJob
	q := url.Values{"parentJobId": {ref.ID}, "maxResults": {"1000"}, "projection": {"full"}}
	for {
		var r struct {
			Jobs  []bqJob `json:"jobs"`
			Token string  `json:"nextPageToken"`
		}
		if err := x.call(ctx, http.MethodGet, "/jobs", q, nil, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Jobs...)
		if r.Token == "" {
			sort.SliceStable(out, func(i, j int) bool { return bqInt(out[i].Statistics.Created) < bqInt(out[j].Statistics.Created) })
			return out, nil
		}
		q.Set("pageToken", r.Token)
	}
}

// result reads a finished statement's rows (up to rowCap).
func (x *bigquery) result(ctx context.Context, stmt string, ref bqJobRef, job *bqJob, first *bqResults, rowCap int, begin time.Time) Result {
	r := Result{Statement: stmt, RowsAffected: bqInt(first.DML)}
	if t := job.Statistics.Query.Type; t == "SELECT" || len(first.Schema.Fields) > 0 && t == "" {
		rows, page := first.Rows, first
		for page.Token != "" && (rowCap <= 0 || len(rows) <= rowCap) {
			var err error
			if page, err = x.page(ctx, ref, 0, bqPage, page.Token); err != nil {
				r.Err = err
				break
			}
			rows = append(rows, page.Rows...)
		}
		if rowCap > 0 && len(rows) > rowCap {
			rows, r.Truncated = rows[:rowCap], true
		}
		r.Set = bqSet(first.Schema.Fields, rows)
	}
	r.Duration = time.Since(begin)
	return r
}

// ---------------------------------------------------------------- structure

// bqTypes: STRUCT<a INT64, b ARRAY<STRING(10)>>, NUMERIC(10, 2), RANGE<DATE>.
var bqTypes = regexp.MustCompile("^[A-Za-z][A-Za-z0-9_]*[A-Za-z0-9_ ,<>()`]*$")

func bqTypeOK(t string) bool {
	depth := 0
	for _, r := range t {
		switch r {
		case '<', '(':
			depth++
		case '>', ')':
			if depth--; depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && strings.Count(t, "`")%2 == 0 && bqTypes.MatchString(t)
}

func bqCheck(c ColumnDef) error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("column name is required")
	}
	if t := strings.TrimSpace(c.Type); !bqTypeOK(t) {
		return fmt.Errorf("%q doesn't look like a column type (e.g. STRING, NUMERIC(10, 2), ARRAY<INT64>)", t)
	}
	if c.DefaultKind == DefaultExpression && (strings.TrimSpace(c.Default) == "" || strings.Contains(c.Default, ";")) {
		return errors.New("enter the default expression, e.g. CURRENT_TIMESTAMP(), without ';'")
	}
	if c.Collation != "" || c.OnUpdateNow || c.Position != "" {
		return errors.New("BigQuery columns take no collation, position or ON UPDATE here")
	}
	return nil
}

// bqDefaultSQL writes a default value the way its column's type wants it.
func bqDefaultSQL(d Dialect, c ColumnDef) string {
	switch c.DefaultKind {
	case DefaultNull:
		return "NULL"
	case DefaultExpression:
		return strings.TrimSpace(c.Default)
	}
	base := strings.ToUpper(strings.TrimSpace(c.Type))
	if i := strings.IndexAny(base, "(<"); i >= 0 {
		base = base[:i]
	}
	v := strings.TrimSpace(c.Default)
	switch base {
	case "INT64", "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "BYTEINT", "FLOAT64", "NUMERIC", "DECIMAL", "BIGNUMERIC", "BIGDECIMAL":
		if sfNumber.MatchString(v) {
			return v
		}
	case "BOOL", "BOOLEAN":
		if strings.EqualFold(v, "true") || strings.EqualFold(v, "false") {
			return strings.ToUpper(v)
		}
	case "DATE", "DATETIME", "TIME", "TIMESTAMP", "JSON":
		return base + " " + d.QuoteLiteral(Text(c.Default))
	}
	return d.QuoteLiteral(Text(c.Default))
}

func bqColumnSQL(d Dialect, c ColumnDef) string {
	s := d.QuoteIdent(strings.TrimSpace(c.Name)) + " " + strings.TrimSpace(c.Type)
	if c.DefaultKind != DefaultNone {
		s += " DEFAULT " + bqDefaultSQL(d, c)
	}
	if !c.Nullable && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(c.Type)), "ARRAY") {
		s += " NOT NULL"
	}
	if cm := strings.TrimSpace(c.Comment); cm != "" {
		s += " OPTIONS(description=" + d.QuoteLiteral(Text(cm)) + ")"
	}
	return s
}

func bqCreateTable(d Dialect, schema string, t NewTable) (string, error) {
	name := strings.TrimSpace(t.Name)
	if name == "" {
		return "", errors.New("table name is required")
	}
	if len(t.Columns) == 0 {
		return "", errors.New("add at least one column")
	}
	var lines, pk []string
	for _, c := range t.Columns {
		if err := bqCheck(c.ColumnDef); err != nil {
			return "", fmt.Errorf("column %s: %w", c.Name, err)
		}
		if c.AutoIncrement {
			return "", fmt.Errorf("column %s: BigQuery has no auto-increment; use GENERATE_UUID() as the default of a STRING column", c.Name)
		}
		if c.Index != "" {
			return "", fmt.Errorf("column %s: BigQuery has no indexes or unique keys here", c.Name)
		}
		if c.PrimaryKey {
			pk = append(pk, strings.TrimSpace(c.Name))
		}
		lines = append(lines, "  "+bqColumnSQL(d, c.ColumnDef))
	}
	if len(pk) > 0 {
		lines = append(lines, "  PRIMARY KEY ("+quoteList(d, pk)+") NOT ENFORCED")
	}
	s := "CREATE TABLE " + d.QualifiedName(TableRef{Schema: schema, Name: name}) + " (\n" + strings.Join(lines, ",\n") + "\n)"
	if cm := strings.TrimSpace(t.Comment); cm != "" {
		s += "\nOPTIONS(description=" + d.QuoteLiteral(Text(cm)) + ")"
	}
	return s + ";", nil
}

func (x *bigquery) createTable(ctx context.Context, c *Conn, schema string, t NewTable) ([]string, error) {
	s, err := bqCreateTable(c.Dialect, schema, t)
	if err != nil {
		return nil, err
	}
	return []string{s}, x.ddl(ctx, schema, s)
}

func bqAlter(d Dialect, t TableRef, st TableState, op AlterOp) ([]string, error) {
	alter := "ALTER TABLE " + d.QualifiedName(t) + " "
	_, hasPK := primaryIndex(st)
	switch op.Kind {
	case AlterAddColumn:
		if op.Def == nil {
			return nil, errors.New("missing column definition")
		}
		if err := bqCheck(*op.Def); err != nil {
			return nil, err
		}
		if !op.Def.Nullable && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(op.Def.Type)), "ARRAY") {
			return nil, errors.New("BigQuery can't add a NOT NULL column to a table: allow NULL")
		}
		def := *op.Def
		kind := def.DefaultKind
		if kind != DefaultNone { // a new column takes its default in a second step
			def.DefaultKind = DefaultNone
		}
		out := []string{alter + "ADD COLUMN " + bqColumnSQL(d, def) + ";"}
		if kind != DefaultNone {
			out = append(out, alter+"ALTER COLUMN "+d.QuoteIdent(strings.TrimSpace(def.Name))+" SET DEFAULT "+bqDefaultSQL(d, *op.Def)+";")
		}
		return out, nil
	case AlterModifyColumn:
		return bqModify(d, t, st, op, alter)
	case AlterDropColumn:
		if _, ok := findColumn(st, op.Column); !ok {
			return nil, fmt.Errorf("there is no column %q", op.Column)
		}
		if len(st.Columns) == 1 {
			return nil, errors.New("a table needs at least one column; drop the table instead")
		}
		return []string{alter + "DROP COLUMN " + d.QuoteIdent(op.Column) + ";"}, nil
	case AlterSetPrimaryKey, AlterDropIndex:
		cols := op.Columns
		if op.Kind == AlterDropIndex {
			cols = nil
		}
		if len(cols) == 0 && !hasPK {
			return nil, errors.New("the table has no primary key")
		}
		var out []string
		if hasPK {
			out = append(out, alter+"DROP PRIMARY KEY;")
		}
		if len(cols) > 0 {
			if err := checkColumns(st, cols, "primary key"); err != nil {
				return nil, err
			}
			out = append(out, alter+"ADD PRIMARY KEY ("+quoteList(d, cols)+") NOT ENFORCED;")
		}
		return out, nil
	case AlterAddForeignKey:
		if op.ForeignKey == nil {
			return nil, errors.New("missing foreign key")
		}
		fk := *op.ForeignKey
		for _, a := range []string{fk.OnDelete, fk.OnUpdate} {
			if a != "" && !strings.EqualFold(a, "NO ACTION") {
				return nil, errors.New("BigQuery doesn't act on foreign keys: use NO ACTION")
			}
		}
		if err := checkColumns(st, fk.Columns, "foreign key"); err != nil {
			return nil, err
		}
		if strings.TrimSpace(fk.RefTable) == "" || len(fk.RefColumns) != len(fk.Columns) {
			return nil, errors.New("choose the table the key refers to, and a column for each of the key's")
		}
		ref := ForeignRef{Schema: fk.RefSchema, Table: fk.RefTable, From: fk.Columns, Columns: fk.RefColumns}
		if ref.Schema == "" {
			ref.Schema = t.Schema
		}
		name := strings.TrimSpace(fk.Name)
		if name == "" {
			name = indexName(t.Name, strings.Join(fk.Columns, "_"), "fkey")
		}
		return []string{alter + "ADD CONSTRAINT " + d.QuoteIdent(name) + " " + bqKey(d, ref) + ";"}, nil
	case AlterDropForeignKey:
		return []string{alter + "DROP CONSTRAINT " + d.QuoteIdent(op.Name) + ";"}, nil
	case AlterRenameTable:
		n, err := requireName(op.NewName, "table")
		if err != nil {
			return nil, err
		}
		return []string{alter + "RENAME TO " + d.QuoteIdent(n) + ";"}, nil
	case AlterTableComment:
		return []string{alter + "SET OPTIONS(description=" + d.QuoteLiteral(Text(strings.TrimSpace(op.Comment))) + ");"}, nil
	case AlterAddIndex, AlterRenameIndex:
		return nil, errors.New("BigQuery tables have no indexes to add here: cluster or partition the table")
	case AlterAddCheck, AlterDropCheck:
		return nil, errors.New("BigQuery has no CHECK constraints")
	case AlterTruncate:
		return nil, errors.New("empty a table with TRUNCATE TABLE in the query editor (the project needs billing)")
	}
	return nil, fmt.Errorf("BigQuery can't do this (%s)", op.Kind)
}

func bqModify(d Dialect, t TableRef, st TableState, op AlterOp, alter string) ([]string, error) {
	old, ok := findColumn(st, op.Column)
	if !ok || op.Def == nil {
		return nil, fmt.Errorf("there is no column %q", op.Column)
	}
	def := *op.Def
	def.Name = strings.TrimSpace(def.Name)
	if def.Collation != old.Collation {
		return nil, errors.New("BigQuery can't change a column's collation")
	}
	def.Collation = ""
	if err := bqCheck(def); err != nil {
		return nil, err
	}
	if !def.Nullable && old.Nullable {
		return nil, errors.New("BigQuery can't make a column NOT NULL once it allows NULL")
	}
	var out []string
	name := old.Name
	if def.Name != old.Name {
		out = append(out, alter+"RENAME COLUMN "+d.QuoteIdent(old.Name)+" TO "+d.QuoteIdent(def.Name)+";")
		name = def.Name
	}
	col := alter + "ALTER COLUMN " + d.QuoteIdent(name) + " "
	if ty := strings.TrimSpace(def.Type); !sameType(ty, old.Type) {
		out = append(out, col+"SET DATA TYPE "+ty+";")
	}
	if def.DefaultKind != old.DefaultKind || def.DefaultKind != DefaultNone && def.Default != old.DefaultText {
		if def.DefaultKind == DefaultNone {
			out = append(out, col+"DROP DEFAULT;")
		} else {
			out = append(out, col+"SET DEFAULT "+bqDefaultSQL(d, def)+";")
		}
	}
	if def.Nullable && !old.Nullable && !strings.HasPrefix(strings.ToUpper(old.Type), "ARRAY") {
		out = append(out, col+"DROP NOT NULL;")
	}
	if cm := strings.TrimSpace(def.Comment); cm != old.Comment {
		out = append(out, col+"SET OPTIONS(description="+d.QuoteLiteral(Text(cm))+");")
	}
	if len(out) == 0 {
		return nil, errors.New("nothing to change")
	}
	return out, nil
}

func (x *bigquery) alter(ctx context.Context, c *Conn, t TableRef, ops []AlterOp, dryRun bool) ([]string, error) {
	if len(ops) == 0 {
		return nil, errors.New("nothing to change")
	}
	x.forget()
	st, err := c.loadTableState(ctx, t)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, op := range ops {
		s, err := bqAlter(c.Dialect, t, st, op)
		if err != nil {
			return nil, err
		}
		out = append(out, s...)
	}
	if dryRun {
		return out, nil
	}
	for _, op := range ops {
		if op.Kind == AlterRenameTable || op.Kind == AlterSetPrimaryKey || op.Kind == AlterDropIndex {
			if err := x.unreferenced(ctx, c, []Object{{Schema: t.Schema, Name: t.Name}}); err != nil {
				return out, err
			}
		}
	}
	for _, s := range out {
		if err := x.ddl(ctx, t.Schema, s); err != nil {
			return out, fmt.Errorf("%s %w", s, err)
		}
	}
	return out, nil
}

// unreferenced refuses when another table of the dataset has a foreign key
// to one of objs: BigQuery would drop that key without a word.
func (x *bigquery) unreferenced(ctx context.Context, c *Conn, objs []Object) error {
	seen := map[string]bool{}
	for _, o := range objs {
		if seen[o.Schema] {
			continue
		}
		seen[o.Schema] = true
		all, err := x.objects(ctx, c, o.Schema)
		if err != nil {
			return err
		}
		for _, other := range all {
			if other.Kind == KindView || slices.ContainsFunc(objs, func(d Object) bool { return d.Schema == other.Schema && d.Name == other.Name }) {
				continue
			}
			bt, err := x.table(ctx, TableRef{Schema: other.Schema, Name: other.Name})
			if err != nil {
				continue // gone, or not ours to see
			}
			for _, fk := range bt.keys() {
				if slices.ContainsFunc(objs, func(d Object) bool { return d.Schema == fk.Schema && d.Name == fk.Table }) {
					return fmt.Errorf("%s has a foreign key to %s, which BigQuery would drop: delete the key, or that table too", other.Name, fk.Table)
				}
			}
		}
	}
	return nil
}

func (x *bigquery) drop(ctx context.Context, c *Conn, objs []Object) ([]string, error) {
	if err := x.unreferenced(ctx, c, objs); err != nil {
		return nil, err
	}
	var views, tables []string
	for _, o := range objs {
		bt, err := x.table(ctx, TableRef{Schema: o.Schema, Name: o.Name})
		if err != nil {
			return nil, err
		}
		n := c.Dialect.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name}) + ";"
		switch bt.Type {
		case "VIEW", "MATERIALIZED_VIEW":
			views = append(views, "DROP "+strings.ReplaceAll(bt.Type, "_", " ")+" "+n)
		case "EXTERNAL":
			tables = append(tables, "DROP EXTERNAL TABLE "+n)
		case "SNAPSHOT":
			tables = append(tables, "DROP SNAPSHOT TABLE "+n)
		default:
			tables = append(tables, "DROP TABLE "+n)
		}
	}
	out := append(views, tables...)
	for i, s := range out {
		if err := x.ddl(ctx, objs[0].Schema, s); err != nil {
			if i > 0 {
				err = fmt.Errorf("%w (%d of %d were already deleted)", err, i, len(out))
			}
			return nil, fmt.Errorf("%s %w", s, err)
		}
	}
	return out, nil
}

// bqBuildDrop previews a drop.
func bqBuildDrop(d Dialect, objs []Object) []string {
	var views, tables []string
	for _, o := range objs {
		n := d.QualifiedName(TableRef{Schema: o.Schema, Name: o.Name}) + ";"
		if o.Kind == KindView {
			views = append(views, "DROP VIEW "+n)
		} else {
			tables = append(tables, "DROP TABLE "+n)
		}
	}
	return append(views, tables...)
}

// ---------------------------------------------------------------- dumps

// bqLiteral writes a value as SQL of its column's type.
func bqLiteral(d Dialect, f bqField, raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "NULL"
	}
	if f.Mode == "REPEATED" {
		var items []struct {
			V json.RawMessage `json:"v"`
		}
		json.Unmarshal(raw, &items)
		one := f
		one.Mode = ""
		parts := make([]string, len(items))
		for i, it := range items {
			parts[i] = bqLiteral(d, one, it.V)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	if f.Type == "RECORD" || f.Type == "STRUCT" {
		var r bqRow
		json.Unmarshal(raw, &r)
		parts := make([]string, len(f.Fields))
		for i, sub := range f.Fields {
			parts[i] = "NULL"
			if i < len(r.F) {
				parts[i] = bqLiteral(d, sub, r.F[i].V)
			}
		}
		one := f
		return one.sqlType() + "(" + strings.Join(parts, ", ") + ")"
	}
	s := bqText(raw)
	lit := func(v string) string { return d.QuoteLiteral(Text(v)) }
	switch f.Type {
	case "INTEGER", "INT64", "BOOLEAN", "BOOL":
		return s
	case "FLOAT", "FLOAT64":
		if s = bqScalar(f, s); strings.ContainsAny(s, "NI") {
			return "CAST(" + lit(s) + " AS FLOAT64)"
		}
		return s
	case "NUMERIC", "BIGNUMERIC", "DATE", "DATETIME", "TIME", "TIMESTAMP", "JSON":
		return f.Type + " " + lit(bqScalar(f, s))
	case "BYTES":
		b, _ := base64.StdEncoding.DecodeString(s)
		return "FROM_HEX('" + hex.EncodeToString(b) + "')"
	case "GEOGRAPHY":
		return "ST_GEOGFROMTEXT(" + lit(s) + ")"
	case "INTERVAL":
		return "CAST(" + lit(s) + " AS INTERVAL)"
	case "RANGE":
		return "RANGE<" + f.Range.Type + "> " + lit(s)
	}
	return lit(s)
}

func (x *bigquery) dump(ctx context.Context, c *Conn, w io.Writer, o DumpOptions, progress func(DumpProgress)) error {
	if o.BatchSize <= 0 {
		o.BatchSize = 500
	}
	d := c.Dialect
	var tables, views []Object
	for _, ob := range o.Objects {
		if ob.Kind == KindView {
			views = append(views, ob)
		} else {
			tables = append(tables, ob)
		}
	}
	meta := map[string]*bqTable{}
	p := DumpProgress{Tables: len(o.Objects)}
	for _, ob := range o.Objects {
		bt, err := x.table(ctx, TableRef{Schema: ob.Schema, Name: ob.Name})
		if err != nil {
			return fmt.Errorf("%s: %w", ob.Name, err)
		}
		meta[ob.Name] = bt
		if o.Data && bt.stored() {
			p.TotalRows += bqInt(bt.NumRows)
		}
	}
	sw := &sqlWriter{w: w}
	sw.printf("-- GoTable dump of %s (BigQuery)\n-- Tables: %d, views: %d\n\n", c.Config.Name, len(tables), len(views))
	if o.DropFirst {
		for _, v := range views {
			sw.printf("DROP %s IF EXISTS %s;\n", strings.ReplaceAll(meta[v.Name].Type, "_", " "), d.QuoteIdent(v.Name))
		}
		for _, t := range tables {
			sw.printf("DROP TABLE IF EXISTS %s;\n", d.QuoteIdent(t.Name))
		}
		sw.printf("\n")
	}
	var post []string
	for i, t := range tables {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.Table, p.TableIndex = t.Name, i
		if progress != nil {
			progress(p)
		}
		bt := meta[t.Name]
		if o.Structure {
			sw.printf("-- Table %s\n%s\n\n", t.Name, bt.ddl(d, d.QuoteIdent(t.Name), false))
			for _, fk := range bt.keys() {
				if fk.Schema == t.Schema {
					fk.Schema = ""
				}
				post = append(post, "ALTER TABLE "+d.QuoteIdent(t.Name)+" ADD CONSTRAINT "+d.QuoteIdent(fk.Name)+" "+bqKey(d, fk)+";")
			}
		}
		if !o.Data || !bt.stored() {
			continue
		}
		fields := bt.Schema.Fields
		names := make([]string, len(fields))
		for k, f := range fields {
			names[k] = f.Name
		}
		head := "INSERT INTO " + d.QuoteIdent(t.Name) + " (" + quoteList(d, names) + ") VALUES\n  "
		var batch []string
		size := 0
		flush := func() {
			if len(batch) > 0 {
				sw.printf("%s%s;\n", head, strings.Join(batch, ",\n  "))
				batch, size = batch[:0], 0
			}
		}
		ref := TableRef{Schema: t.Schema, Name: t.Name}
		for token := ""; ; {
			data, err := x.data(ctx, ref, 0, bqPage, token)
			if err != nil {
				return fmt.Errorf("%s: %w", t.Name, err)
			}
			for _, r := range data.Rows {
				vals := make([]string, len(fields))
				for k, f := range fields {
					vals[k] = "NULL"
					if k < len(r.F) {
						vals[k] = bqLiteral(d, f, r.F[k].V)
					}
				}
				line := "(" + strings.Join(vals, ", ") + ")"
				if size += len(line) + 4; size > bqStatement {
					flush()
					size = len(line)
				}
				if batch = append(batch, line); len(batch) >= o.BatchSize {
					flush()
				}
				p.Rows++
			}
			if progress != nil {
				progress(p)
			}
			if token = data.Token; token == "" || sw.err != nil {
				break
			}
		}
		flush()
		sw.printf("\n")
	}
	for _, s := range post {
		sw.printf("%s\n", s)
	}
	if o.Structure {
		for i, v := range views {
			p.Table, p.TableIndex = v.Name, len(tables)+i
			if progress != nil {
				progress(p)
			}
			sw.printf("\n-- View %s\n%s\n", v.Name, meta[v.Name].ddl(d, d.QuoteIdent(v.Name), false))
		}
	}
	p.Table, p.TableIndex = "", len(o.Objects)
	if progress != nil {
		progress(p)
	}
	return sw.err
}

// ---------------------------------------------------------------- CSV

// bqLoadValue is a CSV field as a load job's JSON takes it.
func bqLoadValue(f bqField, s string) (json.RawMessage, error) {
	if f.Mode == "REPEATED" || f.Type == "RECORD" || f.Type == "JSON" {
		if !json.Valid([]byte(s)) {
			return nil, fmt.Errorf("column %s takes JSON, not %q", f.Name, snippet(s))
		}
		return json.RawMessage(s), nil
	}
	if f.Type == "BYTES" {
		h := s
		if len(h) >= 2 && (h[:2] == "0x" || h[:2] == "0X") {
			b, err := hex.DecodeString(h[2:])
			if err != nil {
				return nil, fmt.Errorf("column %s: %q isn't hex", f.Name, snippet(s))
			}
			s = base64.StdEncoding.EncodeToString(b)
		} else {
			s = base64.StdEncoding.EncodeToString([]byte(s))
		}
	}
	b, err := json.Marshal(s)
	return b, err
}

// bqLoadFields is a load job's schema.
func bqLoadFields(fields []bqField) []map[string]any {
	out := []map[string]any{}
	for _, f := range fields {
		m := map[string]any{"name": f.Name, "type": f.Type}
		for k, v := range map[string]string{"mode": f.Mode, "maxLength": f.MaxLength, "precision": f.Precision, "scale": f.Scale} {
			if v != "" {
				m[k] = v
			}
		}
		if f.Range.Type != "" {
			m["rangeElementType"] = map[string]string{"type": f.Range.Type}
		}
		if len(f.Fields) > 0 {
			m["fields"] = bqLoadFields(f.Fields)
		}
		out = append(out, m)
	}
	return out
}

// importCSV loads a CSV file with one load job: free, and all or nothing.
func (x *bigquery) importCSV(ctx context.Context, c *Conn, r io.Reader, t TableRef, o CSVImportOptions, progress func(int64)) (int64, error) {
	if len(o.Columns) == 0 || len(o.Columns) != len(o.Mapping) {
		return 0, errors.New("choose at least one column to import")
	}
	bt, err := x.table(ctx, t)
	if err != nil {
		return 0, err
	}
	fields := make([]bqField, len(o.Columns))
	for i, n := range o.Columns {
		k := slices.IndexFunc(bt.Schema.Fields, func(f bqField) bool { return f.Name == n })
		if k < 0 {
			return 0, fmt.Errorf("there is no column %q", n)
		}
		fields[i] = bt.Schema.Fields[k]
	}
	if o.Delimiter == 0 {
		o.Delimiter = ','
	}
	cr := csv.NewReader(r)
	cr.Comma, cr.FieldsPerRecord, cr.LazyQuotes, cr.ReuseRecord = o.Delimiter, -1, true, true
	var total int64
	write := func(w io.Writer) error {
		bw := bufio.NewWriterSize(w, 1<<16)
		for first := true; ; {
			rec, err := cr.Read()
			if errors.Is(err, io.EOF) {
				return bw.Flush()
			}
			if err != nil {
				return fmt.Errorf("CSV: %w", err)
			}
			if first {
				if first = false; o.Header {
					continue
				}
			}
			line, _ := cr.FieldPos(0)
			obj := map[string]json.RawMessage{}
			for i, idx := range o.Mapping {
				if idx < 0 || idx >= len(rec) {
					continue
				}
				s := rec[idx]
				if o.NullText != "" && s == o.NullText || o.EmptyAsNull && s == "" {
					continue // absent is NULL
				}
				if obj[o.Columns[i]], err = bqLoadValue(fields[i], s); err != nil {
					return fmt.Errorf("CSV line %d: %w", line, err)
				}
			}
			b, err := json.Marshal(obj)
			if err != nil {
				return err
			}
			bw.Write(b)
			if err := bw.WriteByte('\n'); err != nil {
				return err
			}
			if total++; progress != nil && total%5000 == 0 {
				progress(total)
			}
		}
	}
	disposition := "WRITE_APPEND"
	if o.Truncate {
		disposition = "WRITE_TRUNCATE_DATA" // keeps the table's structure
	}
	// Only the columns that are filled: the others take their defaults.
	schema := map[string]any{"fields": bqLoadFields(fields)}
	ref := bqJobRef{x.project, "gotable_" + strings.ToLower(model.NewID()), x.place(ctx, t.Schema)}
	conf, _ := json.Marshal(map[string]any{"jobReference": ref, "configuration": map[string]any{"load": map[string]any{
		"destinationTable": bqRef{x.project, t.Schema, t.Name}, "sourceFormat": "NEWLINE_DELIMITED_JSON", "writeDisposition": disposition, "schema": schema, "createDisposition": "CREATE_NEVER"}}})
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	var rowErr error
	go func() {
		part, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}})
		if err == nil {
			_, err = part.Write(conf)
		}
		if err == nil {
			part, err = mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/octet-stream"}})
		}
		if err == nil {
			err = write(part)
			rowErr = err
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	u := bqAPI + "/upload/bigquery/v2/projects/" + url.PathEscape(x.project) + "/jobs?uploadType=multipart"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, pr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "multipart/related; boundary="+mw.Boundary())
	var job bqJob
	err = x.do(req, &job)
	pr.Close()
	if rowErr != nil && !errors.Is(rowErr, io.ErrClosedPipe) {
		return 0, rowErr
	}
	if err != nil {
		return 0, x.explain(err)
	}
	for job.Status.State != "DONE" {
		select {
		case <-ctx.Done():
			x.cancel(ref)
			return 0, ctx.Err()
		case <-time.After(time.Second):
		}
		j, err := x.job(ctx, ref)
		if err != nil {
			return 0, err
		}
		job = *j
	}
	x.forget()
	if e := job.Status.Error; e != nil {
		if n := len(job.Status.All); n > 1 { // the last says what was wrong
			return 0, fmt.Errorf("nothing was imported: %s", job.Status.All[n-1].Message)
		}
		return 0, x.explain(bqJobError(e))
	}
	if progress != nil {
		progress(total)
	}
	return bqInt(job.Statistics.Load.Rows), nil
}
