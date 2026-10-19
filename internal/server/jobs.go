package server

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gotable/internal/db"
	"gotable/internal/export"
	"gotable/internal/model"
)

// Job is a long-running import or export. Progress fields are read by the
// UI through GET /api/jobs/{id}.
type Job struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Status string `json:"status"` // running, done, failed, cancelled
	Phase  string `json:"phase"`
	// Done/Total measure progress in Unit ("bytes" or "rows"); Total 0 = unknown.
	Done       int64            `json:"done"`
	Total      int64            `json:"total"`
	Unit       string           `json:"unit"`
	Rows       int64            `json:"rows"`
	Statements int64            `json:"statements"`
	Failed     int64            `json:"failed"`
	Errors     []db.ImportError `json:"errors"`
	Error      *apiError        `json:"error,omitempty"`
	File       string           `json:"file,omitempty"` // download name of the result
	Size       int64            `json:"size"`
	Started    time.Time        `json:"started"`
	Finished   *time.Time       `json:"finished,omitempty"`

	mu      sync.Mutex
	done    atomic.Int64 // hot counter for streaming byte/row progress
	path    string       // result file
	cleanup []string     // files removed when the job is discarded
	cancel  context.CancelFunc
}

func (j *Job) update(f func(j *Job)) {
	j.mu.Lock()
	f(j)
	j.mu.Unlock()
}

func (j *Job) snapshot() *Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	cp := &Job{ID: j.ID, Kind: j.Kind, Title: j.Title, Status: j.Status, Phase: j.Phase, Done: j.Done, Total: j.Total,
		Unit: j.Unit, Rows: j.Rows, Statements: j.Statements, Failed: j.Failed, Errors: append([]db.ImportError{}, j.Errors...),
		Error: j.Error, File: j.File, Size: j.Size, Started: j.Started, Finished: j.Finished}
	if d := j.done.Load(); d > cp.Done {
		cp.Done = d
	}
	return cp
}

// countingReader counts bytes read into a job's progress.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// ---------------------------------------------------------------- uploads

type upload struct {
	id, name, path string
	size           int64
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	name := unsafeName.ReplaceAllString(filepath.Base(r.Header.Get("X-File-Name")), "_")
	if name == "" || name == "." {
		name = "upload"
	}
	up := &upload{id: randomID(12), name: name}
	up.path = filepath.Join(s.tmpDir, "upload-"+up.id)
	f, err := os.Create(up.path)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	n, err := io.Copy(f, r.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(up.path)
		writeErr(w, 400, fmt.Errorf("upload interrupted: %w", err))
		return
	}
	up.size = n
	s.mu.Lock()
	s.uploads[up.id] = up
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"id": up.id, "name": up.name, "size": up.size})
}

// source is the file an import reads: an upload or a local path.
type source struct {
	UploadID string `json:"uploadId"`
	Path     string `json:"path"`
}

func (s *Server) open(src source) (*os.File, int64, string, error) {
	path, name := src.Path, filepath.Base(src.Path)
	if src.UploadID != "" {
		s.mu.Lock()
		up, ok := s.uploads[src.UploadID]
		s.mu.Unlock()
		if !ok {
			return nil, 0, "", errors.New("the uploaded file is gone; choose it again")
		}
		path, name = up.path, up.name
	} else {
		path = expandHome(path)
	}
	if path == "" {
		return nil, 0, "", errors.New("choose a file to import")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, "", err
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		f.Close()
		return nil, 0, "", fmt.Errorf("%s is not a file", path)
	}
	return f, st.Size(), name, nil
}

func (s *Server) dropUpload(id string) {
	s.mu.Lock()
	up, ok := s.uploads[id]
	delete(s.uploads, id)
	s.mu.Unlock()
	if ok {
		os.Remove(up.path)
	}
}

