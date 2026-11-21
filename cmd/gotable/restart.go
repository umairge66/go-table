package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"gotable/internal/update"
)

// quitApp ends GoTable: the window's Quit once it is open (window_wails.go),
// else the end of the -no-window wait.
var quitApp func()

// restartInto starts the updated GoTable and quits this one. The new one
// waits for this to exit (GOTABLE_WAIT_PID): until then the single-instance
// lock would send it back to this window.
func restartInto(inst update.Install) error {
	exe := inst.Path
	if inst.Kind == update.KindMac {
		exe = filepath.Join(inst.Path, "Contents", "MacOS", "gotable")
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "GOTABLE_WAIT_PID="+strconv.Itoa(os.Getpid()))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	cmd.Process.Release()
	if quitApp != nil {
		quitApp()
	}
	return nil
}

// waitForPrevious waits (up to 30 s) for the GoTable an update restarted from.
func waitForPrevious() {
	pid, err := strconv.Atoi(os.Getenv("GOTABLE_WAIT_PID"))
	os.Unsetenv("GOTABLE_WAIT_PID")
	if err != nil || pid <= 0 {
		return
	}
	waitForExit(pid, 30*time.Second)
}
