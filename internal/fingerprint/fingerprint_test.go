package fingerprint

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// genCode returns n lines of Go-ish code, ten tokens each, whose identifiers
// embed prefix and the line number so different generators never collide.
func genCode(prefix string, from, n int) string {
	var b strings.Builder
	for i := from; i < from+n; i++ {
		fmt.Fprintf(&b, "%s%d := call%s(x%d, y%d)\n", prefix, i, prefix, i, i)
	}
	return b.String()
}

func fingerprints(src, ext string, max int) []uint64 {
	return Winnow(Tokens([]byte(src), ext), max)
}

func overlap(a, b []uint64) int {
	n := 0
	for _, v := range a {
		if _, found := slices.BinarySearch(b, v); found {
			n++
		}
	}
	return n
}

func checkShape(t *testing.T, fp []uint64, max int) {
	t.Helper()
	if len(fp) > max {
		t.Fatalf("len %d > max %d", len(fp), max)
	}
	for i, v := range fp {
		if v >= 1<<53 {
			t.Fatalf("value %d exceeds 2^53", v)
		}
		if i > 0 && fp[i-1] >= v {
			t.Fatalf("not sorted/unique at %d", i)
		}
	}
}

const goSrc = `package demo

import "fmt"

func Sum(values []int) int {
	total := 0
	for _, v := range values {
		if v > 0 {
			total += v
		}
	}
	return total
}

func Print(values []int) {
	fmt.Println("sum:", Sum(values), len(values))
}
`

func TestDeterministic(t *testing.T) {
	a := fingerprints(goSrc, ".go", MaxPerFile)
	if len(a) == 0 {
		t.Fatal("no fingerprints")
	}
	for range 3 {
		if b := fingerprints(goSrc, ".go", MaxPerFile); !slices.Equal(a, b) {
			t.Fatal("fingerprints changed between calls")
		}
	}
}

// TestGolden pins the exact output for a fixed token stream, so a change to
// the hash constants (which would silently break matching against fingerprints
// already held by the collector) fails loudly.
func TestGolden(t *testing.T) {
	tokens := make([]uint64, 60)
	for i := range tokens {
		tokens[i] = uint64(i) * 0x9e3779b97f4a7c15
	}
	got := Winnow(tokens, 3)
	want := []uint64{0xd127e5ba0ca9, 0x16900af58a0f3, 0x25187cddd1b1b}
	if !slices.Equal(got, want) {
		t.Fatalf("golden mismatch: got %#v", got)
	}
	// Token hashes are plain FNV-1a 64 of the token bytes.
	if got := Tokens([]byte("a"), ".go"); !slices.Equal(got, []uint64{0xaf63dc4c8601ec8c}) {
		t.Fatalf("token hash: got %#x", got)
	}
}

func TestWhitespaceAndCommentEditsIgnored(t *testing.T) {
	edited := strings.ReplaceAll(`// Package demo does sums.
package   demo
import "fmt" /* block
comment */
// Sum adds the positive values.
func Sum(values []int)   int {
    total := 0 // running total
    for _, v := range values { if v > 0 { total += v } }
    return total
}
func Print(values []int) {
        fmt.Println("sum:",Sum(values),len(values)) /* trailing */
}`, "\n", "\r\n")

	a := fingerprints(goSrc, ".go", 1000)
	b := fingerprints(edited, ".go", 1000)
	if len(a) == 0 || !slices.Equal(a, b) {
		t.Fatalf("whitespace/comment edit changed fingerprints:\n%v\n%v", a, b)
	}
	if !slices.Equal(Tokens([]byte(goSrc), ".go"), Tokens([]byte(edited), ".go")) {
		t.Fatal("token streams differ")
	}
}

func TestHashCommentsAndStrings(t *testing.T) {
	// s = " a # b " -> 7 tokens; the trailing comment is dropped.
	if got := len(Tokens([]byte(`s = "a#b"  # real comment`), ".py")); got != 7 {
		t.Fatalf("python: got %d tokens, want 7", got)
	}
	// Triple-quoted string spanning lines keeps its # as text.
	src := "x = \"\"\"a\n# not a comment\n\"\"\"  # comment"
	// x = """ a # not a comment """ -> 1+1+3+1+4+3 = 13
	if got := len(Tokens([]byte(src), ".py")); got != 13 {
		t.Fatalf("python triple: got %d tokens, want 14", got)
	}
}

func TestSlashesInStringNotComment(t *testing.T) {
	// a := " x / / y " ; b -> 11 tokens.
	for _, src := range []string{`a := "x//y"; b`, "a := `x//y`; b", `a := 'x//y'; b`} {
		if got := len(Tokens([]byte(src), ".go")); got != 11 {
			t.Errorf("%s: got %d tokens, want 11", src, got)
		}
	}
	// An escaped quote does not end the string early.
	if got := len(Tokens([]byte(`a = "\"//" + b // c`), ".js")); got != 10 {
		t.Errorf("escaped quote: got %d tokens, want 10", got)
	}
	// A real comment after a string is still stripped.
	if got := len(Tokens([]byte(`a := "x" // y`), ".go")); got != 6 {
		t.Errorf("trailing comment: got %d tokens, want 5", got)
	}
}

