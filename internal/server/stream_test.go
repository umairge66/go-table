package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"gotable/internal/model"
)

// slowForever returns rows until it is stopped, a few hundred per second.
const slowForever = "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n) SELECT i, length(hex(randomblob(100000))) AS x FROM n"

func openStreamSession(t *testing.T, h *harness) string {
	t.Helper()
	h.do("PUT", "/api/connections/S1", map[string]any{"connection": map[string]any{"driver": "sqlite", "path": sqliteFile(t)}}, nil)
	var sess struct{ ID string }
	if code := h.do("POST", "/api/sessions", map[string]string{"connectionId": "S1"}, &sess); code != 200 {
		t.Fatalf("open: %d", code)
	}
	return "/api/sessions/" + sess.ID
}

// stream runs a script and hands over the events as they arrive, until fn
// returns false.
func (h *harness) stream(ctx context.Context, sid string, body map[string]any, fn func(streamEvent) bool) *http.Response {
	h.t.Helper()
	body["stream"] = true
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", h.ts.URL+sid+"/execute", bytes.NewReader(b))
	req.Header.Set("X-GoTable-Token", h.srv.Token())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var e streamEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			h.t.Fatalf("%v: %s", err, sc.Bytes())
		}
		if !fn(e) {
			break
		}
	}
	return res
}

func TestExecuteStream(t *testing.T) {
	h := newHarness(t)
	sid := openStreamSession(t, h)

	// Several statements: rows, a write, an error.
	var got []streamEvent
	rows := map[int]int{}
	res := h.stream(context.Background(), sid, map[string]any{"rowCap": 0,
		"sql": "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 5000) SELECT i, NULL AS x FROM n; CREATE TABLE gt_s (a INT); SELECT * FROM gt_s; SELECT * FROM nope; SELECT 1"},
		func(e streamEvent) bool {
			rows[e.I] += len(e.Rows)
			got = append(got, e)
			return true
		})
	if ct := res.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("content type %s", ct)
	}
	if rows[0] != 5000 || strings.Join(got[0].Columns, ",") != "i,x" || got[0].Statement == "" {
		t.Fatalf("rows %v, first %+v", rows, got[0])
	}
	if r := got[1].Rows[0]; *r[0] != "1" || r[1] != nil {
		t.Errorf("row %v", r)
	}
	ends := map[int]*resultDTO{}
	for _, e := range got {
		if e.End != nil {
			ends[e.I] = e.End
		}
	}
	if len(ends) != 4 || ends[0].Error != nil || ends[1].Error != nil || ends[2].Error != nil || ends[3].Error == nil || ends[3].Error.Title != "Table not found" || ends[3].Stopped {
		t.Fatalf("ends %+v", ends)
	}
	if last := got[len(got)-1]; !last.Done || last.Schema != "main" || last.Error != nil {
		t.Fatalf("last %+v", last)
	}
	var hist []model.HistoryEntry
	h.do("GET", "/api/history?connection=S1", nil, &hist)
	if len(hist) != 4 || hist[3].Rows != 5000 {
		t.Errorf("history %+v", hist)
	}

	// The row limit.
	n, truncated := 0, false
	h.stream(context.Background(), sid, map[string]any{"rowCap": 10, "sql": slowForever}, func(e streamEvent) bool {
		n += len(e.Rows)
		if e.End != nil {
			truncated = e.End.Truncated
		}
		return true
	})
	if n != 10 || !truncated {
		t.Errorf("limit: %d rows, truncated %v", n, truncated)
	}

	// An error before anything ran is a plain one.
	if res := h.stream(context.Background(), sid, map[string]any{"sql": "-- nothing"}, func(streamEvent) bool { return true }); res.StatusCode != 400 {
		t.Errorf("status %d", res.StatusCode)
	}
}

// TestStopStream: rows come while the query runs; stopping it keeps them, and
// a page that goes away stops it too.
func TestStopStream(t *testing.T) {
	h := newHarness(t)
	sid := openStreamSession(t, h)
	n := 0
	var end *resultDTO
	start := time.Now()
	h.stream(context.Background(), sid, map[string]any{"rowCap": 0, "sql": slowForever, "queryId": "q1"}, func(e streamEvent) bool {
		if len(e.Rows) > 0 && n == 0 {
			h.do("POST", sid+"/cancel", map[string]string{"queryId": "q1"}, nil)
		}
		n += len(e.Rows)
		if e.End != nil {
			end = e.End
		}
		return true
	})
	if n == 0 || end == nil || !end.Stopped || end.Error == nil || time.Since(start) > 20*time.Second {
		t.Fatalf("%d rows, %+v after %v", n, end, time.Since(start))
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.stream(ctx, sid, map[string]any{"rowCap": 0, "sql": slowForever}, func(e streamEvent) bool { return len(e.Rows) == 0 })
	cancel()
	done := make(chan int)
	go func() { done <- h.do("POST", sid+"/execute", map[string]any{"sql": "SELECT 1"}, nil) }()
	select {
	case code := <-done:
		if code != 200 {
			t.Errorf("after the page went away: %d", code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the query went on after the page went away")
	}
}
