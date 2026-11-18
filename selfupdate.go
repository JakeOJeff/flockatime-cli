package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"snapshot-agent/internal/update"
)

// updateEvery throttles release checks during a long session. One HEAD
// request to github.com an hour is nothing; checking on every capture would be
// rude for no benefit.
const updateEvery = time.Hour

// restartError asks main to restart into the binary at exe, after `run` has
// returned and released the queue file the new process needs to open.
type restartError struct{ exe string }

func (e *restartError) Error() string { return "restarting into " + e.exe }

// autoUpdater checks for a release when `run` starts and before captures,
// at most once per updateEvery.
type autoUpdater struct {
	enabled bool
	last    time.Time
}

// check installs a newer release if there is one, and returns the installed
// path when the caller should restart. Failures are logged and swallowed:
// an unreachable GitHub must never stop snapshots.
func (u *autoUpdater) check() (string, bool) {
	if !u.enabled || time.Since(u.last) < updateEvery {
		return "", false
	}
	u.last = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	latest, err := update.Latest(ctx)
	if err != nil {
		logf("update check failed: %v", err)
		return "", false
	}
	if !update.Newer(latest, agentVersion) {
		return "", false
	}
	logf("updating %s -> %s", agentVersion, latest)
	exe, err := update.Apply(ctx, latest)
	if err != nil {
		logf("update to %s failed, staying on %s: %v", latest, agentVersion, err)
		return "", false
	}
	logf("installed %s; restarting", latest)
	return exe, true
}

// cmdUpdate is the manual form: check now, install if newer. `--check` only
// reports.
func cmdUpdate(args []string) error {
	checkOnly := len(args) > 0 && args[0] == "--check"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	latest, err := update.Latest(ctx)
	if err != nil {
		return fmt.Errorf("checking for a release: %w", err)
	}
	if !update.Newer(latest, agentVersion) {
		fmt.Printf("up to date: %s (latest release %s)\n", agentVersion, latest)
		return nil
	}
	if checkOnly {
		fmt.Printf("%s is available (this is %s); run `snapshot-agent update`\n", latest, agentVersion)
		return nil
	}
	exe, err := update.Apply(ctx, latest)
	if err != nil {
		return err
	}
	fmt.Printf("updated %s -> %s at %s\n", agentVersion, latest, exe)
	fmt.Printf("a running agent keeps the old version until it restarts or its next hourly check\n")
	return nil
}

// asRestart unwraps a restartError.
func asRestart(err error) (string, bool) {
	var r *restartError
	if errors.As(err, &r) {
		return r.exe, true
	}
	return "", false
}
