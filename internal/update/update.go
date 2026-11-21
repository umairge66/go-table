// Package update keeps GoTable up to date from its GitHub releases.
//
// A release carries SHA256SUMS, the checksums of its files, and
// SHA256SUMS.sig, an Ed25519 signature of that file made by the release
// pipeline (cmd/release-sign) with a key kept as a GitHub secret. GoTable
// holds the public key and installs nothing whose checksum or signature
// doesn't match, so a file swapped on GitHub or on the way can't get in.
//
// How an update is installed depends on how GoTable was: an AppImage, the
// Linux .tar.gz, the Windows .exe and the macOS app replace themselves; a
// .deb or .rpm (in /usr/bin, owned by the system) is handed to the system's
// installer.
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is where releases are published.
const Repo = "umairge66/go-table"

// DefaultAPI is GitHub's API for the latest release.
const DefaultAPI = "https://api.github.com/repos/" + Repo + "/releases/latest"

// Release is a published version.
type Release struct {
	Version   string    `json:"version"` // e.g. v0.7.0
	Notes     string    `json:"notes"`   // Markdown, from the commit messages
	URL       string    `json:"url"`     // the release page
	Published time.Time `json:"published"`
	Assets    []Asset   `json:"-"`
}

// Asset is a file of a release.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// Latest asks GitHub for the newest release (drafts and pre-releases are
// never "latest").
func Latest(ctx context.Context, client *http.Client, api string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "GoTable-updater")
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub can't be reached: %w", err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == 404:
		return nil, errors.New("no release has been published yet")
	case res.StatusCode == 403 || res.StatusCode == 429:
		return nil, errors.New("GitHub limits how often it is asked: try again in an hour")
	case res.StatusCode != 200:
		return nil, fmt.Errorf("GitHub answered %s", res.Status)
	}
	var r struct {
		Tag       string    `json:"tag_name"`
		Body      string    `json:"body"`
		HTMLURL   string    `json:"html_url"`
		Published time.Time `json:"published_at"`
		Assets    []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("GitHub's answer can't be read: %w", err)
	}
	rel := &Release{Version: r.Tag, Notes: r.Body, URL: r.HTMLURL, Published: r.Published}
	for _, a := range r.Assets {
		rel.Assets = append(rel.Assets, Asset{a.Name, a.URL, a.Size})
	}
	return rel, nil
}

// Newer reports whether version a is newer than b (v1.2.3; a pre-release
// such as v1.2.3-rc1 is older than v1.2.3). A version that can't be read,
// such as "dev", is never newer and everything is newer than it.
func Newer(a, b string) bool {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka {
		return false
	}
	if !okb {
		return true
	}
	for i := range 3 {
		if pa.n[i] != pb.n[i] {
			return pa.n[i] > pb.n[i]
		}
	}
	// 1.2.3 is newer than 1.2.3-rc1.
	if (pa.pre == "") != (pb.pre == "") {
		return pa.pre == ""
	}
	return pa.pre > pb.pre
}

type semver struct {
	n   [3]int
	pre string
}

func parse(v string) (semver, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	main, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var s semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		s.n[i] = n
	}
	s.pre = pre
	return s, true
}

// ---------------------------------------------------------------- install kinds

// Kind is how this copy of GoTable was installed.
type Kind string

const (
	KindAppImage Kind = "appimage" // Linux AppImage: the file is replaced
	KindTarball  Kind = "tarball"  // Linux .tar.gz: the binary is replaced
	KindDeb      Kind = "deb"      // Debian/Ubuntu package: the installer opens the new one
	KindRPM      Kind = "rpm"      // Fedora package: the same
	KindWindows  Kind = "windows"  // gotable.exe: replaced
	KindMac      Kind = "macos"    // GoTable.app: replaced
	KindDev      Kind = "dev"      // a development build: no updates
)

// Install is this copy: its kind and the path an update replaces (the
// binary, the AppImage file or the .app folder).
type Install struct {
	Kind Kind   `json:"kind"`
	Path string `json:"path"`
	// Installed: put there by the Windows installer (so the version shown
	// in Settings -> Apps follows updates); else a portable copy.
	Installed bool `json:"installed"`
}

