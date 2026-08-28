package discover

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// repo makes a directory look like a git checkout.
func repo(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func names(ps []Project) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

func TestFindsReposAtDepth(t *testing.T) {
	root := t.TempDir()
	repo(t, root, "alpha")
	repo(t, root, "beta")
	repo(t, root, "nested", "gamma") // depth 2

	got := names(Scan(root, 1))
	if len(got) != 2 {
		t.Fatalf("max_depth 1 should find 2 repos, got %v", got)
	}
	if got := names(Scan(root, 2)); len(got) != 3 {
		t.Fatalf("max_depth 2 should find 3 repos, got %v", got)
	}
}

// A repository is the unit, not every folder inside one. Descending into a
// checkout would report submodules as separate projects and make the scan
// crawl the entire working tree.
func TestDoesNotDescendIntoARepository(t *testing.T) {
	root := t.TempDir()
	outer := repo(t, root, "outer")
	repo(t, outer, "vendored")

	got := names(Scan(root, 5))
	if len(got) != 1 || got[0] != "outer" {
		t.Fatalf("expected only the outer repo, got %v", got)
	}
}

func TestSkipsHeavyAndHiddenFolders(t *testing.T) {
	root := t.TempDir()
	repo(t, root, "node_modules", "sneaky")
	repo(t, root, ".cache", "sneaky2")
	repo(t, root, "real")

	got := names(Scan(root, 3))
	if len(got) != 1 || got[0] != "real" {
		t.Fatalf("expected only 'real', got %v", got)
	}
}

// Two checkouts with the same folder name under different parents must not
// collide: the config rejects duplicate project names outright.
func TestDisambiguatesCollidingNames(t *testing.T) {
	root := t.TempDir()
	repo(t, root, "work", "api")
	repo(t, root, "personal", "api")

	got := names(ScanAll([]string{root}, 2, nil))
	if len(got) != 2 {
		t.Fatalf("expected 2 distinct projects, got %v", got)
	}
	if got[0] == got[1] {
		t.Fatalf("names collided: %v", got)
	}
}

// Names already used by manual [[project]] entries are off limits.
func TestRespectsAlreadyTakenNames(t *testing.T) {
	root := t.TempDir()
	repo(t, root, "api")

	taken := map[string]bool{"api": true}
	got := ScanAll([]string{root}, 1, taken)
	if len(got) != 1 {
		t.Fatalf("expected 1 project, got %v", names(got))
	}
	if got[0].Name == "api" {
		t.Fatal("discovery reused a name already taken by a manual project")
	}
}

func TestSameRepoViaTwoRootsAppearsOnce(t *testing.T) {
	root := t.TempDir()
	repo(t, root, "only")

	got := ScanAll([]string{root, root}, 1, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 project, got %v", names(got))
	}
}

func TestUnreadableRootIsNotFatal(t *testing.T) {
	if got := Scan(filepath.Join(t.TempDir(), "absent"), 2); len(got) != 0 {
		t.Fatalf("expected no projects, got %v", names(got))
	}
}
