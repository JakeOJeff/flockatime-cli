//go:build !windows

package main

import (
	"os"
	"syscall"
)

// restartSelf replaces this process with the binary at exe. exec keeps the
// PID, so systemd and launchd see one long-running agent, not a crash followed
// by a restart.
func restartSelf(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
