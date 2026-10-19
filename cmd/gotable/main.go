// Command gotable is a database client for PostgreSQL, MySQL, MariaDB and
// SQLite. Its UI is served by a local HTTP server and shown in a native
// window (Wails; see window_wails.go), or in a Chrome app window with the
// "browser" build tag.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gotable/internal/model"
	"gotable/internal/server"
	"gotable/internal/webui"
)

// version is set at build time (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	storePath := flag.String("config", "", "connections file (default: $XDG_CONFIG_HOME/gotable/connections.json)")
	memSecrets := flag.Bool("no-keyring", false, "keep passwords in memory instead of the desktop keyring")
	addr := flag.String("addr", "127.0.0.1:0", "address to listen on (loopback only)")
	noWindow := flag.Bool("no-window", false, "don't open an app window; print the URL instead")
	flag.Parse()
	if *showVersion {
		fmt.Println("gotable", version)
		return
	}

	path := *storePath
	if path == "" {
		var err error
		if path, err = model.DefaultStorePath(); err != nil {
			log.Fatal(err)
		}
	}
	store, err := model.OpenStore(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gotable:", err)
		os.Exit(1)
	}
	var secrets model.Secrets = model.KeyringSecrets{}
	if *memSecrets {
		secrets = model.NewMemorySecrets()
	}

	host, _, err := net.SplitHostPort(*addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatalf("gotable: -addr must be a loopback address, got %q", *addr)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	srv := server.New(store, secrets, webui.FS())
	if dir := os.Getenv("GOTABLE_TEST_DIALOGS"); dir != "" {
		srv.SetDialogs(testDialogs{dir})
		// Window actions from the page's own title bar go to dir/window.log.
		srv.SetWindowHook(func(action string) error {
			f, err := os.OpenFile(filepath.Join(dir, "window.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = fmt.Fprintln(f, action)
			return err
		})
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go httpSrv.Serve(ln)
	url := fmt.Sprintf("http://%s/?token=%s", ln.Addr(), srv.Token())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *noWindow {
		fmt.Println("GoTable is running at", url)
		<-ctx.Done()
	} else if err := runWindow(ctx, url, srv); err != nil {
		srv.Close()
		fmt.Fprintln(os.Stderr, "gotable: could not open the app window:", err)
		fmt.Fprintln(os.Stderr, "Run `gotable -no-window` to use GoTable from a browser instead.")
		os.Exit(1)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutdown)
	srv.Close()
}

// testDialogs stands in for the system file dialogs in end-to-end tests
// (GOTABLE_TEST_DIALOGS=dir): "Save as" picks dir/saved/<name>, or cancels
// once if dir/cancel-next exists; "Open" picks the path written in
// dir/open-next.
type testDialogs struct{ dir string }

func (d testDialogs) SaveFile(_, name string, _ []server.FileFilter) (string, error) {
	if os.Remove(filepath.Join(d.dir, "cancel-next")) == nil {
		return "", nil
	}
	saved := filepath.Join(d.dir, "saved")
	return filepath.Join(saved, filepath.Base(name)), os.MkdirAll(saved, 0o700)
}

func (d testDialogs) OpenFile(string, []server.FileFilter) (string, error) {
	b, err := os.ReadFile(filepath.Join(d.dir, "open-next"))
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(b)), nil
}
