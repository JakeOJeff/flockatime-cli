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

	"snapshot-agent/internal/snapshot"
)

// entry is one file as of the last snapshot: the content hash its lines were
// taken from, and the hash of each line in order.
type entry struct {
	Content string
	Lines   []uint64
}

// State is the line memory of one project. Load it, pass it to
// snapshot.CaptureWith as the observer, then Save it.
type State struct {
	path  string
	fresh bool // no state on disk yet: this capture is the baseline, report no counts
	prev  map[string]entry
	next  map[string]entry
	dirty bool
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
// it yields a fresh state whose first capture records a baseline.
func Load(dir, project string) (*State, error) {
	s := &State{
		path: filepath.Join(dir, fileName(project)),
		prev: map[string]entry{},
		next: map[string]entry{},
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
	if err := gob.NewDecoder(f).Decode(&s.prev); err != nil {
		// A corrupt or old-format file costs one tick of counts, not the agent.
		s.prev = map[string]entry{}
		s.fresh = true
	}
	return s, nil
}

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
	if s.fresh {
		return
	}
	c := Diff(old.Lines, lines) // old.Lines is nil for a new file: all added
	f.Added, f.Removed, f.TailEdits, f.MidEdits = c.Added, c.Removed, c.Tail, c.Mid
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
	if err := gob.NewEncoder(tmp).Encode(s.next); err != nil {
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
