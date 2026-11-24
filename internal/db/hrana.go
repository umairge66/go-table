package db

// A database/sql driver for libSQL servers (sqld, Turso), which run SQLite
// statements over HTTP: the Hrana protocol, version 3. Statements of one
// session share a stream, named by a baton that changes with each answer; a
// stream left idle for some seconds ends, which loses an open transaction
// and nothing else, so another is opened without a word.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// hranaConnector opens sessions with one server.
type hranaConnector struct {
	base  string // https://host:port
	token string // authentication token, "" for none
	http  *http.Client
}

// newHrana talks to base; dialAddr, if not "", is where its connections
// really go (an SSH tunnel's end).
func newHrana(base, dialAddr, token string, tlsConf *tls.Config) *hranaConnector {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	t := &http.Transport{TLSClientConfig: tlsConf, TLSHandshakeTimeout: 15 * time.Second, IdleConnTimeout: time.Minute, ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if dialAddr != "" {
				addr = dialAddr
			}
			return d.DialContext(ctx, network, addr)
		}}
	return &hranaConnector{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Transport: t}}
}

func (h *hranaConnector) Connect(context.Context) (driver.Conn, error) {
	return &hranaConn{h: h, base: h.base}, nil
}
func (h *hranaConnector) Driver() driver.Driver { return hranaDriver{h} }

type hranaDriver struct{ c *hranaConnector }

func (hranaDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("libSQL connections are opened with a connector")
}

type hranaConn struct {
	h     *hranaConnector
	base  string // the server may send a stream's later requests elsewhere
	baton string // the open stream, "" for none
	inTx  bool   // a transaction is open in it
}

// hranaError is a server's answer to a statement or request.
type hranaError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

func (e *hranaError) Error() string { return e.Message }

var errHranaLost = errors.New("the server ended the transaction (it was idle too long, or the connection broke); nothing of it was saved")

type hranaValue struct {
	Type   string          `json:"type"`
	Value  json.RawMessage `json:"value,omitempty"`
	Base64 *string         `json:"base64,omitempty"`
}

type hranaStmt struct {
	SQL      string       `json:"sql"`
	Args     []hranaValue `json:"args,omitempty"`
	WantRows bool         `json:"want_rows"`
}

type hranaRequest struct {
	Type string     `json:"type"`
	Stmt *hranaStmt `json:"stmt,omitempty"`
}

// post sends one request on the session's stream, and sends it again on a
// new stream if the old one had ended outside a transaction. The server
// refuses a dead stream before running anything, so nothing runs twice.
func (c *hranaConn) post(ctx context.Context, path string, body map[string]any) (*http.Response, error) {
	for {
		if c.baton == "" {
			if c.inTx {
				c.inTx = false
				return nil, errHranaLost
			}
			c.base = c.h.base
			body["baton"] = nil
		} else {
			body["baton"] = c.baton
		}
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.h.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.h.token)
		}
		hadStream := c.baton != ""
		c.baton = "" // each answer brings the next one
		res, err := c.h.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		if res.StatusCode == http.StatusOK {
			return res, nil
		}
		text, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		if hadStream && hranaStreamGone(res.StatusCode, text) {
			continue
		}
		return nil, hranaHTTPError(res, text)
	}
}

// hranaStreamGone: the server no longer knows the stream.
func hranaStreamGone(status int, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusNotFound {
		return false
	}
	s := strings.ToLower(string(body))
	return strings.Contains(s, "stream_expired") || strings.Contains(s, "stream not found") || strings.Contains(s, "invalid baton") || strings.Contains(s, "baton_reused")
}

func hranaHTTPError(res *http.Response, body []byte) error {
	var e struct {
		Message string          `json:"message"`
		Code    string          `json:"code"`
		Error   json.RawMessage `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil {
		var s string
		switch {
		case e.Message != "":
			msg = e.Message
		case json.Unmarshal(e.Error, &s) == nil && s != "":
			msg = s
		}
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden || strings.Contains(msg, "JWT") {
		return &hranaError{Code: "AUTH", Message: strings.TrimSpace("authentication failed: the server refused the token. " + msg)}
	}
	if msg == "" || strings.HasPrefix(msg, "<") { // a web server that isn't libSQL
		msg = "the server answered " + res.Status + ": is this a libSQL server?"
	}
	return &hranaError{Code: e.Code, Message: msg}
}

type hranaResult struct {
	Cols     []hranaCol     `json:"cols"`
	Rows     [][]hranaValue `json:"rows"`
	Affected int64          `json:"affected_row_count"`
	LastID   *string        `json:"last_insert_rowid"`
}

type hranaCol struct {
	Name string  `json:"name"`
	Type *string `json:"decltype"`
}

// exec runs a statement and learns whether a transaction is open after it.
func (c *hranaConn) exec(ctx context.Context, q string, args []hranaValue) (*hranaResult, error) {
	res, err := c.post(ctx, "/v3/pipeline", map[string]any{"requests": []hranaRequest{
		{Type: "execute", Stmt: &hranaStmt{SQL: q, Args: args}}, {Type: "get_autocommit"}}})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Baton   *string `json:"baton"`
		BaseURL *string `json:"base_url"`
		Results []struct {
			Type     string      `json:"type"`
			Error    *hranaError `json:"error"`
			Response struct {
				Result     hranaResult `json:"result"`
				Autocommit *bool       `json:"is_autocommit"`
			} `json:"response"`
		} `json:"results"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, hranaReadError(ctx, err)
	}
	c.next(out.Baton, out.BaseURL)
	if len(out.Results) == 0 {
		return nil, errors.New("the server sent no result")
	}
	if len(out.Results) > 1 && out.Results[1].Response.Autocommit != nil {
		c.inTx = !*out.Results[1].Response.Autocommit
	}
	if e := out.Results[0].Error; e != nil {
		return nil, e
	}
	return &out.Results[0].Response.Result, nil
}

