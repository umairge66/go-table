//go:build !browser && linux

package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"gotable/internal/desktopentry"
)

// registerDesktop gives the task bar GoTable's icon when it runs without
// being installed (see internal/desktopentry). GOTABLE_NO_DESKTOP_ENTRY=1
// turns it off.
func registerDesktop() {
	if os.Getenv("GOTABLE_NO_DESKTOP_ENTRY") != "" {
		return
	}
	// An AppImage runs from a temporary mount: the entry points at the file.
	exe := os.Getenv("APPIMAGE")
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return
		}
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
	}
	// `go run` builds into a temporary directory that is gone next time.
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return
	}
	dirs, err := desktopentry.DefaultDirs()
	if err != nil {
		return
	}
	r, err := desktopentry.Ensure(dirs, exe, appIcon, 256)
	if err != nil {
		log.Printf("desktop entry: %v", err)
		return
	}
	if r == desktopentry.Written || r == desktopentry.Removed {
		desktopentry.Refresh(dirs)
	}
}
