package server

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gotable/internal/update"
)

// updater checks GitHub for a newer GoTable and installs it (internal/update).
type updater struct {
	version string
	install update.Install
	api     string
	key     ed25519.PublicKey
	client  *http.Client
	dir     string // where downloads go
	restart func() error

	mu       sync.Mutex
	latest   *update.Release
	progress updateProgress
}

type updateProgress struct {
	// Phase: idle, downloading, installing, ready (restart to finish),
	// opened (a package handed to the installer) or failed.
	Phase   string    `json:"phase"`
	Done    int64     `json:"done"`
	Total   int64     `json:"total"`
	Version string    `json:"version,omitempty"`
	File    string    `json:"file,omitempty"`
	Error   *apiError `json:"error,omitempty"`
}

// SetVersion tells the server which GoTable it is, for updates. In tests,
// GOTABLE_UPDATE_API stands in for GitHub, with GOTABLE_UPDATE_KEY,
// GOTABLE_UPDATE_TARGET (a file to update, as the .tar.gz's binary) and
// GOTABLE_VERSION; none of them counts without GOTABLE_UPDATE_API.
func (s *Server) SetVersion(version string) {
	u := &updater{version: version, install: update.Detect(version), api: update.DefaultAPI,
		client: &http.Client{Timeout: 10 * time.Minute}, progress: updateProgress{Phase: "idle"}}
	u.key, _ = base64.StdEncoding.DecodeString(update.PublicKey)
	if api := os.Getenv("GOTABLE_UPDATE_API"); api != "" {
		u.api = api
		update.AllowHTTP()
		if k, err := base64.StdEncoding.DecodeString(os.Getenv("GOTABLE_UPDATE_KEY")); err == nil && len(k) == ed25519.PublicKeySize {
			u.key = k
		}
		if v := os.Getenv("GOTABLE_VERSION"); v != "" {
			u.version = v
		}
		if t := os.Getenv("GOTABLE_UPDATE_TARGET"); t != "" {
			u.install = update.Install{Kind: update.KindTarball, Path: t}
		}
	}
	u.dir = filepath.Join(s.tmpDir, "update")
	if dir, err := os.UserCacheDir(); err == nil {
		u.dir = filepath.Join(dir, "gotable", "updates")
	}
	update.Cleanup(u.install)
	s.updates = u
}

// SetRestartHook starts the new version and quits this one.
func (s *Server) SetRestartHook(f func() error) {
	if s.updates != nil {
		s.updates.restart = f
	}
}

// Install is this copy of GoTable, for the restart hook.
func (s *Server) Install() update.Install {
	if s.updates == nil {
		return update.Install{Kind: update.KindDev}
	}
	return s.updates.install
}

func (s *Server) updateInfo(w http.ResponseWriter, r *http.Request) {
	u := s.updates
	if u == nil {
		writeJSON(w, 200, map[string]any{"version": "dev", "kind": "dev", "how": "none"})
		return
	}
	how := "replace"
	switch u.install.Kind {
	case update.KindDeb, update.KindRPM:
		how = "installer"
	case update.KindDev:
		how = "none"
	}
	writeJSON(w, 200, map[string]any{"version": u.version, "kind": u.install.Kind, "how": how, "path": u.install.Path, "installed": u.install.Installed})
}

func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	u := s.updates
	if u == nil || u.install.Kind == update.KindDev {
		writeErr(w, 400, errors.New("this is a development build: it isn't updated from GitHub"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	rel, err := update.Latest(ctx, u.client, u.api)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	u.mu.Lock()
	u.latest = rel
	u.mu.Unlock()
	out := map[string]any{"current": u.version, "latest": rel.Version, "available": update.Newer(rel.Version, u.version),
		"notes": rel.Notes, "url": rel.URL, "published": rel.Published}
	name := update.AssetName(u.install.Kind, rel.Version)
	for _, a := range rel.Assets {
		if a.Name == name {
			out["asset"] = map[string]any{"name": a.Name, "size": a.Size}
		}
	}
	writeJSON(w, 200, out)
}

// updateInstall downloads, checks and installs the release the last check
// found, in the background; /api/update/progress follows it.
func (s *Server) updateInstall(w http.ResponseWriter, r *http.Request) {
	u := s.updates
	if u == nil || u.install.Kind == update.KindDev {
		writeErr(w, 400, errors.New("this is a development build: it isn't updated from GitHub"))
		return
	}
	u.mu.Lock()
	rel := u.latest
	busy := u.progress.Phase == "downloading" || u.progress.Phase == "installing"
	if rel == nil || !update.Newer(rel.Version, u.version) {
		u.mu.Unlock()
		writeErr(w, 400, errors.New("there is no newer version to install: check for updates first"))
		return
	}
	if !busy {
		u.progress = updateProgress{Phase: "downloading", Version: rel.Version}
	}
	u.mu.Unlock()
	if !busy {
		go u.run(rel)
	}
	writeJSON(w, 200, u.snapshot())
}

func (u *updater) run(rel *update.Release) {
	set := func(f func(p *updateProgress)) { u.mu.Lock(); f(&u.progress); u.mu.Unlock() }
	fail := func(err error) {
		f := Explain(err)
		set(func(p *updateProgress) {
			p.Phase, p.Error = "failed", &apiError{Title: "The update wasn't installed", Hint: f.Hint, Detail: err.Error()}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	file, err := update.Download(ctx, u.client, rel, u.install.Kind, u.key, u.dir, func(done, total int64) {
		set(func(p *updateProgress) { p.Done, p.Total = done, total })
	})
	if err != nil {
		fail(err)
		return
	}
	set(func(p *updateProgress) { p.Phase = "installing" })
	applied, err := update.Apply(u.install, file, rel.Version)
	if err != nil {
		fail(err)
		return
	}
	set(func(p *updateProgress) {
		p.File = applied.File
		if applied.Opened {
			p.Phase = "opened"
		} else {
			p.Phase = "ready"
		}
	})
	if applied.Restart {
		os.Remove(file) // installed; a package stays for its installer
	}
}

func (u *updater) snapshot() updateProgress {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.progress
}

func (s *Server) updateProgress(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		writeJSON(w, 200, updateProgress{Phase: "idle"})
		return
	}
	writeJSON(w, 200, s.updates.snapshot())
}

// updateRestart starts the installed version and quits this one.
func (s *Server) updateRestart(w http.ResponseWriter, r *http.Request) {
	u := s.updates
	if u == nil || u.snapshot().Phase != "ready" {
		writeErr(w, 400, errors.New("no update is waiting for a restart"))
		return
	}
	if u.restart == nil {
		writeErr(w, 400, errors.New("restart GoTable yourself to finish the update"))
		return
	}
	writeJSON(w, 200, map[string]bool{"restarting": true})
	go func() {
		time.Sleep(300 * time.Millisecond) // let the answer reach the page
		u.restart()
	}()
}

// updateOpenRelease shows the release the last check found in the system's
// browser. Only that page: the request names nothing.
func (s *Server) updateOpenRelease(w http.ResponseWriter, r *http.Request) {
	u := s.updates
	if u == nil {
		writeErr(w, 400, errors.New("check for updates first"))
		return
	}
	u.mu.Lock()
	rel := u.latest
	u.mu.Unlock()
	if rel == nil || !strings.HasPrefix(rel.URL, "https://github.com/") {
		writeErr(w, 400, errors.New("check for updates first"))
		return
	}
	if err := openBrowser(rel.URL); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"url": rel.URL})
}

// openBrowser opens a web page with the system's browser (a variable for tests).
var openBrowser = func(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}
