// Package aitrace learns which lines of code an AI agent wrote, and when AI
// sessions were active, by reading the transcripts AI coding tools already
// keep on disk (Claude Code, Codex). No hook or editor plugin is needed.
//
// What it keeps is local: per file, a set of hashes of the lines an agent
// wrote, and a log of AI writes and active minutes. A capture then counts how
// many lines of each file still match an AI-written line. A line a person
// later edits stops matching, so attribution follows the code, not the file.
package aitrace

import (
	"encoding/gob"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Tool names as they appear on the wire.
const (
	ToolClaudeCode = "claude-code"
	ToolCodex      = "codex"
)

// Event kinds.
const (
	KindWrite = 1 // an agent wrote Lines lines into Path
	KindTick  = 2 // an AI session was live in Dir during the minute At
)

// Event is one thing seen in a transcript. Seq orders events by when they were
// ingested, which is what "not yet reported" is measured against: a transcript
// read late still gets reported once, even if its timestamps are old.
type Event struct {
	Seq   uint64
	Kind  uint8
	At    int64 // unix seconds; for ticks, the start of the minute
	Tool  string
	Path  string // KindWrite: the file, as the transcript spelled it
	Dir   string // KindTick: the session's working directory, as spelled
	Lines int    // KindWrite: lines written
}

// FileAI is what agents wrote into one file: the hash of every non-blank line
// they produced, with the tool that produced it. Gen moves whenever lines are
// added, so a cached count against an older Gen is recomputed.
type FileAI struct {
	Lines map[uint64]string
	Gen   uint64

	// Cache of the last count: valid while the file's content hash and Gen
	// both match.
	CacheContent string
	CacheGen     uint64
	CacheCount   int
}

// Store is the whole AI trace. Load, Sync, use, Save.
type Store struct {
	Cursors  map[string]int64   // transcript path -> bytes consumed
	Files    map[string]*FileAI // file key -> AI lines
	Events   []Event
	NextSeq  uint64
	Reported map[string]uint64 // project root key -> last Seq sent

	path  string
	dirty bool
}

// Retention bounds what the event log keeps. Line attribution in Files is
// kept for good: code an agent wrote months ago is still agent-written.
const (
	keepEvents = 45 * 24 * time.Hour
	maxEvents  = 200_000
)

// Path is ~/.flockatime/state/ai.gob.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".flockatime", "state", "ai.gob"), nil
}

// Load reads the store at path. A missing or unreadable file starts empty:
// the transcripts are still on disk, so the next Sync rebuilds it.
func Load(path string) *Store {
	s := &Store{path: path}
	if f, err := os.Open(path); err == nil {
		if gob.NewDecoder(f).Decode(s) != nil {
			*s = Store{path: path, dirty: true}
		}
		f.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		s.dirty = true
	}
	s.path = path
	if s.Cursors == nil {
		s.Cursors = map[string]int64{}
	}
	if s.Files == nil {
		s.Files = map[string]*FileAI{}
	}
	if s.Reported == nil {
		s.Reported = map[string]uint64{}
	}
	return s
}

// Save writes the store if it changed, atomically and mode 0600.
func (s *Store) Save() error {
	if !s.dirty || s.path == "" {
		return nil
	}
	s.prune(time.Now())
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".ai-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := gob.NewEncoder(tmp).Encode(s); err != nil {
		tmp.Close()
		return err
	}
	_ = tmp.Chmod(0o600)
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

func (s *Store) prune(now time.Time) {
	cut := now.Add(-keepEvents).Unix()
	i := 0
	for i < len(s.Events) && (s.Events[i].At < cut || len(s.Events)-i > maxEvents) {
		i++
	}
	if i > 0 {
		s.Events = append([]Event(nil), s.Events[i:]...)
	}
}

// addLines records that tool wrote text into the file at abs.
func (s *Store) addLines(tool, abs, text string, at int64) {
	key := Key(abs)
	fa := s.Files[key]
	if fa == nil {
		fa = &FileAI{Lines: map[uint64]string{}}
		s.Files[key] = fa
	}
	n := 0
	for _, line := range strings.Split(text, "\n") {
		h, ok := LineHash(line)
		if !ok {
			continue
		}
		n++
		if _, seen := fa.Lines[h]; !seen {
			fa.Lines[h] = tool
		}
	}
	if n == 0 {
		return
	}
	fa.Gen++
	s.event(Event{Kind: KindWrite, At: at, Tool: tool, Path: filepath.Clean(abs), Lines: n})
}

// tick records that a session of tool was live in dir at unix time at. One
// event per tool, directory and minute.
func (s *Store) tick(tool, dir string, at int64, seen map[string]bool) {
	if dir == "" || at <= 0 {
		return
	}
	min := at - at%60
	k := tool + "\x00" + Key(dir) + "\x00" + strconv.FormatInt(min, 10)
	if seen[k] {
		return
	}
	seen[k] = true
	s.event(Event{Kind: KindTick, At: min, Tool: tool, Dir: filepath.Clean(dir)})
}

func (s *Store) event(e Event) {
	s.NextSeq++
	e.Seq = s.NextSeq
	s.Events = append(s.Events, e)
	s.dirty = true
}

// LastActivity is the newest AI event time, or zero if there is none.
func (s *Store) LastActivity() time.Time {
	var newest int64
	for _, e := range s.Events {
		if e.At > newest {
			newest = e.At
		}
	}
	if newest == 0 {
		return time.Time{}
	}
	if e := newest + 59; e < time.Now().Unix() { // a tick covers its whole minute
		return time.Unix(e, 0)
	}
	return time.Now()
}

// RecentDirs returns the directories AI sessions were live in or wrote
// files in since the given time, newest first.
func (s *Store) RecentDirs(since time.Time) []string {
	var out []string
	seen := map[string]bool{}
	cut := since.Unix()
	for i := len(s.Events) - 1; i >= 0; i-- {
		e := s.Events[i]
		if e.At < cut {
			continue
		}
		d := e.Dir
		if e.Kind == KindWrite {
			d = filepath.Dir(e.Path)
		}
		if k := Key(d); d != "" && !seen[k] {
			seen[k] = true
			out = append(out, d)
		}
	}
	return out
}

// Key normalizes a path so the same file matches however a transcript spelled
// it: cleaned, forward slashes, and lower-cased on case-insensitive Windows.
func Key(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// within reports whether key is root or lies under it (both keys).
func within(key, root string) bool {
	return key == root || strings.HasPrefix(key, strings.TrimSuffix(root, "/")+"/")
}
