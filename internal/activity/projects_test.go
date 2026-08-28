package activity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeLog builds a wakatime.log from heartbeat lines, in the shape
// wakatime-cli actually writes when debug logging is on.
func writeLog(t *testing.T, dir string, entries ...heartbeat) {
	t.Helper()
	var body []byte
	for _, e := range entries {
		line, err := json.Marshal(map[string]any{
			"level": "debug", "caller": "heartbeat/heartbeat.go:149",
			"file": e.File, "time": e.Time, "plugin": "vscode/1.111.0",
		})
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, line...)
		body = append(body, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "wakatime.log"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkrepo(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectComesFromTheEditedFile(t *testing.T) {
	dir := t.TempDir()
	root := mkrepo(t, dir, "myproj")
	deep := filepath.Join(root, "src", "internal")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	writeLog(t, dir, heartbeat{File: filepath.Join(deep, "main.go"), Time: float64(now.Unix())})

	got := WakaTime{Dir: dir}.RecentProjects(now.Add(-time.Hour))
	if len(got) != 1 {
		t.Fatalf("expected 1 project, got %+v", got)
	}
	if got[0].Path != root {
		t.Errorf("path = %q, want the repo root %q", got[0].Path, root)
	}
	if got[0].Name != "myproj" {
		t.Errorf("name = %q, want %q", got[0].Name, "myproj")
	}
}

// Editing ten files in one repo is one project, not ten.
func TestManyFilesInOneRepoCollapse(t *testing.T) {
	dir := t.TempDir()
	root := mkrepo(t, dir, "solo")
	now := float64(time.Now().Unix())
	var hbs []heartbeat
	for i := 0; i < 10; i++ {
		hbs = append(hbs, heartbeat{File: filepath.Join(root, fmt.Sprintf("f%d.go", i)), Time: now})
	}
	writeLog(t, dir, hbs...)

	if got := (WakaTime{Dir: dir}).RecentProjects(time.Now().Add(-time.Hour)); len(got) != 1 {
		t.Fatalf("expected 1 project, got %+v", got)
	}
}

// The whole point of the idle window: yesterday's project is not today's.
func TestOldHeartbeatsAreIgnored(t *testing.T) {
	dir := t.TempDir()
	old := mkrepo(t, dir, "yesterday")
	fresh := mkrepo(t, dir, "today")
	now := time.Now()
	writeLog(t, dir,
		heartbeat{File: filepath.Join(old, "a.go"), Time: float64(now.Add(-24 * time.Hour).Unix())},
		heartbeat{File: filepath.Join(fresh, "b.go"), Time: float64(now.Unix())},
	)

	got := WakaTime{Dir: dir}.RecentProjects(now.Add(-10 * time.Minute))
	if len(got) != 1 || got[0].Name != "today" {
		t.Fatalf("expected only 'today', got %+v", got)
	}
}

// A file outside any checkout has no project, and must not walk up to the
// volume root and report that instead.
func TestFileOutsideARepoIsSkipped(t *testing.T) {
	dir := t.TempDir()
	loose := filepath.Join(dir, "loose")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	writeLog(t, dir, heartbeat{File: filepath.Join(loose, "notes.txt"), Time: float64(now.Unix())})

	if got := (WakaTime{Dir: dir}).RecentProjects(now.Add(-time.Hour)); len(got) != 0 {
		t.Fatalf("expected no projects, got %+v", got)
	}
}

// Without debug = true the log holds only error lines and no "file" field,
// which is the single most likely reason for an empty project list.
func TestLogWithoutHeartbeatsYieldsNothing(t *testing.T) {
	dir := t.TempDir()
	body := `{"level":"warn","now":"2026-03-22T23:31:41+05:30","message":"boom"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "wakatime.log"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (WakaTime{Dir: dir}).RecentProjects(time.Now().Add(-time.Hour)); len(got) != 0 {
		t.Fatalf("expected no projects, got %+v", got)
	}
}

func TestMissingLogIsNotFatal(t *testing.T) {
	if got := (WakaTime{Dir: t.TempDir()}).RecentProjects(time.Now()); len(got) != 0 {
		t.Fatalf("expected no projects, got %+v", got)
	}
}

// Only the tail of the log is read, so a long-lived log stays cheap. The
// recent entries at the end must still be found.
func TestReadsTailOfALargeLog(t *testing.T) {
	dir := t.TempDir()
	root := mkrepo(t, dir, "atend")
	now := time.Now()

	f, err := os.Create(filepath.Join(dir, "wakatime.log"))
	if err != nil {
		t.Fatal(err)
	}
	padding := make([]byte, 200)
	for i := range padding {
		padding[i] = 'x'
	}
	for i := 0; i < 4000; i++ { // ~800 KB, comfortably over the tail window
		fmt.Fprintf(f, `{"level":"debug","pad":"%s"}`+"\n", padding)
	}
	line, _ := json.Marshal(map[string]any{
		"level": "debug", "file": filepath.Join(root, "x.go"), "time": float64(now.Unix()),
	})
	fmt.Fprintf(f, "%s\n", line)
	f.Close()

	got := WakaTime{Dir: dir}.RecentProjects(now.Add(-time.Hour))
	if len(got) != 1 || got[0].Name != "atend" {
		t.Fatalf("expected to find the entry at the end of a large log, got %+v", got)
	}
}

// Keeping dotfiles in a repo at ~ is common. Without a guard, editing any
// loose file under home resolves to the home directory and sets the agent
// hashing everything the user owns --- so home is never a project.
func TestHomeDirectoryIsNeverAProject(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := repoRoot(filepath.Join(loose, "flake.nix"), home); got != "" {
		t.Fatalf("home was reported as a project root: %q", got)
	}
	// A real repo nested under home is still perfectly fine.
	nested := mkrepo(t, home, "code", "realproj")
	if got := repoRoot(filepath.Join(nested, "main.go"), home); got != nested {
		t.Fatalf("nested repo root = %q, want %q", got, nested)
	}
}
