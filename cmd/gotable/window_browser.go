//go:build browser

// The browser build shows GoTable in a Chrome/Chromium window in app mode
// instead of the native window: no C compiler or WebKitGTK needed.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"gotable/internal/server"
)

// runWindow shows GoTable and returns when its window is closed. A second
// launch opens a window on the instance that is already running.
func runWindow(ctx context.Context, url string, _ *server.Server) error {
	if goruntime.GOOS == "linux" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("GoTable needs a Wayland session (WAYLAND_DISPLAY is not set); X11 is not supported")
	}
	if running, ok := runningInstance(); ok {
		if win, err := openWindow(running); err == nil {
			win.Wait()
			return nil
		}
	}
	win, err := openWindow(url)
	if err != nil {
		return err
	}
	writeInstance(url)
	defer removeInstance(url)
	exited := make(chan struct{})
	go func() { win.Wait(); close(exited) }()
	select {
	case <-ctx.Done():
	case <-exited:
	}
	return nil
}

// instancePath is where a windowed GoTable records its URL.
func instancePath() (string, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfgDir, "gotable", "instance.json"), nil
}

type instance struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

// runningInstance returns the start URL of a GoTable that is running and
// answering with its token.
func runningInstance() (string, bool) {
	p, err := instancePath()
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	var in instance
	if json.Unmarshal(data, &in) != nil || !strings.HasPrefix(in.URL, "http://127.0.0.1:") {
		return "", false
	}
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(in.URL)
	if err != nil {
		return "", false
	}
	resp.Body.Close()
	// A valid token answers with a redirect to "/" (403 when it's wrong).
	return in.URL, resp.StatusCode < 400
}

func writeInstance(url string) {
	p, err := instancePath()
	if err != nil {
		return
	}
	data, _ := json.Marshal(instance{URL: url, PID: os.Getpid()})
	if os.MkdirAll(filepath.Dir(p), 0o700) == nil {
		os.WriteFile(p, data, 0o600)
	}
}

// removeInstance deletes the instance file if it still names this process.
func removeInstance(url string) {
	p, err := instancePath()
	if err != nil {
		return
	}
	var in instance
	if data, err := os.ReadFile(p); err == nil && json.Unmarshal(data, &in) == nil && in.URL == url {
		os.Remove(p)
	}
}

// openWindow starts a Chromium-based browser in app mode with its own
// profile, so the window has no tabs or address bar and its lifetime is
// GoTable's lifetime.
func openWindow(url string) (*exec.Cmd, error) {
	candidates := []string{"google-chrome-stable", "google-chrome", "chromium", "chromium-browser", "brave-browser", "microsoft-edge"}
	if goruntime.GOOS == "windows" {
		// Edge comes with Windows; Chrome is the usual alternative.
		candidates = nil
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
			if dir := os.Getenv(env); dir != "" {
				candidates = append(candidates, filepath.Join(dir, `Microsoft\Edge\Application\msedge.exe`),
					filepath.Join(dir, `Google\Chrome\Application\chrome.exe`))
			}
		}
		candidates = append(candidates, "msedge", "chrome")
	}
	if b := os.Getenv("GOTABLE_BROWSER"); b != "" {
		candidates = []string{b}
	}
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	profile := filepath.Join(cfgDir, "gotable", "window")
	for _, name := range candidates {
		bin, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		args := []string{"--app=" + url, "--user-data-dir=" + profile, "--class=GoTable",
			"--window-size=1360,860", "--no-first-run", "--no-default-browser-check", "--disable-features=Translate"}
		if goruntime.GOOS == "linux" {
			// Wayland only: Chrome runs as a native Wayland client, never XWayland.
			args = append(args, "--ozone-platform=wayland")
		}
		cmd := exec.Command(bin, args...)
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd, nil
	}
	return nil, errors.New("no Chrome, Chromium, Brave or Edge found (set GOTABLE_BROWSER)")
}
