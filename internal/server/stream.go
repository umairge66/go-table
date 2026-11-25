package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"gotable/internal/db"
)

// A streamed execution answers with one JSON object per line:
//
//	{"i":0,"statement":"…","columns":[…],"types":[…]}  statement 0 returns rows
//	{"i":0,"rows":[…]}                                 some of them
//	{"i":0,"end":{…}}                                  statement 0 is done
//	{"done":true,"schema":"…"}                         so is the script
//
// An error before anything ran is an ordinary error response.
type streamEvent struct {
	I         int         `json:"i"`
	Statement string      `json:"statement,omitempty"`
	Columns   []string    `json:"columns,omitempty"`
	Types     []string    `json:"types,omitempty"`
	Rows      [][]*string `json:"rows,omitempty"`
	End       *resultDTO  `json:"end,omitempty"`
	Done      bool        `json:"done,omitempty"`
	Schema    string      `json:"schema,omitempty"`
	Error     *apiError   `json:"error,omitempty"`
}

const (
	// Rows are sent together: as many as come within streamDelay, or fit.
	streamDelay = 40 * time.Millisecond
	streamRows  = 2000
	streamBytes = 512 << 10
)

// rowStream writes the events. Rows that come slowly are sent by a timer, so
// it is used by two goroutines.
type rowStream struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	enc     *json.Encoder
	started bool
	closed  bool
	err     error
	i       int
	rows    [][]*string
	bytes   int
	timer   *time.Timer
}

func newRowStream(w http.ResponseWriter) *rowStream {
	return &rowStream{w: w, enc: json.NewEncoder(w)}
}

// write writes an event without flushing it; the caller holds mu.
func (s *rowStream) write(e streamEvent) {
	if s.err != nil || s.closed {
		return
	}
	if !s.started {
		s.started = true
		s.w.Header().Set("Content-Type", "application/x-ndjson")
		s.w.Header().Set("Cache-Control", "no-store")
		s.w.WriteHeader(200)
	}
	s.err = s.enc.Encode(e)
}

// flush sends what waits; the caller holds mu.
func (s *rowStream) flush() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if len(s.rows) > 0 {
		s.write(streamEvent{I: s.i, Rows: s.rows})
		s.rows, s.bytes = nil, 0
	}
	if s.err == nil && s.started && !s.closed {
		s.err = http.NewResponseController(s.w).Flush()
	}
}

// begin isn't flushed at once: the columns arrive with the first rows,
// unless those take longer than streamDelay.
func (s *rowStream) begin(i int, stmt string, cols, types []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.i = i
	s.write(streamEvent{I: i, Statement: stmt, Columns: cols, Types: types})
	s.arm()
	return s.err
}

// arm makes sure what waits is sent soon; the caller holds mu.
func (s *rowStream) arm() {
	if s.timer != nil {
		return
	}
	s.timer = time.AfterFunc(streamDelay, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.flush()
	})
}

func (s *rowStream) row(_ int, row []db.Value) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	out := make([]*string, len(row))
	for j, v := range row {
		if !v.Null {
			t := v.Text
			out[j] = &t
			s.bytes += len(t)
		}
		s.bytes += 4
	}
	s.rows = append(s.rows, out)
	if len(s.rows) >= streamRows || s.bytes >= streamBytes {
		s.flush()
		return s.err
	}
	s.arm()
	return nil
}

// end sends an event, after the rows that wait.
func (s *rowStream) end(e streamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) > 0 {
		s.write(streamEvent{I: s.i, Rows: s.rows})
		s.rows, s.bytes = nil, 0
	}
	s.write(e)
	s.flush()
}

func (s *rowStream) hasStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// close: nothing is written after the handler returns.
func (s *rowStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
	}
}

func (s *Server) executeStream(ctx context.Context, cancel context.CancelFunc, w http.ResponseWriter, se *session, schema, script string, rowCap int) {
	out := newRowStream(w)
	defer out.close()
	var results []db.Result // without rows, for the history
	err := se.c().ExecuteStream(ctx, schema, script, rowCap, db.Sink{
		Begin: out.begin,
		Row: func(i int, row []db.Value) error {
			err := out.row(i, row)
			if err != nil {
				cancel() // the page is gone: so is the query
			}
			return err
		},
		End: func(i int, r db.Result) {
			results = append(results, r)
			d := resultOut(r)
			d.Stopped = r.Err != nil && ctx.Err() != nil
			out.end(streamEvent{I: i, End: &d})
		},
	})
	if len(results) > 0 {
		s.recordResults(se, results)
	}
	if err != nil {
		if !out.hasStarted() {
			writeErr(w, 400, err)
			return
		}
		f := Explain(err)
		out.end(streamEvent{Done: true, Error: &apiError{Title: f.Title, Hint: f.Hint, Detail: err.Error()}})
		return
	}
	out.end(streamEvent{Done: true, Schema: se.c().Schema()})
}