// Detect finds how this copy was installed.
func Detect(version string) Install {
	if _, ok := parse(version); !ok {
		return Install{Kind: KindDev}
	}
	if img := os.Getenv("APPIMAGE"); img != "" && runtime.GOOS == "linux" {
		return Install{Kind: KindAppImage, Path: img}
	}
	exe, err := os.Executable()
	if err != nil {
		return Install{Kind: KindDev}
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	switch runtime.GOOS {
	case "windows":
		return Install{Kind: KindWindows, Path: exe, Installed: installedByInstaller(exe)}
	case "darwin":
		// .../GoTable.app/Contents/MacOS/gotable
		if app := filepath.Dir(filepath.Dir(filepath.Dir(exe))); strings.HasSuffix(app, ".app") {
			return Install{Kind: KindMac, Path: app}
		}
		return Install{Kind: KindDev}
	case "linux":
		// A package owns its file; ask the package manager which.
		if strings.HasPrefix(exe, "/usr/") {
			if exec.Command("dpkg-query", "-S", exe).Run() == nil {
				return Install{Kind: KindDeb, Path: exe}
			}
			if exec.Command("rpm", "-qf", exe).Run() == nil {
				return Install{Kind: KindRPM, Path: exe}
			}
		}
		return Install{Kind: KindTarball, Path: exe}
	}
	return Install{Kind: KindDev}
}

// AssetName is the file of a release that updates this kind of install
// (see scripts/package.sh). Version is the release's, e.g. v0.7.0.
func AssetName(k Kind, version string) string {
	v := strings.TrimPrefix(version, "v")
	switch k {
	case KindAppImage:
		return "GoTable-" + v + "-x86_64.AppImage"
	case KindTarball:
		return "gotable-" + v + "-linux-amd64.tar.gz"
	case KindDeb:
		return "gotable_" + v + "_amd64.deb"
	case KindRPM:
		return "gotable-" + v + "-1.x86_64.rpm"
	case KindWindows:
		return "gotable-" + v + "-windows-amd64.exe"
	case KindMac:
		return "GoTable-" + v + "-macos-universal.zip"
	}
	return ""
}

// ---------------------------------------------------------------- download

// SumsName and SigName are the release files that vouch for the others.
const (
	SumsName = "SHA256SUMS"
	SigName  = "SHA256SUMS.sig"
)

// Progress reports a download: bytes so far and in all.
type Progress func(done, total int64)

// Download fetches the release's file for kind into dir, and returns its
// path once its checksum is in SHA256SUMS and that file's signature checks
// out with key.
func Download(ctx context.Context, client *http.Client, rel *Release, kind Kind, key ed25519.PublicKey, dir string, progress Progress) (string, error) {
	name := AssetName(kind, rel.Version)
	asset, sums, sig := find(rel, name), find(rel, SumsName), find(rel, SigName)
	switch {
	case name == "":
		return "", errors.New("this copy of GoTable can't be updated automatically")
	case asset == nil:
		return "", fmt.Errorf("the release has no %s", name)
	case sums == nil || sig == nil:
		return "", errors.New("the release isn't signed, so it isn't installed: download it from GitHub yourself if you trust it")
	}
	sumsData, err := fetch(ctx, client, sums.URL, 1<<20)
	if err != nil {
		return "", err
	}
	sigData, err := fetch(ctx, client, sig.URL, 4<<10)
	if err != nil {
		return "", err
	}
	if err := Verify(key, sumsData, sigData); err != nil {
		return "", err
	}
	want, ok := ParseSums(sumsData)[name]
	if !ok {
		return "", fmt.Errorf("%s isn't in the release's checksums", name)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := download(ctx, client, asset.URL, path, asset.Size, progress); err != nil {
		return "", err
	}
	got, err := fileSum(path)
	if err != nil {
		return "", err
	}
	if got != want {
		os.Remove(path)
		return "", fmt.Errorf("%s doesn't match its checksum: it was not installed", name)
	}
	return path, nil
}

func find(rel *Release, name string) *Asset {
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			return &rel.Assets[i]
		}
	}
	return nil
}

// Verify checks SHA256SUMS against its signature (base64 of Ed25519).
func Verify(key ed25519.PublicKey, sums, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, sums, raw) {
		return errors.New("the release's signature doesn't match GoTable's key: it was not installed")
	}
	return nil
}

// ParseSums reads "checksum  name" lines, as sha256sum writes them.
func ParseSums(data []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		sum, name, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if ok && len(sum) == 64 {
			out[strings.TrimPrefix(strings.TrimSpace(name), "*")] = strings.ToLower(sum)
		}
	}
	return out
}

