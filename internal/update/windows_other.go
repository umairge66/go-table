//go:build !windows

package update

func installedByInstaller(string) bool { return false }

func recordVersion(string, string) {}