func TestOtherFamilies(t *testing.T) {
	cases := []struct {
		src, ext string
		want     int
	}{
		{"select a -- note\nfrom t", ".sql", 4},
		{"x = 1 --[[ block\n]] y", ".lua", 4},
		{"<p>hi</p><!-- gone -->", ".html", 8},
		{"a { color: red } /* c */ b { background: url(http://x) }", ".css", 19},
		{"a // b", ".txt", 4},
	}
	for _, c := range cases {
		if got := len(Tokens([]byte(c.src), c.ext)); got != c.want {
			t.Errorf("%s %q: got %d tokens, want %d", c.ext, c.src, got, c.want)
		}
	}
}

func TestPartialCopyDetected(t *testing.T) {
	a := genCode("a", 0, 30) // 300 tokens
	lines := strings.SplitAfter(a, "\n")
	slice := strings.Join(lines[10:22], "") // 120 tokens
	b := genCode("b", 0, 15) + slice + genCode("c", 0, 15)

	if n := len(Tokens([]byte(slice), ".go")); n < W+K-1 {
		t.Fatalf("slice only %d tokens; guarantee needs %d", n, W+K-1)
	}
	fa := fingerprints(a, ".go", 1000)
	fb := fingerprints(b, ".go", 1000)
	checkShape(t, fa, 1000)
	checkShape(t, fb, 1000)
	if overlap(fa, fb) == 0 {
		t.Fatal("embedded copy not detected")
	}
	// Unrelated code shares nothing.
	if n := overlap(fa, fingerprints(genCode("z", 0, 30), ".go", 1000)); n != 0 {
		t.Fatalf("unrelated code shares %d fingerprints", n)
	}
}

func TestCappedSelectionOverlaps(t *testing.T) {
	block := genCode("shared", 0, 100) // 1000 tokens
	f1 := fingerprints(genCode("p", 0, 20)+block, ".go", MaxPerFile)
	f2 := fingerprints(block+genCode("q", 0, 20), ".go", MaxPerFile)
	checkShape(t, f1, MaxPerFile)
	checkShape(t, f2, MaxPerFile)
	if len(f1) != MaxPerFile || len(f2) != MaxPerFile {
		t.Fatalf("expected the cap to apply: %d, %d", len(f1), len(f2))
	}
	if n := overlap(f1, f2); n < MaxPerFile/2 {
		t.Fatalf("capped sets overlap in only %d of %d", n, MaxPerFile)
	}
}

func TestShapeAndEdges(t *testing.T) {
	fp := fingerprints(genCode("s", 0, 200), ".go", MaxPerFile)
	checkShape(t, fp, MaxPerFile)
	if Winnow(make([]uint64, K-1), 10) != nil {
		t.Fatal("fewer than K tokens should give nil")
	}
	if Winnow(make([]uint64, 100), 0) != nil {
		t.Fatal("max 0 should give nil")
	}
	// Exactly K tokens: one K-gram, one fingerprint.
	if got := Winnow(make([]uint64, K), 10); len(got) != 1 {
		t.Fatalf("K tokens gave %d fingerprints", len(got))
	}
}

func TestSingleEditChangesFingerprints(t *testing.T) {
	src := genCode("e", 0, 10)
	edited := strings.Replace(src, "e5 :=", "e5 =", 1)
	if slices.Equal(fingerprints(src, ".go", 1000), fingerprints(edited, ".go", 1000)) {
		t.Fatal("a real code edit did not change fingerprints")
	}
}

func TestFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	p := write("demo.GO", []byte(goSrc)) // extension is case-folded
	got, err := File(p, MaxPerFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := fingerprints(goSrc, ".go", MaxPerFile); len(got) == 0 || !slices.Equal(got, want) {
		t.Fatalf("File = %v, want %v", got, want)
	}

	for name, data := range map[string][]byte{
		"short.go": []byte("package x\nvar y = 1\n"),
		"bin.go":   append([]byte(goSrc), 0, 1, 2),
		"huge.go":  []byte(strings.Repeat(genCode("h", 0, 1), MaxFileBytes/10)),
	} {
		got, err := File(write(name, data), MaxPerFile)
		if err != nil || got != nil {
			t.Errorf("%s: got %v, %v; want nil, nil", name, got, err)
		}
	}

	if _, err := File(filepath.Join(dir, "missing.go"), MaxPerFile); err == nil {
		t.Error("missing file should error")
	}
}
