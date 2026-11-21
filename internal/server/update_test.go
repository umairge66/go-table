package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotable/internal/update"
)

// fakeRelease serves a signed release with the Linux .tar.gz, as GitHub would.
func fakeRelease(t *testing.T, version, binary string, tamper bool) (*httptest.Server, ed25519.PublicKey) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "gotable-x/gotable", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	tw.Write([]byte(binary))
	tw.Close()
	gz.Close()
	name := update.AssetName(update.KindTarball, version)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, name), b.Bytes(), 0o600)
	sums, _ := update.Sums(dir, []string{name})
	files := map[string][]byte{name: b.Bytes(), update.SumsName: sums,
		update.SigName: []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)))}
	if tamper {
		files[name] = append([]byte{}, b.Bytes()...)
		files[name][len(files[name])-1] ^= 1
	}
	var s *httptest.Server
	s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			var assets []map[string]any
			for n, d := range files {
				assets = append(assets, map[string]any{"name": n, "browser_download_url": s.URL + "/dl/" + n, "size": len(d)})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": version, "body": "notes", "html_url": "https://github.com/r", "assets": assets})
			return
		}
		w.Write(files[strings.TrimPrefix(r.URL.Path, "/dl/")])
	}))
	t.Cleanup(s.Close)
	return s, pub
}

func TestUpdateAPI(t *testing.T) {
	gh, pub := fakeRelease(t, "v0.9.0", "#!new gotable", false)
	target := filepath.Join(t.TempDir(), "gotable")
	os.WriteFile(target, []byte("#!old gotable"), 0o755)
	t.Setenv("GOTABLE_UPDATE_API", gh.URL+"/latest")
	t.Setenv("GOTABLE_UPDATE_KEY", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("GOTABLE_UPDATE_TARGET", target)
	t.Setenv("GOTABLE_VERSION", "v0.8.0")
	h := newHarness(t)
	h.srv.SetVersion("dev")
	restarted := make(chan bool, 1)
	h.srv.SetRestartHook(func() error { restarted <- true; return nil })

	var info map[string]any
	if c := h.do("GET", "/api/update", nil, &info); c != 200 || info["version"] != "v0.8.0" || info["kind"] != "tarball" || info["how"] != "replace" {
		t.Fatal(c, info)
	}
	// Nothing to install before a check, nor to restart.
	if c := h.do("POST", "/api/update/install", nil, nil); c != 400 {
		t.Fatal(c)
	}
	var check map[string]any
	if c := h.do("POST", "/api/update/check", nil, &check); c != 200 || check["available"] != true || check["latest"] != "v0.9.0" || check["notes"] != "notes" {
		t.Fatal(c, check)
	}
	if a, _ := check["asset"].(map[string]any); a["name"] != "gotable-0.9.0-linux-amd64.tar.gz" {
		t.Fatal(check["asset"])
	}
	var p updateProgress
	if c := h.do("POST", "/api/update/install", nil, &p); c != 200 || p.Phase != "downloading" {
		t.Fatal(c, p)
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && (p.Phase == "downloading" || p.Phase == "installing"); time.Sleep(20 * time.Millisecond) {
		h.do("GET", "/api/update/progress", nil, &p)
	}
	if p.Phase != "ready" || p.Total == 0 || p.Done != p.Total {
		t.Fatalf("%+v", p)
	}
	if b, _ := os.ReadFile(target); string(b) != "#!new gotable" {
		t.Fatalf("%q", b)
	}
	// The release page opens in the browser, and only it.
	var opened string
	openBrowser = func(u string) error { opened = u; return nil }
	if c := h.do("POST", "/api/update/open-release", nil, nil); c != 200 || opened != "https://github.com/r" {
		t.Fatal(c, opened)
	}
	if c := h.do("POST", "/api/update/restart", nil, nil); c != 200 {
		t.Fatal(c)
	}
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("not restarted")
	}
}

func TestUpdateAPIRefusesAndExplains(t *testing.T) {
	// A newer release whose file was changed after signing.
	gh, pub := fakeRelease(t, "v0.9.0", "#!new", true)
	target := filepath.Join(t.TempDir(), "gotable")
	os.WriteFile(target, []byte("#!old"), 0o755)
	t.Setenv("GOTABLE_UPDATE_API", gh.URL+"/latest")
	t.Setenv("GOTABLE_UPDATE_KEY", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("GOTABLE_UPDATE_TARGET", target)
	t.Setenv("GOTABLE_VERSION", "v0.8.0")
	h := newHarness(t)
	h.srv.SetVersion("dev")
	h.do("POST", "/api/update/check", nil, nil)
	var p updateProgress
	h.do("POST", "/api/update/install", nil, &p)
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && (p.Phase == "downloading" || p.Phase == "installing"); time.Sleep(20 * time.Millisecond) {
		h.do("GET", "/api/update/progress", nil, &p)
	}
	if p.Phase != "failed" || p.Error == nil || !strings.Contains(p.Error.Detail, "checksum") {
		t.Fatalf("%+v", p)
	}
	if b, _ := os.ReadFile(target); string(b) != "#!old" {
		t.Fatal("changed")
	}
	if c := h.do("POST", "/api/update/restart", nil, nil); c != 400 {
		t.Fatal(c)
	}

	// Up to date: nothing available.
	t.Setenv("GOTABLE_VERSION", "v0.9.0")
	h2 := newHarness(t)
	h2.srv.SetVersion("dev")
	var check map[string]any
	if c := h2.do("POST", "/api/update/check", nil, &check); c != 200 || check["available"] != false {
		t.Fatal(c, check)
	}

	// A development build doesn't update.
	os.Unsetenv("GOTABLE_UPDATE_API")
	h3 := newHarness(t)
	h3.srv.SetVersion("dev")
	var e struct{ Error apiError }
	if c := h3.do("POST", "/api/update/check", nil, &e); c != 400 || !strings.Contains(e.Error.Detail, "development build") {
		t.Fatal(c, e)
	}
}
