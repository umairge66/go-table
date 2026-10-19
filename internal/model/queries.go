package model

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SavedQuery is a named query kept for a connection and database.
type SavedQuery struct {
	ID           string    `json:"id"`
	ConnectionID string    `json:"connectionId"`
	Database     string    `json:"database"`
	Schema       string    `json:"schema"`
	Name         string    `json:"name"`
	SQL          string    `json:"sql"`
	Updated      time.Time `json:"updated"`
}

// QueryStore persists saved queries as a JSON file.
type QueryStore struct {
	mu      sync.Mutex
	path    string
	queries []*SavedQuery
}

// OpenQueryStore loads path; a missing file is an empty store.
func OpenQueryStore(path string) (*QueryStore, error) {
	s := &QueryStore{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.queries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

func (s *QueryStore) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.queries, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns a connection's queries, most recently updated first.
func (s *QueryStore) List(connID string) []*SavedQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*SavedQuery{}
	for _, q := range s.queries {
		if q.ConnectionID == connID {
			cp := *q
			out = append(out, &cp)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// Upsert inserts or replaces a query (by ID) and saves the file.
func (s *QueryStore) Upsert(q SavedQuery) (*SavedQuery, error) {
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" {
		return nil, errors.New("give the query a name")
	}
	if q.ConnectionID == "" {
		return nil, errors.New("connection is required")
	}
	if q.ID == "" {
		q.ID = NewID()
	}
	q.Updated = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	replaced := false
	for i, old := range s.queries {
		if old.ID == q.ID {
			s.queries[i] = &q
			replaced = true
		}
	}
	if !replaced {
		s.queries = append(s.queries, &q)
	}
	cp := q
	return &cp, s.save()
}

// Delete removes a query.
func (s *QueryStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queries[:0]
	for _, q := range s.queries {
		if q.ID != id {
			out = append(out, q)
		}
	}
	s.queries = out
	return s.save()
}

// HistoryEntry is one executed statement.
type HistoryEntry struct {
	ID           string    `json:"id"`
	ConnectionID string    `json:"connectionId"`
	Database     string    `json:"database"`
	Schema       string    `json:"schema"`
	Kind         string    `json:"kind"` // query or commit
	SQL          string    `json:"sql"`
	At           time.Time `json:"at"`
	Ms           float64   `json:"ms"`
	Rows         int64     `json:"rows"`
	Error        string    `json:"error,omitempty"`
}

// HistoryStore keeps executed statements in an append-only JSON-lines
// file, capped per connection.
type HistoryStore struct {
	mu      sync.Mutex
	path    string
	entries []HistoryEntry // oldest first
	limit   int            // per connection
}

// historyMaxSQL bounds one entry so a pasted multi-megabyte script doesn't
// bloat the file.
const historyMaxSQL = 64 << 10

// OpenHistoryStore loads path; a missing file is an empty history.
func OpenHistoryStore(path string, perConnection int) (*HistoryStore, error) {
	h := &HistoryStore{path: path, limit: perConnection}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), historyMaxSQL*4)
	for sc.Scan() {
		var e HistoryEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			h.entries = append(h.entries, e)
		}
	}
	h.trim()
	return h, nil
}

// trim keeps the newest limit entries of each connection.
func (h *HistoryStore) trim() bool {
	count := map[string]int{}
	keep := make([]bool, len(h.entries))
	for i := len(h.entries) - 1; i >= 0; i-- {
		c := h.entries[i].ConnectionID
		if count[c] < h.limit {
			keep[i] = true
			count[c]++
		}
	}
	out := h.entries[:0]
	for i, e := range h.entries {
		if keep[i] {
			out = append(out, e)
		}
	}
	trimmed := len(out) != len(h.entries)
	h.entries = out
	return trimmed
}

func (h *HistoryStore) rewrite() error {
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return err
	}
	tmp := h.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range h.entries {
		enc.Encode(e)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, h.path)
}

// Add records entries (newest last).
func (h *HistoryStore) Add(entries ...HistoryEntry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range entries {
		if entries[i].ID == "" {
			entries[i].ID = NewID()
		}
		if len(entries[i].SQL) > historyMaxSQL {
			entries[i].SQL = entries[i].SQL[:historyMaxSQL] + "\n-- (truncated)"
		}
	}
	h.entries = append(h.entries, entries...)
	if h.trim() {
		return h.rewrite()
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, e := range entries {
		enc.Encode(e)
	}
	return f.Close()
}

// List returns a connection's history, newest first, optionally filtered
// by text (case-insensitive) and capped at limit.
func (h *HistoryStore) List(connID, text string, limit int) []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	text = strings.ToLower(strings.TrimSpace(text))
	out := []HistoryEntry{}
	for i := len(h.entries) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		e := h.entries[i]
		if e.ConnectionID != connID {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(e.SQL), text) && !strings.Contains(strings.ToLower(e.Database+" "+e.Schema), text) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Clear removes a connection's history.
func (h *HistoryStore) Clear(connID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.entries[:0]
	for _, e := range h.entries {
		if e.ConnectionID != connID {
			out = append(out, e)
		}
	}
	h.entries = out
	return h.rewrite()
}
