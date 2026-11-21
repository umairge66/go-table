package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
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
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v0.7.0", "v0.6.0", true},
		{"v0.6.0", "v0.7.0", false},
		{"v0.6.0", "v0.6.0", false},
		{"v0.10.0", "v0.9.9", true}, // numbers, not text
		{"v1.0.0", "v0.99.99", true},
		{"v0.7.0", "v0.7.0-rc1", true}, // a release beats its pre-release
		{"v0.7.0-rc2", "v0.7.0-rc1", true},
		{"v0.7.0-rc1", "v0.7.0", false},
		{"0.7.0", "v0.6.9", true}, // with or without v
		{"v0.7.0", "dev", true},   // a dev build is older than anything
		{"dev", "v0.1.0", false},
		{"garbage", "v0.1.0", false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

func TestAssetName(t *testing.T) {
	// The names scripts/package.sh gives the release files.
	want := map[Kind]string{
		KindAppImage: "GoTable-0.7.0-x86_64.AppImage",
		KindTarball:  "gotable-0.7.0-linux-amd64.tar.gz",
		KindDeb:      "gotable_0.7.0_amd64.deb",
		KindRPM:      "gotable-0.7.0-1.x86_64.rpm",
		KindWindows:  "gotable-0.7.0-windows-amd64.exe",
		KindMac:      "GoTable-0.7.0-macos-universal.zip",
		KindDev:      "",
	}
	for k, w := range want {
		if got := AssetName(k, "v0.7.0"); got != w {
			t.Errorf("%s: %q", k, got)
		}
	}
}

// fakeGitHub serves a release whose files are given by name; sign decides
// what SHA256SUMS.sig holds.
type fakeGitHub struct {
	*httptest.Server
	files map[string][]byte
}

func newGitHub(t *testing.T, version string, files map[string][]byte, priv ed25519.PrivateKey, sign bool) *fakeGitHub {
	t.Helper()
	testHosts = true
	t.Cleanup(func() { testHosts = false })
	g := &fakeGitHub{files: map[string][]byte{}}
	for k, v := range files {
		g.files[k] = v
	}
	if sign {
		dir := t.TempDir()
		var names []string
		for name, data := range files {
			os.WriteFile(filepath.Join(dir, name), data, 0o600)
			names = append(names, name)
		}
		sums, err := Sums(dir, names)
		if err != nil {
			t.Fatal(err)
		}
		g.files[SumsName] = sums
		g.files[SigName] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)) + "\n")
	}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			var assets []map[string]any
			for name, data := range g.files {
				assets = append(assets, map[string]any{"name": name, "browser_download_url": g.URL + "/dl/" + name, "size": len(data)})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": version, "body": "### What's new", "html_url": "https://github.com/x/releases/" + version, "assets": assets})
			return
		}
		data, ok := g.files[strings.TrimPrefix(r.URL.Path, "/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(g.Close)
	return g
}

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func tarGz(t *testing.T, files map[string]string) []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

func TestLatestAndDownload(t *testing.T) {
	ctx := context.Background()
	pub, priv := keys(t)
	archive := tarGz(t, map[string]string{"gotable-0.7.0/gotable": "#!new binary", "gotable-0.7.0/README.md": "readme"})
	g := newGitHub(t, "v0.7.0", map[string][]byte{"gotable-0.7.0-linux-amd64.tar.gz": archive, "gotable_0.7.0_amd64.deb": []byte("deb")}, priv, true)

	rel, err := Latest(ctx, g.Client(), g.URL+"/latest")
	if err != nil || rel.Version != "v0.7.0" || rel.Notes != "### What's new" || len(rel.Assets) != 4 {
		t.Fatalf("%+v %v", rel, err)
	}
	var seen int64
	dir := t.TempDir()
	path, err := Download(ctx, g.Client(), rel, KindTarball, pub, dir, func(done, total int64) { seen = done })
	if err != nil || filepath.Base(path) != "gotable-0.7.0-linux-amd64.tar.gz" || seen != int64(len(archive)) {
		t.Fatal(path, err, seen)
	}

	// Installing it replaces the binary and keeps the old one until Cleanup.
	exe := filepath.Join(t.TempDir(), "gotable")
	os.WriteFile(exe, []byte("#!old binary"), 0o755)
	applied, err := Apply(Install{Kind: KindTarball, Path: exe}, path, "v0.7.0")
	if err != nil || !applied.Restart {
		t.Fatal(applied, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "#!new binary" {
		t.Fatalf("%q", b)
	}
	if st, _ := os.Stat(exe); st.Mode().Perm()&0o100 == 0 {
		t.Fatal("not executable")
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "#!old binary" {
		t.Fatal("old version not kept")
	}
	Cleanup(Install{Kind: KindTarball, Path: exe})
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatal("old version not removed")
	}

	// A package goes to the system's installer.
	var opened string
	opener = func(p string) error { opened = p; return nil }
	defer func() { opener = func(p string) error { return nil } }()
	deb, err := Download(ctx, g.Client(), rel, KindDeb, pub, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a, err := Apply(Install{Kind: KindDeb, Path: "/usr/bin/gotable"}, deb, "v0.7.0"); err != nil || !a.Opened || opened != deb {
		t.Fatal(a, err, opened)
	}
}

func TestDownloadRefusesWhatIsntVouchedFor(t *testing.T) {
	ctx := context.Background()
	pub, priv := keys(t)
	good := map[string][]byte{"gotable-0.7.0-windows-amd64.exe": []byte("MZ new")}

	// Signed by another key.
	_, other := keys(t)
	g := newGitHub(t, "v0.7.0", good, other, true)
	rel, _ := Latest(ctx, g.Client(), g.URL+"/latest")
	if _, err := Download(ctx, g.Client(), rel, KindWindows, pub, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "signature doesn't match") {
		t.Fatal(err)
	}
	// Not signed at all.
	g = newGitHub(t, "v0.7.0", good, priv, false)
	rel, _ = Latest(ctx, g.Client(), g.URL+"/latest")
	if _, err := Download(ctx, g.Client(), rel, KindWindows, pub, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "isn't signed") {
		t.Fatal(err)
	}
	// Signed, but the file was swapped afterwards.
	g = newGitHub(t, "v0.7.0", good, priv, true)
	g.files["gotable-0.7.0-windows-amd64.exe"] = []byte("MZ evil")
	rel, _ = Latest(ctx, g.Client(), g.URL+"/latest")
	dir := t.TempDir()
	if _, err := Download(ctx, g.Client(), rel, KindWindows, pub, dir, nil); err == nil || !strings.Contains(err.Error(), "doesn't match its checksum") {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatal("the bad file was kept", left)
	}
	// No file for this kind of install.
	g = newGitHub(t, "v0.7.0", good, priv, true)
	rel, _ = Latest(ctx, g.Client(), g.URL+"/latest")
	if _, err := Download(ctx, g.Client(), rel, KindAppImage, pub, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "has no GoTable-0.7.0-x86_64.AppImage") {
		t.Fatal(err)
	}
	// Only HTTPS outside tests.
	testHosts = false
	if _, err := Download(ctx, g.Client(), rel, KindWindows, pub, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatal(err)
	}
}

func TestLatestErrors(t *testing.T) {
	for code, want := range map[int]string{404: "no release", 403: "try again in an hour", 500: "500"} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		_, err := Latest(context.Background(), s.Client(), s.URL)
		s.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%d: %v", code, err)
		}
	}
}

func TestApplyFilesAndApps(t *testing.T) {
	dir := t.TempDir()
	// AppImage / Windows: the download is the program.
	for _, k := range []Kind{KindAppImage, KindWindows} {
		target := filepath.Join(dir, string(k)+"-current")
		os.WriteFile(target, []byte("old"), 0o755)
		dl := filepath.Join(dir, string(k)+"-download")
		os.WriteFile(dl, []byte("new"), 0o600)
		if a, err := Apply(Install{Kind: k, Path: target}, dl, "v0.7.0"); err != nil || !a.Restart {
			t.Fatal(k, a, err)
		}
		if b, _ := os.ReadFile(target); string(b) != "new" {
			t.Fatal(k, string(b))
		}
	}
	// macOS: the .app folder from a .zip, with its symlinks.
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("GoTable.app/Contents/MacOS/gotable")
	w.Write([]byte("new mac"))
	h := &zip.FileHeader{Name: "GoTable.app/Contents/Resources/link"}
	h.SetMode(os.ModeSymlink | 0o777)
	lw, _ := zw.CreateHeader(h)
	lw.Write([]byte("../MacOS/gotable"))
	zw.Close()
	zipFile := filepath.Join(dir, "mac.zip")
	os.WriteFile(zipFile, zb.Bytes(), 0o600)
	app := filepath.Join(dir, "Applications", "GoTable.app")
	os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(app, "Contents", "MacOS", "gotable"), []byte("old mac"), 0o755)
	if a, err := Apply(Install{Kind: KindMac, Path: app}, zipFile, "v0.7.0"); err != nil || !a.Restart {
		t.Fatal(a, err)
	}
	if b, _ := os.ReadFile(filepath.Join(app, "Contents", "MacOS", "gotable")); string(b) != "new mac" {
		t.Fatal(string(b))
	}
	if l, err := os.Readlink(filepath.Join(app, "Contents", "Resources", "link")); err != nil || l != "../MacOS/gotable" {
		t.Fatal(l, err)
	}
	Cleanup(Install{Kind: KindMac, Path: app})
	if _, err := os.Stat(app + ".old"); !os.IsNotExist(err) {
		t.Fatal("old app kept")
	}
	// A zip whose paths leave the folder is refused.
	var evil bytes.Buffer
	ew := zip.NewWriter(&evil)
	f, _ := ew.Create("../../escape")
	f.Write([]byte("x"))
	ew.Close()
	os.WriteFile(zipFile, evil.Bytes(), 0o600)
	if _, err := Apply(Install{Kind: KindMac, Path: app}, zipFile, "v0.7.0"); err == nil || !strings.Contains(err.Error(), "bad path") {
		t.Fatal(err)
	}
	// A folder GoTable can't write to: said so, nothing changed.
	ro := filepath.Join(dir, "ro")
	os.MkdirAll(ro, 0o755)
	exe := filepath.Join(ro, "gotable")
	os.WriteFile(exe, []byte("old"), 0o755)
	os.Chmod(ro, 0o555)
	defer os.Chmod(ro, 0o755)
	if os.Getuid() != 0 {
		dl := filepath.Join(dir, "AppImage-download")
		if _, err := Apply(Install{Kind: KindAppImage, Path: exe}, dl, "v0.7.0"); err == nil || !strings.Contains(err.Error(), "can't write to") {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(exe); string(b) != "old" {
			t.Fatal("changed")
		}
	}
}

func TestParseSums(t *testing.T) {
	sums := ParseSums([]byte(strings.Repeat("a", 64) + "  file one.deb\n" + strings.Repeat("B", 64) + " *bin.exe\nbad line\n"))
	if sums["file one.deb"] != strings.Repeat("a", 64) || sums["bin.exe"] != strings.Repeat("b", 64) || len(sums) != 2 {
		t.Fatal(sums)
	}
}
