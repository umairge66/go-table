// Package desktopentry registers GoTable with the Linux desktop when it runs
// without being installed (the .tar.gz, the AppImage, a build from source).
//
// On Wayland a task bar or dock doesn't take an app's icon from its window:
// it looks up the .desktop file named after the window's app id (gotable)
// and shows that file's icon. The .deb and .rpm install one; without it the
// shell shows a generic icon. So, when no gotable.desktop exists anywhere,
// GoTable writes its own to ~/.local/share/applications, pointing at the
// binary that runs, with the icon next to it in ~/.local/share/icons. It
// only ever changes the files it wrote, which say so (Marker), and removes
// them once an installed package provides the real ones.
package desktopentry

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Marker is in every entry GoTable writes, so it never touches anyone else's.
const Marker = "X-GoTable-Generated=true"

// Name is the app id the window reports, and so the entry's file name.
const Name = "gotable"

// Dirs are where entries are looked for: the user's data directory first
// (where GoTable writes), then the system's.
type Dirs struct {
	User   string
	System []string
}

// DefaultDirs follows the XDG base directory specification.
func DefaultDirs() (Dirs, error) {
	user := os.Getenv("XDG_DATA_HOME")
	if user == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Dirs{}, err
		}
		user = filepath.Join(home, ".local", "share")
	}
	system := filepath.SplitList(os.Getenv("XDG_DATA_DIRS"))
	if len(system) == 0 {
		system = []string{"/usr/local/share", "/usr/share"}
	}
	return Dirs{User: user, System: system}, nil
}

// Entry writes the desktop entry for the binary at exe.
func Entry(exe string) string {
	q := quote(exe)
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=GoTable\n" +
		"GenericName=Database Client\n" +
		"Comment=Browse and edit databases\n" +
		"Exec=" + q + "\n" +
		// TryExec hides the entry once the binary is gone.
		"TryExec=" + q + "\n" +
		"Icon=" + Name + "\n" +
		"Terminal=false\n" +
		"Categories=Development;Database;\n" +
		"StartupNotify=true\n" +
		"StartupWMClass=" + Name + "\n" +
		Marker + "\n"
}

// quote writes a path as the desktop entry spec wants an argument: in
// double quotes, with ", `, $ and \ escaped.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return `"` + r.Replace(s) + `"`
}

// Result says what Ensure did.
type Result string

const (
	Installed   Result = "installed"  // a package's entry exists; nothing to do
	Written     Result = "written"    // GoTable's entry was written or updated
	UpToDate    Result = "up to date" // GoTable's entry was already right
	Removed     Result = "removed"    // GoTable's entry gave way to a package's
	UserDefined Result = "user's own" // the user wrote one: left alone
)

// Ensure makes sure the desktop knows GoTable's icon, for the binary at exe
// and the PNG icon (its size in pixels, e.g. 256).
func Ensure(d Dirs, exe string, icon []byte, size int) (Result, error) {
	own := filepath.Join(d.User, "applications", Name+".desktop")
	iconPath := filepath.Join(d.User, "icons", "hicolor", itoa(size)+"x"+itoa(size), "apps", Name+".png")
	mine, err := os.ReadFile(own)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if exists && !bytes.Contains(mine, []byte(Marker)) {
		return UserDefined, nil
	}
	// A package installed the real one: ours would only hide it.
	for _, dir := range d.System {
		if _, err := os.Stat(filepath.Join(dir, "applications", Name+".desktop")); err == nil {
			if !exists {
				return Installed, nil
			}
			os.Remove(own)
			os.Remove(iconPath)
			return Removed, nil
		}
	}
	if err := writeIfChanged(iconPath, icon); err != nil {
		return "", err
	}
	entry := []byte(Entry(exe))
	if exists && bytes.Equal(mine, entry) {
		return UpToDate, nil
	}
	if err := writeIfChanged(own, entry); err != nil {
		return "", err
	}
	return Written, nil
}

func writeIfChanged(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Refresh asks the desktop to read the new entry now (KDE caches them;
// GNOME watches the directory). Best effort, in the background.
func Refresh(d Dirs) {
	for _, cmd := range [][]string{
		{"update-desktop-database", filepath.Join(d.User, "applications")},
		{"kbuildsycoca6"}, {"kbuildsycoca5"},
	} {
		if p, err := exec.LookPath(cmd[0]); err == nil {
			c := exec.Command(p, cmd[1:]...)
			if c.Start() == nil {
				go c.Wait()
			}
			if strings.HasPrefix(cmd[0], "kbuildsycoca") {
				break // one of the two is enough
			}
		}
	}
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
