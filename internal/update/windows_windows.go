//go:build windows

package update

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// uninstallKey is the installer's entry in Settings -> Apps
// (packaging/windows/gotable.nsi), for the current user.
const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\GoTable`

// installedByInstaller: the installer put this gotable.exe where it runs.
func installedByInstaller(exe string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	dir, _, err := k.GetStringValue("InstallLocation")
	return err == nil && strings.EqualFold(filepath.Clean(dir), filepath.Dir(exe))
}

// recordVersion shows the new version in Settings -> Apps after an update.
func recordVersion(exe, version string) {
	if !installedByInstaller(exe) {
		return
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	k.SetStringValue("DisplayVersion", strings.TrimPrefix(version, "v"))
}