func (c *hranaConn) next(baton, base *string) {
	c.baton = ""
	if baton != nil {
		c.baton = *baton
	}
	if base != nil && *base != "" {
		c.base = strings.TrimRight(*base, "/")
	}
}

func hranaReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("the server's answer couldn't be read: %w", err)
}

func (c *hranaConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	vals, err := hranaArgs(args)
	if err != nil {
		return nil, err
	}
	r, err := c.exec(ctx, q, vals)
	if err != nil {
		return nil, err
	}
	return hranaDone{r}, nil
}

type hranaDone struct{ r *hranaResult }

func (d hranaDone) RowsAffected() (int64, error) { return d.r.Affected, nil }
func (d hranaDone) LastInsertId() (int64, error) {
	if d.r.LastID == nil {
		return 0, nil
	}
	return strconv.ParseInt(*d.r.LastID, 10, 64)
}

// QueryContext streams a statement's rows (a cursor), so a large table isn't
// held in memory here or refused by the server as too large an answer.
func (c *hranaConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	vals, err := hranaArgs(args)
	if err != nil {
		return nil, err
	}
	res, err := c.post(ctx, "/v3/cursor", map[string]any{"batch": map[string]any{
		"steps": []any{map[string]any{"stmt": hranaStmt{SQL: q, Args: vals, WantRows: true}}}}})
	if err != nil {
		return nil, err
	}
	r := &hranaRows{c: c, ctx: ctx, body: res.Body, lines: bufio.NewReaderSize(res.Body, 64<<10)}
	var head struct {
		Baton   *string `json:"baton"`
		BaseURL *string `json:"base_url"`
	}
	if err := r.line(&head); err != nil {
		res.Body.Close()
		return nil, hranaReadError(ctx, err)
	}
	c.next(head.Baton, head.BaseURL)
	// The columns, or why there are none.
	for r.cols == nil && !r.ended {
		if _, err := r.entry(); err != nil {
			r.Close()
			return nil, err
		}
	}
	return r, nil
}

type hranaRows struct {
	c     *hranaConn
	ctx   context.Context
	body  io.ReadCloser
	lines *bufio.Reader
	cols  []hranaCol
	whole bool // the statement ran to its end
	ended bool
}

func (r *hranaRows) line(v any) error {
	b, err := r.lines.ReadBytes('\n')
	if len(bytes.TrimSpace(b)) == 0 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	return json.Unmarshal(b, v)
}

// entry reads the cursor's next entry: a row, or nil for anything else.
func (r *hranaRows) entry() ([]hranaValue, error) {
	var e struct {
		Type  string       `json:"type"`
		Cols  []hranaCol   `json:"cols"`
		Row   []hranaValue `json:"row"`
		Error *hranaError  `json:"error"`
	}
	if err := r.line(&e); err != nil {
		r.ended = true
		if errors.Is(err, io.EOF) && r.whole {
			return nil, nil
		}
		return nil, hranaReadError(r.ctx, err) // cut off: not all the rows
	}
	switch e.Type {
	case "step_end":
		r.whole = true
	case "step_begin":
		r.cols = e.Cols
		if r.cols == nil {
			r.cols = []hranaCol{}
		}
	case "row":
		return e.Row, nil
	case "step_error", "error":
		r.whole = true
		if e.Error == nil {
			e.Error = &hranaError{Message: "the statement failed"}
		}
		return nil, e.Error
	}
	return nil, nil
}

func (r *hranaRows) Columns() []string {
	out := make([]string, len(r.cols))
	for i, c := range r.cols {
		out[i] = c.Name
	}
	return out
}

// ColumnTypeDatabaseTypeName: the type in the table's definition; computed
// columns have none.
func (r *hranaRows) ColumnTypeDatabaseTypeName(i int) string {
	if t := r.cols[i].Type; t != nil {
		return strings.ToUpper(*t)
	}
	return ""
}

