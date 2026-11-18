package snapshot

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// Reveal walks root under the same rules as a capture and returns the
// project-relative paths whose (salted) path hash starts with prefix. It is
// the one place a path hash turns back into a name, and it runs only on the
// owner's machine, at the owner's request: names leave only if they choose to
// share the answer. File contents are never opened.
func Reveal(root string, salt []byte, prefix string) ([]string, error) {
	prefix = strings.ToLower(prefix)
	var (
		matchers []matcher
		out      []string
	)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			matchers = loadIgnore(matchers, root, "")
			return nil
		}
		if d.IsDir() {
			if alwaysSkip[d.Name()] || ignored(matchers, rel, true) {
				return fs.SkipDir
			}
			matchers = loadIgnore(matchers, root, rel)
			return nil
		}
		if !d.Type().IsRegular() || d.Name() == ".git" || ignored(matchers, rel, false) {
			return nil
		}
		if strings.HasPrefix(HashPath(rel, salt), prefix) {
			out = append(out, rel)
		}
		return nil
	})
	return out, err
}
