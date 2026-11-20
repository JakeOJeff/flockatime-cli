package aitrace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"snapshot-agent/internal/snapshot"
)

// claudeTool is one Claude Code transcript line with a single tool call.
func claudeTool(t *testing.T, ts, cwd, name string, input map[string]any) string {
	t.Helper()
	line := map[string]any{
		"type": "assistant", "timestamp": ts, "cwd": cwd,
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": name, "input": input},
		}},
	}
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	repo, claude, codex string
	store               *Store
	src                 Sources
}

func newFixture(t *testing.T) *fixture {
	d := t.TempDir()
	f := &fixture{
		repo:   filepath.Join(d, "repo"),
		claude: filepath.Join(d, "claude", "projects"),
		codex:  filepath.Join(d, "codex", "sessions"),
	}
	f.store = Load(filepath.Join(d, "ai.gob"))
	f.src = Sources{ClaudeDir: f.claude, CodexDir: f.codex}
	return f
}

func (f *fixture) count(t *testing.T, rel string) int {
	t.Helper()
	return f.store.CountFile(filepath.Join(f.repo, rel), contentKey(t, filepath.Join(f.repo, rel)))
}

func contentKey(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) // any value that changes with content works as a cache key
}

const aiCode = "func add(a, b int) int {\n\treturn a + b\n}\n"

func TestClaudeWriteIsAttributedUntilEdited(t *testing.T) {
	f := newFixture(t)
	main := filepath.Join(f.repo, "main.go")
	write(t, filepath.Join(f.claude, "proj", "s1.jsonl"),
		claudeTool(t, "2026-11-20T10:00:00Z", f.repo, "Write", map[string]any{"file_path": main, "content": aiCode}))
	f.store.Sync(f.src)

	// The agent's three lines, plus one the person typed.
	write(t, main, aiCode+"// written by hand\n")
	if got := f.count(t, "main.go"); got != 3 {
		t.Fatalf("ai lines = %d, want 3", got)
	}

	// Editing the body takes that line, and the brace beside it that only
	// counted through it, away from the agent.
	write(t, main, "func add(a, b int) int {\n\treturn b + a\n}\n")
	if got := f.count(t, "main.go"); got != 1 {
		t.Fatalf("after edit: ai lines = %d, want 1", got)
	}
}

func TestLoneBraceIsNotCredited(t *testing.T) {
	f := newFixture(t)
	p := filepath.Join(f.repo, "a.go")
	write(t, filepath.Join(f.claude, "proj", "s1.jsonl"),
		claudeTool(t, "2026-11-20T10:00:00Z", f.repo, "Write", map[string]any{"file_path": p, "content": aiCode}))
	f.store.Sync(f.src)

	write(t, p, "func human() {\n\tprintln(1)\n}\n")
	if got := f.count(t, "a.go"); got != 0 {
		t.Fatalf("a person's closing brace was credited to the agent: %d", got)
	}
}

func TestClaudeEditAndHeredoc(t *testing.T) {
	f := newFixture(t)
	p := filepath.Join(f.repo, "x.py")
	tr := filepath.Join(f.claude, "proj", "s1.jsonl")
	write(t, tr,
		claudeTool(t, "2026-11-20T10:00:00Z", f.repo, "Edit", map[string]any{
			"file_path": p, "old_string": "pass", "new_string": "total = sum(values)\nreturn total"})+
			claudeTool(t, "2026-11-20T10:01:00Z", f.repo, "Bash", map[string]any{
				"command": "cat > notes.txt <<'EOF'\nfirst generated line\nsecond generated line\nEOF\necho done"}))
	f.store.Sync(f.src)

	write(t, p, "def f(values):\n    total = sum(values)\n    return total\n")
	if got := f.count(t, "x.py"); got != 2 {
		t.Fatalf("x.py ai lines = %d, want 2 (re-indented lines still match)", got)
	}
	write(t, filepath.Join(f.repo, "notes.txt"), "first generated line\nsecond generated line\nmine\n")
	if got := f.count(t, "notes.txt"); got != 2 {
		t.Fatalf("notes.txt ai lines = %d, want 2", got)
	}
}

func TestCodexPatchInsideJS(t *testing.T) {
	f := newFixture(t)
	patch := "*** Begin Patch\n*** Add File: src/lib.ts\n+export const answer = 42;\n+export const name = \"codex\";\n*** End Patch"
	js := "text(await tools.apply_patch(" + mustJSON(t, patch) + "));"
	lines := []map[string]any{
		{"timestamp": "2026-11-20T09:00:00Z", "type": "session_meta", "payload": map[string]any{"cwd": f.repo}},
		{"timestamp": "2026-11-20T09:00:05Z", "type": "response_item", "payload": map[string]any{
			"type": "custom_tool_call", "name": "exec", "input": js}},
		// An output echoing patch text must not count as a write.
		{"timestamp": "2026-11-20T09:00:06Z", "type": "response_item", "payload": map[string]any{
			"type": "custom_tool_call_output", "output": "*** Begin Patch\n*** Add File: src/other.ts\n+not written\n*** End Patch"}},
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(mustJSON(t, l) + "\n")
	}
	write(t, filepath.Join(f.codex, "2026", "11", "20", "rollout-1.jsonl"), b.String())
	f.store.Sync(f.src)

	write(t, filepath.Join(f.repo, "src", "lib.ts"), "export const answer = 42;\nexport const name = \"codex\";\n")
	if got := f.count(t, "src/lib.ts"); got != 2 {
		t.Fatalf("lib.ts ai lines = %d, want 2", got)
	}
	if fa := f.store.Files[Key(filepath.Join(f.repo, "src", "other.ts"))]; fa != nil {
		t.Fatal("a tool output was ingested as a write")
	}
}