func (r *hranaRows) Next(dest []driver.Value) error {
	for !r.ended {
		row, err := r.entry()
		if err != nil {
			return err
		}
		if row == nil {
			continue
		}
		for i := range dest {
			if i >= len(row) {
				dest[i] = nil
				continue
			}
			if dest[i], err = row[i].value(); err != nil {
				return err
			}
		}
		return nil
	}
	return io.EOF
}

// Close: the stream can go on only once the cursor is read to its end. In a
// transaction it must, else it is left and a new one opened.
func (r *hranaRows) Close() error {
	if !r.ended {
		if r.c.inTx {
			if _, err := io.Copy(io.Discard, r.lines); err != nil {
				r.c.baton = ""
			}
		} else if n, err := io.CopyN(io.Discard, r.lines, 64<<10); n == 64<<10 && err == nil {
			r.c.baton = ""
		}
		r.ended = true
	}
	return r.body.Close()
}

func (v hranaValue) value() (driver.Value, error) {
	switch v.Type {
	case "null":
		return nil, nil
	case "integer":
		var s string
		if err := json.Unmarshal(v.Value, &s); err != nil {
			return nil, err
		}
		return strconv.ParseInt(s, 10, 64)
	case "float":
		var f float64
		err := json.Unmarshal(v.Value, &f)
		return f, err
	case "text":
		var s string
		err := json.Unmarshal(v.Value, &s)
		return s, err
	case "blob":
		if v.Base64 == nil {
			return []byte{}, nil
		}
		return base64.RawStdEncoding.DecodeString(strings.TrimRight(*v.Base64, "="))
	}
	return nil, fmt.Errorf("unknown value type %q", v.Type)
}

func hranaArgs(args []driver.NamedValue) ([]hranaValue, error) {
	out := make([]hranaValue, len(args))
	for i, a := range args {
		raw := func(typ string, v any) {
			b, _ := json.Marshal(v)
			out[i] = hranaValue{Type: typ, Value: b}
		}
		switch x := a.Value.(type) {
		case nil:
			out[i] = hranaValue{Type: "null"}
		case int64:
			raw("integer", strconv.FormatInt(x, 10))
		case bool:
			n := "0"
			if x {
				n = "1"
			}
			raw("integer", n)
		case float64:
			raw("float", x)
		case string:
			raw("text", x)
		case []byte:
			s := base64.RawStdEncoding.EncodeToString(x)
			out[i] = hranaValue{Type: "blob", Base64: &s}
		case time.Time:
			raw("text", x.Format("2006-01-02 15:04:05.999999999-07:00"))
		default:
			return nil, fmt.Errorf("value of type %T", a.Value)
		}
	}
	return out, nil
}

func (c *hranaConn) Prepare(q string) (driver.Stmt, error) { return hranaPrepared{c, q}, nil }

type hranaPrepared struct {
	c *hranaConn
	q string
}

func (hranaPrepared) Close() error  { return nil }
func (hranaPrepared) NumInput() int { return -1 }
func (p hranaPrepared) Exec(args []driver.Value) (driver.Result, error) {
	return p.c.ExecContext(context.Background(), p.q, hranaNamed(args))
}
func (p hranaPrepared) Query(args []driver.Value) (driver.Rows, error) {
	return p.c.QueryContext(context.Background(), p.q, hranaNamed(args))
}
func (p hranaPrepared) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return p.c.ExecContext(ctx, p.q, args)
}
func (p hranaPrepared) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return p.c.QueryContext(ctx, p.q, args)
}

func hranaNamed(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, a := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: a}
	}
	return out
}

func (c *hranaConn) Ping(ctx context.Context) error {
	_, err := c.exec(ctx, "SELECT 1", nil)
	return err
}

// Close ends the stream; one that is never closed expires by itself.
func (c *hranaConn) Close() error {
	if c.baton == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if res, err := c.post(ctx, "/v3/pipeline", map[string]any{"requests": []hranaRequest{{Type: "close"}}}); err == nil {
		res.Body.Close()
	}
	c.baton, c.inTx = "", false
	return nil
}

func (c *hranaConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *hranaConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if _, err := c.exec(ctx, "BEGIN", nil); err != nil {
		return nil, err
	}
	return hranaTx{c}, nil
}

type hranaTx struct{ c *hranaConn }

func (t hranaTx) Commit() error {
	_, err := t.c.exec(context.Background(), "COMMIT", nil)
	var answer *hranaError
	if err != nil && !errors.As(err, &answer) && !errors.Is(err, errHranaLost) {
		t.c.inTx = false
		return fmt.Errorf("the answer to COMMIT was lost, so the changes may or may not have been saved: %w", err)
	}
	return err
}

// Rollback of a transaction the server already ended has nothing to do.
func (t hranaTx) Rollback() error {
	if !t.c.inTx || t.c.baton == "" {
		t.c.inTx = false
		return nil
	}
	_, err := t.c.exec(context.Background(), "ROLLBACK", nil)
	if errors.Is(err, errHranaLost) {
		return nil
	}
	return err
}
