package model

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.json")
	s := OpenSettingsStore(path)
	if got := string(s.Get()); got != "{}" {
		t.Fatal(got)
	}
	if err := s.Put([]byte(`{"theme":"dark","shortcuts":{"run":"ctrl+e"}}`)); err != nil {
		t.Fatal(err)
	}
	// Kept across opens, as the app's next launch reads it.
	if got := string(OpenSettingsStore(path).Get()); got != "{\n  \"theme\": \"dark\",\n  \"shortcuts\": {\n    \"run\": \"ctrl+e\"\n  }\n}" {
		t.Fatal(got)
	}
	for _, bad := range []string{`[]`, `"x"`, `null`, `{`, string(make([]byte, MaxSettingsSize+1))} {
		if err := s.Put([]byte(bad)); err == nil {
			t.Errorf("%.10q: expected an error", bad)
		}
	}
	// A damaged file reads as no settings rather than failing.
	os.WriteFile(path, []byte("{oops"), 0o600)
	if got := string(s.Get()); got != "{}" {
		t.Fatal(got)
	}
}
