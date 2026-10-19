package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// FileFilter narrows a file dialog to some extensions, e.g. {"SQL file", "*.sql"}.
type FileFilter struct {
	Name    string
	Pattern string // semicolon separated globs
}

// Dialogs shows the operating system's file dialogs. The desktop window
// provides it; without it (a plain browser) the page uses its own fallbacks.
// Both return "" when the user cancels.
type Dialogs interface {
	SaveFile(title, defaultName string, filters []FileFilter) (string, error)
	OpenFile(title string, filters []FileFilter) (string, error)
}

// SetDialogs enables native file dialogs.
func (s *Server) SetDialogs(d Dialogs) {
	s.mu.Lock()
	s.dialogs = d
	s.mu.Unlock()
}

// SetThemeHook receives the page's theme, so native parts of the window
// (e.g. GTK menus for <select>) can match it.
func (s *Server) SetThemeHook(f func(dark bool)) {
	s.mu.Lock()
	s.themeHook = f
	s.mu.Unlock()
}

// nativeTheme passes the page's light/dark theme to the window.
func (s *Server) nativeTheme(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Dark bool `json:"dark"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	f := s.themeHook
	s.mu.Unlock()
	if f != nil {
		f(in.Dark)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// SetWindowHook lets the page's own title bar move, resize, maximize,
// minimize and close the window (actions: drag, resize-<n|ne|e|…>,
// toggle-maximize, minimize, close).
func (s *Server) SetWindowHook(f func(action string) error) {
	s.mu.Lock()
	s.windowHook = f
	s.mu.Unlock()
}

func (s *Server) nativeWindow(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	f := s.windowHook
	s.mu.Unlock()
	if f == nil {
		writeErr(w, 404, errors.New("the window has a system title bar"))
		return
	}
	if err := f(in.Action); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) nativeDialogs() Dialogs {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dialogs
}

var fileKinds = map[string]FileFilter{
	"sql":    {"SQL file", "*.sql"},
	"csv":    {"CSV file", "*.csv;*.tsv;*.txt"},
	"json":   {"JSON file", "*.json"},
	"zip":    {"ZIP archive", "*.zip"},
	"gz":     {"Compressed SQL", "*.sql.gz;*.gz"},
	"sqlite": {"SQLite database", "*.sqlite;*.sqlite3;*.db"},
}

func filtersFor(kinds []string) []FileFilter {
	var out []FileFilter
	for _, k := range kinds {
		if f, ok := fileKinds[k]; ok {
			out = append(out, f)
		}
	}
	return append(out, FileFilter{"All files", "*"})
}

func (s *Server) nativeInfo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	window := s.windowHook != nil
	s.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"dialogs": s.nativeDialogs() != nil, "window": window})
}

// dialogSave asks where to save a file and returns a handle to that place;
// the path itself never reaches the page, and only places the user chose
// can be written.
func (s *Server) dialogSave(w http.ResponseWriter, r *http.Request) {
	d := s.nativeDialogs()
	if d == nil {
		writeErr(w, 404, errors.New("no native dialogs in this window"))
		return
	}
	var in struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	path, err := d.SaveFile("Save as", filepath.Base(in.Name), filtersFor([]string{in.Kind}))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if path == "" {
		writeJSON(w, 200, map[string]any{"cancelled": true})
		return
	}
	// Dialogs don't always add the extension of the chosen filter.
	if ext := "." + in.Kind; in.Kind != "" && filepath.Ext(path) == "" && fileKinds[in.Kind].Name != "" {
		path += ext
	}
	h := randomID(12)
	s.mu.Lock()
	s.saveTargets[h] = path
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"handle": h, "name": filepath.Base(path)})
}

// dialogOpen asks for a file to import and returns its path (imports
// already read local paths directly).
func (s *Server) dialogOpen(w http.ResponseWriter, r *http.Request) {
	d := s.nativeDialogs()
	if d == nil {
		writeErr(w, 404, errors.New("no native dialogs in this window"))
		return
	}
	var in struct {
		Kinds []string `json:"kinds"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	path, err := d.OpenFile("Choose a file", filtersFor(in.Kinds))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if path == "" {
		writeJSON(w, 200, map[string]any{"cancelled": true})
		return
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		writeErr(w, 400, fmt.Errorf("%s is not a file", path))
		return
	}
	writeJSON(w, 200, map[string]any{"path": path, "name": filepath.Base(path), "size": st.Size()})
}

// takeTarget returns (and forgets) the path behind a save handle.
func (s *Server) takeTarget(h string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.saveTargets[h]
	if !ok {
		return "", errors.New("choose where to save the file again")
	}
	delete(s.saveTargets, h)
	return p, nil
}

// writeFile copies src to dst via a temporary file in dst's folder, so a
// failure never leaves a half-written file behind.
func writeFile(dst string, src io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*.part")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, src)
	// Like any saved document: 0644, or the mode of the file it replaces.
	mode := os.FileMode(0o644)
	if st, serr := os.Stat(dst); serr == nil {
		mode = st.Mode().Perm()
	}
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// saveJob writes a finished export to a place chosen in a save dialog.
func (s *Server) saveJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Handle string `json:"handle"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	j, ok := s.jobs[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		writeErr(w, 404, errors.New("export not found"))
		return
	}
	j.mu.Lock()
	status, path := j.Status, j.path
	j.mu.Unlock()
	if status != "done" || path == "" {
		writeErr(w, 409, errors.New("the export isn't finished"))
		return
	}
	dst, err := s.takeTarget(in.Handle)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	defer f.Close()
	if err := writeFile(dst, f); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"name": filepath.Base(dst)})
}

// saveBody writes the request body (text made in the page, e.g. a result
// grid as CSV) to a place chosen in a save dialog.
func (s *Server) saveBody(w http.ResponseWriter, r *http.Request) {
	dst, err := s.takeTarget(strings.TrimSpace(r.URL.Query().Get("handle")))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := writeFile(dst, r.Body); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"name": filepath.Base(dst)})
}

// expandHome turns a leading ~/ (or ~\ on Windows) into the home folder.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