// Sums writes SHA256SUMS for the files in dir (cmd/release-sign uses it).
func Sums(dir string, names []string) ([]byte, error) {
	var b bytes.Buffer
	for _, n := range names {
		s, err := fileSum(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%s  %s\n", s, n)
	}
	return b.Bytes(), nil
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// allowedHost: downloads come from GitHub (and the storage it redirects to).
func allowedHost(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" && !testHosts {
		return fmt.Errorf("refusing to download from %q", raw)
	}
	return nil
}

// testHosts lets tests serve releases over plain HTTP.
var testHosts = false

// AllowHTTP lets downloads use plain HTTP: for end-to-end tests that stand
// in for GitHub (GOTABLE_UPDATE_API), never otherwise.
func AllowHTTP() { testHosts = true }

func fetch(ctx context.Context, client *http.Client, raw string, limit int64) ([]byte, error) {
	if err := allowedHost(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "GoTable-updater")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("download failed: %s", res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}

func download(ctx context.Context, client *http.Client, raw, path string, size int64, progress Progress) error {
	if err := allowedHost(raw); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "GoTable-updater")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("download failed: %s", res.Status)
	}
	if res.ContentLength > 0 {
		size = res.ContentLength
	}
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				return err
			}
			done += int64(n)
			if progress != nil {
				progress(done, size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return fmt.Errorf("download failed: %w", rerr)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------------------------------------------------------------- install

// Applied says what Apply did.
type Applied struct {
	// Restart: the new version is in place and starts with the next launch.
	Restart bool `json:"restart"`
	// Opened: the package was handed to the system's installer (deb, rpm).
	Opened bool   `json:"opened"`
	File   string `json:"file,omitempty"`
}

// opener opens a package with the desktop's installer (a variable for tests).
var opener = func(path string) error {
	return exec.Command("xdg-open", path).Start()
}

// Apply installs a verified download of version over inst.
func Apply(inst Install, file, version string) (Applied, error) {
	switch inst.Kind {
	case KindDeb, KindRPM:
		if err := opener(file); err != nil {
			return Applied{File: file}, fmt.Errorf("the installer couldn't be opened (%v): install %s yourself", err, file)
		}
		return Applied{Opened: true, File: file}, nil
	case KindAppImage, KindWindows:
		err := replaceFile(inst.Path, func(w io.Writer) error {
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(w, f)
			return err
		})
		if err == nil && inst.Kind == KindWindows {
			recordVersion(inst.Path, version)
		}
		return Applied{Restart: true}, err
	case KindTarball:
		return Applied{Restart: true}, replaceFile(inst.Path, func(w io.Writer) error { return extractTar(file, "gotable", w) })
	case KindMac:
		return Applied{Restart: true}, replaceApp(inst.Path, file)
	}
	return Applied{}, errors.New("this copy of GoTable can't be updated automatically")
}

// replaceFile writes a new file next to path and swaps it in. A running
// program can be renamed (Windows can't overwrite it): the old one is kept
// as path.old until the next start (Cleanup).
func replaceFile(path string, write func(io.Writer) error) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gotable-update-*")
	if err != nil {
		return fmt.Errorf("GoTable can't write to %s (%w): download the update from GitHub, or move GoTable to a folder you own", dir, err)
	}
	defer os.Remove(tmp.Name())
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	old := path + ".old"
	os.Remove(old)
	if err := os.Rename(path, old); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Rename(old, path) // put it back
		return err
	}
	return nil
}

// extractTar copies the file named name (in any folder) out of a .tar.gz.
func extractTar(archive, name string, w io.Writer) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("the archive has no %s", name)
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			_, err := io.Copy(w, io.LimitReader(tr, 1<<30))
			return err
		}
	}
}

// replaceApp unpacks GoTable.app from a .zip next to the current one and
// swaps them, keeping the old as GoTable.app.old until the next start.
func replaceApp(app, zipFile string) error {
	dir := filepath.Dir(app)
	staging, err := os.MkdirTemp(dir, ".gotable-update-")
	if err != nil {
		return fmt.Errorf("GoTable can't write to %s (%w): download the update from GitHub", dir, err)
	}
	defer os.RemoveAll(staging)
	if err := unzip(zipFile, staging); err != nil {
		return err
	}
	fresh := filepath.Join(staging, "GoTable.app")
	if _, err := os.Stat(filepath.Join(fresh, "Contents", "MacOS")); err != nil {
		return errors.New("the download has no GoTable.app")
	}
	old := app + ".old"
	os.RemoveAll(old)
	if err := os.Rename(app, old); err != nil {
		return err
	}
	if err := os.Rename(fresh, app); err != nil {
		os.Rename(old, app)
		return err
	}
	return nil
}

func unzip(file, dest string) error {
	r, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		path := filepath.Join(dest, f.Name)
		// No entry may leave dest.
		if rel, err := filepath.Rel(dest, path); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("the archive has a bad path: %s", f.Name)
		}
		switch {
		case f.FileInfo().IsDir():
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case f.Mode()&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return err
			}
			target, _ := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(string(target), path); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode().Perm()|0o600)
			if err == nil {
				_, err = io.Copy(out, rc)
				if cerr := out.Close(); err == nil {
					err = cerr
				}
			}
			rc.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Cleanup removes what an update left: the previous version (.old).
func Cleanup(inst Install) {
	if inst.Path == "" {
		return
	}
	switch inst.Kind {
	case KindMac:
		os.RemoveAll(inst.Path + ".old")
	case KindAppImage, KindTarball, KindWindows:
		os.Remove(inst.Path + ".old")
	}
}
