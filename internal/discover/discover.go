// Package discover finds projects by scanning parent folders for git
// repositories, so a fresh checkout is tracked without anyone editing the
// config file.
package discover

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Project is one discovered repository.
type Project struct {
	Name string
	Path string
}

// skipDirs are never descended into while looking for repositories. They are
// the folders that make a naive scan slow rather than places projects live.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "target": true, "dist": true,
	"build": true, "AppData": true, "Library": true,
}

// Scan returns one Project per git repository at or below root, descending at
// most maxDepth levels. A repository is never descended into, so submodules
// and nested checkouts report as the outermost project only.
func Scan(root string, maxDepth int) []Project {
	var out []Project

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return // unreadable folder: skip it rather than fail the scan
		}
		for _, e := range entries {
			if e.Name() == ".git" {
				out = append(out, Project{Name: filepath.Base(dir), Path: dir})
				return
			}
		}
		if depth >= maxDepth {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || skipDirs[name] || strings.HasPrefix(name, ".") {
				continue
			}
			walk(filepath.Join(dir, name), depth+1)
		}
	}

	walk(root, 0)
	return out
}

// ScanAll scans every root and returns the union, with names made unique.
// Order is stable: roots in the order given, repositories in directory order.
// taken carries names already spoken for --- the manually configured projects
// --- and is extended with the names handed out here.
func ScanAll(roots []string, maxDepth int, taken map[string]bool) []Project {
	var out []Project
	seenPath := make(map[string]bool)
	seenName := taken
	if seenName == nil {
		seenName = make(map[string]bool)
	}

	for _, r := range roots {
		for _, p := range Scan(r, maxDepth) {
			if seenPath[p.Path] {
				continue // the same repo reachable from two roots
			}
			seenPath[p.Path] = true
			p.Name = unique(p.Name, p.Path, seenName)
			seenName[p.Name] = true
			out = append(out, p)
		}
	}
	return out
}

// unique keeps project names distinct. Two checkouts called "api" under
// different parents become "api" and "clientwork-api" rather than colliding,
// because a duplicate name would be rejected by the config as ambiguous.
func unique(name, path string, taken map[string]bool) string {
	if !taken[name] {
		return name
	}
	parent := filepath.Base(filepath.Dir(path))
	if q := parent + "-" + name; !taken[q] {
		return q
	}
	for i := 2; ; i++ {
		q := name + "-" + strconv.Itoa(i)
		if !taken[q] {
			return q
		}
	}
}
