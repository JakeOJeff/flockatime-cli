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

// State mirrors the git block of a snapshot payload. Everything past Branch is
// a count or a time: no path from status or diff is ever kept.
type State struct {
	Head   string
	Branch string
	Dirty  bool
	Ahead  int
	Behind int

	// Working tree, from status. A file both staged and changed again counts
	// in Staged and Modified.
	Staged     int
	Modified   int
	Untracked  int
	Conflicted int

	// Uncommitted lines against HEAD, staged or not.
	Insertions int
	Deletions  int

	Commits      int   // reachable from HEAD
	LastCommitAt int64 // committer time of HEAD, unix seconds
	Stashes      int
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

	// status and diff can run a repository's filter drivers to re-check a
	// file, so both are skipped when the repository defines its own; the
	// working-tree fields stay zero. Submodules are left out because their
	// configs are not checked.
	if !hasRepoFilters(root) {
		// One line per changed path. Only the two-letter code is read; the
		// paths themselves are discarded.
		if out, err := runRaw(root, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=all"); err == nil {
			s.Dirty = strings.TrimSpace(out) != ""
			countStatus(s, out)
		}
		// --no-ext-diff and --no-textconv: both name commands a repository's
		// config can set. A repo with no commits yet has no HEAD to diff.
		if s.Head != "" {
			if out, err := run(root, "diff", "--shortstat", "--no-ext-diff", "--no-textconv",
				"--ignore-submodules=all", "HEAD"); err == nil {
				s.Insertions, s.Deletions = parseShortstat(out)
			}
		}
	}

	// Fails when there is no upstream configured, which leaves both at 0.
	// The output is "behind<TAB>ahead".
	if out, err := run(root, "rev-list", "--left-right", "--count", "@{u}...HEAD"); err == nil {
		if b, a, ok := strings.Cut(out, "\t"); ok {
			s.Behind, _ = strconv.Atoi(b)
			s.Ahead, _ = strconv.Atoi(a)
		}
	}

	if s.Head != "" {
		if out, err := run(root, "rev-list", "--count", "HEAD"); err == nil {
			s.Commits, _ = strconv.Atoi(out)
		}
		if out, err := run(root, "log", "-1", "--no-show-signature", "--format=%ct", "HEAD"); err == nil {
			s.LastCommitAt, _ = strconv.ParseInt(out, 10, 64)
		}
	}
	// Fails when nothing is stashed, which leaves Stashes at 0.
	if out, err := run(root, "rev-list", "--walk-reflogs", "--count", "refs/stash"); err == nil {
		s.Stashes, _ = strconv.Atoi(out)
	}
	return s, nil
}

// countStatus tallies porcelain lines by their two-letter code.
func countStatus(s *State, out string) {
	if out == "" {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		x, y := line[0], line[1]
		switch {
		case x == '?' && y == '?':
			s.Untracked++
		case x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D'):
			s.Conflicted++
		default:
			if x != ' ' {
				s.Staged++
			}
			if y != ' ' {
				s.Modified++
			}
		}
	}
}

// parseShortstat reads " 3 files changed, 10 insertions(+), 2 deletions(-)",
// where either count is left out when it is zero.
func parseShortstat(out string) (ins, del int) {
	for _, part := range strings.Split(out, ",") {
		f := strings.Fields(part)
		if len(f) < 2 {
			continue
		}
		n, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(f[1], "insertion"):
			ins = n
		case strings.HasPrefix(f[1], "deletion"):
			del = n
		}
	}
	return ins, del
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

// run executes git in dir and returns trimmed stdout.
func run(dir string, args ...string) (string, error) {
	out, err := runRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// runRaw is run without the trim, for output whose leading space means
// something (a status code like " M"). Every call is bounded so a hung git (a
// credential prompt, a stale lock) cannot stall the daemon.
func runRaw(dir string, args ...string) (string, error) {
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
	return string(out), nil
}
