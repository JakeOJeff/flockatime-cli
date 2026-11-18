package snapshot

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	oldTime = time.Unix(1_600_000_000, 0)
	newTime = time.Unix(1_700_000_000, 0)
)

// settle sets every entry under root to oldTime, then the named relative paths
// to newTime. Creating files bumps their parents' mtimes, so a test must pin
// everything before it can say which mtime should win.
func settle(t *testing.T, root string, touched ...string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, oldTime, oldTime)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range touched {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(rel)), newTime, newTime); err != nil {
			t.Fatal(err)
		}
	}
}

func walkT(t *testing.T, root string) walkResult {
	t.Helper()
	res, err := walk(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestBuildAtFromDistWithoutRecordingIt(t *testing.T) {
	root := tree(t, map[string]string{"main.go": "package main\n", "dist/js/app.js": "x\n"})
	settle(t, root, "dist/js/app.js")
	res := walkT(t, root)
	if res.buildAt != newTime.Unix() {
		t.Errorf("buildAt %d, want %d", res.buildAt, newTime.Unix())
	}
	if res.testAt != 0 {
		t.Errorf("testAt %d, want 0", res.testAt)
	}
	if recorded(res.files, "dist/js/app.js") || !recorded(res.files, "main.go") {
		t.Error("dist/ contents must be skipped and source recorded")
	}
}

func TestAmbiguousBuildCountsOnlyWhenIgnored(t *testing.T) {
	root := tree(t, map[string]string{".gitignore": "build/\n", "build/a.o": "x\n"})
	settle(t, root, "build/a.o")
	if got := walkT(t, root).buildAt; got != newTime.Unix() {
		t.Errorf("ignored build/: buildAt %d, want %d", got, newTime.Unix())
	}

	root = tree(t, map[string]string{"build/script.go": "package build\n"})
	settle(t, root, "build/script.go", "build")
	res := walkT(t, root)
	if res.buildAt != 0 {
		t.Errorf("source build/: buildAt %d, want 0", res.buildAt)
	}
	if !recorded(res.files, "build/script.go") {
		t.Error("a build/ that is not ignored must be walked as source")
	}
}

func TestTestAtFromCaches(t *testing.T) {
	root := tree(t, map[string]string{".pytest_cache/v/cache/lastfailed": "{}\n"})
	settle(t, root, ".pytest_cache/v")
	if got := walkT(t, root).testAt; got != newTime.Unix() {
		t.Errorf(".pytest_cache: testAt %d, want %d", got, newTime.Unix())
	}

	root = tree(t, map[string]string{".gitignore": "coverage/\n", "coverage/lcov.info": "x\n"})
	settle(t, root, "coverage/lcov.info")
	res := walkT(t, root)
	if res.testAt != newTime.Unix() || res.buildAt != 0 {
		t.Errorf("coverage/: testAt %d buildAt %d, want %d and 0", res.testAt, res.buildAt, newTime.Unix())
	}
}

func TestTestAtFromIgnoredCoverageFile(t *testing.T) {
	root := tree(t, map[string]string{".gitignore": ".coverage\n", ".coverage": "data", "a.py": "x\n"})
	settle(t, root, ".coverage")
	res := walkT(t, root)
	if res.testAt != newTime.Unix() {
		t.Errorf("testAt %d, want %d", res.testAt, newTime.Unix())
	}
	if recorded(res.files, ".coverage") {
		t.Error("ignored .coverage must not be recorded")
	}
}

func TestNoBuildOrTestDirs(t *testing.T) {
	root := tree(t, map[string]string{"main.go": "package main\n", "src/lib.go": "package src\n"})
	res := walkT(t, root)
	if res.buildAt != 0 || res.testAt != 0 {
		t.Errorf("buildAt %d testAt %d, want 0 and 0", res.buildAt, res.testAt)
	}
}

func TestActivityScanStopsAtDepthTwo(t *testing.T) {
	root := tree(t, map[string]string{"target/a/b/c/deep.o": "x\n"})
	settle(t, root, "target/a/b/c/deep.o", "target/a/b/c")
	if got := walkT(t, root).buildAt; got != oldTime.Unix() {
		t.Errorf("buildAt %d, want %d: depth 3 and 4 must not be seen", got, oldTime.Unix())
	}
	settle(t, root, "target/a/b")
	if got := walkT(t, root).buildAt; got != newTime.Unix() {
		t.Errorf("buildAt %d, want %d: depth 2 must be seen", got, newTime.Unix())
	}
}

func TestPycacheNoLongerRecorded(t *testing.T) {
	root := tree(t, map[string]string{"app.py": "x\n", "__pycache__/app.cpython-312.pyc": "bytecode"})
	settle(t, root, "__pycache__/app.cpython-312.pyc")
	res := walkT(t, root)
	if recorded(res.files, "__pycache__/app.cpython-312.pyc") {
		t.Error("__pycache__ contents must not be recorded")
	}
	if res.buildAt != newTime.Unix() {
		t.Errorf("buildAt %d, want %d", res.buildAt, newTime.Unix())
	}
}

func TestFutureMtimeIgnored(t *testing.T) {
	root := tree(t, map[string]string{"dist/a.js": "x\n", "dist/b.js": "y\n"})
	settle(t, root, "dist/a.js")
	future := time.Now().Add(72 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "dist", "b.js"), future, future); err != nil {
		t.Fatal(err)
	}
	if got := walkT(t, root).buildAt; got != newTime.Unix() {
		t.Errorf("buildAt %d, want %d: a future mtime must not win", got, newTime.Unix())
	}
}
