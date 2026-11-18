// Package linestate remembers, per project, a hash of every line of every file
// as of the previous snapshot, so the next snapshot can say how many lines were
// added and removed, and whether they landed at the end of a file or inside it.
//
// Lines are reduced to 64-bit hashes as they stream past; no line is ever held
// as text. The state file holds only those hashes, stays on this machine with
// mode 0600, and never goes on the wire --- only the resulting counts do.
package linestate

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"snapshot-agent/internal/snapshot"
)

// entry is one file as of the last snapshot: the content hash its lines were
// taken from, and the hash of each line in order.
type entry struct {
	Content string
	Lines   []uint64
}

// disk is the state file's layout.
type disk struct {
	// Seq and Tree are the head of the project's snapshot chain: the last
	// sequence number issued and the tree hash it carried.
	Seq  uint64
	Tree string
	// SaltID identifies the path salt the Files keys were hashed with. A
	// different salt renames every key, so the line memory restarts rather
	// than reporting every file as new.
	SaltID string
	Files  map[string]entry
}

// State is the line memory and snapshot chain of one project. Load it, pass
// it to snapshot.CaptureWith as the observer, Chain the snapshot, then Save.
type State struct {
	path   string
	fresh  bool // no usable line memory: this capture is the baseline, report no counts
	seq    uint64
	tree   string
	saltID string
	prev   map[string]entry
	next   map[string]entry
	dirty  bool

	// Changed, if set, is called for every file whose content is new or
	// changed since the last capture (every file, on a baseline), after its
	// line counts are filled in. It is how fingerprints are limited to files
	// that actually changed.
	Changed func(absPath string, f *snapshot.File)
}

// Dir is ~/.flockatime/state, next to the ~/.flockatime/bin the installer uses.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".flockatime", "state"), nil
}

