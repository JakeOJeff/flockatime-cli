// Package gitstate reads repository state by shelling out to git. No git
// library is vendored for this.
//
// The repositories are whatever WakaTime saw you open, so their .git/config
// is untrusted: a downloaded archive can carry one that names a command for
// git to run. Every call here is arranged so that config cannot run anything.
package gitstate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// State mirrors the git block of a snapshot payload.
type State struct {
	Head   string
	Branch string
	Dirty  bool
	Ahead  int
}

// Read returns the state of the repo at root, or (nil, nil) if root has no
// .git. A repo with no commits yet reports an empty Head.
func Read(root string) (*State, error) {
	if fi, err := os.Stat(filepath.Join(root, ".git")); err != nil || (!fi.IsDir() && fi.Size() == 0) {
		return nil, nil
	}

	s := &State{}
	s.Head, _ = run(root, "rev-parse", "HEAD")
	s.Branch, _ = run(root, "rev-parse", "--abbrev-ref", "HEAD")
	if s.Branch == "HEAD" {
		s.Branch = "" // detached
	}

	// --porcelain prints one line per changed path. We only count whether
	// there is any output at all --- the paths themselves are discarded.
	//
	// status can run a repository's filter drivers to re-check a file, so it
	// is skipped when the repository defines its own; Dirty stays false.
	// Submodules are left out because their configs are not checked.
	if !hasRepoFilters(root) {
		status, err := run(root, "status", "--porcelain", "--ignore-submodules=all")
		if err == nil {
			s.Dirty = status != ""
		}
	}

	// Fails when there is no upstream configured, which leaves Ahead at 0.
	if out, err := run(root, "rev-list", "--count", "@{u}..HEAD"); err == nil {
		s.Ahead, _ = strconv.Atoi(out)
	}
	return s, nil
}

// hasRepoFilters reports whether the repository's own config (or anything it
// includes) defines a filter driver. Drivers from the user's global or the
// system config --- git-lfs, typically --- are the user's own and are fine.
// If the config cannot be read, it reports true: when unsure, skip status.
func hasRepoFilters(root string) bool {
	out, err := run(root, "config", "--show-scope", "--includes", "--get-regexp", `^filter\.`)
	if err != nil {
		// Exit status 1 means no matching key at all.
		var exit *exec.ExitError
		return !(errors.As(err, &exit) && exit.ExitCode() == 1)
	}
	for _, line := range strings.Split(out, "\n") {
		scope, _, _ := strings.Cut(line, "\t")
		if scope != "global" && scope != "system" {
			return true
		}
	}
	return false
}

// safeConfig is passed ahead of every command. Command-line config outranks
// the repository's, so a core.fsmonitor there (a command git runs on status)
// is switched off. It also reaches any git that git itself starts.
var safeConfig = []string{
	"-c", "core.fsmonitor=false",
	"-c", "status.submoduleSummary=false",
}

// run executes git in dir and returns trimmed stdout. Every call is bounded so
// a hung git (a credential prompt, a stale lock) cannot stall the daemon.
func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append(append([]string{}, safeConfig...), args...)...)
	cmd.Dir = dir
	cmd.Stdin = nil
	// Keep git non-interactive: never let it block on a terminal prompt.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")

	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
