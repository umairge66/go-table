//go:build !windows

package main

import (
	"syscall"
	"time"
)

func waitForExit(pid int, max time.Duration) {
	for end := time.Now().Add(max); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil { // gone (ESRCH)
			return
		}
	}
}