// Load reads the state for project from dir. A missing file is not an error:
// it yields a fresh state whose first capture records a baseline and starts
// the chain at seq 1. saltID names the path salt in use (see SaltID).
func Load(dir, project, saltID string) (*State, error) {
	s := &State{
		path:   filepath.Join(dir, fileName(project)),
		saltID: saltID,
		prev:   map[string]entry{},
		next:   map[string]entry{},
	}
	f, err := os.Open(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		s.fresh = true
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var d disk
	if err := gob.NewDecoder(f).Decode(&d); err != nil {
		// A corrupt or old-format file costs one tick of counts and restarts
		// the chain, which a collector sees as a reset at seq 1 --- not the agent.
		s.fresh = true
		return s, nil
	}
	s.seq, s.tree = d.Seq, d.Tree
	if d.SaltID != saltID || d.Files == nil {
		s.fresh = true
		s.dirty = true // persist the new salt even if no file changes
		return s, nil
	}
	s.prev = d.Files
	return s, nil
}

// SaltID is a short, non-reversible name for a path salt, so a state file can
// tell that the salt changed without storing it.
func SaltID(salt []byte) string {
	if len(salt) == 0 {
		return "unsalted"
	}
	sum := sha256.Sum256(append([]byte("linestate-salt-id:"), salt...))
	return hex.EncodeToString(sum[:8])
}

// Chain stamps snap with the next sequence number and the previous snapshot's
// tree hash, and makes snap the new head. Call it once per capture that will
// be sent; a capture that is only displayed (`once`) can call it too and
// simply not Save.
func (s *State) Chain(snap *snapshot.Snapshot) {
	s.seq++
	snap.Seq = s.seq
	snap.PrevTreeHash = s.tree
	s.tree = snap.TreeHash
	s.dirty = true
}

// Head is the last sequence number issued and its tree hash.
func (s *State) Head() (seq uint64, tree string) { return s.seq, s.tree }

// Files is how many files the line memory holds.
func (s *State) Files() int { return len(s.prev) }

// Observe implements snapshot.Observer. A file whose content hash is unchanged
// is carried over without being read again; a changed or new one is re-read,
// line-hashed, and diffed against what was stored.
func (s *State) Observe(absPath string, f *snapshot.File) {
	old, known := s.prev[f.PathHash]
	if known && old.Content == f.ContentHash {
		s.next[f.PathHash] = old
		return
	}
	lines, content, err := hashLines(absPath)
	if err != nil {
		if known {
			s.next[f.PathHash] = old // try again next tick against the same baseline
		}
		return
	}
	// content can differ from f.ContentHash if the file was saved between the
	// walk and this read; storing what the lines really came from keeps the
	// next diff honest.
	s.next[f.PathHash] = entry{Content: content, Lines: lines}
	s.dirty = true
	if !s.fresh {
		c := Diff(old.Lines, lines) // old.Lines is nil for a new file: all added
		f.Added, f.Removed, f.TailEdits, f.MidEdits = c.Added, c.Removed, c.Tail, c.Mid
	}
	if s.Changed != nil {
		s.Changed(absPath, f)
	}
}

// Save writes the state if anything changed. Files not observed this capture
// (deleted, newly ignored, grown past the size cap) are forgotten.
func (s *State) Save() error {
	if !s.dirty && len(s.next) == len(s.prev) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".linestate-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	d := disk{Seq: s.seq, Tree: s.tree, SaltID: s.saltID, Files: s.next}
	if err := gob.NewEncoder(tmp).Encode(d); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil && !isWindows() {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func isWindows() bool { return filepath.Separator == '\\' }

// fileName keeps a project name usable as a file name. A name that had to be
// altered gets a short hash so "a/b" and "a_b" cannot share a file.
func fileName(project string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, project)
	if safe != project || safe == "" || strings.Trim(safe, ".") == "" {
		sum := sha256.Sum256([]byte(project))
		safe += "-" + hex.EncodeToString(sum[:4])
	}
	return safe + ".bin"
}

// hashLines streams path once, feeding each line to a 64-bit hasher and the
// whole file to SHA-256. The read buffer is the only place the bytes exist,
// and the hashers are their only consumers --- the same rule as the walk.
func hashLines(path string) (lines []uint64, contentHash string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	whole := sha256.New()
	line := fnv.New64a()
	buf := make([]byte, 32*1024)
	pending := false // bytes written to line since the last newline

	for {
		n, readErr := f.Read(buf)
		chunk := buf[:n]
		whole.Write(chunk)
		for len(chunk) > 0 {
			i := bytes.IndexByte(chunk, '\n')
			if i < 0 {
				line.Write(chunk)
				pending = true
				break
			}
			line.Write(chunk[:i])
			lines = append(lines, line.Sum64())
			line.Reset()
			pending = false
			chunk = chunk[i+1:]
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, "", readErr
		}
	}
	// A last line without a newline counts, as it does in the walk's count.
	if pending {
		lines = append(lines, line.Sum64())
	}
	return lines, hex.EncodeToString(whole.Sum(nil)), nil
}

// Info describes a project's state file for `doctor`.
type Info struct {
	Exists    bool
	Readable  bool   // decoded cleanly
	SaltMatch bool   // written with the salt now in use
	Seq       uint64 // last sequence number issued
	Files     int    // files with line memory
	Bytes     int64
	ModTime   time.Time
}

// Inspect reports on the state file for project without changing it.
func Inspect(dir, project, saltID string) (Info, error) {
	p := filepath.Join(dir, fileName(project))
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Info{}, nil
	}
	if err != nil {
		return Info{}, err
	}
	info := Info{Exists: true, Bytes: fi.Size(), ModTime: fi.ModTime()}
	f, err := os.Open(p)
	if err != nil {
		return info, err
	}
	defer f.Close()
	var d disk
	if gob.NewDecoder(f).Decode(&d) != nil {
		return info, nil
	}
	info.Readable = true
	info.SaltMatch = d.SaltID == saltID
	info.Seq, info.Files = d.Seq, len(d.Files)
	return info, nil
}
