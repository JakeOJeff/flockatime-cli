package gitstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepo makes a repository with one committed file and returns its root.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "."},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"config", "core.autocrlf", "false"},
	} {
		git(t, dir, args...)
	}
	write(t, filepath.Join(dir, "a.txt"), "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// marker is a command that leaves a file behind if git ever runs it.
func marker(t *testing.T) (cmd, path string) {
	path = filepath.Join(t.TempDir(), "ran")
	return "echo ran > '" + filepath.ToSlash(path) + "'", path
}

func assertNotRun(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatal("git ran a command from the repository's own config")
	}
}

func TestDirtyDetected(t *testing.T) {
	dir := gitRepo(t)
	s, err := Read(dir)
	if err != nil || s == nil || s.Dirty || s.Head == "" {
		t.Fatalf("clean repo: got %+v, %v", s, err)
	}
	write(t, filepath.Join(dir, "a.txt"), "two\n")
	if s, _ := Read(dir); !s.Dirty {
		t.Fatal("modified repo not reported dirty")
	}
}

func TestRepoFsmonitorNeverRuns(t *testing.T) {
	dir := gitRepo(t)
	cmd, ran := marker(t)
	git(t, dir, "config", "core.fsmonitor", cmd+"; false")
	write(t, filepath.Join(dir, "a.txt"), "two\n")

	s, _ := Read(dir)
	assertNotRun(t, ran)
	if !s.Dirty {
		t.Fatal("fsmonitor override should still leave status working")
	}
}

func TestRepoFilterDriverNeverRuns(t *testing.T) {
	dir := gitRepo(t)
	cmd, ran := marker(t)
	git(t, dir, "config", "filter.evil.clean", cmd+"; cat")
	write(t, filepath.Join(dir, ".gitattributes"), "* filter=evil\n")
	write(t, filepath.Join(dir, "a.txt"), "two\n")

	s, _ := Read(dir)
	assertNotRun(t, ran)
	if s.Dirty {
		t.Fatal("status should be skipped, leaving Dirty false")
	}
}

// A filter hidden behind include.path is still the repository's own.
func TestIncludedFilterDriverNeverRuns(t *testing.T) {
	dir := gitRepo(t)
	cmd, ran := marker(t)
	extra := filepath.Join(dir, ".git", "extra.cfg")
	write(t, extra, "[filter \"evil\"]\n\tclean = "+cmd+"; cat\n")
	git(t, dir, "config", "include.path", filepath.ToSlash(extra))
	write(t, filepath.Join(dir, ".gitattributes"), "* filter=evil\n")
	write(t, filepath.Join(dir, "a.txt"), "two\n")

	Read(dir)
	assertNotRun(t, ran)
}
