package db

import (
	"context"
	"errors"
	"fmt"
	"io"
)

var errNothingToRun = errors.New("nothing to execute")

// remote is an engine without a database/sql session (Elasticsearch,
// InfluxDB): it does itself what Conn would do with SQL. Its methods run
// with c.mu held; those of engine already do.
type remote interface {
	engine
	ping(ctx context.Context, c *Conn) error
	fetchRows(ctx context.Context, c *Conn, t TableRef, p Page) (*ResultSet, error)
	countRows(ctx context.Context, c *Conn, t TableRef, p Page) (int64, error)
	streamRows(ctx context.Context, c *Conn, t TableRef, p Page, fn func(cols, types []string, row []Value) error) error
	previewChanges(ctx context.Context, c *Conn, t TableRef, changes []Change) ([]string, error)
	applyChanges(ctx context.Context, c *Conn, t TableRef, changes []Change) error
	// split cuts a script into statements, run runs one.
	split(script string) []Span
	run(ctx context.Context, c *Conn, stmt string, rowCap int) Result
	createTable(ctx context.Context, c *Conn, schema string, t NewTable) ([]string, error)
	alter(ctx context.Context, c *Conn, t TableRef, ops []AlterOp, dryRun bool) ([]string, error)
	drop(ctx context.Context, c *Conn, objs []Object) ([]string, error)
	dump(ctx context.Context, c *Conn, w io.Writer, o DumpOptions, progress func(DumpProgress)) error
	importCSV(ctx context.Context, c *Conn, r io.Reader, t TableRef, o CSVImportOptions, progress func(int64)) (int64, error)
}

func (c *Conn) remoteExecuteIn(ctx context.Context, schema, script string, rowCap int) ([]Result, error) {
	spans := c.remote.split(script)
	if len(spans) == 0 {
		return nil, errNothingToRun
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	if schema != "" && schema != c.schema {
		if err := c.remote.useSchema(ctx, c, schema); err != nil {
			return nil, err
		}
		c.schema = schema
	}
	var out []Result
	for _, sp := range spans {
		r := c.remote.run(ctx, c, sp.SQL, rowCap)
		out = append(out, r)
		if r.Err != nil {
			break
		}
	}
	return out, nil
}

// remoteImportScript runs a script's statements one by one, as they are read.
func (c *Conn) remoteImportScript(ctx context.Context, r io.Reader, o ImportOptions, progress func(*ImportResult)) (*ImportResult, error) {
	if o.MaxErrors <= 0 {
		o.MaxErrors = 200
	}
	res := &ImportResult{Errors: []ImportError{}}
	data, err := io.ReadAll(r)
	if err != nil {
		return res, err
	}
	text := string(data)
	if err := c.lock(ctx); err != nil {
		return res, err
	}
	defer c.mu.Unlock()
	line, at := 1, 0
	for _, sp := range c.remote.split(text) {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		for ; at < sp.Start; at++ {
			if text[at] == '\n' {
				line++
			}
		}
		rr := c.remote.run(ctx, c, sp.SQL, 1)
		res.Statements++
		res.Rows += rr.RowsAffected
		if rr.Err != nil {
			res.Failed++
			if len(res.Errors) < o.MaxErrors {
				res.Errors = append(res.Errors, ImportError{Line: line, Statement: snippet(sp.SQL), Message: rr.Err.Error()})
			}
			if !o.ContinueOnError {
				return res, fmt.Errorf("line %d: %w", line, rr.Err)
			}
		}
		if progress != nil && res.Statements%20 == 0 {
			progress(res)
		}
	}
	if progress != nil {
		progress(res)
	}
	return res, nil
}

// closeRemote is Close for an engine of requests: only a tunnel stays open.
func (c *Conn) closeRemote() error {
	if c.remoteClosed {
		return nil
	}
	c.remoteClosed = true
	if c.tunnel != nil {
		return c.tunnel.Close()
	}
	return nil
}
