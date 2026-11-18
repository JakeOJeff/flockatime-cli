package snapshot

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	ignore "github.com/sabhiram/go-gitignore"
)

// maxFileBytes is the per-file ceiling; larger files are skipped entirely.
const maxFileBytes = 2 << 20 // 2 MiB

// alwaysSkip are directory names skipped regardless of .gitignore. Past the
// first five, these are folders only tools write, so walking them as source
// would record generated noise.
var alwaysSkip = map[string]bool{
	".git": true, "node_modules": true, "target": true, "dist": true, "vendor": true,
	"__pycache__": true, ".pytest_cache": true, ".next": true, ".nuxt": true,
	".svelte-kit": true, ".gradle": true, ".tox": true, ".nyc_output": true,
	"htmlcov": true, ".parcel-cache": true, ".turbo": true,
}

// buildNames and testNames are skipped folders whose mtimes say the code was
// built or tested, which a real session does and a replay usually does not.
// They count only when the walk skips the folder: a build/ or bin/ that is
// neither ignored nor in alwaysSkip is ordinary source.
var (
	buildNames = map[string]bool{
		"dist": true, "target": true, "build": true, "out": true, "bin": true,
		"obj": true, ".next": true, ".nuxt": true, ".svelte-kit": true,
		".output": true, "__pycache__": true, ".gradle": true,
		".parcel-cache": true, ".turbo": true,
	}
	testNames = map[string]bool{
		".pytest_cache": true, "coverage": true, ".nyc_output": true,
		"htmlcov": true, ".tox": true, ".jest-cache": true,
	}
)

// buildPrefix catches CLion's per-profile folders (cmake-build-debug, ...).
const buildPrefix = "cmake-build-"

// coverageFile is coverage.py's data file; its mtime is test activity.
const coverageFile = ".coverage"

// Limits on the stat-only scan of a skipped build or test folder: the folder,
// its children and grandchildren, at most activityStats stats in all. Tools
// touch entries near the top on every run, so going deeper buys little.
const (
	activityDepth = 2
	activityStats = 2000
)

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
	// mtimes past this are clock-skew garbage and must not win the max.
	latest := time.Now().Add(24 * time.Hour).Unix()

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
			if name := d.Name(); alwaysSkip[name] || ignored(matchers, rel, true) {
				switch {
				case buildNames[name] || strings.HasPrefix(name, buildPrefix):
					res.buildAt = max(res.buildAt, newestMtime(path, latest))
				case testNames[name]:
					res.testAt = max(res.testAt, newestMtime(path, latest))
				}
				return fs.SkipDir
			}
			matchers = loadIgnore(matchers, root, rel)
			return nil
		}

		// Symlinks, devices, sockets: recorded by neither hash nor count.
		// A .git file is a worktree or submodule pointer, not source.
		if !d.Type().IsRegular() || d.Name() == ".git" {
			return nil
		}
		if d.Name() == coverageFile {
			if info, err := d.Info(); err == nil {
				res.testAt = max(res.testAt, sane(info.ModTime().Unix(), latest))
			}
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

// newestMtime is the newest sane mtime among dir and its entries down to
// activityDepth, from at most activityStats stats. It lists directories but
// never opens a file, and does not follow symlinks: DirEntry.IsDir is false
// for a link, so a linked folder is stat'd but not descended.
func newestMtime(dir string, latest int64) int64 {
	budget := activityStats
	var best int64
	note := func(fi fs.FileInfo) {
		budget--
		best = max(best, sane(fi.ModTime().Unix(), latest))
	}
	if fi, err := os.Lstat(dir); err == nil {
		note(fi)
	}
	var scan func(d string, depth int)
	scan = func(d string, depth int) {
		if depth > activityDepth || budget <= 0 {
			return
		}
		f, err := os.Open(d)
		if err != nil {
			return
		}
		entries, _ := f.ReadDir(budget)
		f.Close()
		for _, e := range entries {
			if budget <= 0 {
				return
			}
			if fi, err := e.Info(); err == nil {
				note(fi)
			}
			if e.IsDir() {
				scan(filepath.Join(d, e.Name()), depth+1)
			}
		}
	}
	scan(dir, 1)
	return best
}

// sane maps an mtime later than latest to 0, so skew cannot win a max.
func sane(t, latest int64) int64 {
	if t > latest {
		return 0
	}
	return t
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
