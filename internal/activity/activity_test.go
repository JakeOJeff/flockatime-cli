package activity

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// touch writes name inside dir and stamps it at the given age.
func touch(t *testing.T, dir, name string, age time.Duration) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

// fixed is a Source frozen at one moment, for testing the Active boundary
// without touching the filesystem.
type fixed time.Time

func (f fixed) LastActivity() time.Time { return time.Time(f) }
func (fixed) Describe() string          { return "fixed" }

func TestWakaTimeUsesNewestHeartbeatFile(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "wakatime-internal.cfg", 10*time.Minute)
	touch(t, dir, "offline_heartbeats.bdb", 1*time.Minute)

	got := time.Since(WakaTime{Dir: dir}.LastActivity()).Round(time.Second)
	if got > 90*time.Second {
		t.Fatalf("expected the 1m-old file to win, got activity %s ago", got)
	}
}

// An unreachable server makes wakatime-cli bank heartbeats locally instead of
// sending them. The offline queue must still count as activity, or an outage
// would look exactly like the user walking away.
func TestOfflineQueueCountsAsActivity(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "wakatime-internal.cfg", 2*time.Hour)
	touch(t, dir, "offline_heartbeats.bdb", 5*time.Second)

	if !Active(WakaTime{Dir: dir}, 10*time.Minute, time.Now()) {
		t.Fatal("offline heartbeats should keep the agent awake")
	}
}

func TestMissingFilesReportNoActivity(t *testing.T) {
	dir := t.TempDir() // exists, but holds neither heartbeat file
	if last := (WakaTime{Dir: dir}).LastActivity(); !last.IsZero() {
		t.Fatalf("expected zero time, got %v", last)
	}
	// A source that cannot tell must read as idle, so a misconfigured agent
	// stays silent rather than snapshotting forever.
	if Active(WakaTime{Dir: dir}, time.Hour, time.Now()) {
		t.Fatal("unknown activity must count as idle")
	}
}

func TestActiveBoundaryIsInclusive(t *testing.T) {
	now := time.Now()
	idleAfter := 10 * time.Minute

	for _, tc := range []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"just inside", idleAfter - time.Second, true},
		{"exactly at the edge", idleAfter, true},
		{"just past", idleAfter + time.Second, false},
	} {
		got := Active(fixed(now.Add(-tc.age)), idleAfter, now)
		if got != tc.want {
			t.Errorf("%s: age %s -> Active=%v, want %v", tc.name, tc.age, got, tc.want)
		}
	}
}

func TestAlwaysIsAlwaysActive(t *testing.T) {
	if !Active(Always{}, time.Nanosecond, time.Now()) {
		t.Fatal("Always must never go dormant")
	}
}

func TestNewRejectsUnknownSource(t *testing.T) {
	if _, err := New("editor-telepathy", t.TempDir()); err == nil {
		t.Fatal("expected an error for an unknown source")
	}
	if _, err := New("wakatime", filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("expected an error when the wakatime dir is missing")
	}
	if _, err := New("", t.TempDir()); err != nil {
		t.Fatalf("empty source should default to always: %v", err)
	}
}
