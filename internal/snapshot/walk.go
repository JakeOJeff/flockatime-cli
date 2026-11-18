package snapshot

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// maxFileBytes is the per-file ceiling; larger files are skipped entirely.
const maxFileBytes = 2 << 20 // 2 MiB

// alwaysSkip are directory names skipped regardless of .gitignore.
var alwaysSkip = map[string]bool{
	".git": true, "node_modules": true, "target": true, "dist": true, "vendor": true,
}

// matcher is one .gitignore file, remembered with the directory it governs
// (relative to the project root, slash-separated, "" for the root itself).
type matcher struct {
	base string
	gi   *ignore.GitIgnore
}

// Walk returns a record for every file under root that survives .gitignore,
// the alwaysSkip list, and the size cap. Errors on individual entries are
// skipped rather than aborting the walk: an unreadable file should not cost
// us the whole snapshot.
func Walk(root string) ([]File, error) {
	r, err := walk(root, Options{})
	return r.files, err
}

// walkResult is everything one walk learns about a tree.
type walkResult struct {
	files   []File
	skipped int   // over the size cap, or failing to stat or read
	buildAt int64 // newest mtime at the top of a build-output folder, 0 if none
	testAt  int64 // the same for test caches
}

// walk is Walk with options, reporting everything in walkResult.
func walk(root string, opt Options) (walkResult, error) {
	var (
		files    []File
		matchers []matcher
		skipped  int
		res      walkResult
	)
	obs := opt.Observer

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

		// Symlinks, devices, sockets: recorded by neither hash nor count.
		if !d.Type().IsRegular() {
			return nil
		}
		if ignored(matchers, rel, false) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() > maxFileBytes {
			skipped++
			return nil
		}

		contentHash, lines, hashErr := hashFile(path)
		if hashErr != nil {
			skipped++
			return nil
		}
		f := File{
			PathHash:    HashPath(rel, opt.PathSalt),
			ContentHash: contentHash,
			Lines:       lines,
			Bytes:       info.Size(),
			ModTime:     info.ModTime().Unix(),
		}
		if obs != nil {
			obs.Observe(path, &f)
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		return walkResult{}, err
	}
	res.files, res.skipped = files, skipped
	return res, nil
}

// loadIgnore appends the .gitignore in dir, if it has one. WalkDir descends
// parents before children, so every ancestor's rules are already loaded by the
// time a file is tested.
func loadIgnore(ms []matcher, root, dirRel string) []matcher {
	p := filepath.Join(root, filepath.FromSlash(dirRel), ".gitignore")
	if _, err := os.Stat(p); err != nil {
		return ms
	}
	gi, err := ignore.CompileIgnoreFile(p)
	if err != nil || gi == nil {
		return ms
	}
	return append(ms, matcher{base: dirRel, gi: gi})
}

// ignored tests rel against every .gitignore that governs it, each with the
// path made relative to that file's own directory --- which is how git scopes
// a nested .gitignore.
func ignored(ms []matcher, rel string, isDir bool) bool {
	for _, m := range ms {
		sub := rel
		if m.base != "" {
			if !strings.HasPrefix(rel, m.base+"/") {
				continue
			}
			sub = strings.TrimPrefix(rel, m.base+"/")
		}
		if m.gi.MatchesPath(sub) {
			return true
		}
		// "build/" style rules only match with the trailing separator.
		if isDir && m.gi.MatchesPath(sub+"/") {
			return true
		}
	}
	return false
}
