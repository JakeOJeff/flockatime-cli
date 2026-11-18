package cadence

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"snapshot-agent/internal/snapshot"
)

var base = time.Unix(1_700_000_000, 0)

// at is base plus s seconds.
func at(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }

// touch creates or updates name under dir with its mtime at base+s.
func touch(t *testing.T, dir, name string, s int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(path, at(s), at(s)); err != nil {
		t.Fatal(err)
	}
	return path
}

// capture stands in for snapshot.Capture: walk dir, observe every file,
// finish at base+s.
func capture(t *testing.T, p *Poller, dir string, s int) []int32 {
	t.Helper()
	c := p.Begin("proj")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		c.Observe(path, &snapshot.File{ModTime: info.ModTime().Unix()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.Finish(at(s))
}

func want(t *testing.T, got []int32, w ...int32) {
	t.Helper()
	if len(w) == 0 {
		if got != nil {
			t.Fatalf("got %v, want nil", got)
		}
		return
	}
	if !reflect.DeepEqual(got, w) {
		t.Fatalf("got %v, want %v", got, w)
	}
}

func TestFirstCaptureIsNil(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "a.go", 10) // even a file saved "after" nothing
	want(t, capture(t, New(), dir, 0))
}

func TestUnchangedIsNil(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "a.go", -100)
	touch(t, dir, "b.go", -50)
	p := New()
	capture(t, p, dir, 0)
	p.Poll()
	want(t, capture(t, p, dir, 120))
	p.Poll()
	want(t, capture(t, p, dir, 240))
}

func TestPollDetectsSave(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "a.go", -100)
	p := New()
	capture(t, p, dir, 0)

	touch(t, dir, "a.go", 30)
	p.Poll()
	touch(t, dir, "a.go", 75)
	p.Poll()
	p.Poll() // nothing new: no extra entry
	want(t, capture(t, p, dir, 120), 30, 75)
}

func TestFinishDetectsSaveAfterLastPoll(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "a.go", -100)
	p := New()
	capture(t, p, dir, 0)
	p.Poll()

	touch(t, dir, "a.go", 50)
	want(t, capture(t, p, dir, 120), 50)
}

func TestNoDoubleCount(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "a.go", -100)
	p := New()
	capture(t, p, dir, 0)

	touch(t, dir, "a.go", 40)
	p.Poll()
	want(t, capture(t, p, dir, 120), 40)
	// And the same save does not leak into the next window.
	want(t, capture(t, p, dir, 240))
}

// A save that lands mid-capture: the walk saw the old mtime, the poller the
// new one before Finish. It counts in this window and not again in the next.
func TestSaveDuringCapture(t *testing.T) {
	dir := t.TempDir()
	path := touch(t, dir, "a.go", -100)
	p := New()
	capture(t, p, dir, 0)

	c := p.Begin("proj")
	c.Observe(path, &snapshot.File{ModTime: at(-100).Unix()})
	touch(t, dir, "a.go", 130)
	p.Poll()
	want(t, c.Finish(at(120)), 120) // clamped to the window

	want(t, capture(t, p, dir, 240))
}

func TestNewFileCounted(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "a.go", -100)
	p := New()
	capture(t, p, dir, 0)

	touch(t, dir, "b.go", 70)
	touch(t, dir, "old.go", -500) // copied in with an old mtime: not a save
	p.Poll()                      // the poller does not know new files
	want(t, capture(t, p, dir, 120), 70)
}

func TestDeletedFileStopsTracking(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "a.go", -100)
	b := touch(t, dir, "b.go", -100)
	p := New()
	capture(t, p, dir, 0)
	if got := p.Status(); got.Projects != 1 || got.Files != 2 {
		t.Fatalf("status %+v", got)
	}
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	p.Poll()
	s := p.Status()
	if s.Files != 1 || s.LastPoll.IsZero() {
		t.Fatalf("status %+v", s)
	}
	want(t, capture(t, p, dir, 120))
}

func TestCapKeepsLatest(t *testing.T) {
	dir := t.TempDir()
	const n = MaxSaves + 100
	p := New()
	capture(t, p, dir, 0)
	for i := 1; i <= n; i++ {
		touch(t, dir, "f"+itoa(i), i)
	}
	got := capture(t, p, dir, 10_000)
	if len(got) != MaxSaves || got[0] != 101 || got[len(got)-1] != n {
		t.Fatalf("len %d, first %d, last %d", len(got), got[0], got[len(got)-1])
	}
}

// Pending saves stay bounded however long no capture comes.
func TestPendingBounded(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "a.go", -100)
	p := New()
	capture(t, p, dir, 0)
	for i := 1; i <= 5*MaxSaves; i++ {
		touch(t, dir, "a.go", i)
		p.Poll()
		if l := len(p.projects["proj"].saves); l >= 2*MaxSaves {
			t.Fatalf("pending grew to %d", l)
		}
	}
	got := capture(t, p, dir, 10*MaxSaves)
	if len(got) != MaxSaves || got[len(got)-1] != 5*MaxSaves {
		t.Fatalf("len %d, last %d", len(got), got[len(got)-1])
	}
}

func TestConcurrentRunAndCapture(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		touch(t, dir, "f"+itoa(i), -100)
	}
	p := New()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); p.Run(ctx, time.Millisecond) }()

	total := 0
	for round := 1; round <= 30; round++ {
		touch(t, dir, "f"+itoa(round%20), round*100-50)
		total += len(capture(t, p, dir, round*100))
		_ = p.Status()
	}
	cancel()
	wg.Wait()
	// The first capture only seeds, so 29 rounds report one save each,
	// whichever of the poller and the walk saw it.
	if total != 29 {
		t.Fatalf("total saves %d, want 29", total)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
