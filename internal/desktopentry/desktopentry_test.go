package desktopentry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) (Dirs, string, string) {
	root := t.TempDir()
	d := Dirs{User: filepath.Join(root, "home"), System: []string{filepath.Join(root, "usr")}}
	return d, filepath.Join(d.User, "applications", "gotable.desktop"), filepath.Join(d.User, "icons", "hicolor", "256x256", "apps", "gotable.png")
}

func TestEnsure(t *testing.T) {
	d, own, icon := setup(t)
	png := []byte("\x89PNG...")
	// Not installed: the entry and the icon are written, for this binary.
	r, err := Ensure(d, "/opt/My Apps/gotable", png, 256)
	if err != nil || r != Written {
		t.Fatal(r, err)
	}
	b, _ := os.ReadFile(own)
	for _, want := range []string{`Exec="/opt/My Apps/gotable"`, `TryExec="/opt/My Apps/gotable"`, "Icon=gotable", "StartupWMClass=gotable", Marker} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %q in\n%s", want, b)
		}
	}
	if got, _ := os.ReadFile(icon); string(got) != string(png) {
		t.Fatal("icon not written")
	}
	// Again: nothing to change. Moved: updated.
	if r, _ := Ensure(d, "/opt/My Apps/gotable", png, 256); r != UpToDate {
		t.Fatal(r)
	}
	if r, _ := Ensure(d, "/home/me/bin/gotable", png, 256); r != Written {
		t.Fatal(r)
	}
	// A package arrives: GoTable's own entry gives way to it.
	sys := filepath.Join(d.System[0], "applications", "gotable.desktop")
	os.MkdirAll(filepath.Dir(sys), 0o755)
	os.WriteFile(sys, []byte("[Desktop Entry]\nName=GoTable\n"), 0o644)
	if r, _ := Ensure(d, "/home/me/bin/gotable", png, 256); r != Removed {
		t.Fatal(r)
	}
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatal("own entry kept")
	}
	if r, _ := Ensure(d, "/home/me/bin/gotable", png, 256); r != Installed {
		t.Fatal(r)
	}
}

func TestEnsureLeavesTheUsersOwnEntry(t *testing.T) {
	d, own, _ := setup(t)
	os.MkdirAll(filepath.Dir(own), 0o755)
	mine := "[Desktop Entry]\nName=My GoTable\nExec=gotable --my-flags\n"
	os.WriteFile(own, []byte(mine), 0o644)
	if r, err := Ensure(d, "/x/gotable", []byte("png"), 256); err != nil || r != UserDefined {
		t.Fatal(r, err)
	}
	if b, _ := os.ReadFile(own); string(b) != mine {
		t.Fatal("changed the user's entry")
	}
}

func TestQuote(t *testing.T) {
	if got := quote(`/a b/$x/"q"/\`); got != `"/a b/\$x/\"q\"/\\"` {
		t.Fatal(got)
	}
}
