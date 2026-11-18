// Package cadence records when files were saved between two captures.
//
// A capture every couple of minutes says what changed but hides the rhythm
// of the work. Between captures a Poller stats the files the last capture
// found and notes each time a file's mtime moves; the next snapshot carries
// those moments as offsets from the previous capture. It only ever calls
// os.Stat: no file is opened, and absolute paths stay in memory here ---
// they are never logged, serialized, or returned.
package cadence

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sort"
	"sync"
	"time"

	"snapshot-agent/internal/snapshot"
)

// MaxSaves caps the offsets one snapshot carries. When there are more, the
// latest MaxSaves are kept: the end of a window is closest to the tree the
// snapshot describes. Pending saves are held to the same order of magnitude
// between captures, so a project left uncaptured for days stays bounded.
const MaxSaves = 500

// Poller watches the files of every project between captures. Safe for
// concurrent use: Run polls on its own goroutine while captures happen on
// another.
type Poller struct {
	mu       sync.Mutex
	projects map[string]*tracked
	lastPoll time.Time
	lastDur  time.Duration
}

// tracked is one project's state between two captures.
type tracked struct {
	ref   int64            // previous capture time T0, unix seconds
	files map[string]int64 // absolute path -> newest mtime seen, unix seconds
	saves []int64          // mtimes of saves seen since ref, unbounded order
}

// Status is for `doctor` and logs. It holds counts and times only.
type Status struct {
	Projects     int
	Files        int
	LastPoll     time.Time // zero until the first pass
	LastPollTook time.Duration
}

// New returns a Poller that tracks nothing until the first Finish.
func New() *Poller {
	return &Poller{projects: make(map[string]*tracked)}
}

// Run stats every tracked file every `every` until ctx is done.
func (p *Poller) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Poll()
		}
	}
}

// statJob is one file to stat in a pass, copied out so the stats run
// without holding the lock a capture's Finish may be waiting on.
type statJob struct {
	project, path string
	mtime         int64 // newest mtime known when the pass started
	gone          bool
}

// Poll does one pass now (Run calls this; tests call it directly).
//
// A file saved several times between two passes counts once: only its last
// mtime is visible. At a ~10 s poll that loses bursts, not sessions.
func (p *Poller) Poll() {
	start := time.Now()

	p.mu.Lock()
	n := 0
	for _, t := range p.projects {
		n += len(t.files)
	}
	jobs := make([]statJob, 0, n)
	for name, t := range p.projects {
		for path, m := range t.files {
			jobs = append(jobs, statJob{project: name, path: path, mtime: m})
		}
	}
	p.mu.Unlock()

	changed := jobs[:0]
	for _, j := range jobs {
		fi, err := os.Stat(j.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			j.gone = true
		case err != nil:
			continue // transient (locked, permissions): try again next pass
		default:
			m := fi.ModTime().Unix() // same truncation as the walk
			if m <= j.mtime {
				continue
			}
			j.mtime = m
		}
		changed = append(changed, j)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	// Apply against current state, not the copy: a Finish may have swapped
	// the tracked set while we were statting.
	for _, j := range changed {
		t := p.projects[j.project]
		if t == nil {
			continue
		}
		old, ok := t.files[j.path]
		if !ok {
			continue
		}
		if j.gone {
			delete(t.files, j.path)
			continue
		}
		if j.mtime <= old {
			continue
		}
		t.files[j.path] = j.mtime
		if j.mtime > t.ref {
			t.addSave(j.mtime)
		}
	}
	p.lastPoll = start
	p.lastDur = time.Since(start)
}

// addSave records one save, trimming to the latest MaxSaves once twice that
// many are pending so memory stays bounded however long a capture is away.
func (t *tracked) addSave(m int64) {
	t.saves = append(t.saves, m)
	if len(t.saves) >= 2*MaxSaves {
		sort.Slice(t.saves, func(i, j int) bool { return t.saves[i] < t.saves[j] })
		t.saves = append(t.saves[:0], t.saves[len(t.saves)-MaxSaves:]...)
	}
}

// Status reports projects tracked, files tracked, and the last poll pass.
func (p *Poller) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := Status{Projects: len(p.projects), LastPoll: p.lastPoll, LastPollTook: p.lastDur}
	for _, t := range p.projects {
		s.Files += len(t.files)
	}
	return s
}

// Collector gathers the file set of one capture. It implements
// snapshot.Observer. Observe is called from the single walking goroutine,
// so it takes no lock; only Finish touches the Poller.
type Collector struct {
	p       *Poller
	project string
	files   map[string]int64
}

// Begin starts collecting the file set of one capture of project.
func (p *Poller) Begin(project string) *Collector {
	return &Collector{p: p, project: project, files: make(map[string]int64)}
}

// Observe records one walked file: its absolute path and mtime.
func (c *Collector) Observe(absPath string, f *snapshot.File) {
	if m, ok := c.files[absPath]; !ok || f.ModTime > m {
		c.files[absPath] = f.ModTime
	}
}

// Finish ends the capture taken at capturedAt. It returns the save offsets
// for the snapshot --- seconds after the previous capture T0, ascending,
// clamped to [0, capturedAt-T0], at most MaxSaves, nil when none --- and
// makes the collected set the tracked set for the project with capturedAt
// as the new T0. The first capture of a project returns nil.
//
// Beyond what the poller saw, the walk itself contributes: a tracked file
// whose walked mtime is newer than the last one seen (saved after the last
// pass), and a file new since T0 with mtime after T0. A save both the poller
// and the walk saw has the same mtime, so it is counted once.
func (c *Collector) Finish(capturedAt time.Time) []int32 {
	p := c.p
	ref := capturedAt.Unix()
	files := c.files
	c.files = nil // a Collector is single-use

	p.mu.Lock()
	defer p.mu.Unlock()

	prev := p.projects[c.project]
	p.projects[c.project] = &tracked{ref: ref, files: files}
	if prev == nil {
		return nil
	}

	t0 := prev.ref
	saves := prev.saves
	for path, m := range files {
		old, ok := prev.files[path]
		switch {
		case !ok:
			if m > t0 {
				saves = append(saves, m)
			}
		case m > old:
			saves = append(saves, m)
		case m < old:
			// The poller saw a newer save than the walk did (it landed
			// mid-capture). Keep the newer mtime so the next window does
			// not count it again.
			files[path] = old
		}
	}

	span := ref - t0
	var out []int32
	for _, m := range saves {
		if m <= t0 {
			continue
		}
		off := m - t0
		if off > span {
			off = span
		}
		if off < 0 {
			off = 0
		}
		out = append(out, int32(off))
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) > MaxSaves {
		out = append([]int32(nil), out[len(out)-MaxSaves:]...)
	}
	return out
}
