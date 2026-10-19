package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Store persists connections and groups to a JSON file.
type Store struct {
	mu          sync.Mutex
	path        string
	Connections []*Connection `json:"connections"`
	Groups      []*Group      `json:"groups"`
}

// DefaultStorePath returns $XDG_CONFIG_HOME/gotable/connections.json.
func DefaultStorePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gotable", "connections.json"), nil
}

// OpenStore loads the store at path. A missing file yields an empty store.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// Path returns the file the store is saved to.
func (s *Store) Path() string { return s.path }

// Save writes the store atomically.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Connection returns the connection with the given ID.
func (s *Store) Connection(id string) *Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.Connections {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// Upsert inserts c or replaces the connection with the same ID.
func (s *Store) Upsert(c *Connection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, old := range s.Connections {
		if old.ID == c.ID {
			s.Connections[i] = c
			return
		}
	}
	s.Connections = append(s.Connections, c)
}

// Delete removes the connection with the given ID.
func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.Connections[:0]
	for _, c := range s.Connections {
		if c.ID != id {
			out = append(out, c)
		}
	}
	s.Connections = out
}

// AddGroup creates a group with the given name.
func (s *Store) AddGroup(name string) *Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := &Group{ID: NewID(), Name: name, Expanded: true}
	s.Groups = append(s.Groups, g)
	return g
}

// DeleteGroup removes a group and moves its connections to the top level.
func (s *Store) DeleteGroup(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.Groups[:0]
	for _, g := range s.Groups {
		if g.ID != id {
			out = append(out, g)
		}
	}
	s.Groups = out
	for _, c := range s.Connections {
		if c.GroupID == id {
			c.GroupID = ""
		}
	}
}

// InGroup returns the connections in group id ("" = ungrouped), sorted by
// name.
func (s *Store) InGroup(id string) []*Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Connection
	for _, c := range s.Connections {
		if c.GroupID == id {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
