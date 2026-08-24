// Package gitstate reads repository state by shelling out to git. No git
// library is vendored for this.
package gitstate

import (
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
	status, err := run(root, "status", "--porcelain")
	if err == nil {
		s.Dirty = status != ""
	}

	// Fails when there is no upstream configured, which leaves Ahead at 0.
	if out, err := run(root, "rev-list", "--count", "@{u}..HEAD"); err == nil {
		s.Ahead, _ = strconv.Atoi(out)
	}
	return s, nil
}

// run executes git in dir and returns trimmed stdout. Every call is bounded so
// a hung git (a credential prompt, a stale lock) cannot stall the daemon.
func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
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
