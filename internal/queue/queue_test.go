package queue

import (
	"path/filepath"
	"testing"

	"snapshot-agent/internal/snapshot"
)

func open(t *testing.T) *Queue {
	t.Helper()
	q, err := Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

func snap(project string, at int64) snapshot.Snapshot {
	return snapshot.Snapshot{Project: project, CapturedAt: at, TreeHash: project}
}

func TestFlushesOldestFirst(t *testing.T) {
	q := open(t)
	for i, p := range []string{"first", "second", "third", "fourth"} {
		if err := q.Push(snap(p, int64(100+i))); err != nil {
			t.Fatal(err)
		}
	}

	got, err := q.Drain(10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "second", "third", "fourth"}
	if len(got) != len(want) {
		t.Fatalf("drained %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Project != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i].Project, want[i])
		}
	}
}

func TestDrainDoesNotRemoveUntilAck(t *testing.T) {
	q := open(t)
	if err := q.Push(snap("only", 1)); err != nil {
		t.Fatal(err)
	}

	if _, err := q.Drain(10); err != nil {
		t.Fatal(err)
	}
	// A send that never completes must leave the entry on disk.
	if n, _ := q.Len(); n != 1 {
		t.Fatalf("queue length after drain = %d, want 1", n)
	}
	if err := q.Ack(1); err != nil {
		t.Fatal(err)
	}
	if n, _ := q.Len(); n != 0 {
		t.Fatalf("queue length after ack = %d, want 0", n)
	}
}

func TestAckRemovesOnlyTheFlushedPrefix(t *testing.T) {
	q := open(t)
	for _, p := range []string{"a", "b", "c"} {
		if err := q.Push(snap(p, 1)); err != nil {
			t.Fatal(err)
		}
	}

	batch, err := q.Drain(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 {
		t.Fatalf("drained %d, want 2", len(batch))
	}
	if err := q.Ack(len(batch)); err != nil {
		t.Fatal(err)
	}

	rest, err := q.Drain(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].Project != "c" {
		t.Fatalf("after ack got %+v, want just c", rest)
	}
}

func TestCapturedAtSurvivesTheQueue(t *testing.T) {
	q := open(t)
	const taken = 1756039182
	if err := q.Push(snap("p", taken)); err != nil {
		t.Fatal(err)
	}

	got, err := q.Drain(1)
	if err != nil {
		t.Fatal(err)
	}
	// A snapshot that waited out an outage must still report when it was
	// actually captured, not when it was finally delivered.
	if got[0].CapturedAt != taken {
		t.Errorf("captured_at = %d, want %d", got[0].CapturedAt, taken)
	}
}

func TestPushDropsOldestWhenFull(t *testing.T) {
	if testing.Short() {
		t.Skip("writes MaxEntries+2 records")
	}
	q := open(t)
	for i := 0; i < MaxEntries+2; i++ {
		if err := q.Push(snap("p", int64(i))); err != nil {
			t.Fatal(err)
		}
	}

	n, err := q.Len()
	if err != nil {
		t.Fatal(err)
	}
	if n != MaxEntries {
		t.Fatalf("queue length = %d, want %d", n, MaxEntries)
	}
	head, err := q.Drain(1)
	if err != nil {
		t.Fatal(err)
	}
	// Entries 0 and 1 were pushed out by the cap.
	if head[0].CapturedAt != 2 {
		t.Errorf("oldest surviving captured_at = %d, want 2", head[0].CapturedAt)
	}
}
