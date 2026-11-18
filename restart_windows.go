//go:build windows

package main

import (
	"os"
	"os/exec"
)

// restartSelf starts the binary at exe with the same arguments and exits.
// Windows has no exec(); the child inherits this process's hidden console, so
// a login-item agent stays hidden after an update.
func restartSelf(exe string) error {
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
