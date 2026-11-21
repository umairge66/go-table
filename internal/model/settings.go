package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// SettingsStore keeps the app's preferences (theme, accent colour, editor
// font size, shortcuts) in settings.json next to the connections. The
// window's own storage can't hold them: its address changes with every
// launch. The server doesn't read them; the web UI owns their shape.
type SettingsStore struct {
	path string
	mu   sync.Mutex
}

// MaxSettingsSize bounds the file: preferences are a few kilobytes.
const MaxSettingsSize = 64 << 10

func OpenSettingsStore(path string) *SettingsStore { return &SettingsStore{path: path} }

// Get returns the saved settings, or {} when there are none (or the file
// was damaged: preferences are not worth failing for).
func (s *SettingsStore) Get() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil || !isObject(data) {
		return json.RawMessage("{}")
	}
	return data
}

// Put replaces the settings with a JSON object, written atomically.
func (s *SettingsStore) Put(data []byte) error {
	if len(data) > MaxSettingsSize {
		return errors.New("settings are too large")
	}
	if !isObject(data) {
		return errors.New("settings must be a JSON object")
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func isObject(data []byte) bool {
	var v map[string]json.RawMessage
	return json.Unmarshal(data, &v) == nil && v != nil
}
