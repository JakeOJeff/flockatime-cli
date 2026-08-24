package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree writes a set of files into a fresh temp dir. Keys are slash-separated
// relative paths.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// recorded reports whether a relative path made it into the walk, matching on
// the path hash because the walk never returns plaintext paths.
func recorded(files []File, rel string) bool {
	want := hashPath(rel)
	for _, f := range files {
		if f.PathHash == want {
			return true
		}
	}
	return false
}

func TestWalkRespectsGitignoreAndSkipLists(t *testing.T) {
	root := tree(t, map[string]string{
		".gitignore":           "*.log\nbuild/\n",
		"keep.txt":             "hello\n",
		"debug.log":            "noise\n",
		"build/out.bin":        "artifact\n",
		"src/.gitignore":       "local.*\n",
		"src/main.go":          "package main\n",
		"src/local.env":        "SECRET=1\n",
		"node_modules/dep.js":  "module.exports={}\n",
		"vendor/lib.go":        "package lib\n",
		"dist/app.js":          "app\n",
		"target/debug/bin":     "elf\n",
		".git/config":          "[core]\n",
		"sub/keep-nested.txt":  "nested\n",
		"sub/ignored-here.log": "noise\n",
	})

	files, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"keep.txt", "src/main.go", "sub/keep-nested.txt"} {
		if !recorded(files, rel) {
			t.Errorf("expected %s to be recorded", rel)
		}
	}
	for _, rel := range []string{
		"debug.log",            // root *.log
		"build/out.bin",        // root build/
		"src/local.env",        // nested .gitignore, scoped to src/
		"sub/ignored-here.log", // root rule applies to subdirectories
		"node_modules/dep.js",  // always skipped
		"vendor/lib.go", "dist/app.js", "target/debug/bin", ".git/config",
	} {
		if recorded(files, rel) {
			t.Errorf("expected %s to be skipped", rel)
		}
	}
}

func TestWalkSkipsOversizeFiles(t *testing.T) {
	root := tree(t, map[string]string{"small.txt": "ok\n"})
	big := filepath.Join(root, "big.bin")
	if err := os.WriteFile(big, make([]byte, maxFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	if recorded(files, "big.bin") {
		t.Error("expected file over the size cap to be skipped")
	}
	if !recorded(files, "small.txt") {
		t.Error("expected small.txt to be recorded")
	}
}

func TestWalkNeverReturnsPlaintextPaths(t *testing.T) {
	root := tree(t, map[string]string{"very-secret-name.txt": "x\n"})
	files, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.Contains(f.PathHash, "secret") || len(f.PathHash) != 64 {
			t.Errorf("path hash %q is not a bare sha256", f.PathHash)
		}
	}
}

func TestTreeHashStableAcrossRuns(t *testing.T) {
	root := tree(t, map[string]string{
		"a.txt":   "alpha\n",
		"b/c.txt": "charlie\n",
		"b/d.txt": "delta\n",
	})

	first, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	if TreeHash(first) != TreeHash(second) {
		t.Fatalf("tree hash changed between runs: %s vs %s", TreeHash(first), TreeHash(second))
	}
}

func TestTreeHashIndependentOfWalkOrder(t *testing.T) {
	files := []File{
		{PathHash: hashPath("a"), ContentHash: "11"},
		{PathHash: hashPath("b"), ContentHash: "22"},
		{PathHash: hashPath("c"), ContentHash: "33"},
	}
	reversed := []File{files[2], files[1], files[0]}
	if TreeHash(files) != TreeHash(reversed) {
		t.Error("tree hash depends on ordering; it must not")
	}
}

func TestTreeHashChangesOnSingleCharacterEdit(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n"})

	before, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alphb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	if TreeHash(before) == TreeHash(after) {
		t.Error("tree hash unchanged after a one-character edit")
	}
}

func TestTreeHashChangesOnRename(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "same\n"})
	before, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "a.txt"), filepath.Join(root, "z.txt")); err != nil {
		t.Fatal(err)
	}
	after, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	if TreeHash(before) == TreeHash(after) {
		t.Error("tree hash unchanged after a rename; path hashes must feed it")
	}
}

func TestLineCounting(t *testing.T) {
	cases := map[string]int{
		"":            0,
		"one line\n":  1,
		"no trailing": 1,
		"a\nb\nc\n":   3,
		"a\nb\nc":     3,
		"\n\n":        2,
	}
	for body, want := range cases {
		root := tree(t, map[string]string{"f.txt": body})
		files, err := Walk(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 {
			t.Fatalf("expected 1 file for %q, got %d", body, len(files))
		}
		if files[0].Lines != want {
			t.Errorf("body %q: got %d lines, want %d", body, files[0].Lines, want)
		}
	}
}

func TestAsUnchangedDropsFileList(t *testing.T) {
	s := &Snapshot{Project: "p", FileCount: 3, TotalLines: 9, Files: []File{{PathHash: "x"}}}
	u := s.AsUnchanged()
	if !u.Unchanged {
		t.Error("expected unchanged flag")
	}
	if len(u.Files) != 0 {
		t.Error("expected empty file list")
	}
	if u.FileCount != 3 || u.TotalLines != 9 {
		t.Error("counts should survive")
	}
	if len(s.Files) != 1 {
		t.Error("original snapshot must not be mutated")
	}
}
