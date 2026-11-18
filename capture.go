package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"snapshot-agent/internal/config"
	"snapshot-agent/internal/linestate"
	"snapshot-agent/internal/snapshot"
)

// capturer holds what every capture needs beyond the walk: the path salt, and
// the per-project state that carries line counts and the snapshot chain.
type capturer struct {
	salt   []byte
	saltID string
}

func newCapturer(cfg *config.Config) (*capturer, error) {
	salt, err := pathSalt(cfg)
	if err != nil {
		return nil, err
	}
	return &capturer{salt: salt, saltID: linestate.SaltID(salt)}, nil
}

// capture snapshots one project with line counts and a chain link. save makes
// this capture the new baseline and advances the chain; `once` passes false,
// so looking never eats the daemon's diffs or skips a sequence number.
// Losing the state costs counts and chain continuity, never the snapshot.
func (c *capturer) capture(name, path string, save bool) (*snapshot.Snapshot, error) {
	opt := snapshot.Options{PathSalt: c.salt}
	var st *linestate.State
	if dir, err := linestate.Dir(); err == nil {
		if st, err = linestate.Load(dir, name, c.saltID); err == nil {
			opt.Observer = st
		} else {
			logf("project %q: line state unavailable: %v", name, err)
		}
	}
	s, err := snapshot.CaptureWith(name, path, agentVersion, opt)
	if err != nil {
		return nil, err
	}
	if st != nil {
		st.Chain(s)
		if save {
			if err := st.Save(); err != nil {
				logf("project %q: saving line state: %v", name, err)
			}
		}
	}
	return s, nil
}

// saltFile is where the generated path salt lives when the config sets none.
func saltFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".flockatime", "path-salt"), nil
}

// pathSalt is the secret path hashes are keyed with. An unsalted path hash is
// the same for everyone, so a collector (or anyone who sees the payload) could
// recover common names like src/main.rs from a dictionary. The salt never
// leaves the machine; `reveal` uses it to map a hash back to a name locally.
//
// path_salt in the config wins, so several machines can share one salt and
// their path hashes line up. Otherwise a random salt is generated once into
// ~/.flockatime/path-salt. cfg may be nil (`once <dir>` needs no config).
func pathSalt(cfg *config.Config) ([]byte, error) {
	if cfg != nil && cfg.PathSalt != "" {
		return decodeSalt(cfg.PathSalt, "path_salt in the config")
	}
	p, err := saltFile()
	if err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(p); err == nil {
		return decodeSalt(string(b), p)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	// O_EXCL: if another process won the race, use its salt, not ours.
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		return decodeSalt(string(b), p)
	}
	if err != nil {
		return nil, fmt.Errorf("creating %s: %w", p, err)
	}
	if _, err := f.WriteString(hex.EncodeToString(raw) + "\n"); err != nil {
		f.Close()
		os.Remove(p)
		return nil, err
	}
	return raw, f.Close()
}

func decodeSalt(s, where string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) < 16 {
		return nil, fmt.Errorf("%s must be at least 32 hex characters", where)
	}
	return b, nil
}
