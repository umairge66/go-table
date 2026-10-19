//go:build !browser

// The default build shows GoTable in a native window (Wails): WebKitGTK on
// Linux, WebView2 on Windows, WKWebView on macOS. The window loads the same
// local server a browser would, and native file dialogs are offered to the
// page through that server (see internal/server/native.go).

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"gotable/internal/server"
)

// runWindow shows GoTable and returns when its window is closed. A second
// launch brings the running window to the front instead.
func runWindow(ctx context.Context, url string, srv *server.Server) error {
	if err := requireWayland(); err != nil {
		return err
	}
	if ownTitleBar {
		prepareOwnTitleBar()
	}
	var app context.Context
	// The window starts on Wails' own origin; send it to the local server.
	target, _ := json.Marshal(url) // a JS string literal
	start := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><style>html{background:#fff}@media (prefers-color-scheme:dark){html{background:#18181b}}</style><script>location.replace(%s)</script>`, target)
	})
	go func() {
		<-ctx.Done() // Ctrl+C / SIGTERM closes the window too
		if app != nil {
			runtime.Quit(app)
		}
	}()
	return wails.Run(&options.App{
		Title:       "GoTable",
		Width:       1360,
		Height:      860,
		MinWidth:    900,
		MinHeight:   560,
		AssetServer: &assetserver.Options{Handler: start},
		OnStartup: func(c context.Context) {
			app = c
			srv.SetDialogs(dialogs{c})
			srv.SetThemeHook(setNativeTheme)
			if ownTitleBar {
				srv.SetWindowHook(func(action string) error {
					if action == "close" {
						runtime.Quit(c)
						return nil
					}
					return windowAction(action)
				})
			}
		},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "gotable-desktop",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				if app != nil {
					runtime.WindowUnminimise(app)
					runtime.Show(app)
				}
			},
		},
		// WebView2: no Ctrl+wheel / Ctrl+plus zoom (browser shortcuts are
		// already off in Wails), title bar follows the system theme.
		Windows: &windows.Options{IsZoomControlEnabled: false, Theme: windows.SystemDefault},
		Linux: &linux.Options{
			ProgramName:      "gotable",
			WebviewGpuPolicy: linux.WebviewGpuPolicyAlways, // "on demand" re-renders (blinks) whenever a popup needs compositing
		},
	})
}

// requireWayland makes GTK talk Wayland directly: GoTable doesn't support
// X11 or XWayland on Linux. It must run before GTK starts (wails.Run).
func requireWayland() error {
	if goruntime.GOOS != "linux" {
		return nil
	}
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		return errors.New("GoTable needs a Wayland session (WAYLAND_DISPLAY is not set); X11 is not supported")
	}
	// GTK panics deep inside Wails when it can't connect; check first.
	sock := name
	if !filepath.IsAbs(sock) {
		sock = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), name)
	}
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return fmt.Errorf("cannot reach the Wayland compositor at %s: %w", sock, err)
	}
	c.Close()
	os.Setenv("GDK_BACKEND", "wayland")
	// GTK_THEME would pin GTK's theme; GoTable sets light/dark itself
	// (setNativeTheme) to match the page.
	os.Unsetenv("GTK_THEME")
	// WebKitGTK's DMA-BUF renderer makes the window blink whenever a popup
	// opens (seen on Intel graphics under Wayland). The older renderer
	// keeps GPU compositing without the flicker. The web process inherits
	// this, so it must be set before wails.Run.
	if _, set := os.LookupEnv("WEBKIT_DISABLE_DMABUF_RENDERER"); !set {
		os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
	}
	return nil
}

// dialogs shows the system file dialogs for the server.
type dialogs struct{ ctx context.Context }

func filters(fs []server.FileFilter) []runtime.FileFilter {
	out := make([]runtime.FileFilter, len(fs))
	for i, f := range fs {
		out[i] = runtime.FileFilter{DisplayName: f.Name, Pattern: f.Pattern}
	}
	return out
}

func (d dialogs) SaveFile(title, name string, fs []server.FileFilter) (string, error) {
	return runtime.SaveFileDialog(d.ctx, runtime.SaveDialogOptions{Title: title, DefaultFilename: name, Filters: filters(fs), CanCreateDirectories: true})
}

func (d dialogs) OpenFile(title string, fs []server.FileFilter) (string, error) {
	return runtime.OpenFileDialog(d.ctx, runtime.OpenDialogOptions{Title: title, Filters: filters(fs)})
}
