package aitrace

import (
	"bufio"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"snapshot-agent/internal/snapshot"
)

// LineHash is the hash attribution matches lines by. Surrounding whitespace
// is ignored, so CRLF endings and re-indentation still match; blank lines are
// never attributed (ok is false).
func LineHash(line string) (h uint64, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" {
		return 0, false
	}
	f := fnv.New64a()
	f.Write([]byte(t))
	return f.Sum64(), true
}

// trivial lines --- "}", ")", "end", "*/" --- are written by everyone. One only
// counts as AI when a neighbouring substantive line does, so a person's
// closing brace is not credited to an agent that once wrote one too.
func trivial(t string) bool {
	if len(t) > 4 {
		return false
	}
	for _, r := range t {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return len(t) <= 3 // "end", "fi", "nil" are as generic as a brace
		}
	}
	return true
}

// CountFile returns how many lines of the file at abs match lines an agent
// wrote into it. contentHash keys the cache, so an unchanged file is not
// re-read. Files no agent ever wrote cost a map lookup.
func (s *Store) CountFile(abs, contentHash string) int {
	fa := s.Files[Key(abs)]
	if fa == nil || len(fa.Lines) == 0 {
		return 0
	}
	if fa.CacheContent == contentHash && fa.CacheGen == fa.Gen {
		return fa.CacheCount
	}
	f, err := os.Open(abs)
	if err != nil {
		return 0
	}
	defer f.Close()

	type ln struct{ ai, triv bool }
	var lines []ln
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		h, ok := LineHash(t)
		_, hit := fa.Lines[h]
		lines = append(lines, ln{ai: ok && hit, triv: ok && trivial(t)})
	}
	n := 0
	for i, l := range lines {
		if !l.ai {
			continue
		}
		if l.triv {
			prev := i > 0 && lines[i-1].ai && !lines[i-1].triv
			next := i+1 < len(lines) && lines[i+1].ai && !lines[i+1].triv
			if !prev && !next {
				continue
			}
		}
		n++
	}
	fa.CacheContent, fa.CacheGen, fa.CacheCount = contentHash, fa.Gen, n
	s.dirty = true
	return n
}

// Observer fills File.AILines during a capture walk and tallies the project.
type Observer struct {
	Store *Store
	Lines int // AI lines across the project
	Files int // files with at least one
}

func (o *Observer) Observe(abs string, f *snapshot.File) {
	n := o.Store.CountFile(abs, f.ContentHash)
	f.AILines = n
	if n > 0 {
		o.Lines += n
		o.Files++
	}
}

// Report builds the snapshot's ai block for the project at root: the totals
// the observer counted, and the AI writes and active minutes not yet reported
// for it. hash turns a project-relative, slash-separated path into its path hash.
// mark makes those events count as reported, which `once` does not do.
func (s *Store) Report(root string, o *Observer, hash func(rel string) string, mark bool) *snapshot.AI {
	rk := Key(root)
	from := s.Reported[rk]
	ai := &snapshot.AI{Lines: o.Lines, Files: o.Files}
	minutes := map[int64]bool{}
	var last uint64
	for _, e := range s.Events {
		if e.Seq <= from {
			continue
		}
		switch e.Kind {
		case KindWrite:
			pk := Key(e.Path)
			if !within(pk, rk) || pk == rk {
				continue
			}
			// Key only cleans, re-slashes and lower-cases, so it keeps lengths:
			// cutting the root's length off the slashed original keeps the
			// file name's real case, which its path hash depends on.
			rel := filepath.ToSlash(filepath.Clean(e.Path))[len(strings.TrimSuffix(rk, "/"))+1:]
			ai.Writes = append(ai.Writes, snapshot.AIWrite{
				At: e.At, Tool: e.Tool, Lines: e.Lines, PathHash: hash(rel),
			})
			minutes[e.At-e.At%60] = true
		case KindTick:
			if !within(Key(e.Dir), rk) {
				continue
			}
			minutes[e.At] = true
		default:
			continue
		}
		if ai.Tools == nil {
			ai.Tools = map[string]int{}
		}
		if e.Kind == KindWrite {
			ai.Tools[e.Tool] += e.Lines
		} else if _, ok := ai.Tools[e.Tool]; !ok {
			ai.Tools[e.Tool] = 0
		}
		last = e.Seq
	}
	for m := range minutes {
		ai.Minutes = append(ai.Minutes, m)
	}
	sort.Slice(ai.Minutes, func(i, j int) bool { return ai.Minutes[i] < ai.Minutes[j] })
	sort.SliceStable(ai.Writes, func(i, j int) bool { return ai.Writes[i].At < ai.Writes[j].At })
	if len(ai.Minutes) > maxMinutes {
		ai.Minutes = ai.Minutes[len(ai.Minutes)-maxMinutes:]
	}
	if len(ai.Writes) > maxWrites {
		ai.Writes = ai.Writes[len(ai.Writes)-maxWrites:]
	}
	if mark && last > from {
		s.Reported[rk] = last
		s.dirty = true
	}
	if ai.Lines == 0 && len(ai.Writes) == 0 && len(ai.Minutes) == 0 {
		return nil
	}
	return ai
}

// maxWrites caps the writes one snapshot carries; the latest are kept. The
// first snapshot after reading months of transcripts would otherwise carry
// every write ever made.
const maxWrites = 500

// maxMinutes caps active minutes per snapshot: about a week of solid AI work.
const maxMinutes = 10000
