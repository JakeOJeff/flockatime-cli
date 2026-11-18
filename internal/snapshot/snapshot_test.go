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

func TestCaptureCountsSkippedAndBytes(t *testing.T) {
	root := tree(t, map[string]string{".gitignore": "ignored.bin\n", "a.txt": "abc\n"})
	for _, name := range []string{"big.bin", "ignored.bin"} {
		if err := os.WriteFile(filepath.Join(root, name), make([]byte, maxFileBytes+1), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Capture("p", root, "test")
	if err != nil {
		t.Fatal(err)
	}
	// big.bin is over the cap; ignored.bin is .gitignore'd, which is not a skip.
	if s.SkippedFiles != 1 {
		t.Errorf("skipped %d, want 1", s.SkippedFiles)
	}
	// .gitignore and a.txt.
	if want := int64(len("ignored.bin\n") + len("abc\n")); s.TotalBytes != want {
		t.Errorf("total bytes %d, want %d", s.TotalBytes, want)
	}
	if s.OS == "" || s.Arch == "" {
		t.Error("os and arch should be set")
	}
}

func TestRevealFindsSaltedPath(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "main.rs"), []byte("fn main(){}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "node_modules", "x", "main.rs"), []byte("x\n"), 0o644)
	salt := []byte("0123456789abcdef")

	s, err := CaptureWith("p", root, "test", Options{PathSalt: salt})
	if err != nil || len(s.Files) != 1 {
		t.Fatalf("capture: %v, %d files", err, len(s.Files))
	}
	got, err := Reveal(root, salt, s.Files[0].PathHash[:10])
	if err != nil || len(got) != 1 || got[0] != "src/main.rs" {
		t.Fatalf("Reveal = %v, %v; want [src/main.rs]", got, err)
	}
	// The wrong salt must not find it: that is the point of salting.
	if got, _ := Reveal(root, []byte("another-salt-16b"), s.Files[0].PathHash); len(got) != 0 {
		t.Fatalf("wrong salt revealed %v", got)
	}
	if HashPath("src/main.rs", salt) == HashPath("src/main.rs", nil) {
		t.Fatal("salted and unsalted hashes are equal")
	}
}

// Worktrees and submodules have a .git *file* pointing at the real repo; it is
// plumbing, not source, and must not be recorded.
func TestGitFileIsSkipped(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	s, err := Capture("p", root, "test")
	if err != nil || s.FileCount != 1 {
		t.Fatalf("FileCount = %d (%v), want 1", s.FileCount, err)
	}
}
