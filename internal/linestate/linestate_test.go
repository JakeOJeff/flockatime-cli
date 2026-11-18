package linestate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"snapshot-agent/internal/snapshot"
)

// h turns "a b c" into one fake line hash per word, so tests read as text.
func h(s string) []uint64 {
	var out []uint64
	for _, w := range strings.Fields(s) {
		var v uint64
		for _, r := range w {
			v = v*131 + uint64(r)
		}
		out = append(out, v)
	}
	return out
}

func TestDiff(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want Counts
	}{
		{"identical", "a b c", "a b c", Counts{}},
		{"new file", "", "a b c", Counts{Added: 3, Tail: 1, Mid: 2}},
		{"emptied", "a b c", "", Counts{Removed: 3, Tail: 3}},
		{"append at end", "a b c d e f g h i j", "a b c d e f g h i j k", Counts{Added: 1, Tail: 1}},
		{"edit in middle", "a b c d e f g h i j", "a b c X e f g h i j", Counts{Added: 1, Removed: 1, Mid: 2}},
		{"insert at top", "a b c d e f g h i j", "X a b c d e f g h i j", Counts{Added: 1, Mid: 1}},
		{"interleaved", "a b c d e f", "a X c d Y f", Counts{Added: 2, Removed: 2, Mid: 4}},
		{"edit last line", "a b c d e f g h i j", "a b c d e f g h i X", Counts{Added: 1, Removed: 1, Tail: 2}},
		{"repeated lines", "x x x", "x x x x", Counts{Added: 1, Tail: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Diff(h(tc.a), h(tc.b))
			if got != tc.want {
				t.Errorf("Diff(%q, %q) = %+v, want %+v", tc.a, tc.b, got, tc.want)
			}
			if got.Tail+got.Mid != got.Added+got.Removed {
				t.Errorf("tail+mid != added+removed: %+v", got)
			}
		})
	}
}

// Past maxEdits the changed region is counted as replaced, which must still
// balance: added - removed is the change in length.
func TestDiffFallback(t *testing.T) {
	var a, b []uint64
	for i := range 3000 {
		a = append(a, uint64(2*i))
		b = append(b, uint64(2*i+1))
	}
	b = append(b, 1, 2, 3)
	c := Diff(a, b)
	if c.Added != len(b) || c.Removed != len(a) {
		t.Fatalf("fallback = %+v, want added %d removed %d", c, len(b), len(a))
	}
}

func TestHashLinesMatchesWalkCount(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "a\n\n": 2} {
		p := filepath.Join(dir, "f")
		os.WriteFile(p, []byte(content), 0o644)
		lines, _, err := hashLines(p)
		if err != nil || len(lines) != want {
			t.Errorf("%q: %d lines (%v), want %d", content, len(lines), err, want)
		}
	}
}

// capture snapshots root through a State loaded from stateDir, saves it, and
// returns the files by their order in the walk.
func capture(t *testing.T, stateDir, root string) []snapshot.File {
	t.Helper()
	st, err := Load(stateDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	s, err := snapshot.CaptureWith("demo", root, "test", snapshot.Options{Observer: st})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	return s.Files
}

func TestObserveAcrossCaptures(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	file := filepath.Join(root, "main.go")
	write := func(s string) {
		if err := os.WriteFile(file, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	body := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"

	write(body)
	if f := capture(t, stateDir, root)[0]; f.Added != 0 || f.Removed != 0 {
		t.Fatalf("baseline capture reported changes: %+v", f)
	}

	write(body + "11\n12\n")
	if f := capture(t, stateDir, root)[0]; f.Added != 2 || f.TailEdits != 2 || f.MidEdits != 0 {
		t.Fatalf("append: %+v", f)
	}

	write(strings.Replace(body, "4\n", "four\n", 1) + "11\n12\n")
	if f := capture(t, stateDir, root)[0]; f.Added != 1 || f.Removed != 1 || f.MidEdits != 2 {
		t.Fatalf("mid edit: %+v", f)
	}

	// Unchanged content: no counts, and nothing rewritten.
	if f := capture(t, stateDir, root)[0]; f.Added+f.Removed != 0 {
		t.Fatalf("unchanged file reported changes: %+v", f)
	}

	// A file that appears after the baseline is all added.
	os.WriteFile(filepath.Join(root, "new.go"), []byte("x\ny\n"), 0o644)
	for _, f := range capture(t, stateDir, root) {
		if f.Lines == 2 && f.Added != 2 {
			t.Fatalf("new file: %+v", f)
		}
	}
}

func TestStateFileHoldsNoText(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	secret := "hunter2-very-secret-line"
	os.WriteFile(filepath.Join(root, "secret-name.txt"), []byte(secret+"\n"), 0o644)
	capture(t, stateDir, root)

	entries, _ := os.ReadDir(stateDir)
	if len(entries) != 1 {
		t.Fatalf("want one state file, got %d", len(entries))
	}
	raw, _ := os.ReadFile(filepath.Join(stateDir, entries[0].Name()))
	for _, leak := range []string{secret, "secret-name"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("state file contains %q", leak)
		}
	}
}

func TestFileName(t *testing.T) {
	if got := fileName("flockatime-cli"); got != "flockatime-cli.bin" {
		t.Errorf("plain name: %s", got)
	}
	a, b := fileName("a/b"), fileName("a_b")
	if a == b || strings.ContainsAny(a, `/\`) {
		t.Errorf("a/b -> %s, a_b -> %s", a, b)
	}
	if got := fileName(".."); !strings.HasPrefix(got, "..-") {
		t.Errorf(".. -> %s", got)
	}
}