func TestSyncIsIncrementalAndWaitsForWholeLines(t *testing.T) {
	f := newFixture(t)
	tr := filepath.Join(f.claude, "proj", "s1.jsonl")
	l1 := claudeTool(t, "2026-11-20T10:00:00Z", f.repo, "Write", map[string]any{"file_path": filepath.Join(f.repo, "a.go"), "content": "one line here"})
	l2 := claudeTool(t, "2026-11-20T10:05:00Z", f.repo, "Write", map[string]any{"file_path": filepath.Join(f.repo, "b.go"), "content": "another line"})
	write(t, tr, l1+l2[:len(l2)/2]) // the second line is still being written
	f.store.Sync(f.src)
	writes := func() int {
		n := 0
		for _, e := range f.store.Events {
			if e.Kind == KindWrite {
				n++
			}
		}
		return n
	}
	if writes() != 1 {
		t.Fatalf("writes after partial sync = %d, want 1", writes())
	}
	appendTo(t, tr, l2[len(l2)/2:])
	f.store.Sync(f.src)
	f.store.Sync(f.src) // nothing new: must not re-ingest
	if writes() != 2 {
		t.Fatalf("writes after completing the line = %d, want 2", writes())
	}

	// A reload resumes from the saved cursor.
	if err := f.store.Save(); err != nil {
		t.Fatal(err)
	}
	again := Load(f.store.path)
	again.Sync(f.src)
	if len(again.Events) != len(f.store.Events) {
		t.Fatalf("reload re-ingested: %d events, want %d", len(again.Events), len(f.store.Events))
	}
}

func TestReportScopesToProjectAndMarks(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(filepath.Dir(f.repo), "repo-other")
	write(t, filepath.Join(f.claude, "proj", "s1.jsonl"),
		claudeTool(t, "2026-11-20T10:00:10Z", f.repo, "Write", map[string]any{"file_path": filepath.Join(f.repo, "Main.go"), "content": "line a\nline b"})+
			claudeTool(t, "2026-11-20T10:03:00Z", other, "Write", map[string]any{"file_path": filepath.Join(other, "z.go"), "content": "elsewhere"}))
	f.store.Sync(f.src)

	var gotRel string
	hash := func(rel string) string { gotRel = rel; return "h:" + rel }
	ai := f.store.Report(f.repo, &Observer{Store: f.store}, hash, false)
	if ai == nil || len(ai.Writes) != 1 || ai.Writes[0].Lines != 2 || ai.Tools[ToolClaudeCode] != 2 {
		t.Fatalf("report = %+v", ai)
	}
	if gotRel != "Main.go" {
		t.Fatalf("path hashed as %q, want the original-case relative path", gotRel)
	}
	if len(ai.Minutes) != 1 || ai.Minutes[0]%60 != 0 {
		t.Fatalf("minutes = %v, want the one minute of the session in this repo", ai.Minutes)
	}

	// Unmarked (`once`), the same events come back; marked, they don't.
	if again := f.store.Report(f.repo, &Observer{Store: f.store}, hash, true); again == nil || len(again.Writes) != 1 {
		t.Fatal("an unmarked report consumed its events")
	}
	if after := f.store.Report(f.repo, &Observer{Store: f.store}, hash, true); after != nil {
		t.Fatalf("events reported twice: %+v", after)
	}
}

func TestHeredocWrites(t *testing.T) {
	cwd := filepath.FromSlash("/work")
	got := heredocWrites("cd x && cat >> out/log.txt <<-END\n\tindented\nEND\ntee -a 'b c.txt' <<EOF\nhello\nEOF\ncat > $F <<EOF\nskip\nEOF\n", cwd)
	if got[filepath.Join(cwd, "out", "log.txt")] != "indented\n" {
		t.Fatalf("<<- body: %q", got)
	}
	if got[filepath.Join(cwd, "b c.txt")] != "hello\n" {
		t.Fatalf("tee target: %q", got)
	}
	if len(got) != 2 {
		t.Fatalf("a $VAR target was resolved: %q", got)
	}
}

func TestObserverFillsFile(t *testing.T) {
	f := newFixture(t)
	p := filepath.Join(f.repo, "m.go")
	write(t, filepath.Join(f.claude, "proj", "s1.jsonl"),
		claudeTool(t, "2026-11-20T10:00:00Z", f.repo, "Write", map[string]any{"file_path": p, "content": aiCode}))
	f.store.Sync(f.src)
	write(t, p, aiCode)
	o := &Observer{Store: f.store}
	file := &snapshot.File{ContentHash: "c1"}
	o.Observe(p, file)
	if file.AILines != 3 || o.Lines != 3 || o.Files != 1 {
		t.Fatalf("file %d, totals %d/%d", file.AILines, o.Lines, o.Files)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