// csvPreview detects the delimiter and returns the first records.
func (s *Server) csvPreview(w http.ResponseWriter, r *http.Request) {
	var in struct {
		source
		Delimiter string `json:"delimiter"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	f, size, name, err := s.open(in.source)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	defer f.Close()
	head := make([]byte, 256<<10)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if !utf8.Valid(head[:max(0, len(head)-4)]) {
		writeErr(w, 400, errors.New("the file isn't UTF-8 text; save it as UTF-8 CSV"))
		return
	}
	delim := in.Delimiter
	if delim == "" {
		delim = detectDelimiter(string(head))
	}
	cr := csv.NewReader(strings.NewReader(string(head)))
	cr.Comma, _ = utf8.DecodeRuneInString(delim)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	var rows [][]string
	for len(rows) < 21 {
		rec, err := cr.Read()
		if err != nil {
			break
		}
		rows = append(rows, append([]string(nil), rec...))
	}
	writeJSON(w, 200, map[string]any{"name": name, "size": size, "delimiter": delim, "rows": nonNil(rows)})
}

// detectDelimiter picks the candidate that splits the first line most.
func detectDelimiter(head string) string {
	line := head
	if i := strings.IndexAny(head, "\r\n"); i >= 0 {
		line = head[:i]
	}
	best, count := ",", 0
	for _, d := range []string{",", ";", "\t", "|"} {
		if n := strings.Count(line, d); n > count {
			best, count = d, n
		}
	}
	return best
}

// ---------------------------------------------------------------- jobs API

// listJobs returns every job, newest first.
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j.snapshot())
	}
	s.mu.Unlock()
	sort.Slice(out, func(a, b int) bool { return out[a].Started.After(out[b].Started) })
	writeJSON(w, 200, out)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j, ok := s.jobs[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		writeErr(w, 404, errors.New("job not found"))
		return
	}
	writeJSON(w, 200, j.snapshot())
}

// deleteJob cancels a running job, or discards a finished one and its file.
func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j, ok := s.jobs[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	j.mu.Lock()
	running := j.Status == "running"
	j.mu.Unlock()
	if running {
		j.cancel()
	} else {
		s.discardJob(j)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) discardJob(j *Job) {
	s.mu.Lock()
	delete(s.jobs, j.ID)
	s.mu.Unlock()
	j.mu.Lock()
	files := append(append([]string{}, j.cleanup...), j.path)
	j.mu.Unlock()
	for _, p := range files {
		if p != "" {
			os.Remove(p)
		}
	}
}

func (s *Server) downloadJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j, ok := s.jobs[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		http.Error(w, "export not found", 404)
		return
	}
	j.mu.Lock()
	status, name, path := j.Status, j.File, j.path
	j.mu.Unlock()
	if path == "" {
		http.Error(w, "export not found", 404)
		return
	}
	if status != "done" {
		http.Error(w, "export not finished", 409)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	http.ServeFile(w, r, path)
}

// startJob runs work in the background on a dedicated connection to the
// session's database, so the UI stays responsive.
func (s *Server) startJob(se *session, schema, kind, title, unit string, work func(ctx context.Context, c *db.Conn, j *Job) error) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{ID: randomID(10), Kind: kind, Title: title, Status: "running", Unit: unit, Started: time.Now(), cancel: cancel, Errors: []db.ImportError{}}
	s.mu.Lock()
	s.jobs[j.ID] = j
	s.mu.Unlock()
	cfg := *se.c().Config
	if schema == "" {
		schema = se.c().Schema()
	}
	go func() {
		defer cancel()
		err := func() error {
			j.update(func(j *Job) { j.Phase = "Connecting" })
			cred, err := db.LoadCredentials(s.secrets, cfg.ID)
			if err != nil {
				return err
			}
			cfg.SelectedSchema = schema
			c, err := db.Open(ctx, &cfg, cred)
			if err != nil {
				return err
			}
			defer c.Close()
			return work(ctx, c, j)
		}()
		now := time.Now()
		j.update(func(j *Job) {
			j.Finished = &now
			if d := j.done.Load(); d > j.Done {
				j.Done = d
			}
			switch {
			case errors.Is(err, context.Canceled) || ctx.Err() != nil && err != nil:
				j.Status, j.Phase = "cancelled", "Cancelled"
			case err != nil:
				f := Explain(err)
				j.Status, j.Phase = "failed", "Failed"
				j.Error = &apiError{Title: f.Title, Hint: f.Hint, Detail: err.Error()}
			default:
				j.Status = "done"
				if j.Total > 0 {
					j.Done = j.Total
				}
			}
		})
		j.mu.Lock()
		path := j.path
		j.mu.Unlock()
		if err != nil && path != "" {
			os.Remove(path)
		}
	}()
	return j
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in jobRequest
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	var j *Job
	switch in.Kind {
	case "export-table":
		j, err = s.exportTableJob(se, in)
	case "export-database":
		j, err = s.exportDatabaseJob(se, in)
	case "import-sql":
		j, err = s.importSQLJob(se, in)
	case "import-csv":
		j, err = s.importCSVJob(se, in)
	default:
		err = fmt.Errorf("unknown job kind %q", in.Kind)
	}
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, j.snapshot())
}

type jobRequest struct {
	Kind string `json:"kind"`
	// Export.
	Format  string   `json:"format"` // csv, json, sql (table); sql, csv-zip (database)
	Table   *tableIn `json:"table"`
	Page    pageIn   `json:"page"`
	AllRows bool     `json:"allRows"`
	Columns []string `json:"columns"`
	CSV     struct {
		Header       bool   `json:"header"`
		Delimiter    string `json:"delimiter"`
		QuoteAlways  bool   `json:"quoteAlways"`
		CRLF         bool   `json:"crlf"`
		NullAsEmpty  bool   `json:"nullAsEmpty"`
		DecimalComma bool   `json:"decimalComma"`
	} `json:"csv"`
	BatchSize  int  `json:"batchSize"`
	IncludeDDL bool `json:"includeDDL"`
	// Database export.
	Schema    string      `json:"schema"`
	Objects   []objectDTO `json:"objects"`
	Structure bool        `json:"structure"`
	Data      bool        `json:"data"`
	DropFirst bool        `json:"dropFirst"`
	// Import.
	Source          source `json:"source"`
	Transaction     bool   `json:"transaction"`
	ContinueOnError bool   `json:"continueOnError"`
	Delimiter       string `json:"delimiter"`
	Header          bool   `json:"header"`
	Mapping         []struct {
		Column string `json:"column"`
		Field  int    `json:"field"`
	} `json:"mapping"`
	EmptyAsNull bool   `json:"emptyAsNull"`
	NullText    string `json:"nullText"`
	Truncate    bool   `json:"truncate"`
}

func (in jobRequest) csvOptions() export.CSVOptions {
	o := export.DefaultCSVOptions()
	o.Header, o.NullAsEmpty, o.DecimalComma = in.CSV.Header, in.CSV.NullAsEmpty, in.CSV.DecimalComma
	if d := []rune(in.CSV.Delimiter); len(d) == 1 {
		o.Delimiter = d[0]
	}
	if in.CSV.QuoteAlways {
		o.Quote = export.QuoteAlways
	}
	if in.CSV.CRLF {
		o.LineBreak = "\r\n"
	}
	return o
}

func (s *Server) resultFile(j *Job, name string) (*os.File, error) {
	path := filepath.Join(s.tmpDir, "export-"+j.ID)
	j.update(func(j *Job) { j.File, j.path = name, path })
	return os.Create(path)
}

func (s *Server) exportTableJob(se *session, in jobRequest) (*Job, error) {
	if in.Table == nil {
		return nil, errors.New("no table given")
	}
	if in.Format != "csv" && in.Format != "json" && in.Format != "sql" {
		return nil, fmt.Errorf("unknown format %q", in.Format)
	}
	ref := db.TableRef{Schema: in.Table.Schema, Name: in.Table.Name}
	return s.startJob(se, in.Schema, "export-table", "Export "+ref.Name, "rows", func(ctx context.Context, c *db.Conn, j *Job) error {
		page := in.Page.page()
		if in.AllRows {
			page.Limit, page.Offset = 0, 0
		}
		j.update(func(j *Job) { j.Phase = "Counting rows" })
		total, err := c.CountRows(ctx, ref, page, in.Columns)
		if err != nil {
			return err
		}
		if page.Limit > 0 {
			total = min(total, int64(page.Limit))
		}
		f, err := s.resultFile(j, ref.Name+"."+in.Format)
		if err != nil {
			return err
		}
		defer f.Close()
		cw := &countingWriter{w: f}
		var wr export.Writer
		switch in.Format {
		case "csv":
			wr = export.NewCSVWriter(cw, in.csvOptions())
		case "json":
			wr = export.NewJSONWriter(cw)
		default:
			o := export.SQLOptions{Table: db.TableRef{Name: ref.Name}, BatchSize: max(in.BatchSize, 1)}
			if in.IncludeDDL {
				o.CreateDDL, _ = c.Definition(ctx, db.Object{Schema: ref.Schema, Name: ref.Name, Kind: db.KindTable})
			}
			wr = export.NewSQLWriter(cw, c.Dialect, o)
		}
		j.update(func(j *Job) { j.Phase, j.Total = "Exporting "+ref.Name, total })
		started := false
		err = c.StreamRows(ctx, ref, page, in.Columns, func(cols, _ []string, row []db.Value) error {
			if !started {
				started = true
				if err := wr.Begin(cols); err != nil {
					return err
				}
			}
			j.done.Add(1)
			return wr.Row(row)
		})
		if err != nil {
			return err
		}
		if !started {
			if err := wr.Begin(in.Columns); err != nil {
				return err
			}
		}
		if err := wr.End(); err != nil {
			return err
		}
		j.update(func(j *Job) { j.Rows, j.Size, j.Phase = j.done.Load(), cw.n, "Finished" })
		return nil
	}), nil
}

func (s *Server) exportDatabaseJob(se *session, in jobRequest) (*Job, error) {
	if len(in.Objects) == 0 {
		return nil, errors.New("choose at least one table")
	}
	if in.Format != "sql" && in.Format != "csv-zip" {
		return nil, fmt.Errorf("unknown format %q", in.Format)
	}
	if in.Format == "sql" && !in.Structure && !in.Data {
		return nil, errors.New("choose structure, data or both")
	}
	objs := make([]db.Object, len(in.Objects))
	for i, o := range in.Objects {
		objs[i] = db.Object{Schema: o.Schema, Name: o.Name, Kind: db.ObjectKind(o.Kind)}
	}
	dbName := se.c().Config.Name
	stamp := time.Now().Format("2006-01-02-1504")
	base := unsafeName.ReplaceAllString(dbName, "_") + "-" + stamp
	return s.startJob(se, in.Schema, "export-database", "Export "+dbName, "rows", func(ctx context.Context, c *db.Conn, j *Job) error {
		if in.Format == "sql" {
			f, err := s.resultFile(j, base+".sql")
			if err != nil {
				return err
			}
			defer f.Close()
			cw := &countingWriter{w: f}
			bw := newBufWriter(cw)
			err = c.Dump(ctx, bw, db.DumpOptions{Objects: objs, Structure: in.Structure, Data: in.Data, DropFirst: in.DropFirst, BatchSize: in.BatchSize},
				func(p db.DumpProgress) {
					j.update(func(j *Job) {
						j.Total, j.Rows = p.TotalRows, p.Rows
						j.done.Store(p.Rows)
						if p.Table != "" {
							j.Phase = fmt.Sprintf("Exporting %s (%d of %d)", p.Table, p.TableIndex+1, p.Tables)
						}
					})
				})
			if err == nil {
				err = bw.Flush()
			}
			j.update(func(j *Job) { j.Size = cw.n })
			return err
		}
		// One CSV per table in a ZIP archive.
		f, err := s.resultFile(j, base+".zip")
		if err != nil {
			return err
		}
		defer f.Close()
		cw := &countingWriter{w: f}
		zw := zip.NewWriter(cw)
		var total int64
		for _, o := range objs {
			if n, err := c.CountRows(ctx, db.TableRef{Schema: o.Schema, Name: o.Name}, db.Page{}, nil); err == nil {
				total += n
			}
		}
		j.update(func(j *Job) { j.Total = total })
		for i, o := range objs {
			j.update(func(j *Job) { j.Phase = fmt.Sprintf("Exporting %s (%d of %d)", o.Name, i+1, len(objs)) })
			fw, err := zw.Create(o.Name + ".csv")
			if err != nil {
				return err
			}
			wr := export.NewCSVWriter(fw, in.csvOptions())
			started := false
			err = c.StreamRows(ctx, db.TableRef{Schema: o.Schema, Name: o.Name}, db.Page{}, nil, func(cols, _ []string, row []db.Value) error {
				if !started {
					started = true
					if err := wr.Begin(cols); err != nil {
						return err
					}
				}
				j.done.Add(1)
				return wr.Row(row)
			})
			if err != nil {
				return fmt.Errorf("%s: %w", o.Name, err)
			}
			if err := wr.End(); err != nil {
				return err
			}
		}
		if err := zw.Close(); err != nil {
			return err
		}
		j.update(func(j *Job) { j.Rows, j.Size = j.done.Load(), cw.n })
		return nil
	}), nil
}

func (s *Server) importSQLJob(se *session, in jobRequest) (*Job, error) {
	f, size, name, err := s.open(in.Source)
	if err != nil {
		return nil, err
	}
	j := s.startJob(se, in.Schema, "import-sql", "Import "+name, "bytes", func(ctx context.Context, c *db.Conn, j *Job) error {
		defer f.Close()
		if c.Config.Driver == model.DriverSQLite && !in.Transaction {
			// Without a transaction every statement would wait for a disk sync.
			c.Exec(ctx, "PRAGMA synchronous = OFF")
		}
		j.update(func(j *Job) { j.Phase, j.Total = "Running statements", size })
		res, err := c.ImportSQL(ctx, countingReader{f, &j.done}, db.ImportOptions{Transaction: in.Transaction, ContinueOnError: in.ContinueOnError},
			func(r *db.ImportResult) {
				if r.Statements%50 == 0 || r.Failed > 0 {
					j.update(func(j *Job) {
						j.Statements, j.Rows, j.Failed = r.Statements, r.Rows, r.Failed
						j.Errors = append(j.Errors[:0], r.Errors...)
					})
				}
			})
		if res != nil {
			j.update(func(j *Job) {
				j.Statements, j.Rows, j.Failed = res.Statements, res.Rows, res.Failed
				j.Errors = append(j.Errors[:0], res.Errors...)
			})
		}
		return err
	})
	if in.Source.UploadID != "" {
		up := s.uploadPath(in.Source.UploadID)
		j.update(func(j *Job) { j.cleanup = append(j.cleanup, up) })
	}
	return j, nil
}

func (s *Server) uploadPath(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if up, ok := s.uploads[id]; ok {
		return up.path
	}
	return ""
}

func (s *Server) importCSVJob(se *session, in jobRequest) (*Job, error) {
	if in.Table == nil {
		return nil, errors.New("no table given")
	}
	o := db.CSVImportOptions{Header: in.Header, EmptyAsNull: in.EmptyAsNull, NullText: in.NullText, Truncate: in.Truncate}
	o.Delimiter, _ = utf8.DecodeRuneInString(in.Delimiter)
	for _, m := range in.Mapping {
		if m.Field >= 0 && m.Column != "" {
			o.Columns = append(o.Columns, m.Column)
			o.Mapping = append(o.Mapping, m.Field)
		}
	}
	if len(o.Columns) == 0 {
		return nil, errors.New("map at least one CSV column to a table column")
	}
	f, size, name, err := s.open(in.Source)
	if err != nil {
		return nil, err
	}
	ref := db.TableRef{Schema: in.Table.Schema, Name: in.Table.Name}
	j := s.startJob(se, in.Schema, "import-csv", "Import "+name+" into "+ref.Name, "bytes", func(ctx context.Context, c *db.Conn, j *Job) error {
		defer f.Close()
		j.update(func(j *Job) { j.Phase, j.Total = "Inserting rows into "+ref.Name, size })
		n, err := c.ImportCSV(ctx, countingReader{f, &j.done}, ref, o, func(rows int64) {
			j.update(func(j *Job) { j.Rows = rows })
		})
		j.update(func(j *Job) { j.Rows = n })
		if err != nil {
			j.update(func(j *Job) { j.Rows = 0 }) // rolled back
		}
		return err
	})
	if in.Source.UploadID != "" {
		up := s.uploadPath(in.Source.UploadID)
		j.update(func(j *Job) { j.cleanup = append(j.cleanup, up) })
	}
	return j, nil
}

func newBufWriter(w io.Writer) *bufio.Writer { return bufio.NewWriterSize(w, 1<<20) }
