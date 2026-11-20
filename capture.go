package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"snapshot-agent/internal/aitrace"
	"snapshot-agent/internal/cadence"
	"snapshot-agent/internal/config"
	"snapshot-agent/internal/fingerprint"
	"snapshot-agent/internal/linestate"
	"snapshot-agent/internal/snapshot"
)

// capturer holds what every capture needs beyond the walk: the path salt, the
// per-project state that carries line counts and the snapshot chain, and in
// `run` the save poller.
type capturer struct {
	salt   []byte
	saltID string
	// poller, when set, watches the files of each capture until the next and
	// supplies the snapshot's save times. Only `run` has one.
	poller *cadence.Poller
	// fpFiles counts files fingerprinted since start, for doctor-style status.
	fpFiles int

	// ai is the AI trace, nil when [ai] disabled is set. aiSynced is when its
	// transcripts were last read, so several projects in one tick share a read.
	ai       *aitrace.Store
	aiSrc    aitrace.Sources
	aiSynced time.Time
}

// observers fans one walk out to several Observers, in order.
type observers []snapshot.Observer

func (o observers) Observe(absPath string, f *snapshot.File) {
	for _, x := range o {
		x.Observe(absPath, f)
	}
}

// fingerprintChanged is linestate's Changed hook: files whose content is new
// or changed get winnowed fingerprints; unchanged files never get re-read for
// them. A file that cannot be fingerprinted just goes without.
func (c *capturer) fingerprintChanged(absPath string, f *snapshot.File) {
	fp, err := fingerprint.File(absPath, fingerprint.MaxPerFile)
	if err == nil && len(fp) > 0 {
		f.Fingerprints = fp
		c.fpFiles++
	}
}

func newCapturer(cfg *config.Config) (*capturer, error) {
	salt, err := pathSalt(cfg)
	if err != nil {
		return nil, err
	}
	c := &capturer{salt: salt, saltID: linestate.SaltID(salt)}
	if cfg == nil || !cfg.AI.Disabled {
		if p, err := aitrace.Path(); err == nil {
			c.ai = aitrace.Load(p)
			c.aiSrc = aitrace.DefaultSources()
		}
	}
	return c, nil
}

// capture snapshots one project with line counts and a chain link. save makes
// this capture the new baseline and advances the chain; `once` passes false,
// so looking never eats the daemon's diffs or skips a sequence number.
// Losing the state costs counts and chain continuity, never the snapshot.
func (c *capturer) capture(name, path string, save bool) (*snapshot.Snapshot, error) {
	var (
		obs observers
		st  *linestate.State
		col *cadence.Collector
	)
	if dir, err := linestate.Dir(); err == nil {
		if st, err = linestate.Load(dir, name, c.saltID); err == nil {
			st.Changed = c.fingerprintChanged
			obs = append(obs, st)
		} else {
			logf("project %q: line state unavailable: %v", name, err)
		}
	}
	if c.poller != nil && save {
		col = c.poller.Begin(name)
		obs = append(obs, col)
	}
	var aiObs *aitrace.Observer
	if c.ai != nil {
		c.syncAI()
		aiObs = &aitrace.Observer{Store: c.ai}
		obs = append(obs, aiObs)
	}
	opt := snapshot.Options{PathSalt: c.salt}
	if len(obs) > 0 {
		opt.Observer = obs
	}
	s, err := snapshot.CaptureWith(name, path, agentVersion, opt)
	if err != nil {
		return nil, err
	}
	if col != nil {
		s.SaveTimes = col.Finish(time.Unix(s.CapturedAt, 0))
	}
	if aiObs != nil {
		s.AI = c.ai.Report(path, aiObs, func(rel string) string { return snapshot.HashPath(rel, c.salt) }, save)
		if err := c.ai.Save(); err != nil {
			logf("saving AI trace: %v", err)
		}
	}
	if n := s.FitFingerprints(snapshot.MaxSnapshotBytes); n > 0 {
		logf("project %q: dropped fingerprints of %d file(s) to keep the snapshot under %d KB",
			name, n, snapshot.MaxSnapshotBytes>>10)
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

// saveEvery is how often the cadence poller stats known files between
// captures: fine enough to show bursts and gaps inside a 2-minute interval,
// and only stat calls.
const saveEvery = 10 * time.Second

// savePoll runs the poller only while a session is active, so a dormant agent
// makes no filesystem calls at all. What it tracks survives a pause.
type savePoll struct {
	p      *cadence.Poller
	cancel context.CancelFunc
}

func (s *savePoll) start() {
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.p.Run(ctx, saveEvery)
}

func (s *savePoll) stop() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// syncAI reads what AI transcripts gained since the last read. Captures in the
// same tick share one read; the cursors are saved with the store, so a restart
// does not re-read months of transcripts.
func (c *capturer) syncAI() {
	if c.ai == nil || time.Since(c.aiSynced) < 5*time.Second {
		return
	}
	c.ai.Sync(c.aiSrc)
	c.aiSynced = time.Now()
}
