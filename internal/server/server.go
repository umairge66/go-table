// Package server exposes GoTable's connection store and database sessions as
// a local JSON API and serves the embedded web UI.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gotable/internal/db"
	"gotable/internal/model"
)

const tokenCookie = "gotable_token"

// Server holds the app state behind the HTTP API.
type Server struct {
	store   *model.Store
	secrets model.Secrets
	token   string
	static  fs.FS

	mu       sync.Mutex
	sessions map[string]*session
	jobs     map[string]*Job
	uploads  map[string]*upload
	tmpDir   string // uploads and export results; removed by Close

	queries *model.QueryStore   // saved queries
	history *model.HistoryStore // executed statements

	dialogs     Dialogs            // native file dialogs (desktop window only)
	themeHook   func(dark bool)    // native widgets follow the page theme
	windowHook  func(string) error // the page's own title bar (frameless window)
	saveTargets map[string]string  // save-dialog handle -> chosen path
}

type session struct {
	id       string
	database string // database name at open, for history
	// cur is the open connection. It is replaced when the user switches to
	// another database on the same server, so it is read atomically.
	cur atomic.Pointer[db.Conn]

	mu      sync.Mutex
	running map[string]context.CancelFunc // query ID -> cancel
}

// c returns the session's current connection.
func (se *session) c() *db.Conn { return se.cur.Load() }

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// New creates a server. static is the web UI (may be nil in tests).
func New(store *model.Store, secrets model.Secrets, static fs.FS) *Server {
	// Uploads and exports can be gigabytes: keep them on disk in the user's
	// cache directory rather than /tmp, which is often RAM-backed (tmpfs).
	base := os.TempDir()
	if dir, err := os.UserCacheDir(); err == nil {
		if os.MkdirAll(filepath.Join(dir, "gotable"), 0o700) == nil {
			base = filepath.Join(dir, "gotable")
		}
	}
	cleanWorkDirs(base)
	tmp, err := os.MkdirTemp(base, fmt.Sprintf("work-%d-", os.Getpid()))
	if err != nil {
		panic(err)
	}
	// Saved queries and history live next to the connections file.
	dir := filepath.Dir(store.Path())
	queries, err := model.OpenQueryStore(filepath.Join(dir, "queries.json"))
	if err != nil {
		queries, _ = model.OpenQueryStore(filepath.Join(tmp, "queries.json"))
	}
	history, err := model.OpenHistoryStore(filepath.Join(dir, "history.jsonl"), 1000)
	if err != nil {
		history, _ = model.OpenHistoryStore(filepath.Join(tmp, "history.jsonl"), 1000)
	}
	return &Server{store: store, secrets: secrets, token: randomID(24), static: static, sessions: map[string]*session{},
		jobs: map[string]*Job{}, uploads: map[string]*upload{}, tmpDir: tmp, queries: queries, history: history,
		saveTargets: map[string]string{}}
}

// Token is the secret the UI must present; the launcher passes it once in
// the start URL and the server turns it into a cookie.
func (s *Server) Token() string { return s.token }

// Close cancels running jobs, disconnects every session and removes
// temporary files.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		j.cancel()
	}
	for id, se := range s.sessions {
		se.c().Close()
		delete(s.sessions, id)
	}
	os.RemoveAll(s.tmpDir)
}

// Handler returns the HTTP handler with authentication applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	if s.static != nil {
		files := http.FileServerFS(s.static)
		mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A source checkout has only a placeholder until the UI is built.
			if _, err := fs.Stat(s.static, "index.html"); err != nil {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				io.WriteString(w, "GoTable's web UI isn't built into this binary.\nRun `make build` (or `make web`, then go build) from the source folder.\n")
				return
			}
			// Unknown paths fall back to the single page app.
			if r.URL.Path != "/" {
				if _, err := fs.Stat(s.static, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
					r.URL.Path = "/"
				}
			}
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
		}))
	}
	return s.guard(mux)
}

// localHost reports whether the Host header names the loopback interface,
// which defeats DNS rebinding.
func localHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1" || h == "[::1]"
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		// Exchange the one-time start URL token for a cookie.
		if t := r.URL.Query().Get("token"); t != "" && r.URL.Path == "/" {
			if subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) != 1 {
				http.Error(w, "invalid token", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: tokenCookie, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			// Continue from this page rather than with an HTTP redirect: a
			// window that arrives from another origin (the desktop shell)
			// would carry that origin through the redirect, and the
			// SameSite=Strict cookie wouldn't be sent. A navigation started
			// here is same-site.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>GoTable</title><script>location.replace("/")</script>`)
			return
		}
		if !s.authorized(r) {
			http.Error(w, "unauthorized: start GoTable from its launcher", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" && !localHost(strings.TrimPrefix(strings.TrimPrefix(o, "http://"), "https://")) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	t := r.Header.Get("X-GoTable-Token")
	if c, err := r.Cookie(tokenCookie); err == nil && t == "" {
		t = c.Value
	}
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

// apiError is the JSON error body: a plain-language explanation plus the
// raw message.
type apiError struct {
	Title  string `json:"title"`
	Hint   string `json:"hint,omitempty"`
	Detail string `json:"detail"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	f := Explain(err)
	writeJSON(w, status, map[string]apiError{"error": {Title: f.Title, Hint: f.Hint, Detail: err.Error()}})
}

var errNotFound = errors.New("not found")

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20))
	return dec.Decode(v)
}

func (s *Server) session(r *http.Request) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, ok := s.sessions[r.PathValue("sid")]
	if !ok {
		return nil, errors.New("this connection was closed; connect again")
	}
	return se, nil
}

// cleanWorkDirs removes work folders left by GoTable processes that are gone
// (killed, crashed, or a second launch that handed over to the running
// window and exited at once). Folders named without a process id (older
// versions) go once they are a week old.
func cleanWorkDirs(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, "work-") {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(name, "work-"), "-", 2)
		if pid, err := strconv.Atoi(parts[0]); err == nil && len(parts) == 2 {
			if !processAlive(pid) {
				os.RemoveAll(filepath.Join(base, name))
			}
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 7*24*time.Hour {
			os.RemoveAll(filepath.Join(base, name))
		}
	}
}

func processAlive(pid int) bool {
	if pid == os.Getpid() {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // FindProcess already failed for missing processes
	}
	return p.Signal(syscall.Signal(0)) == nil
}
