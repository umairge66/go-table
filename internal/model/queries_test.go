package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueryStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queries.json")
	s, err := OpenQueryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(SavedQuery{ConnectionID: "C1", Name: "  "}); err == nil {
		t.Fatal("empty name must fail")
	}
	a, err := s.Upsert(SavedQuery{ConnectionID: "C1", Database: "shop", Name: " Top customers ", SQL: "SELECT 1"})
	if err != nil || a.ID == "" || a.Name != "Top customers" {
		t.Fatalf("%+v %v", a, err)
	}
	time.Sleep(5 * time.Millisecond)
	s.Upsert(SavedQuery{ConnectionID: "C1", Name: "Second", SQL: "SELECT 2"})
	s.Upsert(SavedQuery{ConnectionID: "C2", Name: "Other connection"})
	a.SQL = "SELECT 42"
	time.Sleep(5 * time.Millisecond)
	s.Upsert(*a)

	s2, _ := OpenQueryStore(path)
	list := s2.List("C1")
	if len(list) != 2 || list[0].Name != "Top customers" || list[0].SQL != "SELECT 42" {
		t.Fatalf("reloaded %+v", list)
	}
	s2.Delete(a.ID)
	if l := s2.List("C1"); len(l) != 1 || l[0].Name != "Second" {
		t.Fatalf("after delete %+v", l)
	}
}

func TestHistoryStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	h, err := OpenHistoryStore(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		h.Add(HistoryEntry{ConnectionID: "C1", SQL: "SELECT " + strings.Repeat("x", i)})
	}
	h.Add(HistoryEntry{ConnectionID: "C2", SQL: "SELECT other", Error: "boom"})
	got := h.List("C1", "", 0)
	if len(got) != 3 || got[0].SQL != "SELECT xxxxx" || got[2].SQL != "SELECT xxx" {
		t.Fatalf("capped list %+v", got)
	}
	if l := h.List("C1", "XXXX", 0); len(l) != 2 {
		t.Fatalf("search %+v", l)
	}
	// Reload from disk keeps the same view.
	h2, _ := OpenHistoryStore(path, 3)
	if l := h2.List("C1", "", 0); len(l) != 3 || l[0].SQL != "SELECT xxxxx" {
		t.Fatalf("reloaded %+v", l)
	}
	if l := h2.List("C2", "", 0); len(l) != 1 || l[0].Error != "boom" {
		t.Fatalf("other connection %+v", l)
	}
	h2.Clear("C1")
	h3, _ := OpenHistoryStore(path, 3)
	if len(h3.List("C1", "", 0)) != 0 || len(h3.List("C2", "", 0)) != 1 {
		t.Fatal("clear should only remove C1")
	}
	// Huge statements are truncated.
	h3.Add(HistoryEntry{ConnectionID: "C3", SQL: strings.Repeat("a", historyMaxSQL+10)})
	if e := h3.List("C3", "", 1)[0]; !strings.HasSuffix(e.SQL, "(truncated)") {
		t.Fatal("expected truncation")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("history file mode %v", fi.Mode())
	}
}
